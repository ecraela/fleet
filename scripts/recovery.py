#!/usr/bin/env python3
"""Consistent local-demo checkpoints and restores into a fenced, disposable project.

Backups contain private keys. This is an operator-side drill, never an HTTP API.
There is deliberately no restore-over-the-source operation.
"""
import copy
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import ssl
import stat
import subprocess
import tarfile
import time
import uuid

from demo import write_private
from pair import ROOT, STATE, selected_state

SERVICES = ('postgres', 'victoriametrics', 'victorialogs', 'manager', 'ingest',
            'pair-backend', 'pair-backend-2', 'haproxy-a', 'haproxy-b',
            'pair-entrypoint', 'agent-a', 'agent-b', 'agent-entrypoint')
WRITERS = ('manager', 'ingest', 'agent-a', 'agent-b', 'agent-entrypoint',
           'victoriametrics', 'victorialogs', 'acme-dns')
HELPER_IMAGE = 'haproxy:3.4.7-alpine3.24'


def command(args, **kwargs):
    # Never echo subprocess arguments, config/environment, stderr or archive data:
    # these operations handle operator credentials and node private keys.
    kwargs.setdefault('stdout', subprocess.PIPE)
    kwargs.setdefault('timeout', 180)
    result = subprocess.run(args, stderr=subprocess.PIPE, **kwargs)
    if result.returncode:
        raise RuntimeError(f'Local recovery command failed: {args[0]} (exit {result.returncode})')
    return result.stdout


def source_command(*args):
    cmd, env = source_command_args(*args)
    return command(cmd, env=env)


def private_copy(source, destination):
    """Only ordinary files/directories; never follow links or copy live sockets."""
    info = source.lstat()
    if stat.S_ISDIR(info.st_mode):
        destination.mkdir(mode=0o700)
        for child in source.iterdir():
            private_copy(child, destination/child.name)
        shutil.copystat(source, destination)
    elif stat.S_ISREG(info.st_mode) and info.st_nlink == 1:
        shutil.copyfile(source, destination, follow_symlinks=False)
        shutil.copystat(source, destination, follow_symlinks=False)
    else:
        raise RuntimeError('Checkpoint refuses links and special files')


def digest(file):
    result = hashlib.sha256()
    with file.open('rb') as source:
        for block in iter(lambda: source.read(1024*1024), b''):
            result.update(block)
    return result.hexdigest()


def safe_relative(name):
    path = PurePosixPath(name)
    if not name or path.is_absolute() or '..' in path.parts or '\\' in name:
        raise RuntimeError('Unsafe checkpoint path')
    return path


def validate_archive(file):
    total = 0
    with tarfile.open(file, 'r:') as archive:
        for count, member in enumerate(archive):
            safe_relative(member.name)
            total += member.size
            # The demo deliberately uses setgid directories for shared DPA/socket
            # groups. Preserve them, while rejecting privileged executable files.
            if (not (member.isfile() or member.isdir()) or member.mode & 0o4000 or
                    (member.mode & 0o2000 and not member.isdir()) or
                    count >= 100_000 or total > 2*1024**3):
                raise RuntimeError('Unsupported checkpoint archive entry or size')


def verify(snapshot):
    if snapshot.is_symlink() or snapshot.stat().st_mode & 0o077:
        raise RuntimeError('Checkpoint must be a private directory (0700)')
    manifest = json.loads((snapshot/'manifest.json').read_text())
    if manifest.get('version') != 1 or manifest.get('complete') is not True:
        raise RuntimeError('Incomplete or unsupported checkpoint')
    actual = {}
    for file in snapshot.rglob('*'):
        if file.is_symlink() or not (file.is_file() or file.is_dir()):
            raise RuntimeError('Checkpoint contains a link or special file')
        if file.is_file() and file != snapshot/'manifest.json':
            actual[str(file.relative_to(snapshot))] = digest(file)
    if actual != manifest['files']:
        raise RuntimeError('Checkpoint integrity check failed')
    for name in manifest['archives']:
        validate_archive(snapshot/safe_relative(name))
    # Keep database/installation material and every agent identity in one recovery
    # point. Missing keys must fail before any restore container is created.
    required = [manifest['database'], manifest['operator_token'], manifest['trust']]
    required += [manifest['pki']+'/'+name for name in ('ca.key','server.crt','server.key')]
    for volume, archive in manifest['volumes'].items():
        if not re.fullmatch(r'[a-z][a-z0-9-]*', volume):
            raise RuntimeError('Unsupported recovery volume name')
        if archive and archive not in manifest['archives']:
            raise RuntimeError('Unverified recovery archive')
    for node in manifest['nodes'].values():
        state = snapshot/safe_relative(node['state'])
        identity = json.loads((state/'identity.json').read_text())
        if (identity['node']['id'] != node['id'] or not identity.get('key_pem') or
                not identity.get('certificate_pem') or not identity.get('ca_pem')):
            raise RuntimeError('Checkpoint node identity or private key is missing')
        required.append(str((state/'identity.json').relative_to(snapshot)))
    for name in required:
        if not (snapshot/safe_relative(name)).is_file():
            raise RuntimeError('Checkpoint is missing required recovery material')
    return manifest


