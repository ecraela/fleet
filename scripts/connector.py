#!/usr/bin/env python3
"""Private, repeatable two-node outbound connector lab. No DNS/provider writes.

Uses the existing demo's dedicated HAProxy storage and private fixture secrets.
Never prints pairing codes, cookies, operator tokens, database URLs or keys.
"""
import argparse
import json
import hashlib
import ssl
from agentctl import identity
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
from standalone import LocalClient, command, private, state, wait_local
from lab import container as demo_container, network as demo_network

ROOT = Path(__file__).resolve().parents[1]
RELAY = 'haproxy-converge-relay'
MANAGER = 'haproxy-manager-standalone'


def up(args):
    demo = args.demo_root.resolve()
    base, folder = state(demo)
    directory = folder / 'standalone-console'
    if not (directory / 'administrator.password').exists():
        raise RuntimeError('Prepare the standalone demo accounts with scripts/standalone.py up first.')
    connector = directory / 'connector'
    connector.mkdir(mode=0o700, exist_ok=True)
    lab = directory / 'outbound-lab'
    lab.mkdir(mode=0o700, exist_ok=True)
    pki = lab / 'relay' / 'pki'
    network = demo_network(demo)
    if not pki.exists():
        command(['docker', 'run', '--rm', '--user', f'{os.getuid()}:{os.getgid()}',
                 '--volume', f'{lab}:/state', args.image, 'init', '--state-dir', '/state/relay',
                 '--dns-name', 'localhost', '--dns-name', RELAY, '--ip', '127.0.0.1'], stdout=subprocess.DEVNULL)
    settings = dict(line.split('=', 1) for line in (base / 'compose.env').read_text().splitlines() if '=' in line)
    postgres = demo_container(demo, 'postgres')
    exists = subprocess.check_output(['docker', 'exec', postgres, 'psql', '-U', 'haproxy_manager', '-d', 'postgres', '-Atc', "SELECT 1 FROM pg_database WHERE datname='haproxy_connector_demo'"]).strip()
    if exists != b'1':
        command(['docker', 'exec', postgres, 'createdb', '-U', 'haproxy_manager', 'haproxy_connector_demo'])
    relay_env = lab / 'relay.env'
    if relay_env.exists():
        relay_env.unlink()
    private(relay_env, 'HAPROXY_DATABASE_URL=postgres://haproxy_manager:' + settings['HAPROXY_DB_PASSWORD'] + '@postgres:5432/haproxy_connector_demo?sslmode=disable\nGOMEMLIMIT=96MiB\nGOMAXPROCS=2\n')
    for name in (RELAY, MANAGER):
        if subprocess.run(['docker', 'container', 'inspect', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
            command(['docker', 'stop', '--time', '10', name], stdout=subprocess.DEVNULL)
            command(['docker', 'rm', name], stdout=subprocess.DEVNULL)
    common = ['--network', network, '--user', f'{os.getuid()}:{os.getgid()}', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges:true', '--tmpfs', '/tmp:size=16m,mode=1777']
    command(['docker', 'run', '--detach', '--name', RELAY, *common, '--memory=160m',
             '--env-file', str(relay_env), '--publish', f'127.0.0.1:{args.relay_port}:8443',
             '--volume', f'{pki}:/pki:ro', '--volume', f'{base}/browser-login:/run/secrets/browser-login:ro',
             args.image, 'serve', '--role', 'relay', '--migrate', '--listen', ':8443', '--pki-dir', '/pki',
             '--browser-origin', 'http://localhost:5173', '--browser-insecure-cookies',
             '--browser-login-secret-file', '/run/secrets/browser-login'], stdout=subprocess.DEVNULL)
    command(['docker', 'run', '--detach', '--name', MANAGER, *common, '--memory=384m',
             '--env-file', str(directory / 'manager.env'), '--env', 'GOMEMLIMIT=96MiB', '--env', 'GOGC=50', '--publish', f'127.0.0.1:{args.port}:8080',
             '--volume', f'{base}/pki:/pki:ro', '--volume', f'{folder}/operator-token:/run/secrets/operator:ro',
             '--volume', f'{directory}:/run/local-accounts:ro', '--volume', f'{connector}:/connector',
             '--volume', f'{pki}/ca.crt:/run/relay-ca.crt:ro', args.image,
             'serve', '--role', 'manager', '--migrate', '--listen', '127.0.0.1:8443', '--pki-dir', '/pki',
             '--admin-token-file', '/run/secrets/operator', '--metrics-url', 'http://victoriametrics:8428',
             '--local-insecure-cookies', '--local-http-listen', ':8080',
             '--connector-relay-url', f'https://{RELAY}:8443', '--connector-state-dir', '/connector',
             '--connector-ca-file', '/run/relay-ca.crt'], stdout=subprocess.DEVNULL)
    wait_local('local console listener', lambda: urllib.request.urlopen('http://localhost:' + str(args.port), timeout=2).status == 200, seconds=30)
    local(args).request('/local/connector')
    print('Outbound lab started. Local console:', 'http://localhost:' + str(args.port))
    print('Converge proxy target:', 'https://localhost:' + str(args.relay_port))
    print('Converge dev CA:', pki / 'ca.crt')
    print('Manager API has no published port and listens on its container loopback only.')


def local(args):
    _, folder = state(args.demo_root.resolve())
    client = LocalClient('http://localhost:' + str(args.port))
    client.login('demo-admin', (folder / 'standalone-console/administrator.password').read_text().strip())
    return client


def link(args):
    code = args.code_file.read_text().strip()
    local(args).request('/local/connector', {'code': code, 'allow_manage': not args.read_only})
    print('Local administrator linked the demo; waiting for the authenticated outbound stream.')
    client = local(args)
    wait_local('outbound connection', lambda: client.request('/local/connector')['state'] == 'connected', seconds=30)
    print('Outbound stream connected; local login remains independent.')


def status(args):
    client = local(args)
    result = client.request('/local/connector')
    print(json.dumps({key: result.get(key) for key in ('state', 'linked', 'allow_manage')}, indent=2))


def smoke(args):
    from hardening import Traffic, Load
    _, folder = state(args.demo_root.resolve())
    client = local(args)
    session = client.request('/local/session')
    assert client.request('/local/connector')['state'] == 'connected'
    identities = {name: identity(folder / ('agent-' + name)) for name in ('a', 'b', 'entrypoint')}
    credential = folder / 'standalone-console/connector/connector.json'
    fingerprint = hashlib.sha256(credential.read_bytes()).hexdigest()
    cluster = (folder / 'cluster-id').read_text().strip()
    nodes = [n for n in client.request('/fleet')['nodes'] if n['cluster_id'] == cluster and not n.get('revoked_at')]
    assert {n['name'] for n in nodes} == {'haproxy-a', 'haproxy-b'}
    for node in nodes:
        wait_local('fresh local telemetry after storage startup', lambda node=node: client.request('/nodes/' + node['id'] + '/metrics')['fresh'], seconds=30)
    config = client.request('/nodes/' + nodes[0]['id'] + '/configuration')['control']['discovery']['configuration']['document']
    apps = config.get('applications', [])
    assert {a['name'] for a in apps} == {'Store demo', 'Admin demo'}
    routes = [(a['path_prefix'].rstrip('/') + '/connector-smoke', 'app-a' if a['name'] == 'Store demo' else 'app-b') for a in apps]
    context = ssl.create_default_context(cafile=folder / 'tls/ca.crt')
    context.load_verify_locations(cafile=folder / 'acme/public/trust.pem')
    report = {'passed': False}
    try:
        with Load(Traffic(context, routes), 4, 20) as load:
            time.sleep(3)
            command(['docker', 'stop', '--time', '5', RELAY], stdout=subprocess.DEVNULL)
            try:
                wait_local('offline state', lambda: client.request('/local/connector')['state'] == 'offline', seconds=15)
                # Same local browser session, stored history, live observations and
                # controls remain available while the entire cloud relay is down.
                assert client.request('/local/session')['user_id'] == session['user_id']
                for node in nodes:
                    wait_local('fresh local telemetry during relay outage', lambda node=node: client.request('/nodes/' + node['id'] + '/metrics')['fresh'], seconds=20)
                    assert client.request('/nodes/' + node['id'] + '/history?range=15m')['series']
                print('PASS: relay outage leaves independent local login, fleet and metrics working.', flush=True)
                time.sleep(5)
            finally:
                command(['docker', 'start', RELAY], stdout=subprocess.DEVNULL)
            wait_local('reconnected outbound stream', lambda: client.request('/local/connector')['state'] == 'connected', seconds=30)
            command(['docker', 'restart', MANAGER], stdout=subprocess.DEVNULL)
            wait_local('same local session after restart', lambda: client.request('/local/session')['user_id'] == session['user_id'], seconds=30)
            wait_local('same connector after restart', lambda: client.request('/local/connector')['state'] == 'connected', seconds=30)
            time.sleep(5)
        traffic = load.results()
        assert all(not r['failures'] for r in traffic.values()), 'HTTP/HTTPS application traffic failed'
        assert all(identity(folder / ('agent-' + name)) == old for name, old in identities.items())
        assert hashlib.sha256(credential.read_bytes()).hexdigest() == fingerprint
        ports = json.loads(subprocess.check_output(['docker', 'inspect', MANAGER, '--format', '{{json .HostConfig.PortBindings}}']))
        assert '8443/tcp' not in ports
        probe = subprocess.run(['docker', 'exec', RELAY, 'wget', '-q', '-T', '2', '-O', '/dev/null', 'https://' + MANAGER + ':8443/healthz'], capture_output=True, text=True)
        assert probe.returncode != 0 and 'refused' in probe.stderr.lower(), 'manager API unexpectedly reachable inbound'
        report = {'passed': True, 'nodes': len(nodes), 'local_login_during_outage': True,
                  'reconnected_after_relay_restart': True, 'manager_restart_preserved_local_session': True,
                  'connector_identity_preserved': True, 'node_identities_and_keys_preserved': True,
                  'manager_api_inbound_connection_refused': True, 'traffic': traffic}
        print(json.dumps(report, indent=2), flush=True)
    finally:
        path = folder / 'standalone-console/outbound-lab/latest.json'
        if path.exists():
            path.unlink()
        private(path, json.dumps(report, indent=2) + '\n')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=['up', 'status', 'link', 'smoke'])
    p.add_argument('--demo-root', type=Path, default=ROOT)
    p.add_argument('--image', default='ecraela-haproxy-connector:local')
    p.add_argument('--port', type=int, default=5180)
    p.add_argument('--relay-port', type=int, default=18446)
    p.add_argument('--code-file', type=Path)
    p.add_argument('--read-only', action='store_true')
    args = p.parse_args()
    if args.action == 'link' and not args.code_file:
        p.error('link requires a private --code-file')
    globals()[args.action](args)


if __name__ == '__main__':
    main()
