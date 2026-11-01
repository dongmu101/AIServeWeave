"""Black-box tests of install.sh against a local HTTPS stub of the platform; service-manager acceptance runs on real target systems.
install.sh 对本地 HTTPS 平台桩的黑盒测试；服务管理器验收在真实目标系统上单独执行。
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import pty
import pwd
import subprocess
import tempfile
import unittest
import zipfile

from platform_stub import CA_PEM, CA_SHA, CODE, TOKEN, VERSION, Platform

REPO = Path(__file__).resolve().parents[3]
INSTALL = REPO / 'deploy/agent/install.sh'
AGENT_SH = REPO / 'deploy/agent/agent.sh'


class InstallScriptTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build_dir = tempfile.TemporaryDirectory(prefix='aisw-install-build-')
        build = Path(cls.build_dir.name)
        cls.binary = build / 'agent'
        subprocess.run(['go', 'build', f'-ldflags=-X main.version={VERSION}', '-o', str(cls.binary), './service/aiServeWeaveAgent'], cwd=REPO, check=True)
        cls.goos = 'darwin' if platform.system() == 'Darwin' else 'linux'
        cls.arch = 'arm64' if platform.machine() in ('arm64', 'aarch64') else 'amd64'
        cls.cert, cls.key = build / 'cert.pem', build / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=127.0.0.1',
                        '-addext', 'subjectAltName=IP:127.0.0.1', '-keyout', str(cls.key), '-out', str(cls.cert)],
                       check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.build_dir.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='aisw-install space-')
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / 'installed Agent'
        self.asset = f'aiserveweave-agent_{VERSION}_{self.goos}_{self.arch}'
        self.files = {self.asset: self.binary.read_bytes(), 'agent.sh': AGENT_SH.read_bytes()}
        self.platform = Platform(self.cert, self.key, self.files, self.goos, self.arch)
        self.addCleanup(self.platform.close)
        self.refresh_sums()

    def refresh_sums(self):
        lines = ''.join(f'{hashlib.sha256(data).hexdigest()}  {name}\n' for name, data in self.files.items())
        self.files['SHA256SUMS.txt'] = lines.encode()

    def run_install(self, args, success=True, stdin_script=False, no_tty=True):
        """Runs install.sh, optionally piped through bash -s the way the platform serves it.

        运行 install.sh；可选地像平台下发时那样通过 bash -s 管道执行。
        """
        env = dict(os.environ, CURL_CA_BUNDLE=str(self.cert))
        common = ['--stage', '--root', str(self.root), '--user', pwd.getpwuid(os.getuid()).pw_name]
        if stdin_script:
            script = INSTALL.read_text().replace('__AISW_PLATFORM_URL__', self.platform.url)
            cmd, stdin = ['bash', '-s', '--', *common, *args], script
        else:
            cmd, stdin = ['bash', str(INSTALL), '--platform', self.platform.url, *common, *args], None
        result = subprocess.run(cmd, input=stdin, text=True, capture_output=True, env=env,
                                stdin=None if stdin is not None else subprocess.DEVNULL, start_new_session=no_tty)
        output = result.stdout + result.stderr
        if success:
            self.assertEqual(result.returncode, 0, output)
        else:
            self.assertNotEqual(result.returncode, 0, output)
        for secret in (TOKEN, CODE):
            self.assertNotIn(secret, output)
        return result

    def assert_installed(self, token=TOKEN):
        self.assertTrue((self.root / 'bin/aiserveweave-agent').is_symlink())
        self.assertEqual((self.root / 'state/identity/bootstrap.token').read_text().strip(), token)
        self.assertEqual((self.root / 'state/identity/ca.crt').read_text(), CA_PEM)

    def assert_not_installed(self):
        self.assertFalse((self.root / 'bin/aiserveweave-agent').exists())

    def test_code_install_via_pipe_redeems_once_after_downloads(self):
        self.run_install(['--code', CODE], stdin_script=True)
        self.assert_installed()
        self.assertEqual([json.loads(b)['code'] for b in self.platform.redeem_bodies], [CODE])

    def test_code_install_from_file(self):
        self.run_install(['--code', CODE])
        self.assert_installed()

    def test_download_failure_does_not_redeem(self):
        self.platform.fail_download = True
        result = self.run_install(['--code', CODE], success=False)
        self.assertIn('cannot download', result.stderr)
        self.assertEqual(self.platform.redeem_bodies, [])
        self.assert_not_installed()

    def test_checksum_mismatch_does_not_redeem(self):
        for name in ('agent.sh', self.asset):
            with self.subTest(name=name):
                good = self.files[name]
                self.files[name] = good + b'\n# tampered\n'
                self.addCleanup(self.files.__setitem__, name, good)
                result = self.run_install(['--code', CODE], success=False)
                self.assertIn('SHA256 mismatch', result.stderr)
                self.assertEqual(self.platform.redeem_bodies, [])
                self.assert_not_installed()
                self.files[name] = good

    def test_refused_code_installs_nothing(self):
        for status, message in ((404, 'invalid, already used or expired'), (429, 'too many attempts'), (503, 'could not redeem')):
            with self.subTest(status=status):
                self.platform.redeem_status = status
                result = self.run_install(['--code', CODE], success=False)
                self.assertIn(message, result.stderr)
                self.assert_not_installed()

    def test_token_credential_pins_ca(self):
        args = ['--node-id', 'node-token', '--token', TOKEN, '--registry', 'registry.test:9443', '--gateway', 'gateway.test:8443']
        self.run_install([*args, '--ca-sha256', CA_SHA.upper()])
        self.assert_installed()
        self.assertEqual(self.platform.redeem_bodies, [])

    def test_token_credential_rejects_wrong_ca_fingerprint(self):
        args = ['--node-id', 'node-token', '--token', TOKEN, '--registry', 'registry.test:9443', '--gateway', 'gateway.test:8443']
        result = self.run_install([*args, '--ca-sha256', '0' * 64], success=False)
        self.assertIn('does not match the expected SHA256', result.stderr)
        self.assert_not_installed()

    def test_bundle_credential(self):
        archive = self.base / 'bundle.zip'
        connection = json.dumps({'node_id': 'node-bundle', 'registry_addr': 'registry.test:9443',
                                 'gateway_addr': 'gateway.test:8443', 'expires_at': '2030-01-01T00:00:00Z', 'ca_sha256': CA_SHA}, indent=2)
        with zipfile.ZipFile(archive, 'w') as z:
            z.writestr('ca.pem', CA_PEM)
            z.writestr('bootstrap-token', TOKEN + '\n')
            z.writestr('connection.json', connection + '\n')
            z.writestr('README.txt', 'readme')
        self.run_install(['--bundle', str(archive)])
        self.assert_installed()
        self.assertEqual(self.platform.redeem_bodies, [])

    def test_credential_modes_are_exclusive(self):
        result = self.run_install(['--code', CODE, '--bundle', str(self.base / 'x.zip')], success=False)
        self.assertIn('use only one of', result.stderr)
        self.assertEqual(self.platform.redeem_bodies, [])

    def test_without_credential_or_terminal_fails_without_redeeming(self):
        result = self.run_install([], success=False)
        self.assertIn('no terminal', result.stderr)
        self.assertEqual(self.platform.redeem_bodies, [])
        self.assert_not_installed()

    def test_code_is_prompted_on_the_terminal_without_echo(self):
        env = dict(os.environ, CURL_CA_BUNDLE=str(self.cert))
        cmd = ['bash', str(INSTALL), '--platform', self.platform.url, '--stage', '--root', str(self.root),
               '--user', pwd.getpwuid(os.getuid()).pw_name]
        pid, fd = pty.fork()
        if pid == 0:
            os.execvpe('bash', cmd, env)
        output = b''
        sent = False
        while True:
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            output += chunk
            if not sent and b'Install code' in output:
                os.write(fd, CODE.encode() + b'\n')
                sent = True
        _, status = os.waitpid(pid, 0)
        text = output.decode(errors='replace')
        self.assertEqual(os.waitstatus_to_exitcode(status), 0, text)
        self.assertNotIn(CODE, text)
        self.assertNotIn(TOKEN, text)
        self.assert_installed()

    def test_installed_node_requires_upgrade(self):
        self.run_install(['--code', CODE])
        (self.root / 'state/agent.yaml').write_text('existing configuration')
        result = self.run_install(['--code', CODE], success=False)
        self.assertIn('--upgrade', result.stderr)
        self.assertEqual(len(self.platform.redeem_bodies), 1)
        self.run_install(['--upgrade'])
        self.assertEqual(len(self.platform.redeem_bodies), 1)
        self.assertEqual((self.root / 'state/agent.yaml').read_text(), 'existing configuration')

    def test_upgrade_needs_an_installed_agent_and_no_credential(self):
        self.assertIn('no installed Agent', self.run_install(['--upgrade'], success=False).stderr)
        self.run_install(['--code', CODE])
        (self.root / 'state/agent.yaml').write_text('existing configuration')
        self.assertIn('does not take a credential', self.run_install(['--upgrade', '--code', CODE], success=False).stderr)

    def test_custom_https_source(self):
        self.run_install(['--code', CODE, '--source', self.platform.url + '/custom'])
        self.assert_installed()

    def test_dirty_and_development_platform_versions_are_refused(self):
        for version, message in ((f'{VERSION}-3-gabc1234-dirty', 'dirty tree'), ('dev', 'development build')):
            with self.subTest(version=version):
                self.platform.version = version
                result = self.run_install(['--code', CODE], success=False)
                self.assertIn(message, result.stderr)
                self.assertEqual(self.platform.redeem_bodies, [])

    def test_github_source_needs_a_release_repository(self):
        result = self.run_install(['--code', CODE, '--source', 'github'], success=False)
        self.assertIn('does not name a GitHub repository', result.stderr)
        self.assertEqual(self.platform.redeem_bodies, [])

    def test_plain_http_platform_is_refused(self):
        env = dict(os.environ, CURL_CA_BUNDLE=str(self.cert))
        result = subprocess.run(['bash', str(INSTALL), '--platform', 'http://127.0.0.1:1', '--stage', '--root', str(self.root), '--code', CODE],
                                text=True, capture_output=True, env=env, stdin=subprocess.DEVNULL)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("platform's https origin", result.stderr)


if __name__ == '__main__':
    unittest.main()
