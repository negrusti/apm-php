# Relay APM — WordPress performance

A self-hosted ClickHouse backend, Go ingester and web dashboard for the stock New Relic PHP agent. Built for a few active clients and approximately seven days of retention. **No Docker is required.**

## Local preview

```powershell
go run ./cmd/relay -demo
```

Open [http://127.0.0.1:8088](http://127.0.0.1:8088). This explicitly selected mode uses labeled, illustrative in-memory data and only binds to loopback. It never substitutes for a failed ClickHouse connection. The backend builds with Go 1.17+; the modified daemon and CI use Go 1.27.3. Use a maintained toolchain for deployment.

## Implemented

- One native Go binary serves ingestion, query APIs, authentication and the embedded web UI; no frontend build dependencies.
- Existing daemon collector protocol 17 registration, compressed metric ingestion, and archival of trace/error/SQL/event/module payloads.
- Modified client daemon with its backend IP/hostname and HTTP/HTTPS transport in `daemon.cfg`.
- GitHub CI/CD builds separate backend and daemon `.deb` packages, with native systemd units and Ubuntu 22.04/24.04/26.04 acceptance jobs.
- ClickHouse tables with daily partitions, seven-day TTL and duplicate metric batch handling.
- Plugin/theme and hook charts, callback throughput, application/time filters, search, sorting, pagination, drilldowns and CSV export.
- Separate dashboard/ingestion credentials, tenant-bound runs and bounded requests.

Plugin/theme and hook timings overlap and remain separate views. The agent's plugin metric family includes themes; the UI does not pretend it can distinguish them reliably.

## Deploy and test

Follow the [native Linux deployment guide](docs/native-linux.md), including systemd and HTTPS. Configuration templates are in `deploy/`. See the [API and metric semantics](docs/api.md).

For Ubuntu, use the [package and CI/CD guide](docs/ubuntu-packages.md). Pushes and PRs build package artifacts; version tags publish release assets after native installation and ClickHouse checks pass. No Docker is used.

```powershell
go test ./...
go vet ./...
node --check web/public/app.js
# With the preview running:
./tools/send-fixture.ps1
```

The fixture creates a `Fixture WordPress` application through the real HTTP ingestion path. The native database integration test runs when `APM_TEST_CLICKHOUSE_URL` is set; optionally supply `APM_TEST_CLICKHOUSE_USER` and `APM_TEST_CLICKHOUSE_PASSWORD`. It creates a uniquely named database and leaves it for inspection:

```text
go test ./internal/apm -run TestNativeClickHouseIntegration -v
```

Without the URL the test explicitly skips. Local verification covers HTTP/protocol/storage-contract tests and browser interactions. No native Linux ClickHouse server or stock PHP runtime was available here; their interoperability is not claimed as tested.

## Scope

The dashboard implements WordPress aggregate metrics. Other APM envelopes are archived for future views; there is no trace waterfall, alerting, billing or full New Relic SaaS replacement. The dashboard token is an administrator credential across configured clients, not a client-specific viewer login.

The stock PHP extension remains unchanged. The modified daemon is in `daemon/`; it pins telemetry to the configured backend, blocks redirects and disables the separate Infinite Tracing observer. See [fork details](daemon/RELAY-FORK.md). Improved slow-trace selection and durable retries remain follow-up work. Policy-token/high-security negotiation is explicitly rejected; only the primary app name is stored, without rollup aliases.

The untouched upstream source is pinned to **v12.12.0.41**, commit `cf77822cd783c43cf3f8bb2acc1caa22c49e23cb`.

## Research

- [Source investigation and implementation options](docs/source-investigation.md)
- [Slow transaction trace analysis and recommended improvements](docs/slow-transaction-traces.md)
- [Download provenance](upstream/source-manifest.json)
- [Preserved upstream license](daemon/LICENSE)
- [Preserved upstream third-party notices](daemon/THIRD_PARTY_NOTICES.md)
