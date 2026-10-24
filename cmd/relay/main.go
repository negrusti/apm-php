package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"relayapm/internal/apm"
	"relayapm/web"
	"strings"
	"syscall"
	"time"
)

var version = "development"

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	demo := flag.Bool("demo", false, "serve labeled sample data on loopback; no ClickHouse needed")
	listen := flag.String("listen", env("APM_LISTEN", "127.0.0.1:8088"), "HTTP listen address")
	generate := flag.Bool("generate-secrets", false, "print credentials for a new native installation")
	showVersion := flag.Bool("version", false, "print Relay APM version")
	flag.Parse()
	if *showVersion {
		fmt.Println("Relay APM " + version)
		return
	}
	if *generate {
		for _, name := range []string{"APM_ADMIN_TOKEN", "INGEST_KEY", "CLICKHOUSE_PASSWORD"} {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				log.Fatal(err)
			}
			n := 64
			if name == "INGEST_KEY" {
				n = 40
			}
			fmt.Printf("%s=%s\n", name, hex.EncodeToString(b)[:n])
		}
		return
	}
	cfg := apm.Config{AdminToken: os.Getenv("APM_ADMIN_TOKEN"), CollectorHost: os.Getenv("APM_COLLECTOR_HOST"), Demo: *demo, SecureCookies: env("APM_SECURE_COOKIES", "true") == "true"}
	var store apm.Store
	if *demo {
		host, _, err := net.SplitHostPort(*listen)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			log.Fatal("sample mode requires a literal loopback listen address")
		}
		cfg.AdminToken = "local-sample-only"
		cfg.SecureCookies = false
		cfg.CollectorHost = strings.TrimPrefix(*listen, "http://")
		cfg.Clients = []apm.Client{{Key: "0123456789abcdef0123456789abcdef01234567", Tenant: "sample"}}
		store = apm.NewSampleStore(time.Now())
		log.Print("SAMPLE DATA MODE: no ClickHouse connection; loopback only")
	} else {
		if len(cfg.AdminToken) < 32 {
			log.Fatal("APM_ADMIN_TOKEN must contain at least 32 characters")
		}
		path := env("APM_CLIENTS_FILE", "clients.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Fatal("cannot read APM_CLIENTS_FILE")
		}
		if json.Unmarshal(raw, &cfg.Clients) != nil || len(cfg.Clients) == 0 {
			log.Fatal("client configuration must be a nonempty JSON array")
		}
		keys := map[string]bool{}
		for _, c := range cfg.Clients {
			if len(c.Key) != 40 || c.Tenant == "" || len(c.Tenant) > 100 || keys[c.Key] {
				log.Fatal("each client needs a unique 40-character key and a tenant")
			}
			keys[c.Key] = true
		}
		if cfg.CollectorHost != "" && (strings.Contains(cfg.CollectorHost, "://") || strings.ContainsAny(cfg.CollectorHost, "/\\ \r\n")) {
			log.Fatal("APM_COLLECTOR_HOST must be hostname[:port], without a URL scheme")
		}
		ch := &apm.ClickHouse{URL: env("CLICKHOUSE_URL", "http://127.0.0.1:8123"), Database: env("CLICKHOUSE_DATABASE", "relay_apm"), User: env("CLICKHOUSE_USER", "relay"), Password: os.Getenv("CLICKHOUSE_PASSWORD")}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = ch.Init(ctx)
		cancel()
		if err != nil {
			log.Fatalf("ClickHouse initialization failed: %v", err)
		}
		store = ch
	}
	app := apm.NewServer(cfg, store)
	server := &http.Server{Addr: *listen, Handler: app.Handler(web.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Relay APM ready at http://%s (%s)", *listen, store.Source())
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
