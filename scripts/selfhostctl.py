#!/usr/bin/env -S python3 -I
"""Host-owned Linux/Docker installation, compatible upgrades and age backups.

Runs outside the manager. No Converge, Docker socket mount, remote shell or
unsigned release mode. Restores always use fresh state and stay stopped until
the original installation has been fenced.
"""
import sys

if __name__ == '__main__' and not sys.flags.isolated:
    raise SystemExit('Run this installer with python3 -I to exclude unsigned Python inputs')

import argparse
import base64
from contextlib import contextmanager
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shutil
import ssl
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.parse
import uuid
import types


AGENTCTL_SHA256 = '01e3f37fc1e56fed24e8c32cfe7899dbcfa98f6e9fe52e79977e2dfda1c99d11'


def load_agentctl():
    # The authenticated host tool pins its one executable sibling dependency.
    # Compile this verified snapshot; importing by path would reopen mutable input.
    path = Path(__file__).resolve().with_name('agentctl.py')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > 1 << 20:
            raise SystemExit('Expected a bounded regular installer helper')
        raw = source.read((1 << 20) + 1)
    if len(raw) > 1 << 20 or hashlib.sha256(raw).hexdigest() != AGENTCTL_SHA256:
        raise SystemExit('Installer helper differs from the authenticated host tool')
    module = types.ModuleType('ecraela_agentctl')
    module.__file__ = str(path)
    exec(compile(raw, str(path), 'exec'), module.__dict__)
    return module


trust = load_agentctl()

Error = trust.InstallError
SERVICES = ('postgres', 'metrics', 'logs', 'manager', 'ingest')
WRITERS = ('manager', 'ingest', 'metrics', 'logs')
MAX_ARCHIVE = 32 << 30
MAX_ENTRIES = 200_000
ERROR_LOG = None


def command(args, *, source=None, destination=None, timeout=180):
    result = subprocess.run([str(a) for a in args], stdin=source,
                            stdout=destination if destination is not None else subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, check=False)
    if result.returncode:
        if ERROR_LOG and ERROR_LOG.parent.is_dir():
            trust.private_json(ERROR_LOG, {'program': Path(str(args[0])).name,
                                           'exit_code': result.returncode,
                                           'stderr': result.stderr[:262144].decode(errors='replace')})
        raise Error(f'{Path(str(args[0])).name} failed (exit {result.returncode}); inspect local logs')
    return result.stdout


def write(path, data):
    path = Path(path)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as out:
        out.write(data if isinstance(data, bytes) else data.encode())
        out.flush()
        os.fsync(out.fileno())


def load(path):
    return json.loads(trust.regular(path, 1 << 20))


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def private_directory(path, *, create=False):
    path = Path(path).absolute()
    # Do not follow a symlink hidden in a parent component.
    if path.resolve() != path:
        raise Error('Installation and staging paths must not traverse symlinks')
    if create:
        path.mkdir(mode=0o700, parents=False)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
        raise Error('A private directory with mode 0700 is required')
    return path


