"""替换下载边界验证 fnpack 发现与安装；不访问网络或运行外部下载内容。"""

import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PAGE = 'https://developer.fnnas.com/docs/cli/fnpack/'
WRAPPER = r'''#!PYTHON
import io, json, os, re, sys, urllib.request
from pathlib import Path
sys.argv = sys.argv[1:]
fixture = json.loads(Path(os.environ["FNPACK_TEST_FIXTURE"]).read_text())
def urlopen(url, timeout):
    with open(os.environ["FNPACK_TEST_REQUESTS"], "a") as log:
        log.write(url + "\n")
    if fixture.get("failure") == url or fixture.get("failure") == "all":
        raise OSError("Download failed: " + url)
    if url == "https://developer.fnnas.com/docs/cli/fnpack/":
        return io.BytesIO(fixture["page"].encode())
    version = fixture.get("tool_version") or re.search(r"fnpack-(\d+\.\d+\.\d+)-", url).group(1)
    content = fixture.get("tool", "#!/bin/sh\nprintf 'Version " + version + "\\n'\n")
    return io.BytesIO(content.encode())
urllib.request.urlopen = urlopen
exec(compile(sys.stdin.read(), '<install-fnpack>', 'exec'))
'''


class InstallFnpackTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="qms-fnpack-install-test-")
        self.root = Path(self.tmp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        wrapper = self.bin / "python3"
        wrapper.write_text(WRAPPER.replace('PYTHON', shutil.which("python3"), 1))
        wrapper.chmod(0o755)
        uname = self.bin / "uname"
        uname.write_text('#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
        uname.chmod(0o755)
        self.fixture = self.root / "fixture.json"
        self.requests = self.root / "requests"
        self.install = self.root / "tool dir"
        self.env = dict(os.environ, PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}", FNPACK_TEST_FIXTURE=str(self.fixture), FNPACK_TEST_REQUESTS=str(self.requests))
        for name in ("FNPACK_VERSION", "FNPACK_DOWNLOAD_URL"):
            self.env.pop(name, None)

    def tearDown(self):
        self.tmp.cleanup()

    def run_install(self, page='', **fixture):
        self.fixture.write_text(json.dumps(dict(page=page, **fixture)))
        return subprocess.run(
            ["bash", str(ROOT / "scripts/release/install-fnpack.sh"), str(self.install)],
            env=self.env, capture_output=True, text=True, timeout=30,
        )

    def link(self, version, host="linux-amd64"):
        return f'<a href="https://static2.fnnas.com/fnpack/fnpack-{version}-{host}">download</a>'

    def test_latest_stable_numeric_sort_and_build_host_selection(self):
        page = '\n'.join((self.link("1.9.9"), self.link("1.10.1"), self.link("1.99.0", "linux-arm64"), self.link("2.0.0-beta"), self.link("1.2.3")))
        result = self.run_install(page)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("version=1.10.1", result.stdout)
        self.assertEqual(self.requests.read_text().splitlines(), [PAGE, "https://static2.fnnas.com/fnpack/fnpack-1.10.1-linux-amd64"])
        record = (self.install / "fnpack-source.txt").read_text()
        self.assertEqual(record, result.stdout)
        self.assertIn("sha256=" + hashlib.sha256((self.install / "fnpack").read_bytes()).hexdigest(), record)
        self.assertTrue(os.access(self.install / "fnpack", os.X_OK))

    def test_explicit_version_skips_discovery(self):
        self.env["FNPACK_VERSION"] = "1.2.3"
        result = self.run_install(failure=PAGE)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.requests.read_text().splitlines(), ["https://static2.fnnas.com/fnpack/fnpack-1.2.3-linux-amd64"])

    def test_custom_url_takes_priority_and_redacts_credentials(self):
        secret_url = "https://user:password@example.test/fnpack?token=secret"
        self.env.update(FNPACK_DOWNLOAD_URL=secret_url, FNPACK_VERSION="ignored")
        result = self.run_install(tool_version="3.2.1")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.requests.read_text().splitlines(), [secret_url])
        self.assertIn("version=3.2.1", result.stdout)
        for content in (result.stdout + result.stderr, (self.install / "fnpack-source.txt").read_text()):
            self.assertNotIn("password", content)
            self.assertNotIn("token=secret", content)

    def test_custom_url_failure_cannot_leak_credentials(self):
        self.env["FNPACK_DOWNLOAD_URL"] = "https://user:password@example.test/secret"
        result = self.run_install(failure="all")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("password", result.stdout + result.stderr)
        self.assertFalse((self.install / "fnpack").exists())

    def test_discovery_and_download_failure_do_not_fallback(self):
        for fixture in ({}, {"page": self.link("1.2.3"), "failure": PAGE}, {"page": self.link("1.2.3"), "failure": "https://static2.fnnas.com/fnpack/fnpack-1.2.3-linux-amd64"}):
            with self.subTest(fixture=fixture):
                result = self.run_install(**fixture)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.install / "fnpack").exists())
                self.assertFalse((self.install / "fnpack-source.txt").exists())

    def test_invalid_or_wrong_version_tool_rejected(self):
        for fixture in ({"tool": "invalid executable"}, {"tool": "#!/bin/sh\nexit 1\n"}, {"tool_version": "1.2.2"}):
            with self.subTest(fixture=fixture):
                result = self.run_install(self.link("1.2.3"), **fixture)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.install / "fnpack").exists())
                self.assertFalse(list(self.install.glob(".fnpack-*")))

    def test_invalid_explicit_version_rejected_without_network(self):
        self.env["FNPACK_VERSION"] = "latest"
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.requests.exists())


if __name__ == "__main__":
    unittest.main()
