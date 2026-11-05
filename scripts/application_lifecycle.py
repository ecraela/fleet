#!/usr/bin/env python3
"""Disable, re-enable, remove and restore app A while app B serves HTTP/HTTPS."""
from concurrent.futures import ThreadPoolExecutor
from threading import Event
import ssl
import sys
import time
import urllib.error
import urllib.request

from demo import wait_for
from listeners import Listeners
from pair import selected_state


def state(revision):
    return 'deleted' if revision.get('deleted') else 'disabled' if revision['spec'].get('disabled') else 'active'


def run(watch=False):
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    trust = selected_state() / 'acme/public/trust.pem'
    if trust.exists():
        lab.context = ssl.create_default_context(cafile=trust)
    first, neighbor = lab.latest('Store demo'), lab.latest('Admin demo')
    def effective(revision):
        if revision and revision['status'] in ('rolled_back', 'rollback_failed'):
            return max((r for r in lab.revisions() if r['application_id'] == revision['application_id'] and r['status'] == 'succeeded'), key=lambda r:r['number'], default=revision)
        return revision
    source, neighbor = effective(first), effective(neighbor)
    if not source or not neighbor or (not watch and state(source) != 'active') or state(neighbor) != 'active':
        raise RuntimeError('Start with the two existing active Store demo and Admin demo applications')
    baseline = lab.inventory()
    if any(not c.get('tls_listeners') or not all(l['loaded'] for l in c['tls_listeners']) for c in baseline):
        raise RuntimeError('Enable trusted demo HTTPS before lifecycle testing')
    request_path = source['spec']['path_prefix'] + '/items'
    neighbor_path = neighbor['spec']['path_prefix'] + '/users'
    stop, failures, count = Event(), [], 0

    def traffic():
        nonlocal count
        while not stop.is_set():
            try:
                lab.request(neighbor_path, 'app-b')
                lab.https(neighbor_path, backend='app-b')
                count += 2
            except (OSError, RuntimeError):
                failures.append('App B request failed')
            time.sleep(.05)

    def current():
        return max((r for r in lab.revisions() if r['application_id'] == first['application_id']), key=lambda r: r['number'])

    def mutate(action, previous):
        path = f'/v1/clusters/{lab.cluster}/applications/{first["application_id"]}/lifecycle'
        def start():
            if current()['id'] != previous['id']:
                raise RuntimeError('Another writer changed the demo application')
            body = lab.input({}, previous)
            body.pop('spec')
            body['action'] = action
            try:
                revision = lab.api.request(path, body)['revision']
            except urllib.error.HTTPError as error:
                if error.code == 409:
                    return None  # A scheduled TLS rollout can win the fresh preconditions.
                raise RuntimeError(f'Lifecycle request failed ({error.code})') from error
            if lab.api.request(path, body)['revision']['id'] != revision['id']:
                raise RuntimeError('Lifecycle retry created another rollout')
            return revision
        revision = wait_for('lifecycle ' + action + ' ready', start, seconds=300)
        lab.complete(revision)
        return current()

    def verify(expected, http_status, https_status):
        observed = lab.inventory()
        for index, c in enumerate(observed):
            doc, before = c['document'], baseline[index]['document']
            app_a = next((a for a in doc.get('applications', []) if a['id'] == first['application_id']), None)
            app_b = next((a for a in doc.get('applications', []) if a['id'] == neighbor['application_id']), None)
            binds = lambda d: [{k:l[k] for k in ('name','address','port')} for l in d.get('listeners', [])]
            if app_b != neighbor['spec'] or doc['backends'] != before['backends'] or binds(doc) != binds(before):
                raise RuntimeError('Lifecycle changed app B, unmanaged backends or a shared listener')
            if expected == 'deleted' and app_a is not None or expected != 'deleted' and (not app_a or bool(app_a.get('disabled')) != (expected == 'disabled')):
                raise RuntimeError('Wrong application lifecycle on a node')
            if not c.get('tls_listeners') or not all(l['loaded'] for l in c['tls_listeners']):
                raise RuntimeError('Lifecycle removed loaded TLS')
        for _ in range(12):
            request = urllib.request.Request('http://localhost:18081' + request_path, headers={'Host': source['spec']['hostname']})
            try:
                with urllib.request.urlopen(request, timeout=4) as response:
                    actual = response.status
                    if expected == 'active' and response.headers.get('X-Demo-Backend') != 'app-a':
                        raise RuntimeError('HTTP routed to the wrong application')
            except urllib.error.HTTPError as error:
                actual = error.code
            if actual != http_status:
                raise RuntimeError(f'App A HTTP returned {actual}, expected {http_status}')
            # HTTP retains the demo's unmanaged default_backend. HTTPS has an
            # owned route allowlist and rejects the removed route instead.
            lab.https(request_path, backend='app-a' if expected == 'active' else None, status=https_status)
            lab.request(neighbor_path, 'app-b')
            lab.https(neighbor_path, backend='app-b')
        print(f'PASS: app A {expected} (HTTP {http_status}, HTTPS {https_status}); app B, shared listener, loaded TLS and unmanaged backends preserved on both nodes.', flush=True)

    with ThreadPoolExecutor(max_workers=1) as pool:
        traffic_task = pool.submit(traffic)
        try:
            previous = first
            phases = [('disable','disabled',503,503), ('enable','active',200,200), ('delete','deleted',200,404), ('restore','active',200,200)]
            if watch:
                phases = phases[{'active': 0, 'disabled': 1, 'deleted': 3}[state(source)]:]
            for action, expected, http_status, https_status in phases:
                print('Waiting for console ' + action if watch else 'Starting ' + action, flush=True)
                if watch:
                    def finished():
                        r = current()
                        if r['number'] <= previous['number']:
                            return None
                        if r['status'] in ('rolled_back','rollback_failed'):
                            raise RuntimeError('Console lifecycle rolled back: ' + r['status'])
                        return r if r['status'] == 'succeeded' and state(r) == expected else None
                    previous = wait_for('console ' + action, finished, seconds=600)
                else:
                    previous = mutate(action, previous)
                verify(expected, http_status, https_status)
        finally:
            stop.set()
            traffic_task.result()
            print(f'App B traffic: {count} successful HTTP/HTTPS responses; {len(failures)} request failures.', flush=True)
    if failures:
        raise RuntimeError(f'{len(failures)} app B request failures during lifecycle updates')
    print(f'PASS: {count} uninterrupted app B HTTP/HTTPS requests; lifecycle history retained and app A restored.', flush=True)


if __name__ == '__main__':
    run(watch='--watch-console' in sys.argv)
