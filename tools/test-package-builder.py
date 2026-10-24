#!/usr/bin/env python3
"""Exercise package serialization with small synthetic ELF files in temp storage."""
import importlib.util
from pathlib import Path
import struct
import tempfile
import unittest
from unittest.mock import patch

here = Path(__file__).parent
def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, here / filename)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

builder = module("package_builder", "build-debs.py")
verify = module("package_verifier", "verify-debs.py")


class PackageTests(unittest.TestCase):
    def test_two_components_and_architectures(self):
        def source(path):
            elf = bytearray(64)
            elf[:6] = b"\x7fELF\x02\x01"
            struct.pack_into("<H", elf, 18, 183 if "arm64" in path else 62)
            return bytes(elf)
        with tempfile.TemporaryDirectory() as tmp, patch.object(builder, "ROOT", Path(tmp)), patch.object(builder, "file_bytes", source):
            for arch in ["amd64", "arm64"]:
                for name in ["relay-apm", "relay-php-daemon"]:
                    # Real templates/docs with synthetic, non-executable ELF payload.
                    with patch.object(builder, "text_bytes", lambda p: (here.parent / p).read_text(encoding="utf-8").replace("\r\n", "\n").encode()):
                        package = builder.package(name, arch, "0.1.0~ci.1-1", 1792886400, Path(tmp))
                    verify.verify(package)
                    first = package.read_bytes()
                    with patch.object(builder, "text_bytes", lambda p: (here.parent / p).read_text(encoding="utf-8").replace("\r\n", "\n").encode()):
                        builder.package(name, arch, "0.1.0~ci.1-1", 1792886400, Path(tmp))
                    self.assertEqual(first, package.read_bytes(), "packaging should be deterministic")


if __name__ == "__main__": unittest.main()
