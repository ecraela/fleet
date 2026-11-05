#!/usr/bin/env python3
"""Opt-in private ACME/DNS-01 lab for the existing two-node demo."""
import base64
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import sys
import time
import urllib.request
import uuid

from demo import wait_for, write_private
from pair import client, compose, selected_state
from tls_fixtures import openssl, prepare_assets


def prepare():
    folder = selected_state()
    prepare_assets(folder)
    lab = folder / 'acme'
    for path in (lab, lab / 'dns', lab / 'ca', lab / 'public'):
        path.mkdir(mode=0o700, exist_ok=True)
    tls, ca, dns = folder / 'tls', lab / 'ca', lab / 'dns'
    if not (ca / 'server.crt').exists():
        openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=Converge local ACME CA', '-keyout', ca / 'server.key', '-out', ca / 'server.csr')
        (ca / 'server.key').chmod(0o600)
        write_private(ca / 'server.ext', 'subjectAltName=DNS:acme-ca,DNS:localhost,IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n')
        openssl('x509', '-req', '-in', ca / 'server.csr', '-CA', tls / 'ca.crt', '-CAkey', tls / 'ca.key', '-CAcreateserial', '-days', '365', '-extfile', ca / 'server.ext', '-out', ca / 'server.crt')
    write_private(ca / 'pebble.json', json.dumps({'pebble': {'listenAddress': '0.0.0.0:14000', 'managementListenAddress': '0.0.0.0:15000', 'certificate': '/lab/server.crt', 'privateKey': '/lab/server.key', 'httpPort': 5002, 'tlsPort': 5001, 'externalAccountBindingRequired': False, 'retryAfter': {'authz': 1, 'order': 1}, 'keyAlgorithm': 'ecdsa', 'profiles': {'default': {'description': 'Local renewal test', 'validityPeriod': int(os.getenv('ACME_DEMO_LIFETIME_SECONDS', '86400'))}}}}))
    keys = []
    for letter in ('a', 'b'):
        key_path = lab / ('tsig-' + letter)
        if not key_path.exists():
            write_private(key_path, base64.b64encode(secrets.token_bytes(32)).decode() + '\n')
        key = key_path.read_text().strip()
        keys.append(f'key "node-{letter}.validation.demo.test" {{ algorithm hmac-sha256; secret "{key}"; }};')
        policy = {'certificate_dir': '/var/lib/haproxy/certificates/acme', 'profiles': [{'issuer': 'demo-acme', 'listener': 'public', 'environment': 'staging', 'directory_url': 'https://acme-ca:14000/dir', 'trust_file': '/run/secrets/acme-trust/trust.pem', 'terms_agreed': True, 'dns_server': 'acme-dns:8053', 'resolver': 'acme-dns:8053', 'validation_zone': 'validation.demo.test', 'key_name': f'node-{letter}.validation.demo.test', 'key_file': '/run/secrets/acme-tsig', 'challenges': [{'hostname': 'apps.demo.test', 'target': 'apps.validation.demo.test'}]}]}
        write_private(lab / f'policy-{letter}.json', json.dumps(policy))
    write_private(dns / 'named.conf', '''options { directory "/tmp"; pid-file "/tmp/named.pid"; session-keyfile "/tmp/session.key"; listen-on port 8053 { any; }; listen-on-v6 { none; }; recursion yes; allow-recursion { any; }; dnssec-validation no; allow-query { any; }; allow-transfer { none; }; }; controls {};
''' + '\n'.join(keys) + '''
zone "demo.test" { type primary; file "/lab/source.zone"; allow-update { none; }; };
zone "validation.demo.test" { type primary; file "/lab/validation.zone"; update-policy { grant node-a.validation.demo.test name apps.validation.demo.test TXT; grant node-b.validation.demo.test name apps.validation.demo.test TXT; }; };
''')
    if not (dns / 'source.zone').exists():
        write_private(dns / 'source.zone', '$TTL 1\n@ IN SOA ns.demo.test. hostmaster.demo.test. (1 60 60 3600 1)\n@ IN NS ns.demo.test.\nns IN A 127.0.0.1\napps IN A 127.0.0.1\n_acme-challenge.apps IN CNAME apps.validation.demo.test.\n')
    if not (dns / 'validation.zone').exists():
        write_private(dns / 'validation.zone', '$TTL 1\n@ IN SOA ns.validation.demo.test. hostmaster.demo.test. (1 60 60 3600 1)\n@ IN NS ns.validation.demo.test.\nns IN A 127.0.0.1\n')
    trust = lab / 'public/trust.pem'
    if not trust.exists():
        trust.write_bytes((tls / 'ca.crt').read_bytes())
        trust.chmod(0o644)
    write_private(lab / 'enabled', 'private ACME lab enabled\n')


