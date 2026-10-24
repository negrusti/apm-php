# Ubuntu packages and GitHub CI/CD

Packaging is performed by `.github/workflows/ubuntu-packages.yml` on native
GitHub-hosted Ubuntu runners. No Docker is used for builds, tests or deployment.

Two separate Debian packages are produced for each CPU architecture:

| Package | Install on | Includes |
| --- | --- | --- |
| `relay-apm` | Backend server | ClickHouse ingester, embedded web UI, `relay-apm.service` |
| `relay-php-daemon` | Each PHP client | Modified native daemon, config, `relay-php-daemon.service` |

The same static package works across Ubuntu 22.04, 24.04 and 26.04. No glibc,
OpenSSL, PHP, Python or Go runtime dependency is introduced by these binaries.
Systemd, CA certificates and account-management utilities are package
dependencies. ClickHouse and the stock PHP extension are installed separately.

## Pipeline and release

Pushes, pull requests and manual workflow runs build four packages (two
components × amd64/arm64), plus SHA-256 checksums, as the `ubuntu-debs` artifact.
Release assets are published on tags such as `v0.1.0`, after all checks pass.
The workflow uses the repository's standard `GITHUB_TOKEN`; no separate
publishing secret is needed. PRs receive only read access. Publishing is scoped
to the release job with `contents: write`.

Native acceptance runs on Ubuntu 22.04, 24.04 and 26.04 amd64, and on Ubuntu
24.04 and 26.04 arm64. Ubuntu 22.04 arm64 uses the same static binary, but is
not covered by the hosted runner matrix; validate that combination on a native
VM before deploying it. The matrix checks actual installation, unit validation,
service startup, native FlatBuffers registration, metric forwarding and query
totals for both plugin and hook metrics. It also checks configuration and
service-state preservation on reinstall, plus removal and purge.

Another native Ubuntu 24.04 job installs ClickHouse from its signed official
LTS repository and checks real SQL/storage behavior and the complete native
socket → daemon → ingester → ClickHouse → query API path. The daemon's
`backend` setting is deliberately used even when the registration reply names
a different collector. This job logs the database version tested.

Version tags must match `vMAJOR.MINOR.PATCH` (an optional suffix is allowed).
For example, `v0.1.0` publishes:

```text
relay-apm_0.1.0-1_amd64.deb
relay-php-daemon_0.1.0-1_amd64.deb
relay-apm_0.1.0-1_arm64.deb
relay-php-daemon_0.1.0-1_arm64.deb
SHA256SUMS
```

## Install backend

Download the package matching `dpkg --print-architecture` and its checksum
manifest from a successful workflow artifact or release. Verify the checksum
for the downloaded package (the full manifest also lists other assets):

```bash
grep 'relay-apm_0.1.0-1_amd64.deb$' SHA256SUMS | sha256sum -c -
sudo apt install ./relay-apm_0.1.0-1_amd64.deb
relay-apm -generate-secrets
```

Follow [native Linux setup](native-linux.md) to install/provision ClickHouse,
configure `/etc/relay-apm/relay.env` and `/etc/relay-apm/clients.json`, and set
up HTTPS. The backend binary is `/usr/bin/relay-apm`. Then:

```bash
sudo systemctl enable --now relay-apm
```

If migrating from the earlier manual `/opt/relay-apm` installer, stop the
service and remove its old `/etc/systemd/system/relay-apm.service` override
before installing the package. Preserve configuration; the packaged unit is
under `/usr/lib/systemd/system` and uses `/usr/bin/relay-apm`.

## Install daemon on PHP clients

```bash
sudo apt install ./relay-php-daemon_0.1.0-1_amd64.deb
sudoedit /etc/relay-php-daemon/daemon.cfg
```

Set the backend IP and port for a trusted private HTTP network:

```text
backend = "http://10.0.0.20:8088"
```

Or set a hostname for HTTPS:

```text
backend = "https://apm.example.com"
```

For private certificates, also set `ssl_ca_bundle` to a PEM file readable by
the `relay-php-daemon` service account. HTTPS to a literal IP works if the
certificate has that IP in its SAN. Certificate verification is never disabled.
HTTP carries ingestion keys and telemetry in plaintext. For HTTP clients to
reach Relay directly, bind `APM_LISTEN` to the backend's private interface,
restrict it to client machines, and set `APM_COLLECTOR_HOST` to `IP:port`.
The daemon still pins routing to its own configuration. Keep dashboard HTTPS
and secure cookies enabled when using HTTPS for the UI.

```bash
sudo systemctl enable --now relay-php-daemon
```

Merge `/usr/share/doc/relay-php-daemon/php-relay.ini.example` into the active
New Relic PHP extension INI. The important connection settings are:

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

The backend address belongs only in `daemon.cfg`; no PHP collector-host
override is necessary. Register the ingestion key on the backend, then restart
PHP-FPM/Apache. The reviewed protocol baseline is stock PHP agent v12.12.0.41.
Stop/disable any stock daemon service that is no longer used. Packages have
distinct names and sockets and do not remove your PHP agent installation.

## Service and upgrade behavior

Both packages install native systemd units under `/usr/lib/systemd/system`.
Fresh installation leaves services stopped and disabled until configured.
Upgrades retain config as Debian conffiles and preserve service enablement;
already running services are restarted. A stopped service stays stopped.
Config changes require a service restart. All logs go to `journalctl -u UNIT`.

`apt remove` retains config. `apt purge` removes package conffiles and service
enablement records. Service accounts and application/ClickHouse data remain.
The daemon socket uses upstream's local world-connectable permission so PHP
workers can reach it; it is inside a systemd-owned, non-writable runtime
directory. Per-client ingestion keys are still required by the backend.

## Development verification

The builder is a Python standard-library helper invoked by GitHub Actions;
it produces Debian ar/tar archives, static binaries and a checksum manifest.
`tools/verify-debs.py` checks archive structure, root ownership, permissions,
conffiles, checksums, executable architecture and absence of a dynamic loader.
The workflow additionally uses real `dpkg`, `apt` and systemd on native VMs.

To reproduce a failed workflow locally, use Go 1.27.3 and Python 3.10+:

```bash
python3 tools/build-debs.py --go /path/to/go
python3 tools/verify-debs.py
```

The install/remove acceptance script is intentionally restricted to disposable
Ubuntu VMs; it changes system configuration. The workflow must first be pushed
to a GitHub repository before any native test or release result can be claimed.
