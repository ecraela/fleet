#!/usr/bin/env python3
"""Direct node checks pass, shared-entrypoint app requests fail and roll back."""
import json
import sys

from application_verification import run
from demo import wait_for, write_private
from pair import client, compose, nodes, selected_state


def prepare():
    api = client()
    ca = selected_state() / 'acme/ca/pebble.json'
    if ca.exists() and json.loads(ca.read_text())['pebble']['profiles']['default']['validityPeriod'] < 3600:
        raise RuntimeError('Use a demo ACME lifetime of at least one hour and renew before preparing this lab. Short renewal fixtures can expire while application tests reserve the cluster.')
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    def idle():
        return all(r['status'] in ('succeeded', 'rolled_back', 'rollback_failed')
                   for r in api.request(f'/v1/clusters/{cluster}/rollouts')['rollouts'])
    wait_for('existing demo updates finish', idle, seconds=300)
    updates = []
    for letter in ('a', 'b'):
        path = selected_state() / f'probe-policy-{letter}.json'
        policy = json.loads(path.read_text())
        found = set()
        for c in policy['checks']:
            if c.get('scope') != 'node':
                continue
            if c['name'] not in ('store-http-node', 'store-https-node', 'admin-http-node', 'admin-https-node'):
                continue
            prefix = '/shop' if c['name'].startswith('store-') else '/shop/admin/v2'
            suffix = '/items' if prefix == '/shop' else '/users'
            if c['address'] != f'haproxy-{letter}' or c['path_prefix'] != prefix or c['path'] not in (prefix+suffix, prefix+'/node-check'):
                raise RuntimeError('Preserve custom local checks before preparing this lab')
            c['path'] = prefix + '/node-check'
            found.add(c['name'])
        if len(found) != 4:
            raise RuntimeError('Start with the existing two-app HTTP/HTTPS node-check lab')
        # A shared request can hit the healthy peer. Several independent checks
        # exercise round-robin selection rather than relying on one lucky path.
        for name in ('store-http', 'store-https'):
            base = next(c for c in policy['checks'] if c['name'] == name and c.get('scope', 'entrypoint') == 'entrypoint')
            for index in range(1, 4):
                copy = dict(base, name=f'{name}-gate-{index}')
                existing = next((c for c in policy['checks'] if c['name'] == copy['name']), None)
                if existing is not None and existing != copy:
                    raise RuntimeError('Preserve custom entrypoint checks before preparing this lab')
                if existing is None:
                    policy['checks'].append(copy)
        updates.append((path, policy))
    for path, policy in updates:
        write_private(path, json.dumps(policy) + '\n')
    compose('build', 'manager')
    compose('build', 'pair-fault-backend')
    compose('up', '-d', '--no-deps', 'manager', 'ingest', 'agent-a', 'agent-b', 'agent-entrypoint', 'pair-fault-backend')
    def capable():
        for node in nodes().values():
            c = api.request(f"/v1/nodes/{node['id']}/configuration")['control']
            if c.get('active_rollout_id') or c.get('maintenance_blocked'):
                return None
            d = c.get('discovery') or {}
            if d.get('configuration', {}).get('application_control_version') != 4:
                return None
            if not all(g['path'].endswith('/node-check') for g in d.get('application_probe_grants', []) if g['scope'] == 'node'):
                return None
        return True
    wait_for('both upgraded agents and local check grants', capable, seconds=300)
    print('PASS: both agents support entrypoint gates; the opt-in fault backend only fails Store’s entrypoint request path.', flush=True)


if __name__ == '__main__':
    if '--prepare' in sys.argv:
        prepare()
    else:
        run('--watch-store' in sys.argv, entrypoint=True)