def checkpoint(destination, baseline):
    """Pause writers, checkpoint all owned stores, resume even after a failed copy."""
    destination.mkdir(mode=0o700)  # Exclusive: never overwrite an earlier backup.
    config = json.loads(source_command('config', '--format', 'json'))
    if config['name'] != 'converge-haproxy':
        raise RuntimeError('Recovery only supports the isolated local demo project')
    selected = list(SERVICES)
    if (selected_state()/'acme/enabled').exists():
        selected += ['acme-dns', 'acme-ca']
    running = set(source_command('ps', '--status', 'running', '--services').decode().split())
    if not set(selected) <= running:
        raise RuntimeError('All demo services must be running before checkpointing')
    writers = [name for name in WRITERS if name in selected]
    saved = dict(version=1, complete=False, baseline=baseline, services={}, volumes={},
                 nodes={}, archives=[], image_tags={}, created_at_ms=int(time.time()*1000))
    images={}
    for name in selected:
        cid=source_command('ps','-q',name).decode().strip()
        images[name]=command(['docker','inspect','--format','{{.Image}}',cid]).decode().strip()
        try:
            command(['docker','image','inspect','--format','{{.Id}}',images[name]])
        except RuntimeError:
            raise RuntimeError('Required recovery image is unavailable for '+name) from None
    binds = {}
    (destination/'binds').mkdir(mode=0o700)
    (destination/'volumes').mkdir(mode=0o700)
    try:
        source_command('stop', '-t', '10', *writers)
        for name in selected:
            service = config['services'][name]
            spec = {key:copy.deepcopy(service[key]) for key in (
                'image', 'user', 'entrypoint', 'command', 'environment', 'cap_drop',
                'cap_add', 'group_add', 'security_opt', 'read_only', 'tmpfs',
                'mem_limit', 'healthcheck', 'platform') if key in service}
            spec['volumes'] = []
            saved['image_tags'][name]=spec['image']
            spec['image']=images[name]
            for mount in service.get('volumes', []):
                # Never bring a bootstrap credential into an identity restore.
                if mount['target'] == '/run/secrets/enrollment-token':
                    continue
                item = {key:mount[key] for key in ('type', 'source', 'target', 'read_only') if key in mount}
                if mount['type'] == 'bind':
                    source = mount['source']
                    if source not in binds:
                        relative = 'binds/' + str(len(binds))
                        private_copy(Path(source), destination/relative)
                        binds[source] = relative
                    item['source'] = binds[source]
                    if name == 'manager' and mount['target'] == '/pki':
                        saved['pki'] = item['source']
                        saved['trust'] = item['source']+'/ca.crt'
                    if name == 'manager' and mount['target'] == '/run/secrets/admin-token':
                        saved['installation_operator'] = item['source']
                    if name.startswith('agent-') and mount['target'] == '/var/lib/haproxy-agent':
                        identity = json.loads((destination/item['source']/'identity.json').read_text())
                        saved['nodes'][name] = dict(id=identity['node']['id'], state=item['source'])
                elif mount['type'] == 'volume':
                    volume = mount['source']
                    if volume not in saved['volumes']:
                        saved['volumes'][volume] = None
                        if volume != 'postgres' and not volume.startswith('pair-runtime-'):
                            archive = 'volumes/'+volume+'.tar'
                            source_volume = config['volumes'][volume]['name']
                            with (destination/archive).open('xb') as output:
                                command(['docker', 'run', '--rm', '--network', 'none', '--read-only',
                                         '--cap-drop', 'ALL', '--cap-add', 'DAC_READ_SEARCH', '--user', '0:0', '--entrypoint', 'tar',
                                         '-v', source_volume+':/data:ro', HELPER_IMAGE,
                                         '-C', '/data', '-cf', '-', '.'], stdout=output)
                            (destination/archive).chmod(0o600)
                            saved['volumes'][volume] = archive
                            saved['archives'].append(archive)
                else:
                    raise RuntimeError('Unsupported demo mount type')
                spec['volumes'].append(item)
            saved['services'][name] = spec
        # The selected tenant operator and cluster are not necessarily installation tenant.
        private_copy(selected_state()/'operator-token', destination/'operator-token')
        saved['operator_token'] = 'operator-token'
        saved['organization'] = selected_state().name
        saved['database'] = 'manager.dump'
        with (destination/saved['database']).open('xb') as output:
            # PG credentials stay in the existing container environment, not argv.
            command_source = source_command_args('exec', '-T', 'postgres', 'sh', '-ec',
                'export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc --no-owner --no-acl')
            command(command_source[0], env=command_source[1], stdout=output)
        (destination/saved['database']).chmod(0o600)
    finally:
        # Resume only services that were already running. Serving proxies never stop.
        source_command('start', *writers)
    saved['files'] = {str(p.relative_to(destination)):digest(p) for p in destination.rglob('*') if p.is_file()}
    saved['complete'] = True
    write_private(destination/'manifest.json', json.dumps(saved, indent=2)+'\n')
    verify(destination)
    print('PASS: consistent checkpoint validated; source writers resumed.',flush=True)
    return saved


