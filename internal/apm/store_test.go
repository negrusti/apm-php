package apm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickHouseHTTPContract(t *testing.T) {
	var insertSeen, querySeen bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "relay" || password != "test-password" {
			t.Error("credentials not sent through HTTP auth")
			w.WriteHeader(401)
			return
		}
		q := r.URL.Query()
		sql := q.Get("query")
		if strings.HasPrefix(sql, "INSERT INTO metrics") {
			insertSeen = true
			if q.Get("async_insert") != "1" || q.Get("wait_for_async_insert") != "1" {
				t.Error("unsafe asynchronous acknowledgement")
			}
			raw, _ := io.ReadAll(r.Body)
			var record Record
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Error(err)
			}
			if record.Tenant != "tenant-a" || record.Kind != "plugin" {
				t.Errorf("invalid inserted record: %+v", record)
			}
		} else if strings.Contains(sql, "GROUP BY bucket") {
			querySeen = true
			if !strings.Contains(sql, "FROM metrics FINAL") || !strings.Contains(sql, "scope=''") {
				t.Error("query can double count retries or scoped copies")
			}
			if q.Get("param_name") != PluginPrefix+"x' OR 1=1 --" || strings.Contains(sql, "OR 1=1") {
				t.Error("component name was not parameterized")
			}
			fmt.Fprintln(w, `{"bucket":100,"name":"woocommerce","calls":2,"total_seconds":0.4,"self_seconds":0.2,"max_seconds":0.3}`)
		}
	}))
	defer fake.Close()
	ch := &ClickHouse{URL: fake.URL, Database: "relay_apm", User: "relay", Password: "test-password"}
	if err := ch.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := fixtureBatch()
	b.Metrics = b.Metrics[:1]
	rows, err := records("tenant-a", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = ch.Write(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	points, err := ch.Points(context.Background(), Filter{From: b.Start, To: b.End + 1, Step: 60, Kind: "plugin", Name: "x' OR 1=1 --"})
	if err != nil || len(points) != 1 || points[0].Calls != 2 {
		t.Fatalf("query decode failed: %+v %v", points, err)
	}
	if !insertSeen || !querySeen {
		t.Fatal("storage path not exercised")
	}
}

// This test uses a real native ClickHouse server when explicitly configured.
// A unique database is created and retained for inspection; nothing is dropped.
func TestNativeClickHouseIntegration(t *testing.T) {
	endpoint := os.Getenv("APM_TEST_CLICKHOUSE_URL")
	if endpoint == "" {
		t.Skip("set APM_TEST_CLICKHOUSE_URL to run against native ClickHouse")
	}
	db := fmt.Sprintf("relay_test_%d", time.Now().UnixNano())
	ch := &ClickHouse{URL: endpoint, Database: db, User: os.Getenv("APM_TEST_CLICKHOUSE_USER"), Password: os.Getenv("APM_TEST_CLICKHOUSE_PASSWORD")}
	if ch.User == "" {
		ch.User = "default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := ch.Init(ctx); err != nil {
		t.Fatal(err)
	}
	b := fixtureBatch()
	rows, err := records("tenant-a", b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ch.Write(ctx, rows); err != nil {
			t.Fatal(err)
		}
	}
	f := Filter{From: b.Start - 60, To: b.End + 60, Step: 60, Kind: "plugin", App: rows[0].AppID}
	points, err := ch.Points(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	d := dashboard(f, points, "clickhouse")
	if d.Calls != 10 || len(d.Rows) != 2 {
		t.Fatalf("retry deduplication or scoped filtering failed: %+v", d)
	}
	apps, err := ch.Applications(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("application query failed: %+v %v", apps, err)
	}
	session := Session{Tenant: "tenant-a", App: b.Application, AppID: rows[0].AppID, Host: b.Host}
	if err := ch.Payload(ctx, session, "error_data", []byte(`["run",[]]`)); err != nil {
		t.Fatal(err)
	}
	t.Logf("Real ClickHouse integration passed; inspection database: %s", db)
}
