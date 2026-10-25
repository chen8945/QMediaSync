"""验证 Beta 构建标识与保留期优先级，不修改仓库模板。"""

import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "release/beta-metadata.py"


class BetaMetadataTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="qms-beta-metadata-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for arch in ("amd64", "arm64"):
            manifest = self.root / f"backend/fnos/qmediasync-{arch}/manifest"
            manifest.parent.mkdir(parents=True)
            manifest.write_text("appname = qmediasync\nversion = 1.2.3\n")

    def run_metadata(self, manual="", variable="", sha="1a2b3c4" + "0" * 33):
        return subprocess.run(
            [sys.executable, str(SCRIPT)], cwd=self.root,
            env=dict(os.environ, PYTHONUTF8="1", INPUT_RETENTION_DAYS=manual,
                     BETA_ARTIFACT_RETENTION_DAYS=variable, GITHUB_SHA=sha),
            capture_output=True, text=True, encoding="utf-8", timeout=10,
        )

    def test_retention_priority_and_boundaries(self):
        for manual, variable, expected in (
            ("", "", "90"), ("", "14", "14"), ("7", "14", "7"),
            ("1", "", "1"), ("90", "", "90"), ("7", "invalid", "7"),
        ):
            with self.subTest(manual=manual, variable=variable):
                result = self.run_metadata(manual, variable)
                self.assertEqual(result.returncode, 0, result.stderr)
                outputs = dict(line.split("=", 1) for line in result.stdout.splitlines())
                self.assertEqual(outputs["retention_days"], expected)

    def test_invalid_selected_retention_has_no_output_or_fallback(self):
        for invalid in ("0", "91", "-1", "1.5", "invalid", " ", "7\nversion=bad", "９"):
            for manual, variable in ((invalid, "14"), ("", invalid)):
                with self.subTest(manual=manual, variable=variable):
                    result = self.run_metadata(manual, variable)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")

    def test_version_matches_date_commit_and_source_is_unchanged(self):
        before = {path: path.read_bytes() for path in self.root.rglob("manifest")}
        result = self.run_metadata()
        self.assertEqual(result.returncode, 0, result.stderr)
        outputs = dict(line.split("=", 1) for line in result.stdout.splitlines())
        date = datetime.strptime(outputs["build_date"], "%Y-%m-%dT%H:%M:%SZ")
        self.assertEqual(outputs["version"], f"1.2.3-beta.{date:%Y%m%d%H%M%S}.g1a2b3c4")
        self.assertEqual({path: path.read_bytes() for path in before}, before)

    def test_missing_duplicate_invalid_or_mismatched_source_version_rejected(self):
        manifest = self.root / "backend/fnos/qmediasync-arm64/manifest"
        for content in (None, "version = 1.2.2\n", "version = bad\n", "version = 1.2.3\nversion = 1.2.3\n"):
            with self.subTest(content=content):
                if content is None:
                    manifest.unlink()
                else:
                    manifest.write_text(content)
                result = self.run_metadata()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_invalid_commit_rejected(self):
        for sha in ("", "1a2b3c4", "z" * 40, "1" * 40 + "\nversion=bad"):
            with self.subTest(sha=sha):
                result = self.run_metadata(sha=sha)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
