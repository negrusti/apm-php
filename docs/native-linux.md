# Native Linux deployment

Target: one Linux server running ClickHouse, Relay APM and an HTTPS reverse proxy. No containers or Node.js runtime are required. The frontend is embedded in the Go executable. Examples assume Debian/Ubuntu and systemd.

For Ubuntu 22.04, 24.04 and 26.04, install the separate backend and daemon `.deb`
packages produced by [GitHub CI/CD](ubuntu-packages.md). The following source
installation remains an alternative; package users skip its build/install step.

## Install ClickHouse and Go

Use the [official native ClickHouse instructions](https://clickhouse.com/docs/get-started/setup/self-managed/debian-ubuntu) to configure its signed repository and install `clickhouse-server` and `clickhouse-client`. Select a supported stable/LTS release with asynchronous inserts. Install a maintained Go toolchain from [go.dev](https://go.dev/doc/install).

Keep ClickHouse on loopback. Review existing settings before adding the included configuration and confirm effective listener addresses afterward:

```bash
sudo install -m 0644 deploy/clickhouse-local.xml /etc/clickhouse-server/config.d/relay-local.xml
sudo systemctl restart clickhouse-server
```

Clients must not reach ports 8123/9000; they send telemetry through the HTTPS ingester.

## Build and install

Transfer this project to the server and run from its root. The downloaded `upstream/` source is not required at runtime.

```bash
go test ./...
mkdir -p bin
go build -trimpath -o bin/relay ./cmd/relay
sudo bash deploy/install-service.sh
./bin/relay -generate-secrets
```

The installer creates the service account and installs the binary/templates, preserving existing credentials. It does not start the service. The final command generates a dashboard token, a 40-character ingestion key and a database password. Save these into protected configuration, never source control; generate a unique key for each client.

Alternatively, the supplied `bin/relay-linux-amd64` and `bin/relay-linux-arm64` were built with Go 1.27.3 and CGO disabled. Verify them using `sha256sum -c bin/SHA256SUMS` from the project root. On x86-64 Linux, copy `bin/relay-linux-amd64` to `bin/relay`; on ARM64 use the other binary. Run `chmod +x bin/relay`, then the same installer and secret-generation command. Building on the target with a maintained toolchain remains an option.

## Provision ClickHouse

In an administrator ClickHouse session, substitute the generated database password:

```sql
CREATE DATABASE IF NOT EXISTS relay_apm;
CREATE USER IF NOT EXISTS relay
    IDENTIFIED WITH sha256_password BY 'YOUR_GENERATED_CLICKHOUSE_PASSWORD'
    HOST LOCAL;
GRANT SELECT, INSERT, CREATE TABLE ON relay_apm.* TO relay;
```

Relay checks the existing database and creates its tables. If the database is absent, it attempts creation; the restricted runtime user above expects prior administrator provisioning. Existing tables are not destructively migrated.

## Configure Relay

Edit `/etc/relay-apm/relay.env` based on `deploy/relay.env.example`:

```text
APM_LISTEN=127.0.0.1:8088
APM_ADMIN_TOKEN=<generated dashboard token>
APM_CLIENTS_FILE=/etc/relay-apm/clients.json
APM_COLLECTOR_HOST=apm.example.com
APM_SECURE_COOKIES=true
CLICKHOUSE_URL=http://127.0.0.1:8123
CLICKHOUSE_DATABASE=relay_apm
CLICKHOUSE_USER=relay
CLICKHOUSE_PASSWORD=<generated database password>
```

Edit `/etc/relay-apm/clients.json` to contain entries with unique, exactly 40-character keys:

```json
[
  { "key": "<replace with a generated 40-character key>", "tenant": "client-one" }
]
```

Same-name applications in different tenants have separate IDs. Hosts for one tenant/application aggregate together. Configuration changes require restarting Relay. Rotating the dashboard token invalidates signed sessions and runs, requiring clients to reconnect.

```bash
sudo chown root:relay-apm /etc/relay-apm/relay.env /etc/relay-apm/clients.json
sudo chmod 0640 /etc/relay-apm/relay.env /etc/relay-apm/clients.json
sudo systemctl enable --now relay-apm
curl --fail http://127.0.0.1:8088/healthz
```

## Enable HTTPS

Install Caddy through its [official native packages](https://caddyserver.com/docs/install). Point DNS at the server, configure `deploy/Caddyfile.example` with your actual hostname, then reload Caddy. `APM_COLLECTOR_HOST` must use the same hostname without a scheme/path. Relay remains on loopback.

The certificate must be trusted by the daemon. For a private CA, configure the supported agent/daemon CA settings rather than disabling certificate validation. The example omits access logging because collector URLs contain ingestion keys; redact them if you enable proxy access logs. Relay does not log request URLs/bodies.

Dashboard cookies are HttpOnly, SameSite=Strict and expire after eight hours. `APM_SECURE_COOKIES=true` is intended behind HTTPS. Only for direct local HTTP development with real ClickHouse, explicitly set it to false.

## Connect WordPress

Install the stock New Relic PHP extension and our `relay-php-daemon` package on
each client. Set the destination in `/etc/relay-php-daemon/daemon.cfg`:

```text
backend = "https://apm.example.com"
# Or, on a trusted private network: backend = "http://10.0.0.20:8088"
```

Then configure the stock PHP extension:

```ini
newrelic.appname = "My WordPress site"
newrelic.license = "YOUR_GENERATED_40_CHARACTER_INGEST_KEY"
newrelic.daemon.address = "/run/relay-php-daemon/agent.sock"
newrelic.daemon.dont_launch = 3
newrelic.framework.wordpress.hooks = true
newrelic.framework.wordpress.hooks.options = "plugin_callbacks"
newrelic.browser_monitoring.auto_instrument = false
newrelic.application_logging.forwarding.enabled = false
```

Replace the key placeholder with the exact registered key. It is our credential,
not a New Relic account license. The reviewed daemon/protocol baseline is
v12.12.0.41. Our daemon pins all collector calls to its config, ignoring PHP
collector-host settings and collector redirect hosts. See the package guide for
private CA setup and HTTP exposure. The stock PHP extension is not repackaged.

Enable/start `relay-php-daemon`, then restart PHP-FPM/Apache after configuration
changes. Generate WordPress requests. Sign into Relay with the separate
administrator token. The app appears after the first metric harvest, normally
around one minute.

Policy tokens/high-security mode are not implemented and are explicitly rejected. The normal handshake disables sampled analytics/custom/error/span/log reservoirs for this metrics-focused UI; any received auxiliary trace/error/SQL/event/module envelopes are archived for later views.

## Validate on the Linux host

Using a test administrator allowed to create databases:

```bash
APM_TEST_CLICKHOUSE_URL=http://127.0.0.1:8123 \
APM_TEST_CLICKHOUSE_USER=YOUR_TEST_ADMIN \
APM_TEST_CLICKHOUSE_PASSWORD=YOUR_TEST_PASSWORD \
go test ./internal/apm -run TestNativeClickHouseIntegration -v
```

This verifies real SQL execution, writes, queries, retry deduplication and auxiliary payload archival. It creates and prints a uniquely named database, leaving it for inspection. Then verify actual PHP traffic against known request counts/timings. Contract tests alone do not prove database or PHP-package interoperability.

For HTTP fixtures see [the API guide](api.md) and `tools/send-fixture.ps1` (Windows). Use fresh timestamps. Production never silently falls back to sample data.

## Retention and operation

Both tables have daily partitions and seven-day TTL. Expired parts are removed asynchronously, so modest overshoot is expected. Dashboard queries also exclude old metric periods. Metrics expire by harvest end time; auxiliary payloads expire by receipt time.

ClickHouse asynchronous inserts wait for storage flush before success is acknowledged. Failed storage returns 503; senders must retry. The modified daemon retains upstream selective trace-loss/retry limitations. No additional queue service is required by this implementation.

Deterministic IDs, `ReplacingMergeTree` and `FINAL` queries eliminate identical metric batch retries. They do not make arbitrary overlapping/re-aggregated windows exactly-once. Native senders must keep a retry's content/timestamps unchanged and emit non-overlapping new windows per host.

The systemd service starts with a 768 MB application memory limit; profile and adjust it for your traffic. ClickHouse resources are separate. Requests, query results and ingestion concurrency are bounded. Apply your own operational backup policy and retention to any enabled ClickHouse system log tables.

Inspect operation with `systemctl status relay-apm`, `journalctl -u relay-apm`, and `/healthz`.