def source_command_args(*args):
    """Use the same Compose selection for streamed pg_dump output."""
    env = dict(os.environ, HAPROXY_UID=str(os.getuid()), HAPROXY_GID=str(os.getgid()), HAPROXY_PAIR_STATE=str(selected_state()))
    cmd = ['docker', 'compose', '--project-directory', str(ROOT), '--env-file', str(STATE/'compose.env'), '-f', str(ROOT/'compose.yaml')]
    if os.getenv('HAPROXY_PAIR_STANDALONE', '1') != '1':
        cmd += ['-f', str(ROOT/'compose.console.yaml')]
    cmd += ['-f', str(ROOT/'compose.pair.yaml')]
    if (selected_state()/'acme/enabled').exists():
        cmd += ['-f', str(ROOT/'compose.acme.yaml')]
    if (STATE/'pair/release-overrides.json').exists():
        cmd += ['-f', str(STATE/'pair/release-overrides.json')]
    if (selected_state()/'geo/compose.json').exists():
        cmd += ['-f', str(selected_state()/'geo/compose.json')]
    if (selected_state()/'traffic/compose.json').exists():
        cmd += ['-f', str(selected_state()/'traffic/compose.json')]
    return cmd+list(args), env


class Restore:
    def __init__(self, snapshot, workspace, serving_trust=None, docker_context=None):
        self.snapshot, self.manifest = snapshot, verify(snapshot)
        self.engine=['docker']
        if docker_context:
            endpoint=command(['docker','context','inspect',docker_context,'--format','{{.Endpoints.docker.Host}}']).decode().strip()
            if not endpoint.startswith('unix://'):
                raise RuntimeError('Restore requires a local Docker Unix socket, never a remote context')
            self.engine+=['--context',docker_context]
            for image in {HELPER_IMAGE,*(s['image'] for s in self.manifest['services'].values())}:
                self.docker('image','inspect',image)
        self.workspace = workspace
        workspace.mkdir(mode=0o700)
        self.project = 'converge-haproxy-restore-'+uuid.uuid4().hex[:12]
        config = dict(services=copy.deepcopy(self.manifest['services']),
                      volumes={name:{} for name in self.manifest['volumes']},
                      networks={'default':{'internal':True}})
        private_copy(snapshot/'binds', workspace/'binds')
        for name, service in config['services'].items():
            env = service.get('environment', {})
            for key in list(env):
                if key.startswith('HAPROXY_LOGIN_') or key.startswith('HAPROXY_BROWSER_'):
                    del env[key]
            for mount in service['volumes']:
                if mount['type'] == 'bind':
                    mount['source'] = str(workspace/safe_relative(mount['source']))
        # The restore runs beside the source on small developer/CI machines.
        # Bound its caches instead of competing for the source's serving budget.
        vm = config['services'].get('victoriametrics')
        if vm:
            vm['command'] = [arg for arg in vm['command'] if not arg.startswith('-memory.allowed')]+['-memory.allowedBytes=32MiB']
            vm['environment']['GOMEMLIMIT'] = '128MiB'
            vm['mem_limit'] = '192m'
        pg = config['services'].get('postgres')
        if pg:
            pg['command'] = ['postgres','-c','shared_buffers=16MB','-c','max_connections=32']
            pg['mem_limit'] = '128m'
            # The image's temporary init server accepts Unix-socket probes
            # before POSTGRES_DB exists. Old checkpoints retain that probe;
            # require the final TCP server before importing any database data.
            pg['healthcheck'] = dict(test=['CMD-SHELL', 'pg_isready -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],
                                     interval='2s', timeout='3s', retries=30)
        if serving_trust is not None:
            ca_file = workspace/'probe-ca.pem'
            write_private(ca_file, ''.join(ssl.DER_cert_to_PEM_cert(cert) for cert in serving_trust.get_ca_certs(binary_form=True)))
            config['services']['manager']['volumes'].append(dict(type='bind',source=str(ca_file),target='/run/recovery-ca.pem',read_only=True))
        self.config = config
        self.file = workspace/'compose.json'
        # Disable Compose interpolation: restored credential values are literal.
        write_private(self.file, json.dumps(config).replace('$', '$$')+'\n')

    def docker(self, *args, **kwargs):
        return command([*self.engine,*args], **kwargs)

    def compose(self, *args, **kwargs):
        return self.docker('compose', '-p', self.project, '-f', str(self.file), *args, **kwargs)

    def start(self):
        write_private(self.workspace/'active', self.project+'\n')
        for name, archive in self.manifest['volumes'].items():
            volume = self.project+'_'+name
            self.docker('volume', 'create', '--label', 'com.docker.compose.project='+self.project,
                        '--label', 'com.docker.compose.volume='+name, volume)
            args = ['run', '-i', '--rm', '--network', 'none', '--read-only', '--user', '0:0',
                    '--cap-drop', 'ALL', '--cap-add', 'CHOWN', '--cap-add', 'FOWNER',
                    '--cap-add', 'DAC_OVERRIDE',
                    '-v', volume+':/data', '--entrypoint', '/bin/sh', HELPER_IMAGE, '-ec']
            if archive:
                with (self.snapshot/archive).open('rb') as source:
                    self.docker(*args,'tar -xpf - -C /data', stdin=source)
            elif name.startswith('pair-runtime-'):
                self.docker(*args,'chown 10001:10001 /data; chmod 2770 /data')
        self.compose('up', '-d', '--no-deps', '--wait', '--wait-timeout', '60', 'postgres')
        with (self.snapshot/self.manifest['database']).open('rb') as source:
            self.compose('exec', '-T', 'postgres', 'sh', '-ec',
                'export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_restore -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-acl --exit-on-error', stdin=source)
        stores = ['victoriametrics', 'victorialogs', 'pair-backend', 'pair-backend-2']
        stores += [n for n in ('acme-dns', 'acme-ca') if n in self.config['services']]
        self.compose('up', '-d', '--no-deps', *stores)
        self.compose('up', '-d', '--no-deps', '--wait', '--wait-timeout', '60', 'manager')
        self.compose('up', '-d', '--no-deps', 'ingest')
        self.compose('up', '-d', '--no-deps', '--wait', '--wait-timeout', '60', 'haproxy-a', 'haproxy-b', 'pair-entrypoint')
        print('PASS: checkpoint restored into fresh stores and serving volumes on an internal network.',flush=True)

    def close(self):
        # The randomly generated project is the only permissible cleanup target.
        if not re.fullmatch(r'converge-haproxy-restore-[0-9a-f]{12}', self.project):
            raise RuntimeError('Invalid restore cleanup target')
        self.compose('down', '--volumes', '--remove-orphans', '-t', '3')
        (self.workspace/'active').unlink(missing_ok=True)
