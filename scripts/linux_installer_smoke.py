#!/usr/bin/env python3
"""Linux/systemd CI qualification using an existing synthetic node identity.

Only run on an ephemeral Linux CI runner as root, after agent_releases.py. It
temporarily maps demo-only names and ports and always restores the Docker agent.
"""
import json
import os
from pathlib import Path
import platform
import pwd
import shutil
import tempfile
import time
import subprocess

import agentctl as ctl
from demo import wait_for
from hardening import Load, lab_state
from pair import ROOT, STATE, selected_state, compose
from recovery import command, source_command


def run():
    if platform.system() != 'Linux' or os.geteuid() != 0 or os.getenv('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('This test is restricted to an ephemeral root Linux GitHub Actions runner')
    lab,traffic,original=lab_state()
    root=selected_state()/'agent-releases'
    report=json.loads((root/'latest.json').read_text())
    releases=root/report['run_id']
    user='haproxy-agent-ci'
    try:
        account=pwd.getpwnam(user)
    except KeyError:
        command(['useradd','--system','--user-group','--home-dir','/nonexistent','--shell','/usr/sbin/nologin',user])
        account=pwd.getpwnam(user)
    with tempfile.TemporaryDirectory(prefix='haproxy-linux-installer-') as temporary:
        work=Path(temporary)
        # Hosted runners intentionally make /opt writable for tool caches. Use
        # a genuinely root-owned ancestry; do not relax the installer check.
        install=Path('/etc/ecraela-haproxy-agent-ci/software')
        state=Path('/var/lib/ecraela-haproxy-agent-ci')
        public=Path('/etc/ecraela-haproxy-agent-ci')
        if any(p.exists() for p in (install,state,public)):
            raise RuntimeError('Refusing to overwrite existing CI installation paths')
        if Path('/etc/systemd/system/ecraela-haproxy-agent.service').exists():
            raise RuntimeError('Refusing to replace an existing host agent unit')
        hosts=Path('/etc/hosts')
        old_hosts=hosts.read_bytes()
        def address(service):
            cid=source_command('ps','-q',service).decode().strip()
            v=json.loads(command(['docker','inspect',cid]))[0]
            networks=v['NetworkSettings']['Networks']
            if len(networks)!=1: raise RuntimeError('Expected the isolated demo bridge network')
            return next(iter(networks.values()))['IPAddress']
        manager,ingest,metrics=address('manager'),address('ingest'),address('haproxy-a')
        docker_agent=source_command('ps','-q','agent-a').decode().strip()
        if not docker_agent: raise RuntimeError('Original Docker agent is missing')
        cfg={'mode':'linux','user':user,'install_dir':str(install),'state_dir':str(state),
            'arguments':['--server-url','https://manager:8443','--ingest-url','https://ingest:8443',
                '--server-ca',str(public/'ca.crt'),'--state-dir',str(state),'--name','literal %H $GOMEMLIMIT',
                '--metrics-url',f'http://{metrics}:8404/metrics','--interval','1s']}
        before=ctl.identity(selected_state()/'agent-a')
        touched=False
        try:
            # Linux host can reach the isolated Docker bridge directly. No
            # serving node, manager port or customer configuration is changed.
            hosts.write_bytes(old_hosts+f'\n{manager} manager\n{ingest} ingest\n'.encode())
            compose('stop','-t','10','agent-a')
            touched=True
            shutil.copytree(selected_state()/'agent-a',state)
            for p in [state]+list(state.rglob('*')):
                if not p.is_file() and not p.is_dir(): raise RuntimeError('Unexpected state file')
                os.chown(p,account.pw_uid,account.pw_gid)
            public.mkdir(mode=0o755)
            shutil.copyfile(STATE/'pki/ca.crt',public/'ca.crt')
            (public/'ca.crt').chmod(0o644)
            for version in ('v0.1.0','v0.1.1'):
                dest=work/version
                shutil.copytree(releases/version,dest)
                m=json.loads((dest/'release.json').read_text())
                cid=command(['docker','create',m['image']]).decode().strip()
                try:
                    arch=command(['docker','image','inspect','--format','{{.Architecture}}',m['image']]).decode().strip()
                    command(['docker','cp',cid+':/usr/local/bin/haproxy-agent',str(dest/f'haproxy-agent-linux-{arch}')])
                finally:
                    command(['docker','rm',cid])
            # Both apps keep serving while the node's observation agent changes
            # between genuine Linux systemd-managed executable versions.
            with Load(traffic,4,20) as load:
                ctl.apply(cfg,work/'v0.1.0','v0.1.0',releases/'fixture.pub','install')
                pid=command(['systemctl','show','--property=MainPID','--value','ecraela-haproxy-agent.service']).decode().strip()
                if b'literal %H $GOMEMLIMIT' not in (Path('/proc')/pid/'cmdline').read_bytes().split(b'\x00'):
                    raise RuntimeError('Native service expanded literal agent arguments')
                ctl.apply(cfg,work/'v0.1.1','v0.1.1',releases/'fixture.pub')
                ctl.apply(cfg,work/'v0.1.0','v0.1.0',releases/'fixture.pub','rollback')
                if ctl.identity(state)!=before: raise RuntimeError('Linux lifecycle changed node identity')
                command(['systemctl','restart','ecraela-haproxy-agent.service'])
                m=json.loads((work/'v0.1.0/release.json').read_text())
                ctl.wait_healthy(cfg,m,time.time(),before,45)
            result=load.results()
            if any(r['failures'] for r in result.values()): raise RuntimeError('Linux agent upgrade affected serving')
            print(json.dumps({'linux_systemd':'install, upgrade, rollback and restart passed',
                              'identity_and_key_preserved':True,'traffic':result}))
        finally:
            subprocess.run(['systemctl','disable','--now','ecraela-haproxy-agent.service'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=False)
            Path('/etc/systemd/system/ecraela-haproxy-agent.service').unlink(missing_ok=True)
            subprocess.run(['systemctl','daemon-reload'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=False)
            hosts.write_bytes(old_hosts)
            for p in (install,state,public): shutil.rmtree(p,ignore_errors=True)
            if touched:
                # Resume the exact stopped container, preserving its UID and
                # mounts without restarting Compose's one-shot dependencies.
                command(['docker','start',docker_agent])
                node=lab.nodes['haproxy-a']['id']
                wait_for('restored Docker agent',lambda:lab.api.request(f'/v1/nodes/{node}/configuration').get('fresh'))


if __name__=='__main__':
    run()
