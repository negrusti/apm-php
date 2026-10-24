#!/usr/bin/env python3
"""Build native Ubuntu .deb packages without Docker, dpkg, or third-party Python.

Usage: python3 tools/build-debs.py --go /path/to/go [--arch amd64|arm64]
"""
import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import struct
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def tar_gz(files, epoch):
    raw = io.BytesIO()
    # Explicit directories make ownership/modes independent of the host's umask.
    directories = set()
    for name in files:
        directories.update(p.as_posix() for p in Path(name).parents if p.as_posix() != ".")
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for name in sorted(directories):
            info = tarfile.TarInfo("./" + name)
            info.type, info.mode, info.mtime = tarfile.DIRTYPE, 0o755, epoch
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            tar.addfile(info)
        for name, (data, mode) in sorted(files.items()):
            info = tarfile.TarInfo("./" + name)
            info.size, info.mode, info.mtime = len(data), mode, epoch
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            tar.addfile(info, io.BytesIO(data))
    return gzip.compress(raw.getvalue(), compresslevel=9, mtime=epoch)


def ar_member(name, data, epoch):
    header = f"{name + '/':<16}{epoch:<12}{0:<6}{0:<6}{'100644':<8}{len(data):<10}`\n".encode("ascii")
    if len(header) != 60:
        raise ValueError("invalid ar header")
    return header + data + (b"\n" if len(data) % 2 else b"")


def maintscripts(package, configs):
    group = package
    directory = f"/etc/{package}"
    config_paths = " ".join(directory + "/" + c for c in configs)
    postinst = f"""#!/bin/sh
set -eu
if [ "$1" = configure ]; then
    if ! getent passwd {group} >/dev/null; then
        adduser --system --group --no-create-home --home /nonexistent --disabled-login {group}
    fi
    chown root:{group} {directory} {config_paths}
    chmod 0750 {directory}
    chmod 0640 {config_paths}
    deb-systemd-helper unmask {package}.service >/dev/null || true
    deb-systemd-helper update-state {package}.service >/dev/null || true
    if [ -d /run/systemd/system ]; then
        systemctl --system daemon-reload >/dev/null || true
        # Preserve an administrator's enabled/disabled and running/stopped state.
        # A fresh installation is configured before its first explicit start.
        if systemctl --system is-active --quiet {package}.service; then
            deb-systemd-invoke try-restart {package}.service >/dev/null || true
        fi
    fi
fi
"""
    prerm = f"""#!/bin/sh
set -eu
case "$1" in
    remove|deconfigure) if [ -d /run/systemd/system ]; then
        deb-systemd-invoke stop {package}.service >/dev/null || true
    fi ;;
esac
"""
    postrm = f"""#!/bin/sh
set -eu
if [ "$1" = purge ]; then
    deb-systemd-helper purge {package}.service >/dev/null || true
    deb-systemd-helper unmask {package}.service >/dev/null || true
fi
if [ -d /run/systemd/system ]; then
    systemctl --system daemon-reload >/dev/null || true
fi
# Retain service accounts and application/ClickHouse data on removal or purge.
"""
    return {name: (body.encode(), 0o755) for name, body in
            [("postinst", postinst), ("prerm", prerm), ("postrm", postrm)]}


def file_bytes(path):
    return (ROOT / path).read_bytes()


def text_bytes(path):
    return (ROOT / path).read_text(encoding="utf-8").replace("\r\n", "\n").encode()


