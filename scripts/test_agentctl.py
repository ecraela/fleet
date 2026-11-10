"""Cryptographic and lifecycle regressions; no Docker, root or network required."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
import uuid
from types import SimpleNamespace
from unittest.mock import patch

import agentctl as ctl


class HostUpdater(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.key, self.pub = self.root / 'key', self.root / 'key.pub'
        ctl.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', self.key])
        ctl.run(['openssl', 'pkey', '-in', self.key, '-pubout', '-out', self.pub])
        self.install = self.root / 'installed'
        (self.install / 'bundles').mkdir(parents=True)
        self.install.chmod(0o700)
        (self.install / 'updater').mkdir()
        self.state = self.root / 'state'
        self.state.mkdir()
        self.node = str(uuid.uuid4())
        ctl.private_json(self.state / 'identity.json', {'node': {'id': self.node}})
        self.config = {'mode': 'docker', 'install_dir': str(self.install), 'state_dir': str(self.state)}
        self.old = {'version': 'v0.1.0', 'revision': 'a' * 40,
                    'artifacts': {'haproxy-agent-linux-amd64': 'b' * 64}}
        ctl.private_json(self.install / 'deployment.json',
                         {'schema': 1, 'current': self.old, 'previous': None, 'pending': None, 'trust': ctl.hashlib.sha256(self.pub.read_bytes()).hexdigest()})
        ctl.private_json(self.install / 'updater.json',
                         {'schema': 1, 'manager_url': 'https://manager.example.test', 'public_key': str(self.pub)})
        self.version = 'v0.2.0'
        self.script = b'#!/usr/bin/env python3\nprint("signed updater")\n'
        self.bundle = self.install / 'bundles' / self.version
        self.bundle.mkdir()
        (self.bundle / 'agentctl.py').write_bytes(self.script)
        self.manifest = {'schema': 1, 'repository': ctl.REPOSITORY, 'version': self.version,
                         'revision': 'c' * 40, 'image': 'sha256:' + 'd' * 64,
                         'protocol': 1, 'state_schema': 1, 'managed_upgrades': True,
                         'compatibility': {'manager': {'min': 'v0.1.0', 'max': self.version},
                                           'agent': {'min': 'v0.1.0', 'max': self.version}},
                         'artifacts': {'agentctl.py': ctl.hashlib.sha256(self.script).hexdigest(),
                                       'haproxy-agent-linux-amd64': 'e' * 64,
                                       'haproxy-agent-linux-arm64': 'f' * 64}}
        (self.bundle / 'release.json').write_text(json.dumps(self.manifest))
        ctl.run(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin', '-in', self.bundle / 'release.json', '-out', self.bundle / 'release.sig'])
        self.update = {'id': str(uuid.uuid4()), 'node_id': self.node, 'action': 'upgrade',
                       'status': 'queued', 'target_version': self.version, 'expected_version': self.old['version'],
                       'target_revision': self.manifest['revision'], 'platform': 'linux/amd64',
                       'agent_sha256': 'e' * 64,
                       'manifest_sha256': ctl.hashlib.sha256((self.bundle / 'release.json').read_bytes()).hexdigest(),
                       'publisher': 'ed25519:' + ctl.hashlib.sha256(self.pub.read_bytes()).hexdigest()}
        self.statuses = []

    def manager_request(self, client, origin, method, path, body=None):
        if method == 'GET':
            return {'update': self.update, 'manager_build': {'version': 'v0.1.0', 'protocol': 1}}
        if path.endswith('/status'):
            self.statuses.append(body['status'])
        return {}

    def apply(self, c, folder, version, key, action, timeout):
        self.assertEqual(folder, self.bundle)
        self.assertEqual(version, self.version)
        self.assertEqual(action, 'upgrade')
        history = json.loads((self.install / 'deployment.json').read_text())
        history.update(current=self.manifest, previous=self.old, pending=None, last_result='upgrade')
        ctl.private_json(self.install / 'deployment.json', history)

    def test_reconciles_signed_target_and_retries_only_after_host_verification(self):
        with patch.object(ctl.platform, 'system', return_value='Linux'), \
                patch.object(ctl.platform, 'machine', return_value='x86_64'), \
                patch.object(ctl.os, 'geteuid', return_value=0), \
                patch.object(ctl, 'updater_client', return_value=(object(), self.node)), \
                patch.object(ctl, 'updater_request', side_effect=self.manager_request), \
                patch.object(ctl, 'resource_preflight'), \
                patch.object(ctl, 'apply', side_effect=self.apply) as apply:
            ctl.reconcile(self.config, 5)
            apply.assert_called_once()
            self.assertEqual(self.statuses, ['preparing', 'activating', 'verifying', 'succeeded'])
            updater = self.install / 'updater/agentctl.py'
            self.assertEqual(updater.read_bytes(), self.script)
            updater.unlink()  # Simulate a missing script on a completion retry.
            self.update['status'] = 'verifying'
            self.statuses.clear()
            ctl.reconcile(self.config, 5)
            apply.assert_called_once()
            self.assertEqual(self.statuses, ['succeeded'])
            self.assertEqual(updater.read_bytes(), self.script)

    def test_invalid_signature_never_activates_and_reports_attention(self):
        (self.bundle / 'release.sig').write_bytes(b'X' * 64)
        with patch.object(ctl.platform, 'system', return_value='Linux'), \
                patch.object(ctl.platform, 'machine', return_value='x86_64'), \
                patch.object(ctl.os, 'geteuid', return_value=0), \
                patch.object(ctl, 'updater_client', return_value=(object(), self.node)), \
                patch.object(ctl, 'updater_request', side_effect=self.manager_request), \
                patch.object(ctl, 'apply') as apply, \
                self.assertRaises(ctl.InstallError):
            ctl.reconcile(self.config, 5)
        apply.assert_not_called()
        self.assertEqual(self.statuses, ['needs_attention'])

    def test_release_without_managed_updater_never_activates(self):
        self.manifest.pop('managed_upgrades')
        (self.bundle / 'release.json').write_text(json.dumps(self.manifest))
        ctl.run(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin',
                 '-in', self.bundle / 'release.json', '-out', self.bundle / 'release.sig'])
        self.update['manifest_sha256'] = ctl.hashlib.sha256((self.bundle / 'release.json').read_bytes()).hexdigest()
        with patch.object(ctl.platform, 'system', return_value='Linux'), \
                patch.object(ctl.platform, 'machine', return_value='x86_64'), \
                patch.object(ctl.os, 'geteuid', return_value=0), \
                patch.object(ctl, 'updater_client', return_value=(object(), self.node)), \
                patch.object(ctl, 'updater_request', side_effect=self.manager_request), \
                patch.object(ctl, 'apply') as apply, \
                self.assertRaises(ctl.InstallError):
            ctl.reconcile(self.config, 5)
        apply.assert_not_called()
        self.assertEqual(self.statuses, ['needs_attention'])

    def test_stages_offline_release_with_installed_publisher(self):
        source = self.root / 'offline-release'
        self.bundle.rename(source)
        with patch.object(ctl.platform, 'system', return_value='Linux'), \
                patch.object(ctl.os, 'geteuid', return_value=0):
            ctl.stage_release(self.config, source, self.version, self.pub)
        self.assertEqual((self.bundle / 'release.json').read_bytes(),
                         (source / 'release.json').read_bytes())
        self.assertEqual((self.bundle / 'agentctl.py').read_bytes(), self.script)
        altered = {**self.manifest, 'revision': 'f' * 40}
        (source / 'release.json').write_text(json.dumps(altered))
        ctl.run(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin',
                 '-in', source / 'release.json', '-out', source / 'release.sig'])
        with patch.object(ctl.platform, 'system', return_value='Linux'), \
                patch.object(ctl.os, 'geteuid', return_value=0), \
                self.assertRaises(ctl.InstallError):
            ctl.stage_release(self.config, source, self.version, self.pub)


class Releases(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.key, self.pub = self.root / 'key.pem', self.root / 'key.pub'
        ctl.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', self.key])
        ctl.run(['openssl', 'pkey', '-in', self.key, '-pubout', '-out', self.pub])
        self.state = self.root / 'identity'
        self.state.mkdir(mode=0o700)
        ctl.private_json(self.state / 'identity.json', {'node': {'id': 'node-a'}, 'key_pem': 'synthetic-preserved-key', 'certificate_pem': 'synthetic-cert'})
        self.c = {'mode': 'docker', 'state_dir': str(self.state), 'install_dir': str(self.root / 'software')}

    def release(self, version='v0.1.0', **changes):
        folder = self.root / version
        folder.mkdir(exist_ok=True)
        m = {'schema': 1, 'repository': ctl.REPOSITORY, 'version': version, 'revision': 'a'*40,
             'image': 'sha256:'+'b'*64, 'protocol': 1, 'state_schema': 1,
             'artifacts': {'haproxy-agent-linux-arm64': 'c'*64}}
        m.update(changes)
        (folder / 'release.json').write_text(json.dumps(m))
        ctl.run(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin', '-in', folder / 'release.json', '-out', folder / 'release.sig'])
        return folder, m

    def test_real_signature_and_exact_version(self):
        path, m = self.release()
        self.assertEqual(ctl.verify_release(path, 'v0.1.0', self.pub), m)
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.1', self.pub)
        (path / 'release.json').write_text(json.dumps({**m, 'image': 'sha256:'+'d'*64}))
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_foreign_signer_and_mutable_images_refused(self):
        path, _ = self.release(image='ghcr.io/ecraela/fleet:latest')
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.0', self.pub)
        path,_ = self.release()
        foreign = self.root / 'foreign.pem'
        ctl.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', foreign])
        ctl.run(['openssl', 'pkey', '-in', foreign, '-pubout', '-out', self.pub])
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_predecessor_publisher_is_not_implicitly_trusted(self):
        for change in ({'repository': 'ecraela/haproxy-manager'},
                       {'image': 'ghcr.io/ecraela/haproxy-manager@sha256:' + 'b' * 64}):
            path, _ = self.release(**change)
            with self.assertRaises(ctl.InstallError):
                ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_fleet_image_requires_the_exact_signed_digest(self):
        path, manifest = self.release(image='ghcr.io/ecraela/fleet@sha256:' + 'b' * 64)
        self.assertEqual(ctl.verify_release(path, 'v0.1.0', self.pub), manifest)
        (path / 'release.json').write_text(json.dumps({**manifest, 'image': 'ghcr.io/ecraela/fleet@sha256:' + 'd' * 64}))
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_verification_authenticates_the_snapshot_that_is_parsed(self):
        path, original = self.release()
        actual_run = ctl.run
        def mutate_input(args, **kwargs):
            if 'pkeyutl' in args:
                (path/'release.json').write_text(json.dumps({**original,'image':'sha256:'+'d'*64}))
            return actual_run(args, **kwargs)
        with patch.object(ctl,'run',side_effect=mutate_input):
            verified = ctl.verify_release(path,'v0.1.0',self.pub)
        self.assertEqual(verified,original)
        self.assertNotEqual(json.loads((path/'release.json').read_text()),verified)

    def test_incompatible_state_and_artifact_paths_refused(self):
        for change in ({'protocol': 2}, {'state_schema': 2}, {'artifacts': {'../../binary': 'c'*64}}, {'repository': 'other/repo'}):
            path, _ = self.release(**change)
            with self.assertRaises(ctl.InstallError):
                ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_signed_peer_ranges_gate_managed_upgrade(self):
        ranges = {'manager': {'min': 'v0.1.0-rc.20', 'max': 'v0.1.0'},
                  'agent': {'min': 'v0.1.0-rc.21', 'max': 'v0.1.0'}}
        path, manifest = self.release(compatibility=ranges)
        verified = ctl.verify_release(path, 'v0.1.0', self.pub)
        self.assertTrue(ctl.compatible_peer(verified, 'manager', 'v0.1.0-rc.21'))
        with self.assertRaisesRegex(ctl.InstallError, 'outside the signed'):
            ctl.compatible_peer(verified, 'agent', 'v0.1.0-rc.20')
        with self.assertRaisesRegex(ctl.InstallError, 'unknown'):
            ctl.compatible_peer(verified, 'agent', 'dev')
        legacy = {k: v for k, v in manifest.items() if k != 'compatibility'}
        with self.assertRaisesRegex(ctl.InstallError, 'Invalid signed release compatibility'):
            ctl.compatible_peer(legacy, 'agent', 'v0.1.0')

    def test_malformed_signed_peer_ranges_are_refused(self):
        good = {'manager': {'min': 'v0.1.0-rc.20', 'max': 'v0.1.0'},
                'agent': {'min': 'v0.1.0-rc.21', 'max': 'v0.1.0'}}
        for ranges in (None, {}, {'manager': good['manager']},
                       {**good, 'agent': {'min': 'v0.2.0', 'max': 'v0.1.0'}},
                       {**good, 'agent': {'min': 'v0.1.0', 'max': 'v0.1.0-rc.21'}},
                       {**good, 'agent': {'min': 'latest', 'max': 'v0.1.0'}},
                       {**good, 'agent': {'min': 'v0.1.0', 'max': 'v0.1.0', 'extra': 1}}):
            path, _ = self.release(compatibility=ranges)
            with self.assertRaises(ctl.InstallError):
                ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_signed_legal_artifacts_are_allowed_but_arbitrary_paths_are_not(self):
        artifacts = {'haproxy-agent-linux-arm64': 'c' * 64,
                     **{name: 'd' * 64 for name in ctl.LEGAL_ARTIFACTS}}
        path, manifest = self.release(artifacts=artifacts)
        self.assertEqual(ctl.verify_release(path, 'v0.1.0', self.pub), manifest)
        for name in ('../LICENSE', 'legal/source.tar.gz', 'anything.py'):
            path, _ = self.release(artifacts={**artifacts, name: 'd' * 64})
            with self.assertRaises(ctl.InstallError):
                ctl.verify_release(path, 'v0.1.0', self.pub)

    def test_native_install_retains_authenticated_legal_files_and_refuses_tampering(self):
        folder, manifest = self.release()
        binary = b'fixture executable'
        (folder / 'haproxy-agent-linux-amd64').write_bytes(binary)
        manifest['artifacts']['haproxy-agent-linux-amd64'] = hashlib.sha256(binary).hexdigest()
        for name in ctl.LEGAL_ARTIFACTS:
            body = ('fixture ' + name).encode()
            (folder / name).write_bytes(body)
            manifest['artifacts'][name] = hashlib.sha256(body).hexdigest()
        engine = object.__new__(ctl.Linux)
        engine.root = self.root / 'native'
        with patch.object(ctl.platform, 'machine', return_value='x86_64'), \
                patch.object(ctl, 'run', return_value=json.dumps({'version': manifest['version'], 'revision': manifest['revision']}).encode()):
            engine.prepare(manifest, folder)
            for name in ctl.LEGAL_ARTIFACTS:
                self.assertEqual((engine.root / 'releases' / manifest['version'] / name).read_bytes(), (folder / name).read_bytes())
            (folder / 'LICENSE').write_text('tampered fixture')
            with self.assertRaisesRegex(ctl.InstallError, 'checksum'):
                engine.prepare(manifest, folder)

    def test_health_requires_version_hash_identity_and_fresh_metrics(self):
        _, m = self.release()
        since = time.time() - 1
        v = {'node_id': 'node-a', 'observed_at': datetime.now(timezone.utc).isoformat(),
             'build': {'version': m['version'], 'revision': m['revision'], 'platform': 'linux/arm64', 'protocol': 1, 'executable_sha256': 'c'*64}}
        ctl.private_json(self.state / 'agent-health.json', v)
        expected = ctl.identity(self.state)
        self.assertTrue(ctl.health(self.state, m, since, expected))
        self.assertFalse(ctl.health(self.state, m, time.time()+1, expected))
        for change in ({'node_id': 'node-b'}, {'build': {**v['build'], 'executable_sha256': 'd'*64}}, {'build': {**v['build'], 'version': 'dev'}}):
            ctl.private_json(self.state / 'agent-health.json', {**v, **change})
            self.assertFalse(ctl.health(self.state, m, since, expected))

    def engine(self, fail=None):
        class Engine:
            calls = []
            def prepare(s, m, folder): pass
            def switch(s, m):
                s.calls.append(m['version'])
                if m['version'] == fail:
                    raise ctl.InstallError('synthetic startup failure')
            def stop(s): pass
        return Engine()

    def test_upgrade_rollback_preserve_complete_state(self):
        baseline, _ = self.release()
        candidate, _ = self.release('v0.1.1')
        ctl.private_json(self.state / 'acme-account.json', {'private': 'synthetic-account'})
        ctl.private_json(self.state / 'operations.json', {'pending': 'synthetic-op'})
        before = {p.name: p.read_bytes() for p in self.state.iterdir()}
        engine = self.engine()
        with patch.object(ctl, 'wait_healthy'):
            ctl.apply(self.c, baseline, 'v0.1.0', self.pub, 'install', adapter=engine)
            ctl.apply(self.c, candidate, 'v0.1.1', self.pub, adapter=engine)
            ctl.apply(self.c, baseline, 'v0.1.0', self.pub, 'rollback', adapter=engine)
        self.assertEqual(engine.calls, ['v0.1.0', 'v0.1.1', 'v0.1.0'])
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.state.iterdir()})

    def test_failed_upgrade_automatically_recovers_old_version(self):
        baseline, _ = self.release()
        candidate, _ = self.release('v0.1.1')
        engine = self.engine(fail='v0.1.1')
        with patch.object(ctl, 'wait_healthy'):
            ctl.apply(self.c, baseline, 'v0.1.0', self.pub, 'install', adapter=engine)
            with self.assertRaises(ctl.InstallError):
                ctl.apply(self.c, candidate, 'v0.1.1', self.pub, adapter=engine)
        record = json.loads((Path(self.c['install_dir']) / 'deployment.json').read_text())
        self.assertEqual(record['current']['version'], 'v0.1.0')
        self.assertIsNone(record['pending'])
        self.assertEqual(record['last_result'], 'automatic_rollback')
        self.assertEqual(engine.calls, ['v0.1.0', 'v0.1.1', 'v0.1.0'])

    def test_missing_key_is_never_reenrolled_on_upgrade(self):
        baseline, _ = self.release()
        candidate, _ = self.release('v0.1.1')
        engine = self.engine()
        with patch.object(ctl, 'wait_healthy'):
            ctl.apply(self.c, baseline, 'v0.1.0', self.pub, 'install', adapter=engine)
        (self.state / 'identity.json').unlink()
        with self.assertRaises(ctl.InstallError):
            ctl.apply(self.c, candidate, 'v0.1.1', self.pub, adapter=engine)
        self.assertEqual(engine.calls, ['v0.1.0'])

    def test_interrupted_first_install_and_upgrade_are_recovered_without_reenrollment(self):
        _,m = self.release()
        root=Path(self.c['install_dir'])
        root.mkdir(mode=0o700)
        for current in (None,m):
            ctl.private_json(root/'deployment.json',{'schema':1,'current':current,'previous':None,
                'pending':{'release':m,'identity':None if current is None else ctl.identity(self.state)}})
            engine=self.engine()
            with patch.object(ctl,'Docker',return_value=engine),patch.object(ctl,'wait_healthy'):
                ctl.recover(self.c,5)
            v=json.loads((root/'deployment.json').read_text())
            self.assertIsNone(v['pending'])
            self.assertEqual(v['current'],m)
            self.assertEqual(v['identity'],ctl.identity(self.state))
        # Missing key material is never recreated during an interrupted recovery.
        ctl.private_json(root/'deployment.json',{'current':None,'previous':None,'pending':{'release':m,'identity':None}})
        (self.state/'identity.json').unlink()
        with self.assertRaises(FileNotFoundError): ctl.recover(self.c,5)

    def test_symlinked_artifacts_and_state_overlap_refused(self):
        path, _ = self.release()
        (path / 'release.sig').unlink()
        (path / 'release.sig').symlink_to(self.pub)
        with self.assertRaises(ctl.InstallError):
            ctl.verify_release(path, 'v0.1.0', self.pub)
        local = self.root / 'installer.json'
        ctl.private_json(local, {**self.c, 'install_dir': str(self.state / 'software')})
        with self.assertRaises(ctl.InstallError):
            ctl.config(local)

    def test_systemd_unit_is_unprivileged_and_literal(self):
        c = {**self.c, 'user': 'haproxy-agent', 'arguments': ['--state-dir', str(self.state), '--name', 'node %H $GOMEMLIMIT "test"']}
        v = ctl.unit(c)
        self.assertIn('User=haproxy-agent', v)
        self.assertIn('ProtectSystem=strict', v)
        self.assertIn('node %%H $$GOMEMLIMIT \\"test\\"', v)
        self.assertNotIn('docker.sock', v)

    def test_native_install_never_replaces_another_service(self):
        units=self.root/'systemd'
        units.mkdir()
        (units/'ecraela-haproxy-agent.service').write_text('Existing administrator-managed service')
        with patch.object(ctl.platform,'system',return_value='Linux'), patch.object(ctl.os,'geteuid',return_value=0), patch.object(ctl.pwd,'getpwnam',return_value=SimpleNamespace(pw_uid=1001,pw_gid=1001)), patch.object(ctl,'SYSTEMD_UNIT_DIRS',(str(units),)), patch.object(ctl,'run') as commands:
            with self.assertRaisesRegex(ctl.InstallError,'another installation'):
                ctl.Linux({**self.c,'arguments':['--state-dir',str(self.state)]},self.root/'software')
            commands.assert_not_called()


if __name__ == '__main__':
    unittest.main()
