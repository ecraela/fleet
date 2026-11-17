"""The Geo acceptance assertion waits for its own complete, coarse evidence."""
import unittest
from types import SimpleNamespace
from unittest.mock import patch
from urllib.error import HTTPError

from geography import (recent_requests, untrusted_observations, untrusted_requests,
                       entrypoint_probe_batch, entrypoint_proxy_observations)
from requests import maintenance


class UntrustedObservationsTest(unittest.TestCase):
    def event(self, at, method='OPTIONS', geo=None, status=220, node='node-a'):
        return dict(id=f'{method}-{at}', at_ms=at, method=method, status=status, node_id=node,
                    geo=geo or dict(source='peer', status='non_public'))

    def test_late_trusted_gets_and_previous_runs_do_not_change_the_assertion(self):
        rows = [self.event(1200, 'GET', dict(source='xff', status='partial', country='GB')),
                self.event(900, geo=dict(source='xff', status='partial', country='GB')),
                self.event(1100), self.event(1200, status=221, node='node-b')]
        self.assertEqual(len(untrusted_observations(rows, 1000, {220:'node-a',221:'node-b'})), 2)

    def test_incomplete_ingestion_cannot_pass(self):
        expected = {220:'node-a',221:'node-b'}
        self.assertIsNone(untrusted_observations([self.event(1100)], 1000, expected))
        self.assertIsNone(untrusted_observations([self.event(1100),self.event(1101)], 1000, expected))
        self.assertIsNone(untrusted_observations([self.event(1100,status=221)], 1000, expected))
        self.assertIsNone(untrusted_observations([], 1000, expected))

    def test_a_spoofed_or_missing_geo_result_still_fails(self):
        for geo in (dict(source='xff', status='partial', country='GB'),
                    dict(source='peer', status='partial', asn=15169),
                    dict(source='unknown', status='unresolved'),
                    dict(source='unknown', status='unavailable')):
            with self.subTest(geo=geo), self.assertRaisesRegex(RuntimeError, 'overrode'):
                untrusted_observations([self.event(1100, geo=geo)], 1000, {220:'node-a'})
        missing = self.event(1100)
        del missing['geo']
        with self.assertRaisesRegex(RuntimeError, 'overrode'):
            untrusted_observations([missing], 1000, {220:'node-a'})

    def test_all_requests_must_be_read_across_search_pages(self):
        calls = []
        def query(kind, **filters):
            calls.append((kind, filters))
            if 'before' not in filters:
                return dict(requests=[self.event(1200+i,status=220+i) for i in range(10)], next_cursor='next', end_ms=1500)
            return dict(requests=[self.event(1100+i,status=230+i) for i in range(2)], next_cursor='', end_ms=1500)
        self.assertEqual(len(untrusted_requests(query, 1000, {code:'node-a' for code in range(220,232)})), 12)
        self.assertEqual(calls[1], ('requests', dict(method='OPTIONS', end=1500, before='next')))

    def test_fresh_batch_pages_keep_the_node_and_frozen_window(self):
        calls=[]
        def query(kind, **filters):
            calls.append(filters)
            rows=[self.event(1200),self.event(1100)] if 'before' not in filters else [self.event(900)]
            return dict(requests=rows,next_cursor='next',end_ms=1500)
        self.assertEqual(len(recent_requests(query,1000,node='node-a',method='POST')),2)
        self.assertEqual(calls[1],dict(node='node-a',method='POST',end=1500,before='next'))


class EntrypointObservationsTest(unittest.TestCase):
    def test_round_robin_batch_covers_both_nodes_for_each_application_and_protocol(self):
        seen=set(); position=0
        def request(scheme,backend):
            nonlocal position
            seen.add((backend,position%2,scheme));position+=1
        lab=SimpleNamespace(request=lambda path,backend:request('http',backend),
                            https=lambda path,backend:request('https',backend))
        entrypoint_probe_batch(lab)
        self.assertEqual(position,64)
        self.assertEqual(seen,{(app,node,scheme) for app in ('app-a','app-b') for node in (0,1) for scheme in ('http','https')})

    def query(self,missing=None):
        def read(kind,**filters):
            node=filters['node']
            scheme='https' if 'before' in filters else 'http'
            source='peer' if missing==(filters['app'],node,scheme) else 'proxy_protocol'
            row=dict(at_ms=1100 if scheme=='https' else 1500,scheme=scheme,geo=dict(source=source))
            return dict(requests=[row] if scheme=='https' else [row]*10,
                        next_cursor='' if scheme=='https' else 'older',end_ms=2000)
        return read

    def test_all_eight_groups_can_be_found_beyond_the_first_search_page(self):
        nodes={'haproxy-a':{'id':'node-a'},'haproxy-b':{'id':'node-b'}}
        self.assertIsNotNone(entrypoint_proxy_observations(self.query(),1000,nodes))

    def test_one_missing_proxy_group_cannot_pass(self):
        nodes={'haproxy-a':{'id':'node-a'},'haproxy-b':{'id':'node-b'}}
        self.assertIsNone(entrypoint_proxy_observations(self.query(('Admin demo','node-b','https')),1000,nodes))


