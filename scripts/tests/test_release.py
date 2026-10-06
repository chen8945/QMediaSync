"""用临时仓库与 git-cliff 替身验证发布脚本的交互、回滚、dry-run 与沙箱模拟；不操作真实仓库与远端。"""

import os
import pty
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPTS_SRC = REPO_ROOT / "scripts" / "release"

CLIFF_STUB = r"""#!/usr/bin/env bash
# 测试替身：忽略 cliff.toml，输出确定性的发布说明段落
out=/dev/stdout
tag=unknown
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    --tag) tag="${args[$((i + 1))]}"; i=$((i + 1)) ;;
    -o) out="${args[$((i + 1))]}"; i=$((i + 1)) ;;
  esac
done
{
  echo "## [${tag}] - 2026-01-01"
  echo
  echo "### 新功能"
  echo "- Add app"
  echo
  echo "### 修复"
  echo "- Crash"
} > "$out"
"""

EDITOR_STUB = r"""#!/usr/bin/env bash
# 测试替身：在发布说明末尾追加一行，模拟用户手动编辑
printf '%s\n' '- 手动补充说明' >> "$1"
"""

GIT_EDITOR_STUB = r"""#!/usr/bin/env bash
# 测试替身：模拟 core.editor / GIT_EDITOR 指向的编辑器，追加可区分的标记行
printf '%s\n' '- 来自git配置编辑器' >> "$1"
"""

REJECT_TAG_HOOK = r"""#!/usr/bin/env bash
# 测试替身：拒绝 tag 推送，模拟远端竞态导致的失败
while read -r _old _new ref; do
  case "$ref" in
    refs/tags/*) echo "拒绝 tag 推送" >&2; exit 1 ;;
  esac
done
exit 0
"""


class ReleaseFixture:
    """一个 bare 远端 + 工作克隆的发布测试环境，main 带 v0.1.0，dev 领先两个提交。"""

    def __init__(self, root: Path):
        self.root = root
        self.origin = root / "origin.git"
        self.work = root / "work"
        self.bin = root / "bin"

    @classmethod
    def create(cls, root: Path) -> "ReleaseFixture":
        fx = cls(root)
        fx.bin.mkdir()
        (fx.bin / "git-cliff").write_text(CLIFF_STUB)
        (fx.bin / "git-cliff").chmod(0o755)

        fx._git("init", "--bare", "-q", str(fx.origin), cwd=root)
        fx._git("clone", "-q", str(fx.origin), str(fx.work), cwd=root)
        fx.git("config", "user.email", "release-test@example.com")
        fx.git("config", "user.name", "Release Test")
        fx.git("config", "commit.gpgsign", "false")

        (fx.work / "README.md").write_text("fixture\n")
        fx.git("add", "README.md")
        # 真实仓库在首个发布时已有 CHANGELOG.md 与发布脚本，fixture 保持一致
        (fx.work / "CHANGELOG.md").write_text("# Changelog\n\n历史内容\n")
        fx.git("add", "CHANGELOG.md")
        scripts_dir = fx.work / "scripts" / "release"
        scripts_dir.mkdir(parents=True)
        for name in ("release.sh", "lib.sh", "gen-changelog.sh"):
            shutil.copy2(SCRIPTS_SRC / name, scripts_dir / name)
        fx.git("add", "scripts")
        fx.git("commit", "-qm", "chore: fixture init")
        fx.git("branch", "-M", "main")
        fx.git("tag", "v0.1.0")
        fx.git("push", "-q", "-u", "origin", "main")
        fx.git("push", "-q", "origin", "v0.1.0")

        fx.git("checkout", "-q", "-b", "dev")
        (fx.work / "app.txt").write_text("feature\n")
        fx.git("add", "app.txt")
        fx.git("commit", "-qm", "feat: add app")
        (fx.work / "bug.txt").write_text("bugfix\n")
        fx.git("add", "bug.txt")
        fx.git("commit", "-qm", "fix: crash")
        fx.git("push", "-q", "-u", "origin", "dev")
        return fx

    def _git(self, *args, cwd):
        subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)

    def git(self, *args, check=True):
        return subprocess.run(
            ["git", *args], cwd=self.work, check=check, capture_output=True, text=True
        )

    def commit_file(self, name: str, content: str, message: str):
        (self.work / name).write_text(content)
        self.git("add", name)
        self.git("commit", "-qm", message)

    def run_release(self, args, stdin="", editor=None):
        env = dict(os.environ)
        env["PATH"] = f"{self.bin}{os.pathsep}{env['PATH']}"
        env.pop("VISUAL", None)
        env.pop("GIT_EDITOR", None)
        if editor is None:
            env.pop("EDITOR", None)
        else:
            env["EDITOR"] = editor
        return subprocess.run(
            ["bash", "scripts/release/release.sh", *args],
            cwd=self.work,
            input=stdin,
            capture_output=True,
            text=True,
            env=env,
            timeout=120,
        )

    def run_in_pty(self, args, stdin_data=b"", env_extra=None):
        """在伪终端下运行发布脚本，用于验证颜色开关；返回 (退出码, 合并输出字节)。"""
        master, slave = pty.openpty()
        env = dict(os.environ)
        env["PATH"] = f"{self.bin}{os.pathsep}{env['PATH']}"
        env.pop("VISUAL", None)
        env.pop("EDITOR", None)
        env.pop("GIT_EDITOR", None)
        if env_extra:
            env.update(env_extra)
        proc = subprocess.Popen(
            ["bash", "scripts/release/release.sh", *args],
            cwd=self.work,
            stdin=subprocess.PIPE,
            stdout=slave,
            stderr=slave,
            env=env,
        )
        os.close(slave)
        proc.stdin.write(stdin_data)
        proc.stdin.close()
        output = b""
        try:
            while True:
                chunk = os.read(master, 65536)
                if not chunk:
                    break
                output += chunk
        except OSError:
            pass
        os.close(master)
        proc.wait(timeout=60)
        return proc.returncode, output

    def local_tags(self) -> set:
        return set(self.git("tag", "-l").stdout.split())

    def remote_tags(self) -> set:
        out = self.git("ls-remote", "--tags", "origin").stdout
        return {
            line.split()[-1].rsplit("/", 1)[-1]
            for line in out.splitlines()
            if line.strip() and not line.endswith("^{}")
        }

    def status(self) -> str:
        return self.git("status", "--porcelain").stdout

    def current_branch(self) -> str:
        return self.git("branch", "--show-current").stdout.strip()

    def reject_tag_pushes(self):
        hook = self.origin / "hooks" / "pre-receive"
        hook.write_text(REJECT_TAG_HOOK)
        hook.chmod(0o755)


class ReleaseScriptTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="qms-release-test-")
        self.fx = ReleaseFixture.create(Path(self._tmp.name))

    def tearDown(self):
        self._tmp.cleanup()

    def test_help(self):
        result = self.fx.run_release(["-h"])
        self.assertEqual(result.returncode, 0)
        self.assertIn("用法:", result.stdout)

    def test_unknown_option(self):
        result = self.fx.run_release(["--bogus", "v0.1.1"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("未知选项", result.stderr)

    def test_dry_run_and_simulate_are_mutually_exclusive(self):
        result = self.fx.run_release(["--dry-run", "--simulate", "v0.1.1"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("不能同时使用", result.stderr)

    def test_rejects_non_increasing_tag(self):
        result = self.fx.run_release(["v0.1.0"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("必须大于当前最新版本", result.stderr)

    def test_rejects_invalid_tag_format(self):
        result = self.fx.run_release(["0.1.1"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("tag 格式必须是", result.stderr)

    def test_dry_run_explicit_tag_needs_no_interaction_and_changes_nothing(self):
        result = self.fx.run_release(["--dry-run", "v0.1.1"])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("## [v0.1.1]", result.stdout)
        self.assertIn("dry-run 结束", result.stdout)
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.current_branch(), "dev")
        self.assertEqual(self.fx.local_tags(), {"v0.1.0"})
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0"})
        self.assertFalse((self.fx.work / ".changes").exists())
        self.assertNotIn("v0.1.1", (self.fx.work / "CHANGELOG.md").read_text())

    def test_dry_run_derived_tag_with_confirmation(self):
        result = self.fx.run_release(["--dry-run", "patch"], stdin="\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("推导发布版本: v0.1.1", result.stdout)
        self.assertIn("## [v0.1.1]", result.stdout)
        self.assertEqual(self.fx.status(), "")

    def test_dry_run_reports_non_fast_forward(self):
        self.fx.git("checkout", "-q", "main")
        self.fx.commit_file("mainonly.txt", "x\n", "feat: main only")
        self.fx.git("checkout", "-q", "dev")
        result = self.fx.run_release(["--dry-run", "v0.1.1"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("无法快进合并", result.stderr)

    def test_full_release_end_to_end(self):
        result = self.fx.run_release(["patch"], stdin="\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("发布流程已提交并触发", result.stdout)

        changelog = (self.fx.work / "CHANGELOG.md").read_text()
        self.assertEqual(changelog.count("## [v0.1.1]"), 1)
        notes = (self.fx.work / ".changes" / "v0.1.1.md").read_text()
        self.assertIn("- Add app", notes)
        committed_changelog = self.fx.git("show", "HEAD:CHANGELOG.md").stdout
        self.assertIn("## [v0.1.1]", committed_changelog)

        release_commit = self.fx.git("rev-parse", "main").stdout.strip()
        self.assertIn("chore: release v0.1.1", self.fx.git("log", "-1", "--format=%s", "main").stdout)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})
        self.assertEqual(
            self.fx.git("rev-parse", "dev").stdout.strip(), release_commit
        )
        origin_dev = self.fx.git("rev-parse", "origin/dev").stdout.strip()
        self.assertEqual(origin_dev, release_commit)
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.current_branch(), "dev")

    def test_invalid_override_retries_then_accepts(self):
        result = self.fx.run_release(["patch"], stdin="banana\n\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("无效输入: banana", result.stdout)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_invalid_typo_at_confirm_retries(self):
        result = self.fx.run_release(["v0.1.1"], stdin="\nyesplease\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("无效输入: yesplease", result.stdout)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_edit_notes_syncs_changelog(self):
        editor_stub = self.fx.bin / "editor-stub"
        editor_stub.write_text(EDITOR_STUB)
        editor_stub.chmod(0o755)
        result = self.fx.run_release(["patch"], stdin="\ne\n\nyes\n", editor=str(editor_stub))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

        changelog = (self.fx.work / "CHANGELOG.md").read_text()
        self.assertEqual(changelog.count("## [v0.1.1]"), 1)
        self.assertIn("- 手动补充说明", changelog)
        self.assertIn(
            "- 手动补充说明",
            self.fx.git("show", "HEAD:.changes/v0.1.1.md").stdout,
        )
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_edit_falls_back_to_git_core_editor(self):
        # 无 VISUAL / EDITOR 时应使用 git core.editor
        git_stub = self.fx.bin / "git-editor-stub"
        git_stub.write_text(GIT_EDITOR_STUB)
        git_stub.chmod(0o755)
        self.fx.git("config", "core.editor", str(git_stub))
        result = self.fx.run_release(["patch"], stdin="\ne\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        changelog = (self.fx.work / "CHANGELOG.md").read_text()
        self.assertIn("- 来自git配置编辑器", changelog)
        self.assertNotIn("- 手动补充说明", changelog)
        self.assertEqual(changelog.count("## [v0.1.1]"), 1)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_editor_env_wins_over_git_core_editor(self):
        env_stub = self.fx.bin / "editor-stub"
        env_stub.write_text(EDITOR_STUB)
        env_stub.chmod(0o755)
        git_stub = self.fx.bin / "git-editor-stub"
        git_stub.write_text(GIT_EDITOR_STUB)
        git_stub.chmod(0o755)
        self.fx.git("config", "core.editor", str(git_stub))
        result = self.fx.run_release(["patch"], stdin="\ne\n\nyes\n", editor=str(env_stub))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        changelog = (self.fx.work / "CHANGELOG.md").read_text()
        self.assertIn("- 手动补充说明", changelog)
        self.assertNotIn("- 来自git配置编辑器", changelog)

    def test_back_from_confirm_returns_to_review(self):
        result = self.fx.run_release(["v0.1.1"], stdin="\nb\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout.count("将执行以下外部动作"), 2)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_back_to_version_reselect_regenerates_once(self):
        result = self.fx.run_release(["patch"], stdin="\nv\n\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("返回重选版本", result.stdout)
        changelog = (self.fx.work / "CHANGELOG.md").read_text()
        self.assertEqual(changelog.count("## [v0.1.1]"), 1)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.1.1"})

    def test_abort_restores_worktree_and_branch(self):
        result = self.fx.run_release(["patch"], stdin="\nq\n")
        self.assertEqual(result.returncode, 1)
        self.assertIn("已中止发布", result.stdout)
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.current_branch(), "dev")
        self.assertFalse((self.fx.work / ".changes" / "v0.1.1.md").exists())
        self.assertNotIn("## [v0.1.1]", (self.fx.work / "CHANGELOG.md").read_text())
        self.assertEqual(self.fx.local_tags(), {"v0.1.0"})
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0"})

    def test_abort_keeps_edited_notes(self):
        editor_stub = self.fx.bin / "editor-stub"
        editor_stub.write_text(EDITOR_STUB)
        editor_stub.chmod(0o755)
        result = self.fx.run_release(["patch"], stdin="\ne\nq\n", editor=str(editor_stub))
        self.assertEqual(result.returncode, 1)
        notes = self.fx.work / ".changes" / "v0.1.1.md"
        self.assertTrue(notes.exists())
        self.assertIn("- 手动补充说明", notes.read_text())
        self.assertIn("保留在 .changes/v0.1.1.md", result.stdout)
        # CHANGELOG 还原为干净状态，仅剩未跟踪的发布说明
        self.assertNotIn("## [v0.1.1]", (self.fx.work / "CHANGELOG.md").read_text())
        self.assertEqual(self.fx.status(), "?? .changes/\n")
        self.assertEqual(self.fx.current_branch(), "dev")

    def test_eof_aborts_safely(self):
        result = self.fx.run_release(["patch"], stdin="")
        self.assertEqual(result.returncode, 1)
        self.assertIn("输入流已关闭", result.stdout)
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.current_branch(), "dev")

    def test_colors_enabled_on_tty(self):
        rc, output = self.fx.run_in_pty(["patch"], b"\nq\n")
        self.assertEqual(rc, 1)
        self.assertIn(b"\x1b[1;36m", output)  # 粗体青：阶段标题与输入提示
        self.assertIn(b"\x1b[33m", output)    # 黄色：警示与回滚提示
        self.assertIn(b"\x1b[0m", output)     # 颜色复位
        self.assertNotIn(b"\x1b[1;31m", output)  # 中止流程不出现错误红

    def test_no_color_disables_ansi_on_tty(self):
        rc, output = self.fx.run_in_pty(["patch"], b"\nq\n", env_extra={"NO_COLOR": "1"})
        self.assertEqual(rc, 1)
        self.assertNotIn(b"\x1b[1;36m", output)
        self.assertNotIn(b"\x1b[33m", output)
        self.assertIn("已中止发布".encode(), output)

    def test_minor_bump_warning_at_confirm(self):
        result = self.fx.run_release(["v0.2.0"], stdin="\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("⚠ 检测到 minor 版本发布: v0.1.0 -> v0.2.0", result.stdout)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0", "v0.2.0"})

    def test_push_failure_reports_state(self):
        self.fx.reject_tag_pushes()
        result = self.fx.run_release(["patch"], stdin="\n\nyes\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("发布流程在上述步骤失败", result.stdout)
        self.assertIn("tag v0.1.1: 已创建但未推送", result.stdout)
        self.assertIn("git push origin v0.1.1", result.stdout)
        # main 已推送、tag 已创建但未推送
        self.assertIn("v0.1.1", self.fx.git("tag", "-l").stdout)
        self.assertIn("chore: release v0.1.1", self.fx.git("log", "-1", "--format=%s", "origin/main").stdout)
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0"})

    def test_simulate_runs_in_sandbox_without_touching_real_repo(self):
        # 末尾输入 k 保留沙箱，便于断言沙箱内产物
        result = self.fx.run_release(["--simulate", "patch"], stdin="\n\nyes\nk\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("== 模拟完成 ==", result.stdout)
        self.assertIn("已保留沙箱", result.stdout)
        match = re.search(r"沙箱目录: (\S+)", result.stdout)
        self.assertIsNotNone(match, result.stdout)
        sandbox = Path(match.group(1))
        try:
            self.assertIn("## [v0.1.1]", (sandbox / "work" / "CHANGELOG.md").read_text())
            self.assertTrue((sandbox / "work" / ".changes" / "v0.1.1.md").exists())
        finally:
            shutil.rmtree(sandbox, ignore_errors=True)

        # 真实仓库与远端零变更
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.current_branch(), "dev")
        self.assertEqual(self.fx.local_tags(), {"v0.1.0"})
        self.assertEqual(self.fx.remote_tags(), {"v0.1.0"})
        self.assertNotIn("v0.1.1", (self.fx.work / "CHANGELOG.md").read_text())

    def test_simulate_deletes_sandbox_by_default_on_eof(self):
        # 流程输入耗尽后，删除询问收到 EOF，应按回车默认删除
        result = self.fx.run_release(["--simulate", "patch"], stdin="\n\nyes\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("已删除临时沙箱", result.stdout)
        match = re.search(r"沙箱目录: (\S+)", result.stdout)
        self.assertIsNotNone(match, result.stdout)
        self.assertFalse(Path(match.group(1)).exists())
        # 真实仓库零变更
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.local_tags(), {"v0.1.0"})

    def test_simulate_abort_keeps_real_repo_clean(self):
        result = self.fx.run_release(["--simulate", "patch"], stdin="\nq\n")
        self.assertEqual(result.returncode, 1)
        self.assertIn("== 模拟结束", result.stdout)
        self.assertIn("已删除临时沙箱", result.stdout)
        self.assertEqual(self.fx.status(), "")
        self.assertEqual(self.fx.local_tags(), {"v0.1.0"})
        match = re.search(r"沙箱目录: (\S+)", result.stdout)
        self.assertIsNotNone(match, result.stdout)
        self.assertFalse(Path(match.group(1)).exists())


if __name__ == "__main__":
    unittest.main()