def up():
    prepare()
    compose('build', 'manager', 'acme-ca')
    compose('up', '-d', '--no-deps', 'acme-dns', 'acme-ca')
    provision_volumes()
    folder = selected_state()
    context = ssl.create_default_context(cafile=folder / 'tls/ca.crt')
    def root():
        try:
            with urllib.request.urlopen('https://localhost:15000/roots/0', context=context, timeout=3) as response:
                pem = response.read(65537)
            return pem if len(pem) <= 65536 and pem.startswith(b'-----BEGIN CERTIFICATE-----') else None
        except (OSError, ValueError):
            return None
    pem = wait_for('private ACME root', root)
    # Public test trust only; private CA and node keys are never copied here.
    trust = folder / 'acme/public/trust.pem'
    retained = trust.read_bytes() if trust.exists() else (folder / 'tls/ca.crt').read_bytes()
    trust_changed = pem.strip() not in retained
    if trust_changed:
        if len(retained) + len(pem) > 128 << 10:
            raise RuntimeError('Archive unneeded local CA roots before refreshing test trust')
        trust.write_bytes(retained.rstrip() + b'\n' + pem)
        trust.chmod(0o644)
    compose('up', '-d', '--wait', '--no-deps', 'manager', 'haproxy-a', 'haproxy-b')
    # Issuers load trust at startup. Recreate only the agent processes when
    # Pebble regenerated its root, retaining their mounted identities and keys.
    restart = ('--force-recreate',) if trust_changed else ()
    compose('up', '-d', '--no-deps', *restart, 'agent-a', 'agent-b')
    wait_for_grants()
    print('Local ACME lab started. Existing private DNS delegation is a fixture; Converge delegation changes still use DNS review.', flush=True)


def plan():
    api = client()
    cluster = (selected_state() / 'cluster-id').read_text().strip()
    path = f'/v1/clusters/{cluster}/certificates'
    return api, path, api.request(path)


def provision_volumes():
    from recovery import command, source_command
    # Keep the completed dependency container for later Compose start/recovery
    # calls. Wait for actual success before a non-root agent opens the volume.
    compose('up', '-d', '--no-deps', 'acme-volume-init')
    def completed():
        cid = source_command('ps', '-a', '-q', 'acme-volume-init').decode().strip()
        if not cid:
            return None
        state = json.loads(command(['docker', 'inspect', '--format', '{{json .State}}', cid]))
        if state['Status'] in ('exited', 'dead'):
            if state['Status'] != 'exited' or state['ExitCode'] != 0:
                raise RuntimeError('Private ACME volume initialization failed')
            return True
        return None
    wait_for('private ACME volume initialization', completed)


def wait_for_grants():
    # Container startup is not control-plane readiness. The manager exposes an
    # option only after both agents have reported matching local DNS grants.
    # Poll this read-only evidence; never infer authority from a running process.
    def reported():
        state = plan()[2]
        return next((o for o in state.get('options', []) if o['issuer'] == 'demo-acme'), None)
    try:
        return wait_for('both agents reporting local ACME grants', reported, seconds=120)
    except SystemExit:
        grant_diagnostics()
        raise


def grant_diagnostics():
    # These are fixed status fields and known error codes only. Never dump raw
    # discovery/configuration, credentials, container environment or full logs.
    from pair import nodes
    from recovery import command, source_command
    for name, node in nodes().items():
        value = client().request(f"/v1/nodes/{node['id']}/configuration")
        discovery = value['control'].get('discovery') or {}
        cfg = discovery.get('configuration') or {}
        service = 'agent-' + name.removeprefix('haproxy-')
        cid = source_command('ps', '-a', '-q', service).decode().strip()
        status, exit_code, restarts = command(['docker', 'inspect', '--format', '{{.State.Status}} {{.State.ExitCode}} {{.RestartCount}}', cid]).decode().split()
        result = subprocess.run(['docker', 'logs', '--tail', '100', cid], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10)
        log = result.stdout.decode(errors='replace')
        known = [code for code in ('local ACME policy unavailable', 'ACME requires local TLS and configuration authority', 'permission denied', 'configuration discovery unavailable', 'certificate expired') if code in log]
        print(json.dumps({'node': name, 'configuration_fresh': value.get('fresh'),
                          'configuration_enabled': value.get('configuration_enabled'),
                          'has_document': bool(cfg.get('document')), 'grant_count': len(cfg.get('acme_grants', [])),
                          'agent_state': status, 'exit_code': int(exit_code),
                          'restarts': int(restarts), 'known_errors': known}), flush=True)


