import hashlib
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

import selfhost_smoke as smoke


class NativeQualificationBoundary(unittest.TestCase):
    def test_native_names_use_control_network_and_are_restored_on_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            hosts = Path(temporary) / 'hosts'
            original = b'127.0.0.1 localhost\n'
            hosts.write_bytes(original)
            engine = Mock(c={'project': 'isolated'})
            engine.compose.side_effect = [b'manager-id', b'ingest-id']
            def inspect(command):
                address = '172.19.0.2' if command[-1] == 'manager-id' else '172.19.0.3'
                return json.dumps([{'NetworkSettings': {'Networks': {
                    'isolated_data': {'IPAddress': '172.18.0.2'},
                    'isolated_control': {'IPAddress': address}}}}]).encode()
            with patch.object(smoke.ctl, 'configuration', return_value=engine.c), \
                    patch.object(smoke.ctl, 'Engine', return_value=engine), \
                    patch.object(smoke.ctl, 'command', side_effect=inspect):
                with self.assertRaisesRegex(RuntimeError, 'test interrupted'):
                    with smoke.native_manager_hosts(Path('/installation'), hosts):
                        self.assertIn(b'172.19.0.2 manager', hosts.read_bytes())
                        self.assertIn(b'172.19.0.3 ingest', hosts.read_bytes())
                        self.assertNotIn(b'172.18.0.2', hosts.read_bytes())
                        raise RuntimeError('test interrupted')
            self.assertEqual(hosts.read_bytes(), original)

    def test_native_delegation_does_not_expose_raw_worker_stderr(self):
        args = types.SimpleNamespace(manager_port=18543, ingest_port=19543)
        release = (Path('/test/release'), {'version': 'v0.1.0'})
        result = types.SimpleNamespace(returncode=1, stdout=b'not JSON', stderr=b'private diagnostic data')
        with patch.object(smoke.os, 'geteuid', return_value=1000), \
                patch.object(smoke.subprocess, 'run', return_value=result):
            with self.assertRaisesRegex(smoke.ctl.Error, 'before reporting a curated error') as error:
                smoke.native_agent_lifecycle(args, Path('/test'), Path('/test/customer'),
                                             release, release, [])
        self.assertNotIn('private diagnostic data', str(error.exception))

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
