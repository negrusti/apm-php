#!/usr/bin/env bash
set -euo pipefail
# Run from the project root after installing ClickHouse and building ./bin/relay.
# This installs application files; it does not overwrite existing credentials.
if [[ ${EUID} -ne 0 ]]; then echo "Run this installer as root." >&2; exit 1; fi
if [[ ! -f bin/relay || ! -f deploy/relay-apm.service ]]; then
  echo "From the project root, build first: go build -o bin/relay ./cmd/relay" >&2; exit 1
fi
if ! id relay-apm >/dev/null 2>&1; then useradd --system --home-dir /opt/relay-apm --shell /usr/sbin/nologin relay-apm; fi
install -d -m 0755 /opt/relay-apm
install -d -m 0750 -o root -g relay-apm /etc/relay-apm
install -m 0755 bin/relay /opt/relay-apm/relay
install -m 0644 deploy/relay-apm.service /etc/systemd/system/relay-apm.service
if [[ ! -f /etc/relay-apm/relay.env ]]; then install -m 0640 -o root -g relay-apm deploy/relay.env.example /etc/relay-apm/relay.env; fi
if [[ ! -f /etc/relay-apm/clients.json ]]; then install -m 0640 -o root -g relay-apm deploy/clients.example.json /etc/relay-apm/clients.json; fi
systemctl daemon-reload
echo "Installed. Configure /etc/relay-apm/relay.env and clients.json, provision ClickHouse, then enable the service."
