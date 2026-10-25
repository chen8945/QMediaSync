"""用临时模板和 fnpack 替身检查 FPK 组装及失败边界，不修改真实模板。"""

import hashlib
import json
import os
import shutil
import struct
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FNPACK = r'''#!/usr/bin/env python3
import io, os, tarfile
from pathlib import Path
mode = os.environ.get("FNPACK_TEST_MODE", "ok")
if mode == "fail":
    raise SystemExit(7)
if mode == "no-output":
    raise SystemExit(0)
if mode == "garbage":
    Path("qmediasync.fpk").write_bytes(b"invalid")
    raise SystemExit(0)
if mode == "wrong-version":
    path = Path("manifest")
    path.write_text(path.read_text().replace("1.2.3", "1.2.2"))
if mode == "wrong-binary":
    Path("app/QMediaSync").write_bytes(b"wrong binary")
if mode == "missing-service":
    Path("app/scripts/fnos-service.sh").unlink()
if mode == "missing-web-asset":
    Path("app/web_statics/assets.js").unlink()
def archive_mode(member):
    # shortcut: Windows 替身模拟 Unix 执行位，真实权限由 Linux CI 验证。
    if os.name == "nt" and member.name in ("QMediaSync", "scripts/fnos-service.sh"):
        member.mode |= 0o111
    return member
payload = io.BytesIO()
with tarfile.open(fileobj=payload, mode="w:gz") as app:
    for path in sorted(Path("app").iterdir()):
        app.add(path, arcname=path.name, filter=archive_mode)
payload.seek(0)
with tarfile.open("qmediasync.fpk", "w:gz") as fpk:
    for path in sorted(Path(".").iterdir()):
        if path.name not in ("app", "qmediasync.fpk"):
            fpk.add(path, arcname=path.name)
    info = tarfile.TarInfo("app.tgz")
    info.size = len(payload.getvalue())
    fpk.addfile(info, payload)
'''



class PackageFnosTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="qms-fpk-test-")
        self.root = Path(self.tmp.name)
        self.script = self.root / "scripts/release/package-fnos.sh"
        self.script.parent.mkdir(parents=True)
        shutil.copy2(ROOT / "scripts/release/package-fnos.sh", self.script)
        self.fnos = self.root / "backend/fnos"
        for arch, platform in (("amd64", "x86"), ("arm64", "arm")):
            template = self.fnos / f"qmediasync-{arch}"
            for name in (
                "app/ui/config", "app/ui/images/icon_64.png", "app/ui/images/icon_256.png",
                "config/privilege", "config/resource", "wizard/install", "wizard/uninstall", "ICON.PNG", "ICON_256.PNG",
                *(f"cmd/{hook}" for hook in ("main", "install_init", "install_callback", "upgrade_init", "upgrade_callback", "config_init", "config_callback", "uninstall_init", "uninstall_callback")),
            ):
                path = template / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(f"fixture {name}\n")
            (template / "manifest").write_text(f"appname = qmediasync\nversion = 1.2.3\nplatform = {platform}\n")
            (template / "qmediasync.fpk").write_bytes(b"stale source artifact")
            (template / "config.yaml").write_text("obsolete sample\n")
        (self.fnos / "common").mkdir()
        (self.fnos / "common/service.sh").write_text("#!/bin/sh\nexit 0\n")
        self.bin = self.root / "tools"
        self.bin.mkdir()
        (self.bin / "fnpack").write_text(FNPACK)
        (self.bin / "fnpack").chmod(0o755)
        self.web = self.root / "web assets"
        self.web.mkdir()
        (self.web / "index.html").write_text("QMS frontend\n")
        (self.web / "assets.js").write_text("QMS asset\n")
        self.binary = self.root / "QMS binary"
        self.out = self.root / "release assets"
        self.env = dict(os.environ, PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}", REQUIRE_FNPACK="1")

    def tearDown(self):
        self.tmp.cleanup()

    def write_binary(self, arch="amd64", version="v1.2.3"):
        header = bytearray(b"\x7fELF\x02\x01" + b"\x00" * 14)
        struct.pack_into("<H", header, 18, 62 if arch == "amd64" else 183)
        self.binary.write_bytes(header + b"test executable")
        metadata = {"version": version, "goos": "linux", "goarch": arch,
                    "sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest()}
        Path(str(self.binary) + ".build.json").write_text(json.dumps(metadata))

    def run_package(self, arch="amd64", mode="ok", tag="v1.2.3"):
        return subprocess.run(
            ["bash", str(self.script), tag, arch, str(self.binary), str(self.web), str(self.out)],
            cwd=self.root.parent, env=dict(self.env, FNPACK_TEST_MODE=mode),
            capture_output=True, text=True, timeout=30,
        )

    def source_hashes(self):
        return {str(p.relative_to(self.fnos)): hashlib.sha256(p.read_bytes()).hexdigest() for p in self.fnos.rglob("*") if p.is_file()}

    def test_both_architectures_clean_staging_and_names(self):
        before = self.source_hashes()
        for arch, asset in (("amd64", "x86_64"), ("arm64", "arm64")):
            with self.subTest(arch=arch):
                self.write_binary(arch)
                result = self.run_package(arch)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                package = self.out / f"QMediaSync_fnos_{asset}.fpk"
                with tarfile.open(package) as outer:
                    self.assertNotIn("config.yaml", outer.getnames())
                    with tarfile.open(fileobj=outer.extractfile("app.tgz")) as app:
                        self.assertEqual(app.extractfile("QMediaSync").read(), self.binary.read_bytes())
                        self.assertIn("scripts/fnos-service.sh", app.getnames())
                self.assertEqual(self.source_hashes(), before)
        self.assertEqual(len(list(self.out.iterdir())), 2)

    def test_beta_versions_only_change_staging_manifest_for_both_architectures(self):
        version = "1.2.3-beta.20261025103000.g1a2b3c4"
        before = self.source_hashes()
        for arch, asset in (("amd64", "x86_64"), ("arm64", "arm64")):
            with self.subTest(arch=arch):
                self.write_binary(arch, version)
                result = self.run_package(arch, tag=version)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                with tarfile.open(self.out / f"QMediaSync_{version}_fnos_{asset}.fpk") as package:
                    manifest = package.extractfile("manifest").read().decode().splitlines()
                    self.assertIn(f"version = {version}", manifest)
                    self.assertIn("appname = qmediasync", manifest)
                self.assertEqual(self.source_hashes(), before)

    def test_beta_failure_preserves_source_and_rejects_wrong_inputs_or_package(self):
        version = "1.2.3-beta.20261025103000.g1a2b3c4"
        before = self.source_hashes()
        for mode in ("fail", "no-output", "wrong-version", "wrong-binary", "missing-web-asset"):
            with self.subTest(mode=mode):
                self.write_binary(version=version)
                result = self.run_package(mode=mode, tag=version)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.out.exists())
                self.assertEqual(self.source_hashes(), before)
        self.write_binary()
        self.assertNotEqual(self.run_package(tag=version).returncode, 0)
        self.write_binary(version="1.2.2-beta.20261025103000.g1a2b3c4")
        self.assertNotEqual(self.run_package(tag="1.2.2-beta.20261025103000.g1a2b3c4").returncode, 0)
        self.assertEqual(self.source_hashes(), before)

    def test_stale_artifact_and_success_without_output_are_rejected(self):
        self.write_binary()
        self.out.mkdir()
        stale = self.out / "QMediaSync_fnos_x86_64.fpk"
        stale.write_bytes(b"old published artifact")
        result = self.run_package(mode="no-output")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Invalid fnOS package", result.stderr)
        self.assertEqual(stale.read_bytes(), b"old published artifact")

    def test_build_failure_or_invalid_archive_cannot_publish(self):
        self.write_binary()
        for mode in ("fail", "garbage", "wrong-version", "wrong-binary", "missing-service", "missing-web-asset"):
            with self.subTest(mode=mode):
                result = self.run_package(mode=mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(self.out.exists())

    def test_mismatch_in_either_source_manifest_rejects_build(self):
        self.write_binary()
        for arch in ("amd64", "arm64"):
            with self.subTest(arch=arch):
                manifest = self.fnos / f"qmediasync-{arch}/manifest"
                original = manifest.read_text()
                manifest.write_text(original.replace("1.2.3", "1.2.2"))
                result = self.run_package()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("manifest version must match", result.stderr)
                self.assertEqual(manifest.read_text(), original.replace("1.2.3", "1.2.2"))
                manifest.write_text(original)
        self.assertFalse(self.out.exists())

    def test_wrong_binary_architecture_and_missing_web_rejected(self):
        self.write_binary("arm64")
        result = self.run_package()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not a Linux amd64", result.stderr)
        self.write_binary()
        (self.web / "index.html").unlink()
        self.assertNotEqual(self.run_package().returncode, 0)

    def test_build_metadata_rejects_missing_wrong_version_platform_or_hash(self):
        self.write_binary()
        metadata_path = Path(str(self.binary) + ".build.json")
        original = json.loads(metadata_path.read_text())
        for metadata in (None, "invalid", {}, {**original, "version": "v1.2.2"},
                         {**original, "version": None}, {**original, "goos": "windows"},
                         {**original, "goarch": "arm64"}, {**original, "sha256": "0" * 64}):
            with self.subTest(metadata=metadata):
                if metadata is None:
                    metadata_path.unlink()
                elif metadata == "invalid":
                    metadata_path.write_text("private invalid build data")
                else:
                    metadata_path.write_text(json.dumps(metadata))
                result = self.run_package()
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("private invalid build data", result.stdout + result.stderr)
                self.assertFalse(self.out.exists())
        metadata_path.write_text(json.dumps({**original, "version": "1.2.3"}))
        result = self.run_package()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        with tarfile.open(self.out / "QMediaSync_fnos_x86_64.fpk") as outer:
            with tarfile.open(fileobj=outer.extractfile("app.tgz")) as app:
                self.assertFalse(any(name.endswith(".build.json") for name in app.getnames()))

    def test_missing_tool_required_or_explicitly_optional(self):
        (self.bin / "fnpack").unlink()
        self.assertNotEqual(self.run_package().returncode, 0)
        self.env["REQUIRE_FNPACK"] = "0"
        result = self.run_package()
        self.assertEqual(result.returncode, 0)
        self.assertIn("skipping fnOS", result.stdout)

    def test_invalid_tag_and_arch(self):
        for arch, tag in (("amd64", "latest"), ("riscv64", "v1.2.3"),
                          ("amd64", "1.2.3-beta.bad"), ("amd64", "1.2.3-beta.20261025103000.g../bad")):
            result = self.run_package(arch=arch, tag=tag)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
