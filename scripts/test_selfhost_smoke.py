import hashlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import selfhost_smoke as smoke


class NativeQualificationBoundary(unittest.TestCase):
    def test_refuses_non_ci_host_before_docker_or_filesystem_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / 'installation'
            with patch('sys.argv', ['selfhost_smoke.py', '--native-agent', '--work', str(work)]), \
                    patch.object(smoke.platform, 'system', return_value='Linux'), \
                    patch.dict(os.environ, {'GITHUB_ACTIONS': 'false'}), \
                    patch.object(smoke.ctl, 'local_docker') as docker:
                with self.assertRaises(SystemExit) as error:
                    smoke.main()
                self.assertEqual(error.exception.code, 2)
                docker.assert_not_called()
                self.assertFalse(work.exists())

    def test_native_worker_refuses_tampered_installer_before_privileged_actions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = root / 'release'
            bundle.mkdir()
            (bundle / 'agentctl.py').write_bytes(b'tampered installer')
            authenticated = {'artifacts': {'agentctl.py': hashlib.sha256(b'authenticated installer').hexdigest()}}
            argv = ['selfhost_smoke.py', '--native-agent-worker', '--work', str(root),
                    '--baseline-release', str(bundle), '--baseline-version', 'v0.1.0',
                    '--candidate-release', str(bundle), '--candidate-version', 'v0.1.1']
            with patch('sys.argv', argv), \
                    patch.object(smoke.platform, 'system', return_value='Linux'), \
                    patch.dict(os.environ, {'GITHUB_ACTIONS': 'true'}), \
                    patch.object(smoke.os, 'geteuid', return_value=0), \
                    patch.object(smoke.ctl, 'release', return_value=(authenticated, {}, None)), \
                    patch.object(smoke, 'native_agent_lifecycle') as native:
                with self.assertRaisesRegex(smoke.ctl.Error, 'artifact checksum differs'):
                    smoke.main()
                native.assert_not_called()


if __name__ == '__main__':
    unittest.main()
