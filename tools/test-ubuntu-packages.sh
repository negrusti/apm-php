#!/usr/bin/env bash
set -euo pipefail
# Destructive install/remove acceptance test: ONLY on a disposable native VM.
if [[ ${1:-} != --disposable-vm ]]; then
  echo 'Run only on a disposable Ubuntu VM: test-ubuntu-packages.sh --disposable-vm [--clickhouse]' >&2
  exit 2
fi
if [[ ${EUID} -ne 0 ]]; then echo 'Run as root on a disposable VM.' >&2; exit 2; fi
source /etc/os-release
case "$ID:$VERSION_ID" in ubuntu:22.04|ubuntu:24.04|ubuntu:26.04) ;; *) echo "Unsupported test VM: $ID $VERSION_ID" >&2; exit 2 ;; esac
project_dir=$(cd "$(dirname "$0")/.." && pwd)
architecture=$(dpkg --print-architecture)
shopt -s nullglob
packages=("$project_dir"/dist/debs/*_"$architecture".deb)
[[ ${#packages[@]} == 2 ]] || { echo 'Expected exactly two packages for the VM architecture.' >&2; exit 1; }
probe="$project_dir/bin/relay-probe-linux-$architecture"
[[ -x "$probe" ]] || { echo "Missing acceptance probe: $probe" >&2; exit 1; }
trap 'journalctl -u relay-apm -u relay-php-daemon --no-pager -n 100 || true' ERR
apt-get install -y "${packages[@]}"
relay-apm -version
relay-php-daemon --version
systemd-analyze verify /usr/lib/systemd/system/relay-apm.service /usr/lib/systemd/system/relay-php-daemon.service
for unit in relay-apm relay-php-daemon; do
  if systemctl is-active --quiet "$unit"; then echo 'Fresh installation unexpectedly started an unconfigured service.' >&2; exit 1; fi
done
[[ $(stat -c %a /etc/relay-apm/relay.env) == 640 ]]
[[ $(stat -c %a /etc/relay-php-daemon/daemon.cfg) == 640 ]]
sed -i 's|^backend = .*|backend = "http://127.0.0.1:8088"|' /etc/relay-php-daemon/daemon.cfg
# These credentials are public test fixtures, never production credentials.
export APM_PROBE_KEY=0123456789abcdef0123456789abcdef01234567
export APM_PROBE_ADMIN=local-sample-only
if [[ ${2:-} == --clickhouse ]]; then
  export APM_PROBE_ADMIN=package-acceptance-token-for-disposable-ci-vm
  clickhouse-client --multiquery --query "CREATE DATABASE relay_apm; CREATE USER relay IDENTIFIED BY 'disposable-ci-password' HOST LOCAL; GRANT SELECT, INSERT, CREATE TABLE ON relay_apm.* TO relay;"
  cat > /etc/relay-apm/relay.env <<EOF
APM_LISTEN=127.0.0.1:8088
APM_ADMIN_TOKEN=$APM_PROBE_ADMIN
APM_CLIENTS_FILE=/etc/relay-apm/clients.json
APM_COLLECTOR_HOST=must-not-be-used.invalid
APM_SECURE_COOKIES=false
CLICKHOUSE_URL=http://127.0.0.1:8123
CLICKHOUSE_DATABASE=relay_apm
CLICKHOUSE_USER=relay
CLICKHOUSE_PASSWORD=disposable-ci-password
EOF
  printf '[{"key":"%s","tenant":"ci"}]\n' "$APM_PROBE_KEY" > /etc/relay-apm/clients.json
else
  # Explicit test-only override; never shipped in a package.
  install -d /etc/systemd/system/relay-apm.service.d
  cat > /etc/systemd/system/relay-apm.service.d/acceptance.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/bin/relay-apm -demo
EOF
fi
printf '\n# Preserve this administrator edit across reinstall.\n' >> /etc/relay-apm/relay.env
config_checksum=$(sha256sum /etc/relay-apm/relay.env /etc/relay-apm/clients.json /etc/relay-php-daemon/daemon.cfg)
systemctl daemon-reload
systemctl enable --now relay-apm relay-php-daemon
for attempt in {1..30}; do
  if curl --silent --fail http://127.0.0.1:8088/healthz >/dev/null && [[ -S /run/relay-php-daemon/agent.sock ]]; then break; fi
  sleep 1
done
systemctl is-active --quiet relay-apm relay-php-daemon
curl --silent --fail http://127.0.0.1:8088/healthz >/dev/null
# Reinstall while active; configuration, service enablement and running state survive.
dpkg --force-confold -i "${packages[@]}"
[[ "$config_checksum" == "$(sha256sum /etc/relay-apm/relay.env /etc/relay-apm/clients.json /etc/relay-php-daemon/daemon.cfg)" ]]
systemctl is-enabled --quiet relay-apm relay-php-daemon
systemctl is-active --quiet relay-apm relay-php-daemon
"$probe" -phase send
sleep 2
systemctl stop relay-php-daemon
"$probe" -phase check
# Verify a stopped/disabled service remains stopped/disabled during an upgrade.
systemctl disable relay-php-daemon
dpkg --force-confold -i "${packages[@]}"
if systemctl is-active --quiet relay-php-daemon || systemctl is-enabled --quiet relay-php-daemon; then
  echo 'Reinstall changed the administrator service state.' >&2; exit 1
fi
apt-get remove -y relay-apm relay-php-daemon
[[ -f /etc/relay-apm/relay.env && -f /etc/relay-php-daemon/daemon.cfg ]]
apt-get purge -y relay-apm relay-php-daemon
[[ ! -f /etc/relay-apm/relay.env && ! -f /etc/relay-php-daemon/daemon.cfg ]]
echo "PASS: Ubuntu $VERSION_ID $architecture installation, services, protocol, upgrade, removal and purge"