def action(kind):
    api, path, state = plan()
    current = state['plan']
    option = next((o for o in state['options'] if o['issuer'] == 'demo-acme'), None)
    if kind == 'enable' and current and current['enabled']:
        return current
    if option is None:
        raise RuntimeError('Wait for both agents to report their local ACME grants')
    body = {'request_id': str(uuid.uuid4()), 'expected_version': current['version'] if current else 0, 'action': kind, 'issuer': option['issuer'], 'binding': option['binding']}
    result = api.request(path, body)['plan']
    if api.request(path, body)['plan']['id'] != result['id']:
        raise RuntimeError('Certificate request retry created another plan')
    return result


def ready(generation=None, seconds=180):
    def check():
        _, _, state = plan()
        p = state['plan']
        if p and p['status'] == 'error':
            raise RuntimeError('Certificate automation needs attention: ' + p['error_code'])
        return p if p and p['status'] == 'active' and (generation is None or p['generation'] >= generation) else None
    return wait_for('automatic TLS active', check, seconds=seconds)


def verify(p):
    from listeners import Listeners
    lab = Listeners()
    lab.context = ssl.create_default_context(cafile=selected_state() / 'acme/public/trust.pem')
    pins = {n['name']: next(j['certificate']['sha256'] for j in p['jobs'] if j['node_id'] == n['id']) for n in lab.nodes.values()}
    if len(set(pins.values())) != 2:
        raise RuntimeError('Nodes did not issue independent certificate identities')
    for path, backend in [('/shop/items', 'app-a'), ('/shop/admin/v2/users', 'app-b')]:
        seen = set()
        for _ in range(30):
            node, digest = lab.https(path, backend=backend)
            if pins.get(node) != digest:
                raise RuntimeError('The served node certificate does not match its issued identity')
            seen.add(node)
        if seen != set(pins):
            raise RuntimeError('Both HAProxy nodes did not serve trusted HTTPS')
    print(f'PASS: generation {p["generation"]}, independent keys/certificates on both nodes, both apps route over trusted HTTPS.', flush=True)


def smoke():
    from concurrent.futures import ThreadPoolExecutor
    from threading import Event
    from listeners import Listeners
    errors = []
    requests = 0
    stop = Event()
    lab = Listeners()
    lab.context = ssl.create_default_context(cafile=selected_state() / 'acme/public/trust.pem')
    def traffic():
        nonlocal requests
        while not stop.is_set():
            for path, backend in [('/shop/items', 'app-a'), ('/shop/admin/v2/users', 'app-b')]:
                try:
                    request = urllib.request.Request('http://127.0.0.1:18081' + path, headers={'Host': 'apps.demo.test'})
                    with urllib.request.urlopen(request, timeout=3) as response:
                        body = response.read(65536)
                        if response.status != 200 or response.headers.get('X-Demo-Backend') != backend or b'Converge HAProxy demo backend' not in body:
                            errors.append('HTTP routing')
                        requests += 1
                    # Initial issuance may enable HTTPS for the first time.
                    if https_ready.is_set():
                        lab.https(path, backend=backend)
                        requests += 1
                except (OSError, RuntimeError):
                    errors.append('Application unavailable')
            time.sleep(.05)
    https_ready = Event()
    with ThreadPoolExecutor(max_workers=1) as pool:
        task = pool.submit(traffic)
        try:
            p = action('enable'); p = ready(p['generation']); verify(p)
            https_ready.set()
            p = action('renew'); p = ready(p['generation']); verify(p)
        finally:
            stop.set(); task.result()
    if errors: raise RuntimeError('Traffic errors during rolling TLS update: ' + str(len(errors)))
    print(f'PASS: issuance and renewal use rolling drain/update/health/rejoin; {requests} correctly routed HTTP/HTTPS requests across both apps stayed available.', flush=True)


if __name__ == '__main__':
    command = sys.argv[1] if len(sys.argv) > 1 else 'up'
    if command == 'up': up()
    elif command == 'smoke': smoke()
    elif command == 'verify': verify(ready())
    elif command == 'renew': action('renew'); verify(ready())
    elif command == 'status':
        p = plan()[2]['plan']; print(json.dumps({k:p[k] for k in ('status','generation','renew_at_ms')} if p else {'status':'not configured'}))
    else: raise SystemExit('Use up, smoke, verify, renew or status')
