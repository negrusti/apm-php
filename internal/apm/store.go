package apm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store interface {
	Write(context.Context, []Record) error
	Payload(context.Context, Session, string, []byte) error
	Applications(context.Context) ([]Application, error)
	Points(context.Context, Filter) ([]Point, error)
	Ping(context.Context) error
	Source() string
}
type ClickHouse struct {
	URL, Database, User, Password string
	HTTP                          *http.Client
}

func (c *ClickHouse) Source() string { return "clickhouse" }
func (c *ClickHouse) execute(ctx context.Context, query string, params map[string]string, data []byte, defaultDB bool) ([]byte, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("invalid ClickHouse URL")
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("output_format_json_quote_64bit_integers", "0")
	q.Set("max_execution_time", "15")
	q.Set("max_memory_usage", "268435456")
	if !defaultDB {
		q.Set("database", c.Database)
	}
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.User, c.Password)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("ClickHouse connection failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 32<<20 {
		return nil, errors.New("ClickHouse result exceeds limit")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ClickHouse request failed (HTTP %d)", resp.StatusCode)
	}
	return body, nil
}
func (c *ClickHouse) Init(ctx context.Context) error {
	if !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`).MatchString(c.Database) {
		return errors.New("invalid ClickHouse database name")
	}
	// A provisioned database permits a runtime user without global CREATE DATABASE.
	if _, err := c.execute(ctx, "SELECT 1", nil, nil, false); err != nil {
		if _, err = c.execute(ctx, "CREATE DATABASE IF NOT EXISTS "+c.Database, nil, nil, true); err != nil {
			return err
		}
	}
	for _, sql := range []string{
		`CREATE TABLE IF NOT EXISTS metrics (
 tenant_id LowCardinality(String), application_id String, application_name LowCardinality(String), host LowCardinality(String),
 period_start DateTime('UTC'), period_end DateTime('UTC'), batch_id String, metric_name String, scope String, kind LowCardinality(String),
 calls Float64, total_seconds Float64, self_seconds Float64, min_seconds Float64, max_seconds Float64, sum_squares Float64
 ) ENGINE=ReplacingMergeTree PARTITION BY toDate(period_end)
 ORDER BY (application_id, kind, period_end, metric_name, scope, host, batch_id)
 TTL period_end + INTERVAL 7 DAY DELETE SETTINGS ttl_only_drop_parts=1`,
		`CREATE TABLE IF NOT EXISTS payloads (
 tenant_id LowCardinality(String), application_id String, application_name LowCardinality(String), host String,
 received_at DateTime('UTC'), method LowCardinality(String), payload_id String, body String
 ) ENGINE=ReplacingMergeTree PARTITION BY toDate(received_at)
 ORDER BY (application_id, method, payload_id)
 TTL received_at + INTERVAL 7 DAY DELETE SETTINGS ttl_only_drop_parts=1`,
	} {
		if _, err := c.execute(ctx, sql, nil, nil, false); err != nil {
			return err
		}
	}
	return nil
}
func (c *ClickHouse) Ping(ctx context.Context) error {
	_, err := c.execute(ctx, "SELECT 1", nil, nil, false)
	return err
}
func (c *ClickHouse) Write(ctx context.Context, rows []Record) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	_, err := c.execute(ctx, "INSERT INTO metrics FORMAT JSONEachRow", map[string]string{"async_insert": "1", "wait_for_async_insert": "1", "async_insert_busy_timeout_ms": "250", "async_insert_use_adaptive_busy_timeout": "0"}, b.Bytes(), false)
	return err
}
func (c *ClickHouse) Payload(ctx context.Context, s Session, method string, body []byte) error {
	row := map[string]interface{}{"tenant_id": s.Tenant, "application_id": s.AppID, "application_name": s.App, "host": s.Host, "received_at": time.Now().Unix(), "method": method, "payload_id": hash(s.Tenant + s.AppID + method + string(body)), "body": string(body)}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = c.execute(ctx, "INSERT INTO payloads FORMAT JSONEachRow", map[string]string{"async_insert": "1", "wait_for_async_insert": "1", "async_insert_busy_timeout_ms": "250", "async_insert_use_adaptive_busy_timeout": "0"}, data, false)
	return err
}
func (c *ClickHouse) Applications(ctx context.Context) ([]Application, error) {
	data, err := c.execute(ctx, `SELECT application_id AS id, any(application_name) AS name, any(tenant_id) AS tenant, toUnixTimestamp(max(period_end)) AS last_seen FROM metrics FINAL WHERE period_end >= now()-INTERVAL 7 DAY GROUP BY application_id ORDER BY name FORMAT JSONEachRow`, nil, nil, false)
	if err != nil {
		return nil, err
	}
	out := []Application{}
	err = decodeLines(data, func(line []byte) error {
		var a Application
		if e := json.Unmarshal(line, &a); e != nil {
			return e
		}
		out = append(out, a)
		return nil
	})
	return out, err
}
func (c *ClickHouse) Points(ctx context.Context, f Filter) ([]Point, error) {
	sql := `SELECT intDiv(toUnixTimestamp(period_end), {step:UInt32}) * {step:UInt32} AS bucket,
 substring(metric_name, {prefix:UInt32}) AS name, sum(calls) AS calls, sum(total_seconds) AS total_seconds, sum(self_seconds) AS self_seconds, max(max_seconds) AS max_seconds
 FROM metrics FINAL WHERE period_end >= toDateTime({from:UInt32}) AND period_end < toDateTime({to:UInt32}) AND period_end >= now()-INTERVAL 7 DAY
 AND kind={kind:String} AND scope=''`
	params := map[string]string{"param_step": fmt.Sprint(f.Step), "param_from": fmt.Sprint(f.From), "param_to": fmt.Sprint(f.To), "param_kind": f.Kind}
	prefix := PluginPrefix
	if f.Kind == "hook" {
		prefix = HookPrefix
	}
	params["param_prefix"] = fmt.Sprint(len(prefix) + 1)
	if f.App != "" && f.App != "all" {
		sql += " AND application_id={app:String}"
		params["param_app"] = f.App
	}
	if f.Name != "" {
		sql += " AND metric_name={name:String}"
		params["param_name"] = prefix + f.Name
	}
	sql += " GROUP BY bucket, metric_name ORDER BY bucket, metric_name LIMIT 200001 FORMAT JSONEachRow"
	data, err := c.execute(ctx, sql, params, nil, false)
	if err != nil {
		return nil, err
	}
	out := []Point{}
	err = decodeLines(data, func(line []byte) error {
		var p Point
		if e := json.Unmarshal(line, &p); e != nil {
			return e
		}
		out = append(out, p)
		return nil
	})
	if len(out) > 200000 {
		return nil, errors.New("too many points; choose an application or shorter range")
	}
	return out, err
}
func decodeLines(data []byte, fn func([]byte) error) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// SampleStore is an explicitly selected local preview backend, never a live fallback.
type SampleStore struct {
	mu       sync.RWMutex
	rows     []Record
	seen     map[string]bool
	payloads int
}

func NewSampleStore(now time.Time) *SampleStore {
	s := &SampleStore{seen: map[string]bool{}}
	plugins := []string{"woocommerce", "elementor", "wordpress-seo", "contact-form-7", "wp-rocket", "astra"}
	hooks := []string{"init", "wp_head", "the_content", "woocommerce_before_shop_loop", "template_redirect", "wp_footer"}
	anchor := now.Unix() / 60 * 60
	for appIndex, app := range []string{"Northstar · WordPress", "Fieldnotes · WordPress"} {
		for ts := anchor - 7*86400 + 60; ts <= anchor; {
			step := int64(60)
			if ts < anchor-86400 {
				step = 300
			}
			phase := float64(ts-anchor) / 3600
			b := Batch{Application: app, Host: fmt.Sprintf("web-%02d", appIndex+1), Start: ts - step, End: ts}
			for kind, names := range [][]string{plugins, hooks} {
				for i, name := range names {
					factor := 1 + 0.22*math.Sin(phase*2+float64(i)) + 0.12*math.Cos(phase*7)
					if appIndex == 1 {
						factor *= 0.57
					}
					calls := math.Round((34 + float64((6-i)*14)) * factor * float64(step) / 60)
					avg := float64(7-i) * 0.0027 * (1 + 0.3*math.Sin(phase+float64(i)))
					self := calls * avg * 0.65
					if kind == 1 {
						self *= 0.86
					}
					prefix := PluginPrefix
					if kind == 1 {
						prefix = HookPrefix
					}
					b.Metrics = append(b.Metrics, Metric{Name: prefix + name, Values: []float64{calls, calls * avg, self, avg * 0.15, avg * 4, calls * avg * avg}})
				}
			}
			rows, err := records("sample", b, now)
			if err == nil {
				s.rows = append(s.rows, rows...)
			}
			ts += step
		}
	}
	return s
}
func (s *SampleStore) Source() string             { return "sample" }
func (s *SampleStore) Ping(context.Context) error { return nil }
func (s *SampleStore) Write(_ context.Context, rows []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if len(rows) == 0 || s.seen[rows[0].BatchID] {
		return nil
	}
	if len(s.rows)+len(rows) > 250000 {
		return errors.New("sample store capacity reached")
	}
	s.seen[rows[0].BatchID] = true
	s.rows = append(s.rows, rows...)
	return nil
}
func (s *SampleStore) Payload(_ context.Context, _ Session, _ string, _ []byte) error {
	s.mu.Lock()
	s.payloads++
	s.mu.Unlock()
	return nil
}
func (s *SampleStore) Applications(_ context.Context) ([]Application, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]Application{}
	cut := time.Now().Add(-7 * 24 * time.Hour).Unix()
	for _, r := range s.rows {
		if r.End < cut {
			continue
		}
		a := m[r.AppID]
		if r.End > a.LastSeen {
			m[r.AppID] = Application{r.AppID, r.App, r.Tenant, r.End}
		}
	}
	out := []Application{}
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *SampleStore) Points(_ context.Context, f Filter) ([]Point, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]*Point{}
	prefix := PluginPrefix
	if f.Kind == "hook" {
		prefix = HookPrefix
	}
	for _, r := range s.rows {
		if r.End < f.From || r.End >= f.To || r.Kind != f.Kind || r.Scope != "" || (f.App != "" && f.App != "all" && f.App != r.AppID) {
			continue
		}
		name := strings.TrimPrefix(r.Name, prefix)
		if f.Name != "" && f.Name != name {
			continue
		}
		bucket := r.End / f.Step * f.Step
		key := fmt.Sprint(bucket) + "/" + name
		p := m[key]
		if p == nil {
			p = &Point{Bucket: bucket, Name: name}
			m[key] = p
		}
		p.Calls += r.Calls
		p.Total += r.Total
		p.Self += r.Self
		p.Max = math.Max(p.Max, r.Max)
	}
	out := []Point{}
	for _, p := range m {
		out = append(out, *p)
	}
	return out, nil
}
