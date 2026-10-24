package apm

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AdminToken, CollectorHost string
	Clients                   []Client
	Demo                      bool
	SecureCookies             bool
}
type Server struct {
	Config Config
	Store  Store
	secret []byte
	slots  chan struct{}
}
type Session struct {
	Tenant   string `json:"tenant"`
	App      string `json:"app"`
	AppID    string `json:"id"`
	Host     string `json:"host"`
	ClientID string `json:"client"`
	Issued   int64  `json:"issued"`
}

func NewServer(cfg Config, store Store) *Server {
	return &Server{Config: cfg, Store: store, secret: []byte(cfg.AdminToken), slots: make(chan struct{}, 8)}
}
func (s *Server) Handler(web http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/login", s.login)
	mux.HandleFunc("/api/logout", s.logout)
	mux.HandleFunc("/api/status", s.authorize(s.status))
	mux.HandleFunc("/api/applications", s.authorize(s.applications))
	mux.HandleFunc("/api/dashboard", s.authorize(s.dashboard))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "endpoint not found") })
	mux.HandleFunc("/v1/metrics", s.nativeMetrics)
	mux.HandleFunc("/agent_listener/invoke_raw_method", s.collector)
	mux.Handle("/", web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		defer func() {
			if err := recover(); err != nil {
				log.Printf("request panic: %T", err)
				fail(w, 500, "internal error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
func reply(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (s *Server) sign(value string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) token(value string) string { return value + "." + s.sign(value) }
func (s *Server) verify(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !equal(s.sign(parts[0]), parts[1]) {
		return "", false
	}
	return parts[0], true
}
func (s *Server) authenticated(r *http.Request) bool {
	if s.Config.Demo {
		return true
	}
	if equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.Config.AdminToken) {
		return true
	}
	cookie, err := r.Cookie("relay_session")
	if err != nil {
		return false
	}
	value, ok := s.verify(cookie.Value)
	if !ok {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	return err == nil && expires > time.Now().Unix()
}
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			fail(w, 405, "GET required")
			return
		}
		if !s.authenticated(r) {
			fail(w, 401, "sign in required")
			return
		}
		next(w, r)
	}
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	if !sameOrigin(r) {
		fail(w, 403, "origin rejected")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&input) != nil || !equal(input.Token, s.Config.AdminToken) {
		fail(w, 401, "access token is incorrect")
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		fail(w, 500, "could not create session")
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: s.token(fmt.Sprint(expires.Unix()) + ":" + hex.EncodeToString(nonce)), Path: "/", HttpOnly: true, Secure: s.Config.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 8 * 3600})
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !sameOrigin(r) {
		fail(w, 403, "request rejected")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: "", Path: "/", HttpOnly: true, Secure: s.Config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.Store.Ping(ctx) != nil {
		reply(w, 503, map[string]string{"status": "unavailable"})
		return
	}
	reply(w, 200, map[string]string{"status": "ok"})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, map[string]interface{}{"source": s.Store.Source(), "retention_days": 7, "sample": s.Config.Demo, "collector_host": s.Config.CollectorHost, "now": time.Now().Unix()})
}
func (s *Server) applications(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.Applications(r.Context())
	if err != nil {
		fail(w, 503, "storage unavailable")
		return
	}
	reply(w, 200, apps)
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().Unix()
	f := Filter{App: q.Get("app"), Kind: q.Get("kind"), Name: q.Get("name"), To: now, From: now - 3600, Step: 60}
	if f.Kind == "" {
		f.Kind = "plugin"
	}
	if value := q.Get("from"); value != "" {
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			fail(w, 400, "invalid from timestamp")
			return
		}
		f.From = v
	}
	if value := q.Get("to"); value != "" {
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			fail(w, 400, "invalid to timestamp")
			return
		}
		f.To = v
	}
	duration := f.To - f.From
	if duration > 3600 {
		f.Step = 300
	}
	if duration > 6*3600 {
		f.Step = 900
	}
	if duration > 86400 {
		f.Step = 3600
	}
	if validateFilter(f) != nil || f.From < now-7*86400-60 || f.To > now+300 || len(f.Name) > 1024 || len(f.App) > 100 {
		fail(w, 400, "choose a valid range within the last seven days")
		return
	}
	points, err := s.Store.Points(r.Context(), f)
	if err != nil {
		fail(w, 503, "could not query storage; retry or select a shorter range")
		return
	}
	reply(w, 200, dashboard(f, points, s.Store.Source()))
}
func (s *Server) client(key string) (Client, bool) {
	for _, c := range s.Config.Clients {
		if equal(c.Key, key) {
			return c, true
		}
	}
	return Client{}, false
}
func (s *Server) enter(w http.ResponseWriter) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		fail(w, 503, "receiver is busy; retry later")
		return false
	}
}