def assert_static_elf(data, arch):
    if data[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", data, 18)[0] != {"amd64": 62, "arm64": 183}[arch]:
        raise ValueError(f"not a Linux {arch} ELF binary")
    offset = struct.unpack_from("<Q", data, 32)[0]
    size, count = struct.unpack_from("<HH", data, 54)
    if any(struct.unpack_from("<I", data, offset + i * size)[0] == 3 for i in range(count)):
        raise ValueError("binary has a dynamic interpreter; glibc-independent build required")


def package(name, arch, version, epoch, out):
    daemon = name == "relay-php-daemon"
    binary = file_bytes(f"bin/{name if daemon else 'relay'}-linux-{arch}")
    assert_static_elf(binary, arch)
    configs = ["daemon.cfg"] if daemon else ["relay.env", "clients.json"]
    data = {
        f"usr/bin/{name}": (binary, 0o755),
        f"usr/lib/systemd/system/{name}.service": (text_bytes(f"deploy/packages/{name}.service"), 0o644),
        f"usr/share/doc/{name}/ubuntu-packages.md": (text_bytes("docs/ubuntu-packages.md"), 0o644),
    }
    if daemon:
        data.update({
            "etc/relay-php-daemon/daemon.cfg": (text_bytes("deploy/daemon.cfg.example"), 0o640),
            f"usr/share/doc/{name}/php-relay.ini.example": (text_bytes("deploy/php-relay.ini.example"), 0o644),
            f"usr/share/doc/{name}/fork.md": (text_bytes("daemon/RELAY-FORK.md"), 0o644),
            f"usr/share/doc/{name}/copyright": (text_bytes("daemon/LICENSE") + b"\n" + text_bytes("daemon/THIRD_PARTY_NOTICES.md"), 0o644),
        })
        description = "Relay daemon for the stock New Relic PHP extension\n Pins collector traffic to a configurable Relay backend; includes systemd service."
    else:
        data.update({
            "etc/relay-apm/relay.env": (text_bytes("deploy/relay.env.example"), 0o640),
            "etc/relay-apm/clients.json": (text_bytes("deploy/clients.example.json"), 0o640),
            f"usr/share/doc/{name}/native-linux.md": (text_bytes("docs/native-linux.md"), 0o644),
            f"usr/share/doc/{name}/Caddyfile.example": (text_bytes("deploy/Caddyfile.example"), 0o644),
            f"usr/share/doc/{name}/clickhouse-local.xml": (text_bytes("deploy/clickhouse-local.xml"), 0o644),
        })
        description = "Relay APM backend and WordPress dashboard\n Native ClickHouse ingester and embedded dashboard; includes systemd service."
    metadata = f"""Package: {name}
Version: {version}
Section: net
Priority: optional
Architecture: {arch}
Maintainer: Relay APM maintainers <maintainers@relay-apm.invalid>
Depends: adduser, ca-certificates, systemd, init-system-helpers (>= 1.56)
Installed-Size: {(sum(len(v[0]) for v in data.values()) + 1023) // 1024}
Description: {description}
"""
    control = maintscripts(name, configs)
    control["control"] = (metadata.encode(), 0o644)
    control["conffiles"] = (("".join(f"/etc/{name}/{c}\n" for c in configs)).encode(), 0o644)
    control["md5sums"] = (("".join(f"{hashlib.md5(content).hexdigest()}  {path}\n" for path, (content, _) in sorted(data.items()))).encode(), 0o644)
    contents = b"!<arch>\n" + b"".join(ar_member(n, b, epoch) for n, b in [
        ("debian-binary", b"2.0\n"), ("control.tar.gz", tar_gz(control, epoch)), ("data.tar.gz", tar_gz(data, epoch))])
    target = out / f"{name}_{version}_{arch}.deb"
    target.write_bytes(contents)
    print(f"Created {target.relative_to(ROOT)} ({len(contents):,} bytes)", flush=True)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--arch", choices=["amd64", "arm64"], action="append")
    parser.add_argument("--version", default="0.1.0-1")
    parser.add_argument("--skip-build", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9][A-Za-z0-9.+~\-]*", args.version):
        parser.error("invalid Debian version")
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", "1792886400"))
    arches = list(dict.fromkeys(args.arch or ["amd64", "arm64"]))
    go = str(Path(args.go).resolve()) if Path(args.go).is_file() else args.go
    out = ROOT / "dist" / "debs"
    out.mkdir(parents=True, exist_ok=True)
    (ROOT / "bin").mkdir(exist_ok=True)
    built = []
    for arch in arches:
        if not args.skip_build:
            env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0", GOTOOLCHAIN="local")
            subprocess.run([go, "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-ldflags", f"-s -w -X main.version={args.version}", "-o", str(ROOT / "bin" / f"relay-linux-{arch}"), "./cmd/relay"], cwd=ROOT, env=env, check=True)
            prefix = "github.com/newrelic/newrelic-php-agent/daemon/internal/newrelic/version"
            subprocess.run([go, "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-tags", "use_system_certs", "-ldflags", f"-s -w -X {prefix}.Number=12.12.0.41+relay.{args.version} -X {prefix}.Commit=cf77822cd783", "-o", str(ROOT / "bin" / f"relay-php-daemon-linux-{arch}"), "./cmd/daemon"], cwd=ROOT / "daemon", env=env, check=True)
        for name in ["relay-apm", "relay-php-daemon"]:
            built.append(package(name, arch, args.version, epoch, out))
    (out / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(f.read_bytes()).hexdigest()}  {f.name}\n" for f in sorted(built)), encoding="ascii", newline="\n")


if __name__ == "__main__":
    main()
