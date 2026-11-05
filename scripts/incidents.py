#!/usr/bin/env python3
"""Observe real app failures, recovery and agent loss without changing routing."""
from concurrent.futures import ThreadPoolExecutor
import json
import ssl
import sys
from threading import Event
import time

from demo import wait_for, write_private
from listeners import Listeners, local_dpa
from pair import compose, selected_state


def refresh_demo_dns():
    """Re-resolve Docker hostnames after recreating synthetic app containers."""
    lab = Listeners()
    for n in lab.nodes.values():
        c = lab.api.request(f"/v1/nodes/{n['id']}/configuration")['control']
        cfg = (c.get('discovery') or {}).get('configuration') or {}
        if c.get('active_rollout_id') or c.get('maintenance_id') or not cfg.get('document'):
            raise RuntimeError('Finish demo operations before refreshing local DNS')
        apps = cfg['document'].get('applications', [])
        if len(apps) != 2 or any(a['hostname'] != 'apps.demo.test' or a['name'] not in ('Store demo', 'Admin demo') or any(s['address'] not in ('pair-backend', 'pair-backend-2') for s in a['servers']) for a in apps):
            raise RuntimeError('Preserve custom application settings before this demo-only reload')
    for letter in ('a', 'b'):
        f, headers = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true', letter=letter)
        transaction, _ = local_dpa('/services/haproxy/transactions?version=' + headers['configuration-version'], {}, 'POST', letter=letter)
        tid = transaction['id']
        committed = False
        try:
            # The exact existing section is retained. A graceful reload refreshes
            # startup DNS resolutions without editing compiled backend ownership.
            local_dpa('/services/haproxy/configuration/frontends/public?full_section=true&transaction_id=' + tid, f, 'PUT', letter=letter)
            _, committed_headers = local_dpa('/services/haproxy/transactions/' + tid + '?force_reload=true', method='PUT', letter=letter)
            committed = True
            reload_id = committed_headers.get('reload-id')
            if reload_id:
                wait_for('demo node graceful reload', lambda: local_dpa('/services/haproxy/reloads/' + reload_id, letter=letter)[0]['status'] == 'succeeded')
        finally:
            if not committed:
                local_dpa('/services/haproxy/transactions/' + tid, method='DELETE', letter=letter)
    print('PASS: demo-only graceful reloads refreshed Docker backend addresses without changing routes or TLS.', flush=True)


