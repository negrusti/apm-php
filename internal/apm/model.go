package apm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const PluginPrefix = "Framework/WordPress/Plugin/"
const HookPrefix = "Framework/WordPress/Hook/"

type Client struct {
	Key    string `json:"key"`
	Tenant string `json:"tenant"`
}
type Application struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Tenant   string `json:"tenant"`
	LastSeen int64  `json:"last_seen"`
}
type Metric struct {
	Name   string    `json:"name"`
	Scope  string    `json:"scope,omitempty"`
	Values []float64 `json:"values"`
}
type Batch struct {
	Application string   `json:"application"`
	Host        string   `json:"host"`
	Start       int64    `json:"start"`
	End         int64    `json:"end"`
	Metrics     []Metric `json:"metrics"`
}
type Record struct {
	Tenant  string  `json:"tenant_id"`
	AppID   string  `json:"application_id"`
	App     string  `json:"application_name"`
	Host    string  `json:"host"`
	Start   int64   `json:"period_start"`
	End     int64   `json:"period_end"`
	BatchID string  `json:"batch_id"`
	Name    string  `json:"metric_name"`
	Scope   string  `json:"scope"`
	Kind    string  `json:"kind"`
	Calls   float64 `json:"calls"`
	Total   float64 `json:"total_seconds"`
	Self    float64 `json:"self_seconds"`
	Min     float64 `json:"min_seconds"`
	Max     float64 `json:"max_seconds"`
	Squares float64 `json:"sum_squares"`
}
type Point struct {
	Bucket int64   `json:"bucket"`
	Name   string  `json:"name"`
	Calls  float64 `json:"calls"`
	Total  float64 `json:"total_seconds"`
	Self   float64 `json:"self_seconds"`
	Max    float64 `json:"max_seconds"`
}
type Filter struct {
	App, Kind, Name string
	From, To, Step  int64
}
type Row struct {
	Name    string  `json:"name"`
	Calls   float64 `json:"calls"`
	TotalMS float64 `json:"total_ms"`
	SelfMS  float64 `json:"self_ms"`
	AvgMS   float64 `json:"avg_ms"`
	MaxMS   float64 `json:"max_ms"`
	Share   float64 `json:"share"`
}
type Series struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}
type Dashboard struct {
	From        int64     `json:"from"`
	To          int64     `json:"to"`
	Step        int64     `json:"step"`
	Kind        string    `json:"kind"`
	Source      string    `json:"source"`
	Calls       float64   `json:"calls"`
	TotalMS     float64   `json:"total_ms"`
	SelfMS      float64   `json:"self_ms"`
	AvgMS       float64   `json:"avg_ms"`
	CallsPerMin float64   `json:"calls_per_min"`
	Rows        []Row     `json:"rows"`
	Times       []int64   `json:"times"`
	Series      []Series  `json:"series"`
	CallRates   []float64 `json:"call_rates"`
	Note        string    `json:"note"`
}

func hash(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func applicationID(tenant, name string) string {
	return hash(fmt.Sprintf("%d:%s%s", len(tenant), tenant, name))[:24]
}

func records(tenant string, b Batch, now time.Time) ([]Record, error) {
	if strings.TrimSpace(b.Application) == "" || len(b.Application) > 200 || len(b.Host) > 255 {
		return nil, errors.New("application is required (maximum 200 characters); host maximum is 255")
	}
	if b.Start <= 0 || b.End <= b.Start || b.End-b.Start > 86400 || b.End > now.Unix()+300 || b.End < now.Add(-7*24*time.Hour).Unix() {
		return nil, errors.New("invalid harvest interval; end must be within the seven-day retention window")
	}
	if len(b.Metrics) == 0 || len(b.Metrics) > 20000 {
		return nil, errors.New("provide between 1 and 20000 metrics")
	}
	// Aggregate duplicate names/scopes within a batch before assigning a deterministic ID.
	merged := map[string]Metric{}
	for _, m := range b.Metrics {
		if m.Name == "" || len(m.Name) > 1024 || len(m.Scope) > 1024 || len(m.Values) != 6 {
			return nil, errors.New("each metric needs a name and exactly six summary values")
		}
		for _, v := range m.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return nil, errors.New("metric values must be finite and nonnegative")
			}
		}
		if m.Values[4] < m.Values[3] {
			return nil, errors.New("metric maximum cannot be below minimum")
		}
		k := m.Name + "\x00" + m.Scope
		if old, ok := merged[k]; ok {
			old.Values[0] += m.Values[0]
			old.Values[1] += m.Values[1]
			old.Values[2] += m.Values[2]
			old.Values[3] = math.Min(old.Values[3], m.Values[3])
			old.Values[4] = math.Max(old.Values[4], m.Values[4])
			old.Values[5] += m.Values[5]
			merged[k] = old
		} else {
			m.Values = append([]float64(nil), m.Values...)
			merged[k] = m
		}
	}
	b.Metrics = make([]Metric, 0, len(merged))
	for _, m := range merged {
		b.Metrics = append(b.Metrics, m)
	}
	sort.Slice(b.Metrics, func(i, j int) bool {
		if b.Metrics[i].Name == b.Metrics[j].Name {
			return b.Metrics[i].Scope < b.Metrics[j].Scope
		}
		return b.Metrics[i].Name < b.Metrics[j].Name
	})
	encoded, err := json.Marshal(struct {
		Tenant string
		Batch  Batch
	}{tenant, b})
	if err != nil {
		return nil, err
	}
	id := hash(string(encoded))
	out := make([]Record, 0, len(b.Metrics))
	for _, m := range b.Metrics {
		kind := "other"
		if strings.HasPrefix(m.Name, PluginPrefix) {
			kind = "plugin"
		}
		if strings.HasPrefix(m.Name, HookPrefix) {
			kind = "hook"
		}
		out = append(out, Record{tenant, applicationID(tenant, b.Application), b.Application, b.Host, b.Start, b.End, id, m.Name, m.Scope, kind, m.Values[0], m.Values[1], m.Values[2], m.Values[3], m.Values[4], m.Values[5]})
	}
	return out, nil
}

