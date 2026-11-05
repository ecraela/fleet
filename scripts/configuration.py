#!/usr/bin/env python3
"""Real, permission-authorized configuration saves, reloads and rollback."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import copy
import os
import time
import urllib.error
import uuid

from pair import client, compose, nodes, request_backend
from demo import wait_for


def smoke():
    api, pair = client(), nodes()
    names = os.getenv('HAPROXY_CONFIGURATION_NODES', 'haproxy-a,haproxy-b').split(',')
    if len(pair) != 2 or not set(names) <= set(pair):
        raise SystemExit('Start the two-node demo first: python3 scripts/pair.py up')

    def fresh(name):
        def read():
            data = api.request(f"/v1/nodes/{pair[name]['id']}/configuration")
            return data if data['fresh'] and data['configuration_enabled'] else None
        return wait_for(f'{name} saved configuration', read)

    def submit(name, document=None, restore=None):
        data = fresh(name)
        discovery = data['control']['discovery']
        state = discovery['configuration']
        body = {'request_id': str(uuid.uuid4()), 'expected_generation': discovery['generation'],
                'expected_artifact_id': state['artifact_id'], 'expected_version': state['version']}
        if document is not None:
            body['document'] = document
        endpoint = f"/v1/nodes/{pair[name]['id']}/configuration"
        if restore:
            endpoint += f"/versions/{restore['id']}/rollback"
        operation = api.request(endpoint, body)['operation']
        repeated = api.request(endpoint, body)['operation']
        if operation['id'] != repeated['id']:
            raise RuntimeError('A repeated save created another operation')

        def complete():
            current = api.request(f"/v1/nodes/{pair[name]['id']}/discovery")
            result = next((op for op in current['operations'] if op['id'] == operation['id']), None)
            if result and result['status'] in {'failed', 'unknown'}:
                raise RuntimeError(f"Configuration ended {result['status']}: {result['error_code']}")
            return result if result and result['status'] == 'succeeded' else None
        wait_for(f'{name} verified reload', complete, seconds=75)
        result = fresh(name)
        revision = next(v for v in result['versions'] if v['id'] == operation['configuration_revision_id'])
        if revision['status'] != 'applied' or result['control']['current_revision_id'] != revision['id']:
            raise RuntimeError('Verified change did not become the current saved revision')
        return result, body

    def counts(total=240):
        samples = [request_backend() for _ in range(total)]
        if any(status != 200 for _, _, status in samples):
            raise RuntimeError('Application traffic failed during configuration testing')
        return Counter((node, backend) for node, backend, _ in samples)

    for name in names:
        before = fresh(name)
        baseline = min((v for v in before['versions'] if v.get('artifact_id')), key=lambda v: v['number'])
        try:
            if before['control']['discovery']['configuration']['artifact_id'] != baseline['artifact_id']:
                before, _ = submit(name, restore=baseline)
            desired = copy.deepcopy(before['control']['discovery']['configuration']['document'])
            desired['backends'][0]['servers'][0]['weight'] = 200
            # Continue routed GETs while DPA validates, commits and reloads.
            def traffic():
                deadline = time.monotonic() + 8
                total = 0
                while time.monotonic() < deadline:
                    if request_backend()[2] != 200:
                        raise RuntimeError('A GET failed during reload')
                    total += 1
                    time.sleep(.03)
                return total
            with ThreadPoolExecutor(max_workers=1) as pool:
                continuous = pool.submit(traffic)
                saved, stale = submit(name, desired)
                uninterrupted = continuous.result()
            distribution = counts()
            ratio = distribution[(name, 'app-a')] / max(1, distribution[(name, 'app-b')])
            if not 1.6 <= ratio <= 2.4:
                raise RuntimeError(f'Saved weights did not change traffic: {dict(distribution)}')
            print(f'PASS: {name} direct save/reload, one idempotent revision, {ratio:.2f}:1 weighted traffic and {uninterrupted} uninterrupted GETs.', flush=True)

            stale['request_id'] = str(uuid.uuid4())
            try:
                api.request(f"/v1/nodes/{pair[name]['id']}/configuration", stale)
            except urllib.error.HTTPError as error:
                if error.code != 409:
                    raise
            else:
                raise RuntimeError('A stale configuration edit overwrote a newer version')

            identity = pair[name]['id']
            for service in (name, 'agent-' + name[-1]):
                previous = fresh(name)['control']
                compose('restart', '-t', '2', service)
                def after_restart():
                    result = fresh(name)
                    current = result['control']
                    changed = (current['discovery']['generation'] != previous['discovery']['generation']
                               if service == name else current['connected_at'] != previous['connected_at'])
                    loaded = any(s['name'] == 'app-a' and s['initial_weight'] == 200 for s in current['discovery']['servers'])
                    return result if changed and loaded else None
                recovered = wait_for(f'{name} new observation after restart', after_restart)
                if recovered['control']['current_revision_id'] != saved['control']['current_revision_id'] or nodes()[name]['id'] != identity:
                    raise RuntimeError('Restart lost version history or node identity')
            print(f'PASS: {name} proxy and agent restarts retain saved weights, current revision and identity.', flush=True)

            changed = copy.deepcopy(desired)
            changed['backends'][0]['algorithm'] = 'leastconn'
            changed['backends'][0]['servers'].append({'name': 'app-c', 'address': 'pair-backend-2', 'port': 8080, 'weight': 50, 'maintenance': False})
            submit(name, changed)
            counts(40)
            changed['backends'][0]['servers'][0]['maintenance'] = True
            submit(name, changed)
            routed = counts(120)
            if routed[(name, 'app-a')]:
                raise RuntimeError('Persistent maintenance still received normal traffic')
            print(f'PASS: {name} saved balancing strategy, server addition and maintenance mode are verified in the running proxy.', flush=True)

            restored, _ = submit(name, restore=baseline)
            latest = restored['versions'][0]
            if latest.get('rollback_of') != baseline['id'] or latest['number'] <= baseline['number']:
                raise RuntimeError('Rollback overwrote history instead of adding a revision')
            compose('restart', '-t', '2', name)
            previous_generation = restored['control']['discovery']['generation']
            recovered = wait_for(f'{name} rollback loaded after restart', lambda: (
                data if (data := fresh(name))['control']['discovery']['generation'] != previous_generation else None))
            if recovered['control']['discovery']['configuration']['document'] != baseline['document']:
                raise RuntimeError('Rollback did not survive another proxy restart')
            counts(40)
            print(f'PASS: {name} rollback is a new version and survives a subsequent restart; original server options are restored from the local full snapshot.', flush=True)
        finally:
            current = fresh(name)
            if current['control']['discovery']['configuration']['document'] != baseline['document']:
                submit(name, restore=baseline)
    print('PASS: saved configuration and rollback exercised without approvals; demo restored to its initial backend configuration.', flush=True)


if __name__ == '__main__':
    smoke()