@contextmanager
def locked(root):
    root = private_directory(root)
    fd = os.open(root / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Error('Another installation operation is running') from None
        yield root


def local_docker():
    if os.getenv('DOCKER_HOST') or os.getenv('DOCKER_CONTEXT'):
        raise Error('Use a local Unix Docker context without host overrides')
    context = json.loads(command(['docker', 'context', 'inspect']))
    if not context[0]['Endpoints']['docker']['Host'].startswith('unix://'):
        raise Error('Remote Docker contexts are refused')
    info = json.loads(command(['docker', 'info', '--format', '{{json .}}']))
    if info.get('OSType') != 'linux':
        raise Error('A Linux Docker Engine is required')
    command(['docker', 'compose', 'version'])
    return info


class SignedRelease(dict):
    """Parsed manifest plus the exact verified bytes retained for recovery."""
    def __init__(self, manifest, files):
        super().__init__(manifest)
        self.files = files


def release(folder, version, key=None):
    folder = Path(folder)
    signature = 'release.sig' if key else 'release.sigstore.json'
    names = ('release.json', signature, 'selfhost.json', 'agentctl.py', 'selfhostctl.py')
    # Authenticate, parse and retain a single immutable snapshot. An incoming
    # directory changing during verification cannot substitute a storage profile
    # or leave an unauthenticated manifest in the recovery bundle.
    files = {name: trust.regular(folder / name, 1 << 20) for name in names}
    with tempfile.TemporaryDirectory(prefix='hm-release-') as tmp:
        for name, body in files.items():
            write(Path(tmp) / name, body)
        verified, publisher = trust._verify_release(Path(tmp), version, key)
    manifest = SignedRelease(verified, files)
    for name in ('selfhost.json', 'agentctl.py', 'selfhostctl.py'):
        if hashlib.sha256(files[name]).hexdigest() != manifest['artifacts'].get(name):
            raise Error('Self-hosted release artifact differs from the signed manifest')
    for name in trust.LEGAL_ARTIFACTS:
        if name in manifest['artifacts']:
            body = trust.regular(folder / name, trust.MAX_BINARY)
            if hashlib.sha256(body).hexdigest() != manifest['artifacts'][name]:
                raise Error('Legal artifact differs from the signed manifest')
            manifest.files[name] = body
    profile = json.loads(files['selfhost.json'])
    repositories = {'postgres': 'postgres', 'metrics': 'victoriametrics/victoria-metrics',
                    'logs': 'victoriametrics/victoria-logs'}
    if (profile.get('schema') != 1 or profile.get('database_schema') not in (19, 20) or
            set(profile.get('images', {})) != set(repositories)):
        raise Error('Unsupported self-hosted storage or database schema')
    for name, repository in repositories.items():
        if not re.fullmatch(re.escape(repository) + r'@sha256:[a-f0-9]{64}', profile['images'][name]):
            raise Error('Every storage image must have a signed repository and digest pin')
    return manifest, profile, publisher


def retain(root, manifest):
    dest = root / 'releases' / manifest['version']
    dest.mkdir(mode=0o700, parents=True, exist_ok=True)
    for name, body in manifest.files.items():
        if (dest / name).exists():
            maximum = trust.MAX_BINARY if name in trust.LEGAL_ARTIFACTS else 1 << 20
            if trust.regular(dest / name, maximum) != body:
                raise Error('A retained exact release changed')
        else:
            write(dest / name, body)
    return dest


def owned_state(root, c):
    state = root / 'state'
    state.mkdir(mode=0o700)
    if os.geteuid() == 0:
        os.chown(state, c['uid'], c['gid'])
    return state


def origin(value, dev=False):
    u = urllib.parse.urlsplit(value)
    if (u.scheme not in ('http', 'https') or not u.hostname or u.username or
            u.password or u.path or u.query or u.fragment or
            any(ord(ch) < 33 for ch in value)):
        raise Error('Use an exact HTTPS console origin')
    if dev:
        if u.scheme != 'http' or u.hostname != 'localhost':
            raise Error('Development HTTP is restricted to localhost')
    elif u.scheme != 'https':
        raise Error('Production local login requires HTTPS')
    # Accessing .port also validates malformed/out-of-range ports.
    _ = u.port
    return u


def configuration(root):
    c = load(root / 'installation.json')
    if (c.get('schema') != 1 or not re.fullmatch(r'ecraela-hm-[a-f0-9]{16}', c.get('project', '')) or
            not isinstance(c.get('uid'), int) or c['uid'] <= 0 or
            not isinstance(c.get('gid'), int) or c['gid'] <= 0):
        raise Error('Invalid installation identity or service account')
    origin(c['origin'], bool(c.get('dev_http_port')))
    ipaddress.IPv4Address(c['bind_address'])
    ports = [c['manager_port'], c['ingest_port']] + ([c['dev_http_port']] if c.get('dev_http_port') else [])
    if len(set(ports)) != len(ports) or any(type(p) is not int or not 1024 <= p <= 65535 for p in ports):
        raise Error('Choose distinct published ports in 1024–65535')
    if c.get('dev_http_port') and (c['bind_address'] != '127.0.0.1' or
                                   origin(c['origin'], True).port != c['dev_http_port']):
        raise Error('Development HTTP origin and loopback port must match')
    if c.get('connector_relay_url'):
        origin(c['connector_relay_url'])
    if type(c.get('connector_private_ca', False)) is not bool:
        raise Error('Invalid connector trust configuration')
    if c.get('connector_private_ca') and not c.get('connector_relay_url'):
        raise Error('Connector trust requires a configured relay')
    return c


def state_identity(root):
    state = root / 'state'
    names = ('organization-id', 'admin-token', 'pki/ca.crt', 'pki/ca.key', 'pki/server.crt', 'pki/server.key')
    values = {name: hashlib.sha256(trust.regular(state / name, 128 << 10)).hexdigest() for name in names}
    organization = trust.regular(state / 'organization-id', 128).decode().strip()
    uuid.UUID(organization)
    return {'organization_id': organization, 'files': values}


def compose_spec(root, c, manifest, profile):
    state = root / 'state'
    organization = trust.regular(state / 'organization-id', 128).decode().strip()
    password = trust.regular(state / 'database-password', 128).decode().strip()
    runtime_password = trust.regular(state / 'database-runtime-password', 128).decode().strip()
    if not all(re.fullmatch(r'[a-f0-9]{64}', value) for value in (password, runtime_password)):
        raise Error('Database credential is missing or corrupt')
    dsn = f'postgres://hm_runtime:{runtime_password}@postgres:5432/haproxy_manager?sslmode=disable'
    runtime = {'image': manifest['image'], 'user': f"{c['uid']}:{c['gid']}",
               'read_only': True, 'cap_drop': ['ALL'],
               'security_opt': ['no-new-privileges:true'], 'restart': 'unless-stopped',
               'tmpfs': ['/tmp:size=16m,mode=1777'], 'mem_limit': '128m',
               'environment': {'HAPROXY_DATABASE_URL': dsn, 'GOMEMLIMIT': '80MiB', 'GOGC': '50',
                               'GOMAXPROCS': '1', 'HAPROXY_LOGS_URL': 'http://logs:9428'},
               'networks': ['data', 'control']}
    manager = json.loads(json.dumps(runtime))
    manager['environment'].update({
        'HAPROXY_ORGANIZATION_ID': organization, 'HAPROXY_LOCAL_ORIGIN': c['origin'],
        'HAPROXY_AGENT_IMAGE': manifest['image'],
        'HAPROXY_AGENT_MANAGER_URL': c['agent_manager_url'],
        'HAPROXY_AGENT_INGEST_URL': c['agent_ingest_url']})
    if manifest['image'].startswith('ghcr.io/'):
        manager['environment'].update({
            'HAPROXY_AGENT_RELEASE_VERSION': manifest['version'],
            'HAPROXY_AGENT_RELEASE_URL': f"https://github.com/{trust.REPOSITORY}/releases/download/{manifest['version']}"})
    manager['command'] = ['serve', '--role', 'manager', '--listen', ':8443', '--pki-dir', '/pki',
                          '--admin-token-file', '/run/secrets/operator', '--metrics-url', 'http://metrics:8428']
    manager['volumes'] = [f'{state}/pki:/pki:ro', f'{state}/admin-token:/run/secrets/operator:ro']
    if c.get('connector_relay_url'):
        manager['environment'].update({
            'HAPROXY_CONNECTOR_RELAY_URL': c['connector_relay_url'],
            'HAPROXY_CONNECTOR_STATE_DIR': '/connector'})
        manager['volumes'].append(f'{state}/connector:/connector')
        if c.get('connector_private_ca'):
            manager['environment']['HAPROXY_CONNECTOR_CA_FILE'] = '/run/secrets/connector-ca.crt'
            manager['volumes'].append(f'{state}/connector-ca.crt:/run/secrets/connector-ca.crt:ro')
    manager['ports'] = [f"{c['bind_address']}:{c['manager_port']}:8443"]
    if c.get('dev_http_port'):
        manager['command'] += ['--local-insecure-cookies', '--local-http-listen', ':8080']
        manager['ports'] += [f"127.0.0.1:{c['dev_http_port']}:8080"]
    manager['healthcheck'] = {'test': ['CMD', 'curl', '-fsS', '--cacert', '/pki/ca.crt',
                                      'https://localhost:8443/healthz'], 'interval': '2s',
                             'timeout': '3s', 'retries': 30}
    ingest = json.loads(json.dumps(runtime))
    ingest['command'] = ['serve', '--role', 'ingest', '--listen', ':8443', '--pki-dir', '/pki',
                         '--metrics-url', 'http://metrics:8428']
    # Ingestion has no CA signing key, operator token or local accounts interface.
    ingest['volumes'] = [f'{state}/pki/{name}:/pki/{name}:ro' for name in ('ca.crt', 'server.crt', 'server.key')]
    ingest['ports'] = [f"{c['bind_address']}:{c['ingest_port']}:8443"]
    tools = json.loads(json.dumps(runtime))
    tools.update(profiles=['tools'], restart='no', networks=['data'], command=['migrate'])
    tools['environment'].update({
        'HAPROXY_DATABASE_URL': f'postgres://hm_owner:{password}@postgres:5432/haproxy_manager?sslmode=disable',
        'HAPROXY_ORGANIZATION_ID': organization})
    return {
        'name': c['project'],
        'services': {
            'postgres': {'image': profile['images']['postgres'], 'restart': 'unless-stopped',
                         'environment': {'POSTGRES_USER': 'hm_owner', 'POSTGRES_DB': 'haproxy_manager',
                                         'POSTGRES_PASSWORD': password},
                         'command': ['postgres', '-c', 'shared_buffers=16MB', '-c', 'max_connections=30'],
                         'volumes': ['database:/var/lib/postgresql'], 'networks': ['data'], 'mem_limit': '160m',
                         'healthcheck': {'test': ['CMD', 'pg_isready', '-h', '127.0.0.1', '-U', 'hm_owner', '-d', 'haproxy_manager'],
                                         'interval': '2s', 'timeout': '3s', 'retries': 30}},
            'metrics': {'image': profile['images']['metrics'], 'restart': 'unless-stopped', 'mem_limit': '128m',
                        'command': ['-storageDataPath=/storage', '-retentionPeriod=7d',
                                    '-memory.allowedBytes=32MiB', '-search.maxConcurrentRequests=2',
                                    '-search.maxQueryDuration=5s', '-inmemoryDataFlushInterval=1s'],
                        'environment': {'GOMEMLIMIT': '80MiB', 'GOMAXPROCS': '1'},
                        'volumes': ['metrics:/storage'], 'networks': ['data']},
            'logs': {'image': profile['images']['logs'], 'restart': 'unless-stopped', 'mem_limit': '128m',
                     'command': ['-storageDataPath=/storage', '-retentionPeriod=7d',
                                 '-retention.maxDiskSpaceUsageBytes=1GiB', '-memory.allowedBytes=24MiB',
                                 '-search.maxConcurrentRequests=2', '-search.maxQueryDuration=3s',
                                 '-inmemoryDataFlushInterval=1s'],
                     'environment': {'GOMEMLIMIT': '80MiB', 'GOMAXPROCS': '1'},
                     'volumes': ['logs:/storage'], 'networks': ['data']},
            'manager': manager, 'ingest': ingest, 'tools': tools},
        'networks': {'data': {'internal': True}, 'control': {}},
        'volumes': {name: {'labels': {'ecraela.haproxy.installation': c['project']}}
                    for name in ('database', 'metrics', 'logs')}}


class Engine:
    def __init__(self, root, c):
        self.root, self.c = root, c
        self.file = root / 'runtime.json'

    def compose(self, *args, **kwargs):
        return command(['docker', 'compose', '--project-directory', self.root, '--env-file', '/dev/null',
                        '-p', self.c['project'], '-f', self.file, *args], **kwargs)

    def prepare(self, manifest, profile):
        info = local_docker()
        for image in [manifest['image'], *profile['images'].values()]:
            try:
                image_info = json.loads(command(['docker', 'image', 'inspect', image]))[0]
            except Error:
                if image.startswith('sha256:'):
                    raise Error('A required content-addressed local image is missing; preload it for recovery') from None
                command(['docker', 'pull', image])
                image_info = json.loads(command(['docker', 'image', 'inspect', image]))[0]
            arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(info['Architecture'], info['Architecture'])
            if image_info['Os'] != 'linux' or image_info['Architecture'] != arch:
                raise Error('Release image architecture differs from the Linux Docker Engine')
            command(['docker', 'tag', image_info['Id'],
                     'ecraela-haproxy-retained:' + image_info['Id'].removeprefix('sha256:')])
        # Verify the actual executable in the image, not only its label or tag.
        cid = command(['docker', 'create', '--network', 'none', '--entrypoint', '/bin/false',
                       manifest['image']]).decode().strip()
        try:
            with tempfile.TemporaryDirectory(prefix='.image-', dir=self.root) as tmp:
                binary = Path(tmp) / 'manager'
                command(['docker', 'cp', cid + ':/usr/local/bin/haproxy-manager', binary])
                if sha(binary) != manifest['artifacts'].get('haproxy-manager-linux-' + arch):
                    raise Error('Manager image executable differs from signed Linux artifact')
        finally:
            command(['docker', 'rm', cid])
        actual = json.loads(command(['docker', 'run', '--rm', '--network', 'none', '--read-only',
                                     '--cap-drop', 'ALL', '--entrypoint', '/usr/local/bin/haproxy-manager',
                                     manifest['image'], 'version']))
        if (actual.get('version') != manifest['version'] or actual.get('revision') != manifest['revision'] or
                actual.get('protocol') != 1 or
                actual.get('executable_sha256') != manifest['artifacts']['haproxy-manager-linux-' + arch]):
            raise Error('Manager executable build identity differs from the authenticated manifest')

    def render(self, manifest, profile):
        trust.private_json(self.file, compose_spec(self.root, self.c, manifest, profile))

    def initialize(self, manifest):
        state = self.root / 'state'
        u = origin(self.c['origin'], bool(self.c.get('dev_http_port')))
        args = ['docker', 'run', '--rm', '--network', 'none', '--user', f"{self.c['uid']}:{self.c['gid']}",
                '--read-only', '--cap-drop', 'ALL', '--volume', f'{state}:/state', manifest['image'],
                'init', '--state-dir', '/state', '--dns-name', 'localhost', '--dns-name', 'manager',
                '--dns-name', 'ingest', '--ip', '127.0.0.1']
        try:
            ipaddress.ip_address(u.hostname)
            args += ['--ip', u.hostname]
        except ValueError:
            if u.hostname not in ('localhost', 'manager', 'ingest'):
                args += ['--dns-name', u.hostname]
        command(args)

    def migrate(self, profile):
        self.compose('up', '-d', '--no-build', '--pull', 'never', '--wait', 'postgres')
        password = trust.regular(self.root / 'state/database-runtime-password', 128).decode().strip()
        if not re.fullmatch(r'[a-f0-9]{64}', password):
            raise Error('Runtime database credential is missing or corrupt')
        sql = ("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='hm_runtime') "
               f"THEN CREATE ROLE hm_runtime LOGIN PASSWORD '{password}' "
               "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION; END IF; END $$;\n")
        self.sql(sql)
        self.compose('run', '--rm', '--no-deps', 'tools', 'migrate')
        self.sql('REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT USAGE ON SCHEMA public TO hm_runtime; '
                 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO hm_runtime; '
                 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO hm_runtime; '
                 'REVOKE ALL ON hm_schema_migrations FROM hm_runtime; '
                 'GRANT SELECT ON hm_schema_migrations TO hm_runtime;')
        version = self.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
                               '-d', 'haproxy_manager', '-Atc',
                               'SELECT max(version) FROM hm_schema_migrations;').decode().strip()
        # Schema 20 only adds latest-observation storage. A schema-19 binary
        # can run against it during software rollback; never remove that table.
        if version != str(profile['database_schema']) and not (profile['database_schema'] == 19 and version == '20'):
            raise Error('The running database schema differs from this supported release profile')

    def sql(self, sql):
        with tempfile.TemporaryFile() as source:
            source.write(sql.encode())
            source.seek(0)
            return self.compose('exec', '-T', 'postgres', 'psql', '-v', 'ON_ERROR_STOP=1',
                                '-U', 'hm_owner', '-d', 'haproxy_manager', source=source)

    def switch(self):
        self.compose('up', '-d', '--no-build', '--pull', 'never', *SERVICES)

    def stop(self, services=WRITERS):
        self.compose('stop', '-t', '15', *services)

    def running(self):
        return set(self.compose('ps', '--status', 'running', '--services').decode().split())

    def ids(self):
        return self.compose('ps', '-aq').decode().split()

    def healthy(self, manifest, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if set(SERVICES) <= self.running():
                    for cid in self.ids():
                        v = json.loads(command(['docker', 'inspect', cid]))[0]
                        if v['State'].get('OOMKilled'):
                            raise Error('An installation container was killed for memory exhaustion')
                    # Verify database-backed API, both stores, UI assets and runtime build.
                    token = trust.regular(self.root / 'state/admin-token', 128).decode().strip()
                    org = state_identity(self.root)['organization_id']
                    config = ('url = "https://localhost:8443/v1/fleet"\n'
                              'cacert = "/pki/ca.crt"\n'
                              f'header = "Authorization: Bearer {token}"\n'
                              f'header = "X-Organization-ID: {org}"\nfail\nsilent\nshow-error\n')
                    with tempfile.TemporaryFile() as source:
                        source.write(config.encode())
                        source.seek(0)
                        fleet = json.loads(self.compose('exec', '-T', 'manager', 'curl', '--config', '-', source=source))
                    if not isinstance(fleet.get('nodes'), list):
                        raise Error('Manager fleet readiness response is invalid')
                    for url in ('http://metrics:8428/health', 'http://logs:9428/health',
                                'https://localhost:8443/'):
                        self.compose('exec', '-T', 'manager', 'curl', '-fsS', '--max-time', '5',
                                     '--cacert', '/pki/ca.crt', url)
                    actual = json.loads(self.compose('exec', '-T', 'manager',
                                                     '/usr/local/bin/haproxy-manager', 'version'))
                    if actual['version'] != manifest['version'] or actual['revision'] != manifest['revision']:
                        raise Error('Running manager version differs from the selected release')
                    return
            except (Error, ValueError, KeyError):
                pass
            time.sleep(1)
        raise Error('Manager, local console, database and telemetry stores did not become ready')


def install(args):
    manifest, profile, publisher = release(args.release, args.version, args.public_key)
    root = private_directory(args.directory, create=True)
    uid, gid = (10001, 10001) if os.geteuid() == 0 else (os.getuid(), os.getgid())
    u = origin(args.origin, bool(args.dev_http_port))
    c = {'schema': 1, 'project': 'ecraela-hm-' + secrets.token_hex(8), 'uid': uid, 'gid': gid,
         'origin': args.origin, 'bind_address': args.bind_address,
         'manager_port': args.manager_port, 'ingest_port': args.ingest_port,
         'dev_http_port': args.dev_http_port,
         'agent_manager_url': args.agent_manager_url or f'https://{u.hostname}:{args.manager_port}',
         'agent_ingest_url': args.agent_ingest_url or f'https://{u.hostname}:{args.ingest_port}'}
    trust.private_json(root / 'installation.json', c)
    configuration(root)
    if c['dev_http_port'] and c['bind_address'] != '127.0.0.1':
        raise Error('The development HTTP installation must publish only on loopback')
    for url in (c['agent_manager_url'], c['agent_ingest_url']):
        origin(url)
    password = trust.regular(args.password_file, 1025)
    if args.password_file.stat().st_mode & 0o077 or not 15 <= len(password.rstrip(b'\n')) <= 1024:
        raise Error('Initial administrator password must be a private file with 15–1024 bytes')
    if not re.fullmatch(r'[a-z0-9][a-z0-9@._+-]{2,127}', args.admin_username):
        raise Error('Use a local administrator username of 3–128 lowercase characters')
    with locked(root):
        engine = Engine(root, c)
        engine.prepare(manifest, profile)
        retain(root, manifest)
        state = owned_state(root, c)
        engine.initialize(manifest)
        write(state / 'database-password', secrets.token_hex(32) + '\n')
        write(state / 'database-runtime-password', secrets.token_hex(32) + '\n')
        write(state / 'bootstrap.password', password)
        if os.geteuid() == 0:
            for name in ('database-password', 'database-runtime-password', 'bootstrap.password'):
                os.chown(state / name, uid, gid)
        history = {'schema': 1, 'trust': publisher, 'current': None, 'previous': None,
                   'pending': {'action': 'install', 'release': manifest, 'profile': profile,
                               'username': args.admin_username}, 'identity': state_identity(root)}
        trust.private_json(root / 'deployment.json', history)
        engine.render(manifest, profile)
        complete_install(root, engine, history)


def configure_connector(args):
    root = private_directory(args.directory)
    origin(args.connector_relay_url)
    certificate = None
    if args.connector_ca_file:
        certificate = trust.regular(args.connector_ca_file, 1 << 20)
        if b'PRIVATE KEY' in certificate:
            raise Error('Connector trust must contain public CA certificates only')
        try:
            ssl.create_default_context(cadata=certificate.decode())
        except (UnicodeError, ssl.SSLError):
            raise Error('Invalid connector CA certificate bundle') from None
    with locked(root):
        c, h = configuration(root), load(root / 'deployment.json')
        if h.get('pending') or not h.get('current') or h.get('restored_stopped'):
            raise Error('Complete installation or recovery before configuring Converge')
        state = root / 'state'
        connector = state / 'connector'
        if (connector / 'connector.json').exists() and c.get('connector_relay_url') != args.connector_relay_url:
            raise Error('Unlink Converge before changing the configured relay')
        previous = dict(c)
        ca = state / 'connector-ca.crt'
        old_ca = trust.regular(ca, 1 << 20) if ca.exists() else None
        connector.mkdir(mode=0o700, exist_ok=True)
        if connector.is_symlink() or connector.stat().st_mode & 0o077:
            raise Error('Connector identity directory must be private')
        if os.geteuid() == 0:
            os.chown(connector, c['uid'], c['gid'])
        def save_ca(value):
            ca.unlink(missing_ok=True)
            if value is not None:
                write(ca, value)
                if os.geteuid() == 0:
                    os.chown(ca, c['uid'], c['gid'])
        c.update(connector_relay_url=args.connector_relay_url, connector_private_ca=certificate is not None)
        try:
            save_ca(certificate)
            trust.private_json(root / 'installation.json', c)
            engine = Engine(root, c)
            engine.render(h['current'], h['profile'])
            engine.switch()
            engine.healthy(h['current'], args.timeout)
        except BaseException:
            save_ca(old_ca)
            trust.private_json(root / 'installation.json', previous)
            engine = Engine(root, previous)
            engine.render(h['current'], h['profile'])
            engine.switch()
            engine.healthy(h['current'], args.timeout)
            raise
        print(json.dumps({'result': 'connector_configured', 'relay': args.connector_relay_url}))


def complete_install(root, engine, history):
    pending = history['pending']
    engine.migrate(pending['profile'])
    # A host-only crash checkpoint prevents recreating/resetting an account.
    # The retained password exists solely until a successful bootstrap/recovery.
    name = pending['username']
    sql = "SELECT count(*) FROM hm_local_users WHERE username = '" + name + "';"
    found = engine.compose('exec', '-T', 'postgres', 'psql', '-U', 'hm_owner',
                           '-d', 'haproxy_manager', '-Atc', sql).decode().strip()
    if found == '0':
        engine.compose('run', '--rm', '--no-deps', '--volume',
                       f'{root}/state/bootstrap.password:/run/secrets/bootstrap-password:ro',
                       'tools', 'user', 'add', '--username', name, '--role', 'administrator',
                       '--password-file', '/run/secrets/bootstrap-password')
    elif found != '1':
        raise Error('Unexpected bootstrap account state')
    engine.switch()
    engine.healthy(pending['release'])
    history.update(current=pending['release'], previous=None, profile=pending['profile'],
                   pending=None, last_result='installed', completed_at=int(time.time()))
    trust.private_json(root / 'deployment.json', history)
    (root / 'state/bootstrap.password').unlink(missing_ok=True)
    print(json.dumps({'result': 'installed', 'version': history['current']['version'],
                      'console': engine.c['origin'], 'organization_id': history['identity']['organization_id']}))


def compatible(old, candidate):
    if (old.get('database_schema') not in (19, 20) or candidate.get('database_schema') not in (19, 20) or
            {**old, 'database_schema': 20} != {**candidate, 'database_schema': 20}):
        raise Error('This release changes storage pins/schema; a separately qualified migration is required')


def change(args):
    manifest, profile, publisher = release(args.release, args.version, args.public_key)
    root = private_directory(args.directory)
    with locked(root):
        c, h = configuration(root), load(root / 'deployment.json')
        if h.get('pending') or not h.get('current') or h.get('restored_stopped'):
            raise Error('Recover or activate this installation before changing its release')
        if h['trust'] != publisher:
            raise Error('The release publisher trust root changed')
        if state_identity(root) != h['identity']:
            raise Error('Installation identity or key is missing/changed; restore it, never reinitialize')
        compatible(h['profile'], profile)
        if args.action == 'upgrade' and trust.version_tuple(manifest['version']) <= trust.version_tuple(h['current']['version']):
            raise Error('Upgrade requires a newer exact release')
        if args.action == 'rollback' and manifest != h.get('previous'):
            raise Error('Software rollback is limited to the retained previous signed release')
        engine = Engine(root, c)
        engine.prepare(manifest, profile)
        retain(root, manifest)
        h['pending'] = {'action': args.action, 'release': manifest, 'profile': profile}
        trust.private_json(root / 'deployment.json', h)
        try:
            engine.stop(('manager', 'ingest'))
            engine.render(manifest, profile)
            engine.migrate(profile)
            engine.switch()
            engine.healthy(manifest, args.timeout)
        except BaseException:
            try:
                engine.render(h['current'], h['profile'])
                engine.switch()
                engine.healthy(h['current'], args.timeout)
                h.update(pending=None, last_result='automatic_rollback')
                trust.private_json(root / 'deployment.json', h)
            except BaseException:
                raise Error('Upgrade/recovery needs attention; pending state and previous release are preserved') from None
            raise Error('Candidate failed readiness; the previous release is running again') from None
        h.update(previous=h['current'], current=manifest, profile=profile, pending=None,
                 last_result=args.action, completed_at=int(time.time()))
        trust.private_json(root / 'deployment.json', h)
        print(json.dumps({'result': args.action, 'version': manifest['version']}))


def recover(args):
    root = private_directory(args.directory)
    with locked(root):
        c, h = configuration(root), load(root / 'deployment.json')
        if not h.get('pending'):
            raise Error('No interrupted installation operation')
        if h['pending'].get('action') not in ('install', 'upgrade', 'rollback'):
            raise Error('An incomplete restore stays stopped; repeat restoration into a fresh directory')
        if state_identity(root) != h['identity']:
            raise Error('Installation identity changed; automatic recovery is refused')
        engine = Engine(root, c)
        if h['pending']['action'] == 'install':
            complete_install(root, engine, h)
        else:
            if not h.get('current'):
                raise Error('A retained release is required for recovery')
            engine.render(h['current'], h['profile'])
            engine.switch()
            engine.healthy(h['current'], args.timeout)
            h.update(pending=None, last_result='recovered')
            trust.private_json(root / 'deployment.json', h)
            print(json.dumps({'result': 'recovered', 'version': h['current']['version']}))


def safe_name(name):
    p = PurePosixPath(name)
    if not name or p.is_absolute() or '..' in p.parts or '\\' in name or '\0' in name:
        raise Error('Unsafe backup path')
    return p


def validate_archive(path):
    total, seen = 0, set()
    with tarfile.open(path, 'r:') as archive:
        for count, entry in enumerate(archive):
            name = str(safe_name(entry.name))
            total += entry.size
            if (name in seen or count >= MAX_ENTRIES or total > MAX_ARCHIVE or
                    not (entry.isfile() or entry.isdir()) or entry.size < 0 or entry.mode & 0o4000 or
                    (entry.mode & 0o2000 and not entry.isdir())):
                raise Error('Backup contains duplicate paths, links, special files or excessive data')
            seen.add(name)


def copy_tree(source, target):
    info = source.lstat()
    if stat.S_ISDIR(info.st_mode):
        target.mkdir(mode=0o700)
        for child in source.iterdir():
            if child.name not in ('.lock', 'bootstrap.password'):
                copy_tree(child, target / child.name)
    elif stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and not info.st_mode & 0o6000:
        with source.open('rb') as src, target.open('xb') as dest:
            shutil.copyfileobj(src, dest, 1 << 20)
        target.chmod(info.st_mode & 0o777)
    else:
        raise Error('Backup refuses links, sockets and special files')
    target.chmod(info.st_mode & (0o2777 if stat.S_ISDIR(info.st_mode) else 0o777))
    if os.geteuid() == 0:
        os.chown(target, info.st_uid, info.st_gid)


def pack_tree(root, destination):
    with tarfile.open(destination, 'w:') as archive:
        for path in sorted(root.rglob('*')):
            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                raise Error('Backup staging contains an unsafe file')
            archive.add(path, arcname=path.relative_to(root), recursive=False)
    destination.chmod(0o600)
    validate_archive(destination)


def extract(path, target):
    validate_archive(path)
    # Paths have been checked as a complete set before creating any content.
    with tarfile.open(path, 'r:') as archive:
        for entry in archive:
            out = target / safe_name(entry.name)
            if entry.isdir():
                out.mkdir(mode=0o700, parents=True, exist_ok=True)
            else:
                out.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with archive.extractfile(entry) as src:
                    write(out, src.read()) if entry.size < (1 << 20) else stream_file(src, out)
            os.chmod(out, entry.mode & 0o777)
            if os.geteuid() == 0:
                os.chown(out, entry.uid, entry.gid)


def stream_file(source, path):
    with path.open('xb') as dest:
        shutil.copyfileobj(source, dest, 1 << 20)
    path.chmod(0o600)


def proof_message(proof):
    return json.dumps({k: proof[k] for k in ('schema', 'kind', 'sha256', 'size')},
                      sort_keys=True, separators=(',', ':')).encode()


def sign_backup(path, key):
    if key.stat().st_mode & 0o077:
        raise Error('The backup signing key must be private')
    trust.regular(key, 16384)
    if b'ED25519' not in command(['openssl', 'pkey', '-in', key, '-text', '-noout']):
        raise Error('An independently held Ed25519 backup signing key is required')
    proof = {'schema': 1, 'kind': 'ecraela-haproxy-backup', 'sha256': sha(path), 'size': path.stat().st_size}
    with tempfile.TemporaryDirectory(prefix='hm-backup-sign-') as tmp:
        message, signature = Path(tmp) / 'message', Path(tmp) / 'signature'
        write(message, proof_message(proof))
        command(['openssl', 'pkeyutl', '-sign', '-inkey', key, '-rawin', '-in', message, '-out', signature])
        proof['signature'] = base64.b64encode(trust.regular(signature, 128)).decode()
    return proof


def verify_backup(path, public_key):
    proof = load(Path(str(path) + '.proof'))
    if (proof.get('schema') != 1 or proof.get('kind') != 'ecraela-haproxy-backup' or
            proof.get('size') != path.stat().st_size or proof['size'] > MAX_ARCHIVE or
            proof.get('sha256') != sha(path)):
        raise Error('Encrypted backup size/digest differs from its signed proof')
    with tempfile.TemporaryDirectory(prefix='hm-backup-verify-') as tmp:
        tmp = Path(tmp)
        write(tmp / 'key.pub', trust.regular(public_key, 16384))
        if b'ED25519' not in command(['openssl', 'pkey', '-pubin', '-in', tmp / 'key.pub', '-text_pub', '-noout']):
            raise Error('An independently provisioned Ed25519 backup verification key is required')
        signature = base64.b64decode(proof['signature'], validate=True)
        if len(signature) != 64:
            raise Error('Invalid backup signature')
        write(tmp / 'message', proof_message(proof))
        write(tmp / 'signature', signature)
        command(['openssl', 'pkeyutl', '-verify', '-pubin', '-inkey', tmp / 'key.pub',
                 '-rawin', '-in', tmp / 'message', '-sigfile', tmp / 'signature'])


def seal(staging, destination, recipient, parent, signing_key):
    if not re.fullmatch(r'age1[0-9a-z]{58}', recipient or ''):
        raise Error('An independently stored age X25519 recipient is required')
    proof_file = Path(str(destination) + '.proof')
    if Path(destination).exists() or proof_file.exists():
        raise Error('A backup is never overwritten')
    files = {str(p.relative_to(staging)): sha(p) for p in staging.rglob('*') if p.is_file()}
    meta = load(staging / 'backup.json')
    meta['files'] = {k: v for k, v in files.items() if k != 'backup.json'}
    trust.private_json(staging / 'backup.json', meta)
    archive = Path(parent) / 'payload.tar'
    pack_tree(staging, archive)
    fd, pending = tempfile.mkstemp(prefix='.hm-backup-', dir=Path(destination).parent)
    pending = Path(pending)
    try:
        with archive.open('rb') as source:
            with os.fdopen(fd, 'wb') as out:
                command(['age', '-r', recipient], source=source, destination=out, timeout=900)
                out.flush()
                os.fsync(out.fileno())
        # link is exclusive, unlike replace: concurrent destinations are refused.
        os.link(pending, destination)
        proof = sign_backup(Path(destination), signing_key)
        write(proof_file, json.dumps(proof, sort_keys=True) + '\n')
    finally:
        pending.unlink(missing_ok=True)


@contextmanager
def opened_backup(args):
    identity_file = args.identity_file
    if identity_file.stat().st_mode & 0o077:
        raise Error('The age decryption identity must be a private file')
    trust.regular(identity_file, 16384)
    with tempfile.TemporaryDirectory(prefix='hm-restore-') as temp:
        temp = Path(temp)
        ciphertext = temp / 'backup.age'
        fd = os.open(args.backup, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, 'rb') as source, ciphertext.open('xb') as snapshot:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_ARCHIVE:
                raise Error('Expected a regular encrypted backup within the 32 GiB bound')
            size = 0
            while block := source.read(1 << 20):
                size += len(block)
                if size > MAX_ARCHIVE:
                    raise Error('Backup exceeds the supported 32 GiB bound')
                snapshot.write(block)
        ciphertext.chmod(0o400)
        write(Path(str(ciphertext) + '.proof'), trust.regular(Path(str(args.backup) + '.proof')))
        verify_backup(ciphertext, args.backup_public_key)
        payload = temp / 'payload.tar'
        with payload.open('xb') as out:
            command(['age', '-d', '-i', identity_file, ciphertext], destination=out, timeout=900)
        # age must authenticate the entire input successfully before extraction.
        destination = temp / 'snapshot'
        destination.mkdir(mode=0o700)
        extract(payload, destination)
        meta = load(destination / 'backup.json')
        actual = {str(p.relative_to(destination)): sha(p) for p in destination.rglob('*')
                  if p.is_file() and p != destination / 'backup.json'}
        if meta.get('schema') != 1 or meta.get('complete') is not True or actual != meta.get('files'):
            raise Error('Backup is incomplete or its manifest does not match its files')
        yield destination, meta


def backup(args):
    root = private_directory(args.directory)
    if args.signing_key.resolve().is_relative_to(root):
        raise Error('Keep the independent backup signing key outside installation state')
    with locked(root), tempfile.TemporaryDirectory(prefix='.backup-', dir=root) as tmp:
        tmp = Path(tmp)
        c, h = configuration(root), load(root / 'deployment.json')
        if h.get('pending') or not h.get('current') or h.get('restored_stopped'):
            raise Error('Only a complete, active installation can be backed up')
        if state_identity(root) != h['identity']:
            raise Error('Incomplete installation identity; refusing a partial backup')
        engine = Engine(root, c)
        if not set(SERVICES) <= engine.running():
            raise Error('All installation services must be running before checkpointing')
        stage = tmp / 'snapshot'
        stage.mkdir(mode=0o700)
        (stage / 'volumes').mkdir(mode=0o700)
        meta = {'schema': 1, 'kind': 'installation', 'complete': False,
                'created_at': int(time.time()), 'source_project': c['project'], 'identity': h['identity']}
        try:
            engine.stop()
            for name in ('installation.json', 'deployment.json', 'state', 'releases'):
                copy_tree(root / name, stage / name)
            with (stage / 'database.dump').open('xb') as out:
                engine.compose('exec', '-T', 'postgres', 'sh', '-ec',
                               'export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc --no-owner --no-acl',
                               destination=out)
            for name in ('metrics', 'logs'):
                volume = c['project'] + '_' + name
                archive = stage / 'volumes' / (name + '.tar')
                with archive.open('xb') as out:
                    command(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL',
                             '--cap-add', 'DAC_READ_SEARCH', '--user', '0:0', '--entrypoint', 'tar',
                             '--volume', volume + ':/data:ro', h['current']['image'], '-C', '/data', '-cf', '-', '.'],
                            destination=out)
                validate_archive(archive)
            meta['complete'] = True
            trust.private_json(stage / 'backup.json', meta)
            seal(stage, args.backup, args.recipient, tmp, args.signing_key)
        finally:
            # Resume every writer even after disk full, failed dump or encryption.
            engine.compose('start', *WRITERS)
        engine.healthy(h['current'], args.timeout)
        print(json.dumps({'result': 'encrypted_backup', 'backup': str(args.backup),
                          'organization_id': h['identity']['organization_id']}))


