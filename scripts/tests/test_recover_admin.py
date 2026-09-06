"""通过 Docker 命令替身验证容器选择、状态恢复和凭据交付；不连接真实部署。"""

import fcntl
import json
import os
from pathlib import Path
import pty
import subprocess
import tempfile
import termios
import textwrap
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "recover-admin.sh"
PASSWORD = "new-random-password-123"
DOCKER = r'''#!/usr/bin/env python3
import io, json, os, pathlib, signal, sys, tarfile
root = pathlib.Path(os.environ["QMS_FAKE_DOCKER_DIR"])
state_path = root / "state.json"
state = json.loads(state_path.read_text())
args = sys.argv[1:]
with (root / "calls.jsonl").open("a") as out:
    out.write(json.dumps(args) + "\n")
def save():
    state_path.write_text(json.dumps(state))
def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)
if args[0] == "compose":
    i = 1
    files = []
    while args[i] in ("-f", "--file", "-p", "--project-name", "--env-file"):
        if args[i] in ("-f", "--file"):
            files.append(args[i + 1])
        i += 2
    command, options = args[i], args[i + 1:]
    if command == "config":
        if "--services" in options:
            print("\n".join(state.get("services", ["qmediasync", "postgres"] + (["qms"] if state.get("ambiguous") else []))))
        elif "--images" in options:
            print(state.get("image", "ghcr.io/chen8945/qmediasync:test") if options[-1] != "postgres" else "postgres:15-alpine")
        elif "--hash" in options:
            if "-" in files:
                assert sys.stdin.read().strip() == "normalized-compose"
            digest = "unresolved-env-hash" if state.get("env_file") and "-" not in files else "expected-hash"
            print(options[-1] + " " + digest)
        elif "--quiet" in options:
            override = pathlib.Path(files[-1]).read_text()
            assert "logging: !override {driver: none}" in override
            assert "target: /app/config" in override
            assert "external: true" in override if state.get("volume") else "create_host_path: false" in override
            if state.get("changed_network"):
                assert 'name: "original-db-network"' in override
            if state.get("bind_options"):
                assert 'propagation: rslave' in override and 'selinux: Z' in override
            state["override"] = override
            save()
        elif not options:
            print("normalized-compose")
    elif command == "ps":
        container = "original-container" if options[-1] == state.get("target_service", "qmediasync") else options[-1] + "-container"
        print("" if state.get("no_containers") else container + ("\nsecond-container" if state.get("replicas") else ""))
    elif command == "run":
        assert state["running"] is False
        assert options[options.index("--entrypoint") + 1] == "/app/QMediaSync"
        assert options[options.index("--entrypoint") + 2] == state.get("target_service", "qmediasync")
        assert options[options.index("--user") + 1] == "1001:2002"
        assert "--rm" in options and "--no-deps" in options
        assert options[options.index("--pull") + 1] == "never"
        if state.get("run_failure"):
            fail("模拟恢复事务失败")
        state["recovered"] = True
        save()
        if "--delete-admin" in options:
            assert "--yes" in options
            print("管理员已删除，全部 API Key 已清理")
        else:
            print("用户名：admin\n新密码：new-random-password-123")
    else:
        fail("unexpected compose command: " + command)
elif args[0] == "inspect":
    template = args[2]
    if template == "{{.Name}} / {{.State.Status}}":
        print("/" + args[-1] + " / " + ("running" if state["running"] else "exited"))
        sys.exit(0)
    assert args[-1] == "original-container"
    if template == "{{.State.Status}}":
        print("running" if state["running"] else "exited")
    elif template == "{{.State.Running}}":
        print("true" if state["running"] else "false")
    elif "config-hash" in template:
        print("changed-hash" if state.get("config_mismatch") else "expected-hash")
    elif template == "{{.Config.Image}}":
        print("ghcr.io/chen8945/qmediasync:test")
    elif template == "{{.Image}}":
        print("image-id")
    elif ".HostConfig.Mounts" in template:
        print("other-instance/config" if state.get("subpath") else "")
    elif template == "{{.HostConfig.NetworkMode}}":
        print("test-project_default")
    elif ".NetworkSettings.Networks" in template:
        print(json.dumps("original-db-network"))
    elif ".Mounts" in template:
        if ".Type" in template and "json" not in template:
            print("volume" if state.get("volume") else "bind")
        elif ".RW" in template:
            print("false" if state.get("readonly") else "true")
        elif ".Propagation" in template:
            print("rslave" if state.get("bind_options") else "rprivate")
        elif ".Mode" in template:
            print("rw,Z" if state.get("bind_options") else "rw")
        else:
            print(json.dumps("existing-volume" if state.get("volume") else "/original config/$literal"))
    elif ".Config.Env" in template:
        print("1001")
    elif template == "{{.Config.User}}":
        print("")
    elif "com.docker.compose.project" in template:
        print("test-project")
    else:
        fail("unexpected inspect template: " + template)
elif args[:2] == ["image", "inspect"]:
    print("new-image-id" if state.get("image_mismatch") else "image-id")
elif args[0] == "diff":
    print("C /app/QMediaSync" if state.get("updated_binary") else "")
elif args[0] == "cp":
    content = b"root:x:0:0:root:/root:/bin/sh\n1001:x:1001:2002::/home/1001:/bin/sh\n"
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w") as archive:
        entry = tarfile.TarInfo("passwd")
        entry.size = len(content)
        archive.addfile(entry, io.BytesIO(content))
    sys.stdout.buffer.write(data.getvalue())
elif args[0] == "stop":
    assert args[1:] == ["original-container"]
    state["running"] = False
    save()
elif args[0] == "start":
    assert args[1:] == ["original-container"]
    if state.get("restart_signal"):
        os.killpg(os.getpgrp(), getattr(signal, state["restart_signal"]))
    if state.get("start_failure"):
        fail("模拟启动失败")
    state["running"] = True
    save()
else:
    fail("unexpected docker command: " + str(args))
'''


