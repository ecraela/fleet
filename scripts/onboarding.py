#!/usr/bin/env python3
"""Onboard already-serving HAProxy nodes in an isolated, Converge-free stack.

Only the randomly named project created by this script is managed or removed.
Private bootstrap state stays in the ignored work directory; evidence is curated.
"""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import threading
import time
import urllib.request
import uuid

from hardening import Load

ROOT = Path(__file__).resolve().parents[1]


def command(args, check=True):
    result = subprocess.run(list(map(str, args)), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and result.returncode:
        raise RuntimeError('Onboarding fixture command failed: ' + ' '.join(map(str, args[:3])))
    return result


def private(path, value):
    with path.open('x') as file:
        file.write(value)
    path.chmod(0o600)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait(label, operation, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = operation()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(.5)
    raise RuntimeError('Timed out waiting for ' + label)


class API:
    def __init__(self, work, manager_port, local_port):
        self.local_origin = f'http://localhost:{local_port}'
        self.origin = f'https://localhost:{manager_port}'
        self.context = ssl.create_default_context(cafile=work / 'pki/ca.crt')
        self.token = (work / 'admin-token').read_text().strip()
        self.organization = (work / 'organization-id').read_text().strip()
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=self.context),
                                                 urllib.request.HTTPCookieProcessor(self.cookies))
        self.csrf = ''

    def request(self, path, body=None, local=False):
        headers = {'Accept': 'application/json', 'X-Organization-ID': self.organization}
        if local:
            path = '/api/haproxy' + path
            headers.update({'Origin': self.local_origin, 'X-HAProxy-Session': 'local', 'X-CSRF-Token': self.csrf})
        else:
            headers['Authorization'] = 'Bearer ' + self.token
        if body is not None:
            headers['Content-Type'] = 'application/json'
        request = urllib.request.Request((self.local_origin if local else self.origin) + path, headers=headers,
                                         data=json.dumps(body).encode() if body is not None else None)
        with self.opener.open(request, timeout=8) as response:
            return json.loads(response.read())


class Traffic:
    def __init__(self, entrypoint_port):
        self.port = entrypoint_port
        self.seen = set()
        self.lock = threading.Lock()

    def request(self, index):
        app = 'a' if index % 2 == 0 else 'b'
        req = urllib.request.Request(f'http://localhost:{self.port}/existing/{index}',
                                     headers={'Host': f'legacy-{app}.example.test'})
        with urllib.request.urlopen(req, timeout=3) as response:
            node = response.headers.get('X-Existing-Node')
            if response.status != 200 or response.headers.get('X-Demo-Backend') != 'existing-' + app or node not in ('a', 'b'):
                raise RuntimeError('Existing application route changed')
            response.read()
            with self.lock:
                self.seen.add((app, node))
            return 'http', app, node


