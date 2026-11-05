#!/usr/bin/env python3
"""Verify each rolled node over HTTP/HTTPS; keep app B serving during failures."""
import copy
from concurrent.futures import ThreadPoolExecutor
import json
import ssl
import sys
from threading import Event
import time
import urllib.error

from demo import wait_for, write_private
from listeners import Listeners
from pair import selected_state


def run(watch_store=False, entrypoint=False):
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    trust = selected_state() / 'acme/public/trust.pem'
    if trust.exists():
        lab.context = ssl.create_default_context(cafile=trust)

    def latest(app_id):
        return max((r for r in lab.revisions() if r['application_id'] == app_id), key=lambda r:r['number'])

    def effective(r):
        if r['status'] in ('rolled_back', 'rollback_failed'):
            return max((v for v in lab.revisions() if v['application_id'] == r['application_id'] and v['status'] == 'succeeded'), key=lambda v:v['number'])
        return r

    first, neighbor = lab.latest('Store demo'), lab.latest('Admin demo')
    if not first or not neighbor or any(effective(r).get('deleted') or effective(r)['spec'].get('disabled') for r in (first,neighbor)):
        raise RuntimeError('Start with both active demo applications and trusted HTTPS')
    if entrypoint:
        for c in lab.inventory():
            refs = {l['certificate_ref'] for l in c['document'].get('listeners', [])}
            certificates = [cert for cert in c.get('tls_certificates', []) if cert['reference'] in refs]
            if not certificates or any(cert['not_after_ms'] < (time.time()+900)*1000 for cert in certificates):
                raise RuntimeError('Renew the demo TLS certificates with at least 15 minutes remaining before running the entrypoint failure tests.')
    stop, failures, count = Event(), [], 0

    def traffic():
        nonlocal count
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users', 'app-b')
                lab.https('/shop/admin/v2/users', backend='app-b')
                count += 2
            except (OSError, RuntimeError) as error:
                failures.append(type(error).__name__)
                if len(failures) <= 5:
                    print(f'App B traffic failure: {type(error).__name__}: {error}', flush=True)
            stop.wait(.05)

    def deploy(spec, previous, outcome='succeeded'):
        def start():
            if latest(previous['application_id'])['id'] != previous['id']:
                raise RuntimeError('Another writer changed this demo application')
            body = lab.input(spec, previous)
            path = lab.path + '/' + previous['application_id'] + '/deploy'
            try:
                revision = lab.api.request(path, body)['revision']
            except urllib.error.HTTPError as error:
                if error.code == 409:
                    return None  # TLS renewal may win the fresh preconditions.
                raise RuntimeError(f'Application verification request failed ({error.code})') from error
            if lab.api.request(path, body)['revision']['id'] != revision['id']:
                raise RuntimeError('Retry created another rollout')
            return revision
        revision = wait_for('verification deployment ready', start, seconds=300)
        return lab.complete(revision, outcome)

    evidence = []
    with ThreadPoolExecutor(max_workers=1) as pool:
        monitoring = pool.submit(traffic)
        try:
            for current in (first, neighbor):
                spec = copy.deepcopy(effective(current)['spec'])
                spec['verification'] = {'http': True, 'https': True}
                if entrypoint:
                    spec['verification']['entrypoint'] = True
                if watch_store and current['application_id'] == first['application_id']:
                    def saved():
                        r = latest(first['application_id'])
                        if r['number'] > first['number'] and r['status'] in ('rolled_back','rollback_failed'):
                            raise RuntimeError('Console verification policy rolled back')
                        return r if r['status'] == 'succeeded' and r['spec'].get('verification') == spec['verification'] else None
                    revision = wait_for('console Store verification policy', saved, seconds=600)
                    rollout = next(r for r in lab.api.request(f'/v1/clusters/{lab.cluster}/rollouts')['rollouts'] if r['id'] == revision['rollout_id'])
                elif effective(current)['spec'] == spec:
                    if not all(any(a == spec for a in c['document'].get('applications',[])) for c in lab.inventory()):
                        raise RuntimeError('Enabled verification policy is not loaded on every node')
                    print('PASS: ' + spec['name'] + ' requested verification is already enabled and loaded on both nodes.', flush=True)
                    continue
                else:
                    rollout = deploy(spec, current)
                if not all(n.get('application_verification',{}).get('status') == 'passed' and n['application_verification']['phase'] == 'after_rejoin' for n in rollout['nodes']):
                    raise RuntimeError('Successful rollout skipped application verification')
                if entrypoint and not all(any(c.get('scope') == 'entrypoint' and c['passes'] == 2 for c in n['application_verification']['checks']) for n in rollout['nodes']):
                    raise RuntimeError('Entrypoint verification was omitted')
                print('PASS: ' + spec['name'] + ' passed direct HTTP/HTTPS before and after rejoin' + (' and entrypoint checks after rejoin' if entrypoint else '') + ' on both nodes.', flush=True)
            baseline = lab.inventory()
            a, b = effective(latest(first['application_id'])), effective(latest(neighbor['application_id']))
            for scheme in ('http', 'https'):
                previous = latest(first['application_id'])
                spec = copy.deepcopy(a['spec'])
                spec['verification'] = {'http': scheme == 'http', 'https': scheme == 'https'}
                if entrypoint:
                    spec['verification']['entrypoint'] = True
                # Healthy at the transport/HAProxy level, wrong at the app level.
                spec['servers'][0]['address'] = 'pair-fault-backend' if entrypoint else 'pair-backend-2'
                rollout = deploy(spec, previous, 'rolled_back')
                prefix = 'application_entrypoint_' if entrypoint else 'application_'
                if rollout['error_code'] != f'{prefix}{scheme}_failed' or (not entrypoint and any(n.get('apply_operation_id') for n in rollout['nodes'][1:])):
                    raise RuntimeError('Application failure advanced another node')
                target = next(n for n in rollout['nodes'] if n.get('application_verification', {}).get('status') == 'failed')
                failed = target.get('application_verification',{})
                restored = target.get('rollback_application_verification',{})
                expected_phase = 'after_cluster_restore' if entrypoint else 'after_rejoin'
                if failed.get('status') != 'failed' or restored.get('status') != 'passed' or restored.get('phase') != expected_phase:
                    raise RuntimeError('Failure or rollback request evidence missing')
                if not entrypoint and target.get('rollback_maintenance'):
                    raise RuntimeError('A node that failed before rejoin was unnecessarily withdrawn a second time')
                if entrypoint:
                    direct = [c for c in failed['checks'] if c.get('scope') == 'node' and c['scheme'] == scheme and c['application_id'] == first['application_id']]
                    if failed.get('phase') != 'after_rejoin' or not target.get('rollback_maintenance') or not direct or any(c['error_code'] or c['status_code'] != 200 for c in direct):
                        raise RuntimeError('The entrypoint failure was not isolated from healthy direct node checks')
                    if not all(c['passes'] == 2 for c in restored['checks']) or not any(c.get('scope') == 'entrypoint' for c in restored['checks']):
                        raise RuntimeError('Restored entrypoint was not verified')
                    if any(n['status'] not in ('rolled_back', 'skipped') or n.get('recovery_maintenance_id') for n in rollout['nodes']):
                        raise RuntimeError('A restored peer was stranded before compensation completed')
                evidence.append(rollout)
                for index,c in enumerate(lab.inventory()):
                    doc, before = c['document'], baseline[index]['document']
                    if doc['applications'] != before['applications'] or doc['backends'] != before['backends']:
                        raise RuntimeError('Rollback changed app B or did not restore app A')
                    binds = lambda d:[{k:l[k] for k in ('name','address','port')} for l in d.get('listeners',[])]
                    if binds(doc) != binds(before) or not c.get('tls_listeners') or not all(l['loaded'] for l in c['tls_listeners']):
                        raise RuntimeError('Shared listener or TLS was lost')
                if effective(latest(neighbor['application_id']))['id'] != b['id']:
                    raise RuntimeError('App B deployment history changed')
                lab.verify('/shop/items', 'app-a')
                for _ in range(8):
                    lab.https('/shop/items', backend='app-a')
                print(f'PASS: {scheme.upper()} ' + ('entrypoint request failed while direct node checks passed; changed nodes restored before final shared checks' if entrypoint else 'wrong-application response triggered rollback before rejoin') + '; app B unchanged, baseline reverified and shared TLS preserved.', flush=True)
        finally:
            stop.set()
            monitoring.result()
            print(f'App B traffic: {count} successful HTTP/HTTPS requests, {len(failures)} failures.', flush=True)
    write_private(selected_state() / ('entrypoint-verification-results.json' if entrypoint else 'application-verification-results.json'), json.dumps(evidence, indent=2) + '\n')
    if failures or count == 0:
        raise RuntimeError('App B was interrupted during application verification')


if __name__ == '__main__':
    run('--watch-store' in sys.argv)
