"""Apply the small, reviewable Relay fork to a pristine upstream daemon copy.

The modified sources are shipped in daemon/. This script records how they differ
from upstream v12.12.0.41; it is not run when installing a package.
"""
from pathlib import Path

root = Path(__file__).resolve().parents[1] / "daemon"

def replace(path, before, after):
    target = root / path
    text = target.read_text(encoding="utf-8")
    if before not in text:
        raise RuntimeError(f"upstream context missing: {path}: {before[:70]}")
    target.write_text(text.replace(before, after, 1), encoding="utf-8", newline="\n")

replace("cmd/daemon/main.go", 'flagSet.StringVar(&cfg.Proxy, "proxy", cfg.Proxy, "")',
        'flagSet.StringVar(&cfg.Proxy, "proxy", cfg.Proxy, "")\n\tflagSet.StringVar(&cfg.Backend, "backend", cfg.Backend, "backend URL (http or https)")')
replace("cmd/daemon/main.go", "type Config struct {", 'type Config struct {\n\tBackend string `config:"backend"` // Relay destination, never selected by the PHP module')
replace("cmd/daemon/main.go", 'const usage = `Usage: newrelic-daemon [OPTIONS]', 'const usage = `Usage: relay-php-daemon [OPTIONS]\n\n   --backend <url>            Required Relay backend URL; also settable as backend in config')
replace("cmd/daemon/main.go", 'fmt.Printf("New Relic daemon version %s\\n", version.Full())', 'fmt.Printf("Relay PHP daemon %s (New Relic compatible)\\n", version.Full())')
replace("cmd/daemon/main.go", '\tif cfg.Utilization {', '\tif cfg.Backend == "" {\n\t\tfmt.Fprintln(os.Stderr, "backend is required in daemon config (http://IP:port or https://hostname)")\n\t\tos.Exit(1)\n\t}\n\n\tif cfg.Utilization {')
replace("cmd/daemon/worker.go", '\t\tCAFile: cfg.CAFile,', '\t\tBackend: cfg.Backend,\n\t\tCAFile: cfg.CAFile,')
replace("cmd/daemon/worker.go", '\t\tClient:            client,', '\t\tClient:            client,\n\t\tDisableInfiniteTracing: true,')
replace("internal/newrelic/client.go", 'type ClientConfig struct {', 'type ClientConfig struct {\n\tBackend string')
replace("internal/newrelic/client.go", '\t\tCAFile:      cfg.CAFile,', '\t\tBackend: cfg.Backend,\n\t\tCAFile:      cfg.CAFile,')
replace("internal/newrelic/collector/collector.go", '\tu.Scheme = "https"', '\tu.Scheme = "https"\n\tif cmd.Scheme != "" {\n\t\tu.Scheme = cmd.Scheme\n\t}')
replace("internal/newrelic/collector/client.go", 'type RpmCmd struct {', 'type RpmCmd struct {\n\tScheme string // Set by the Relay client, never by agent messages')
replace("internal/newrelic/collector/client.go", 'type ClientConfig struct {', 'type ClientConfig struct {\n\tBackend string // Relay backend; empty is retained only for upstream library tests')
replace("internal/newrelic/collector/client.go", '\t// Use defaults from the http package.', '\tbackend, err := ParseBackend(cfg.Backend)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\n\t// Use defaults from the http package.')
replace("internal/newrelic/collector/client.go", '\tc := &clientImpl{', '\tc := &clientImpl{\n\t\tbackend: backend,')
replace("internal/newrelic/collector/client.go", '\t\t\tTransport: transport,', '\t\t\tTransport: transport,\n\t\t\t// Collector redirects must never forward credentials to another destination.\n\t\t\tCheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },')
replace("internal/newrelic/collector/client.go", 'type clientImpl struct {', 'type clientImpl struct {\n\tbackend *url.URL')
replace("internal/newrelic/collector/client.go", '\t// Create the JSON payload', '\tif c.backend != nil {\n\t\t// Copy before overriding: callers may reuse their command across harvests.\n\t\tcopy := *cmd\n\t\tcopy.Collector = c.backend.Host\n\t\tcopy.Scheme = c.backend.Scheme\n\t\tcmd = &copy\n\t}\n\t// Create the JSON payload')
replace("internal/newrelic/processor.go", 'type ProcessorConfig struct {', 'type ProcessorConfig struct {\n\tDisableInfiniteTracing bool')
replace("internal/newrelic/processor.go", '\tresultChan := make(chan AppInfoReply, 1)', '\tif p.cfg.DisableInfiniteTracing {\n\t\tcopy := *info\n\t\tcopy.TraceObserverHost = ""\n\t\tcopy.TraceObserverPort = 0\n\t\tinfo = &copy\n\t}\n\tresultChan := make(chan AppInfoReply, 1)')
replace("internal/newrelic/crossagent/crossagent.go", '../../../axiom/tests/cross_agent_tests', '../../test-data/cross-agent')