def restore(args):
    local_docker()
    # Verify/decrypt all bytes before touching the new installation or Docker.
    if args.directory.exists():
        raise Error('Restore requires a new directory; in-place restore is refused')
    with opened_backup(args) as (stage, meta):
        if meta['kind'] != 'installation':
            raise Error('Use node-restore for a node backup')
        h = load(stage / 'deployment.json')
        folder = stage / 'releases' / h['current']['version']
        manifest, profile, publisher = release(folder, h['current']['version'], args.public_key)
        if publisher != h['trust'] or manifest != h['current'] or profile != h['profile']:
            raise Error('Backup release or publisher differs from the installed release')
        if state_identity(stage) != meta['identity'] or meta['identity'] != h['identity']:
            raise Error('Backup installation keys or tenant identity are incomplete')
        for name in ('metrics', 'logs'):
            validate_archive(stage / 'volumes' / (name + '.tar'))
        root = private_directory(args.directory, create=True)
        for name in ('installation.json', 'deployment.json', 'state', 'releases'):
            copy_tree(stage / name, root / name)
        c = load(root / 'installation.json')
        c.update(project='ecraela-hm-' + secrets.token_hex(8), restored_from=meta['source_project'])
        if args.origin:
            c['origin'] = args.origin
        if args.manager_port:
            c['manager_port'] = args.manager_port
        if args.ingest_port:
            c['ingest_port'] = args.ingest_port
        if args.dev_http_port is not None:
            c['dev_http_port'] = args.dev_http_port
        c['bind_address'] = '127.0.0.1'
        trust.private_json(root / 'installation.json', c)
        c = configuration(root)
        h.update(pending={'action': 'restore'}, restored_stopped=True)
        trust.private_json(root / 'deployment.json', h)
        engine = Engine(root, c)
        engine.prepare(manifest, profile)
        engine.render(manifest, profile)
        try:
            engine.compose('up', '-d', '--no-build', '--pull', 'never', '--wait', 'postgres')
            with (stage / 'database.dump').open('rb') as source:
                engine.compose('exec', '-T', 'postgres', 'sh', '-ec',
                               'export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-acl --exit-on-error',
                               source=source)
            for name in ('metrics', 'logs'):
                volume = c['project'] + '_' + name
                command(['docker', 'volume', 'create', '--label', 'ecraela.haproxy.installation=' + c['project'], volume])
                with (stage / 'volumes' / (name + '.tar')).open('rb') as source:
                    command(['docker', 'run', '--rm', '-i', '--network', 'none', '--read-only',
                             '--cap-drop', 'ALL', '--cap-add', 'CHOWN', '--cap-add', 'FOWNER',
                             '--cap-add', 'DAC_OVERRIDE', '--user', '0:0', '--entrypoint', 'tar',
                             '--volume', volume + ':/data', manifest['image'], '-C', '/data', '-xpf', '-'],
                            source=source)
            engine.migrate(profile)
        finally:
            engine.stop(SERVICES)
        h.update(pending=None, last_result='restored_stopped')
        trust.private_json(root / 'deployment.json', h)
        print(json.dumps({'result': 'restored_stopped', 'directory': str(root),
                          'version': manifest['version'], 'organization_id': h['identity']['organization_id']}))


