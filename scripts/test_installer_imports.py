"""Installer launch paths must not execute unsigned Python inputs."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


class InstallerImports(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bundle = self.root / 'release'
        self.bundle.mkdir(mode=0o700)
        for name in ('agentctl.py', 'selfhostctl.py'):
            shutil.copyfile(SCRIPTS / name, self.bundle / name)
        self.marker = self.root / 'unsigned-code-executed'
        self.payload = f"open({str(self.marker)!r}, 'w').write('unsigned input')\n"

    def launch(self, script, isolated=True, **kwargs):
        return subprocess.run([sys.executable, *(['-I'] if isolated else []),
                               str(self.bundle / script), '--help'],
                              cwd=self.bundle, capture_output=True, timeout=10, **kwargs)

    def test_isolated_installers_ignore_siblings_cwd_and_pythonpath(self):
        for name in ('argparse.py', 'platform.py', 'sitecustomize.py'):
            (self.bundle / name).write_text(self.payload)
        environment = {**os.environ, 'PYTHONPATH': str(self.bundle)}
        for script in ('agentctl.py', 'selfhostctl.py'):
            with self.subTest(script=script):
                result = self.launch(script, env=environment)
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                self.assertIn(b'usage:', result.stdout)
                self.assertFalse(self.marker.exists(), 'unsigned Python input executed')

    def test_direct_nonisolated_invocation_fails_before_sibling_imports(self):
        (self.bundle / 'argparse.py').write_text(self.payload)
        environment = {k: v for k, v in os.environ.items() if not k.startswith('PYTHON')}
        for script in ('agentctl.py', 'selfhostctl.py'):
            with self.subTest(script=script):
                result = self.launch(script, isolated=False, env=environment)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.marker.exists(), 'guard ran after an unsigned import')
                self.assertIn(b'python3 -I', result.stderr)

    def test_modified_helper_is_not_executed(self):
        helper = self.bundle / 'agentctl.py'
        helper.write_text(self.payload + helper.read_text())
        result = self.launch('selfhostctl.py')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.marker.exists(), 'unverified helper executed')


if __name__ == '__main__':
    unittest.main()