const bodyLimit = 16 << 20

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	defer r.Body.Close()
	var reader io.Reader = r.Body
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "deflate":
		z, err := zlib.NewReader(reader)
		if err != nil {
			return nil, errors.New("invalid deflate body")
		}
		defer z.Close()
		reader = z
	case "gzip":
		z, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.New("invalid gzip body")
		}
		defer z.Close()
		reader = z
	default:
		return nil, errors.New("unsupported content encoding")
	}
	body, err := io.ReadAll(io.LimitReader(reader, bodyLimit+1))
	if err != nil {
		return nil, errors.New("could not read request")
	}
	if len(body) > bodyLimit {
		return nil, errors.New("decompressed body exceeds 16 MiB")
	}
	return body, nil
}
func (s *Server) nativeMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	client, ok := s.client(r.Header.Get("X-APM-Ingest-Key"))
	if !ok {
		fail(w, 401, "invalid ingestion key")
		return
	}
	if !s.enter(w) {
		return
	}
	defer func() { <-s.slots }()
	body, err := readBody(w, r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var b Batch
	if json.Unmarshal(body, &b) != nil {
		fail(w, 400, "invalid metric batch")
		return
	}
	rows, err := records(client.Tenant, b, time.Now())
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if s.Store.Write(r.Context(), rows) != nil {
		fail(w, 503, "storage unavailable; retry the same batch")
		return
	}
	reply(w, 200, map[string]interface{}{"accepted": len(rows), "batch_id": rows[0].BatchID})
}
func (s *Server) issue(c Client, app, host string) string {
	payload, _ := json.Marshal(Session{c.Tenant, app, applicationID(c.Tenant, app), host, hash(c.Key), time.Now().Unix()})
	return s.token(base64.RawURLEncoding.EncodeToString(payload))
}
func (s *Server) session(run string, c Client) (Session, bool) {
	var session Session
	if len(run) > 4096 {
		return session, false
	}
	value, ok := s.verify(run)
	if !ok {
		return session, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &session) != nil {
		return session, false
	}
	return session, session.Tenant == c.Tenant && equal(session.ClientID, hash(c.Key)) && session.Issued <= time.Now().Unix()+300 && session.Issued > time.Now().Add(-30*24*time.Hour).Unix()
}
func collectorReply(w http.ResponseWriter, v interface{}) {
	reply(w, 200, map[string]interface{}{"return_value": v})
}
func (s *Server) collector(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	q := r.URL.Query()
	client, ok := s.client(q.Get("license_key"))
	if !ok {
		fail(w, 401, "invalid ingestion key")
		return
	}
	if q.Get("protocol_version") != "17" || q.Get("marshal_format") != "json" {
		fail(w, 400, "collector protocol 17 with JSON is required")
		return
	}
	if !s.enter(w) {
		return
	}
	defer func() { <-s.slots }()
	body, err := readBody(w, r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !json.Valid(body) {
		fail(w, 400, "invalid JSON payload")
		return
	}
	switch method := q.Get("method"); method {
	case "preconnect":
		if s.Config.CollectorHost == "" {
			fail(w, 503, "collector hostname is not configured")
			return
		}
		var pre []struct {
			Token string `json:"security_policies_token"`
			High  bool   `json:"high_security"`
		}
		if json.Unmarshal(body, &pre) != nil || len(pre) != 1 {
			fail(w, 400, "invalid preconnect payload")
			return
		}
		if pre[0].Token != "" || pre[0].High {
			fail(w, 400, "security-policy negotiation is not implemented; omit policy tokens and high-security mode")
			return
		}
		collectorReply(w, map[string]interface{}{"redirect_host": s.Config.CollectorHost, "security_policies": map[string]interface{}{}})
	case "connect":
		var apps []struct {
			Names []string `json:"app_name"`
			Host  string   `json:"host"`
			High  bool     `json:"high_security"`
		}
		if json.Unmarshal(body, &apps) != nil || len(apps) != 1 || len(apps[0].Names) == 0 || strings.TrimSpace(apps[0].Names[0]) == "" || len(apps[0].Names[0]) > 200 || len(apps[0].Host) > 255 || apps[0].High {
			fail(w, 400, "invalid connect payload")
			return
		}
		app := apps[0]
		id := applicationID(client.Tenant, app.Names[0])
		accountNumber, _ := strconv.ParseUint(hash(client.Tenant)[:12], 16, 64)
		applicationNumber, _ := strconv.ParseUint(id[:12], 16, 64)
		accountID := strconv.FormatUint(accountNumber+1, 10)
		collectorReply(w, map[string]interface{}{
			"agent_run_id": s.issue(client, app.Names[0], app.Host), "apdex_t": 0.5, "entity_guid": "relay:" + id, "account_id": accountID, "primary_application_id": strconv.FormatUint(applicationNumber+1, 10), "trusted_account_key": accountID,
			"collect_traces": true, "collect_errors": true, "collect_analytics_events": false, "collect_error_events": false, "collect_span_events": false, "collect_custom_events": false,
			"sampling_target": 10, "sampling_target_period_in_seconds": 60, "max_payload_size_in_bytes": 8 << 20,
			"event_harvest_config":      map[string]interface{}{"report_period_ms": 60000, "harvest_limits": map[string]int{"analytic_event_data": 0, "custom_event_data": 0, "error_event_data": 0, "log_event_data": 0}},
			"span_event_harvest_config": map[string]int{"report_period_ms": 60000, "harvest_limit": 0}, "url_rules": []interface{}{}, "transaction_name_rules": []interface{}{}, "metric_name_rules": []interface{}{},
		})
	case "metric_data":
		run, b, e := parseCollectorMetrics(body)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		if q.Get("run_id") != "" && q.Get("run_id") != run {
			fail(w, 409, "run mismatch")
			return
		}
		session, valid := s.session(run, client)
		if !valid {
			fail(w, 409, "run expired; reconnect")
			return
		}
		b.Application = session.App
		b.Host = session.Host
		rows, e := records(client.Tenant, b, time.Now())
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		if s.Store.Write(r.Context(), rows) != nil {
			fail(w, 503, "storage unavailable; retry")
			return
		}
		collectorReply(w, nil)
	case "transaction_sample_data", "error_data", "sql_trace_data", "update_loaded_modules", "analytic_event_data", "error_event_data", "span_event_data", "custom_event_data", "log_event_data":
		var args []json.RawMessage
		var run string
		if json.Unmarshal(body, &args) != nil || len(args) < 1 || json.Unmarshal(args[0], &run) != nil {
			fail(w, 400, "payload must begin with a run ID")
			return
		}
		if q.Get("run_id") != "" && q.Get("run_id") != run {
			fail(w, 409, "run mismatch")
			return
		}
		session, valid := s.session(run, client)
		if !valid {
			fail(w, 409, "run expired; reconnect")
			return
		}
		if s.Store.Payload(r.Context(), session, method, body) != nil {
			fail(w, 503, "storage unavailable; retry")
			return
		}
		collectorReply(w, nil)
	default:
		fail(w, 400, "unsupported collector method")
	}
}