class MaintenanceRequestTest(unittest.TestCase):
    def test_withdraw_waits_for_target_discovery_after_agent_restart(self):
        reads, writes = 0, []
        def request(path, body=None):
            nonlocal reads
            if body is not None:
                writes.append(body)
                return {}
            if writes:
                return {'maintenance': {'status': 'maintenance'}}
            reads += 1
            cfg = {'document': {'backends': []}, 'observed_at_ms': 100000,
                   'artifact_id': 'owned', 'version': 1}
            d = {'configuration': cfg, 'generation': 'ready'}
            return {'target': {'discovery': None if reads == 1 else d},
                    'controller': {'discovery': d}, 'binding_id': 'bound',
                    'reason_code': '', 'maintenance': None}
        def poll(label, operation, seconds=30):
            for _ in range(3):
                if value := operation():
                    return value
            self.fail('target discovery did not settle: ' + label)
        lab = SimpleNamespace(api=SimpleNamespace(request=request))
        with patch('requests.wait_for', side_effect=poll), patch('requests.time.time', return_value=100):
            maintenance(lab, {'id': 'node', 'name': 'local node'}, 'withdraw')
        self.assertEqual(reads, 2)
        self.assertEqual(len(writes), 1)

    def exercise(self, rejection):
        bodies, reads, accepted = [], 0, False
        def request(path, body=None):
            nonlocal reads, accepted
            if body is not None:
                bodies.append(body)
                if len(bodies)==1: raise HTTPError('http://local.test',rejection,'synthetic rejection',None,None)
                accepted=True;return {}
            if accepted: return {'maintenance':{'status':'completed'}}
            reads+=1
            version=1 if not bodies else 2
            cfg={'document':{'backends':[]},'observed_at_ms':100000+reads*1000,'artifact_id':'owned','version':version}
            d={'configuration':cfg,'observed_at_ms':100000+reads*1000,'generation':str(version),'servers':[{'health':'UP','check_status':'L7OK'}]}
            return {'target':{'discovery':d},'controller':{'discovery':d},'binding_id':'bound','reason_code':'','maintenance':{'id':'held','status':'maintenance'}}
        def poll(label, operation, seconds=30):
            for _ in range(15):
                if value:=operation(): return value
            self.fail('retry did not settle: '+label)
        lab=SimpleNamespace(api=SimpleNamespace(request=request))
        with patch('requests.wait_for',side_effect=poll),patch('requests.time.time',side_effect=lambda:100+reads):
            maintenance(lab,{'id':'node','name':'local node'},'rejoin')
        return bodies

    def test_unaccepted_conflict_rereads_the_expected_version_before_retry(self):
        bodies=self.exercise(409)
        self.assertEqual([b['expected_version'] for b in bodies],[1,2])
        self.assertNotEqual(bodies[0]['request_id'],bodies[1]['request_id'])
        self.assertEqual(bodies[1]['maintenance_id'],'held')

    def rejoin_observations(self, observations, terminal='completed'):
        writes = []
        samples = iter(observations)
        now = [100]
        def request(path, body=None):
            if body is not None:
                self.assertFalse(writes, 'accepted maintenance must never be replayed')
                writes.append(body)
                return {}
            if writes:
                return {'maintenance': {'status': terminal, 'error_code': 'target_changed'}}
            generation, observed = next(samples)
            now[0] = observed / 1000
            cfg = dict(document={'backends': []}, artifact_id='owned', version=1,
                       observed_at_ms=observed)
            d = dict(generation=generation, observed_at_ms=observed, configuration=cfg,
                     servers=[dict(health='UP', check_status='L7OK')])
            return dict(target=dict(discovery=d), controller=dict(discovery=d),
                        binding_id='bound', reason_code='', maintenance=None)
        def poll(label, operation, seconds=30):
            for _ in range(len(observations)):
                if value := operation(): return value
            raise TimeoutError(label)
        lab = SimpleNamespace(api=SimpleNamespace(request=request))
        with patch('requests.wait_for', side_effect=poll), patch('requests.time.time', side_effect=lambda: now[0]):
            maintenance(lab, dict(id='node', name='local node'), 'rejoin')
        return writes

    def test_rejoin_restarts_the_window_when_reload_changes_the_runtime(self):
        rows = [('old', 100000), ('old', 104000), ('new', 105000),
                ('new', 109000), ('new', 110000)]
        writes = self.rejoin_observations(rows)
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0]['expected_generation'], 'new')

    def test_repeated_cached_observation_cannot_pass_the_rejoin_window(self):
        with self.assertRaises(TimeoutError):
            self.rejoin_observations([('same', 109000)] * 8)

    def test_accepted_attention_is_reported_without_replaying_the_rejoin(self):
        with self.assertRaisesRegex(RuntimeError, 'target_changed'):
            self.rejoin_observations([('same', 105000), ('same', 110000)], 'attention')

    def test_permission_failures_are_never_retried(self):
        with self.assertRaises(HTTPError) as error:self.exercise(403)
        self.assertEqual(error.exception.code,403)
        error.exception.close()


if __name__ == '__main__':
    unittest.main()
