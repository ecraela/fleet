#!/usr/bin/env python3
"""Run the standalone embedded console against a local two-node demo.

No Converge login, proxy or database is used. All account secrets stay in the
ignored demo state directory. Existing HAProxy nodes and agents are untouched.
"""
import argparse
from agentctl import identity
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import time
import urllib.error
import urllib.request
import uuid

from lab import network as demo_network

ROOT = Path(__file__).resolve().parents[1]
NAME = 'haproxy-manager-standalone'


def command(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def private(path, text):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(text)


def state(demo):
    state_dir = demo / 'deploy/local/state.local'
    pair = state_dir / 'pair'
    org = str(uuid.UUID((pair / 'active-organization').read_text().strip()))
    folder = pair / org
    return state_dir, folder


class LocalClient:
    def __init__(self, origin):
        self.origin = origin
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = ''
        self.organization = ''

    def request(self, path, body=None, method=None):
        headers = {'Origin': self.origin, 'X-HAProxy-Session': 'local', 'Accept': 'application/json'}
        if self.csrf:
            headers['X-CSRF-Token'] = self.csrf
        if self.organization:
            headers['X-Organization-ID'] = self.organization
        if body is not None:
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.origin + '/api/haproxy/v1' + path,
                                     data=json.dumps(body).encode() if body is not None else None,
                                     headers=headers, method=method)
        with self.opener.open(req, timeout=15) as response:
            data = response.read()
            return json.loads(data) if data else None

    def login(self, username, password):
        result = self.request('/local/session', {'username': username, 'password': password})
        self.csrf, self.organization = result['csrf_token'], result['organization_id']
        return result


