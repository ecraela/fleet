#!/usr/bin/env python3
"""Real shared-listener applications, rolling deploy and app-scoped rollback."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import copy
import threading
import time
import urllib.request
import urllib.error
import uuid

from demo import wait_for
from pair import client, compose, nodes, selected_state


class Applications:
    def __init__(self):
        self.api, self.nodes = client(), nodes()
        self.cluster = (selected_state() / 'cluster-id').read_text().strip()
        self.path = f'/v1/clusters/{self.cluster}/applications'

    def revisions(self):
        return self.api.request(self.path)['revisions']

    def latest(self, name):
        return max((r for r in self.revisions() if r['spec']['name'] == name), key=lambda r: r['number'], default=None)

    def input(self, spec, current=None):
        targets = []
        for node in sorted(self.nodes.values(), key=lambda n: n['name']):
            def ready():
                value = self.api.request(f"/v1/nodes/{node['id']}/configuration")
                d = value['control'].get('discovery') or {}
                return d if value['fresh'] and value['configuration_enabled'] and d['configuration'].get('application_control_version') in (1, 2, 3, 4, 5, 6) and not value['control'].get('active_rollout_id') and not value['control'].get('maintenance_blocked') else None
            d = wait_for('application-capable node ' + node['name'], ready, seconds=240)
            c = d['configuration']
            targets.append({'node_id': node['id'], 'expected_generation': d['generation'], 'expected_artifact_id': c['artifact_id'], 'expected_version': c['version']})
        def entrypoint():
            v = self.api.request(f"/v1/nodes/{targets[0]['node_id']}/maintenance")
            c = (v['controller'].get('discovery') or {}).get('configuration') or {}
            return v if not v['reason_code'] and c.get('document') and time.time()*1000-c.get('observed_at_ms', 0) < 5000 else None
        view = wait_for('application entrypoint ready', entrypoint)
        d = view['controller']['discovery']
        c = d['configuration']
        return {'request_id': str(uuid.uuid4()), 'expected_revision': current['number'] if current else 0, 'spec': spec, 'nodes': targets, 'entrypoint': {'node_id': view['controller_node_id'], 'binding_id': view['binding_id'], 'expected_generation': d['generation'], 'expected_artifact_id': c['artifact_id'], 'expected_version': c['version']}}

    def start(self, spec, current=None, rollback=None):
        if current and current['status'] == 'succeeded' and not rollback and dict(spec, id=current['application_id']) == current['spec']:
            return current
        suffix = f"/{current['application_id']}/versions/{rollback['id']}/rollback" if rollback else f"/{current['application_id']}/deploy" if current else ''
        for attempt in range(3):
            body = self.input(spec, current)
            try:
                r = self.api.request(self.path + suffix, body)['revision']
                break
            except urllib.error.HTTPError as error:
                # Only a definitive precondition rejection permits a fresh input.
                # Never replay transport failures or an uncertain accepted write.
                if error.code != 409 or attempt == 2:
                    raise
                records = self.revisions()
                if current:
                    latest = max((v for v in records if v['application_id'] == current['application_id']),
                                 key=lambda v: v['number'], default=None)
                    if not latest or latest['id'] != current['id']:
                        raise
                elif any(v['spec']['name'] == spec['name'] for v in records):
                    raise
                time.sleep(1)
        repeat = self.api.request(self.path + suffix, body)['revision']
        if repeat['id'] != r['id']:
            raise RuntimeError('Application retry created a second deployment')
        return r

    def complete(self, revision, outcome='succeeded'):
        if revision['status'] == 'succeeded' and outcome == 'succeeded':
            return revision
        previous = None
        def read():
            nonlocal previous
            rows = self.api.request(f'/v1/clusters/{self.cluster}/rollouts')['rollouts']
            r = next(r for r in rows if r['id'] == revision['rollout_id'])
            state = (r['status'], tuple((n['name'], n['status']) for n in r['nodes']))
            if state != previous:
                print(state, flush=True)
                previous = state
            if r['status'] in {'succeeded', 'rolled_back', 'rollback_failed'}:
                if r['status'] != outcome:
                    raise RuntimeError(f"Application outcome {r['status']}: {r['error_code']}; nodes {[(n['name'], n['status'], n['error_code']) for n in r['nodes']]}")
                return r
        return wait_for('application deployment ' + outcome, read, seconds=240)

    def request(self, path, backend=None):
        req = urllib.request.Request('http://localhost:18081' + path, headers={'Host': 'apps.demo.test'})
        with urllib.request.urlopen(req, timeout=4) as response:
            if response.status != 200 or b'Converge HAProxy demo backend' not in response.read():
                raise RuntimeError('Application request failed')
            if backend and response.headers.get('X-Demo-Backend') != backend:
                raise RuntimeError('Shared listener routed to the wrong application')
            return response.headers.get('X-HAProxy-Node')

    def verify(self, path, backend):
        distribution = Counter(self.request(path, backend) for _ in range(40))
        if set(distribution) != {'haproxy-a', 'haproxy-b'}:
            raise RuntimeError('Application is not serving through both HAProxy nodes')
        print('PASS:', path, backend, dict(distribution), flush=True)


def spec(name, path, address):
    return {'id': '', 'name': name, 'listener': 'public', 'hostname': 'apps.demo.test', 'path_prefix': path, 'algorithm': 'roundrobin', 'health_path': '/healthz', 'servers': [{'name': 'web', 'address': address, 'port': 8080, 'weight': 100, 'maintenance': False}]}


def smoke():
    app = Applications()
    # A fresh metrics sample can arrive before configuration discovery does.
    def discovered(node):
        value = app.api.request(f"/v1/nodes/{node['id']}/configuration")
        cfg = ((value['control'].get('discovery') or {}).get('configuration') or {})
        return cfg if value['fresh'] and cfg.get('document') else None
    baseline = [wait_for('initial application configuration ' + n['name'], lambda n=n: discovered(n))['document']['backends'] for n in app.nodes.values()]
    a = app.start(spec('Store demo', '/shop', 'pair-backend'), app.latest('Store demo'))
    app.complete(a)
    b = app.start(spec('Admin demo', '/shop/admin', 'pair-backend-2'), app.latest('Admin demo'))
    app.complete(b)
    app.verify('/shop/items', 'app-a')
    app.verify('/shop/admin/users', 'app-b')
    print('PASS: two applications share public; longest matching path wins.', flush=True)

    stop = threading.Event()
    def continuous():
        count = 0
        while not stop.is_set():
            app.request('/shop/admin/users', 'app-b')
            count += 1
            time.sleep(.04)
        return count
    with ThreadPoolExecutor(max_workers=1) as pool:
        traffic = pool.submit(continuous)
        try:
            changed = copy.deepcopy(a['spec'])
            changed['path_prefix'] = '/store'
            edited = app.start(changed, a)
            compose('restart', '-t', '2', 'manager')
            app.complete(edited)
        finally:
            stop.set()
        count = traffic.result()
    app.verify('/store/items', 'app-a')
    print(f'PASS: manager restart retains application deployment; {count} uninterrupted neighboring-app requests.', flush=True)

    changed_b = copy.deepcopy(b['spec'])
    changed_b['path_prefix'] = '/shop/admin/v2'
    current_b = app.start(changed_b, b)
    app.complete(current_b)
    restored = app.start({}, edited, a)
    app.complete(restored)
    app.verify('/shop/items', 'app-a')
    app.verify('/shop/admin/v2/users', 'app-b')
    print('PASS: app A rollback preserves app B’s newer revision.', flush=True)

    broken = copy.deepcopy(restored['spec'])
    broken['health_path'] = '/healthz/fail'
    rejected = app.start(broken, restored)
    app.complete(rejected, 'rolled_back')
    app.verify('/shop/items', 'app-a')
    app.verify('/shop/admin/v2/users', 'app-b')
    for index, node in enumerate(app.nodes.values()):
        cfg = app.api.request(f"/v1/nodes/{node['id']}/configuration")['control']['discovery']['configuration']['document']
        if cfg['backends'] != baseline[index] or len(cfg['applications']) != 2:
            raise RuntimeError('Application deployment changed unmanaged settings')
    print('PASS: failed HTTP health checks automatically restore serving apps; unmanaged backend settings remain intact.', flush=True)
    print('Demo routes: Host apps.demo.test; /shop/items and /shop/admin/v2/users through http://localhost:18081/', flush=True)


if __name__ == '__main__':
    smoke()
