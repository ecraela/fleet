#!/usr/bin/env python3
"""Exercise signed desired-state manager upgrades on an isolated Docker host.

The host runner must have systemd, Docker, Python 3 and access to the same
absolute work path. This is a disposable qualification, not a customer upgrade.
"""
import argparse
from contextlib import nullcontext
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import uuid

import selfhost_smoke as smoke
import selfhostctl as ctl


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--colima-profile', help='use root in this isolated Colima VM')
    parser.add_argument('--reuse-images', action='store_true')
    parser.add_argument('--with-agents', action='store_true', help='include a managed agent canary and live HAProxy traffic')
    parser.add_argument('--keep', action='store_true')
    args = parser.parse_args()
    smoke.acceptance_host(ctl.local_docker())
    if not args.colima_profile and (platform.system() != 'Linux' or os.geteuid() != 0):
        parser.error('The local host runner requires Linux root and systemd')
    work = args.work.resolve()
    work.mkdir(mode=0o700)
    install = work / 'installation'
    nodes_file = work / 'nodes.json'
    report = {'passed': False}
    manager_port, ingest_port = smoke.port(), smoke.port()

    def host(*command):
        prefix = (['colima', 'ssh', '--profile', args.colima_profile, '--', 'sudo']
                  if args.colima_profile else [])
        result = subprocess.run([*prefix, *map(str, command)], check=False,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if result.returncode:
            raise ctl.Error(f'{command[0]} failed on isolated host ({result.returncode}): '
                            + (result.stderr or result.stdout)[-1200:])
        return result

    timers = ('ecraela-fleet-manager-updater.timer', 'ecraela-fleet-catalog-sync.timer')
    agent_timer = 'ecraela-haproxy-updater.timer'
    try:
        releases, publisher = smoke.fixture_releases(work, None, args.reuse_images)
        baseline, candidate = releases[:2]
        password = work / 'administrator.password'
        ctl.write(password, ctl.secrets.token_urlsafe(32) + '\n')
        smoke.host_command(baseline[0], 'install', '--directory', install,
                           '--release', baseline[0], '--version', baseline[1]['version'],
                           '--public-key', publisher, '--origin', f'https://localhost:{manager_port}',
                           '--manager-port', manager_port, '--ingest-port', ingest_port,
                           '--agent-manager-url', 'https://manager:8443',
                           '--agent-ingest-url', 'https://ingest:8443',
                           '--admin-username', 'install-admin',
                           '--password-file', password)
        api = smoke.API(install)
        api.login(password.read_text().strip())
        for folder, manifest in (baseline, candidate):
            smoke.host_command(folder, 'catalog-add', '--directory', install,
                               '--release', folder, '--version', manifest['version'],
                               '--public-key', publisher)
        if args.with_agents:
            cluster = api.request('/v1/clusters', {'name': 'Managed upgrade qualification'})['cluster']['id']
            for letter in ('a', 'b'):
                token = api.request(f'/v1/clusters/{cluster}/enrollment-tokens', {'ttl_seconds': 600})['token']
                ctl.write(work / ('enrollment-token-' + letter), token + '\n')
            authority = work / 'application-authority'
            authority.mkdir(mode=0o700)
            ctl.command(['openssl', 'req', '-new', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                         '-nodes', '-keyout', authority / 'ca.key', '-out', authority / 'ca.crt', '-days', '2',
                         '-subj', '/CN=Disposable managed upgrade CA', '-addext', 'basicConstraints=critical,CA:TRUE',
                         '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-addext', 'subjectKeyIdentifier=hash'])
            (authority / 'ca.key').chmod(0o600)
            ctl.write(authority / 'extensions', 'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n'
                      'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\n'
                      'authorityKeyIdentifier=keyid,issuer\n')
            ports = [smoke.port() for _ in range(4)]
            smoke.prepare_proxy_image()
            nodes = smoke.node_spec(work, install, baseline[1], authority, ports)
            ctl.trust.private_json(nodes_file, nodes)
            ctl.command(['docker', 'compose', '--project-directory', work, '--env-file', '/dev/null',
                         '-f', nodes_file, 'up', '-d', '--no-build', '--pull', 'never'])
            smoke.wait('two enrolled agents', api.fresh)
            node_id = ctl.trust.identity(work / 'node-a/agent')['node_id']
            original_identity = ctl.trust.identity(work / 'node-a/agent')
            agent_config = work / 'agent-a.json'
            ctl.trust.private_json(agent_config, {
                'mode': 'docker', 'state_dir': str(work / 'node-a/agent'),
                'install_dir': str(work / 'agent-software-a'),
                'docker': {'service': 'agent-a', 'project_directory': str(work),
                           'env_file': '/dev/null', 'files': [str(nodes_file)]}})
            smoke.host_command(baseline[0], 'install', '--config', agent_config,
                               '--release', baseline[0], '--version', baseline[1]['version'],
                               '--public-key', publisher, agent=True)
            smoke.wait('agent software baseline', api.fresh)
            host('python3', '-I', baseline[0] / 'agentctl.py', 'enable-updates',
                 '--config', agent_config, '--release', baseline[0],
                 '--version', baseline[1]['version'], '--public-key', publisher,
                 '--manager-url', f'https://localhost:{manager_port}')
            host('python3', '-I', baseline[0] / 'agentctl.py', 'stage-release',
                 '--config', agent_config, '--release', candidate[0],
                 '--version', candidate[1]['version'], '--public-key', publisher)
            host('systemctl', 'stop', agent_timer)
            host('systemctl', 'start', 'ecraela-haproxy-updater.service')
        else:
            ports = None
            authority = None
        host('python3', '-I', baseline[0] / 'selfhostctl.py', 'manager-enable-updates',
             '--directory', install, '--public-key', publisher)
        host('systemctl', 'stop', *timers)
        host('systemctl', 'start', 'ecraela-fleet-manager-updater.service')

        def expect(version, action, previous):
            plan = api.request('/v1/software/manager/plan?version=' + version + '&action=' + action)['plan']
            if not plan['eligible']:
                raise ctl.Error('Managed manager preflight refused: ' + plan.get('reason', 'unknown'))
            api.request('/v1/software/manager', {'request_id': str(uuid.uuid4()), 'action': action,
                                                 'version': version, 'expected_version': previous})
            host('systemctl', 'start', 'ecraela-fleet-manager-updater.service')
            current = ctl.load(install / 'deployment.json')['current']['version']
            updates = api.request('/v1/software/manager')['updates']
            if current != version or not updates or updates[0]['status'] != 'succeeded':
                raise ctl.Error('Managed host update did not report observed success')
            report[action] = {'version': current, 'status': updates[0]['status']}

        with smoke.traffic(ports, authority / 'ca.crt') if ports else nullcontext(None) as counts:
            if args.with_agents:
                def agent_change(version, action, previous):
                    smoke.wait('fresh agent cohort before change', api.fresh)
                    base = f'/v1/nodes/{node_id}/software'
                    plan = api.request(base + '/plan?version=' + version + '&action=' + action)['plan']
                    if not plan['eligible']:
                        raise ctl.Error('Managed agent preflight refused: ' + plan.get('reason', 'unknown'))
                    api.request(base, {'request_id': str(uuid.uuid4()), 'action': action,
                                       'version': version, 'expected_version': previous})
                    host('systemctl', 'start', 'ecraela-haproxy-updater.service')
                    smoke.wait('fresh discovery after managed agent change', api.fresh)
                    updates = api.request(base)['updates']
                    if not updates or updates[0]['status'] != 'succeeded' or \
                            ctl.trust.identity(work / 'node-a/agent') != original_identity:
                        raise ctl.Error('Managed agent change did not preserve identity and report success')
                    report['agent_' + action] = {'version': version, 'status': updates[0]['status']}

                agent_change(candidate[1]['version'], 'upgrade', baseline[1]['version'])
                agent_change(baseline[1]['version'], 'rollback', candidate[1]['version'])
            expect(candidate[1]['version'], 'upgrade', baseline[1]['version'])
            expect(baseline[1]['version'], 'rollback', candidate[1]['version'])
        if counts is not None:
            if counts['failures'] or counts['requests'] < 20:
                raise ctl.Error('HAProxy traffic failed during managed software changes')
            report['serving_traffic'] = counts
        report.update(passed=True, signed_catalog_entries=2, host_timer_enabled=True,
                      observed_baseline=baseline[1]['version'])
    finally:
        if install.exists():
            agent_units = ('ecraela-haproxy-updater.service', agent_timer)
            manager_units = ('ecraela-fleet-manager-updater.service', timers[0],
                             'ecraela-fleet-catalog-sync.service', timers[1])
            for unit in (*agent_units, *manager_units):
                unit_root = work / 'agent-software-a/updater' if unit in agent_units else install / 'updater'
                try:
                    host('systemctl', 'disable', '--now', unit)
                except ctl.Error:
                    pass
                link = '/etc/systemd/system/' + unit
                try:
                    if host('readlink', link).stdout.strip() == str(unit_root / unit):
                        host('rm', '--', link)
                except ctl.Error:
                    pass
            try:
                host('systemctl', 'daemon-reload')
            except ctl.Error:
                pass
            if nodes_file.exists():
                try:
                    ctl.command(['docker', 'compose', '--project-directory', work, '--env-file', '/dev/null',
                                 '-f', nodes_file, 'down', '--volumes', '--remove-orphans'])
                except ctl.Error:
                    pass
            try:
                ctl.Engine(install, ctl.configuration(install)).compose('down', '--volumes', '--remove-orphans')
            except (ctl.Error, OSError):
                pass
        ctl.trust.private_json(work / 'latest.json', report)
        if not args.keep:
            for path in work.iterdir():
                if path.name != 'latest.json':
                    shutil.rmtree(path) if path.is_dir() else path.unlink()
    print(json.dumps(report, indent=2), flush=True)


if __name__ == '__main__':
    main()
