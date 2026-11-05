#!/usr/bin/env python3
"""Exercise rolling withdrawal, real connection drainage, rejoin and recovery."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import copy
import queue
import threading
import time
import urllib.error
import uuid

from demo import wait_for
from pair import client, compose, nodes, request_backend, selected_state


def backends_ready(discovery):
    document = (discovery.get('configuration') or {}).get('document') or {}
    expected = [(b['name'], s) for b in document.get('backends', []) for s in b['servers']]
    actual = {(s['backend'], s['name']): s for s in discovery.get('servers', [])}
    for backend, desired in expected:
        observed = actual.get((backend, desired['name']))
        if observed is None:
            return False
        if desired.get('maintenance'):
            if observed.get('state') != 'maint':
                return False
        elif desired['weight'] > 0:
            check = observed.get('check_status', '').removeprefix('* ')
            if observed.get('state') != 'ready' or observed.get('health') != 'UP' or check not in ('L4OK', 'L6OK', 'L7OK', 'L7OKC') or observed.get('weight') != desired['weight']:
                return False
    return bool(expected)


def smoke():
    api, pair = client(), nodes()
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    endpoint = f'/v1/clusters/{cluster}/rollouts'
    if set(pair) != {'haproxy-a', 'haproxy-b'}:
        raise RuntimeError('Start the two-node demo first: python3 scripts/pair.py up')

    def configuration(name):
        return api.request(f"/v1/nodes/{pair[name]['id']}/configuration")

    def ready(name):
        def read():
            data = configuration(name)
            d = data['control'].get('discovery') or {}
            return data if data['fresh'] and data['configuration_enabled'] and backends_ready(d) and not d.get('error_code') and not data['control'].get('active_rollout_id') and not data['control'].get('maintenance_blocked') else None
        return wait_for(name + ' ready for a cluster update', read, seconds=60)

    def start_once(document, drain_timeout):
        inputs = []
        for name in sorted(pair):
            d = ready(name)['control']['discovery']
            cfg = d['configuration']
            inputs.append({'node_id': pair[name]['id'], 'expected_generation': d['generation'], 'expected_artifact_id': cfg['artifact_id'], 'expected_version': cfg['version']})
        def entrypoint_ready():
            v = api.request(f"/v1/nodes/{pair['haproxy-a']['id']}/maintenance")
            d = v['controller'].get('discovery') or {}
            cfg = d.get('configuration') or {}
            return v if not v['reason_code'] and not d.get('error_code') and len(d.get('generation', '')) == 64 and cfg.get('document') and backends_ready(d) and not cfg.get('error_code') and time.time()*1000-cfg.get('observed_at_ms', 0) < 5000 else None
        view = wait_for('managed entrypoint ready', entrypoint_ready)
        d = view['controller']['discovery']
        cfg = d['configuration']
        body = {'request_id': str(uuid.uuid4()), 'document': document, 'nodes': inputs, 'entrypoint': {'node_id': view['controller_node_id'], 'binding_id': view['binding_id'], 'expected_generation': d['generation'], 'expected_artifact_id': cfg['artifact_id'], 'expected_version': cfg['version']}, 'drain_timeout_seconds': drain_timeout}
        rollout = api.request(endpoint, body)['rollout']
        return rollout, body

    def start(document, drain_timeout=30):
        for attempt in range(3):
            try:
                rollout, body = start_once(document, drain_timeout)
            except urllib.error.HTTPError as error:
                if error.code != 409 or attempt == 2:
                    raise
                # A rejected snapshot never authorizes a write. Re-read all
                # fences after a control acknowledgement or health transition.
                time.sleep(1)
                continue
            if api.request(endpoint, body)['rollout']['id'] != rollout['id']:
                raise RuntimeError('Duplicate rollout request created another update')
            return rollout

    def get(rollout):
        current = next(r for r in api.request(endpoint)['rollouts'] if r['id'] == rollout['id'])
        first, second = current['nodes']
        if second.get('maintenance') and current['status'] == 'running' and (first.get('maintenance') or {}).get('status') != 'completed':
            raise RuntimeError('The second node was withdrawn before the first rejoined')
        return current

    def completed(rollout, outcome='succeeded', seconds=240):
        def read():
            r = get(rollout)
            if r['status'] in {'succeeded', 'rolled_back', 'rollback_failed'}:
                if r['status'] != outcome:
                    raise RuntimeError(f"Unexpected rollout outcome {r['status']}: {r['error_code']}; nodes {[(n['name'], n['status'], n['error_code']) for n in r['nodes']]}")
                return r
        return wait_for('cluster rollout ' + outcome, read, seconds=seconds)

    def release_streams(release):
        command = ('touch',) if release else ('rm', '-f')
        for backend in ('pair-backend', 'pair-backend-2'):
            compose('exec', '-T', backend, *command, '/tmp/release-stream')

    def streams(pool, gated=False):
        if gated:
            release_streams(False)
        entered = queue.Queue()
        path = '/?stream_gate=1' if gated else '/?stream=25'
        running = [pool.submit(request_backend, path, 10, entered.put) for _ in range(2)]
        served = {entered.get(timeout=10) for _ in running}
        if 'haproxy-a' not in served:
            raise RuntimeError('Streaming requests did not reach the first rolling target')
        return running

    def verify_drain(rollout, running, rollback=False):
        field = 'rollback_maintenance' if rollback else 'maintenance'
        operation = 'rollback_operation_id' if rollback else 'apply_operation_id'
        def live_drain():
            node = get(rollout)['nodes'][0]
            if node.get(operation):
                raise RuntimeError('Configuration started before the live connection drained')
            if all(task.done() for task in running):
                raise RuntimeError('Streaming probes finished before live drainage was observed')
            maintenance = node.get(field) or {}
            return node if maintenance.get('status') == 'draining' and maintenance.get('connections', 0) > 0 else None
        wait_for('node A draining live requests', live_drain, seconds=70)
        if any(request_backend()[0] != 'haproxy-b' for _ in range(40)):
            raise RuntimeError('The withdrawn target still received a new request')
        if rollback:
            release_streams(True)
        while not all(task.done() for task in running):
            if get(rollout)['nodes'][0].get(operation):
                raise RuntimeError('A target reload interrupted a streaming request')
            time.sleep(.2)
        if any(task.result()[2] != 200 for task in running):
            raise RuntimeError('A draining request did not complete successfully')

    def recover_reserved(rollout):
        for node in rollout['nodes']:
            if not node.get('recovery_maintenance_id'):
                continue
            path = f"/v1/nodes/{node['node_id']}/maintenance"
            view = api.request(path)
            m = view['maintenance']
            d = view['controller']['discovery']
            cfg = d['configuration']
            target = view['target']['discovery']
            if m['id'] != node['recovery_maintenance_id'] or m['status'] != 'attention' or m.get('operation_id') or cfg['artifact_id'] != m['owned_artifact_id']:
                raise RuntimeError('The test does not own a settled maintenance reservation')
            api.request(path, {'request_id': str(uuid.uuid4()), 'action': 'rejoin', 'maintenance_id': m['id'], 'binding_id': view['binding_id'], 'expected_generation': target['generation'], 'expected_entrypoint_generation': d['generation'], 'expected_artifact_id': cfg['artifact_id'], 'expected_version': cfg['version'], 'drain_timeout_seconds': 30})
            wait_for(node['name'] + ' explicit reservation recovery', lambda: api.request(path)['maintenance']['status'] == 'completed', seconds=100)

    original = copy.deepcopy(ready('haproxy-a')['control']['discovery']['configuration']['document'])
    if ready('haproxy-b')['control']['discovery']['configuration']['document'] != original:
        raise RuntimeError('The smoke expects identical starting backend settings')
    active = None
    try:
        weighted = copy.deepcopy(original)
        weighted['backends'][0]['servers'][0]['weight'] = 200
        stop = threading.Event()
        def traffic():
            total = 0
            while not stop.is_set():
                if request_backend()[2] != 200:
                    raise RuntimeError('Application traffic failed during a rolling update')
                total += 1
                time.sleep(.04)
            return total
        with ThreadPoolExecutor(max_workers=3) as pool:
            held = streams(pool)
            flowing = pool.submit(traffic)
            try:
                active = start(weighted)
                verify_drain(active, held)
                success = completed(active)
            finally:
                stop.set()
            total = flowing.result()
        if any(n['maintenance']['status'] != 'completed' for n in success['nodes']):
            raise RuntimeError('A successful rollout left a node withdrawn')
        counts = Counter((node, backend) for node, backend, status in (request_backend() for _ in range(240)) if status == 200)
        for name in pair:
            ratio = counts[(name, 'app-a')]/max(1, counts[(name, 'app-b')])
            if not 1.6 <= ratio <= 2.4:
                raise RuntimeError('Saved cluster weights did not affect routing')
        print(f'PASS: real streaming drain before reload, peer-only routing, verified rejoin before node B, idempotent request and {total} uninterrupted GETs; both nodes serve 2:1 weighted traffic.', flush=True)

        active = start(original)
        first = wait_for('first node operation queued', lambda: (n if (n := get(active)['nodes'][0]).get('apply_operation_id') else None), seconds=90)
        operation, maintenance = first['apply_operation_id'], first['maintenance']['id']
        compose('restart', '-t', '2', 'manager')
        completed(active)
        first = get(active)['nodes'][0]
        if first['apply_operation_id'] != operation or first['maintenance']['id'] != maintenance:
            raise RuntimeError('Manager restart replaced a durable operation or maintenance checkpoint')
        print('PASS: manager restart resumes the same rollout, maintenance checkpoint and node operation.', flush=True)

        unhealthy = copy.deepcopy(original)
        unhealthy['backends'][0]['servers'].append({'name': 'rollout-unhealthy', 'address': '127.0.0.1', 'port': 1, 'weight': 1, 'maintenance': False})
        active = start(unhealthy)
        failed = completed(active, 'rolled_back')
        if failed['nodes'][1].get('maintenance'):
            raise RuntimeError('Unhealthy first node did not stop the second withdrawal')
        print('PASS: failed backend health restores node A while withdrawn, then rejoins it; node B is untouched.', flush=True)

        # Fail the second step after A is already updated and serving. A must be
        # drained again before its reverse rollback, including real live streams.
        active = start(weighted)
        wait_for('first node rejoined', lambda: (r if (r := get(active))['nodes'][0]['status'] == 'applied' else None), seconds=100)
        # Freeze the next preflight while B's old observation expires. Without
        # this, a still-fresh observation can admit B to maintenance before the
        # failure arrives, correctly yielding an unverified-drain reservation.
        compose('stop', '-t', '1', 'manager')
        try:
            compose('stop', '-t', '1', 'haproxy-b')
            time.sleep(7)
            # Open the probes before recovery can withdraw A. Hold them until
            # live drainage is observed, even if manager startup takes time.
            with ThreadPoolExecutor(max_workers=2) as pool:
                try:
                    held = streams(pool, gated=True)
                    compose('start', 'manager')
                    wait_for('late failure begins reverse recovery', lambda: get(active)['status'] == 'rolling_back', seconds=65)
                    compose('start', 'haproxy-b')
                    verify_drain(active, held, rollback=True)
                finally:
                    release_streams(True)
                failed = completed(active, 'rolled_back')
        finally:
            compose('start', 'manager')
            compose('start', 'haproxy-b')
        if failed['nodes'][0].get('rollback_maintenance', {}).get('status') != 'completed':
            raise RuntimeError('Previously updated node did not drain and rejoin during rollback')
        print('PASS: later node failure triggers reverse recovery; previously rejoined A drains real streams before rollback and rejoins safely.', flush=True)

        with ThreadPoolExecutor(max_workers=2) as pool:
            held = streams(pool)
            active = start(weighted, drain_timeout=5)
            timed_out = completed(active, 'rollback_failed', seconds=70)
            if timed_out['nodes'][0].get('apply_operation_id') or timed_out['nodes'][1].get('maintenance') or not timed_out['nodes'][0].get('recovery_maintenance_id'):
                raise RuntimeError('A drain timeout changed a target or lost its reservation')
            if any(task.result()[2] != 200 for task in held):
                raise RuntimeError('Drain timeout forcibly closed a request')
        recover_reserved(timed_out)
        print('PASS: bounded drain timeout preserves live requests and hands off maintenance without reloading either node; explicit healthy rejoin releases the reservation.', flush=True)
    finally:
        if active:
            current = get(active)
            if current['status'] in {'running', 'rolling_back'}:
                current = wait_for('in-flight rollout recovery', lambda: (r if (r := get(active))['status'] in {'succeeded', 'rolled_back', 'rollback_failed'} else None), seconds=280)
            for node in current['nodes']:
                if node.get('recovery_maintenance_id'):
                    m = api.request(f"/v1/nodes/{node['node_id']}/maintenance")['maintenance']
                    if m['id'] == node['recovery_maintenance_id'] and m['status'] != 'completed':
                        raise RuntimeError('Recovery is reserved for maintenance; inspect the node before retrying this smoke')
        if any(ready(name)['control']['discovery']['configuration']['document'] != original for name in pair):
            completed(start(original))
    for name in pair:
        if ready(name)['control']['discovery']['configuration']['document'] != original:
            raise RuntimeError('Demo baseline was not restored')
    print('PASS: rolling maintenance and automatic recovery; original configuration restored and both nodes serving.', flush=True)


if __name__ == '__main__':
    smoke()