func parseCollectorMetrics(body []byte) (string, Batch, error) {
	var args []json.RawMessage
	var b Batch
	var run string
	if json.Unmarshal(body, &args) != nil || len(args) != 4 {
		return run, b, errors.New("metric_data requires [run_id, start, end, metrics]")
	}
	if json.Unmarshal(args[0], &run) != nil || json.Unmarshal(args[1], &b.Start) != nil || json.Unmarshal(args[2], &b.End) != nil {
		return run, b, errors.New("invalid run or timestamps")
	}
	var pairs [][]json.RawMessage
	if json.Unmarshal(args[3], &pairs) != nil {
		return run, b, errors.New("invalid metrics array")
	}
	for _, p := range pairs {
		if len(p) != 2 {
			return run, b, errors.New("invalid metric pair")
		}
		var m Metric
		if json.Unmarshal(p[0], &m) != nil || json.Unmarshal(p[1], &m.Values) != nil {
			return run, b, errors.New("invalid metric descriptor or values")
		}
		b.Metrics = append(b.Metrics, m)
	}
	return run, b, nil
}

func dashboard(f Filter, points []Point, source string) Dashboard {
	d := Dashboard{From: f.From, To: f.To, Step: f.Step, Kind: f.Kind, Source: source, Rows: []Row{}, Times: []int64{}, Series: []Series{}, CallRates: []float64{}, Note: "Plugin/theme and hook timings overlap. Share is within this metric family, not a percentage of request latency. Time buckets use harvest end times."}
	rows := map[string]*Row{}
	for _, p := range points {
		r := rows[p.Name]
		if r == nil {
			r = &Row{Name: p.Name}
			rows[p.Name] = r
		}
		r.Calls += p.Calls
		r.TotalMS += p.Total * 1000
		r.SelfMS += p.Self * 1000
		r.MaxMS = math.Max(r.MaxMS, p.Max*1000)
		d.Calls += p.Calls
		d.TotalMS += p.Total * 1000
		d.SelfMS += p.Self * 1000
	}
	for _, r := range rows {
		if r.Calls > 0 {
			r.AvgMS = r.TotalMS / r.Calls
		}
		if d.SelfMS > 0 {
			r.Share = r.SelfMS / d.SelfMS * 100
		}
		d.Rows = append(d.Rows, *r)
	}
	sort.Slice(d.Rows, func(i, j int) bool {
		if d.Rows[i].SelfMS == d.Rows[j].SelfMS {
			return d.Rows[i].Name < d.Rows[j].Name
		}
		return d.Rows[i].SelfMS > d.Rows[j].SelfMS
	})
	if d.Calls > 0 {
		d.AvgMS = d.TotalMS / d.Calls
	}
	d.CallsPerMin = d.Calls / (float64(f.To-f.From) / 60)
	first := f.From / f.Step * f.Step
	size := int((f.To-1-first)/f.Step + 1)
	top := map[string]int{}
	for i, r := range d.Rows {
		if i >= 5 {
			break
		}
		top[r.Name] = len(d.Series)
		d.Series = append(d.Series, Series{r.Name, make([]float64, size)})
	}
	if len(d.Rows) > 5 {
		d.Series = append(d.Series, Series{"Other", make([]float64, size)})
	}
	d.CallRates = make([]float64, size)
	for i := 0; i < size; i++ {
		d.Times = append(d.Times, first+int64(i)*f.Step)
	}
	for _, p := range points {
		i := int((p.Bucket - first) / f.Step)
		if i < 0 || i >= size {
			continue
		}
		index, ok := top[p.Name]
		if !ok {
			index = len(d.Series) - 1
		}
		if index < 0 {
			continue
		}
		start := max64(first+int64(i)*f.Step, f.From)
		end := min64(first+int64(i+1)*f.Step, f.To)
		mins := float64(end-start) / 60
		d.Series[index].Values[i] += p.Self * 1000 / mins
		d.CallRates[i] += p.Calls / mins
	}
	return d
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func validateFilter(f Filter) error {
	if f.To <= f.From || f.To-f.From > 7*86400 || f.Step < 60 || f.Step > 3600 {
		return fmt.Errorf("invalid time range")
	}
	if f.Kind != "plugin" && f.Kind != "hook" {
		return errors.New("kind must be plugin or hook")
	}
	return nil
}
