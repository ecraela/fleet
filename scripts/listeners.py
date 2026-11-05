#!/usr/bin/env python3
"""HTTPS on customer-side HAProxy nodes: real TLS, rolling rotation and rollback."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import copy
import hashlib
import http.client
import json
import socket
import ssl
import subprocess
import threading
import time
import urllib.error
import uuid

from applications import Applications
from demo import wait_for
from pair import client, compose, nodes, selected_state


def local_dpa(path, data=None, method=None, letter='entrypoint'):
    """Local lab migration only. Credentials go through stdin, never argv/logs."""
    credentials = json.loads((selected_state() / f'dataplane-{letter}.json').read_text())
    config = f'url = {json.dumps("http://localhost/v3" + path)}\nuser = {json.dumps(credentials["username"] + ":" + credentials["password"])}\n'
    if data is not None:
        config += 'header = "Content-Type: application/json"\ndata = ' + json.dumps(json.dumps(data)) + '\n'
    config += 'request = ' + json.dumps(method or ('POST' if data is not None else 'GET')) + '\n'
    result = subprocess.run(['docker', 'exec', '-i', f'converge-haproxy-agent-{letter}-1', 'curl', '--silent', '--show-error', '--include', '--unix-socket', '/run/haproxy-runtime/dataplane.sock', '--config', '-'], input=config, text=True, capture_output=True, check=True)
    head, body = result.stdout.replace('\r\n', '\n').split('\n\n', 1)
    status = int(head.splitlines()[0].split()[1])
    headers = {k.lower(): v.strip() for k, v in (line.split(':', 1) for line in head.splitlines()[1:] if ':' in line)}
    if status not in range(200, 300):
        raise RuntimeError(f'Local entrypoint API rejected {method or "GET"} {path.split("?")[0]} ({status})')
    return json.loads(body) if body else None, headers


def migrate_entrypoint():
    """Upgrade an existing HTTP lab through DPA; never overwrite its config file."""
    f, headers = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true')
    if f.get('mode') == 'tcp':
        return
    if f.get('mode', 'http') != 'http' or f.get('default_backend') != 'haproxy_nodes' or f.get('http_request_rule_list') or f.get('backend_switching_rule_list'):
        raise RuntimeError('This is not the supported local demo entrypoint')
    for rule in f.get('http_response_rule_list', []):
        if rule.get('type') != 'set-header' or rule.get('hdr_name') != 'X-Demo-Entrypoint':
            raise RuntimeError('Preserve locally customized entrypoint rules before upgrading this demo')
    for node in client().request('/v1/nodes')['nodes']:
        if node.get('revoked_at'):
            continue
        control = client().request(f"/v1/nodes/{node['id']}/configuration")['control']
        if control.get('active_rollout_id') or control.get('maintenance_id') or control.get('pending_operation_id'):
            raise RuntimeError('Finish active demo changes before upgrading its entrypoint')
    transaction, _ = local_dpa('/services/haproxy/transactions?version=' + headers['configuration-version'], {}, 'POST')
    tid = transaction['id']
    committed = False
    try:
        f['mode'], f['tcplog'] = 'tcp', True
        f.pop('httplog', None)
        f.pop('http_response_rule_list', None)
        f.setdefault('binds', {})['https_passthrough'] = {'name': 'https_passthrough', 'address': '*', 'port': 8443}
        local_dpa('/services/haproxy/configuration/frontends/public?full_section=true&transaction_id=' + tid, f, 'PUT')
        b, _ = local_dpa('/services/haproxy/configuration/backends/haproxy_nodes?full_section=true&transaction_id=' + tid)
        b['mode'] = 'tcp'
        if set(b['servers']) != {'haproxy-a', 'haproxy-b'}:
            raise RuntimeError('Unexpected demo entrypoint servers')
        for server in b['servers'].values():
            server.pop('port', None)
        local_dpa('/services/haproxy/configuration/backends/haproxy_nodes?full_section=true&transaction_id=' + tid, b, 'PUT')
        _, response_headers = local_dpa('/services/haproxy/transactions/' + tid + '?force_reload=true', method='PUT')
        committed = True
        if reload_id := response_headers.get('reload-id'):
            def loaded():
                state, _ = local_dpa('/services/haproxy/reloads/' + reload_id)
                if state['status'] == 'failed':
                    raise RuntimeError('Demo entrypoint reload failed')
                return state['status'] == 'succeeded'
            wait_for('TCP demo entrypoint reload', loaded)
    finally:
        if not committed:
            local_dpa('/services/haproxy/transactions/' + tid, method='DELETE')
    print('PASS: demo entrypoint forwards HTTP and HTTPS through the same two drainable upstream identities.', flush=True)


class Listeners(Applications):
    def __init__(self):
        super().__init__()
        self.path = f'/v1/clusters/{self.cluster}/listeners'
        self.context = ssl.create_default_context(cafile=selected_state() / 'tls/ca.crt')

    def inventory(self):
        return [self.api.request(f"/v1/nodes/{n['id']}/configuration")['control']['discovery']['configuration'] for n in self.nodes.values()]

    def listener(self, reference):
        states = self.inventory()
        certificates = [next(c for c in s['tls_certificates'] if c['reference'] == reference) for s in states]
        if len({c['sha256'] for c in certificates}) != 1:
            raise RuntimeError('Certificate identity differs across nodes')
        return dict(states[0]['listener_bindings'][0], certificate_ref=reference, certificate_sha256=certificates[0]['sha256'])

    def body(self, listeners):
        base = super().input({})
        base.pop('spec')
        base['expected_revision'] = max((r['number'] for r in self.revisions()), default=0)
        base['listeners'] = listeners
        return base

    def start_tls(self, reference=None, rollback=None):
        listeners = rollback['listeners'] if rollback else [self.listener(reference)]
        latest = max(self.revisions(), key=lambda r: r['number'], default=None)
        if not rollback and latest and latest['status'] == 'succeeded' and latest['listeners'] == listeners and all(s['document'].get('listeners', []) == listeners for s in self.inventory()):
            return latest
        body = self.body(listeners)
        suffix = f"/versions/{rollback['id']}/rollback" if rollback else ''
        r = self.api.request(self.path + suffix, body)['revision']
        if self.api.request(self.path + suffix, body)['revision']['id'] != r['id']:
            raise RuntimeError('TLS request retry created another rollout')
        return r

    def https(self, path='/shop/items', *, host='apps.demo.test', sni='apps.demo.test', backend=None, status=200, context=None):
        raw = socket.create_connection(('127.0.0.1', 18444), timeout=4)
        with (context or self.context).wrap_socket(raw, server_hostname=sni) as connection:
            digest = hashlib.sha256(connection.getpeercert(binary_form=True)).hexdigest()
            connection.sendall(f'GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n'.encode())
            response = http.client.HTTPResponse(connection)
            response.begin()
            body = response.read()
            if response.status != status or (status == 200 and b'Converge HAProxy demo backend' not in body):
                raise RuntimeError(f'HTTPS request returned {response.status}, expected {status}')
            if backend and response.getheader('X-Demo-Backend') != backend:
                raise RuntimeError('HTTPS routed to the wrong application')
            return response.getheader('X-HAProxy-Node'), digest

    def verify_tls(self, reference):
        pin = self.listener(reference)['certificate_sha256']
        for path, backend in [('/shop/items', 'app-a'), ('/shop/admin/v2/users', 'app-b')]:
            results = [self.https(path, backend=backend) for _ in range(30)]
            if {node for node, _ in results} != {'haproxy-a', 'haproxy-b'} or any(digest != pin for _, digest in results):
                raise RuntimeError('TLS identity or node distribution mismatch')
            print('PASS: trusted HTTPS', path, dict(Counter(node for node, _ in results)), reference, flush=True)

    def failover(self, reference):
        pin = self.listener(reference)['certificate_sha256']
        cluster = next(c for c in self.api.request('/v1/clusters')['clusters'] if c['id'] == self.cluster)
        controller = cluster['topology']['control']['node_id']
        for stopped, survivor in [('haproxy-a', 'haproxy-b'), ('haproxy-b', 'haproxy-a')]:
            try:
                compose('stop', '-t', '1', stopped)
                def detected():
                    view = self.api.request(f'/v1/nodes/{controller}/discovery')
                    d = view['control'].get('discovery') or {}
                    return view['fresh'] and any(s['backend'] == 'haproxy_nodes' and s['name'] == stopped and s['health'] == 'DOWN' for s in d.get('servers', []))
                wait_for('entrypoint detects ' + stopped, detected)
                for index in range(20):
                    path, backend = ('/shop/items', 'app-a') if index % 2 else ('/shop/admin/v2/users', 'app-b')
                    node, digest = self.https(path, backend=backend)
                    if node != survivor or digest != pin:
                        raise RuntimeError('HTTPS failover changed node or certificate identity')
                print(f'PASS: {stopped} stopped; {survivor} serves both HTTPS apps with the expected certificate.', flush=True)
            finally:
                compose('start', stopped)
            def recovered():
                view = self.api.request(f"/v1/nodes/{self.nodes[stopped]['id']}/discovery")
                d = view['control'].get('discovery') or {}
                c = d.get('configuration') or {}
                return view['fresh'] and not c.get('error_code') and c.get('document') and all(s['health'] == 'UP' for s in d.get('servers', [])) and any(o['loaded'] and o['sha256'] == pin for o in c.get('tls_listeners', []))
            wait_for(stopped + ' loaded TLS and backend health', recovered)
            wait_for('both HTTPS nodes rejoin', lambda: {self.https()[0] for _ in range(10)} == {'haproxy-a', 'haproxy-b'})

    def edit_after_restart(self):
        normal = Applications()
        current = normal.latest('Store demo')
        baseline = next(a for a in self.inventory()[0]['document']['applications'] if a['id'] == current['application_id'])
        changed = copy.deepcopy(baseline)
        weight = changed['servers'][0]['weight']
        changed['servers'][0]['weight'] = weight - 1 if weight > 1 else 2
        edited = normal.start(changed, current)
        normal.complete(edited)
        restored = normal.start(baseline, edited)
        normal.complete(restored)
        self.verify_tls('demo-v1')
        print('PASS: application edits after both cold starts preserve local defaults, HTTPS and original weights.', flush=True)


def smoke():
    migrate_entrypoint()
    app = Listeners()
    # Reuse existing two applications; fresh labs can run python3 scripts/applications.py first.
    if len(Applications().revisions()) < 2:
        raise RuntimeError('Run python3 scripts/applications.py to prepare the two demo applications first')
    first = app.start_tls('demo-v1')
    app.complete(first)
    app.verify_tls('demo-v1')
    app.https('/unconfigured', status=404)
    app.https('/shop/items', host='other.demo.test', status=404)
    probe_context = ssl.create_default_context(cafile=selected_state() / 'tls/ca.crt')
    probe_context.check_hostname = False  # Require a server-side SNI alert, not a client hostname failure.
    try:
        app.https(sni='unconfigured.demo.test', context=probe_context)
    except ssl.SSLError as error:
        if 'UNRECOGNIZED_NAME' not in error.reason:
            raise
    else:
        raise RuntimeError('Unknown TLS SNI was accepted')
    print('PASS: unknown HTTPS routes, Host/SNI mismatches and unknown SNI are rejected.', flush=True)
    before = len(app.revisions())
    for reference in ('demo-expired', 'demo-bad-key', 'demo-wrong-host'):
        body = app.body([app.listener(reference)])
        try:
            app.api.request(app.path, body)
        except urllib.error.HTTPError as error:
            if error.code not in (400, 409):
                raise
        else:
            raise RuntimeError('Unsafe certificate accepted: ' + reference)
    if len(app.revisions()) != before:
        raise RuntimeError('Rejected certificate changed history')
    print('PASS: expired, mismatched-key and wrong-host certificates are rejected before rollout.', flush=True)
    stop = threading.Event()
    def traffic():
        count = 0
        while not stop.is_set():
            app.https('/shop/admin/v2/users', backend='app-b')
            count += 1
            time.sleep(.05)
        return count
    with ThreadPoolExecutor(max_workers=1) as pool:
        requests = pool.submit(traffic)
        try:
            rotated = app.start_tls('demo-v2')
            compose('restart', '-t', '2', 'manager')
            app.complete(rotated)
        finally:
            stop.set()
        count = requests.result()
    app.verify_tls('demo-v2')
    print(f'PASS: certificate rotation survives manager restart; {count} uninterrupted HTTPS requests.', flush=True)
    restored = app.start_tls(rollback=first)
    app.complete(restored)
    app.verify_tls('demo-v1')
    normal = Applications()
    normal.verify('/shop/items', 'app-a')
    normal.verify('/shop/admin/v2/users', 'app-b')
    print('PASS: TLS rollback preserves both HTTP applications and their current routing.', flush=True)
    current = normal.latest('Store demo')
    broken = copy.deepcopy(current['spec'])
    broken['health_path'] = '/healthz/fail'
    rejected = normal.start(broken, current)
    normal.complete(rejected, 'rolled_back')
    app.verify_tls('demo-v1')
    print('PASS: failed application health automatically restores its configuration and loaded TLS identity.', flush=True)
    app.failover('demo-v1')
    app.edit_after_restart()


if __name__ == '__main__':
    smoke()