def run(image, keep=False):
    os.umask(0o077)
    work = ROOT / '.build.local/onboarding' / uuid.uuid4().hex
    work.mkdir(mode=0o700, parents=True)
    project = 'hm-onboarding-' + work.name[:12]
    manager_port, entrypoint_port, local_port = port(), port(), port()
    print(f'Isolated onboarding workspace: {work}', flush=True)
    services = {}
    volumes = {'runtime-a': {}, 'runtime-b': {}}
    specification = {'name': project, 'services': services, 'volumes': volumes}
    base = dict(image=image, user=f'{os.getuid()}:{os.getgid()}', read_only=True,
                cap_drop=['ALL'], security_opt=['no-new-privileges:true'],
                environment={'GOMEMLIMIT': '80MiB', 'GOMAXPROCS': '1'}, mem_limit='128m')
    for letter in ('a', 'b'):
        runtime = 'runtime-' + letter
        (work / ('agent-' + letter)).mkdir(mode=0o700)
        private(work / f'proxy-{letter}.cfg', f'''# Ansible managed: existing customer-style configuration
global
  maxconn 128
  nbthread 1
  stats socket /runtime/admin.sock mode 600 level admin
  stats socket /runtime/observe.sock mode 600 level operator
defaults
  mode http
  timeout connect 2s
  timeout client 10s
  timeout server 10s
frontend existing_apps
  bind :8080
  http-response set-header X-Existing-Node {letter}
  use_backend old_app_a if {{ hdr(host) -i legacy-a.example.test }}
  default_backend old_app_b
frontend local_metrics
  bind :8405
  http-request use-service prometheus-exporter if {{ path /metrics }}
backend old_app_a
  server original_a backend-a:8080 check
backend old_app_b
  server original_b backend-b:8080 check
''')
        services['backend-' + letter] = dict(base, entrypoint=['/usr/local/bin/demo-backend'],
                                            environment={'DEMO_BACKEND': 'existing-' + letter}, mem_limit='32m')
        services['proxy-' + letter] = dict(base, image='haproxy:3.4.7-alpine3.24',
            command=['haproxy', '-W', '-db', '-f', '/existing/haproxy.cfg'],
            volumes=[f'{work}/proxy-{letter}.cfg:/existing/haproxy.cfg:ro', f'{runtime}:/runtime'],
            depends_on={'backend-a': {'condition': 'service_started'}, 'backend-b': {'condition': 'service_started'},
                        'runtime-init': {'condition': 'service_completed_successfully'}},
            healthcheck={'test': ['CMD', 'wget', '-q', '-O', '/dev/null', 'http://127.0.0.1:8405/metrics'], 'interval': '1s', 'retries': 30}, mem_limit='64m')
    services['runtime-init'] = dict(image='haproxy:3.4.7-alpine3.24', user='0:0', read_only=True,
        cap_drop=['ALL'], cap_add=['CHOWN', 'FOWNER'], network_mode='none',
        security_opt=['no-new-privileges:true'], entrypoint=['/bin/sh', '-c'],
        command=[f'chown {os.getuid()}:{os.getgid()} /a /b && chmod 700 /a /b'],
        volumes=['runtime-a:/a', 'runtime-b:/b'], mem_limit='16m')
    private(work / 'entrypoint.cfg', '''global
  maxconn 128
  nbthread 1
defaults
  mode http
  timeout connect 2s
  timeout client 10s
  timeout server 10s
frontend customer_entrypoint
  bind :8080
  default_backend existing_proxies
backend existing_proxies
  balance roundrobin
  server old_a proxy-a:8080 check
  server old_b proxy-b:8080 check
''')
    services['entrypoint'] = dict(base, image='haproxy:3.4.7-alpine3.24',
        command=['haproxy', '-W', '-db', '-f', '/existing/entrypoint.cfg'],
        volumes=[f'{work}/entrypoint.cfg:/existing/entrypoint.cfg:ro'],
        ports=[f'127.0.0.1:{entrypoint_port}:8080'], depends_on={name: {'condition': 'service_healthy'} for name in ('proxy-a', 'proxy-b')}, mem_limit='64m')
    password = secrets.token_hex(32)
    services['postgres'] = dict(image='postgres:18.6-alpine3.24', mem_limit='192m',
        environment={'POSTGRES_DB': 'haproxy_manager', 'POSTGRES_USER': 'haproxy_manager', 'POSTGRES_PASSWORD': password},
        healthcheck={'test': ['CMD', 'pg_isready', '-h', '127.0.0.1', '-U', 'haproxy_manager', '-d', 'haproxy_manager'], 'interval': '1s', 'retries': 60})
    services['metrics'] = dict(image='victoriametrics/victoria-metrics:v1.154.1', mem_limit='192m',
        command=['-retentionPeriod=1d', '-memory.allowedBytes=64MiB', '-inmemoryDataFlushInterval=1s'])
    for role in ('manager', 'ingest'):
        args = ['serve', '--role', role, '--listen', ':8443', '--pki-dir', '/bootstrap/pki',
                '--metrics-url', 'http://metrics:8428']
        if role == 'manager':
            args += ['--migrate', '--admin-token-file', '/bootstrap/admin-token',
                     '--local-origin', f'http://localhost:{local_port}',
                     '--local-http-listen', ':8080', '--local-insecure-cookies']
        services[role] = dict(base, command=args, volumes=[f'{work}:/bootstrap:ro'],
            environment={'HAPROXY_DATABASE_URL': f'postgres://haproxy_manager:{password}@postgres:5432/haproxy_manager?sslmode=disable',
                         'GOMEMLIMIT': '80MiB', 'GOMAXPROCS': '1'},
            depends_on={'postgres': {'condition': 'service_healthy'}, 'metrics': {'condition': 'service_started'}},
            **({'ports': [f'127.0.0.1:{manager_port}:8443', f'127.0.0.1:{local_port}:8080']} if role == 'manager' else {}))
    spec = work / 'compose.json'
    private(spec, json.dumps(specification))

    def compose(*args, check=True):
        return command(['docker', 'compose', '--project-directory', work, '--env-file', '/dev/null', '-f', spec, *args], check)

    def process_ids():
        return {letter: compose('exec', '-T', 'proxy-' + letter, 'pidof', 'haproxy').stdout.decode().strip()
                for letter in ('a', 'b')}

    def host_check(letter, extra=(), success=True):
        args = ['docker', 'run', '--rm', '--network', project + '_default', '--user', base['user'],
                '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges:true',
                '--volume', f'{project}_runtime-{letter}:/runtime:ro',
                '--volume', f'{work}/proxy-{letter}.cfg:/existing/haproxy.cfg:ro',
                '--entrypoint', '/usr/local/bin/haproxy-agent', image, 'preflight',
                '--metrics-url', f'http://proxy-{letter}:8405/metrics', *extra]
        result = command(args, check=False)
        assert (result.returncode == 0) == success, 'Unexpected host-check result'
        report = json.loads(result.stdout)
        assert str(work) not in json.dumps(report), 'Private path entered setup report'
        return report

    try:
        print('Start existing apps and proxies before management', flush=True)
        compose('up', '-d', 'entrypoint')
        traffic = Traffic(entrypoint_port)
        wait('existing applications before management installation', lambda: traffic.request(0) and traffic.request(1))
        for index in (0, 0, 1, 1) * 4:
            traffic.request(index)
        before_processes = process_ids()
        before_files = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in work.glob('*.cfg')}
        with Load(traffic, 4, 40) as load:
            load.mark('install-manager-alongside-existing-apps')
            command(['docker', 'run', '--rm', '--user', base['user'], '--volume', f'{work}:/bootstrap',
                     image, 'init', '--state-dir', '/bootstrap', '--dns-name', 'manager',
                     '--dns-name', 'ingest', '--dns-name', 'localhost', '--ip', '127.0.0.1'])
            organization = (work / 'organization-id').read_text().strip()
            for role in ('manager', 'ingest'):
                services[role]['environment']['HAPROXY_ORGANIZATION_ID'] = organization
            spec.write_text(json.dumps(specification))
            compose('up', '-d', 'manager', 'ingest')
            api = API(work, manager_port, local_port)
            wait('independent manager', lambda: api.request('/v1/nodes'))
            cluster = api.request('/v1/clusters', {'name': 'Existing infrastructure'})['cluster']['id']
            for letter in ('a', 'b'):
                host_check(letter)
                failure = host_check(letter, ['--metrics-url', 'http://127.0.0.1:1/metrics'], success=False)
                assert failure['checks'][0]['code'] == 'metrics_unavailable'
                args = ['--runtime-socket', '/runtime/admin.sock', '--config-file', '/existing/haproxy.cfg',
                        '--configuration-owner', 'external']
                discovery = host_check(letter, args + ['--allow-runtime-control'])
                assert discovery['checks'][3]['code'] == 'owner_external'
                assert discovery['checks'][4]['code'] == 'runtime_control_ready'
                restricted = host_check(letter, args + ['--runtime-socket', '/runtime/observe.sock', '--allow-runtime-control'], success=False)
                assert restricted['checks'][4]['code'] == 'runtime_admin_required'
                assert not list((work / ('agent-' + letter)).iterdir()), 'Preflight created identity'
                token = api.request(f'/v1/clusters/{cluster}/enrollment-tokens', {'ttl_seconds': 600})['token']
                private(work / ('token-' + letter), token + '\n')
                services['agent-' + letter] = dict(base, entrypoint=['/usr/local/bin/haproxy-agent'],
                    command=['--server-url', 'https://manager:8443', '--ingest-url', 'https://ingest:8443',
                             '--server-ca', '/bootstrap/pki/ca.crt', '--enrollment-token-file', '/token',
                             '--state-dir', '/state', '--name', 'existing-' + letter,
                             '--metrics-url', f'http://proxy-{letter}:8405/metrics', *args],
                    volumes=[f'{work}/pki/ca.crt:/bootstrap/pki/ca.crt:ro', f'{work}/token-{letter}:/token:ro',
                             f'{work}/agent-{letter}:/state', f'runtime-{letter}:/runtime:ro',
                             f'{work}/proxy-{letter}.cfg:/existing/haproxy.cfg:ro'])
            spec.write_text(json.dumps(specification))
            print('Read-only preflight and restricted socket checks passed; enrolling existing nodes', flush=True)
            load.mark('enroll-existing-nodes')
            compose('up', '-d', 'agent-a', 'agent-b')

            def fresh_nodes():
                fleet = api.request('/v1/fleet')['nodes']
                return fleet if len(fleet) == 2 and all(n['telemetry_fresh'] and n['scrape_ok'] for n in fleet) else None

            nodes = wait('two enrolled existing proxies and live metrics', fresh_nodes)
            identities = {letter: hashlib.sha256((work / f'agent-{letter}/identity.json').read_bytes()).hexdigest() for letter in ('a', 'b')}
            for node in nodes:
                view = wait('setup report', lambda: (v if (v := api.request(f"/v1/nodes/{node['id']}/discovery"))['connected'] and
                    (v['control'].get('discovery') or {}).get('setup') else None))
                checks = {c['name']: c for c in view['control']['discovery']['setup']['checks']}
                assert checks['metrics']['status'] == 'ready' and checks['discovery']['status'] == 'ready'
                assert checks['ownership']['code'] == 'owner_external'
                assert checks['runtime_control']['status'] == checks['configuration_control']['status'] == 'disabled'
                assert not view['management_enabled']
            # Restart agents with explicit runtime authority while leaving the
            # external configuration owner and original proxy files in place.
            load.mark('enable-runtime-without-adopting-configuration')
            for letter in ('a', 'b'):
                services['agent-' + letter]['command'].append('--allow-runtime-control')
            spec.write_text(json.dumps(specification))
            compose('up', '-d', '--no-deps', 'agent-a', 'agent-b')
            for node in nodes:
                wait('explicit runtime grant', lambda: api.request(f"/v1/nodes/{node['id']}/discovery")['management_enabled'])
            assert identities == {letter: hashlib.sha256((work / f'agent-{letter}/identity.json').read_bytes()).hexdigest() for letter in ('a', 'b')}
            assert before_processes == process_ids(), 'Onboarding reloaded existing HAProxy processes'
            assert before_files == {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in work.glob('*.cfg')}
            password_path = work / 'local-password'
            private(password_path, secrets.token_urlsafe(32) + '\n')
            compose('run', '--rm', '--no-deps', 'manager', 'user', 'add', '--username', 'pilot-admin',
                    '--role', 'administrator', '--password-file', '/bootstrap/local-password')
            session = api.request('/v1/local/session', {'username': 'pilot-admin', 'password': password_path.read_text().strip()}, local=True)
            api.csrf = session['csrf_token']
            assert len(api.request('/v1/nodes', local=True)['nodes']) == 2
            load.mark('manager-offline-apps-still-serving')
            compose('stop', '-t', '2', 'manager', 'ingest')
            time.sleep(3)
            compose('start', 'manager', 'ingest')
            wait('recovered reporting', fresh_nodes)
            assert before_processes == process_ids()
            time.sleep(1)
        measurements = load.results()
        assert all(row['success'] > 0 and not row['failures'] for row in measurements.values()), 'Existing traffic failed'
        assert {('a', 'a'), ('a', 'b'), ('b', 'a'), ('b', 'b')} <= traffic.seen, 'Both apps must serve through both proxies'
        report = dict(traffic=measurements, existing_configuration_preserved=True, proxy_processes_preserved=True,
                      identities_preserved=True, preflight_did_not_enroll=True, local_login_without_converge=True,
                      external_owner_preserved=True, broken_metrics_rejected=True)
        report['insufficient_runtime_permissions_rejected'] = True
        private(work / 'latest.json', json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2))
        if keep:
            print(f'Isolated pilot UI: http://localhost:{local_port}/ (private login files in {work})')
    except BaseException:
        # Container state is useful during cold-start failures; configuration,
        # command arguments and logs can contain bootstrap credentials.
        states=[]
        try:
            for cid in compose('ps','-aq',check=False).stdout.decode().split():
                container=json.loads(command(['docker','inspect',cid]).stdout)[0]
                state=container['State']
                states.append(dict(service=container['Config']['Labels']['com.docker.compose.service'],
                                   status=state['Status'],exit_code=state['ExitCode'],oom_killed=state['OOMKilled'],
                                   health=(state.get('Health') or {}).get('Status','none')))
        except (RuntimeError,ValueError,OSError):
            pass
        private(work/'latest.json',json.dumps(dict(status='failed',containers=states),indent=2)+'\n')
        print('Onboarding container states:',json.dumps(states),flush=True)
        raise
    finally:
        if not keep:
            compose('down', '--volumes', '--remove-orphans', check=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', default='ecraela-haproxy-onboarding:local', help='locally built candidate image')
    parser.add_argument('--keep', action='store_true', help='retain only this isolated stack for browser checks')
    options = parser.parse_args()
    run(options.image, options.keep)
