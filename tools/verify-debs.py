#!/usr/bin/env python3
"""Verify package structure, contents, permissions, checksums and static ELF ABI."""
import hashlib
import io
from pathlib import Path
import sys
import tarfile

from importlib.machinery import SourceFileLoader
builder = SourceFileLoader("deb_builder", str(Path(__file__).with_name("build-debs.py"))).load_module()


def members(blob):
    assert blob.startswith(b"!<arch>\n"), "missing ar magic"
    offset, result = 8, {}
    while offset < len(blob):
        h = blob[offset:offset + 60]
        assert len(h) == 60 and h[-2:] == b"`\n", "invalid ar header"
        name = h[:16].decode("ascii").strip().rstrip("/")
        length = int(h[48:58])
        assert name not in result, "duplicate ar member"
        result[name] = blob[offset + 60:offset + 60 + length]
        assert len(result[name]) == length, "truncated member"
        offset += 60 + length + length % 2
    assert offset == len(blob)
    assert list(result) == ["debian-binary", "control.tar.gz", "data.tar.gz"]
    assert result["debian-binary"] == b"2.0\n"
    return result


def untar(blob):
    result = {}
    with tarfile.open(fileobj=io.BytesIO(blob), mode="r:gz") as archive:
        for item in archive:
            name = item.name.removeprefix("./")
            assert not name.startswith("/") and ".." not in Path(name).parts
            assert item.uid == item.gid == 0
            if item.isdir(): continue
            assert item.isfile(), "unexpected link/device in package"
            assert name not in result
            result[name] = (archive.extractfile(item).read(), item.mode)
    return result


def verify(path):
    m = members(path.read_bytes())
    control, data = untar(m["control.tar.gz"]), untar(m["data.tar.gz"])
    metadata = control["control"][0].decode()
    package = next(s.split(": ",1)[1] for s in metadata.splitlines() if s.startswith("Package: "))
    architecture = next(s.split(": ",1)[1] for s in metadata.splitlines() if s.startswith("Architecture: "))
    assert package in ["relay-apm", "relay-php-daemon"]
    assert "systemd" in metadata and "init-system-helpers" in metadata
    binary = data[f"usr/bin/{package}"]
    assert binary[1] == 0o755
    builder.assert_static_elf(binary[0], architecture)
    unit = data[f"usr/lib/systemd/system/{package}.service"]
    assert unit[1] == 0o644 and f"ExecStart=/usr/bin/{package}".encode() in unit[0]
    assert f"User={package}".encode() in unit[0]
    for name in ["postinst", "prerm", "postrm"]:
        assert control[name][1] == 0o755 and control[name][0].startswith(b"#!/bin/sh\n")
        assert b"\r" not in control[name][0]
    conffiles = control["conffiles"][0].decode().splitlines()
    assert len(conffiles) == (1 if package == "relay-php-daemon" else 2)
    for conf in conffiles:
        assert conf.startswith(f"/etc/{package}/") and data[conf[1:]][1] == 0o640
    for line in control["md5sums"][0].decode().splitlines():
        checksum, name = line.split("  ", 1)
        assert hashlib.md5(data[name][0]).hexdigest() == checksum
    assert len(control["md5sums"][0].decode().splitlines()) == len(data)
    if package == "relay-php-daemon":
        config = data["etc/relay-php-daemon/daemon.cfg"][0]
        assert b'backend = "https://apm.example.com"' in config
        assert b"http://10.0.0.20:8088" in config
        assert f"usr/share/doc/{package}/copyright" in data
    print(f"PASS {path.name}: {architecture}, systemd, conffiles, checksums, static ELF")


def main():
    directory = builder.ROOT / "dist/debs"
    files = [Path(p) for p in sys.argv[1:]] or sorted(directory.glob("*.deb"))
    assert files, "no packages to verify"
    for path in files: verify(path)
    if not sys.argv[1:]:
        for line in (directory / "SHA256SUMS").read_text().splitlines():
            checksum, name = line.split("  ", 1)
            assert Path(name).name == name
            assert hashlib.sha256((directory / name).read_bytes()).hexdigest() == checksum


if __name__ == "__main__": main()
