#!/usr/bin/env python3
"""fnOS 包装脚本的隔离进程与数据保留回归，不访问真实 NAS。"""
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.parse


ROOT = Path(__file__).resolve().parents[2]
FNOS = ROOT / 'backend/fnos'
CHILD = r'''#!/usr/bin/env python3
import json, os, signal, time
from pathlib import Path
root = Path(os.environ['QMS_TEST_ROOT'])
events = root / 'events.jsonl'
starts = events.read_text().splitlines() if events.exists() else []
number = len(starts) + 1
mask = os.umask(0)
os.umask(mask)
mode = os.environ.get('QMS_TEST_MODE', 'normal')
if mode == 'media_permissions':
    directory = root / ('media.' + str(number))
    directory.mkdir(mode=0o777)
    fd = os.open(directory / 'sample.strm', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o777)
    os.write(fd, b'http://example.test/media')
    os.close(fd)
with events.open('a') as f:
    f.write(json.dumps({'pid': os.getpid(), 'args': __import__('sys').argv[1:], 'cwd': os.getcwd(), 'umask': mask}) + '\n')
request = Path(os.environ['QMS_INITIAL_CONFIG_RESTART_FILE'])
def stop(*_):
    if mode == 'stop_request':
        request.write_text(str(os.getpid()))
    if mode == 'slow_stop':
        time.sleep(.3)
        (root / 'finished').touch()
    raise SystemExit(0)
signal.signal(signal.SIGTERM, signal.SIG_IGN if mode == 'ignore_term' else stop)
if mode == 'early_exit':
    raise SystemExit(7)
while True:
    if (root / ('save.' + str(number))).exists():
        if mode == 'symlink_request':
            target = root / 'unrelated'
            target.write_text(str(os.getpid()))
            request.symlink_to(target)
        else:
            value = {'wrong_request': '1', 'empty_request': '', 'invalid_request': 'garbage'}.get(mode, str(os.getpid()))
            request.write_text(value)
        raise SystemExit(0)
    time.sleep(.02)
'''


