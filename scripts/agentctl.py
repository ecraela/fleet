#!/usr/bin/env -S python3 -I
"""Host-owned, signed, pinned agent installs. Never runs inside the agent.

Python 3.11+, Docker Compose v2 or Linux/systemd. Cosign verifies published
releases; an independently provisioned Ed25519 public key supports offline use.
No unsigned mode, shell evaluation, remote Docker context or state downgrade.
"""
import sys

if __name__ == '__main__' and not sys.flags.isolated:
    raise SystemExit('Run this installer with python3 -I to exclude unsigned Python inputs')

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import errno
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import re
import shutil
import stat
import subprocess
import tempfile
import time

VERSION = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[a-z0-9.-]+)?\Z')
DIGEST = re.compile(r'[a-f0-9]{64}\Z')
REPOSITORY = 'ecraela/fleet'
MAX_BINARY = 96 << 20
LEGAL_ARTIFACTS = ('LICENSE', 'THIRD_PARTY_NOTICES.txt', 'dependencies.json', 'source.tar.gz', 'source.json')
SYSTEMD_UNIT_DIRS = ('/etc/systemd/system', '/run/systemd/system', '/usr/lib/systemd/system', '/lib/systemd/system')


class InstallError(Exception):
    pass


def run(args, *, capture=False):
    result = subprocess.run([str(a) for a in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, check=False, timeout=180)
    if result.returncode:
        # Local configuration and process output can contain credentials.
        raise InstallError(f'{Path(str(args[0])).name} failed (exit {result.returncode}); inspect the local service logs')
    return result.stdout if capture else None


def regular(path, maximum=1 << 20):
    path = Path(path)
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as error:
        if error.errno in {errno.ELOOP, errno.EMLINK}:
            raise InstallError('Symlinked input files are refused') from None
        raise
    with os.fdopen(fd, 'rb') as f:
        s = os.fstat(f.fileno())
        if not stat.S_ISREG(s.st_mode) or s.st_size > maximum:
            raise InstallError('Expected a bounded regular file, not a symlink or device')
        raw = f.read(maximum + 1)
        if len(raw) > maximum:
            raise InstallError('File exceeds its size limit')
        return raw


def private_json(path, value):
    path = Path(path)
    fd, tmp = tempfile.mkstemp(prefix='.agentctl-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(value, f, sort_keys=True)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def verify_release(folder, expected, public_key=None):
    return _verify_release(folder, expected, public_key)[0]


def _verify_release(folder, expected, public_key=None):
    if not VERSION.fullmatch(expected):
        raise InstallError('An exact vMAJOR.MINOR.PATCH release is required')
    folder = Path(folder).resolve(strict=True)
    raw = regular(folder / 'release.json', 65536)
    # Authenticate exact bytes before trusting image names, paths or versions.
    # External verifiers must authenticate exactly the bytes we later parse,
    # even if an incoming artifact directory changes during verification.
    with tempfile.TemporaryDirectory(prefix='agent-release-verification-') as temp:
        snapshot = Path(temp)
        manifest = snapshot/'release.json'
        manifest.write_bytes(raw)
        if public_key:
            key = regular(public_key,16384)
            signature = regular(folder/'release.sig',128)
            if len(signature) != 64:
                raise InstallError('An Ed25519 release signature is required')
            (snapshot/'key.pub').write_bytes(key)
            (snapshot/'release.sig').write_bytes(signature)
            trust = hashlib.sha256(key).hexdigest()
            if b'ED25519' not in run(['openssl','pkey','-pubin','-in',snapshot/'key.pub','-text_pub','-noout'],capture=True):
                raise InstallError('An Ed25519 trust key is required')
            run(['openssl','pkeyutl','-verify','-pubin','-inkey',snapshot/'key.pub',
                 '-rawin','-in',manifest,'-sigfile',snapshot/'release.sig'])
        else:
            (snapshot/'release.sigstore.json').write_bytes(regular(folder/'release.sigstore.json',1<<20))
            trust = 'sigstore:' + REPOSITORY
            run(['cosign','verify-blob','--bundle',snapshot/'release.sigstore.json',
                 '--certificate-identity',f'https://github.com/{REPOSITORY}/.github/workflows/release.yml@refs/tags/{expected}',
                 '--certificate-oidc-issuer','https://token.actions.githubusercontent.com',manifest])
    m = json.loads(raw)
    if (m.get('schema') != 1 or m.get('repository') != REPOSITORY or
            m.get('version') != expected or m.get('protocol') != 1 or m.get('state_schema') != 1 or
            not re.fullmatch(r'[a-f0-9]{40}', m.get('revision', ''))):
        raise InstallError('Release version, repository, protocol or state format is incompatible')
    if not re.fullmatch(r'(?:ghcr\.io/ecraela/fleet@)?sha256:[a-f0-9]{64}', m.get('image', '')):
        raise InstallError('A signed registry digest is required; mutable tags are refused')
    if 'compatibility' in m:
        validate_compatibility(m['compatibility'], expected)
    if not isinstance(m.get('artifacts'), dict) or not 1 <= len(m['artifacts']) <= 12:
        raise InstallError('Invalid release artifact set')
    for name, digest in m['artifacts'].items():
        if not (re.fullmatch(r'(?:haproxy-(?:agent|manager)-linux-(?:amd64|arm64)|agentctl\.py|selfhostctl\.py|selfhost\.json)', name)
                or name in LEGAL_ARTIFACTS) or not isinstance(digest, str) or not DIGEST.fullmatch(digest):
            raise InstallError('Invalid release artifact name or checksum')
    return m,trust


def validate_compatibility(value, version):
    """Validate the signed, inclusive peer ranges used by managed upgrades."""
    if not isinstance(value, dict) or set(value) != {'manager', 'agent'}:
        raise InstallError('Invalid signed release compatibility')
    if not isinstance(version, str) or not VERSION.fullmatch(version):
        raise InstallError('Invalid signed release version')
    for role in ('manager', 'agent'):
        bounds = value[role]
        if not isinstance(bounds, dict) or set(bounds) != {'min', 'max'}:
            raise InstallError('Invalid signed release compatibility')
        low, high = bounds['min'], bounds['max']
        if (not isinstance(low, str) or not isinstance(high, str) or
                not VERSION.fullmatch(low) or not VERSION.fullmatch(high) or
                version_tuple(low) > version_tuple(high) or
                not version_tuple(low) <= version_tuple(version) <= version_tuple(high)):
            raise InstallError('Invalid signed release compatibility range')
    return value


def compatible_peer(manifest, peer_role, peer_version):
    """A missing legacy range is unknown, never unlimited compatibility."""
    if peer_role not in ('manager', 'agent'):
        raise InstallError('Unknown peer role')
    bounds = validate_compatibility(manifest.get('compatibility'), manifest.get('version'))[peer_role]
    if not isinstance(peer_version, str) or not VERSION.fullmatch(peer_version):
        raise InstallError('Peer release version is unknown')
    if not version_tuple(bounds['min']) <= version_tuple(peer_version) <= version_tuple(bounds['max']):
        raise InstallError('Peer release is outside the signed compatibility range')
    return True


def identity(state):
    value = json.loads(regular(Path(state) / 'identity.json', 128 << 10))
    node = value.get('node', {})
    if not node.get('id') or not value.get('key_pem') or not value.get('certificate_pem'):
        raise InstallError('Existing identity is incomplete; refusing to re-enroll or replace keys')
    return {'node_id': node['id'], 'key_sha256': hashlib.sha256(value['key_pem'].encode()).hexdigest()}


def health(state, release, since, existing=None):
    try:
        v = json.loads(regular(Path(state) / 'agent-health.json', 4096))
        observed = datetime.fromisoformat(v['observed_at'].replace('Z', '+00:00')).timestamp()
        info = v['build']
        expected_hash = release['artifacts'].get('haproxy-agent-' + info['platform'].replace('/', '-'))
        return (observed > since and -1 <= time.time() - observed < 6 and info['version'] == release['version'] and
                info['revision'] == release['revision'] and info['protocol'] == 1 and
                expected_hash is not None and expected_hash == info['executable_sha256'] and
                (not existing or v['node_id'] == existing['node_id']))
    except (OSError, ValueError, KeyError, TypeError):
        return False


def config(path):
    path = Path(path).resolve(strict=True)
    if path.stat().st_mode & 0o022:
        raise InstallError('Installer config must not be group/world writable')
    c = json.loads(regular(path, 65536))
    if c.get('mode') not in {'linux', 'docker'}:
        raise InstallError('Choose linux or docker installation')
    for key in ('state_dir', 'install_dir'):
        p = Path(c.get(key, ''))
        if not p.is_absolute() or p == Path('/') or '..' in p.parts:
            raise InstallError('State and installation directories must be explicit absolute paths')
        if p.exists() and (p.is_symlink() or not p.is_dir()):
            raise InstallError('Unsafe installation directory')
    state, install = Path(c['state_dir']).resolve(), Path(c['install_dir']).resolve()
    if state == install or state in install.parents or install in state.parents:
        raise InstallError('Identity and software installation directories must be separate')
    c['_config_path'] = str(path)
    return c


@contextmanager
def locked(c):
    root = Path(c['install_dir'])
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    if root.stat().st_mode & 0o067:
        raise InstallError('Installer directory must have mode 0700 or service-group traversal only (0710)')
    with open(root / '.lock', 'a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise InstallError('Another install or upgrade is running') from None
        yield root


class Docker:
    def __init__(self, c, root):
        self.c, self.root = c, root
        d = c['docker']
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}', d.get('service', '')):
            raise InstallError('Invalid Docker Compose agent service')
        if os.getenv('DOCKER_HOST') or os.getenv('DOCKER_CONTEXT'):
            raise InstallError('Use a local Unix Docker context without host overrides')
        context = json.loads(run(['docker', 'context', 'inspect'], capture=True))
        if not context[0]['Endpoints']['docker']['Host'].startswith('unix://'):
            raise InstallError('Remote Docker contexts are refused')
        self.args = ['docker', 'compose', '--project-directory', d['project_directory']]
        if d.get('env_file'):
            self.args += ['--env-file', d['env_file']]
        for file in d['files']:
            regular(file)
            self.args += ['-f', file]
        # Supplied Compose files remain the source of mounts, local policies,
        # UID, groups, limits, args and network. Only this service's image changes.
        self.override = root / 'image.json'
        services = json.loads(run(self.args + ['config', '--format', 'json'], capture=True))['services']
        entrypoint = services.get(d['service'], {}).get('entrypoint', [])
        if entrypoint != ['/usr/local/bin/haproxy-agent']:
            raise InstallError('Selected Compose service must explicitly run only haproxy-agent')

    def prepare(self, release, folder):
        image = release['image']
        if not image.startswith('sha256:'):
            run(['docker', 'pull', image])
        info = json.loads(run(['docker', 'image', 'inspect', image], capture=True))[0]
        # A local content-addressed retention tag keeps normal dangling-image
        # cleanup from removing the installed/rollback executable unexpectedly.
        run(['docker','tag',info['Id'],'ecraela-haproxy-agent-retained:'+info['Id'].removeprefix('sha256:')])
        arch = {'aarch64': 'arm64', 'x86_64': 'amd64', 'arm64': 'arm64'}.get(platform.machine())
        if info.get('Os') != 'linux' or info.get('Architecture') != arch:
            raise InstallError('Docker image architecture does not match this host')
        # Check the executable embedded in the authenticated image before
        # replacing any process. A signed manifest must bind both artifacts.
        container = run(['docker','create','--entrypoint','/bin/false',image], capture=True).decode().strip()
        try:
            with tempfile.TemporaryDirectory(prefix='verify-image-', dir=self.root) as tmp:
                binary = Path(tmp)/'haproxy-agent'
                run(['docker','cp',container+':/usr/local/bin/haproxy-agent',binary])
                digest = hashlib.sha256(regular(binary,MAX_BINARY)).hexdigest()
                if release['artifacts'].get('haproxy-agent-linux-'+arch) != digest:
                    raise InstallError('Image executable differs from the signed release artifact')
        finally:
            run(['docker','rm',container])

    def switch(self, release):
        private_json(self.override, {'services': {self.c['docker']['service']: {'image': release['image']}}})
        run(self.args + ['-f', self.override, 'up', '-d', '--no-deps', '--no-build', '--pull', 'never', '--force-recreate', self.c['docker']['service']])

    def stop(self):
        run(self.args + ['stop', '-t', '10', self.c['docker']['service']])


def unit(c):
    user = c.get('user', 'haproxy-agent')
    if not re.fullmatch(r'[a-z_][a-z0-9_-]{0,31}', user):
        raise InstallError('Invalid service user')
    args = c.get('arguments', [])
    if not args or len(args)>128 or any(not isinstance(a, str) or len(a)>4096 or any(ord(ch) < 32 for ch in a) for a in args):
        raise InstallError('Explicit agent arguments are required')
    if '--state-dir' not in args or args.index('--state-dir')+1>=len(args) or args[args.index('--state-dir') + 1] != c['state_dir']:
        raise InstallError('Agent --state-dir must match the preserved identity directory')
    executable = str(Path(c['install_dir']) / 'current/haproxy-agent')
    escaped = ' '.join(json.dumps(a).replace('%', '%%').replace('$','$$') for a in [executable] + args)
    # Writable directories are an explicit local policy. Include socket/config
    # directories only when opting into the corresponding HAProxy capabilities.
    writable = c.get('writable_paths', [])
    if any(not Path(p).is_absolute() or re.search(r'[\s%"\\]', p) for p in [c['state_dir']] + writable):
        raise InstallError('Writable paths must be absolute literal systemd paths')
    return f'''[Unit]
Description=Ecraela HAProxy agent
After=network-online.target haproxy.service
Wants=network-online.target
[Service]
Type=simple
User={user}
Group={user}
ExecStart={escaped}
Restart=on-failure
RestartSec=3
Environment=GOMEMLIMIT=64MiB GOMAXPROCS=1
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
ReadWritePaths={' '.join([c['state_dir']] + writable)}
MemoryMax=96M
[Install]
WantedBy=multi-user.target
'''


class Linux:
    def __init__(self, c, root):
        if platform.system() != 'Linux' or os.geteuid() != 0:
            raise InstallError('Linux/systemd installs require local root')
        self.c, self.root = c, root
        self.unit = unit(c)
        # Root software must remain executable by the unprivileged service user.
        self.user = pwd.getpwnam(c.get('user', 'haproxy-agent'))
        if self.user.pw_uid == 0 or self.user.pw_gid == 0:
            raise InstallError('The native agent must use an unprivileged service account')
        # One native service per host. A fresh profile must not replace or stop
        # a different installation when its initial activation fails.
        expected = (root / 'ecraela-haproxy-agent.service').resolve()
        for directory in SYSTEMD_UNIT_DIRS:
            existing = Path(directory) / expected.name
            if (existing.exists() or existing.is_symlink()) and existing.resolve() != expected:
                raise InstallError('An existing native agent service belongs to another installation; use its upgrade profile')
        # A root installer must not execute or activate binaries through a path
        # that an unprivileged account could replace after checksum validation.
        for parent in (root,*root.parents):
            s=parent.stat()
            if s.st_uid != 0 or s.st_mode & 0o022:
                raise InstallError('Native software directories and ancestors must be root-owned and non-writable by other accounts')
        # The enclosing installer directory remains private; grant traversal to
        # this account with a named group, never make identity state public.
        os.chown(root, 0, self.user.pw_gid)
        os.chmod(root, 0o710)

    def prepare(self, release, folder):
        arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
        name = f'haproxy-agent-linux-{arch}'
        raw = regular(folder / name, MAX_BINARY)
        if hashlib.sha256(raw).hexdigest() != release['artifacts'].get(name):
            raise InstallError('Agent executable checksum does not match signed release')
        dest = self.root / 'releases' / release['version']
        dest.mkdir(parents=True, mode=0o755, exist_ok=True)
        binary = dest / 'haproxy-agent'
        if binary.exists() and hashlib.sha256(regular(binary, MAX_BINARY)).hexdigest() != release['artifacts'][name]:
            raise InstallError('Existing pinned release changed; refusing overwrite')
        if not binary.exists():
            binary.write_bytes(raw)
            binary.chmod(0o755)
        for legal_name in LEGAL_ARTIFACTS:
            if legal_name not in release['artifacts']:
                continue  # Older signed releases predate legal packaging.
            body = regular(folder / legal_name, MAX_BINARY)
            if hashlib.sha256(body).hexdigest() != release['artifacts'][legal_name]:
                raise InstallError('Legal artifact checksum does not match signed release')
            legal_path = dest / legal_name
            if legal_path.is_symlink():
                raise InstallError('Symlinked legal artifacts are refused')
            if legal_path.exists() and regular(legal_path, MAX_BINARY) != body:
                raise InstallError('Existing pinned legal artifact changed; refusing overwrite')
            if not legal_path.exists():
                legal_path.write_bytes(body)
                legal_path.chmod(0o644)
        # Signed releases must tell the truth about the executable they carry.
        actual = json.loads(run([binary, 'version'], capture=True))
        if actual.get('version') != release['version'] or actual.get('revision') != release['revision']:
            raise InstallError('Executable build identity differs from signed manifest')

    def switch(self, release):
        current, tmp = self.root / 'current', self.root / '.current-new'
        tmp.unlink(missing_ok=True)
        tmp.symlink_to(Path('releases') / release['version'])
        os.replace(tmp, current)
        path = self.root / 'ecraela-haproxy-agent.service'
        path.write_text(self.unit)
        path.chmod(0o644)
        state = Path(self.c['state_dir'])
        if not state.exists():
            state.mkdir(parents=True, mode=0o700)
            os.chown(state, self.user.pw_uid, self.user.pw_gid)
        run(['systemctl', 'link', path])
        run(['systemctl', 'daemon-reload'])
        run(['systemctl', 'enable', 'ecraela-haproxy-agent.service'])
        run(['systemctl', 'restart', 'ecraela-haproxy-agent.service'])

    def stop(self):
        run(['systemctl', 'stop', 'ecraela-haproxy-agent.service'])


def wait_healthy(c, release, since, previous, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if health(c['state_dir'], release, since, previous):
            if previous and identity(c['state_dir']) != previous:
                raise InstallError('Node identity or key changed')
            return
        time.sleep(0.5)
    raise InstallError('New agent did not report fresh successful HAProxy metrics before the deadline')


def apply(c, folder, version, key=None, action='upgrade', timeout=45, adapter=None):
    release,trust = _verify_release(folder, version, key)
    with locked(c) as root:
        record = root / 'deployment.json'
        history = json.loads(regular(record)) if record.exists() else {'schema': 1, 'current': None, 'previous': None}
        if history.get('pending'):
            raise InstallError('An interrupted upgrade is pending. Run recover before any new change')
        old = history['current']
        if old and history.get('trust') != trust:
            raise InstallError('Release trust root changed; provision rotation separately')
        if action != 'install' and not old:
            raise InstallError('No managed release; install a signed baseline first')
        if action == 'install' and old:
            raise InstallError('Already installed; use upgrade or rollback')
        existing = identity(c['state_dir']) if (Path(c['state_dir']) / 'identity.json').exists() else None
        if not existing and action != 'install':
            raise InstallError('Missing identity; refusing to enroll during upgrade or rollback')
        if action == 'upgrade' and old and version_tuple(version) <= version_tuple(old['version']):
            raise InstallError('Upgrade requires a newer version; use rollback for the retained release')
        if action == 'rollback' and (not history['previous'] or release != history['previous']):
            raise InstallError('Rollback is limited to the retained previous signed release')
        engine = adapter or (Docker(c, root) if c['mode'] == 'docker' else Linux(c, root))
        engine.prepare(release, Path(folder))
        # A durable transaction marker precedes the first process change. The
        # old image/binary is kept locally. Rollback never rewinds identity,
        # renewed certificates, ACME account state or command journals.
        history['trust'] = trust
        history['pending'] = {'release': release, 'identity': existing, 'started_at': time.time()}
        private_json(record, history)
        since = time.time()
        try:
            engine.switch(release)
            wait_healthy(c, release, since, existing, timeout)
        except BaseException:
            if old:
                try:
                    since = time.time()
                    engine.switch(old)
                    wait_healthy(c, old, since, existing, timeout)
                    history['pending'] = None
                    history['last_result'] = 'automatic_rollback'
                    private_json(record, history)
                except BaseException:
                    # A persisted marker keeps a failed recovery reviewable.
                    raise InstallError('Upgrade and automatic rollback need attention; retained release and identity are preserved') from None
            else:
                engine.stop()
                history['pending'] = None
                history['last_result'] = 'install_failed'
                private_json(record, history)
            raise
        history.update(current=release, previous=old, pending=None, last_result=action,
                       identity=identity(c['state_dir']), completed_at=time.time())
        private_json(record, history)
        print(json.dumps({'version': version, 'result': action, 'node_id': history['identity']['node_id']}))


def version_tuple(version):
    if not VERSION.fullmatch(version):
        raise InstallError('Invalid version')
    base, _, pre = version[1:].partition('-')
    identifiers = tuple((not part.isdigit(), part if not part.isdigit() else int(part)) for part in pre.split('.'))
    return (*map(int, base.split('.')), not bool(pre), identifiers)


def recover(c, timeout):
    with locked(c) as root:
        record = root / 'deployment.json'
        history = json.loads(regular(record))
        pending = history.get('pending')
        if not pending:
            raise InstallError('No interrupted installation or upgrade to recover')
        existing = identity(c['state_dir'])
        if pending['identity'] and existing != pending['identity']:
            raise InstallError('Identity changed; refusing automatic recovery')
        target = history['current'] or pending['release']
        engine = Docker(c, root) if c['mode'] == 'docker' else Linux(c, root)
        since = time.time()
        engine.switch(target)
        wait_healthy(c, target, since, existing, timeout)
        history.update(current=target, identity=existing, pending=None, last_result='recovered')
        private_json(record, history)
        print(json.dumps({'result': 'recovered', 'version': history['current']['version']}))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=['install', 'upgrade', 'rollback', 'recover', 'status', 'verify'])
    p.add_argument('--config', type=Path)
    p.add_argument('--release', type=Path, help='downloaded release directory; never executes remote scripts')
    p.add_argument('--version', help='exact version, including v prefix')
    p.add_argument('--public-key', type=Path, help='independently provisioned Ed25519 public key for offline signatures')
    p.add_argument('--timeout', type=int, default=45)
    args = p.parse_args()
    if not 5 <= args.timeout <= 120:
        p.error('timeout must be 5–120 seconds')
    if args.action == 'verify':
        print(json.dumps(verify_release(args.release, args.version, args.public_key)))
        return
    c = config(args.config)
    if args.action == 'status':
        v = json.loads(regular(Path(c['install_dir']) / 'deployment.json'))
        print(json.dumps({'version': (v.get('current') or {}).get('version'),
                          'previous_version': (v.get('previous') or {}).get('version'),
                          'pending': bool(v.get('pending')), 'last_result': v.get('last_result')}))
    elif args.action == 'recover':
        recover(c, args.timeout)
    else:
        apply(c, args.release, args.version, args.public_key, args.action, args.timeout)


if __name__ == '__main__':
    try:
        main()
    except (InstallError, OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        # Validation errors never include subprocess command lines or secrets.
        print(f'agentctl: {error if isinstance(error, InstallError) else "invalid local configuration, release or runtime response"}', file=sys.stderr)
        sys.exit(1)
