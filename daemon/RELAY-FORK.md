# Relay PHP daemon fork

This directory contains the complete Go daemon from New Relic PHP agent
v12.12.0.41, commit `cf77822cd783c43cf3f8bb2acc1caa22c49e23cb`, plus the
changes below. Apache-2.0 license and third-party notices are preserved.
The downloaded baseline in `upstream/` remains untouched. The stock PHP
extension is not rebuilt or included in our package.

## Changes

- Required `backend` setting in daemon configuration, also exposed as
  `--backend`. Supports `http://IP:port`, `https://hostname[:port]`, and HTTPS
  to an IP covered by the server certificate. No URL credentials/paths allowed.
- Every collector command is pinned to that origin, including preconnect,
  connect, metrics, traces, errors, SQL and module inventory. Agent collector
  settings and collector-provided redirect hosts cannot change the origin.
- HTTP redirects are rejected, including redirects to the same host. Configure
  the final HTTP/HTTPS origin directly; credentials never traverse redirects.
- Infinite Tracing's separate observer connection is disabled before app
  registration. Ordinary transaction traces still use the Relay collector.
- Packages use Ubuntu's system CA trust store. An optional `ssl_ca_bundle`
  supports a private CA. TLS verification stays enabled. Explicit HTTP sends
  credentials and telemetry unencrypted and is intended for a trusted network.
- Distinct executable/version, config directory, systemd unit and Unix socket.
  The service runs as an unprivileged account and logs to the journal. Cloud
  metadata detection is disabled in the supplied configuration.
- Cross-agent fixtures are copied under `test-data/cross-agent` so tests can
  run without the full PHP source tree. Native acceptance probe is CI-only.

`tools/prepare-daemon-fork.py` records source substitutions against a pristine
copy; do not rerun it over an already modified directory. Relay-specific code
and tests are in `collector/relay*.go`, `newrelic/relay_test.go`, and
`cmd/relay-probe`. Build with Go 1.27.3 and `-tags use_system_certs`.

## Compatibility and remaining limits

The PHP-to-daemon FlatBuffers protocol and upstream transaction aggregation
remain intact. Runtime compatibility must be verified against the stock PHP
extension version actually deployed. The native CI probe sends that protocol;
it does not execute WordPress or load a real PHP extension.

This fork does not yet improve upstream slow-trace selection, failed trace
rollover, or crash persistence. Metrics preserve upstream bounded retries;
there is no durable disk queue. See `docs/slow-transaction-traces.md` for the
trace improvements proposed separately.
