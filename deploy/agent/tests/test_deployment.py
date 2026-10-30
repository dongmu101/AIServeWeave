"""Black-box staging tests; service-manager acceptance runs on real target systems.
黑盒暂存测试；服务管理器验收在真实目标系统上单独执行。
"""
import hashlib
import os
from pathlib import Path
import platform
import pwd
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[3]
SCRIPT = REPO / 'deploy/agent/agent.sh'

class DeploymentTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build_dir = tempfile.TemporaryDirectory(prefix='aisw-script-build-')
        cls.binary = Path(cls.build_dir.name) / 'agent'
        subprocess.run(['go', 'build', '-ldflags=-X main.version=v0.99.1', '-o', str(cls.binary), './service/aiServeWeaveAgent'], cwd=REPO, check=True)
        cls.goos = 'darwin' if platform.system() == 'Darwin' else 'linux'
        cls.arch = 'arm64' if platform.machine() in ('arm64', 'aarch64') else 'amd64'
    @classmethod
    def tearDownClass(cls):
        cls.build_dir.cleanup()
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='aisw-deploy space-')
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / 'installed Agent'
        self.ca = self.base / 'ca.crt';self.ca.write_text('fixture-ca')
        self.token = self.base / 'token';self.token.write_text('secret-fixture-token')
        self.manifest = self.base / 'SHA256SUMS.txt'
        self.asset = f'aiserveweave-agent_v0.99.1_{self.goos}_{self.arch}'
        self.write_manifest(self.binary)
    def write_manifest(self, binary):
        self.manifest.write_text(f'{hashlib.sha256(binary.read_bytes()).hexdigest()}  {self.asset}\n'+'0'*64+'  unrelated_asset\n')
    def run_script(self, action, extra=(), success=True):
        args = ['bash', str(SCRIPT), action, '--stage', '--root', str(self.root), '--user', pwd.getpwuid(os.getuid()).pw_name]
        args += list(extra)
        result = subprocess.run(args, text=True, capture_output=True)
        if success: self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
        else: self.assertNotEqual(result.returncode, 0, result.stdout+result.stderr)
        self.assertNotIn('secret-fixture-token',result.stdout+result.stderr)
        return result
    def install(self):
        self.run_script('install', ['--version','v0.99.1','--file',str(self.binary),'--checksums',str(self.manifest),'--node-id','node-one','--registry','registry.test:9090','--gateway','gateway.test:9090','--ca-file',str(self.ca),'--token-file',str(self.token)])
        # Stage intentionally leaves YAML initialization to the target executable.
        # 暂存有意把 YAML 初始化留给目标机上的可执行程序。
        (self.root/'state/agent.yaml').write_text('existing configuration')
    def test_upgrade_verification_failure_preserves_active_version(self):
        self.install()
        entry = self.root/'bin/aiserveweave-agent'
        before = entry.resolve()
        self.manifest.write_text('0'*64+f'  {self.asset}\n')
        self.run_script('upgrade',['--version','v0.99.1','--file',str(self.binary),'--checksums',str(self.manifest)],success=False)
        self.assertEqual(entry.resolve(),before)
        self.assertEqual((self.root/'state/agent.yaml').read_text(),'existing configuration')
    def test_repeat_rollback_and_uninstall_preserve_identity(self):
        self.install()
        identity = self.root/'state/identity/node.key';identity.write_text('node-identity')
        args=['--version','v0.99.1','--file',str(self.binary),'--checksums',str(self.manifest)]
        self.run_script('install',args)
        self.run_script('rollback',['--version','v0.99.1'])
        self.run_script('uninstall')
        self.assertFalse((self.root/'bin/aiserveweave-agent').exists())
        self.assertEqual(identity.read_text(),'node-identity')
        self.assertEqual((self.root/'state/agent.yaml').read_text(),'existing configuration')
    def test_wrong_binary_format_is_rejected(self):
        bad=self.base/'wrong.exe';bad.write_bytes(b'MZ-not-a-linux-or-mac-binary')
        self.write_manifest(bad)
        self.run_script('install',['--version','v0.99.1','--file',str(bad),'--checksums',str(self.manifest)],success=False)
        self.assertFalse((self.root/'bin/aiserveweave-agent').exists())
    def test_duplicate_manifest_is_rejected(self):
        self.manifest.write_text(self.manifest.read_text()*2)
        self.run_script('install',['--version','v0.99.1','--file',str(self.binary),'--checksums',str(self.manifest)],success=False)
        self.assertFalse((self.root/'bin/aiserveweave-agent').exists())

if __name__ == '__main__': unittest.main()