def activate(args):
    root = private_directory(args.directory)
    with locked(root):
        c, h = configuration(root), load(root / 'deployment.json')
        if not args.source_fenced or not h.get('restored_stopped') or h.get('pending'):
            raise Error('A completed restore and --source-fenced are required')
        source = c.get('restored_from')
        live = command(['docker', 'ps', '-q', '--filter', 'label=com.docker.compose.project=' + source]).decode().split()
        for cid in live:
            info = json.loads(command(['docker', 'inspect', cid]))[0]
            if info['Config'].get('Labels', {}).get('com.docker.compose.service') in ('manager', 'ingest'):
                raise Error('The source manager/ingestion is still running; fence it first')
        if state_identity(root) != h['identity']:
            raise Error('Restored installation identity changed')
        engine = Engine(root, c)
        try:
            engine.switch()
            engine.healthy(h['current'], args.timeout)
        except BaseException:
            engine.stop(SERVICES)
            raise
        h.update(restored_stopped=False, last_result='activated_restore')
        trust.private_json(root / 'deployment.json', h)
        print(json.dumps({'result': 'activated_restore', 'console': c['origin']}))


def node_configuration(path):
    c = load(path)
    trust.identity(c['state_dir'])
    if 'agent' in c.get('paths', {}):
        raise Error('The reserved agent identity path cannot be replaced')
    paths = {'agent': Path(c['state_dir']), **{k: Path(v) for k, v in c.get('paths', {}).items()}}
    if not paths or len(paths) > 16 or not c.get('containers'):
        raise Error('Node backups need explicit local writer containers and persistent paths')
    for name, value in paths.items():
        if (not re.fullmatch(r'[a-z][a-z0-9_-]{0,31}', name) or not value.is_absolute() or
                value.resolve() != value or not value.is_dir() or value == Path('/')):
            raise Error('Node backup paths must be named, absolute ordinary directories')
    for a in paths.values():
        if any(a != b and (a in b.parents or b in a.parents) for b in paths.values()):
            raise Error('Node backup paths must not overlap')
    for container in c['containers']:
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}', container):
            raise Error('Invalid local node container')
        info = json.loads(command(['docker', 'inspect', container]))[0]
        if info['State']['Running']:
            raise Error('Stop all node configuration/identity writers before backing up their paths')
    return c, paths


