# Verification — 25 October 2026

## Completed locally

- `go test ./... -count=1 -cover` passed with Go 1.27.3 on Windows/amd64. The core ingestion/query package has 65.8% statement coverage. The real database test is gated and skipped without its endpoint.
- `go vet ./...` and `node --check web/public/app.js` passed.
- Seven automated tests exercise collector registration/compression, metric ingestion, weighted averages, plugin/hook separation, scoped-copy exclusion, identical retries, tenant/run isolation, authentication/origin checks, invalid/expired/nonfinite metrics, decompression limits, storage failures, and the ClickHouse HTTP contract with parameterized queries and flush-before-acknowledgement settings.
- Static Linux amd64 and arm64 binaries built successfully with Go 1.27.3 and `CGO_ENABLED=0`. Checksums are recorded in `bin/SHA256SUMS`.
- Browser checks exercised plugin and hook charts, application selection, 6-hour and 7-day ranges, search, sorting, drilldowns, CSV export and connection instructions.
- A native HTTP fixture appeared in the UI: WooCommerce 120 calls, 20 ms average, 2.4 s total, 1.2 s self time, and 80% of the fixture's plugin-family self time. The separate `init` hook showed 150 calls and 1.5 s self time. The drilldown matched the table's exact window.
- Screenshots in `docs/screenshots/` show the verified sample dashboard and fixture detail. Sample mode is explicitly labeled and loopback-only; its data is temporary, not stored in ClickHouse.
- The downloaded upstream source is unchanged.

## Not run in this Windows workspace

- Actual ClickHouse SQL execution, TTL cleanup and persistence across server restarts.
- End-to-end traffic from a stock PHP module and daemon.
- Linux systemd/Caddy installation and external HTTPS/DNS.
- Production traffic/retention/resource load testing.

Run the included `TestNativeClickHouseIntegration` against the Linux server, then validate the stock PHP package and WordPress workload. Follow `native-linux.md`; these checks are not silently represented as having passed.