def wait_local(label, operation, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = operation()
            if value:
                return value
        except urllib.error.HTTPError as error:
            if error.code not in (429, 503):
                raise
        except (urllib.error.URLError, OSError):
            pass
        time.sleep(0.5)
    raise RuntimeError('Timed out waiting for ' + label)


def prepare_accounts(folder):
    directory = folder / 'standalone-console'
    directory.mkdir(mode=0o700, exist_ok=True)
    for role in ('administrator', 'viewer'):
        password = directory / (role + '.password')
        if not password.exists():
            private(password, secrets.token_urlsafe(32) + '\n')
    return directory


def up(args):
    demo = args.demo_root.resolve()
    state_dir, folder = state(demo)
    directory = prepare_accounts(folder)
    settings = dict(line.split('=', 1) for line in (state_dir / 'compose.env').read_text().splitlines() if '=' in line)
    network = demo_network(demo)
    # No shared login secret or Converge endpoint is passed to this manager.
    env_path = directory / 'manager.env'
    if env_path.exists():
        env_path.unlink()
    private(env_path, '\n'.join([
        'HAPROXY_DATABASE_URL=postgres://haproxy_manager:' + settings['HAPROXY_DB_PASSWORD'] + '@postgres:5432/haproxy_manager?sslmode=disable',
        'HAPROXY_LOGS_URL=http://victorialogs:9428',
        'HAPROXY_LOCAL_ORIGIN=http://localhost:' + str(args.port),
        'HAPROXY_ORGANIZATION_ID=' + folder.name,
        'HAPROXY_AGENT_IMAGE=converge-haproxy:local',
        'HAPROXY_AGENT_MANAGER_URL=https://manager:8443',
        'HAPROXY_AGENT_INGEST_URL=https://ingest:8443',
        'HAPROXY_AGENT_NETWORK=' + network,
        'HAPROXY_AGENT_METRICS_URL=http://haproxy:8404/metrics',
        'GOMEMLIMIT=256MiB', 'GOMAXPROCS=2', ''
    ]))
    if subprocess.run(['docker', 'container', 'inspect', NAME], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        raise RuntimeError('Standalone console container already exists; use down before up. No existing state was replaced.')
    command(['docker', 'run', '--detach', '--name', NAME, '--network', network,
             '--user', f'{os.getuid()}:{os.getgid()}', '--read-only', '--cap-drop=ALL',
             '--security-opt=no-new-privileges:true', '--memory=384m', '--tmpfs', '/tmp:size=32m,mode=1777',
             '--env-file', str(env_path), '--publish', f'127.0.0.1:{args.port}:8080',
             '--publish', f'127.0.0.1:{args.tls_port}:8443',
             '--volume', f'{state_dir}/pki:/pki:ro',
             '--volume', f'{folder}/operator-token:/run/secrets/operator:ro',
             '--volume', f'{directory}:/run/local-accounts:ro', args.image,
             'serve', '--role', 'manager', '--migrate', '--listen', ':8443', '--pki-dir', '/pki',
             '--admin-token-file', '/run/secrets/operator', '--metrics-url', 'http://victoriametrics:8428',
             '--local-insecure-cookies', '--local-http-listen', ':8080'], stdout=subprocess.DEVNULL)
    for attempt in range(60):
        try:
            with urllib.request.urlopen(f'http://localhost:{args.port}/', timeout=2) as response:
                if response.status == 200:
                    break
        except (OSError, urllib.error.URLError):
            time.sleep(1)
    else:
        raise RuntimeError('Standalone console did not become ready; inspect sanitized container startup errors.')
    for role in ('administrator', 'viewer'):
        username = 'demo-admin' if role == 'administrator' else 'demo-viewer'
        password = (directory / (role + '.password')).read_text().rstrip('\n')
        try:
            LocalClient(f'http://localhost:{args.port}').login(username, password)
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise
            command(['docker', 'exec', NAME, 'haproxy-manager', 'user', 'add',
                     '--username', username, '--organization-id', folder.name, '--role', role,
                     '--password-file', '/run/local-accounts/' + role + '.password'], stdout=subprocess.DEVNULL)
    print('Standalone console ready at http://localhost:' + str(args.port))
    print('Local accounts: demo-admin and demo-viewer. Password files are private in ' + str(directory))


def smoke(args):
    _, folder = state(args.demo_root.resolve())
    directory = folder / 'standalone-console'
    origin = 'http://localhost:' + str(args.port)
    admin = LocalClient(origin)
    session = admin.login('demo-admin', (directory / 'administrator.password').read_text().rstrip('\n'))
    assert session['capabilities'] == ['haproxy.read', 'haproxy.manage']
    fleet = admin.request('/fleet')
    cluster = (folder / 'cluster-id').read_text().strip()
    nodes = [node for node in fleet['nodes'] if node['cluster_id'] == cluster and not node.get('revoked_at')]
    assert {node['name'] for node in nodes} == {'haproxy-a', 'haproxy-b'}
    before = {name: identity(folder / ('agent-' + name)) for name in ('a', 'b', 'entrypoint')}
    for node in nodes:
        def fresh_metrics():
            value = admin.request('/nodes/' + node['id'] + '/metrics')
            return value if value['fresh'] and value['scrape_ok'] else None
        snapshot = wait_local('fresh persisted node metrics', fresh_metrics)
        history = admin.request('/nodes/' + node['id'] + '/history?range=15m')
        assert history['node_id'] == node['id'] and history['series']
        discovery = admin.request('/nodes/' + node['id'] + '/discovery')
        assert discovery['control']['discovery']
    stream_path = '/clusters/' + cluster + '/events?nodes=' + ','.join(n['id'] for n in nodes)
    req = urllib.request.Request(origin + '/api/haproxy/v1' + stream_path,
                                 headers={'Origin': origin, 'X-HAProxy-Session': 'local',
                                          'X-Organization-ID': admin.organization, 'Accept': 'text/event-stream'})
    frames = []
    with admin.opener.open(req, timeout=5) as response:
        for _ in range(40):
            line = response.readline(1 << 20).decode()
            if line.startswith('data: '):
                frame = json.loads(line[6:])
                if all(n['fresh'] and n['scrape_ok'] for n in frame['nodes']):
                    frames.append(frame)
                if len(frames) == 2:
                    break
    assert len(frames) == 2
    if args.restart:
        command(['docker', 'restart', NAME], stdout=subprocess.DEVNULL)
        for _ in range(30):
            try:
                retained = admin.request('/local/session')
                assert retained['user_id'] == session['user_id']
                break
            except (OSError, urllib.error.URLError):
                time.sleep(1)
        else:
            raise RuntimeError('Persisted local session did not survive manager restart')
    viewer = LocalClient(origin)
    reader = viewer.login('demo-viewer', (directory / 'viewer.password').read_text().rstrip('\n'))
    assert reader['capabilities'] == ['haproxy.read']
    assert viewer.request('/fleet')['nodes']
    try:
        viewer.request('/clusters', {'name': 'This must not be created'})
    except urllib.error.HTTPError as error:
        assert error.code == 403
    else:
        raise RuntimeError('Read-only account was able to mutate the fleet')
    # Tenant consistency is enforced from the cookie, never supplied by this header.
    viewer.organization = str(uuid.uuid4())
    try:
        viewer.request('/fleet')
    except urllib.error.HTTPError as error:
        assert error.code == 401
    else:
        raise RuntimeError('Cross-tenant header was accepted')
    traffic_results = None
    if args.traffic_seconds:
        from hardening import Traffic, Load
        context = ssl.create_default_context(cafile=folder / 'tls/ca.crt')
        if (folder / 'acme/public/trust.pem').exists():
            context.load_verify_locations(cafile=folder / 'acme/public/trust.pem')
        document = admin.request('/nodes/' + nodes[0]['id'] + '/configuration')['control']['discovery']['configuration']['document']
        applications = document.get('applications', [])
        assert {a['name'] for a in applications} == {'Store demo', 'Admin demo'}
        routes = [(a['path_prefix'].rstrip('/') + '/console-smoke',
                   'app-a' if a['name'] == 'Store demo' else 'app-b') for a in applications]
        with Load(Traffic(context, routes), 4, 20) as load:
            time.sleep(args.traffic_seconds)
        traffic_results = load.results()
        print('HTTP/HTTPS traffic:', json.dumps(traffic_results, sort_keys=True), flush=True)
        if any(row['failures'] for row in traffic_results.values()):
            failure_path = directory / 'latest.json'
            if failure_path.exists():
                failure_path.unlink()
            private(failure_path, json.dumps({'passed': False, 'traffic': traffic_results}, indent=2) + '\n')
            raise RuntimeError('Verified HTTP/HTTPS traffic failed; inspect the sanitized measurement report')
        assert sum(row['success'] for row in traffic_results.values()) > 0
    admin.request('/local/session', method='DELETE')
    try:
        admin.request('/fleet')
    except urllib.error.HTTPError as error:
        assert error.code == 401
    else:
        raise RuntimeError('Logout did not revoke the session')
    assert all(identity(folder / ('agent-' + name)) == value for name, value in before.items())
    report = {'passed': True, 'fresh_nodes': len(nodes), 'live_frames': len(frames),
              'session_restart_tested': args.restart, 'node_identities_and_keys_preserved': True,
              'traffic': traffic_results}
    report_path = directory / 'latest.json'
    if report_path.exists():
        report_path.unlink()
    private(report_path, json.dumps(report, indent=2) + '\n')
    print('PASS: local admin/viewer login, two-node fleet, live SSE metrics, history, discovery, persisted sessions, tenant guards, read-only permissions, logout and unchanged node keys.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['up', 'smoke', 'down'])
    parser.add_argument('--demo-root', type=Path, default=ROOT)
    parser.add_argument('--traffic-seconds', type=int, choices=range(1, 61), default=0, help='verify both synthetic apps over trusted HTTP/HTTPS for a bounded interval')
    parser.add_argument('--restart', action='store_true', help='also verify a persisted session across a manager restart')
    parser.add_argument('--image', default='ecraela-haproxy-standalone:local')
    parser.add_argument('--port', type=int, default=5180)
    parser.add_argument('--tls-port', type=int, default=18445)
    args = parser.parse_args()
    if args.action == 'down':
        subprocess.run(['docker', 'rm', '--force', NAME], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    elif args.action == 'up':
        up(args)
    else:
        smoke(args)

if __name__ == '__main__':
    main()
