#!/usr/bin/env python3
"""Clean signed-install/upgrade/encrypted-restore qualification without Converge.

Uses disposable signed fixtures by default, or two authenticated published
release bundles. Never publishes a release or reads an existing installation.
Only its randomly named Compose projects are cleaned up.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from datetime import datetime
import http.cookiejar
import json
import os
import platform
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

import selfhostctl as ctl

ROOT = Path(__file__).resolve().parents[1]
HAPROXY = 'haproxy@sha256:3c8c1df0ae3bea8834b3890fa6fa3b1653b4d6d9c44aecefba46428bfcfde2c0'
VERSIONS = ('v0.0.1-installtest', 'v0.0.2-installtest', 'v0.0.3-installtest')


def acceptance_host(info):
    # A configured 4 GiB VM reports slightly less after kernel reservations.
    if int(info.get('MemTotal', 0)) < 7 * (1 << 29):
        raise ctl.Error('Use a dedicated Linux Docker Engine with at least 4 GiB allocated for acceptance')
    if int(info.get('ContainersRunning', 0)) != 0:
        raise ctl.Error('Acceptance requires a dedicated Docker Engine without other running workloads')


def prepare_proxy_image():
    try:
        ctl.command(['docker', 'image', 'inspect', HAPROXY])
    except ctl.Error:
        ctl.command(['docker', 'pull', HAPROXY])


def wait(label, function, seconds=60):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            value = function()
            if value:
                return value
        except (OSError, ValueError, KeyError, ctl.Error):
            pass
        time.sleep(0.5)
    owner = getattr(function, '__self__', None)
    if getattr(owner, 'last_readiness', None) is not None:
        print(json.dumps({'readiness_timeout': label, 'observations': owner.last_readiness}), flush=True)
    raise ctl.Error('Timed out: ' + label)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class API:
    def __init__(self, installation):
        self.installation = installation
        c = ctl.configuration(installation)
        self.origin = c['origin']
        self.base = f"https://localhost:{c['manager_port']}"
        self.context = ssl.create_default_context(cafile=installation / 'state/pki/ca.crt')
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=self.context),
                                                  urllib.request.HTTPCookieProcessor(self.cookies))
        self.organization = ctl.state_identity(installation)['organization_id']
        self.token = ctl.trust.regular(installation / 'state/admin-token', 128).decode().strip()
        self.csrf = ''

    def request(self, path, body=None, local=False):
        headers = {'Accept': 'application/json', 'X-Organization-ID': self.organization}
        if local:
            headers.update({'Origin': self.origin, 'X-HAProxy-Session': 'local', 'X-CSRF-Token': self.csrf})
            path = '/api/haproxy' + path
        else:
            headers['Authorization'] = 'Bearer ' + self.token
        if body is not None:
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.base + path,
                                     data=json.dumps(body).encode() if body is not None else None, headers=headers)
        with self.opener.open(req, timeout=8) as response:
            data = response.read()
            return json.loads(data) if data else {}

    def login(self, password):
        result = self.request('/v1/local/session', {'username': 'install-admin', 'password': password}, local=True)
        assert result['organization_id'] == self.organization and result['role'] == 'administrator'
        self.csrf = result['csrf_token']
        return result

    def fresh(self, expected=2, since=0):
        result = self.request('/v1/fleet')
        nodes = result['nodes']
        self.last_readiness = {'node_count': len(nodes), 'expected_nodes': expected,
                               'nodes': [{key: bool(n.get(key)) for key in
                                          ('telemetry_fresh', 'scrape_ok', 'agent_fresh')} for n in nodes]}
        valid = len(nodes) == expected and all(
            n.get('telemetry_fresh') and n.get('scrape_ok') and n.get('agent_fresh') and
            datetime.fromisoformat(n['observed_at'].replace('Z', '+00:00')).timestamp() > since for n in nodes)
        return result if valid else None


def sign_fixture(folder, version, revision, image, signing):
    for name in ('agentctl.py', 'selfhostctl.py'):
        shutil.copyfile(ROOT / 'scripts' / name, folder / name)
    shutil.copyfile(ROOT / 'deploy/selfhost.json', folder / 'selfhost.json')
    manifest = {'schema': 1, 'repository': ctl.trust.REPOSITORY, 'version': version, 'revision': revision,
                'protocol': 1, 'state_schema': 1, 'image': image,
                'artifacts': {p.name: ctl.sha(p) for p in folder.iterdir() if p.is_file()}}
    ctl.write(folder / 'release.json', json.dumps(manifest, sort_keys=True) + '\n')
    ctl.command(['openssl', 'pkeyutl', '-sign', '-inkey', signing, '-rawin',
                 '-in', folder / 'release.json', '-out', folder / 'release.sig'])
    return folder, manifest


def fixture_releases(work, base, reuse=False):
    fixtures = work / 'releases'
    fixtures.mkdir(mode=0o700)
    signing = work / 'publisher.key'
    public = work / 'publisher.pub'
    ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', signing])
    signing.chmod(0o600)
    ctl.command(['openssl', 'pkey', '-in', signing, '-pubout', '-out', public])
    if not base:
        base = 'ecraela-selfhost-fixture-base:local'
        ctl.command(['docker', 'build', '-t', base, ROOT], timeout=900)
    base_id = ctl.command(['docker', 'image', 'inspect', '--format', '{{.Id}}', base]).decode().strip()
    base_tag = 'ecraela-selfhost-fixture-base:' + base_id.removeprefix('sha256:')
    ctl.command(['docker', 'tag', base_id, base_tag])
    info = ctl.local_docker()
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(info['Architecture'], info['Architecture'])
    if reuse:
        build = json.loads(ctl.command(['docker', 'run', '--rm', '--network', 'none',
                                       '--entrypoint', '/usr/local/bin/haproxy-manager',
                                       'ecraela-selfhost-fixture:' + VERSIONS[0], 'version']))
        revision = build['revision']
    else:
        revision = ctl.command(['git', '-C', ROOT, 'rev-parse', 'HEAD']).decode().strip()
    module = 'github.com/ecraela/fleet'
    environment = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch,
                       GOMAXPROCS='2', GOMEMLIMIT='192MiB', GOFLAGS='-p=1')
    # Embedded UI must be present even when using a cached runtime base image.
    if not reuse:
        ctl.command(['pnpm', '--dir', ROOT / 'web', 'build'], timeout=180)
    broken = ROOT / '.build.local/selfhost-fixture/broken.go'
    broken.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    broken.write_text('''package main

import (
\t"encoding/json"
\t"github.com/ecraela/fleet/internal/buildinfo"
\t"os"
\t"syscall"
)

func main() {
\tif len(os.Args) > 1 && os.Args[1] == "version" {
\t\tjson.NewEncoder(os.Stdout).Encode(buildinfo.Current())
\t\treturn
\t}
\tif len(os.Args) > 1 && os.Args[1] == "serve" {
\t\tos.Exit(42)
\t}
\tsyscall.Exec("/usr/local/bin/haproxy-manager-real", os.Args, os.Environ())
}
''')
    result = []
    for index, version in enumerate(VERSIONS):
        print('Building signed fixture ' + version, flush=True)
        folder = fixtures / version
        folder.mkdir(mode=0o700)
        if reuse:
            tag = 'ecraela-selfhost-fixture:' + version
            image = ctl.command(['docker', 'image', 'inspect', '--format', '{{.Id}}', tag]).decode().strip()
            cid = ctl.command(['docker', 'create', image]).decode().strip()
            try:
                for name in ('agent', 'manager'):
                    ctl.command(['docker', 'cp', cid + ':/usr/local/bin/haproxy-' + name,
                                 folder / f'haproxy-{name}-linux-{arch}'])
            finally:
                ctl.command(['docker', 'rm', cid])
            result.append(sign_fixture(folder, version, revision, image, signing))
            continue
        flags = f'-s -w -X {module}/internal/buildinfo.Version={version} -X {module}/internal/buildinfo.Revision={revision}'
        for name in ('manager', 'agent'):
            out = folder / f'haproxy-{name}-linux-{arch}'
            package = str(broken) if index == 2 and name == 'manager' else './cmd/haproxy-' + name
            process = subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags', flags,
                                       '-o', str(out), package], cwd=ROOT, env=environment,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if process.returncode:
                raise ctl.Error('Could not compile the local signed fixture binaries')
        context = folder / 'image'
        context.mkdir(mode=0o700)
        for name in ('manager', 'agent'):
            shutil.copyfile(folder / f'haproxy-{name}-linux-{arch}', context / ('haproxy-' + name))
        dockerfile = f'FROM {base_tag}\nCOPY --chmod=0755 haproxy-manager /usr/local/bin/haproxy-manager\nCOPY --chmod=0755 haproxy-agent /usr/local/bin/haproxy-agent\n'
        if index == 2:
            shutil.copyfile(result[1][0] / f'haproxy-manager-linux-{arch}', context / 'manager-real')
            dockerfile += 'COPY --chmod=0755 manager-real /usr/local/bin/haproxy-manager-real\n'
        (context / 'Dockerfile').write_text(dockerfile)
        tag = 'ecraela-selfhost-fixture:' + version
        ctl.command(['docker', 'build', '--network=none', '-t', tag, context])
        image = ctl.command(['docker', 'image', 'inspect', '--format', '{{.Id}}', tag]).decode().strip()
        result.append(sign_fixture(folder, version, revision, image, signing))
    signing.unlink()
    return result, public


def host_command(bundle, *args, check=True, agent=False):
    tool = 'agentctl.py' if agent else 'selfhostctl.py'
    result = subprocess.run([sys.executable, '-I', str(bundle / tool), *map(str, args)],
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode and check:
        # Curated tool errors do not echo secrets or process commands.
        raise ctl.Error('Standalone release command failed: ' + result.stderr.decode().strip())
    return result


def agent_lifecycle(config, profile, baseline, candidate, trust_args):
    ctl.trust.private_json(profile, config)
    before = ctl.trust.identity(config['state_dir'])
    for action, release in [('install', baseline), ('upgrade', candidate), ('rollback', baseline)]:
        host_command(candidate[0], action, '--config', profile, '--release', release[0],
                     '--version', release[1]['version'], *trust_args, agent=True)
        if ctl.trust.identity(config['state_dir']) != before:
            raise ctl.Error('Agent lifecycle changed its identity or private key')


@contextmanager
def native_manager_hosts(installation, hosts=Path('/etc/hosts')):
    engine = ctl.Engine(installation, ctl.configuration(installation))
    network = engine.c['project'] + '_control'
    addresses = {}
    for service in ('manager', 'ingest'):
        container = engine.compose('ps', '-q', service).decode().strip()
        info = json.loads(ctl.command(['docker', 'inspect', container]))[0]
        address = info['NetworkSettings']['Networks'].get(network, {}).get('IPAddress')
        if not address:
            raise ctl.Error('Missing isolated management bridge address')
        addresses[service] = address
    original = hosts.read_bytes()
    for line in original.decode().splitlines():
        if set(line.split('#', 1)[0].lower().split()[1:]) & addresses.keys():
            raise ctl.Error('Refusing to replace existing management host aliases')
    try:
        hosts.write_bytes(original + ''.join(f'\n{address} {name}\n' for name, address in addresses.items()).encode())
        yield
    finally:
        hosts.write_bytes(original)


def native_agent_lifecycle(args, work, install, baseline, candidate, trust_args):
    import pwd

    if os.geteuid() != 0:
        command = ['sudo', '--preserve-env=GITHUB_ACTIONS,DOCKER_CONFIG,PATH', sys.executable,
                   Path(__file__).resolve(), '--native-agent-worker', '--work', work,
                   '--baseline-release', baseline[0], '--baseline-version', baseline[1]['version'],
                   '--candidate-release', candidate[0], '--candidate-version', candidate[1]['version'],
                   '--manager-port', args.manager_port, '--ingest-port', args.ingest_port]
        if trust_args:
            command += ['--fixture-publisher', trust_args[1]]
        result = subprocess.run(list(map(str, command)), stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=180, check=False)
        if result.returncode:
            try:
                failure = json.loads(result.stdout[:4096])
            except ValueError:
                failure = {}
            message = failure.get('native_agent_error') if isinstance(failure, dict) else None
            if isinstance(message, str):
                raise ctl.Error('Native agent qualification failed: ' + message[:2048])
            raise ctl.Error('Native agent worker failed before reporting a curated error')
        return

    def compose(*options):
        return ctl.command(['docker', 'compose', '--project-directory', work, '--env-file', '/dev/null',
                            '-f', work / 'nodes.json', *options])

    public = Path('/etc/ecraela-fleet-qualification')
    state = Path('/var/lib/ecraela-fleet-qualification')
    unit = Path('/etc/systemd/system/ecraela-haproxy-agent.service')
    if any(p.exists() for p in (public, state, unit)):
        raise ctl.Error('Refusing to replace an existing native agent installation')
    user = 'fleet-agent-ci'
    try:
        account = pwd.getpwnam(user)
    except KeyError:
        ctl.command(['useradd', '--system', '--user-group', '--home-dir', '/nonexistent',
                     '--shell', '/usr/sbin/nologin', user])
        account = pwd.getpwnam(user)
    proxy = compose('ps', '-q', 'haproxy-a').decode().strip()
    info = json.loads(ctl.command(['docker', 'inspect', proxy]))[0]
    networks = info['NetworkSettings']['Networks']
    if len(networks) != 1:
        raise ctl.Error('Expected one isolated proxy network')
    metrics = next(iter(networks.values()))['IPAddress']
    original = work / 'node-a/agent'
    before = ctl.trust.identity(original)
    agent = compose('ps', '-q', 'agent-a').decode().strip()
    if not agent:
        raise ctl.Error('Original Docker agent is missing')
    compose('stop', '-t', '10', 'agent-a')
    try:
        shutil.copytree(original, state, symlinks=True)
        for path in [state, *state.rglob('*')]:
            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                raise ctl.Error('Unexpected native agent state file')
            os.chown(path, account.pw_uid, account.pw_gid)
        public.mkdir(mode=0o755)
        shutil.copyfile(install / 'state/pki/ca.crt', public / 'ca.crt')
        (public / 'ca.crt').chmod(0o644)
        config = {'mode': 'linux', 'user': user, 'install_dir': str(public / 'software'),
                  'state_dir': str(state), 'arguments': [
                      '--server-url', 'https://manager:8443',
                      '--ingest-url', 'https://ingest:8443',
                      '--server-ca', str(public / 'ca.crt'), '--state-dir', str(state),
                      '--name', 'node-a',
                      '--metrics-url', f'http://{metrics}:8404/metrics', '--interval', '1s']}
        # The copied identity remains bound to its original endpoints. Resolve
        # those names on this disposable host rather than rewriting its state.
        with native_manager_hosts(install):
            agent_lifecycle(config, work / 'native-agent.json', baseline, candidate, trust_args)
            since = time.time()
            ctl.command(['systemctl', 'restart', 'ecraela-haproxy-agent.service'])
            ctl.trust.wait_healthy(config, baseline[1], since, before, 45)
        if ctl.trust.identity(state) != before or ctl.trust.identity(original) != before:
            raise ctl.Error('Native agent restart changed the original identity or key')
    finally:
        subprocess.run(['systemctl', 'disable', '--now', 'ecraela-haproxy-agent.service'],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        unit.unlink(missing_ok=True)
        subprocess.run(['systemctl', 'daemon-reload'], check=True)
        shutil.rmtree(state, ignore_errors=True)
        shutil.rmtree(public, ignore_errors=True)
        ctl.command(['docker', 'start', agent])


def published_releases(args):
    """Authenticate every file and image before executing downloaded installers."""
    result = []
    for folder, version in ((args.baseline_release, args.baseline_version),
                            (args.candidate_release, args.candidate_version)):
        manifest, profile, _ = ctl.release(folder, version)
        for name, digest in manifest['artifacts'].items():
            raw = ctl.trust.regular(folder / name, ctl.trust.MAX_BINARY)
            if ctl.hashlib.sha256(raw).hexdigest() != digest:
                raise ctl.Error('Published release artifact checksum differs: ' + name)
        if not manifest['image'].startswith('ghcr.io/ecraela/fleet@sha256:'):
            raise ctl.Error('Published qualification requires the signed GHCR image')
        ctl.command(['cosign', 'verify', '--certificate-identity',
                     f'https://github.com/{ctl.trust.REPOSITORY}/.github/workflows/release.yml@refs/tags/{version}',
                     '--certificate-oidc-issuer', 'https://token.actions.githubusercontent.com', manifest['image']])
        if result:
            ctl.compatible(result[0][2], profile)
            if ctl.trust.version_tuple(version) <= ctl.trust.version_tuple(result[0][1]['version']):
                raise ctl.Error('Published candidate must be newer than its baseline')
        result.append((folder, manifest, profile))
    return [(folder, manifest) for folder, manifest, _ in result]


def node_spec(work, installation, manifest, certificate_root, ports):
    c = ctl.configuration(installation)
    network = c['project'] + '_control'
    services = {}
    for backend in ('a', 'b'):
        services['backend-' + backend] = {
            'image': manifest['image'], 'entrypoint': ['/usr/local/bin/demo-backend'],
            'environment': {'DEMO_BACKEND': 'app-' + backend}, 'networks': ['control'],
            'read_only': True, 'cap_drop': ['ALL'], 'mem_limit': '24m',
            'healthcheck': {'test': ['CMD', 'curl', '-fsS', 'http://localhost:8080/healthz'],
                            'interval': '1s', 'timeout': '2s', 'retries': 30}}
    for index, letter in enumerate(('a', 'b')):
        node = work / ('node-' + letter)
        for directory in ('agent', 'haproxy', 'tls'):
            (node / directory).mkdir(mode=0o700, parents=True, exist_ok=True)
        cfg = f'''global
  maxconn 256
defaults
  mode http
  timeout connect 1s
  timeout client 5s
  timeout server 5s
frontend public
  bind *:8080
  bind *:8443 ssl crt /tls/server.pem
  http-response set-header X-HAProxy-Node node-{letter}
  acl admin path_beg /admin
  use_backend app_b if admin
  default_backend app_a
frontend metrics
  bind *:8404
  http-request use-service prometheus-exporter if {{ path /metrics }}
backend app_a
  server app-a backend-a:8080 check
backend app_b
  server app-b backend-b:8080 check
'''
        ctl.write(node / 'haproxy/haproxy.cfg', cfg)
        key, csr, certificate = node / 'tls/server.key', node / 'tls/server.csr', node / 'tls/server.crt'
        ctl.command(['openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                     '-nodes', '-keyout', key, '-out', csr, '-subj', '/CN=localhost'])
        ctl.command(['openssl', 'x509', '-req', '-in', csr, '-CA', certificate_root / 'ca.crt',
                     '-CAkey', certificate_root / 'ca.key', '-set_serial', str(index + 10), '-days', '2',
                     '-extfile', certificate_root / 'extensions', '-out', certificate])
        ctl.write(node / 'tls/server.pem', certificate.read_bytes() + key.read_bytes())
        key.chmod(0o600)
        services['haproxy-' + letter] = {
            'image': HAPROXY, 'user': f'{os.getuid()}:{os.getgid()}',
            'command': ['haproxy', '-W', '-db', '-f', '/config/haproxy.cfg'],
            'volumes': [f'{node}/haproxy:/config:ro', f'{node}/tls:/tls:ro'],
            'read_only': True, 'cap_drop': ['ALL'], 'networks': ['control'], 'mem_limit': '64m',
            'depends_on': {name: {'condition': 'service_healthy'} for name in ('backend-a', 'backend-b')},
            'ports': [f'127.0.0.1:{ports[index * 2]}:8080', f'127.0.0.1:{ports[index * 2 + 1]}:8443']}
        services['agent-' + letter] = {
            'image': manifest['image'], 'user': f'{os.getuid()}:{os.getgid()}',
            'entrypoint': ['/usr/local/bin/haproxy-agent'],
            'command': ['--server-url', 'https://manager:8443', '--ingest-url', 'https://ingest:8443',
                        '--server-ca', '/pki/ca.crt', '--state-dir', '/state',
                        '--enrollment-token-file', '/enrollment-token', '--name', 'node-' + letter,
                        '--metrics-url', f'http://haproxy-{letter}:8404/metrics'],
            'volumes': [f'{node}/agent:/state', f'{installation}/state/pki/ca.crt:/pki/ca.crt:ro',
                        f'{work}/enrollment-token-{letter}:/enrollment-token:ro'],
            'environment': {'GOMEMLIMIT': '32MiB', 'GOMAXPROCS': '1'},
            'read_only': True, 'cap_drop': ['ALL'], 'networks': ['control'], 'mem_limit': '48m'}
    return {'name': 'ecraela-hm-nodes-' + secrets.token_hex(6), 'services': services,
            'networks': {'control': {'external': True, 'name': network}}}


@contextmanager
def traffic(ports, ca):
    stop = threading.Event()
    counts = {'requests': 0, 'failures': 0}
    lock = threading.Lock()
    context = ssl.create_default_context(cafile=ca)
    def worker(index):
        counter = 0
        while not stop.is_set():
            n = (counter + index) % 4
            backend = 'b' if counter % 2 else 'a'
            req = urllib.request.Request(('https' if n % 2 else 'http') + '://localhost:' + str(ports[n]) +
                                          ('/admin/users' if backend == 'b' else '/shop/items'))
            failed = False
            try:
                with urllib.request.urlopen(req, context=context, timeout=3) as response:
                    failed = response.status != 200 or response.headers.get('X-Demo-Backend') != 'app-' + backend
            except OSError:
                failed = True
            with lock:
                counts['requests'] += 1
                counts['failures'] += int(failed)
            counter += 1
            stop.wait(0.2)
    with ThreadPoolExecutor(max_workers=2) as pool:
        tasks = [pool.submit(worker, i) for i in range(2)]
        try:
            yield counts
        finally:
            stop.set()
            for task in tasks:
                task.result()


def scenario(args, work, report):
    if args.baseline_release:
        baseline, candidate = published_releases(args)
        broken, publisher = None, None
        report['publisher_verification'] = 'sigstore-workflow-identity-and-image'
        report['published_releases'] = [dict(version=m['version'], revision=m['revision'], image=m['image'])
                                        for _, m in (baseline, candidate)]
    else:
        releases, publisher = fixture_releases(work, args.base_image, args.reuse_images)
        baseline, candidate, broken = releases
    if not publisher:
        # Qualify a direct candidate install as well as the baseline upgrade.
        fresh = work / 'candidate-clean'
        password = work / 'candidate-password'
        ctl.write(password, secrets.token_urlsafe(32) + '\n')
        host_command(candidate[0], 'install', '--directory', fresh, '--release', candidate[0],
                     '--version', candidate[1]['version'], '--origin', 'https://localhost:' + str(args.manager_port),
                     '--manager-port', args.manager_port, '--ingest-port', args.ingest_port,
                     '--admin-username', 'install-admin', '--password-file', password)
        fresh_api = API(fresh)
        fresh_api.login(password.read_text().strip())
        assert not fresh_api.request('/v1/fleet')['nodes']
        assert fresh_api.cookies and all(cookie.secure for cookie in fresh_api.cookies)
        fresh_engine = ctl.Engine(fresh, ctl.configuration(fresh))
        expected = ctl.load(fresh / 'deployment.json')['profile']['database_schema']
        actual = fresh_engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
                                     '-d', 'haproxy_manager', '-Atc',
                                     'SELECT max(version) FROM hm_schema_migrations;').decode().strip()
        assert actual == str(expected)
        report.update(fresh_candidate_install=True, candidate_database_schema=int(actual))
        fresh_engine.compose('down', '--volumes', '--remove-orphans')
        print('PASS: direct candidate install, HTTPS local login and database schema.', flush=True)
    trust_args = ['--public-key', publisher] if publisher else []
    install = work / 'customer'
    password = work / 'administrator.password'
    ctl.write(password, secrets.token_urlsafe(32) + '\n')
    api_port, ingest_port = args.manager_port, args.ingest_port
    console = ('http://localhost:' + str(args.console_port) if publisher else
               'https://localhost:' + str(api_port))
    dev_args = ['--dev-http-port', args.console_port] if publisher else []
    bundle = baseline[0]
    print('Installing into fresh storage from signed release files only.', flush=True)
    host_command(candidate[0], 'install', '--directory', install, '--release', bundle, '--version', baseline[1]['version'],
                 *trust_args, '--origin', console, *dev_args,
                 '--manager-port', api_port, '--ingest-port', ingest_port, '--admin-username', 'install-admin',
                 '--password-file', password, '--agent-manager-url', 'https://manager:8443',
                 '--agent-ingest-url', 'https://ingest:8443')
    api = API(install)
    api.login(password.read_text().strip())
    if not publisher:
        assert api.cookies and all(cookie.secure for cookie in api.cookies)
        report['https_local_login_secure_cookies'] = True
    original = ctl.state_identity(install)
    report['fresh_install_local_login'] = True
    engine = ctl.Engine(install, ctl.configuration(install))
    baseline_schema = engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
                                    '-d', 'haproxy_manager', '-Atc',
                                    'SELECT max(version) FROM hm_schema_migrations;').decode().strip()
    assert baseline_schema == str(ctl.load(install / 'deployment.json')['profile']['database_schema'])
    report['baseline_database_schema'] = int(baseline_schema)
    isolated_role = engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
        '-d', 'haproxy_manager', '-Atc',
        "SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole FROM pg_roles WHERE rolname='hm_runtime';").decode().strip()
    grants = engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner', '-d', 'haproxy_manager',
        '-Atc', "SELECT has_table_privilege('hm_runtime','hm_local_audit','TRUNCATE') OR has_schema_privilege('hm_runtime','public','CREATE');").decode().strip()
    assert isolated_role == 't' and grants == 'f'
    report['runtime_database_role_limited'] = True
    report['installer_platform'] = {'os': platform.system(), 'architecture': platform.machine()}
    cluster = api.request('/v1/clusters', {'name': 'Fresh self-hosted cluster'})['cluster']['id']
    for letter in ('a', 'b'):
        token = api.request(f'/v1/clusters/{cluster}/enrollment-tokens', {'ttl_seconds': 600})['token']
        ctl.write(work / ('enrollment-token-' + letter), token + '\n')
    authority = work / 'application-authority'
    authority.mkdir(mode=0o700)
    ctl.command(['openssl', 'req', '-new', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                 '-nodes', '-keyout', authority / 'ca.key', '-out', authority / 'ca.crt', '-days', '2',
                 '-subj', '/CN=Disposable installation test CA', '-addext', 'basicConstraints=critical,CA:TRUE',
                 '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-addext', 'subjectKeyIdentifier=hash'])
    (authority / 'ca.key').chmod(0o600)
    ctl.write(authority / 'extensions', 'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n'
              'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\n'
              'authorityKeyIdentifier=keyid,issuer\n')
    ports = [port() for _ in range(4)]
    prepare_proxy_image()
    nodes = node_spec(work, install, baseline[1], authority, ports)
    nodes_file = work / 'nodes.json'
    ctl.trust.private_json(nodes_file, nodes)
    def compose(*options):
        return ctl.command(['docker', 'compose', '--project-directory', work, '--env-file', '/dev/null',
                            '-f', nodes_file, *options])
    compose('up', '-d', '--no-build', '--pull', 'never')
    wait('two new enrolled identities and metrics', api.fresh)
    identities = {letter: ctl.trust.identity(work / ('node-' + letter) / 'agent') for letter in ('a', 'b')}
    report['two_nodes_enrolled'] = True
    # Verify a real historic range before checkpointing rather than only "now".
    time.sleep(3)
    node_id = identities['a']['node_id']
    def history_ready():
        h = api.request(f'/v1/nodes/{node_id}/history?range=15m')
        return h if any(s['values'] for s in h['series']) else None
    historic = wait('historic metric points', history_ready)
    with traffic(ports, authority / 'ca.crt') as counts:
        transitions = [('upgrade', candidate), ('rollback', baseline)]
        if not publisher:
            transitions.append(('upgrade', candidate))
        report['database_schemas_after_transition'] = []
        for action, fixture in transitions:
            # Keep the new authenticated host tool for software rollback. The
            # old installer predates the additive schema-20 compatibility rule.
            host_command(candidate[0], action, '--directory', install, '--release', fixture[0],
                         '--version', fixture[1]['version'], *trust_args)
            wait('fresh telemetry after ' + action, api.fresh)
            api.request('/v1/local/session', local=True)
            assert ctl.state_identity(install) == original
            actual = engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
                                    '-d', 'haproxy_manager', '-Atc',
                                    'SELECT max(version) FROM hm_schema_migrations;').decode().strip()
            expected = ctl.load(install / 'deployment.json')['profile']['database_schema']
            assert actual == str(expected) or (expected == 19 and actual == '20')
            report['database_schemas_after_transition'].append(
                {'action': action, 'version': fixture[1]['version'], 'database_schema': int(actual)})
        report['upgrade_and_rollback'] = True
        for letter in ('a', 'b'):
            config = {'mode': 'docker', 'state_dir': str(work / ('node-' + letter) / 'agent'),
                      'install_dir': str(work / ('agent-software-' + letter)),
                      'docker': {'service': 'agent-' + letter, 'project_directory': str(work),
                                 'env_file': '/dev/null', 'files': [str(nodes_file)]}}
            agent_lifecycle(config, work / ('agent-' + letter + '.json'), baseline, candidate, trust_args)
            wait('fresh telemetry after agent rollback ' + letter, api.fresh)
        report['docker_agent_install_upgrade_rollback'] = True
        if args.native_agent:
            native_agent_lifecycle(args, work, install, baseline, candidate, trust_args)
            wait('fresh telemetry after native agent recovery', api.fresh)
            report['linux_systemd_agent_install_upgrade_rollback_restart'] = True
        if broken:
            failed = host_command(candidate[0], 'upgrade', '--directory', install, '--release', broken[0],
                                  '--version', broken[1]['version'], *trust_args, '--timeout', 8, check=False)
            h = ctl.load(install / 'deployment.json')
            assert failed.returncode and h['last_result'] == 'automatic_rollback' and h['current'] == baseline[1]
            try:
                wait('telemetry after automatic rollback', api.fresh)
            except ctl.Error:
                report['rollback_readiness'] = getattr(api, 'last_readiness', None)
                report['node_container_states'] = []
                for cid in compose('ps', '-aq').decode().split():
                    observation = ctl.command(['docker', 'inspect', '--format',
                        '{"service":{{json (index .Config.Labels "com.docker.compose.service")}},'
                        '"status":{{json .State.Status}},"exit_code":{{json .State.ExitCode}},'
                        '"oom_killed":{{json .State.OOMKilled}}}', cid])
                    report['node_container_states'].append(json.loads(observation))
                raise
            report['failed_upgrade_automatic_rollback'] = True
        else:
            # Checkpoint the actual candidate, then restore that same exact release.
            bundle = candidate[0]
        # Back up one fenced node at a time; the same tool runs on the node host.
        age_identity, backup_key, backup_pub = work / 'restore.identity', work / 'backup.key', work / 'backup.pub'
        ctl.command(['age-keygen', '-o', age_identity])
        age_identity.chmod(0o600)
        recipient = ctl.command(['age-keygen', '-y', age_identity]).decode().strip()
        ctl.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', backup_key])
        backup_key.chmod(0o600)
        ctl.command(['openssl', 'pkey', '-in', backup_key, '-pubout', '-out', backup_pub])
        node_backups = {}
        for letter in ('a', 'b'):
            compose('stop', 'agent-' + letter)
            cid = compose('ps', '-aq', 'agent-' + letter).decode().strip()
            node = work / ('node-' + letter)
            config = work / ('backup-node-' + letter + '.json')
            ctl.trust.private_json(config, {'state_dir': str(node / 'agent'), 'containers': [cid],
                                          'paths': {'haproxy': str(node / 'haproxy'), 'tls': str(node / 'tls')}})
            encrypted = work / ('node-' + letter + '.age')
            host_command(candidate[0], 'node-backup', '--node-config', config, '--backup', encrypted,
                         '--recipient', recipient, '--signing-key', backup_key)
            node_backups[letter] = encrypted
            compose('start', 'agent-' + letter)
        encrypted = work / 'installation.age'
        host_command(candidate[0], 'backup', '--directory', install, '--backup', encrypted,
                     '--recipient', recipient, '--signing-key', backup_key)
        report['encrypted_signed_backup'] = True
        wait('telemetry after backup', api.fresh)
        restored = work / 'restored'
        host_command(candidate[0], 'restore', '--directory', restored, '--backup', encrypted,
                     '--identity-file', age_identity, '--backup-public-key', backup_pub, *trust_args,
                     '--manager-port', api_port, '--ingest-port', ingest_port)
        new_c = ctl.configuration(restored)
        assert new_c['project'] != ctl.configuration(install)['project']
        assert not ctl.Engine(restored, new_c).running()
        assert ctl.state_identity(restored) == original
        report['restore_fresh_storage_stays_stopped'] = True
        denied = host_command(candidate[0], 'activate', '--directory', restored, '--source-fenced', check=False)
        assert denied.returncode and not ctl.Engine(restored, new_c).running()
        report['activation_refused_live_source'] = True
        # Original data plane keeps serving while its management plane is fenced.
        host_command(candidate[0], 'stop', '--directory', install)
        compose('stop', 'agent-a', 'agent-b')
        host_command(candidate[0], 'activate', '--directory', restored, '--source-fenced')
        restored_api = API(restored)
        restored_api.login(password.read_text().strip())
        # Start restored agents only after their originals are stopped; no enroll token.
        restored_services = {}
        restored_ports = []
        for letter, encrypted_node in node_backups.items():
            target = work / ('restored-node-' + letter)
            host_command(candidate[0], 'node-restore', '--directory', target, '--backup', encrypted_node,
                         '--identity-file', age_identity, '--backup-public-key', backup_pub)
            assert ctl.trust.identity(target / 'agent') == identities[letter]
            assert (target / 'tls/server.key').read_bytes() == (work / ('node-' + letter) / 'tls/server.key').read_bytes()
            http_port, https_port = port(), port()
            restored_ports.append((letter, http_port, https_port))
            proxy = json.loads(json.dumps(nodes['services']['haproxy-' + letter]))
            proxy.pop('depends_on', None)
            proxy.update(volumes=[f'{target}/haproxy:/config:ro', f'{target}/tls:/tls:ro'],
                         networks=['source', 'target'],
                         ports=[f'127.0.0.1:{http_port}:8080', f'127.0.0.1:{https_port}:8443'])
            restored_services['restored-haproxy-' + letter] = proxy
            spec = json.loads(json.dumps(nodes['services']['agent-' + letter]))
            spec['volumes'] = [f'{target}/agent:/state', f'{restored}/state/pki/ca.crt:/pki/ca.crt:ro']
            i = spec['command'].index('--enrollment-token-file')
            del spec['command'][i:i + 2]
            index = spec['command'].index('--metrics-url')
            spec['command'][index + 1] = f'http://restored-haproxy-{letter}:8404/metrics'
            spec['networks'] = ['source', 'target']
            restored_services['agent-' + letter] = spec
        restored_nodes = {'name': 'ecraela-hm-restored-agents-' + secrets.token_hex(6),
                          'services': restored_services,
                          'networks': {'source': {'external': True, 'name': ctl.configuration(install)['project'] + '_control'},
                                       'target': {'external': True, 'name': new_c['project'] + '_control'}}}
        restore_nodes_file = work / 'restored-agents.json'
        ctl.trust.private_json(restore_nodes_file, restored_nodes)
        resumed_at = time.time()
        ctl.command(['docker', 'compose', '-f', restore_nodes_file, 'up', '-d', '--no-build', '--pull', 'never'])
        wait('restored original identities reporting new telemetry',
             lambda: restored_api.fresh(since=resumed_at))
        recovered_requests = 0
        tls_context = ssl.create_default_context(cafile=authority / 'ca.crt')
        for letter, http_port, https_port in restored_ports:
            expected_certificate = ssl.PEM_cert_to_DER_cert(
                (work / ('restored-node-' + letter) / 'tls/server.crt').read_text())
            with socket.create_connection(('localhost', https_port), timeout=5) as conn:
                with tls_context.wrap_socket(conn, server_hostname='localhost') as secure:
                    assert secure.getpeercert(binary_form=True) == expected_certificate
            for i in range(20):
                use_tls = i % 2 == 0
                app_b = i % 4 < 2
                url = ('https' if use_tls else 'http') + '://localhost:' + str(https_port if use_tls else http_port)
                with urllib.request.urlopen(url + ('/admin/users' if app_b else '/shop/items'),
                                            context=tls_context, timeout=5) as response:
                    assert response.status == 200 and response.headers['X-Demo-Backend'] == ('app-b' if app_b else 'app-a')
                    recovered_requests += 1
        report['restored_tls_serving_verified'] = True
        report['restored_http_https_requests'] = recovered_requests
        assert restored_api.request('/v1/clusters')['clusters'][0]['id'] == cluster
        history = restored_api.request(f'/v1/nodes/{node_id}/history?range=15m')
        points = lambda h: {(s['metric'].get('series'), p[0], p[1]) for s in h['series'] for p in s['values']}
        if not points(history) & points(historic):
            raise ctl.Error('Historical telemetry did not survive restore')
        report['node_ids_tls_keys_history_preserved'] = True
        report['local_login_after_restore'] = True
        report['no_converge_services_or_state'] = True
        report['serving_traffic'] = counts
    # Include any request that finished while the workload was being stopped.
    if counts['failures'] or counts['requests'] < 20:
        raise ctl.Error('Application serving failed during management lifecycle qualification')
    # Keep only the restored proxy pair active when retaining the test lab.
    compose('stop', 'haproxy-a', 'haproxy-b')
    current = ctl.load(restored / 'deployment.json')['current']
    report.update(passed=True, directory=str(restored), console=console, version=current['version'])
    print(json.dumps(report, indent=2), flush=True)


def cleanup(work):
    for file in (work / 'restored-agents.json', work / 'nodes.json'):
        if file.exists():
            ctl.command(['docker', 'compose', '-f', file, 'down', '--volumes', '--remove-orphans'])
    for directory in (work / 'customer', work / 'restored', work / 'candidate-clean'):
        if (directory / 'runtime.json').exists():
            ctl.Engine(directory, ctl.configuration(directory)).compose('down', '--volumes', '--remove-orphans')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work', type=Path, required=True, help='new private disposable environment directory')
    p.add_argument('--base-image', help='optional cached runtime image for lightweight fixture builds')
    p.add_argument('--reuse-images', action='store_true', help='reuse previously compiled local fixture images')
    p.add_argument('--baseline-release', type=Path, help='downloaded published baseline bundle; uses Sigstore')
    p.add_argument('--baseline-version', help='exact published baseline tag')
    p.add_argument('--candidate-release', type=Path, help='downloaded published candidate bundle; uses Sigstore')
    p.add_argument('--candidate-version', help='exact published candidate tag')
    p.add_argument('--native-agent', action='store_true', help='test systemd using sudo on an ephemeral Linux GitHub Actions runner')
    p.add_argument('--native-agent-worker', action='store_true', help=argparse.SUPPRESS)
    p.add_argument('--fixture-publisher', type=Path, help=argparse.SUPPRESS)
    p.add_argument('--manager-port', type=int, default=18543)
    p.add_argument('--ingest-port', type=int, default=19543)
    p.add_argument('--console-port', type=int, default=5181)
    p.add_argument('--keep', action='store_true', help='keep only this disposable installation for inspection')
    args = p.parse_args()
    if args.native_agent or args.native_agent_worker:
        if platform.system() != 'Linux' or os.getenv('GITHUB_ACTIONS') != 'true':
            p.error('Native agent qualification requires an ephemeral Linux GitHub Actions runner')
    if args.native_agent_worker:
        if os.geteuid() != 0 or not all((args.baseline_release, args.baseline_version,
                                       args.candidate_release, args.candidate_version)):
            p.error('Native agent worker requires root and two authenticated release bundles')
        releases = []
        for folder, version in ((args.baseline_release, args.baseline_version),
                                (args.candidate_release, args.candidate_version)):
            manifest, _, _ = ctl.release(folder, version, args.fixture_publisher)
            for name, digest in manifest['artifacts'].items():
                if ctl.hashlib.sha256(ctl.trust.regular(folder / name, ctl.trust.MAX_BINARY)).hexdigest() != digest:
                    raise ctl.Error('Native agent release artifact checksum differs: ' + name)
            releases.append((folder, manifest))
        trust_args = ['--public-key', args.fixture_publisher] if args.fixture_publisher else []
        native_agent_lifecycle(args, args.work.resolve(), args.work.resolve() / 'customer',
                               *releases, trust_args)
        return
    if args.fixture_publisher:
        p.error('Fixture publisher is restricted to the native CI worker')
    published = (args.baseline_release, args.baseline_version, args.candidate_release, args.candidate_version)
    if any(published) and (not all(published) or args.base_image or args.reuse_images):
        p.error('Supply both published bundles and exact versions, without fixture image options')
    acceptance_host(ctl.local_docker())
    work = args.work.resolve()
    work.mkdir(mode=0o700)
    ctl.ERROR_LOG = work / 'last-error.json'
    report = {'passed': False}
    try:
        scenario(args, work, report)
    finally:
        ctl.trust.private_json(work / 'latest.json', report)
        if not args.keep:
            cleanup(work)
            # Preserve the sanitized evidence, not test keys or plaintext state.
            for path in work.iterdir():
                if path.name != 'latest.json':
                    shutil.rmtree(path) if path.is_dir() else path.unlink()


if __name__ == '__main__':
    try:
        main()
    except ctl.Error as error:
        if '--native-agent-worker' not in sys.argv:
            raise
        print(json.dumps({'native_agent_error': str(error)[:2048]}), flush=True)
        raise SystemExit(1)
