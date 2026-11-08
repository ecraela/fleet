"""Release, recovery and backup failure contracts; no Docker daemon needed."""
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import selfhostctl as ctl
from selfhost_smoke import HAPROXY, acceptance_host, prepare_proxy_image, published_releases

ROOT = Path(__file__).resolve().parents[1]


class SelfHosted(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.key, self.pub = self.root / 'publisher.key', self.root / 'publisher.pub'
        ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', self.key])
        self.key.chmod(0o600)
        ctl.command(['openssl', 'pkey', '-in', self.key, '-pubout', '-out', self.pub])

    def signed(self, version='v0.1.0', profile=None, repository=None, signing_key=None):
        folder = self.root / version
        folder.mkdir(mode=0o700)
        profile = profile or json.loads((ROOT / 'deploy/selfhost.json').read_text())
        ctl.write(folder / 'selfhost.json', json.dumps(profile))
        for name in ('agentctl.py', 'selfhostctl.py'):
            ctl.write(folder / name, b'authenticated fixture artifact')
        repository = repository or ctl.trust.REPOSITORY
        manifest = {'schema': 1, 'repository': repository, 'version': version,
                    'revision': 'a' * 40, 'protocol': 1, 'state_schema': 1,
                    'image': 'sha256:' + 'b' * 64,
                    'artifacts': {p.name: ctl.sha(p) for p in folder.iterdir()}}
        ctl.write(folder / 'release.json', json.dumps(manifest))
        ctl.command(['openssl', 'pkeyutl', '-sign', '-inkey', signing_key or self.key, '-rawin',
                     '-in', folder / 'release.json', '-out', folder / 'release.sig'])
        return folder, manifest, profile

    def installation(self, manifest, profile, public_key=None):
        root = self.root / 'installation'
        root.mkdir(mode=0o700)
        state = root / 'state'
        (state / 'pki').mkdir(mode=0o700, parents=True)
        for name in ('ca.crt', 'ca.key', 'server.crt', 'server.key'):
            ctl.write(state / 'pki' / name, 'synthetic-original-' + name)
        ctl.write(state / 'admin-token', 'synthetic-original-operator')
        ctl.write(state / 'organization-id', '12345678-1234-4234-8234-123456789abc\n')
        ctl.write(state / 'database-password', 'c' * 64 + '\n')
        ctl.write(state / 'database-runtime-password', 'd' * 64 + '\n')
        c = {'schema': 1, 'project': 'ecraela-hm-' + 'a' * 16,
             'uid': 10001, 'gid': 10001, 'bind_address': '127.0.0.1',
             'origin': 'https://localhost:18443', 'manager_port': 18443, 'ingest_port': 19443,
             'dev_http_port': None, 'agent_manager_url': 'https://localhost:18443',
             'agent_ingest_url': 'https://localhost:19443'}
        ctl.trust.private_json(root / 'installation.json', c)
        h = {'schema': 1, 'current': manifest, 'previous': None, 'profile': profile,
             'pending': None, 'identity': ctl.state_identity(root),
             'trust': ctl.hashlib.sha256((public_key or self.pub).read_bytes()).hexdigest()}
        ctl.trust.private_json(root / 'deployment.json', h)
        return root, c, h

    def test_authenticates_installer_profile_and_every_storage_pin(self):
        folder, manifest, profile = self.signed()
        self.assertEqual(ctl.release(folder, manifest['version'], self.pub)[:2], (manifest, profile))
        (folder / 'selfhost.json').write_text(json.dumps({**profile, 'database_schema': 21}))
        with self.assertRaises(ctl.Error):
            ctl.release(folder, manifest['version'], self.pub)
        folder, manifest, profile = self.signed('v0.1.1', {'schema': 1, 'database_schema': 19,
                    'images': {'postgres': 'postgres:latest', 'metrics': 'a', 'logs': 'b'}})
        with self.assertRaises(ctl.Error):
            ctl.release(folder, manifest['version'], self.pub)

    def test_legal_files_are_authenticated_and_retained_for_recovery(self):
        folder, manifest, _ = self.signed()
        for name in ctl.trust.LEGAL_ARTIFACTS:
            body = ('fixture ' + name).encode()
            (folder / name).write_bytes(body)
            manifest['artifacts'][name] = ctl.hashlib.sha256(body).hexdigest()
        (folder / 'release.json').write_text(json.dumps(manifest))
        ctl.command(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin',
                     '-in', folder / 'release.json', '-out', folder / 'release.sig'])
        verified, _, _ = ctl.release(folder, manifest['version'], self.pub)
        for name in ctl.trust.LEGAL_ARTIFACTS:
            self.assertEqual(verified.files[name], (folder / name).read_bytes())
        (folder / 'THIRD_PARTY_NOTICES.txt').write_text('tampered fixture')
        with self.assertRaisesRegex(ctl.Error, 'Legal artifact'):
            ctl.release(folder, manifest['version'], self.pub)

    def test_reupgrade_retains_large_source_without_relaxing_file_checks(self):
        baseline, old, _ = self.signed('v0.1.0')
        folder, manifest, _ = self.signed('v0.1.1')
        source = b'signed source archive\n' * 60000
        self.assertGreater(len(source), 1 << 20)
        (folder / 'source.tar.gz').write_bytes(source)
        manifest['artifacts']['source.tar.gz'] = ctl.hashlib.sha256(source).hexdigest()
        (folder / 'release.json').write_text(json.dumps(manifest))
        ctl.command(['openssl', 'pkeyutl', '-sign', '-inkey', self.key, '-rawin',
                     '-in', folder / 'release.json', '-out', folder / 'release.sig'])
        old, _, _ = ctl.release(baseline, old['version'], self.pub)
        candidate, _, _ = ctl.release(folder, manifest['version'], self.pub)
        root = self.root / 'retained-installation'
        for release in (old, candidate, old, candidate):
            retained = ctl.retain(root, release)
            self.assertTrue(retained.is_dir())
        archive = retained / 'source.tar.gz'
        self.assertEqual(archive.read_bytes(), source)
        archive.write_bytes(source[:-1] + b'!')
        with self.assertRaisesRegex(ctl.Error, 'retained exact release changed'):
            ctl.retain(root, candidate)
        archive.write_bytes(source)
        helper = retained / 'agentctl.py'
        helper.write_bytes(b'x' * ((1 << 20) + 1))
        with self.assertRaisesRegex(ctl.Error, 'bounded regular file'):
            ctl.retain(root, candidate)
        helper.write_bytes(candidate.files['agentctl.py'])
        archive.unlink()
        archive.symlink_to(folder / 'source.tar.gz')
        with self.assertRaisesRegex(ctl.Error, 'Symlinked input'):
            ctl.retain(root, candidate)

    def test_no_cloud_dependency_or_storage_ports_and_no_ca_signer_in_ingest(self):
        _, manifest, profile = self.signed()
        root, c, _ = self.installation(manifest, profile)
        spec = ctl.compose_spec(root, c, manifest, profile)
        for name in ('postgres', 'metrics', 'logs'):
            self.assertNotIn('ports', spec['services'][name])
            self.assertEqual(spec['services'][name]['networks'], ['data'])
        self.assertTrue(spec['networks']['data']['internal'])
        ingest = json.dumps(spec['services']['ingest'])
        self.assertNotIn('ca.key', ingest)
        self.assertNotIn('admin-token', ingest)
        self.assertNotIn('docker.sock', json.dumps(spec))
        self.assertNotIn('HAPROXY_LOGIN_SECRET', json.dumps(spec))
        self.assertNotIn('CONNECTOR_RELAY', json.dumps(spec))
        self.assertNotIn('build', spec['services']['manager'])
        self.assertNotIn('--migrate', spec['services']['manager']['command'])

    def test_profile_and_retained_artifacts_use_the_verified_snapshot(self):
        folder, expected, profile = self.signed()
        original = ctl.trust._verify_release
        original_files = {p.name: p.read_bytes() for p in folder.iterdir()}
        def mutate_after_verification(snapshot, version, key):
            result = original(snapshot, version, key)
            (folder / 'selfhost.json').write_text('{"schema": 900}')
            (folder / 'release.json').write_text('{"tampered": true}')
            return result
        with patch.object(ctl.trust, '_verify_release', side_effect=mutate_after_verification):
            manifest, parsed, _ = ctl.release(folder, expected['version'], self.pub)
        self.assertEqual(parsed, profile)
        self.assertEqual(manifest, expected)
        target = self.root / 'retained'
        target.mkdir(mode=0o700)
        saved = ctl.retain(target, manifest)
        self.assertEqual({p.name: p.read_bytes() for p in saved.iterdir()}, original_files)

    def test_connector_survives_render_rollback_and_cold_state_copy(self):
        _, manifest, profile = self.signed()
        root, _, history = self.installation(manifest, profile)
        ca = self.root / 'relay-ca.crt'
        ctl.command(['openssl', 'req', '-x509', '-new', '-key', self.key,
                     '-subj', '/CN=installer test relay', '-days', '1', '-out', ca])
        args = SimpleNamespace(directory=root, connector_relay_url='https://relay.example.test',
                               connector_ca_file=ca, timeout=5)
        class Engine:
            def __init__(self, root, c): self.root, self.c = root, c
            def render(self, manifest, profile): ctl.compose_spec(self.root, self.c, manifest, profile)
            def switch(self): pass
            def healthy(self, *args): pass
        with patch.object(ctl, 'Engine', Engine):
            ctl.configure_connector(args)
        ctl.write(root / 'state/connector/connector.json', 'synthetic-preserved-connector-identity')
        restored = self.root / 'restored'
        ctl.copy_tree(root, restored)
        for folder, version in ((root, 'v0.2.0'), (restored, manifest['version'])):
            c = ctl.configuration(folder)
            spec = ctl.compose_spec(folder, c, {**manifest, 'version': version}, profile)
            manager = spec['services']['manager']
            self.assertEqual(manager['environment']['HAPROXY_CONNECTOR_RELAY_URL'], args.connector_relay_url)
            self.assertIn(f'{folder}/state/connector:/connector', manager['volumes'])
            self.assertEqual((folder / 'state/connector/connector.json').read_text(), 'synthetic-preserved-connector-identity')
            self.assertEqual((folder / 'state/connector-ca.crt').read_bytes(), ca.read_bytes())
            for service in ('ingest', 'tools'):
                self.assertNotIn('CONNECTOR', json.dumps(spec['services'][service]))
        self.assertEqual(ctl.state_identity(root), history['identity'])

    def test_connector_refuses_bad_destination_and_rebinding_a_linked_identity(self):
        _, manifest, profile = self.signed()
        root, c, _ = self.installation(manifest, profile)
        args = SimpleNamespace(directory=root, connector_relay_url='', connector_ca_file=None, timeout=5)
        for url in ('http://relay.test', 'https://relay.test/path', 'https://user:secret@relay.test'):
            args.connector_relay_url = url
            with patch.object(ctl, 'Engine') as engine, self.assertRaises(ctl.Error):
                ctl.configure_connector(args)
            engine.assert_not_called()
        c['connector_relay_url'] = 'https://original.test'
        ctl.trust.private_json(root / 'installation.json', c)
        (root / 'state/connector').mkdir(mode=0o700)
        ctl.write(root / 'state/connector/connector.json', 'synthetic-existing-identity')
        args.connector_relay_url = 'https://different.test'
        with patch.object(ctl, 'Engine') as engine, self.assertRaises(ctl.Error):
            ctl.configure_connector(args)
        engine.assert_not_called()
        self.assertEqual(ctl.configuration(root)['connector_relay_url'], 'https://original.test')

    def test_connector_configuration_failure_restores_standalone_settings(self):
        _, manifest, profile = self.signed()
        root, c, h = self.installation(manifest, profile)
        args = SimpleNamespace(directory=root, connector_relay_url='https://relay.test',
                               connector_ca_file=None, timeout=5)
        class Engine:
            def __init__(self, root, current): self.c = current
            def render(self, *args): pass
            def switch(self): pass
            def healthy(self, *args):
                if self.c.get('connector_relay_url'): raise ctl.Error('fixture readiness failure')
        with patch.object(ctl, 'Engine', Engine), self.assertRaises(ctl.Error):
            ctl.configure_connector(args)
        self.assertEqual(ctl.configuration(root), c)
        self.assertEqual(ctl.state_identity(root), h['identity'])

    def test_upgrade_rejects_missing_identity_before_changing_processes(self):
        folder, manifest, profile = self.signed()
        root, _, _ = self.installation(manifest, profile)
        candidate, _, _ = self.signed('v0.1.1')
        (root / 'state/pki/ca.key').unlink()
        args = SimpleNamespace(directory=root, release=candidate, version='v0.1.1',
                               public_key=self.pub, action='upgrade', timeout=5)
        with patch.object(ctl, 'Engine') as engine, self.assertRaises(FileNotFoundError):
            ctl.change(args)
        engine.assert_not_called()

    def test_failed_candidate_recovers_prior_release_and_preserves_identity(self):
        _, manifest, profile = self.signed()
        root, _, h = self.installation(manifest, profile)
        candidate, new, _ = self.signed('v0.1.1')
        args = SimpleNamespace(directory=root, release=candidate, version='v0.1.1',
                               public_key=self.pub, action='upgrade', timeout=5)
        class Engine:
            calls = []
            def __init__(self, root, c): pass
            def prepare(self, *args): pass
            def stop(self, *args): pass
            def render(self, selected, profile): self.calls.append(selected['version'])
            def migrate(self, profile): raise ctl.Error('fixture migration failure')
            def switch(self): pass
            def healthy(self, *args): pass
        with patch.object(ctl, 'Engine', Engine), self.assertRaises(ctl.Error):
            ctl.change(args)
        after = ctl.load(root / 'deployment.json')
        self.assertEqual(after['current'], manifest)
        self.assertIsNone(after['pending'])
        self.assertEqual(after['last_result'], 'automatic_rollback')
        self.assertEqual(ctl.state_identity(root), h['identity'])
        self.assertEqual(Engine.calls, [new['version'], manifest['version']])

    def test_storage_changes_cannot_silently_downgrade_database_or_storage(self):
        _, _, profile = self.signed()
        for candidate in ({**profile, 'database_schema': 18},
                          {**profile, 'database_schema': 21},
                          {**profile, 'images': {**profile['images'], 'postgres': 'other'}}):
            with self.assertRaises(ctl.Error):
                ctl.compatible(profile, candidate)

    def test_only_the_known_additive_database_change_preserves_storage_compatibility(self):
        _, _, current = self.signed()
        baseline = {**current, 'database_schema': 19}
        ctl.compatible(baseline, current)
        ctl.compatible(current, baseline)
        self.assertEqual(baseline['database_schema'], 19)
        self.assertEqual(current['database_schema'], 20)
        with self.assertRaises(ctl.Error):
            ctl.compatible(current, {**current, 'unknown_storage_setting': True})

    def test_migration_accepts_only_the_release_schema_or_additive_software_rollback(self):
        _, manifest, profile = self.signed()
        root, c, _ = self.installation(manifest, profile)
        engine = ctl.Engine(root, c)
        for expected, actual, valid in ((19, 19, True), (20, 20, True), (19, 20, True),
                                        (20, 19, False), (19, 21, False), (20, 21, False)):
            with self.subTest(expected=expected, actual=actual), \
                    patch.object(engine, 'sql'), \
                    patch.object(engine, 'compose', side_effect=[b'', b'', f'{actual}\n'.encode()]):
                if valid:
                    engine.migrate({**profile, 'database_schema': expected})
                else:
                    with self.assertRaises(ctl.Error):
                        engine.migrate({**profile, 'database_schema': expected})

    def test_upgrade_and_previous_release_rollback_record_their_signed_profiles(self):
        profile = json.loads((ROOT / 'deploy/selfhost.json').read_text())
        baseline, old, baseline_profile = self.signed('v0.1.0', {**profile, 'database_schema': 19})
        candidate, new, _ = self.signed('v0.1.1', profile)
        root, _, initial = self.installation(old, baseline_profile)

        class Engine:
            migrations = []
            def __init__(self, root, c): pass
            def prepare(self, *args): pass
            def stop(self, *args): pass
            def render(self, *args): pass
            def migrate(self, selected): self.migrations.append(selected['database_schema'])
            def switch(self): pass
            def healthy(self, *args): pass

        with patch.object(ctl, 'Engine', Engine):
            for action, folder, manifest, expected in (
                    ('upgrade', candidate, new, profile), ('rollback', baseline, old, baseline_profile)):
                args = SimpleNamespace(directory=root, release=folder, version=manifest['version'],
                                       public_key=self.pub, action=action, timeout=5)
                ctl.change(args)
                after = ctl.load(root / 'deployment.json')
                self.assertEqual(after['profile'], expected)
                self.assertEqual(after['current'], manifest)
                self.assertEqual(after['identity'], initial['identity'])
                self.assertIsNone(after['pending'])
        self.assertEqual(Engine.migrations, [20, 19])

    def test_explicit_migration_and_previous_publisher_rollback_preserve_manager_state(self):
        source_key, source_pub = self.root / 'predecessor.key', self.root / 'predecessor.pub'
        ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', source_key])
        ctl.command(['openssl', 'pkey', '-in', source_key, '-pubout', '-out', source_pub])
        source, old, profile = self.signed(repository=ctl.trust.PREDECESSOR_REPOSITORY,
                                           signing_key=source_key)
        candidate, new, _ = self.signed('v0.1.1')
        root, _, h = self.installation(old, profile, source_pub)
        before = {p.name: p.read_bytes() for p in (root / 'state/pki').iterdir()}
        old_trust = ctl.hashlib.sha256(source_pub.read_bytes()).hexdigest()
        new_trust = ctl.hashlib.sha256(self.pub.read_bytes()).hexdigest()

        class Engine:
            calls = []
            prepared = []
            def __init__(self, root, c): pass
            def prepare(self, selected, profile): self.prepared.append(selected['repository'])
            def stop(self, *args): pass
            def render(self, selected, profile): self.calls.append(selected['version'])
            def migrate(self, *args): pass
            def switch(self): pass
            def healthy(self, *args): pass

        with patch.object(ctl, 'Engine', Engine):
            args = SimpleNamespace(directory=root, release=candidate, version='v0.1.1',
                                   public_key=self.pub, action='upgrade', timeout=5)
            with self.assertRaisesRegex(ctl.Error, 'explicit migration'):
                ctl.change(args)
            args.action = 'migrate'
            args.source_release = source
            args.source_public_key = source_pub
            ctl.change(args)
            migrated = ctl.load(root / 'deployment.json')
            self.assertEqual((migrated['current'], migrated['previous']), (new, old))
            self.assertEqual((migrated['trust'], migrated['previous_trust']), (new_trust, old_trust))
            args = SimpleNamespace(directory=root, release=source, version='v0.1.0',
                                   public_key=source_pub, action='rollback', timeout=5)
            ctl.change(args)
        rolled_back = ctl.load(root / 'deployment.json')
        self.assertEqual((rolled_back['current'], rolled_back['previous']), (old, new))
        self.assertEqual((rolled_back['trust'], rolled_back['previous_trust']), (old_trust, new_trust))
        self.assertEqual(rolled_back['identity'], h['identity'])
        self.assertEqual(before, {p.name: p.read_bytes() for p in (root / 'state/pki').iterdir()})
        self.assertEqual(Engine.calls, ['v0.1.1', 'v0.1.0'])
        self.assertEqual(Engine.prepared, [ctl.trust.PREDECESSOR_REPOSITORY, ctl.trust.REPOSITORY,
                                           ctl.trust.PREDECESSOR_REPOSITORY])
        self.assertTrue((root / 'releases/v0.1.0/release.sig').is_file())

    def test_failed_migration_recovers_predecessor_and_rejects_wrong_source(self):
        source, old, profile = self.signed(repository=ctl.trust.PREDECESSOR_REPOSITORY)
        candidate, new, _ = self.signed('v0.1.1')
        root, _, h = self.installation(old, profile)
        args = SimpleNamespace(directory=root, release=candidate, version='v0.1.1',
                               public_key=self.pub, action='migrate', timeout=5,
                               source_release=source, source_public_key=self.pub)
        with self.assertRaisesRegex(ctl.Error, 'explicit migration'):
            ctl.change(SimpleNamespace(**{**vars(args), 'action': 'upgrade'}))

        class Engine:
            calls = []
            def __init__(self, root, c): pass
            def prepare(self, *args): pass
            def stop(self, *args): pass
            def render(self, selected, profile): self.calls.append(selected['version'])
            def migrate(self, *args): pass
            def switch(self): pass
            def healthy(self, selected, *args):
                if selected['version'] == new['version']:
                    raise ctl.Error('candidate failed readiness')

        wrong_key = self.root / 'wrong.pub'
        ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', self.root / 'wrong.key'])
        ctl.command(['openssl', 'pkey', '-in', self.root / 'wrong.key', '-pubout', '-out', wrong_key])
        args.source_public_key = wrong_key
        with patch.object(ctl, 'Engine', Engine), self.assertRaises(ctl.Error):
            ctl.change(args)
        self.assertEqual(Engine.calls, [])
        args.source_public_key = self.pub
        with patch.object(ctl, 'Engine', Engine), self.assertRaisesRegex(ctl.Error, 'previous release is running'):
            ctl.change(args)
        after = ctl.load(root / 'deployment.json')
        self.assertEqual(after['current'], old)
        self.assertEqual(after['trust'], h['trust'])
        self.assertEqual(after['identity'], h['identity'])
        self.assertIsNone(after['pending'])
        self.assertEqual(after['last_result'], 'automatic_rollback')
        self.assertEqual(Engine.calls, ['v0.1.1', 'v0.1.0'])

    def test_interrupted_migration_recovers_predecessor_and_keeps_publisher(self):
        _, old, profile = self.signed(repository=ctl.trust.PREDECESSOR_REPOSITORY)
        _, candidate, _ = self.signed('v0.1.1')
        root, _, original = self.installation(old, profile)
        history = ctl.load(root / 'deployment.json')
        history['pending'] = {'action': 'migrate', 'release': candidate, 'profile': profile,
                              'trust': 'new-publisher'}
        ctl.trust.private_json(root / 'deployment.json', history)

        class Engine:
            calls = []
            def __init__(self, root, c): pass
            def render(self, selected, profile): self.calls.append(selected['version'])
            def switch(self): pass
            def healthy(self, *args): pass

        with patch.object(ctl, 'Engine', Engine):
            ctl.recover(SimpleNamespace(directory=root, timeout=5))
        recovered = ctl.load(root / 'deployment.json')
        self.assertEqual(recovered['current'], old)
        self.assertEqual(recovered['trust'], original['trust'])
        self.assertIsNone(recovered['pending'])
        self.assertEqual(Engine.calls, ['v0.1.0'])

    def test_acceptance_refuses_small_or_shared_engines_before_creating_a_lab(self):
        for info in ({'MemTotal': 2 << 30, 'ContainersRunning': 0},
                     {'MemTotal': 4 << 30, 'ContainersRunning': 1}):
            with self.assertRaises(ctl.Error):
                acceptance_host(info)
        acceptance_host({'MemTotal': 4 << 30, 'ContainersRunning': 0})

    def test_clean_engine_pulls_the_pinned_proxy_and_cached_engine_needs_no_registry(self):
        with patch.object(ctl, 'command', side_effect=[ctl.Error('image missing'), b'pulled']) as command:
            prepare_proxy_image()
            self.assertEqual(command.call_args.args[0], ['docker', 'pull', HAPROXY])
        with patch.object(ctl, 'command', return_value=b'cached') as command:
            prepare_proxy_image()
            command.assert_called_once_with(['docker', 'image', 'inspect', HAPROXY])

    def test_published_qualification_rejects_changed_files_before_running_installers(self):
        baseline, manifest, profile = self.signed('v0.1.0-rc.4')
        candidate, new, _ = self.signed('v0.1.0-rc.5')
        for m in (manifest, new):
            m['image'] = 'ghcr.io/ecraela/fleet@sha256:' + 'b' * 64
        args = SimpleNamespace(baseline_release=baseline, baseline_version=manifest['version'],
                               candidate_release=candidate, candidate_version=new['version'])
        def verified(folder, version):
            return (manifest if folder == baseline else new), profile, 'sigstore:ecraela/fleet'
        with patch.object(ctl, 'release', side_effect=verified), patch.object(ctl, 'command') as command:
            self.assertEqual(len(published_releases(args)), 2)
            self.assertEqual(command.call_count, 2)
            self.assertTrue(all(c.args[0][:2] == ['cosign', 'verify'] for c in command.call_args_list))
            (candidate / 'selfhostctl.py').write_text('changed installer')
            with self.assertRaises(ctl.Error):
                published_releases(args)

    def test_published_qualification_requires_registry_digest_and_forward_compatible_release(self):
        baseline, manifest, profile = self.signed('v0.1.0-rc.4')
        args = SimpleNamespace(baseline_release=baseline, baseline_version=manifest['version'],
                               candidate_release=baseline, candidate_version=manifest['version'])
        with patch.object(ctl, 'release', return_value=(manifest, profile, 'sigstore:test')):
            with self.assertRaises(ctl.Error):
                published_releases(args)
            manifest['image'] = 'ghcr.io/ecraela/fleet@sha256:' + 'b' * 64
            with patch.object(ctl, 'command'), self.assertRaises(ctl.Error):
                published_releases(args)

    def test_verified_cached_images_work_without_registry_access(self):
        _, manifest, profile = self.signed()
        binary = b'authenticated image executable'
        digest = ctl.hashlib.sha256(binary).hexdigest()
        manifest['artifacts']['haproxy-manager-linux-arm64'] = digest
        root, c, _ = self.installation(manifest, profile)
        calls = []
        def offline(args, **kwargs):
            args = list(map(str, args))
            calls.append(args)
            if args[:3] == ['docker', 'image', 'inspect']:
                return json.dumps([{'Id': 'sha256:' + 'c' * 64, 'Os': 'linux', 'Architecture': 'arm64'}]).encode()
            if args[1] == 'create':
                return b'fixture-container'
            if args[1] == 'cp':
                Path(args[-1]).write_bytes(binary)
            if args[1] == 'run':
                return json.dumps({'version': manifest['version'], 'revision': manifest['revision'],
                                   'protocol': 1, 'executable_sha256': digest}).encode()
            if args[1] == 'pull':
                raise AssertionError('Offline recovery must not contact a registry for a cached exact image')
            return b''
        with patch.object(ctl, 'local_docker', return_value={'Architecture': 'aarch64'}), patch.object(ctl, 'command', side_effect=offline):
            ctl.Engine(root, c).prepare(manifest, profile)
        self.assertFalse(any(call[1] == 'pull' for call in calls))

    def test_resume_writers_when_backup_copy_or_encryption_fails(self):
        _, manifest, profile = self.signed()
        root, c, _ = self.installation(manifest, profile)
        args = SimpleNamespace(directory=root, backup=self.root / 'backup.age',
                               recipient='age1' + 'a' * 58, signing_key=self.key, timeout=5)
        calls = []
        class Engine:
            def __init__(self, root, c): pass
            def running(self): return set(ctl.SERVICES)
            def stop(self): calls.append('stop')
            def compose(self, *args, **kwargs): calls.append(args)
        with patch.object(ctl, 'Engine', Engine), patch.object(ctl, 'copy_tree', side_effect=OSError('disk full')):
            with self.assertRaises(OSError):
                ctl.backup(args)
        self.assertEqual(calls[0], 'stop')
        self.assertEqual(calls[-1], ('start', *ctl.WRITERS))

    def test_symlinks_duplicate_paths_and_special_archive_entries_are_refused(self):
        for kind in ('link', 'duplicate', 'device', 'traversal', 'setuid'):
            path = self.root / (kind + '.tar')
            with tarfile.open(path, 'w:') as archive:
                entry = tarfile.TarInfo('../escape' if kind == 'traversal' else 'file')
                if kind == 'link':
                    entry.type, entry.linkname = tarfile.SYMTYPE, '/etc/passwd'
                elif kind == 'device':
                    entry.type = tarfile.CHRTYPE
                elif kind == 'setuid':
                    entry.mode = 0o4755
                archive.addfile(entry)
                if kind == 'duplicate':
                    archive.addfile(entry)
            with self.assertRaises(ctl.Error):
                ctl.validate_archive(path)
        source = self.root / 'unsafe'
        source.mkdir()
        (source / 'key').symlink_to(self.key)
        with self.assertRaises(ctl.Error):
            ctl.copy_tree(source, self.root / 'copy')

    def test_activation_refuses_live_source_even_with_operator_fencing_flag(self):
        _, manifest, profile = self.signed()
        root, c, h = self.installation(manifest, profile)
        c['restored_from'] = 'ecraela-hm-' + 'b' * 16
        ctl.trust.private_json(root / 'installation.json', c)
        h['restored_stopped'] = True
        ctl.trust.private_json(root / 'deployment.json', h)
        args = SimpleNamespace(directory=root, source_fenced=True, timeout=5)
        def running(args, **kwargs):
            return (b'container\n' if args[1] == 'ps' else
                    json.dumps([{'Config': {'Labels': {'com.docker.compose.service': 'manager'}}}]).encode())
        with patch.object(ctl, 'command', side_effect=running), patch.object(ctl, 'Engine') as engine:
            with self.assertRaises(ctl.Error):
                ctl.activate(args)
        engine.assert_not_called()

    def test_recover_cannot_activate_an_incomplete_restore(self):
        _, manifest, profile = self.signed()
        root, _, h = self.installation(manifest, profile)
        h.update(pending={'action': 'restore'}, restored_stopped=True)
        ctl.trust.private_json(root / 'deployment.json', h)
        with patch.object(ctl, 'Engine') as engine, self.assertRaises(ctl.Error):
            ctl.recover(SimpleNamespace(directory=root, timeout=5))
        engine.assert_not_called()

    @unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'age CLI required for encrypted backup tests')
    def test_restore_consumes_verified_bytes_despite_source_replacement_or_mutation(self):
        identity = self.root / 'backup.identity'
        ctl.command(['age-keygen', '-o', identity])
        identity.chmod(0o600)
        recipient = ctl.command(['age-keygen', '-y', identity]).decode().strip()
        stage = self.root / 'snapshot'
        stage.mkdir(mode=0o700)
        (stage / 'agent').mkdir(mode=0o700)
        original = {'node': {'id': 'synthetic-node'}, 'key_pem': 'original-test-key',
                    'certificate_pem': 'synthetic-test-certificate'}
        ctl.trust.private_json(stage / 'agent/identity.json', original)
        ctl.trust.private_json(stage / 'backup.json', {'schema': 1, 'kind': 'node', 'complete': True,
                                'identity': ctl.trust.identity(stage / 'agent'), 'paths': ['agent']})
        backup = self.root / 'node.age'
        ctl.seal(stage, backup, recipient, self.root, self.key)
        ciphertext = backup.read_bytes()
        replacement = {**original, 'key_pem': 'attacker-selected-test-key'}
        ctl.trust.private_json(stage / 'agent/identity.json', replacement)
        ctl.trust.private_json(stage / 'backup.json', {'schema': 1, 'kind': 'node', 'complete': True,
                                'identity': ctl.trust.identity(stage / 'agent'), 'paths': ['agent'],
                                'files': {'agent/identity.json': ctl.sha(stage / 'agent/identity.json')}})
        payload = self.root / 'replacement.tar'
        ctl.pack_tree(stage, payload)
        evil = self.root / 'replacement.age'
        with payload.open('rb') as source, evil.open('wb') as destination:
            ctl.command(['age', '-r', recipient], source=source, destination=destination)
        # The attacker has only a public encryption recipient, not a signed proof.
        verified = ctl.verify_backup
        for mode in ('replace', 'in-place'):
            for action in ('opened_backup', 'node_restore'):
                with self.subTest(mode=mode, action=action):
                    backup.write_bytes(ciphertext)
                    def switch_source(path, public_key):
                        verified(path, public_key)
                        if mode == 'replace':
                            candidate = self.root / 'swap.age'
                            candidate.write_bytes(evil.read_bytes())
                            candidate.replace(backup)
                        else:
                            backup.write_bytes(evil.read_bytes())
                    args = SimpleNamespace(backup=backup, identity_file=identity, backup_public_key=self.pub,
                                           directory=self.root / f'restored-{mode}-{action}')
                    with patch.object(ctl, 'verify_backup', side_effect=switch_source):
                        if action == 'opened_backup':
                            with ctl.opened_backup(args) as (restored, _):
                                actual = ctl.load(restored / 'agent/identity.json')
                        else:
                            ctl.node_restore(args)
                            actual = ctl.load(args.directory / 'agent/identity.json')
                    self.assertEqual(actual, original, 'restore consumed unsigned replacement state')

    @unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'age CLI required for encrypted backup tests')
    def test_real_encryption_signature_roundtrip_wrong_keys_damage_and_no_overwrite(self):
        age_key = self.root / 'backup.identity'
        ctl.command(['age-keygen', '-o', age_key])
        age_key.chmod(0o600)
        recipient = ctl.command(['age-keygen', '-y', age_key]).decode().strip()
        stage = self.root / 'snapshot'
        stage.mkdir(mode=0o700)
        ctl.write(stage / 'node.key', 'synthetic-original-node-key')
        ctl.trust.private_json(stage / 'backup.json', {'schema': 1, 'kind': 'node', 'complete': True})
        backup = self.root / 'node.age'
        ctl.seal(stage, backup, recipient, self.root, self.key)
        self.assertNotIn(b'synthetic-original-node-key', backup.read_bytes())
        args = SimpleNamespace(backup=backup, identity_file=age_key, backup_public_key=self.pub)
        with ctl.opened_backup(args) as (restored, meta):
            self.assertEqual((restored / 'node.key').read_text(), 'synthetic-original-node-key')
            self.assertTrue(meta['complete'])
        with self.assertRaises(ctl.Error):
            ctl.seal(stage, backup, recipient, self.root, self.key)
        foreign_key, foreign_pub = self.root / 'foreign.key', self.root / 'foreign.pub'
        ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', foreign_key])
        ctl.command(['openssl', 'pkey', '-in', foreign_key, '-pubout', '-out', foreign_pub])
        with self.assertRaises(ctl.Error):
            ctl.verify_backup(backup, foreign_pub)
        wrong_age = self.root / 'wrong.identity'
        ctl.command(['age-keygen', '-o', wrong_age])
        wrong_age.chmod(0o600)
        with self.assertRaises(ctl.Error), ctl.opened_backup(SimpleNamespace(**{**vars(args), 'identity_file': wrong_age})):
            pass
        original = backup.read_bytes()
        backup.write_bytes(original[:-1] + bytes([original[-1] ^ 1]))
        with self.assertRaises(ctl.Error), ctl.opened_backup(args):
            pass
        backup.write_bytes(original[:-10])
        with self.assertRaises(ctl.Error), ctl.opened_backup(args):
            pass


if __name__ == '__main__':
    unittest.main()
