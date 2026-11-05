"""Offline guards for a drill that handles real keys and Docker volumes."""
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

from hardening import Load, collection_delta, fresh, require_local_docker, restored_request, restored_servers_ready, verify_request_history
from recovery import SERVICES, WRITERS, Restore, checkpoint, command, digest, private_copy, source_command_args, validate_archive, verify


class RecoveryTest(unittest.TestCase):
    def test_failed_command_reports_exit_status_without_credentials_or_output(self):
        result=Mock(returncode=22,stdout=b'private response',stderr=b'private credential')
        with patch('recovery.subprocess.run',return_value=result):
            with self.assertRaisesRegex(RuntimeError,r'^Local recovery command failed: docker \(exit 22\)$'):
                command(['docker','exec','private-token'],input=b'private request')

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.root.chmod(0o700)

    def fixture(self):
        snapshot = self.root/'checkpoint'
        snapshot.mkdir(mode=0o700)
        (snapshot/'pki').mkdir()
        for name in ('ca.crt','ca.key','server.crt','server.key'):
            (snapshot/'pki'/name).write_text('synthetic material')
        (snapshot/'manager.dump').write_text('synthetic database')
        (snapshot/'operator-token').write_text('synthetic token')
        (snapshot/'binds').mkdir()
        (snapshot/'binds/identity').mkdir()
        (snapshot/'binds/identity/identity.json').write_text(json.dumps(dict(node=dict(id='node-a'),key_pem='synthetic key',certificate_pem='synthetic cert',ca_pem='synthetic CA')))
        manifest = dict(version=1,complete=True,database='manager.dump',operator_token='operator-token',pki='pki',trust='pki/ca.crt',archives=[],volumes={},nodes={'agent-a':dict(id='node-a',state='binds/identity')},services={'manager':dict(volumes=[dict(type='bind',source='pki',target='/pki')])})
        self.seal(snapshot,manifest)
        return snapshot,manifest

    def seal(self,snapshot,manifest):
        manifest['files'] = {str(p.relative_to(snapshot)):digest(p) for p in snapshot.rglob('*') if p.is_file() and p!=snapshot/'manifest.json'}
        (snapshot/'manifest.json').write_text(json.dumps(manifest))

    def test_integrity_missing_key_and_partial_backup_fail_before_restore(self):
        snapshot,manifest = self.fixture()
        verify(snapshot)
        identity = snapshot/'binds/identity/identity.json'
        identity.write_text('{}')
        with self.assertRaisesRegex(RuntimeError,'integrity'):
            verify(snapshot)
        identity.write_text(json.dumps(dict(node=dict(id='node-a'),certificate_pem='cert',ca_pem='CA')))
        self.seal(snapshot,manifest)
        with self.assertRaisesRegex(RuntimeError,'private key'):
            Restore(snapshot,self.root/'not-created')
        self.assertFalse((self.root/'not-created').exists())
        manifest['complete'] = False
        self.seal(snapshot,manifest)
        with self.assertRaisesRegex(RuntimeError,'Incomplete'):
            verify(snapshot)

    def test_missing_installation_signing_key_is_not_a_valid_backup(self):
        snapshot,manifest = self.fixture()
        (snapshot/'pki/ca.key').unlink()
        self.seal(snapshot,manifest)
        with self.assertRaisesRegex(RuntimeError,'required recovery material'):
            verify(snapshot)

    def test_refuses_world_readable_backup_and_symlink_copy(self):
        snapshot,_ = self.fixture()
        snapshot.chmod(0o755)
        with self.assertRaisesRegex(RuntimeError,'private directory'):
            verify(snapshot)
        source = self.root/'link'
        source.symlink_to('/etc/hosts')
        with self.assertRaisesRegex(RuntimeError,'links and special files'):
            private_copy(source,self.root/'copied')

    def test_tar_rejects_traversal_links_special_files_and_privileged_modes(self):
        for name,kind,mode in (('../escape',tarfile.REGTYPE,0o600),('/absolute',tarfile.REGTYPE,0o600),('./link',tarfile.SYMTYPE,0o600),('./fifo',tarfile.FIFOTYPE,0o600),('./suid',tarfile.REGTYPE,0o4600)):
            with self.subTest(name=name):
                file=self.root/'archive.tar'
                with tarfile.open(file,'w') as archive:
                    member=tarfile.TarInfo(name)
                    member.type,member.mode=kind,mode
                    archive.addfile(member)
                with self.assertRaises(RuntimeError): validate_archive(file)
        with tarfile.open(self.root/'valid.tar','w') as archive:
            member=tarfile.TarInfo('./identity.json')
            member.size=2
            archive.addfile(member,io.BytesIO(b'{}'))
        validate_archive(self.root/'valid.tar')

    def test_checkpoint_resumes_original_writers_after_copy_failure(self):
        config=dict(name='converge-haproxy',services={name:dict(image='synthetic:local',volumes=[]) for name in SERVICES})
        config['services']['postgres']['volumes']=[dict(type='bind',source='/synthetic',target='/synthetic')]
        def source(*args):
            if args[0]=='config': return json.dumps(config).encode()
            if args[0]=='ps': return ' '.join(SERVICES).encode()
            return b''
        with patch('recovery.command',return_value=b'sha256:synthetic'), patch('recovery.selected_state',return_value=self.root), patch('recovery.source_command',side_effect=source) as calls, patch('recovery.private_copy',side_effect=OSError('synthetic copy failure')):
            with self.assertRaises(OSError): checkpoint(self.root/'failed',{})
        calls.assert_any_call('start',*(n for n in WRITERS if n in SERVICES))
        self.assertFalse((self.root/'failed/manifest.json').exists())

    def test_missing_source_image_fails_before_stopping_writers(self):
        config=dict(name='converge-haproxy',services={name:dict(image='synthetic:local',volumes=[]) for name in SERVICES})
        def source(*args):
            return json.dumps(config).encode() if args[0]=='config' else ' '.join(SERVICES).encode()
        def docker(args,**kwargs):
            if args[1:3]==['image','inspect']: raise RuntimeError('synthetic missing image')
            return b'sha256:synthetic'
        with patch('recovery.selected_state',return_value=self.root), patch('recovery.source_command',side_effect=source) as calls, patch('recovery.command',side_effect=docker):
            with self.assertRaisesRegex(RuntimeError,'Required recovery image'):
                checkpoint(self.root/'missing-image',{})
        self.assertFalse(any(call.args[0]=='stop' for call in calls.call_args_list))

    def test_restore_uses_fenced_network_and_only_new_workspace_binds(self):
        snapshot,_=self.fixture()
        restored=Restore(snapshot,self.root/'restore')
        self.assertTrue(restored.config['networks']['default']['internal'])
        self.assertTrue(restored.project.startswith('converge-haproxy-restore-'))
        self.assertEqual(restored.config['services']['manager']['volumes'][0]['source'],str(self.root/'restore/pki'))
        # No commands ran while preparing a restore, and source material survives.
        self.assertTrue((snapshot/'pki/ca.key').exists())

    def test_old_checkpoint_socket_probe_cannot_admit_the_temporary_postgres_server(self):
        snapshot,manifest=self.fixture()
        manifest['services']['postgres'] = dict(
            command=['postgres'], volumes=[],
            environment={'POSTGRES_USER':'saved-owner','POSTGRES_DB':'saved-database'},
            healthcheck={'test':['CMD','pg_isready']})
        self.seal(snapshot,manifest)
        restored=Restore(snapshot,self.root/'postgres-restore')
        pg=restored.config['services']['postgres']
        self.assertIn('-h 127.0.0.1',pg['healthcheck']['test'][1])
        self.assertEqual(pg['environment']['POSTGRES_DB'],'saved-database')
        self.assertEqual(pg['environment']['POSTGRES_USER'],'saved-owner')
        # Compose must pass the saved variable names to the container shell,
        # never expand them against the operator's host environment.
        rendered=json.loads(restored.file.read_text())
        probe=rendered['services']['postgres']['healthcheck']['test'][1]
        self.assertIn('"$$POSTGRES_USER"',probe)
        self.assertIn('"$$POSTGRES_DB"',probe)

    def test_checkpoint_uses_the_same_standalone_default_as_the_demo(self):
        for env,integrated in (({},False),({'HAPROXY_PAIR_STANDALONE':'0'},True)):
            with self.subTest(env=env), patch.dict(os.environ,env,clear=True), patch('recovery.selected_state',return_value=self.root):
                args,_=source_command_args('ps')
                self.assertEqual(any(arg.endswith('/compose.console.yaml') for arg in args),integrated)

    def test_checkpoint_retains_the_running_traffic_collection_overlay(self):
        overlay=self.root/'traffic/compose.json'
        overlay.parent.mkdir()
        overlay.write_text('{}')
        with patch('recovery.selected_state',return_value=self.root):
            args,_=source_command_args('config','--format','json')
        self.assertIn(str(overlay),args)
        self.assertEqual(args[args.index(str(overlay))-1],'-f')

    def test_restore_can_target_a_separate_local_engine_without_changing_source_context(self):
        snapshot,manifest=self.fixture()
        manifest['services']['manager']['image']='synthetic:local'
        self.seal(snapshot,manifest)
        with patch('recovery.command',return_value=b'unix:///private/qa/docker.sock') as calls:
            restored=Restore(snapshot,self.root/'qa',docker_context='qa')
            restored.compose('ps')
        calls.assert_any_call(['docker','context','inspect','qa','--format','{{.Endpoints.docker.Host}}'])
        calls.assert_any_call(['docker','--context','qa','image','inspect','synthetic:local'])
        self.assertEqual(calls.call_args.args[0][:4],['docker','--context','qa','compose'])

    def test_restore_rejects_a_remote_target_before_creating_private_workspace(self):
        snapshot,_=self.fixture()
        with patch('recovery.command',return_value=b'ssh://remote.example') as calls:
            with self.assertRaisesRegex(RuntimeError,'local Docker Unix socket'):
                Restore(snapshot,self.root/'rejected',docker_context='remote')
        self.assertEqual(calls.call_count,1)
        self.assertFalse((self.root/'rejected').exists())

    def test_restore_target_requires_images_before_creating_private_workspace(self):
        snapshot,manifest=self.fixture()
        manifest['services']['manager']['image']='synthetic:local'
        self.seal(snapshot,manifest)
        def command(args,**kwargs):
            if args[1:3]==['context','inspect']: return b'unix:///private/qa/docker.sock'
            raise RuntimeError('missing target image')
        with patch('recovery.command',side_effect=command):
            with self.assertRaisesRegex(RuntimeError,'missing target image'):
                Restore(snapshot,self.root/'missing-target-image',docker_context='qa')
        self.assertFalse((self.root/'missing-target-image').exists())

    def test_latency_quantile_counts_failures_separately(self):
        load=Load(None,1,4)
        row=load.stats['load']
        row['success']=100
        row['latency'].update({10:94,100:6})
        row['failures']['TimeoutError']=2
        result=load.results()['load']
        self.assertEqual(result['p95_ms'],100)
        self.assertEqual(result['success'],100)
        self.assertEqual(result['failures'],{'TimeoutError':2})

    def test_restored_probe_pins_http_version_and_checks_routing(self):
        response=(b'HTTP/1.1 200 OK\r\nX-HAProxy-Node: haproxy-a\r\n'
                  b'X-Demo-Backend: app-a\r\n\r\nConverge HAProxy demo backend\nCV_CERT_BEGIN\n')
        clone=Mock()
        clone.compose.return_value=response
        self.assertEqual(restored_request(clone,[('/shop','app-a')],0),('http','app-a','haproxy-a',''))
        self.assertIn('--http1.1',clone.compose.call_args.args)
        clone.compose.return_value=response.replace(b'X-Demo-Backend: app-a',b'X-Demo-Backend: another-app')
        with self.assertRaisesRegex(RuntimeError,'route or backend'):
            restored_request(clone,[('/shop','app-a')],0)

    def test_recovery_waits_for_discovery_after_manager_restart(self):
        from datetime import datetime,timezone
        api=Mock()
        api.request.side_effect=[{'nodes':[{'id':'node-a','name':'haproxy-a','cluster_id':'cluster'}]},
                                 {'observed_at':datetime.now(timezone.utc).isoformat(),'fresh':True,'scrape_ok':True},
                                 {'fresh':False,'control':{'discovery':None}}]
        baseline={'cluster':'cluster','configurations':{'haproxy-a':{'document':{}}}}
        self.assertFalse(fresh(api,baseline))

    def test_connected_restore_waits_for_both_nodes_and_the_entrypoint_backends(self):
        baseline={'cluster':'cluster','configurations':{'haproxy-a':{},'haproxy-b':{}}}
        nodes=[{'id':name,'name':name,'cluster_id':'cluster'} for name in baseline['configurations']]
        def view(health):
            return {'fresh':True,'control':{'discovery':{'observed_at_ms':2000,'servers':[{'health':health,'check_status':'L7OK'}]}}}
        views={name:view('UP') for name in [*baseline['configurations'],'entrypoint']}
        def request(path):
            if path=='/v1/nodes': return {'nodes':nodes}
            if path=='/v1/clusters': return {'clusters':[{'id':'cluster','topology':{'control':{'node_id':'entrypoint'}}}]}
            return views[path.split('/')[3]]
        api=Mock();api.request.side_effect=request
        with patch('hardening.fresh',return_value=True):
            self.assertTrue(restored_servers_ready(api,baseline,1))
            for node in views:
                views[node]=view('DOWN')
                self.assertFalse(restored_servers_ready(api,baseline,1))
                views[node]=view('UP')
            views['haproxy-a']['control']['discovery']['servers']=[]
            self.assertFalse(restored_servers_ready(api,baseline,1))
            views['haproxy-a']=view('UP');views['entrypoint']['fresh']=False
            self.assertFalse(restored_servers_ready(api,baseline,1))

    def test_historic_requests_survive_late_arrivals_displacing_the_first_page(self):
        from urllib.parse import parse_qs, urlsplit
        endpoint = '/applications/app-a/requests?range=15m&end=100000'
        records = [{'id':'saved-a','status':200}, {'id':'saved-b','status':503}]
        def response(path):
            query = parse_qs(urlsplit(path).query)
            self.assertEqual(query['end'], ['100000'])
            if 'request_id' not in query:
                return {'requests':[{'id':'late-arrival','status':200}]}
            return {'requests':[r for r in records if r['id']==query['request_id'][0]]}
        api = Mock()
        api.request.side_effect = response
        self.assertEqual(verify_request_history(api, {endpoint:records}), 2)
        self.assertEqual(api.request.call_count, 2)

    def test_historic_request_loss_or_mutation_still_fails_restore(self):
        record = {'id':'saved-a','status':200,'node_id':'node-a'}
        for actual in ([], [dict(record,status=503)], [record,record]):
            with self.subTest(actual=actual):
                api = Mock()
                api.request.return_value = {'requests':actual}
                with self.assertRaisesRegex(RuntimeError,'not restored unchanged'):
                    verify_request_history(api, {'/requests?range=15m&end=100000':[record]})

    def test_load_detects_collection_loss_and_counter_resets(self):
        before={'node':dict(boot_id='boot',received=10,rejected=0,dropped=0,unconfirmed=0)}
        after={'node':dict(before['node'],received=20)}
        self.assertEqual(collection_delta(before,after)['node']['received'],10)
        for field in ('dropped','rejected','unconfirmed'):
            changed={'node':dict(after['node'],**{field:1})}
            with self.assertRaisesRegex(RuntimeError,'under steady load'):
                collection_delta(before,changed)
        with self.assertRaisesRegex(RuntimeError,'restarted'):
            collection_delta(before,{'node':dict(after['node'],boot_id='new')})

    def test_drills_refuse_remote_docker_contexts_and_host_overrides(self):
        with patch('hardening.command',return_value=b'unix:///local/docker.sock'), patch.dict(os.environ,{'DOCKER_HOST':'tcp://remote.test:2375'}):
            with self.assertRaisesRegex(RuntimeError,'never a remote context'):
                require_local_docker()
        with patch('hardening.command',return_value=b'ssh://remote.test'), patch.dict(os.environ,{'DOCKER_HOST':''}):
            with self.assertRaisesRegex(RuntimeError,'never a remote context'):
                require_local_docker()


if __name__=='__main__': unittest.main()