def node_backup(args):
    local_docker()
    c, paths = node_configuration(args.node_config)
    if any(args.signing_key.resolve().is_relative_to(p) for p in paths.values()):
        raise Error('Keep the backup signing key outside node state')
    with tempfile.TemporaryDirectory(prefix='hm-node-backup-') as tmp:
        tmp = Path(tmp)
        stage = tmp / 'snapshot'
        stage.mkdir(mode=0o700)
        for name, path in paths.items():
            copy_tree(path, stage / name)
        expected = trust.identity(c['state_dir'])
        if trust.identity(stage / 'agent') != expected:
            raise Error('Node identity changed during checkpointing')
        trust.private_json(stage / 'backup.json', {'schema': 1, 'kind': 'node', 'complete': True,
                                                 'identity': expected, 'paths': sorted(paths),
                                                 'created_at': int(time.time())})
        seal(stage, args.backup, args.recipient, tmp, args.signing_key)
        print(json.dumps({'result': 'encrypted_node_backup', 'node_id': expected['node_id']}))


def node_restore(args):
    if args.directory.exists():
        raise Error('Node restore requires a fresh directory')
    with opened_backup(args) as (stage, meta):
        if meta['kind'] != 'node' or trust.identity(stage / 'agent') != meta['identity']:
            raise Error('Node identity is incomplete or differs from the backup')
        root = private_directory(args.directory, create=True)
        for name in meta['paths']:
            if not re.fullmatch(r'[a-z][a-z0-9_-]{0,31}', name):
                raise Error('Invalid node state path')
            copy_tree(stage / name, root / name)
        print(json.dumps({'result': 'node_restored_stopped', 'node_id': meta['identity']['node_id']}))