def run():
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    trust = selected_state() / 'acme/public/trust.pem'
    if trust.exists():
        lab.context = ssl.create_default_context(cafile=trust)
    apps = lab.revisions()
    def effective(name):
        rows = [r for r in apps if r['spec']['name'] == name]
        if not rows:
            raise RuntimeError('Start with the existing Store demo and Admin demo')
        current = max(rows, key=lambda r: r['number'])
        if current['status'] in ('rolled_back', 'rollback_failed'):
            current = max((r for r in rows if r['status'] == 'succeeded'), key=lambda r: r['number'])
        return current
    a, b = effective('Store demo'), effective('Admin demo')
    for r, prefix in ((a, '/shop'), (b, '/shop/admin/v2')):
        s = r['spec']
        if s['hostname'] != 'apps.demo.test' or s['path_prefix'] != prefix or s.get('disabled') or r.get('deleted') or any(server['address'] not in ('pair-backend', 'pair-backend-2') for server in s['servers']):
            raise RuntimeError('Preserve custom apps before running the synthetic incident lab')
    if any(c.get('active_rollout_id') or c.get('maintenance_id') for c in (
        lab.api.request(f"/v1/nodes/{n['id']}/configuration")['control'] for n in lab.nodes.values()
    )):
        raise RuntimeError('Finish active configuration or maintenance work first')
    before = lab.inventory()
    path = f'/v1/clusters/{lab.cluster}/incidents'
    def page():
        value = lab.api.request(path)
        if value['notifications']['configured']:
            raise RuntimeError('The incident demo requires console-only alerts; preserve configured notification destinations')
        return value
    wait_for('background incident evaluation', lambda: page()['evaluated_at_ms'] > 0)
    wait_for('both demo apps recover before injecting faults', lambda: not any(i['rule']=='application_health' and i['status']=='active' for i in page()['incidents']), seconds=90)
    def app_incidents(status):
        value = page()
        if any(i['rule'] == 'application_health' and i['resource_id'] == b['application_id'] and i['status'] == 'active' for i in value['incidents']):
            raise RuntimeError('App B acquired an incident during the app A fault')
        result = [i for i in value['incidents'] if i['rule'] == 'application_health' and i['resource_id'] == a['application_id'] and i['scope'] == 'entrypoint' and i['status'] == status]
        return result if {i['scheme'] for i in result} == {'http', 'https'} else None
    def fault(enabled):
        for backend in ('pair-backend', 'pair-backend-2'):
            compose('exec', '-T', backend, *(['touch', '/tmp/store-fault'] if enabled else ['rm', '-f', '/tmp/store-fault']))
    stop, failures = Event(), []
    count = 0
    evidence = {}
    def traffic():
        nonlocal count
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users', 'app-b')
                lab.https('/shop/admin/v2/users', backend='app-b')
                count += 2
            except Exception as error:
                failures.append(type(error).__name__)
            stop.wait(.05)
    with ThreadPoolExecutor(max_workers=1) as pool:
        running = pool.submit(traffic)
        agent_stopped = False
        try:
            fault(True)
            opened = wait_for('Store HTTP and HTTPS incidents', lambda: app_incidents('active'), seconds=90)
            ids = {i['id'] for i in opened}
            evidence['opened'] = opened
            print('PASS: app A HTTP and HTTPS failures opened grouped entrypoint incidents; app B remains healthy.', flush=True)
            compose('restart', 'manager')
            wait_for('manager health after restart', lambda: lab.api.request('/healthz', authorized=False))
            retained = wait_for('durable active incidents after restart', lambda: app_incidents('active'))
            if {i['id'] for i in retained} != ids:
                raise RuntimeError('Manager restart replaced incident identities')
            print('PASS: manager restart retained active incidents and the historical timeline.', flush=True)
            fault(False)
            recovered = wait_for('fresh passing evidence resolves app A', lambda: app_incidents('resolved'), seconds=90)
            if {i['id'] for i in recovered} != ids:
                raise RuntimeError('Recovery lost incident identity')
            evidence['recovered'] = recovered
            print('PASS: advancing healthy HTTP/HTTPS observations resolved the incidents.', flush=True)
            fault(True)
            reopened = wait_for('repeated failures reopen the same incidents', lambda: app_incidents('active'), seconds=90)
            if {i['id'] for i in reopened} != ids or any(i['occurrences'] != j['occurrences'] + 1 for i in reopened for j in opened if i['id'] == j['id']):
                raise RuntimeError('Flapping duplicated incident identities or lost occurrences')
            fault(False)
            wait_for('app A recovers again', lambda: app_incidents('resolved'), seconds=90)
            print('PASS: repeated failures reused the same incident groups and recorded reopen/recovery.', flush=True)
            node = lab.nodes['haproxy-a']['id']
            compose('stop', '-t', '1', 'agent-a')
            agent_stopped = True
            def offline():
                return next((i for i in page()['incidents'] if i['rule'] == 'agent_offline' and i['resource_id'] == node and i['status'] == 'active'), None)
            evidence['offline'] = wait_for('offline agent incident', offline, seconds=100)
            compose('start', 'agent-a')
            agent_stopped = False
            wait_for('agent metrics and heartbeat resolve the incident', lambda: not offline(), seconds=90)
            print('PASS: agent loss and recovery were detected while both HAProxy processes kept serving.', flush=True)
        finally:
            fault(False)
            if agent_stopped:
                compose('start', 'agent-a')
            stop.set()
            running.result(timeout=10)
    if failures:
        raise RuntimeError(f'App B had {len(failures)} failures across {count} successful requests')
    after = lab.inventory()
    if [c['document'] for c in before] != [c['document'] for c in after]:
        raise RuntimeError('The incident lab changed a routing or shared TLS document')
    evidence['final'] = page()
    evidence['app_b_requests'] = count
    evidence['app_b_failures'] = len(failures)
    write_private(selected_state() / 'incidents-evidence.json', json.dumps(evidence, indent=2))
    print(f'PASS: app B served {count} HTTP/trusted-HTTPS requests with 0 failures. Shared listeners/TLS and both application definitions were preserved.', flush=True)


if __name__ == '__main__':
    if '--prepare' in sys.argv:
        refresh_demo_dns()
    else:
        run()