@unittest.skipUnless(os.name == 'posix' and Path('/proc/self/stat').exists(), 'Linux process identity required')
class FnOSLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='qms fnos 测试 ')
        self.root = Path(self.tmp.name)
        self.app = self.root / 'app directory'
        self.var = self.root / 'private var'
        self.shared = self.root / '@appshare/qmediasync'
        self.etc = self.root / 'etc'
        for p in (self.app / 'scripts', self.var, self.shared, self.etc):
            p.mkdir(parents=True)
        shutil.copy(FNOS / 'common/service.sh', self.app / 'scripts/fnos-service.sh')
        (self.app / 'QMediaSync').write_text(CHILD)
        (self.app / 'QMediaSync').chmod(0o755)
        self.env = dict(os.environ, TRIM_APPDEST=str(self.app), TRIM_PKGVAR=str(self.var),
                        TRIM_PKGETC=str(self.etc), TRIM_DATA_SHARE_PATHS=str(self.shared),
                        TRIM_UID=str(os.getuid()), QMS_TEST_ROOT=str(self.root))
        self.main = FNOS / 'qmediasync-amd64/cmd/main'

    def tearDown(self):
        self.command('stop', timeout=100)
        self.tmp.cleanup()

    def command(self, action, *, timeout=10, env=None, umask=-1):
        return subprocess.run(['bash', str(self.main), action], env=env or self.env,
                              capture_output=True, text=True, timeout=timeout, umask=umask)

    def hook(self, name, *, arch='amd64', env=None):
        return subprocess.run(['bash', str(FNOS / f'qmediasync-{arch}/cmd/{name}')],
                              env=env or self.env, capture_output=True, text=True, timeout=15)

    def events(self):
        p = self.root / 'events.jsonl'
        return [json.loads(line) for line in p.read_text().splitlines()] if p.exists() else []

    def wait_for(self, condition, timeout=5):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if condition():
                return
            time.sleep(.03)
        self.fail('timed out waiting for subprocess state: ' + (self.var / 'info.log').read_text())

    def test_initial_configuration_handoff_keeps_arguments_and_starts_once(self):
        stale = self.var / '.initial-config-restart'
        stale.write_text('1')
        self.assertEqual(self.command('start').returncode, 0)
        self.assertFalse(stale.exists())
        self.assertEqual(self.command('start').returncode, 0)
        self.assertEqual(len(self.events()), 1)
        (self.root / 'save.1').touch()
        self.wait_for(lambda: len(self.events()) == 2)
        self.assertEqual(self.command('status').returncode, 0)
        for e in self.events():
            self.assertEqual(e['cwd'], str(self.app))
            self.assertEqual(e['args'], ['--guid', str(os.getuid()), '--fnos', 'true'])
        (self.root / 'save.2').touch()
        self.wait_for(lambda: self.command('status').returncode == 3)
        self.assertEqual(len(self.events()), 2, 'formal service must not become a restart loop')

    def test_media_permissions_follow_platform_umask_while_runtime_files_stay_private(self):
        self.env['QMS_TEST_MODE'] = 'media_permissions'
        for action in ('start', 'run'):
            for mask in (0o022, 0o002, 0o077):
                with self.subTest(action=action, umask=oct(mask)):
                    before = len(self.events())
                    wrapper = None
                    if action == 'run':
                        (self.var / '.service-stopping').unlink(missing_ok=True)
                        wrapper = subprocess.Popen(['bash', str(self.app / 'scripts/fnos-service.sh'), 'run'],
                                                   env=self.env, stdout=subprocess.PIPE,
                                                   stderr=subprocess.PIPE, text=True, umask=mask)
                    try:
                        if action == 'start':
                            self.assertEqual(self.command('start', umask=mask).returncode, 0)
                        self.wait_for(lambda: len(self.events()) == before + 1)
                        (self.root / f'save.{before + 1}').touch()
                        self.wait_for(lambda: len(self.events()) == before + 2)
                        self.assertEqual(self.command('status', umask=mask).returncode, 0)
                        for number in (before + 1, before + 2):
                            directory = self.root / f'media.{number}'
                            for path in (directory, directory / 'sample.strm'):
                                self.assertEqual(path.stat().st_mode & 0o777, 0o777 & ~mask)
                        for event in self.events()[before:]:
                            self.assertEqual(event['umask'], mask)
                        for name in ('service.pid', 'app.pid', '.service-control.lock', 'info.log'):
                            self.assertEqual((self.var / name).stat().st_mode & 0o077, 0, name)
                    finally:
                        self.assertEqual(self.command('stop', umask=mask).returncode, 0)
                        if wrapper is not None:
                            output = wrapper.communicate(timeout=10)
                            self.assertIn(wrapper.returncode, (0, 128 + signal.SIGTERM), output)
                    self.assertEqual((self.var / '.service-stopping').stat().st_mode & 0o077, 0)
                    self.assertEqual(self.command('status', umask=mask).returncode, 3)

    def test_concurrent_start_requests_share_one_instance(self):
        processes = [subprocess.Popen(['bash', str(self.main), 'start'], env=self.env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                     for _ in range(2)]
        for process in processes:
            output = process.communicate(timeout=10)
            self.assertEqual(process.returncode, 0, output)
        self.assertEqual(len(self.events()), 1)
        self.assertEqual(self.command('status').returncode, 0)

    def test_wrapper_signal_stops_child_and_crash_does_not_respawn(self):
        self.assertEqual(self.command('start').returncode, 0)
        wrapper = int((self.var / 'service.pid').read_text().split()[0])
        os.kill(wrapper, signal.SIGTERM)
        self.wait_for(lambda: not (self.var / 'service.pid').exists())
        self.assertEqual(self.command('status').returncode, 3)
        self.assertEqual(self.command('start').returncode, 0)
        os.kill(self.events()[-1]['pid'], signal.SIGKILL)
        self.wait_for(lambda: not (self.var / 'service.pid').exists())
        self.assertEqual(self.command('status').returncode, 3)
        self.assertEqual(len(self.events()), 2)

    def test_invalid_or_foreign_requests_do_not_restart(self):
        for mode in ('wrong_request', 'empty_request', 'invalid_request', 'symlink_request'):
            with self.subTest(mode=mode):
                self.env['QMS_TEST_MODE'] = mode
                before = len(self.events())
                self.assertEqual(self.command('start').returncode, 0)
                (self.root / f'save.{before + 1}').touch()
                self.wait_for(lambda: self.command('status').returncode == 3 and
                              not (self.var / '.initial-config-restart').exists())
                self.assertEqual(len(self.events()), before + 1)
                self.assertFalse((self.var / '.initial-config-restart').exists())
        self.assertTrue((self.root / 'unrelated').exists())

    def test_stop_wins_even_when_child_saves_during_termination(self):
        self.env['QMS_TEST_MODE'] = 'stop_request'
        self.assertEqual(self.command('start').returncode, 0)
        self.assertEqual(self.command('stop').returncode, 0)
        self.assertEqual(self.command('status').returncode, 3)
        self.assertFalse((self.var / '.initial-config-restart').exists())
        self.assertEqual(len(self.events()), 1)

    def test_stop_waits_for_child_cleanup(self):
        self.env['QMS_TEST_MODE'] = 'slow_stop'
        self.assertEqual(self.command('start').returncode, 0)
        self.assertEqual(self.command('stop').returncode, 0)
        self.assertTrue((self.root / 'finished').exists())
        self.assertEqual(self.command('status').returncode, 3)

    def test_timeout_forces_termination_without_respawn(self):
        self.env['QMS_TEST_MODE'] = 'ignore_term'
        self.assertEqual(self.command('start').returncode, 0)
        # 只加速停止命令的轮询，不改变产品等待上限或子进程行为。
        bindir = self.root / 'bin'
        bindir.mkdir()
        sleep = bindir / 'sleep'
        sleep.write_text('#!/bin/bash\nexec /bin/sleep .01\n')
        sleep.chmod(0o755)
        env = dict(self.env, PATH=str(bindir) + os.pathsep + self.env['PATH'])
        result = self.command('stop', env=env)
        self.assertIn('停止超时', result.stderr)
        self.assertEqual(self.command('status').returncode, 3)
        self.assertEqual(len(self.events()), 1)

    def test_startup_failure_and_pid_reuse_are_not_running(self):
        self.env['QMS_TEST_MODE'] = 'early_exit'
        self.assertEqual(self.command('start').returncode, 1)
        self.assertEqual(self.command('status').returncode, 3)
        del self.env['QMS_TEST_MODE']
        for name in ('service.pid', 'app.pid'):
            (self.var / name).write_text(f'{os.getpid()} 0\n')
        self.assertEqual(self.command('status').returncode, 3)
        self.assertEqual(self.command('stop').returncode, 0)
        self.assertEqual(self.command('start').returncode, 0)
        self.assertEqual(self.command('stop').returncode, 0)
        (self.app / 'QMediaSync').unlink()
        self.assertEqual(self.command('start').returncode, 1)

    def test_install_is_idempotent_and_preserves_all_instance_material(self):
        data = {'config.yaml': b'jwtSecret: keep\n', 'config.yml': b'legacy',
                'qmediasync.db': b'database', 'encryption.key': b'key', 'logs/app.log': b'log'}
        for base in (self.etc, self.shared / 'config'):
            for name, value in data.items():
                p = base / name
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_bytes(value)
        for arch in ('amd64', 'arm64'):
            for _ in range(2):
                self.assertEqual(self.hook('install_callback', arch=arch).returncode, 0)
            for base in (self.etc, self.shared / 'config'):
                for name, value in data.items():
                    self.assertEqual((base / name).read_bytes(), value)

    def test_uninstall_keeps_by_default_and_deletes_only_confirmed_config(self):
        for arch in ('amd64', 'arm64'):
            config = self.shared / 'config'
            config.mkdir(exist_ok=True)
            (config / 'encryption.key').write_text('keep')
            media = self.shared / 'media.strm'
            media.write_text('media')
            self.assertEqual(self.hook('uninstall_init', arch=arch).returncode, 0)
            self.assertTrue(config.exists())
            env = dict(self.env, wizard_delete_data='yes')
            self.assertEqual(self.hook('uninstall_init', arch=arch, env=env).returncode, 0)
            self.assertFalse(config.exists())
            self.assertTrue(media.exists())
            self.assertTrue(self.etc.exists())
            self.assertEqual(self.hook('uninstall_callback', arch=arch, env=env).returncode, 0)

    def test_uninstall_rejects_ambiguous_paths_and_symlinked_config(self):
        config = self.shared / 'config'
        config.mkdir()
        (config / 'encryption.key').touch()
        for path in ('', '/', str(self.shared) + ':' + str(self.etc), str(self.etc),
                     str(self.shared) + '/../qmediasync', str(self.shared) + '\n'):
            with self.subTest(path=path):
                env = dict(self.env, wizard_delete_data='yes', TRIM_DATA_SHARE_PATHS=path)
                self.assertEqual(self.hook('uninstall_init', env=env).returncode, 1)
                self.assertTrue(config.exists())
        shutil.rmtree(config)
        config.symlink_to(self.etc, target_is_directory=True)
        self.assertEqual(self.hook('uninstall_init', env=dict(self.env, wizard_delete_data='yes')).returncode, 1)
        self.assertTrue(self.etc.exists())

    def test_directory_callback_encodes_paths_and_reports_errors(self):
        received = []
        response = [200, {'code': 200}]

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                received.append(urllib.parse.parse_qs(self.rfile.read(int(self.headers['Content-Length'])).decode()))
                self.send_response(response[0])
                self.end_headers()
                self.wfile.write(json.dumps(response[1]).encode())

            def log_message(self, *_):
                pass

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            path = '/media/中文 A&B+%:/other'
            env = dict(self.env, TRIM_SERVICE_PORT=str(server.server_port), TRIM_DATA_ACCESSIBLE_PATHS=path)
            self.assertEqual(self.hook('config_callback', env=env).returncode, 0)
            self.assertEqual(received[-1]['path'], [path])
            response[:] = [404, {}]
            result = self.hook('config_callback', env=env)
            self.assertEqual(result.returncode, 1)
            self.assertIn('尚未完成数据库设置', result.stderr)
            response[:] = [200, {'code': 400}]
            self.assertEqual(self.hook('config_callback', env=env).returncode, 1)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
        self.assertEqual(self.hook('config_callback', env=env).returncode, 1)

    def test_templates_have_shared_identity_and_web_only_wizard(self):
        for arch, platform in (('amd64', 'x86'), ('arm64', 'arm')):
            d = FNOS / f'qmediasync-{arch}'
            manifest = dict((a.strip(), b.strip()) for a, b in
                            (line.split('=', 1) for line in (d / 'manifest').read_text().splitlines()))
            self.assertEqual(manifest['appname'], 'qmediasync')
            self.assertEqual(manifest['platform'], platform)
            for key in ('maintainer', 'distributor'):
                self.assertEqual(manifest[key], 'chen8945')
                self.assertEqual(manifest[key + '_url'], 'https://github.com/chen8945/QMediaSync')
            self.assertEqual(json.loads((d / 'app/ui/config').read_text())['.url']['qmediasync.Application']['title'], 'QMediaSync')
            for name in ('config/privilege', 'config/resource', 'wizard/uninstall'):
                json.loads((d / name).read_text())
            self.assertNotIn('"field"', (d / 'wizard/install').read_text())
            self.assertFalse((d / 'config.yaml').exists())
            for p in (d / 'cmd').iterdir():
                self.assertEqual(subprocess.run(['bash', '-n', str(p)], capture_output=True).returncode, 0)


if __name__ == '__main__':
    unittest.main()