def main():
    global ERROR_LOG
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=('install', 'upgrade', 'rollback', 'recover', 'status', 'stop',
                                      'backup', 'restore', 'activate', 'node-backup', 'node-restore', 'verify',
                                      'connector-configure'))
    p.add_argument('--directory', type=Path, help='private installation/new restore directory')
    p.add_argument('--release', type=Path)
    p.add_argument('--version')
    p.add_argument('--public-key', type=Path, help='independently provisioned offline release publisher key')
    p.add_argument('--origin')
    p.add_argument('--bind-address', default='127.0.0.1')
    p.add_argument('--manager-port', type=int)
    p.add_argument('--ingest-port', type=int)
    p.add_argument('--dev-http-port', type=int, help='explicit localhost-only development console')
    p.add_argument('--agent-manager-url')
    p.add_argument('--agent-ingest-url')
    p.add_argument('--connector-relay-url', help='fixed HTTPS relay origin; pairing cannot change this destination')
    p.add_argument('--connector-ca-file', type=Path, help='optional public private-CA bundle copied into installation state')
    p.add_argument('--admin-username', default='admin')
    p.add_argument('--password-file', type=Path)
    p.add_argument('--backup', type=Path, help='new encrypted age backup or existing restore input')
    p.add_argument('--recipient', help='public age recipient; private key must live separately')
    p.add_argument('--signing-key', type=Path, help='independently held private Ed25519 backup signing key')
    p.add_argument('--backup-public-key', type=Path, help='independently provisioned backup signature verification key')
    p.add_argument('--identity-file', type=Path, help='private age decryption identity')
    p.add_argument('--node-config', type=Path)
    p.add_argument('--source-fenced', action='store_true', help='operator confirms the original installation is stopped')
    p.add_argument('--timeout', type=int, default=60)
    args = p.parse_args()
    if not 5 <= args.timeout <= 180:
        p.error('timeout must be 5–180 seconds')
    required = {
        'install': ('directory', 'release', 'version', 'origin', 'password_file'),
        'upgrade': ('directory', 'release', 'version'), 'rollback': ('directory', 'release', 'version'),
        'verify': ('release', 'version'), 'recover': ('directory',), 'status': ('directory',),
        'stop': ('directory',), 'activate': ('directory',),
        'backup': ('directory', 'backup', 'recipient', 'signing_key'),
        'restore': ('directory', 'backup', 'identity_file', 'backup_public_key'),
        'node-backup': ('node_config', 'backup', 'recipient', 'signing_key'),
        'node-restore': ('directory', 'backup', 'identity_file', 'backup_public_key'),
        'connector-configure': ('directory', 'connector_relay_url')}
    if any(getattr(args, key) is None for key in required[args.action]):
        p.error('required options: ' + ', '.join('--' + key.replace('_', '-') for key in required[args.action]))
    if args.directory:
        args.directory = args.directory.absolute()
        ERROR_LOG = args.directory / 'last-error.log'
    if args.action == 'verify':
        m, _, _ = release(args.release, args.version, args.public_key)
        print(json.dumps({'result': 'verified', 'version': m['version']}))
    elif args.action == 'install':
        args.manager_port = args.manager_port or 8443
        args.ingest_port = args.ingest_port or 9443
        install(args)
    elif args.action in ('upgrade', 'rollback'):
        change(args)
    elif args.action == 'recover':
        recover(args)
    elif args.action == 'connector-configure':
        configure_connector(args)
    elif args.action == 'backup':
        backup(args)
    elif args.action == 'restore':
        restore(args)
    elif args.action == 'activate':
        activate(args)
    elif args.action == 'node-backup':
        node_backup(args)
    elif args.action == 'node-restore':
        node_restore(args)
    else:
        root = private_directory(args.directory)
        with locked(root):
            c, h = configuration(root), load(root / 'deployment.json')
            if args.action == 'stop':
                Engine(root, c).stop(SERVICES)
                print(json.dumps({'result': 'stopped'}))
            else:
                print(json.dumps({'version': (h.get('current') or {}).get('version'),
                                  'previous_version': (h.get('previous') or {}).get('version'),
                                  'pending': bool(h.get('pending')), 'last_result': h.get('last_result'),
                                  'console': c['origin'], 'project': c['project']}))


if __name__ == '__main__':
    try:
        main()
    except (Error, OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        # Neither arguments nor child output is safe to echo around backups/keys.
        print('selfhostctl: ' + (str(error) if isinstance(error, Error) else
              'invalid private state, release or prerequisite; inspect local service logs'),
              file=sys.stderr)
        sys.exit(1)
