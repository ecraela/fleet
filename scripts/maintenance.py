#!/usr/bin/env python3
"""Real whole-node withdrawal, connection drain and safe explicit rejoin."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import queue
import time
import urllib.error
import uuid

from demo import wait_for
from pair import client, compose, nodes, request_backend, selected_state, smoke as pair_smoke
from rollout import backends_ready


def restarted_entrypoint_ready(view, after_ms):
    discovery = (view.get('controller') or {}).get('discovery') or {}
    configuration = discovery.get('configuration') or {}
    if min(discovery.get('observed_at_ms', 0), configuration.get('observed_at_ms', 0)) <= after_ms:
        return False
    if discovery.get('error_code') or configuration.get('error_code'):
        return False
    servers = {s['name']: s for s in discovery.get('servers', []) if s['backend'] == view['backend']}
    target = servers.get(view['server']) or {}
    if target.get('state') != 'maint' or (view.get('maintenance') or {}).get('status') != 'maintenance':
        raise RuntimeError('The restarted entrypoint did not preserve node withdrawal')
    peers = [s for name, s in servers.items() if name != view['server']]
    return bool(peers) and all(s.get('state') == 'ready' and s.get('health') == 'UP' for s in peers)


def rejoin_target_ready(snapshot):
    if not snapshot.get('fresh') or not snapshot.get('configuration_enabled'):
        return False
    discovery = (snapshot.get('control') or {}).get('discovery') or {}
    frontends = [p for p in discovery.get('proxies', []) if p['kind'] == 'frontend']
    return bool(frontends) and all(p.get('status') == 'OPEN' for p in frontends) and backends_ready(discovery)


def smoke():
    pair_smoke()
    api, pair = client(), nodes()
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    paths = {name: f"/v1/nodes/{node['id']}/maintenance" for name, node in pair.items()}

    def view(name):
        return api.request(paths[name])

    def fresh(name):
        def read():
            v = view(name)
            controls = [v['target'], v['controller']]
            d = v['controller'].get('discovery') or {}
            cfg = d.get('configuration') or {}
            controller_ready = not d.get('error_code') and len(d.get('generation', '')) == 64 and cfg.get('document') and not cfg.get('error_code') and time.time()*1000-cfg.get('observed_at_ms', 0) < 5000
            return v if not v['reason_code'] and controller_ready and all(c.get('discovery') and time.time()*1000-c['discovery']['observed_at_ms'] < 5000 for c in controls) else None
        return wait_for(name+' maintenance discovery', read)

    def change(name, action, timeout=30):
        v = fresh(name)
        target, controller = v['target']['discovery'], v['controller']['discovery']
        cfg = controller['configuration']
        current = v.get('maintenance')
        body = {'request_id': str(uuid.uuid4()), 'action': action, 'binding_id': v['binding_id'], 'expected_generation': target.get('generation') or (current or {}).get('target_generation', ''), 'expected_entrypoint_generation': controller['generation'], 'expected_artifact_id': cfg['artifact_id'], 'expected_version': cfg['version'], 'drain_timeout_seconds': timeout}
        if current and current['status'] not in {'completed', 'failed'}:
            body['maintenance_id'] = current['id']
        result = api.request(paths[name], body)['maintenance']
        if api.request(paths[name], body)['maintenance']['id'] != result['id']:
            raise RuntimeError('Request retry duplicated maintenance')
        return result

    def stage(name, status, seconds=90):
        def read():
            m = view(name).get('maintenance')
            if m and m['status'] == 'attention' and status != 'attention':
                raise RuntimeError(f"Maintenance needs attention: {m['error_code']} at {m['step']}")
            return m if m and m['status'] == status else None
        return wait_for(name+' '+status, read, seconds=seconds)

    def only(name, total=30):
        counts = Counter(request_backend()[0] for _ in range(total))
        if counts != Counter({name: total}):
            raise RuntimeError(f'Withdrawn node received new traffic: {dict(counts)}')

    def healthy_target(name):
        v = fresh(name)
        snapshot = api.request(f"/v1/nodes/{pair[name]['id']}/configuration")
        return v if rejoin_target_ready(snapshot) else None

    def rejoin(name):
        for attempt in range(3):
            wait_for(name + ' saved configuration and health before rejoin', lambda: healthy_target(name))
            try:
                change(name, 'rejoin')
                break
            except urllib.error.HTTPError as error:
                if error.code != 409:
                    raise
                if attempt == 2:
                    snapshot = api.request(f"/v1/nodes/{pair[name]['id']}/configuration")
                    current = view(name)
                    maintenance = current.get('maintenance') or {}
                    saved = (current['controller'].get('discovery') or {}).get('configuration') or {}
                    print(f"Rejoin conflict: fresh={snapshot.get('fresh')}, configuration_enabled={snapshot.get('configuration_enabled')}, target_ready={rejoin_target_ready(snapshot)}, maintenance={maintenance.get('status')}, error={maintenance.get('error_code')}, entrypoint_owned={saved.get('artifact_id') == maintenance.get('owned_artifact_id')}", flush=True)
                    raise
                # A rejected fenced preflight has no side effects. Re-read it
                # after startup settles; never replay a stale write request.
                time.sleep(1)
        stage(name, 'completed')
        wait_for('both nodes in traffic', lambda: set(request_backend()[0] for _ in range(20)) == set(pair))

    active = None
    try:
        # A delayed request crosses the real shared entrypoint. Observe which
        # node owns it through its public frontend session count.
        with ThreadPoolExecutor(max_workers=1) as pool:
            entered = queue.Queue()
            running = pool.submit(request_backend, '/?stream=25', 10, entered.put)
            active = entered.get(timeout=5)
            if active not in pair:
                raise RuntimeError('Streaming response did not identify its HAProxy node')
            other = next(name for name in pair if name != active)
            change(active, 'withdraw')
            draining = stage(active, 'draining')
            if running.done() or not draining.get('connections'):
                raise RuntimeError('Did not observe drain with the original live connection')
            only(other)
            if view(active)['maintenance']['status'] == 'maintenance':
                raise RuntimeError('Maintenance completed before the live request ended')
            node, _, code = running.result()
            if node != active or code != 200:
                raise RuntimeError('The existing connection was interrupted')
        stage(active, 'maintenance')
        only(other)
        print('PASS: real in-flight HTTP request finishes while new traffic uses only the peer; zero connections verified before maintenance.', flush=True)

        # Persistent isolation must survive entrypoint and manager restarts,
        # including a stopped or freshly restarted target.
        compose('stop', '-t', '2', active)
        compose('restart', '-t', '2', 'pair-entrypoint')
        compose('restart', '-t', '2', 'manager', 'agent-entrypoint')
        wait_for('manager after restart', lambda: api.request('/healthz', authorized=False))
        restarted_at = int(time.time() * 1000)
        # A cached pre-restart discovery can still satisfy the five-second
        # freshness window while the new proxy is starting its health checks.
        wait_for('post-restart withdrawal and healthy peer', lambda: restarted_entrypoint_ready(fresh(active), restarted_at), seconds=90)
        only(other)
        try:
            change(active, 'rejoin')
        except urllib.error.HTTPError as error:
            if error.code != 409:
                raise
        else:
            raise RuntimeError('An offline target was allowed to rejoin')
        # Compose start also replays shared init dependencies, rewriting the
        # live peer/entrypoint DPA files. Restart only this withdrawn node.
        compose('up', '-d', '--no-deps', '--no-build', active)
        wait_for('restarted target is healthy', lambda: (v if (v:=fresh(active))['target']['discovery'].get('error_code') == '' and all(s['health']=='UP' for s in v['target']['discovery']['servers']) else None))
        only(other)
        rejoin(active)
        print('PASS: entrypoint, manager and agent restarts retain isolation; unhealthy rejoin refused; recovered target explicitly rejoins after health windows.', flush=True)
        active = None

        # A short drain deadline never force-closes the held request or releases
        # the persistent withdrawal. The operator can retry after it finishes.
        with ThreadPoolExecutor(max_workers=1) as pool:
            entered = queue.Queue()
            running = pool.submit(request_backend, '/?stream=25', 10, entered.put)
            active = entered.get(timeout=5)
            if active not in pair:
                raise RuntimeError('Streaming response did not identify its HAProxy node')
            other = next(name for name in pair if name != active)
            change(active, 'withdraw', timeout=5)
            stage(active, 'attention', seconds=35)
            only(other)
            if running.result()[2] != 200:
                raise RuntimeError('Drain timeout interrupted the existing request')
        change(active, 'withdraw')
        stage(active, 'maintenance')
        rejoin(active)
        active = None
        print('PASS: drain deadline leaves node withdrawn without terminating connections; explicit retry and rejoin restore both nodes.', flush=True)
    finally:
        if active:
            compose('up', '-d', '--no-deps', '--no-build', active)
            current = view(active).get('maintenance')
            if current and current['status'] in {'attention','maintenance'}:
                try:
                    rejoin(active)
                except Exception as error:
                    print(f'Node remains reserved for maintenance: {type(error).__name__}. Inspect the console before retrying.', flush=True)
    pair_smoke()
    print('PASS: both HAProxy nodes serve traffic again with durable maintenance history.', flush=True)


if __name__ == '__main__':
    smoke()
