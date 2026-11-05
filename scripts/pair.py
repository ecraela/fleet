#!/usr/bin/env python3
"""Real HAProxy pair: private enrollment, traffic distribution and node failover."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

from demo import Client, prepare as prepare_database, prepare_browser, wait_for, write_private

ROOT = Path(os.getenv('HAPROXY_DEMO_ROOT') or Path(__file__).resolve().parents[1]).resolve()
STATE = ROOT / 'deploy/local/state.local'
PAIR = STATE / 'pair'


def selected_state():
    active = PAIR / 'active-organization'
    organization = os.getenv('HAPROXY_DEMO_ORGANIZATION_ID') or (
        active.read_text().strip() if active.exists() else (STATE / 'organization-id').read_text().strip()
    )
    organization = str(uuid.UUID(organization))
    return PAIR / organization


def client():
    folder = selected_state()
    result = Client(STATE)
    result.organization = folder.name
    result.token = (folder / 'operator-token').read_text().strip()
    return result


def retain_demo_images(command, env):
    """Keep exact container images recoverable when a shared build tag moves."""
    def read(args):
        result = subprocess.run(args, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
        if result.returncode:
            raise RuntimeError('Cannot retain the local demo images before rebuilding')
        return result.stdout.decode().strip()

    containers = read(command + ['ps', '-a', '-q']).split()
    if not containers:
        return
    images = read(['docker', 'inspect', '--format', '{{.Image}}', *containers]).split()
    if len(images) != len(containers):
        raise RuntimeError('Incomplete local demo image inventory')
    for image in sorted(set(images)):
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
            raise RuntimeError('Unexpected local demo image identity')
        # Linux's containerd image store may drop an untagged image reference
        # even while an older container still runs from its snapshot.
        read(['docker', 'image', 'tag', image, 'converge-haproxy-demo-retained:' + image.removeprefix('sha256:')])


def compose(*args, check=True):
    env = dict(os.environ, HAPROXY_UID=str(os.getuid()), HAPROXY_GID=str(os.getgid()), HAPROXY_PAIR_STATE=str(selected_state()))
    command = ['docker', 'compose', '--project-directory', str(ROOT), '--env-file', str(STATE / 'compose.env'), '-f', str(ROOT / 'compose.yaml')]
    if os.getenv('HAPROXY_PAIR_STANDALONE', '1') != '1':
        command += ['-f', str(ROOT / 'compose.console.yaml')]
    command += ['-f', str(ROOT / 'compose.pair.yaml')]
    if (selected_state() / 'acme/enabled').exists():
        command += ['-f', str(ROOT / 'compose.acme.yaml')]
    if (PAIR / 'release-overrides.json').exists():
        command += ['-f', str(PAIR / 'release-overrides.json')]
        args = tuple(a for a in args if a != '--build')
    if (selected_state() / 'geo/compose.json').exists():
        command += ['-f', str(selected_state() / 'geo/compose.json')]
    if (selected_state() / 'traffic/compose.json').exists():
        command += ['-f', str(selected_state() / 'traffic/compose.json')]
    if args and (args[0] == 'build' or '--build' in args):
        retain_demo_images(command, env)
    command += list(args)
    return subprocess.run(command, env=env, check=check)


def prepare():
    STATE.mkdir(mode=0o700, exist_ok=True)
    if not (STATE / 'admin-token').exists():
        env = dict(os.environ, HAPROXY_UID=str(os.getuid()), HAPROXY_GID=str(os.getgid()))
        subprocess.run(['docker','compose','--project-directory',str(ROOT),'-f',str(ROOT / 'compose.yaml'),'run','--build','--rm','--no-deps','init'], env=env, check=True)
    prepare_database(STATE)
    PAIR.mkdir(mode=0o700, exist_ok=True)
    folder = selected_state()
    folder.mkdir(mode=0o700, exist_ok=True)
    from tls_fixtures import prepare_assets
    prepare_assets(folder)
    from probes import prepare_assets as prepare_probes
    prepare_probes(folder)
    for letter in ('a', 'b', 'entrypoint'):
        (folder / f'agent-{letter}').mkdir(mode=0o700, exist_ok=True)
    token_file = folder / 'operator-token'
    if not token_file.exists():
        write_private(token_file, secrets.token_urlsafe(32) + '\n')
    for letter in ('a', 'b', 'entrypoint'):
        credentials_path = folder / f'dataplane-{letter}.json'
        if not credentials_path.exists():
            write_private(credentials_path, json.dumps({'username': 'agent', 'password': secrets.token_urlsafe(32)}) + '\n')
        credentials = json.loads(credentials_path.read_text())
        configuration = {
            'config_version': 2, 'name': f'haproxy-{letter}',
            'dataplaneapi': {
                'scheme': ['unix'], 'socket_path': '/run/haproxy/dataplane.sock',
                'read_timeout': '5s', 'write_timeout': '15s', 'max_body_size': '2MiB',
                'user': [{'name': credentials['username'], 'password': credentials['password'], 'insecure': True}],
                'transaction': {'transaction_dir': '/var/lib/haproxy/dpa/transactions', 'backups_dir': '/var/lib/haproxy/dpa/backups', 'backups_number': 20, 'max_open_transactions': 4},
                'resources': {name: '/var/lib/haproxy/dpa/' + directory for name, directory in [('dataplane_storage_dir','storage'),('general_storage_dir','storage'),('maps_dir','maps'),('ssl_certs_dir','certs'),('spoe_dir','spoe'),('spoe_transaction_dir','spoe-transactions'),('waf_dir','waf')]},
            },
            'haproxy': {'config_file': '/var/lib/haproxy/haproxy.cfg', 'haproxy_bin': '/usr/local/sbin/haproxy',
                'master_worker_mode': True, 'master_runtime': '/run/haproxy/master.sock',
                'reload': {'reload_strategy': 'custom', 'reload_cmd': '/bin/false', 'restart_cmd': '/bin/false', 'reload_delay': 1}},
            'log_targets': [{'log_to': 'stdout', 'log_level': 'warning', 'log_types': ['app']}],
        }
        # JSON is valid YAML. Credentials stay in private node-local files.
        write_private(folder / f'dataplane-{letter}.yaml', json.dumps(configuration) + '\n')
    registry = PAIR / 'operators.json'
    operators = json.loads(registry.read_text()) if registry.exists() else []
    digest = hashlib.sha256(token_file.read_text().strip().encode()).hexdigest()
    operators = [item for item in operators if item['organization_id'] != folder.name]
    # The installation operator already covers its own organization.
    installation = (STATE / 'organization-id').read_text().strip()
    if folder.name != installation:
        operators.append({'organization_id': folder.name, 'token_sha256': digest})
    else:
        write_private(token_file, (STATE / 'admin-token').read_text())
    write_private(registry, json.dumps(operators) + '\n')
    write_private(PAIR / 'active-organization', folder.name + '\n')
    prepare_browser(STATE)


def enroll():
    api, folder = client(), selected_state()
    wait_for('pair manager', lambda: api.request('/healthz', authorized=False))
    cluster_path = folder / 'cluster-id'
    if not cluster_path.exists():
        cluster = api.request('/v1/clusters', {'name': 'Local two-node HAProxy demo'})['cluster']
        write_private(cluster_path, cluster['id'] + '\n')
    cluster = str(uuid.UUID(cluster_path.read_text().strip()))
    entrypoint_path = folder / 'entrypoint-cluster-id'
    if not entrypoint_path.exists():
        entrypoint_cluster = api.request('/v1/clusters', {'name': 'Local pair entrypoint'})['cluster']
        write_private(entrypoint_path, entrypoint_cluster['id'] + '\n')
    for letter in ('a', 'b', 'entrypoint'):
        if (folder / f'agent-{letter}/identity.json').exists():
            # Keep identity and history across restarts; do not issue unused tokens.
            continue
        enrollment_cluster = entrypoint_path.read_text().strip() if letter == 'entrypoint' else cluster
        result = api.request(f'/v1/clusters/{enrollment_cluster}/enrollment-tokens', {'ttl_seconds': 600})
        write_private(folder / f'token-{letter}', result['token'] + '\n')


def nodes():
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    candidates = client().request('/v1/nodes')['nodes']
    return {n['name']: n for n in candidates if n['cluster_id'] == cluster and not n.get('revoked_at')}


def metrics(name, *, up=True):
    node = nodes().get(name)
    if not node:
        return None
    result = client().request(f"/v1/nodes/{node['id']}/metrics")
    return result if result.get('fresh') and result.get('scrape_ok') == up else None


def topology():
    pair = wait_for('both enrolled pair nodes', lambda: (n if len(n := nodes()) == 2 else None))
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    entrypoint_cluster = (selected_state() / 'entrypoint-cluster-id').read_text().strip()
    def entrypoint_node():
        return next((n for n in client().request('/v1/nodes')['nodes'] if n['cluster_id'] == entrypoint_cluster and not n.get('revoked_at')), None)
    controller = wait_for('entrypoint agent enrollment', entrypoint_node)
    declared = {
        'entrypoint': 'http://localhost:18081/', 'strategy': 'roundrobin',
        'frontend': 'public', 'backend': 'app-a:8080, app-b:8080',
        'nodes': [{'node_id': pair[f'haproxy-{letter}']['id'], 'address': f'haproxy-{letter}:8080'} for letter in ('a', 'b')],
        'control': {'node_id': controller['id'], 'backend': 'haproxy_nodes', 'servers': [{'node_id': pair[f'haproxy-{letter}']['id'], 'server': f'haproxy-{letter}'} for letter in ('a', 'b')]},
    }
    existing = next((c for c in client().request('/v1/clusters')['clusters'] if c['id'] == cluster), {})
    if existing.get('topology') != declared:
        client().request(f'/v1/clusters/{cluster}/topology', declared, method='PUT')
    for name in pair:
        wait_for(f'{name} fresh HAProxy metrics', lambda name=name: metrics(name))


def request_backend(path='/', timeout=4, started=None):
    try:
        response = urllib.request.urlopen('http://localhost:18081' + path, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        if started is not None:
            started(response.headers.get('X-HAProxy-Node'))
        if b'Converge HAProxy demo backend' not in response.read():
            raise RuntimeError('Request did not reach the application backend')
        return response.headers.get('X-HAProxy-Node'), response.headers.get('X-Demo-Backend'), response.status


def request(path='/', timeout=4):
    node, _, status = request_backend(path, timeout)
    return node, status


def smoke():
    topology()
    distribution = Counter(request()[0] for _ in range(40))
    if set(distribution) != {'haproxy-a', 'haproxy-b'} or min(distribution.values()) < 10:
        raise SystemExit(f'Unexpected distribution: {dict(distribution)}')
    if len({n['id'] for n in nodes().values()}) != 2:
        raise SystemExit('Pair did not enroll two separate identities')
    print(f'PASS: one entrypoint, two HAProxy processes, two identities and fresh metrics: {dict(distribution)}')
    print('Traffic: http://localhost:18081/; console: http://localhost:5173/app/organizations/' + client().organization + '/haproxy')


def failover():
    smoke()
    for stopped, survivor in [('haproxy-a', 'haproxy-b'), ('haproxy-b', 'haproxy-a')]:
        # Exercise continuous safe GETs across the stop, detection and recovery.
        def traffic():
            results = []
            for _ in range(100):
                results.append(request())
                time.sleep(0.1)
            return results
        with ThreadPoolExecutor(max_workers=1) as pool:
            running = pool.submit(traffic)
            try:
                compose('stop', '-t', '1', stopped)
                wait_for(f'{stopped} failed scrape reported by its running agent', lambda: metrics(stopped, up=False))
                distribution = Counter(request()[0] for _ in range(20))
                if distribution != Counter({survivor: 20}):
                    raise SystemExit(f'Traffic did not fail over to {survivor}: {dict(distribution)}')
                results = running.result()
                if any(status != 200 for _, status in results):
                    raise SystemExit('A GET failed during failover')
                print(f'PASS: stopped {stopped}; {survivor} served all post-detection traffic; 100 continuous GETs succeeded.')
            finally:
                compose('start', stopped)
            wait_for(f'{stopped} metrics after recovery', lambda: metrics(stopped))
            wait_for('both proxies rejoining traffic', lambda: set(request()[0] for _ in range(10)) == {'haproxy-a', 'haproxy-b'})
    smoke()
    print('PASS: both node failures and recovery. Existing TCP sessions on a killed node are not preserved.')


def operations():
    """Direct authorized runtime operations, verified against real routed traffic."""
    smoke()
    api, pair = client(), nodes()

    def discovery(name):
        value = api.request(f"/v1/nodes/{pair[name]['id']}/discovery")
        return value if value['fresh'] and value['management_enabled'] else None

    def fresh(name):
        return wait_for(f'{name} runtime discovery', lambda: discovery(name))

    def operation(name, kind, server='app-a', weight=0):
        value = fresh(name)
        observed = value['control']['discovery']
        target = next(s for s in observed['servers'] if s['backend'] == 'application' and s['name'] == server)
        body = {
            'request_id': str(uuid.uuid4()), 'kind': kind, 'backend': 'application',
            'server': server, 'weight': weight, 'expected_generation': observed['generation'],
            'expected_state': target['state'], 'expected_weight': target['weight'],
        }
        endpoint = f"/v1/nodes/{pair[name]['id']}/operations"
        created = api.request(endpoint, body)['operation']
        duplicate = api.request(endpoint, body)['operation']
        if duplicate['id'] != created['id']:
            raise RuntimeError('A repeated request created a second operation')

        def completed():
            current = api.request(f"/v1/nodes/{pair[name]['id']}/discovery")
            found = next((op for op in current['operations'] if op['id'] == created['id']), None)
            if found and found['status'] in {'failed', 'unknown'}:
                raise RuntimeError(f"{kind} ended {found['status']}: {found['error_code']}")
            return found if found and found['status'] == 'succeeded' else None
        result = wait_for(f'{name} {kind} {server} verified', completed)
        # The result and current inventory can arrive in adjacent heartbeats.
        wait_for(f'{name} observed {kind}', lambda: (
            current if (current := discovery(name)) and any(
                s['name'] == server and s['backend'] == 'application' and
                (s['weight'] == weight if kind == 'weight' else s['state'] == ('drain' if kind == 'drain' else 'ready'))
                for s in current['control']['discovery']['servers']) else None))
        return result

    def counts(total=120):
        result = Counter((node, backend) for node, backend, status in (request_backend() for _ in range(total)) if status == 200)
        if sum(result.values()) != total:
            raise RuntimeError('Application requests failed during runtime changes')
        return result

    try:
        for name in sorted(pair):
            fresh(name)
            operation(name, 'drain')
            routed = counts()
            if routed[(name, 'app-a')] or routed[(name, 'app-b')] < 30:
                raise RuntimeError(f'Drain did not stop normal selection on {name}: {dict(routed)}')
            other = next(n for n in pair if n != name)
            if not routed[(other, 'app-a')] or not routed[(other, 'app-b')]:
                raise RuntimeError('A node-local change affected the other proxy')
            try:
                operation(name, 'drain', 'app-b')
            except urllib.error.HTTPError as error:
                if error.code != 409:
                    raise
            else:
                raise RuntimeError('The last healthy backend server could be drained')
            operation(name, 'restore')
            operation(name, 'weight', weight=200)
            routed = counts(240)
            ratio = routed[(name, 'app-a')] / max(1, routed[(name, 'app-b')])
            if not 1.6 <= ratio <= 2.4:
                raise RuntimeError(f'Unexpected 200:100 weighted distribution: {dict(routed)}')
            operation(name, 'weight', weight=100)
            print(f'PASS: {name} direct drain/restore, last-server guard, idempotent requests and 200:100 weights ({ratio:.2f}:1 actual).', flush=True)

        before = {name: node['id'] for name, node in pair.items()}
        for service in ('agent-a', 'manager'):
            connections = {name: fresh(name)['control']['connected_at'] for name in pair}
            compose('restart', '-t', '2', service)
            wait_for('manager after restart', lambda: api.request('/healthz', authorized=False))
            for name in pair:
                # Compose can return before the outbound stream reconnects. A
                # still-fresh observation from the previous stream is not proof
                # that a write is safe on the replacement connection.
                changed = service == 'manager' or name == 'haproxy-a'
                wait_for(name + ' new control discovery', lambda: (
                    current if (current := discovery(name)) and
                    (not changed or current['control']['connected_at'] != connections[name]) else None))
            if {name: node['id'] for name, node in nodes().items()} != before:
                raise RuntimeError('Restart changed enrolled identities')
            operation('haproxy-a', 'weight', weight=120)
            operation('haproxy-a', 'weight', weight=100)
            print(f'PASS: {service} restart reconnects the outbound stream and retains node identities/history.', flush=True)
    finally:
        for name in pair:
            for server in ('app-a', 'app-b'):
                current = fresh(name)['control']['discovery']
                target = next(s for s in current['servers'] if s['name'] == server)
                if target['weight'] != 100:
                    operation(name, 'weight', server, 100)
                if target['state'] != 'ready':
                    operation(name, 'restore', server)
    print('PASS: both proxies restored to ready servers with weight 100; application traffic continued.')


def traffic():
    seconds = max(1, min(int(os.getenv('SECONDS', '60')), 3600))
    workers = max(1, min(int(os.getenv('WORKERS', '4')), 16))
    end = time.monotonic() + seconds
    def worker(index):
        totals = Counter()
        i = index
        while time.monotonic() < end:
            status = 503 if i % 50 == 0 else 404 if i % 17 == 0 else 200
            node, code = request(f'/?bytes=8192&delay={20 + i % 80}&status={status}')
            totals[(node, code)] += 1
            i += 1
            time.sleep(0.05)
        return totals
    with ThreadPoolExecutor(max_workers=workers) as pool:
        totals = sum(pool.map(worker, range(workers)), Counter())
    print('Generated traffic by proxy and response code:', dict(totals))


def latency():
    """Assert collection-to-console-feed latency with real routed HTTP bursts."""
    api = client()
    pair_nodes = nodes()
    ids = {node['id'] for node in pair_nodes.values()}
    if len(ids) != 2:
        raise SystemExit('Start the real pair first: python3 scripts/pair.py up')
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    request_stream = urllib.request.Request(
        f'https://localhost:18443/v1/clusters/{cluster}/events?nodes=' + ','.join(sorted(ids)),
        headers={'Authorization': 'Bearer ' + api.token, 'X-Organization-ID': api.organization},
    )
    results = []
    with urllib.request.urlopen(request_stream, context=api.context, timeout=6) as response:
        def next_frame():
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                line = response.readline(1024 * 1024 + 1)
                if not line:
                    raise SystemExit('Live stream ended during latency test')
                if line.startswith(b'data: '):
                    frame = json.loads(line[6:])
                    if frame.get('cluster_id') != cluster:
                        raise SystemExit('Live stream returned another cluster')
                    if {n['node_id'] for n in frame.get('nodes', [])} == ids:
                        return frame['nodes']
            raise SystemExit('No cluster observations received')

        def code_rate(node):
            return next((point['value'] for point in node['series'] if point['metric'].get('series') == 'http_responses' and point['metric'].get('code') == '3xx'), 0)

        for trial in range(3):
            deadline = time.monotonic() + 20
            while True:
                baseline = next_frame()
                if all(n['fresh'] and n['scrape_ok'] and code_rate(n) == 0 for n in baseline):
                    break
                if time.monotonic() > deadline:
                    raise SystemExit('Need fresh idle 3xx counters for the latency test')
            previous = {n['node_id']: n['observed_at'] for n in baseline}
            started = time.monotonic()
            seen = {}
            # A 302 without Location is intentionally returned by this lab.
            # It separates the marker from ordinary 2xx/4xx/5xx demo traffic.
            with ThreadPoolExecutor(max_workers=8) as pool:
                futures = [pool.submit(request, '/?bytes=8192&status=302') for _ in range(40)]
                while len(seen) < 2:
                    for node in next_frame():
                        if node['fresh'] and node['scrape_ok'] and node['observed_at'] > previous[node['node_id']] and code_rate(node) > 0:
                            seen.setdefault(node['node_id'], time.monotonic() - started)
                    if time.monotonic() - started > 5:
                        raise SystemExit(f'Live traffic exceeded five seconds: {seen}')
                for future in futures:
                    future.result()
            elapsed = max(seen.values())
            results.append(elapsed)
            print(f'PASS: burst {trial + 1} reached the live chart feed for both nodes in {elapsed:.2f}s', flush=True)
    print(f'PASS: worst live latency {max(results):.2f}s across three real traffic bursts (limit 5s).')
    return results


def main():
    action = sys.argv[1] if len(sys.argv) == 2 else ''
    if action == 'up':
        prepare()
        compose('build', 'manager', 'haproxy-a', 'pair-backend')
        compose('up', '-d', 'postgres', 'victoriametrics', 'manager', 'ingest', 'pair-backend', 'pair-backend-2', 'pair-config-init')
        for letter in ('a', 'b'):
            compose('up', '-d', '--wait', f'haproxy-{letter}')
        compose('up', '-d', 'pair-entrypoint')
        enroll()
        compose('up', '-d', 'agent-a', 'agent-b', 'agent-entrypoint')
        smoke()
    elif action in {'smoke', 'failover', 'traffic', 'latency', 'operations'}:
        {'smoke': smoke, 'failover': failover, 'traffic': traffic, 'latency': latency, 'operations': operations}[action]()
    elif action in {'stop-a', 'stop-b', 'start-a', 'start-b'}:
        verb, letter = action.split('-')
        compose(verb, f'haproxy-{letter}')
    elif action == 'logs':
        compose('logs', '--tail=100', 'manager', 'ingest', 'agent-a', 'agent-b', 'agent-entrypoint', 'haproxy-a', 'haproxy-b', 'pair-entrypoint', 'pair-backend', 'pair-backend-2')
    elif action == 'status':
        compose('ps')
    elif action == 'down':
        compose('stop', 'agent-a', 'agent-b', 'agent-entrypoint', 'pair-entrypoint', 'haproxy-a', 'haproxy-b', 'pair-backend')
    elif action == 'shutdown':
        compose('down')
    else:
        raise SystemExit('Usage: pair.py {up|smoke|failover|traffic|latency|operations|stop-a|stop-b|start-a|start-b|logs|status|down|shutdown}')


if __name__ == '__main__':
    main()
