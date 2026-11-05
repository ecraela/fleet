#!/usr/bin/env python3
"""Qualify signed Docker agent installs/upgrade/rollback on the two-node demo."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
import time
import uuid

import agentctl as ctl
from demo import wait_for
from hardening import Load, lab_state
from pair import ROOT, STATE, selected_state, compose
from recovery import command


def image_release(root, version, revision, existing=None, broken=False):
    folder = root / version
    folder.mkdir(mode=0o700)
    tag = 'ecraela-haproxy-release:' + version
    if not existing:
        if broken:
            (folder / 'bad-agent').write_text('#!/bin/sh\nexit 42\n')
            (folder / 'bad-agent').chmod(0o755)
            (folder / 'Dockerfile').write_text('FROM ecraela-haproxy-release:v0.1.0\nUSER root\nCOPY --chmod=755 bad-agent /usr/local/bin/haproxy-agent\nUSER 10001:10001\n')
            args = ['docker','build','-t',tag,str(folder)]
        else:
            args = ['docker','build','--build-arg','VERSION='+version,'--build-arg','REVISION='+revision,'-t',tag,str(ROOT)]
        command(args)
    image = command(['docker','image','inspect','--format','{{.Id}}',existing or tag]).decode().strip()
    platform = command(['docker','image','inspect','--format','{{.Os}}-{{.Architecture}}',image]).decode().strip()
    container = command(['docker','create',image]).decode().strip()
    binary = folder / ('haproxy-agent-'+platform)
    try:
        command(['docker','cp',container+':/usr/local/bin/haproxy-agent',str(binary)])
    finally:
        command(['docker','rm',container])
    manifest = {'schema':1,'repository':ctl.REPOSITORY,'version':version,'revision':revision,
                'protocol':1,'state_schema':1,'image':image,
                'artifacts':{binary.name:hashlib.sha256(binary.read_bytes()).hexdigest()}}
    (folder/'release.json').write_text(json.dumps(manifest,sort_keys=True)+'\n')
    ctl.run(['openssl','pkeyutl','-sign','-inkey',root/'fixture.key','-rawin','-in',folder/'release.json','-out',folder/'release.sig'])
    return folder,manifest


def run():
    reports = selected_state()/'agent-releases'
    reports.mkdir(mode=0o700,exist_ok=True)
    root = reports/uuid.uuid4().hex
    root.mkdir(mode=0o700)
    # Synthetic release signer, never an installation CA, node or TLS key.
    with tempfile.TemporaryDirectory(prefix='signed-fixtures-',dir=root) as temp:
        fixtures=Path(temp)
        ctl.run(['openssl','genpkey','-algorithm','ED25519','-out',fixtures/'fixture.key'])
        pub=root/'fixture.pub'
        ctl.run(['openssl','pkey','-in',fixtures/'fixture.key','-pubout','-out',pub])
        baseline,b=image_release(fixtures,'v0.1.0','a'*40)
        candidate,_=image_release(fixtures,'v0.1.1','b'*40)
        failed,_=image_release(fixtures,'v0.1.2','c'*40,broken=True)
        folder=selected_state()
        os.environ.update(HAPROXY_UID=str(os.getuid()),HAPROXY_GID=str(os.getgid()),HAPROXY_PAIR_STATE=str(folder))
        lab,traffic,original=lab_state()
        overlay=STATE/'pair/release-overrides.json'
        ctl.private_json(overlay,{'services':{s:{'image':b['image']} for s in ('manager','ingest')}})
        configs,identities={},{}
        for letter in ('a','b','entrypoint'):
            identities[letter]=ctl.identity(folder/f'agent-{letter}')
            files=[str(ROOT/'compose.yaml')]
            if os.getenv('HAPROXY_PAIR_STANDALONE')!='1': files.append(str(ROOT/'compose.console.yaml'))
            files.append(str(ROOT/'compose.pair.yaml'))
            if (folder/'acme/enabled').exists(): files.append(str(ROOT/'compose.acme.yaml'))
            files.append(str(overlay))
            configs[letter]={'mode':'docker','state_dir':str(folder/f'agent-{letter}'),
                'install_dir':str(root/letter),'docker':{'service':'agent-'+letter,
                'project_directory':str(ROOT),'env_file':str(STATE/'compose.env'),'files':files}}
            ctl.private_json(root/f'{letter}.json',configs[letter])
        results=[]
        with Load(traffic,4,40) as load:
            load.mark('versioned-manager-and-ingestion')
            compose('up','-d','--no-deps','--no-build','manager','ingest')
            wait_for('versioned manager',lambda:lab.api.request('/healthz',authorized=False))
            for letter in ('a','b','entrypoint'):
                load.mark('install-'+letter)
                ctl.apply(configs[letter],baseline,'v0.1.0',pub,'install')
                results.append({'node':letter,'result':'installed'})
            for letter in ('a','b'):
                load.mark('upgrade-'+letter)
                ctl.apply(configs[letter],candidate,'v0.1.1',pub)
                node=lab.nodes['haproxy-'+letter]['id']
                wait_for('versioned discovery '+letter,lambda:(d if (d:=lab.api.request(f'/v1/nodes/{node}/discovery'))['control']['discovery'].get('agent',{}).get('version')=='v0.1.1' else None))
                results.append({'node':letter,'result':'upgraded'})
            load.mark('failed-start-and-automatic-rollback')
            try:
                ctl.apply(configs['a'],failed,'v0.1.2',pub,timeout=5)
            except ctl.InstallError:
                record=json.loads((root/'a/deployment.json').read_text())
                if record['current']['version']!='v0.1.1' or record.get('pending') or record['last_result']!='automatic_rollback': raise
            else:
                raise RuntimeError('Broken agent unexpectedly passed readiness')
            results.append({'node':'a','result':'failed_start_automatically_rolled_back'})
            for letter in ('a','b'):
                load.mark('rollback-'+letter)
                ctl.apply(configs[letter],baseline,'v0.1.0',pub,'rollback')
                if ctl.identity(configs[letter]['state_dir'])!=identities[letter]:
                    raise RuntimeError('Node identity or private key changed')
                results.append({'node':letter,'result':'rolled_back_identity_preserved'})
            time.sleep(3)
        traffic_results=load.results()
        if any(r['failures'] for r in traffic_results.values()):
            raise RuntimeError('Serving traffic failed during agent qualification')
        # Retain signed manifests, pinned image references and the public key.
        # The private fixture signer is removed with the temporary directory.
        services=json.loads(overlay.read_text())['services']
        services.update({'agent-'+letter:{'image':b['image']} for letter in configs})
        ctl.private_json(overlay,{'services':services})
        for release in (baseline,candidate,failed):
            dest=root/release.name
            dest.mkdir(mode=0o700)
            for name in ('release.json','release.sig'): shutil.copyfile(release/name,dest/name)
        for name,node in lab.nodes.items():
            cfg=lab.api.request(f"/v1/nodes/{node['id']}/configuration")['control']['discovery']['configuration']
            if cfg['document']!=original['configurations'][name]['document']:
                raise RuntimeError('Agent upgrades changed the serving configuration')
        if lab.revisions()!=original['history']:
            raise RuntimeError('Agent upgrades changed application version history')
        report={'results':results,'traffic':traffic_results,'identities_and_keys_preserved':True,
                'configuration_preserved':True,'signing':'temporary offline Ed25519 fixture; private signer deleted',
                'final_version':'v0.1.0'}
        ctl.private_json(root/'latest.json',report)
        ctl.private_json(reports/'latest.json',dict(report,run_id=root.name))
        print(json.dumps(report,indent=2))


if __name__=='__main__':
    run()
