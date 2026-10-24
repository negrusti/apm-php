// Relay native package acceptance probe. Not installed on client machines.
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/newrelic/newrelic-php-agent/daemon/internal/flatbuffersdata"
	"github.com/newrelic/newrelic-php-agent/daemon/internal/newrelic"
	"github.com/newrelic/newrelic-php-agent/daemon/internal/newrelic/collector"
	"github.com/newrelic/newrelic-php-agent/daemon/internal/newrelic/protocol"
)

const appName = "Relay package acceptance"

func main() {
	phase := flag.String("phase", "send", "send native transactions or check backend totals")
	socket := flag.String("socket", "/run/relay-php-daemon/agent.sock", "daemon socket")
	backend := flag.String("backend", "http://127.0.0.1:8088", "backend URL")
	flag.Parse()
	if *phase == "send" {
		send(*socket)
		return
	}
	if *phase != "check" {
		log.Fatal("phase must be send or check")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	read := func(path string, out any) {
		req, err := http.NewRequest("GET", *backend+path, nil)
		if err != nil {
			log.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("APM_PROBE_ADMIN"))
		resp, err := client.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Fatalf("API status %d", resp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
			log.Fatal(err)
		}
	}
	var apps []struct{ ID, Name string }
	read("/api/applications", &apps)
	id := ""
	for _, app := range apps {
		if app.Name == appName {
			id = app.ID
		}
	}
	if id == "" {
		log.Fatal("native daemon metrics never reached backend")
	}
	for _, kind := range []string{"plugin", "hook"} {
		// Explicit JSON names for underscore-separated fields.
		var raw struct {
			Rows []struct {
				Name  string  `json:"name"`
				Calls float64 `json:"calls"`
				Self  float64 `json:"self_ms"`
				Total float64 `json:"total_ms"`
			}
		}
		read("/api/dashboard?app="+url.QueryEscape(id)+"&kind="+kind, &raw)
		if len(raw.Rows) != 1 || raw.Rows[0].Calls != 2 || raw.Rows[0].Self != 20 || raw.Rows[0].Total != 40 {
			log.Fatalf("incorrect %s totals: %+v", kind, raw.Rows)
		}
	}
	fmt.Println("PASS: native socket -> modified daemon -> ingester -> plugin and hook queries")
}

func send(socket string) {
	info := &newrelic.AppInfo{License: collector.LicenseKey(os.Getenv("APM_PROBE_KEY")), Appname: appName, AgentLanguage: "php", AgentVersion: "12.12.0.41", Hostname: "package-probe", RedirectCollector: "must-not-be-used.invalid", TraceObserverHost: "must-not-be-used.invalid", TraceObserverPort: 443}
	app, err := flatbuffersdata.MarshalAppInfo(info)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := newrelic.OpenClientConnection(socket)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	writer := newrelic.MessageWriter{W: conn, Type: newrelic.MessageTypeBinary}
	deadline := time.Now().Add(30 * time.Second)
	var run string
	for time.Now().Before(deadline) {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := writer.Write(app); err != nil {
			log.Fatal(err)
		}
		msg, err := newrelic.ReadMessage(conn)
		if err != nil {
			log.Fatal(err)
		}
		root := protocol.GetRootAsMessage(msg.Bytes, 0)
		var table flatbuffers.Table
		if root.DataType() != protocol.MessageBodyAppReply || !root.Data(&table) {
			log.Fatal("invalid native registration reply")
		}
		var reply protocol.AppReply
		reply.Init(table.Bytes, table.Pos)
		if reply.Status() == protocol.AppStatusConnected {
			var session struct {
				ID string `json:"agent_run_id"`
			}
			if err := json.Unmarshal(reply.ConnectReply(), &session); err != nil {
				log.Fatal(err)
			}
			run = session.ID
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run == "" {
		log.Fatal("daemon did not connect to configured backend in 30 seconds")
	}
	var txn flatbuffersdata.Txn
	fixture := `{"Name":"WebTransaction/WordPress/package-test","Metrics":[{"Name":"Framework/WordPress/Plugin/package-test","Data":[2,0.04,0.02,0.02,0.02,0.0008],"Forced":true},{"Name":"Framework/WordPress/Hook/package_hook","Data":[2,0.04,0.02,0.02,0.02,0.0008],"Forced":true}]}`
	if err := json.Unmarshal([]byte(fixture), &txn); err != nil {
		log.Fatal(err)
	}
	txn.RunID = run
	payload, err := txn.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Native WordPress transaction fixture sent; stop daemon to flush harvest")
}
