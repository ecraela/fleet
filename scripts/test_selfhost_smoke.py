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
    def test_zero_exit_without_native_evidence_does_not_qualify(self):
        args = types.SimpleNamespace(manager_port=18543, ingest_port=19543)
        release = (Path('/test/release'), {'version': 'v0.1.0'})
        result = types.SimpleNamespace(returncode=0, stdout=b'private diagnostic data', stderr=b'')
        with patch.object(smoke.os, 'geteuid', return_value=1000), \
                patch.object(smoke.subprocess, 'run', return_value=result):
            with self.assertRaisesRegex(smoke.ctl.Error, 'complete qualification evidence') as error:
                smoke.native_agent_lifecycle(args, Path('/test'), Path('/test/customer'), release, release, [])
            self.assertNotIn('private diagnostic data', str(error.exception))

    def test_native_delegation_returns_managed_transition_evidence(self):
        args = types.SimpleNamespace(manager_port=18543, ingest_port=19543)
        release = (Path('/test/release'), {'version': 'v0.1.0'})
        evidence = {'direct_install_upgrade_rollback_restart': True,
                    'managed_updates': {'status': 'passed', 'baseline_updater_verified': True}}
        result = types.SimpleNamespace(returncode=0, stdout=json.dumps({'native_agent': evidence}).encode(), stderr=b'')
        with patch.object(smoke.os, 'geteuid', return_value=1000), \
                patch.object(smoke.subprocess, 'run', return_value=result):
            self.assertEqual(smoke.native_agent_lifecycle(args, Path('/test'), Path('/test/customer'),
                                                        release, release, []), evidence)

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


class ManagedNativeQualification(unittest.TestCase):
    def test_requires_its_own_success_receipt_and_preserves_signed_updater(self):
        for status in ('succeeded', 'needs_attention'):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                (work / 'administrator.password').write_text('disposable-password')
                root = work / 'software'
                (root / 'updater').mkdir(parents=True)
                script = root / 'updater/agentctl.py'
                contents = {'v0.1.0': b'signed baseline', 'v0.1.1': b'signed candidate'}
                releases = [(work / version, {'version': version, 'managed_upgrades': True,
                                             'artifacts': {'agentctl.py': hashlib.sha256(body).hexdigest()}})
                            for version, body in contents.items()]
                identity = {'node_id': 'synthetic-node'}
                config = {'install_dir': str(root), 'state_dir': str(work / 'identity')}
                api = Mock(base='https://localhost:18443')
                current = {'version': 'v0.1.0'}
                def request(path, body=None, local=False):
                    self.assertTrue(local, 'qualification must exercise the browser session and CSRF path')
                    if '/plan?' in path:
                        return {'plan': {'eligible': True}}
                    if body:
                        current['version'] = body['version']
                        return {'update': {'id': 'requested-update'}}
                    return {'updates': [{'id': 'another-update', 'status': 'succeeded'},
                                        {'id': 'requested-update', 'status': status}]}
                api.request.side_effect = request
                def host_command(*args, **kwargs):
                    if args[1] == 'enable-updates':
                        script.write_bytes(contents['v0.1.0'])
                def systemctl(command):
                    if command[:2] == ['systemctl', 'start']:
                        script.write_bytes(contents[current['version']])
                with patch.object(smoke, 'API', return_value=api), \
                        patch.object(smoke, 'host_command', side_effect=host_command), \
                        patch.object(smoke.ctl, 'command', side_effect=systemctl), \
                        patch.object(smoke.ctl.trust, 'identity', return_value=identity), \
                        patch.object(smoke.ctl.trust, 'wait_healthy'), \
                        patch.object(smoke, 'wait', side_effect=lambda label, function: function()):
                    if status == 'succeeded':
                        evidence = smoke.managed_native_agent_lifecycle(work, work / 'installation', config,
                                                                       work / 'profile.json', *releases, [])
                        self.assertEqual(evidence['status'], 'passed')
                        self.assertEqual([row['action'] for row in evidence['transitions']],
                                         ['upgrade', 'rollback', 'upgrade'])
                        self.assertEqual(script.read_bytes(), contents['v0.1.1'])
                    else:
                        with self.assertRaisesRegex(smoke.ctl.Error, 'report success'):
                            smoke.managed_native_agent_lifecycle(work, work / 'installation', config,
                                                                work / 'profile.json', *releases, [])

    def test_missing_signed_managed_capability_is_reported_as_unsupported(self):
        release = (Path('/release'), {'version': 'v0.1.0'})
        with patch.object(smoke, 'API') as api:
            result = smoke.managed_native_agent_lifecycle(Path('/work'), Path('/installation'), {},
                                                          Path('/profile'), release, release, [])
        self.assertEqual(result['status'], 'unsupported_by_baseline_or_candidate')
        api.assert_not_called()


class FixturePackaging(unittest.TestCase):
    def test_signed_fixtures_require_every_native_packaging_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            packaging, bundle = work / 'packaging', work / 'release'
            packaging.mkdir()
            bundle.mkdir()
            key, public = work / 'publisher.key', work / 'publisher.pub'
            smoke.ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', key])
            smoke.ctl.command(['openssl', 'pkey', '-in', key, '-pubout', '-out', public])
            for name in smoke.ctl.trust.LEGAL_ARTIFACTS:
                (packaging / name).write_bytes(('synthetic ' + name).encode())
            version = smoke.VERSIONS[0]
            _, manifest = smoke.sign_fixture(bundle, version, 'a' * 40, 'sha256:' + 'b' * 64, key, packaging)
            authenticated, _ = smoke.ctl.trust._verify_release(bundle, version, public)
            self.assertEqual(authenticated, manifest)
            for name in smoke.ctl.trust.LEGAL_ARTIFACTS:
                self.assertEqual(manifest['artifacts'][name], hashlib.sha256((packaging / name).read_bytes()).hexdigest())
            (packaging / 'source.json').unlink()
            incomplete = work / 'incomplete'
            incomplete.mkdir()
            with self.assertRaises(FileNotFoundError):
                smoke.sign_fixture(incomplete, version, 'a' * 40, 'sha256:' + 'b' * 64, key, packaging)
            self.assertFalse((incomplete / 'release.sig').exists())


if __name__ == '__main__':
    unittest.main()