class RecoverAdminScriptTest(unittest.TestCase):
    def test_recovery_contract(self):
        cases = [
            ("running", {}, [], True),
            ("stopped", {"running": False}, [], True),
            ("named volume", {"volume": True}, [], True),
            ("volume subpath", {"volume": True, "subpath": True}, [], False),
            ("service env file", {"env_file": True}, [], True),
            ("changed network mapping", {"changed_network": True}, [], True),
            ("bind options", {"bind_options": True}, [], True),
            ("restart failure keeps password", {"start_failure": True}, [], False),
            ("interrupt during restart", {"restart_signal": "SIGINT"}, [], True),
            ("termination during restart", {"restart_signal": "SIGTERM"}, [], True),
            ("transaction failure restores service", {"run_failure": True}, [], False),
            ("config mismatch", {"config_mismatch": True}, [], False),
            ("image mismatch", {"image_mismatch": True}, [], False),
            ("updated binary", {"updated_binary": True}, [], False),
            ("readonly data", {"readonly": True}, [], False),
            ("ambiguous services", {"ambiguous": True}, [], False),
            ("explicit service", {"ambiguous": True}, ["--service", "qmediasync"], True),
            ("select ambiguous service", {"ambiguous": True, "target_service": "qms", "terminal_input": "3\n"}, [], True),
            ("custom image without terminal", {"services": ["postgres", "media"], "image": "local/media:test"}, [], False),
            ("select custom image through pipe", {"services": ["postgres", "media"], "image": "local/media:test", "target_service": "media", "terminal_input": "2\n", "pipe": True}, [], True),
            ("invalid selections retry", {"ambiguous": True, "terminal_input": "abc\n0\n99\n1\n"}, [], True),
            ("selection cancelled", {"ambiguous": True, "terminal_input": "\x04"}, [], False),
            ("no deployed containers", {"ambiguous": True, "no_containers": True, "terminal_input": "1\n"}, [], False),
            ("explicit custom service", {"services": ["postgres", "media"], "image": "local/media:test", "target_service": "media"}, ["--service", "media"], True),
            ("multiple containers", {"replicas": True}, [], False),
            ("delete confirmed", {}, ["--action", "delete-admin", "--yes"], True),
            ("delete confirmed through pipe", {"terminal_input": "DELETE\n", "pipe": True}, ["--action", "delete-admin"], True),
            ("delete unconfirmed", {}, ["--action", "delete-admin"], False),
            ("deployment context", {}, ["-f", "custom compose.yaml", "-f", "override.yaml", "-p", "test-project", "--env-file", "deployment.env"], True),
        ]
        for name, overrides, extra_args, success in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                state = {"running": True, **overrides}
                (root / "state.json").write_text(json.dumps(state))
                (root / "compose.yaml").write_text("services: {}\n")
                (root / "compose.override.yaml").write_text("services: {}\n")
                docker = root / "docker"
                docker.write_text(textwrap.dedent(DOCKER))
                docker.chmod(0o755)
                env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "QMS_FAKE_DOCKER_DIR": str(root)}
                command = ["bash", "-x", str(SCRIPT), "--action", "reset-password", *extra_args]
                if state.get("pipe"):
                    command = ["bash", "-c", 'cat "$1" | bash -sx -- "${@:2}"', "bash", *command[2:]]
                input_options = {"input": ""}
                if "terminal_input" in state:
                    master, slave = pty.openpty()
                    os.write(master, state["terminal_input"].encode())
                    input_options = {"stdin": slave, "preexec_fn": lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0)}
                try:
                    completed = subprocess.run(
                        command, cwd=root, env=env, capture_output=True, text=True, timeout=20,
                        start_new_session=True, **input_options,
                    )
                finally:
                    if "terminal_input" in state:
                        os.close(master)
                        os.close(slave)
                self.assertEqual(completed.returncode == 0, success, completed.stderr)
                final = json.loads((root / "state.json").read_text())
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                mutations = [call for call in calls if call[0] in ("stop", "start") or "run" in call]
                recovered = final.get("recovered", False)
                self.assertNotIn(PASSWORD, completed.stderr)
                self.assertNotIn(PASSWORD, (root / "calls.jsonl").read_text())
                if recovered and "delete-admin" not in extra_args:
                    self.assertIn(PASSWORD, completed.stdout)
                else:
                    self.assertNotIn(PASSWORD, completed.stdout)
                if not success and name not in ("restart failure keeps password", "transaction failure restores service"):
                    self.assertEqual(mutations, [], "校验失败前不应停服或恢复")
                if name == "restart failure keeps password":
                    self.assertFalse(final["running"])
                    self.assertIn("启动失败", completed.stderr)
                else:
                    self.assertEqual(final["running"], state["running"])
                if name == "stopped":
                    self.assertFalse(any(call[0] in ("stop", "start") for call in calls))
                compose_calls = [call for call in calls if call[0] == "compose" and call[1:3] != ["-f", "-"]]
                if name == "deployment context":
                    for call in compose_calls:
                        for value in ("custom compose.yaml", "override.yaml", "test-project", "deployment.env"):
                            self.assertIn(value, call)
                else:
                    for call in compose_calls:
                        self.assertIn("compose.yaml", call)
                        self.assertIn("compose.override.yaml", call)
                if recovered and not state.get("volume"):
                    self.assertIn("$$literal", final["override"])
                if name in ("select ambiguous service", "select custom image through pipe", "invalid selections retry"):
                    self.assertIn("original-container", completed.stderr)
                    self.assertIn("postgres-container", completed.stderr)
                if name == "invalid selections retry":
                    self.assertIn("无效", completed.stderr)


if __name__ == "__main__":
    unittest.main()
