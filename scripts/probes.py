#!/usr/bin/env python3
"""Private entrypoint health evidence; one failed app must not affect its peer."""
import json
from pathlib import Path

from demo import wait_for, write_private
from pair import client, compose, selected_state


def prepare_assets(folder):
    path = Path(folder) / 'probe-policy.json'
    checks = []
    for name, prefix, path_suffix, backend in [('store', '/shop', '/items', 'app-a'), ('admin', '/shop/admin/v2', '/users', 'app-b')]:
        for scheme, port in [('http', 8080), ('https', 8443)]:
            check = dict(name=f'{name}-{scheme}', listener='public', hostname='apps.demo.test', path_prefix=prefix, path=prefix + path_suffix, scheme=scheme, address='pair-entrypoint', port=port, expected_status=200, response_header='X-Demo-Backend', response_value=backend)
            if scheme == 'https':
                check['ca_file'] = '/run/secrets/probe-ca.crt'
            checks.append(check)
    if not path.exists():
        write_private(path, json.dumps({'checks': checks}) + '\n')
    # Keep existing entrypoint checks and custom local policy intact. Each agent
    # gets separate direct destinations, so a healthy peer cannot hide a failure.
    for letter in ('a', 'b'):
        target = Path(folder) / f'probe-policy-{letter}.json'
        if target.exists():
            continue
        direct = [dict(c, name=c['name'] + '-node', scope='node', address=f'haproxy-{letter}', path=c['path_prefix'] + '/node-check') for c in checks]
        write_private(target, json.dumps({'checks': json.loads(path.read_text())['checks'] + direct}) + '\n')


def health():
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    return client().request(f'/v1/clusters/{cluster}/applications/status')['applications']


def healthy():
    rows = health()
    return rows if len(rows) == 2 and all(a['configuration'] == 'loaded' and a['reachability'] == 'reachable' and len(a['nodes']) == 2 and all(len(n['probes']) == 2 for n in a['nodes']) for a in rows) else None


def smoke():
    before = wait_for('HTTP and HTTPS app checks from both nodes', healthy, seconds=60)
    api = client()
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    revisions = api.request(f'/v1/clusters/{cluster}/applications')['revisions']
    store = next(r['application_id'] for r in revisions if r['spec']['name'] == 'Store demo')
    admin = next(r['application_id'] for r in revisions if r['spec']['name'] == 'Admin demo')
    print('PASS: HTTP and trusted HTTPS reach both applications through the entrypoint from both node locations.', flush=True)
    try:
        compose('stop', '-t', '1', 'pair-backend')
        def isolated():
            rows = {a['application_id']: a for a in health()}
            return rows if rows[store]['reachability'] == 'unhealthy' and rows[admin]['reachability'] == 'reachable' and all(a['configuration'] == 'loaded' for a in rows.values()) else None
        wait_for('Store unhealthy, Admin still reachable, configuration still loaded', isolated, seconds=40)
        print('PASS: backend failure changes Store to unhealthy without changing loaded configuration or Admin reachability.', flush=True)
    finally:
        compose('start', 'pair-backend')
    wait_for('Store and Admin recover', healthy, seconds=45)
    # Agent/session loss must expire evidence rather than preserve green status.
    try:
        compose('stop', '-t', '1', 'agent-a')
        wait_for('missing probe location becomes unknown', lambda: all(a['reachability'] == 'unknown' for a in health()), seconds=30)
        print('PASS: disconnected probe location becomes unknown instead of retaining stale success.', flush=True)
    finally:
        compose('start', 'agent-a')
    wait_for('probe location rejoins with fresh results', healthy, seconds=60)
    if len(api.request(f'/v1/clusters/{cluster}/applications')['revisions']) != len(revisions):
        raise RuntimeError('Read-only health checks changed deployment history')
    print('PASS: recovery is automatic and read-only checks preserve application revisions.', flush=True)


if __name__ == '__main__':
    smoke()
