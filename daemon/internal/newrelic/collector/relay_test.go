// Relay modifications, 2026. SPDX-License-Identifier: Apache-2.0
package collector

import (
	"compress/zlib"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayBackendValidation(t *testing.T) {
	for _, raw := range []string{"https://apm.example.com", "http://10.0.0.20:8088", "https://[::1]:443/", "https://192.0.2.20"} {
		if u, err := ParseBackend(raw); err != nil || u == nil {
			t.Fatalf("valid %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"apm.example.com", "ftp://host", "https://user:pass@host", "https://host/path", "https://host?x=1", "https://host?", "https://host#x", "https://host:0", "https://host:99999", "https://host:", "https://"} {
		if _, err := ParseBackend(raw); err == nil {
			t.Errorf("accepted invalid backend %q", raw)
		}
	}
}

func TestRelayBackendTransport(t *testing.T) {
	for _, tls := range []bool{false, true} {
		t.Run(map[bool]string{false: "http-ip", true: "https-private-ca"}[tls], func(t *testing.T) {
			var received atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/agent_listener/invoke_raw_method" || r.URL.Query().Get("license_key") != "relay-test-key" {
					t.Error("incorrect collector request")
				}
				z, err := zlib.NewReader(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				defer z.Close()
				body, _ := io.ReadAll(z)
				if string(body) != `["run",1,2,[]]` {
					t.Errorf("body: %s", body)
				}
				received.Add(1)
				io.WriteString(w, `{"return_value":null}`)
			})
			s := httptest.NewUnstartedServer(handler)
			if tls {
				s.StartTLS()
			} else {
				s.Start()
			}
			defer s.Close()
			cfg := &ClientConfig{Backend: s.URL, Timeout: time.Second * 5}
			if tls {
				cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
				cfg.CAFile = filepath.Join(t.TempDir(), "ca.pem")
				if err := os.WriteFile(cfg.CAFile, cert, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{CommandPreconnect, CommandConnect, CommandMetrics, CommandTraces, CommandErrors, CommandSlowSQLs, CommandPhpPackages} {
				cmd := &RpmCmd{Name: method, Collector: "must-never-be-contacted.invalid", License: "relay-test-key", MaxPayloadSize: 1024}
				resp := c.Execute(cmd, RpmControls{Collectible: CollectibleFunc(func(bool) ([]byte, error) { return []byte(`["run",1,2,[]]`), nil })})
				if resp.Err != nil || resp.StatusCode != 200 {
					t.Fatalf("%s: %+v", method, resp)
				}
				if !strings.HasSuffix(cmd.Collector, ".invalid") {
					t.Fatal("mutated caller's collector")
				}
			}
			if received.Load() != 7 {
				t.Fatalf("received %d", received.Load())
			}
		})
	}
}

func TestRelayDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/?license_key=secret", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c, err := NewClient(&ClientConfig{Backend: origin.URL, Timeout: time.Second * 5})
	if err != nil {
		t.Fatal(err)
	}
	resp := c.Execute(&RpmCmd{Name: CommandMetrics, License: "secret", MaxPayloadSize: 1024}, RpmControls{Collectible: CollectibleFunc(func(bool) ([]byte, error) { return []byte(`[]`), nil })})
	if resp.StatusCode != 307 || resp.Err == nil || leaked.Load() != 0 {
		t.Fatalf("redirect escaped: %+v, requests %d", resp, leaked.Load())
	}
}
