package apm

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var testClients = []Client{{"1111111111111111111111111111111111111111", "tenant-a"}, {"2222222222222222222222222222222222222222", "tenant-b"}}

const testAdmin = "a-long-random-admin-secret-for-tests-only"

func fixtureConfig() Config {
	return Config{AdminToken: testAdmin, CollectorHost: "collector.example.test", Clients: testClients}
}
func fixtureBatch() Batch {
	end := time.Now().Unix() - 2
	return Batch{Application: "Test WordPress", Host: "web-01", Start: end - 60, End: end, Metrics: []Metric{
		{Name: PluginPrefix + "woocommerce", Values: []float64{2, 0.4, 0.2, 0.1, 0.3, 0.1}},
		{Name: PluginPrefix + "elementor", Values: []float64{8, 0.8, 0.4, 0.05, 0.2, 0.1}},
		{Name: HookPrefix + "init", Values: []float64{10, 1.2, 0.6, 0.05, 0.3, 0.2}},
		{Name: PluginPrefix + "woocommerce", Scope: "WebTransaction/Test", Values: []float64{2, 0.4, 0.2, 0.1, 0.3, 0.1}},
	}}
}
func request(h http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func collectorPath(method, key, run string) string {
	q := url.Values{"method": {method}, "license_key": {key}, "marshal_format": {"json"}, "protocol_version": {"17"}}
	if run != "" {
		q.Set("run_id", run)
	}
	return "/agent_listener/invoke_raw_method?" + q.Encode()
}
func collect(t *testing.T, h http.Handler, method, key, run string, payload interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	_, _ = z.Write(body)
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return request(h, "POST", collectorPath(method, key, run), compressed.Bytes(), map[string]string{"Content-Encoding": "deflate"})
}
func connectTest(t *testing.T, h http.Handler, key string) string {
	t.Helper()
	res := collect(t, h, "connect", key, "", []interface{}{map[string]interface{}{"app_name": []string{"Test WordPress"}, "host": "web-01"}})
	if res.Code != 200 {
		t.Fatalf("connect: %d %s", res.Code, res.Body.String())
	}
	var body struct {
		Value struct {
			Run    string `json:"agent_run_id"`
			Traces bool   `json:"collect_traces"`
		} `json:"return_value"`
	}
	if json.Unmarshal(res.Body.Bytes(), &body) != nil || body.Value.Run == "" || !body.Value.Traces {
		t.Fatal("incomplete connect reply")
	}
	return body.Value.Run
}

func TestCollectorLifecycleAndWordPressBreakdown(t *testing.T) {
	store := &SampleStore{}
	s := NewServer(fixtureConfig(), store)
	h := s.Handler(http.NotFoundHandler())
	pre := collect(t, h, "preconnect", testClients[0].Key, "", []interface{}{map[string]interface{}{"high_security": false}})
	if pre.Code != 200 || !strings.Contains(pre.Body.String(), "collector.example.test") {
		t.Fatalf("preconnect: %s", pre.Body.String())
	}
	run := connectTest(t, h, testClients[0].Key)
	b := fixtureBatch()
	pairs := []interface{}{}
	for _, m := range b.Metrics {
		pairs = append(pairs, []interface{}{map[string]string{"name": m.Name, "scope": m.Scope}, m.Values})
	}
	payload := []interface{}{run, b.Start, b.End, pairs}
	for i := 0; i < 2; i++ {
		res := collect(t, h, "metric_data", testClients[0].Key, run, payload)
		if res.Code != 200 {
			t.Fatalf("metric ingestion: %d %s", res.Code, res.Body.String())
		}
	}
	if len(store.rows) != 4 {
		t.Fatalf("identical retry duplicated records: %d", len(store.rows))
	}
	response := request(h, "GET", "/api/dashboard?kind=plugin", nil, map[string]string{"Authorization": "Bearer " + testAdmin})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var d Dashboard
	if err := json.Unmarshal(response.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Calls != 10 || math.Abs(d.AvgMS-120) > 0.0001 || math.Abs(d.SelfMS-600) > 0.0001 || len(d.Rows) != 2 {
		t.Fatalf("wrong weighted totals or scoped double counting: %+v", d)
	}
	if math.Abs(d.Rows[0].Share-66.6666667) > 0.001 {
		t.Fatalf("incorrect family share: %+v", d.Rows)
	}
	hooks := request(h, "GET", "/api/dashboard?kind=hook", nil, map[string]string{"Authorization": "Bearer " + testAdmin})
	if err := json.Unmarshal(hooks.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Calls != 10 || len(d.Rows) != 1 || d.Rows[0].Name != "init" {
		t.Fatal("hook metrics were mixed with plugin metrics")
	}
	// Authorization must be bound to the client key, not just a valid signed run.
	denied := collect(t, h, "metric_data", testClients[1].Key, run, payload)
	if denied.Code != 409 {
		t.Fatalf("cross-tenant run accepted: %d", denied.Code)
	}
	invalid := collect(t, h, "metric_data", testClients[0].Key, run+"x", []interface{}{run + "x", b.Start, b.End, pairs})
	if invalid.Code != 409 {
		t.Fatalf("tampered run accepted: %d", invalid.Code)
	}
	// Signing is stateless, so an ordinary service restart preserves valid runs.
	if _, ok := NewServer(fixtureConfig(), store).session(run, testClients[0]); !ok {
		t.Fatal("restart invalidates a valid run")
	}
}
func TestNativeEndpointAndValidation(t *testing.T) {
	store := &SampleStore{}
	h := NewServer(fixtureConfig(), store).Handler(http.NotFoundHandler())
	b := fixtureBatch()
	raw, _ := json.Marshal(b)
	bad := request(h, "POST", "/v1/metrics", raw, nil)
	if bad.Code != 401 {
		t.Fatal("missing key was accepted")
	}
	res := request(h, "POST", "/v1/metrics", raw, map[string]string{"X-APM-Ingest-Key": testClients[0].Key})
	if res.Code != 200 || len(store.rows) != 4 {
		t.Fatalf("native ingest failed: %s", res.Body.String())
	}
	b.Metrics[0].Values[1] = -1
	raw, _ = json.Marshal(b)
	bad = request(h, "POST", "/v1/metrics", raw, map[string]string{"X-APM-Ingest-Key": testClients[0].Key})
	if bad.Code != 400 || len(store.rows) != 4 {
		t.Fatal("invalid batch partially inserted")
	}
	b = fixtureBatch()
	b.End = time.Now().Add(-8 * 24 * time.Hour).Unix()
	b.Start = b.End - 60
	if _, err := records("test", b, time.Now()); err == nil {
		t.Fatal("expired data accepted")
	}
	b = fixtureBatch()
	b.Metrics[0].Values = []float64{1, 2}
	if _, err := records("test", b, time.Now()); err == nil {
		t.Fatal("invalid summary accepted")
	}
	b = fixtureBatch()
	b.Metrics[0].Values[1] = math.Inf(1)
	if _, err := records("test", b, time.Now()); err == nil {
		t.Fatal("non-finite value accepted")
	}
}
func TestLoginAndSameOrigin(t *testing.T) {
	h := NewServer(fixtureConfig(), &SampleStore{}).Handler(http.NotFoundHandler())
	if res := request(h, "GET", "/api/status", nil, nil); res.Code != 401 {
		t.Fatal("dashboard was public")
	}
	raw, _ := json.Marshal(map[string]string{"token": testAdmin})
	rejected := request(h, "POST", "/api/login", raw, map[string]string{"Origin": "https://evil.example"})
	if rejected.Code != 403 || len(rejected.Result().Cookies()) != 0 {
		t.Fatal("cross-origin login accepted")
	}
	res := request(h, "POST", "/api/login", raw, nil)
	if res.Code != 200 || len(res.Result().Cookies()) != 1 {
		t.Fatal("login failed")
	}
	cookie := res.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("weak session cookie")
	}
	authenticated := request(h, "GET", "/api/status", nil, map[string]string{"Cookie": cookie.String()})
	if authenticated.Code != 200 {
		t.Fatal("cookie did not authorize dashboard")
	}
	badRange := request(h, "GET", "/api/dashboard?from=1&to=9999999999", nil, map[string]string{"Authorization": "Bearer " + testAdmin})
	if badRange.Code != 400 {
		t.Fatal("unbounded time range accepted")
	}
}
func TestDeterministicBatchAndTenantIdentity(t *testing.T) {
	b := fixtureBatch()
	first, err := records("tenant-a", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b.Metrics[0], b.Metrics[1] = b.Metrics[1], b.Metrics[0]
	again, err := records("tenant-a", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first[0].BatchID != again[0].BatchID {
		t.Fatal("metric order changed retry identity")
	}
	other, _ := records("tenant-b", b, time.Now())
	if first[0].AppID == other[0].AppID || first[0].BatchID == other[0].BatchID {
		t.Fatal("tenant identities collide")
	}
}

type unavailableStore struct{ SampleStore }

func (s *unavailableStore) Write(context.Context, []Record) error { return fmt.Errorf("unavailable") }
func (s *unavailableStore) Payload(context.Context, Session, string, []byte) error {
	return fmt.Errorf("unavailable")
}
func TestStorageFailureIsNotAcknowledged(t *testing.T) {
	h := NewServer(fixtureConfig(), &unavailableStore{}).Handler(http.NotFoundHandler())
	b := fixtureBatch()
	raw, _ := json.Marshal(b)
	res := request(h, "POST", "/v1/metrics", raw, map[string]string{"X-APM-Ingest-Key": testClients[0].Key})
	if res.Code != 503 {
		t.Fatal("failed persistence acknowledged")
	}
	run := connectTest(t, h, testClients[0].Key)
	res = collect(t, h, "transaction_sample_data", testClients[0].Key, run, []interface{}{run, []interface{}{}})
	if res.Code != 503 {
		t.Fatal("failed trace persistence acknowledged")
	}
}
func TestDecompressionLimit(t *testing.T) {
	var b bytes.Buffer
	writer := zlib.NewWriter(&b)
	if _, err := io.Copy(writer, io.LimitReader(&zeroReader{}, bodyLimit+1)); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(b.Bytes()))
	r.Header.Set("Content-Encoding", "deflate")
	if _, err := readBody(httptest.NewRecorder(), r); err == nil {
		t.Fatal("decompression bomb accepted")
	}
}

type zeroReader struct{}

func (*zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '0'
	}
	return len(p), nil
}
