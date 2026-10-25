"""检查正式版与 Beta 安装包的命名、资源布局及 Linux 执行权限。"""

import os
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "release/package-release-asset.sh"
VERSIONS = ("v1.2.3", "1.2.3-beta.20261025103000.g1a2b3c4")
ARCHITECTURES = (("amd64", "x86_64"), ("arm64", "arm64"))


class PackageReleaseAssetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="qms-package-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "binary").write_bytes(b"test executable")
        self.web = self.root / "web"
        self.web.mkdir()
        (self.web / "index.html").write_text("test frontend")
        for script in ("entrypoint.sh", "watch-update.sh"):
            (self.root / script).write_text("#!/bin/sh\nexit 0\n")
        (self.root / "icon.ico").write_bytes(b"test icon")

    def build_package(self, version, goos, arch, asset_arch):
        out = self.root / f"{version}-{goos}-{arch}"
        result = subprocess.run(
            ["bash", str(SCRIPT), version, goos, arch, str(self.root / "binary"), str(self.web), str(out)],
            env=dict(os.environ, DOCKER_ENTRYPOINT=str(self.root / "entrypoint.sh"),
                     DOCKER_WATCH_UPDATE=str(self.root / "watch-update.sh"), ICON_PATH=str(self.root / "icon.ico")),
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        base = f"QMediaSync_{goos}_{asset_arch}"
        name = f"QMediaSync_{version}_{goos}_{asset_arch}" if "-beta." in version else base
        return out / name, base

    def test_windows_release_and_beta_packages(self):
        for version in VERSIONS:
            for arch, asset_arch in ARCHITECTURES:
                with self.subTest(version=version, arch=arch):
                    path, _ = self.build_package(version, "windows", arch, asset_arch)
                    with zipfile.ZipFile(str(path) + ".zip") as package:
                        self.assertEqual(set(package.namelist()), {"QMediaSync.exe", "icon.ico", "web_statics/", "web_statics/index.html"})
                        self.assertEqual(package.read("QMediaSync.exe"), b"test executable")
                        self.assertEqual(package.read("web_statics/index.html"), b"test frontend")

    def test_linux_release_and_beta_packages(self):
        for version in VERSIONS:
            for arch, asset_arch in ARCHITECTURES:
                with self.subTest(version=version, arch=arch):
                    path, base = self.build_package(version, "linux", arch, asset_arch)
                    with tarfile.open(str(path) + ".tar.gz") as package:
                        self.assertEqual(package.extractfile(f"{base}/QMediaSync").read(), b"test executable")
                        self.assertTrue(package.getmember(f"{base}/QMediaSync").mode & 0o111)
                        self.assertEqual(package.extractfile(f"{base}/web_statics/index.html").read(), b"test frontend")
                        for script in ("docker-entrypoint.sh", "watch_update.sh"):
                            self.assertIn(f"{base}/scripts/{script}", package.getnames())


if __name__ == "__main__":
    unittest.main()
