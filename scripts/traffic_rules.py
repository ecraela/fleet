#!/usr/bin/env python3
"""Native application access policies on the disposable two-node HTTP/TLS lab."""
from concurrent.futures import ThreadPoolExecutor
import copy
import json
import os
from pathlib import Path
import ssl
import subprocess
from threading import Event
import time
import urllib.parse
import uuid

from agentctl import identity
from demo import wait_for, write_private
from geography import change_section, docker, install_profile, recent_requests
from lab import network
from listeners import Listeners, local_dpa
from pair import ROOT, compose, selected_state
from requests import TRAFFIC_LOG_FORMAT, maintenance

SOURCE=Path(__file__).resolve().parents[1]

class TrafficLab(Listeners):
    def __init__(self):
        super().__init__()
        self.path=f'/v1/clusters/{self.cluster}/applications'
        self.folder=selected_state()
        self.context=ssl.create_default_context(cafile=self.folder/'acme/public/trust.pem')
        self.state=self.folder/'traffic'
        self.state.mkdir(mode=0o700,exist_ok=True)
        self.fixture='haproxy-traffic-fixture-'+uuid.uuid4().hex[:12]

    def query(self,kind='requests',**filters):
        app=self.latest('Store demo')['application_id']
        return self.api.request(self.path+'/'+app+'/'+kind+'?'+urllib.parse.urlencode(dict(range='15m',**filters)))

    def ready(self,letter,mode,version=0):
        v=self.api.request('/v1/nodes/'+self.nodes['haproxy-'+letter]['id']+'/configuration')
        c=(v['control'].get('discovery') or {}).get('configuration') or {}
        p=c.get('traffic_rules') or {}
        return v if v['fresh'] and c.get('version',0)>=version and c.get('application_control_version')==6 and p.get('client_source')==mode and p.get('country_ready') and p.get('asn_ready') else None

    def setup(self,image):
        records={n:self.latest(n) for n in ('Store demo','Admin demo')}
        self.before=self.inventory()
        for name,r in records.items():
            if r and r['status']=='rolled_back':
                active=[a for a in self.before[0]['document'].get('applications',[]) if a['id']==r['application_id']]
                records[name]=next((v for v in self.revisions() if v['application_id']==r['application_id'] and v['status']=='succeeded' and active and v['spec']==active[0]),None)
        if any(not r or r['status']!='succeeded' or not r['spec'].get('request_telemetry') or r['spec'].get('traffic_policy') or r['spec']['hostname']!='apps.demo.test' for r in records.values()):
            raise RuntimeError('Use two healthy synthetic applications with telemetry and no custom traffic policies')
        self.original=copy.deepcopy(records['Store demo']['spec'])
        self.identities={letter:identity(self.folder/('agent-'+letter)) for letter in ('a','b','entrypoint')}
        write_private(self.state/'baseline.json',json.dumps(dict(spec=self.original,identities=self.identities,configuration=self.before),indent=2)+'\n')
        services={s:{'image':image} for s in ('manager','ingest','agent-a','agent-b')}
        geo=self.folder/'geo'
        if not (geo/'compose.json').exists(): raise RuntimeError('Run the local geography scenario first')
        self.source_files={letter:(geo/('policy-'+letter+'.json')).read_bytes() for letter in ('a','b')}
        for letter,raw in self.source_files.items():
            p=json.loads(raw)
            if p['mode']!='peer' or p.get('trusted_proxies'): raise RuntimeError('Preserve custom client-IP trust before running this synthetic scenario')
            maps=self.state/('maps-'+letter);maps.mkdir(mode=0o755,exist_ok=True)
            grant=dict(version=1,haproxy_log_socket='/run/haproxy/access.sock',client_ip_policy='/run/haproxy-geo/policy-'+letter+'.json',maps_directory='/run/haproxy/traffic-maps')
            write_private(self.state/('grant-'+letter+'.json'),json.dumps(grant)+'\n')
            services['agent-'+letter].update(environment={'HAPROXY_TRAFFIC_RULES_POLICY':'/run/haproxy-traffic/grant-'+letter+'.json'},volumes=[str(self.state)+':/run/haproxy-traffic:ro',str(maps)+':/run/haproxy/traffic-maps:rw'])
            current=docker('inspect','converge-haproxy-haproxy-'+letter+'-1','--format','{{.Image}}')
            services['haproxy-'+letter]={'image':current,'volumes':[str(maps)+':/run/haproxy/traffic-maps:ro']}
        write_private(self.state/'compose.json',json.dumps({'services':services},indent=2)+'\n')
        compose('up','-d','--no-build','--no-deps','manager','ingest')
        wait_for('traffic manager HTTPS',lambda:self.api.request('/healthz',authorized=False))
        for letter in ('a','b'):
            node=self.nodes['haproxy-'+letter]
            if docker('inspect','converge-haproxy-agent-'+letter+'-1','--format','{{.Image}}')==image and self.ready(letter,'peer'):
                print('PASS: local node '+letter+' already has the requested image and traffic grant',flush=True)
                continue
            maintenance(self,node,'withdraw')
            try:
                compose('up','-d','--no-build','--no-deps','--force-recreate','haproxy-'+letter,'agent-'+letter)
                def dpa_started():
                    try:return local_dpa('/services/haproxy/configuration/frontends/public?full_section=true',letter=letter)
                    except (subprocess.SubprocessError,RuntimeError):return None
                wait_for('restarted local Data Plane API',dpa_started)
                install_profile(self,letter)
                change_section(letter,'frontends/public',lambda f:f.update(log_format=TRAFFIC_LOG_FORMAT))
                _,headers=local_dpa('/services/haproxy/configuration/frontends/public?full_section=true',letter=letter)
                version=int(headers['configuration-version'])
                wait_for('traffic capability '+letter,lambda:self.ready(letter,'peer',version))
            finally: maintenance(self,node,'rejoin')
            print('PASS: local traffic capability, immutable maps and rejoined node '+letter,flush=True)
        docker('run','-d','--name',self.fixture,'--network',network(ROOT),'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--memory=24m','--volume',str(self.folder/'acme/public/trust.pem')+':/trust.pem:ro','--entrypoint','sleep',image,'7200')
        info=json.loads(docker('inspect',self.fixture,'--format','{{json .NetworkSettings.Networks}}'))
        self.peer=next(iter(info.values()))['IPAddress']

    def send(self,letter,scheme,*,headers=(),status=200,path='/shop/items',proxy=False):
        port=8443 if scheme=='https' else 8080
        args=['exec',self.fixture,'curl','--silent','--show-error','--max-time','5','--noproxy','*','--cacert','/trust.pem','--connect-to',f'apps.demo.test:{port}:haproxy-{letter}:{port}','--request','OPTIONS','-H','Host: apps.demo.test','-o','/dev/null','-w','%{http_code}']
        if proxy: args+=['--haproxy-protocol','--haproxy-clientip','81.2.69.160']
        for header in headers: args+=['-H','X-Forwarded-For: '+header]
        result=docker(*args,f'{scheme}://apps.demo.test:{port}'+path)
        if status is not None and result!=str(status): raise RuntimeError(f'Native {letter}/{scheme} policy returned {result}; expected {status}')
        return result

    def deploy(self,policy,outcome='succeeded',*,verification=None):
        current=self.latest('Store demo')
        spec=copy.deepcopy(self.original)
        if verification is not None: spec['verification']=dict(verification)
        if policy: spec['traffic_policy']=policy
        else: spec.pop('traffic_policy',None)
        if current['status']=='rolled_back' and all(next((a for a in c['document'].get('applications',[]) if a['id']==spec['id']),None)==spec for c in self.inventory()):
            # No write is needed when compensation already restored exactly
            # this specification. Keep the failed attempt in version history.
            return next(v for v in self.revisions() if v['application_id']==current['application_id'] and v['status']=='succeeded' and v['spec']==spec)
        revision=self.start(spec,current)
        self.complete(revision,outcome)
        return revision

    def reject_handshake(self,letter,scheme,**arguments):
        try:response=self.send(letter,scheme,status=None,**arguments)
        except subprocess.CalledProcessError as error:
            if error.stdout.strip()!='000':raise RuntimeError('Rejected envelope produced an HTTP response') from None
        else:
            # An untrusted v1 prefix can be rejected by the HTTP parser with
            # 400. A policy's 403 would mean it reached application rules.
            if response!='400':raise RuntimeError('Invalid PROXY handshake reached the application')

    def source(self,mode,peers=()):
        if any(a.get('traffic_policy') for c in self.inventory() for a in c['document'].get('applications',[])):
            raise RuntimeError('Remove active policies before changing the host-owned client trust')
        for letter in ('a','b'):
            self.source_one(letter,mode,peers)

    def source_one(self,letter,mode,peers=()):
        p=json.loads(self.source_files[letter]);p['mode']=mode
        if peers: p['trusted_proxies']=list(peers)
        else: p.pop('trusted_proxies',None)
        write_private(self.folder/'geo'/('policy-'+letter+'.json'),json.dumps(p)+'\n')
        compose('restart','agent-'+letter)
        wait_for('new host client policy '+letter,lambda:self.ready(letter,mode))

    def management_outage(self):
        services=('manager','ingest','agent-a','agent-b')
        try:
            compose('stop','-t','5',*services)
            for _ in range(8):
                for letter in ('a','b'):
                    for scheme in ('http','https'):
                        self.send(letter,scheme,status=403)
                        self.send(letter,scheme,path='/shop/admin/v2/users')
        finally:
            compose('start',*services)
            wait_for('manager after offline enforcement',lambda:self.api.request('/healthz',authorized=False))
            for letter in ('a','b'):wait_for('restored agent '+letter,lambda:self.ready(letter,'peer'))
        print('PASS: native decisions and app B keep serving with manager, ingest and both agents stopped.',flush=True)

    def proxy_protocol(self,image):
        entry=json.loads(docker('inspect','converge-haproxy-pair-entrypoint-1','--format','{{json .NetworkSettings.Networks}}'))
        ingress=next(iter(entry.values()))['IPAddress']
        original={}
        for letter in ('a','b'):
            f,_=local_dpa('/services/haproxy/configuration/frontends/public?full_section=true',letter=letter)
            if any(r.get('action') in ('expect-proxy','set-src','set-src-port') for r in f.get('tcp_request_rule_list',[])):
                raise RuntimeError('Preserve custom receiver trust before testing PROXY')
            original[letter]=f['tcp_request_rule_list']
        b,_=local_dpa('/services/haproxy/configuration/backends/haproxy_nodes?full_section=true')
        if any(s.get('send-proxy')=='enabled' or s.get('send-proxy-v2')=='enabled' for s in b['servers'].values()):
            raise RuntimeError('Preserve custom sender trust before testing PROXY')
        changed=[]
        untrusted=self.fixture+'-untrusted'
        docker('run','-d','--name',untrusted,'--network',network(ROOT),'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--memory=24m','--volume',str(self.folder/'acme/public/trust.pem')+':/trust.pem:ro','--entrypoint','sleep',image,'1800')
        def sender(letter,withdrawn,enabled):
            def mutate(b):
                s=b['servers']['haproxy-'+letter]
                s['maintenance']='enabled' if withdrawn else 'disabled'
                if enabled:s['send-proxy-v2']='enabled'
                else:s.pop('send-proxy-v2',None)
            if not withdrawn:
                stable=[None]
                def healthy():
                    v=self.api.request('/v1/nodes/'+self.nodes['haproxy-'+letter]['id']+'/configuration')
                    d=v['control'].get('discovery') or {};servers=d.get('servers',[])
                    if not v['fresh'] or not servers or any(s['health']!='UP' for s in servers):stable[0]=None;return False
                    stable[0]=stable[0] or time.monotonic()
                    return time.monotonic()-stable[0]>=5
                wait_for('healthy host before PROXY rejoin',healthy)
            change_section('entrypoint','backends/haproxy_nodes',mutate)
            if withdrawn:
                stable=[None]
                def drained():
                    v=self.api.request('/v1/nodes/'+self.nodes['haproxy-'+letter]['id']+'/configuration')
                    d=v['control'].get('discovery') or {}
                    f=next((p for p in d.get('proxies',[]) if p['name']=='public' and p['kind']=='frontend'),{})
                    if not v['fresh'] or time.time()*1000-d.get('observed_at_ms',0)>5000 or f.get('active_connections')!=0:stable[0]=None;return False
                    stable[0]=stable[0] or time.monotonic()
                    return time.monotonic()-stable[0]>=3
                wait_for('host PROXY receiver drained',drained)
        try:
            for letter in ('a','b'):
                sender(letter,True,False);changed.append(letter)
                def receiver(f):
                    # Capture the peer before accepting the envelope and the
                    # accepted client afterwards at tcp-request session.
                    peer=[r for r in original[letter] if r.get('var_name')=='cv_peer']
                    rest=[r for r in original[letter] if r.get('var_name')!='cv_peer']
                    f['tcp_request_rule_list']=peer+[dict(type='connection',action='expect-proxy',cond='if',cond_test='{ var(sess.cv_peer) -m ip '+ingress+'/32 '+self.peer+'/32 }')]+rest
                change_section(letter,'frontends/public',receiver)
                self.source_one(letter,'proxy_protocol',[ingress+'/32',self.peer+'/32'])
                sender(letter,False,True)
            countries=rule('Country fixture',countries=['GB','JP'])
            self.deploy(policy([countries],mode='enforce'))
            for letter in ('a','b'):
                for scheme in ('http','https'):
                    self.send(letter,scheme,proxy=True,status=403)
                    # A PROXY envelope plus an XFF claim remains unresolved.
                    self.send(letter,scheme,proxy=True,headers=['1.0.0.1'])
                    self.reject_handshake(letter,scheme)
                    saved=self.fixture;self.fixture=untrusted
                    try:
                        self.send(letter,scheme,headers=['81.2.69.160'])
                        self.reject_handshake(letter,scheme,proxy=True)
                    finally:self.fixture=saved
            # A connection through the real entrypoint carries PROXY v2.
            self.deploy(policy([rule('Local v2 source',cidrs=['0.0.0.0/0','::/0'])],mode='observe'))
            since=int(time.time()*1000)
            for _ in range(12):self.request('/shop/items','app-a');self.https('/shop/items',backend='app-a')
            wait_for('PROXY v2 decisions from both nodes and protocols',lambda: len({(r['node_id'],r['scheme']) for r in recent_requests(self.query,since,traffic_decision='would_block',client_source='proxy_protocol')})==4)
            self.deploy(None)
            print('PASS: native PROXY v1/v2, HTTP/HTTPS, mixed-header rejection and untrusted-envelope resistance.',flush=True)
        finally:
            self.deploy(None)
            for letter in reversed(changed):
                sender(letter,True,True)
                change_section(letter,'frontends/public',lambda f:f.update(tcp_request_rule_list=original[letter]))
                self.source_one(letter,'peer')
                sender(letter,False,False)
            docker('rm','-f',untrusted)

def rule(name,**match):
    return dict(id=str(uuid.uuid4()),name=name,action='block',**match)

def policy(rules,mode='observe',unknown='allow'):
    return dict(version=1,mode=mode,unknown_action=unknown,rules=rules)

def extended(lab,image,evidence):
    lab.source('xff',[lab.peer+'/32'])
    asn=rule('ASN fixture',asns=[15169],path_prefix='/shop/items')
    lab.deploy(policy([asn],mode='enforce'))
    for letter in ('a','b'):
        for scheme in ('http','https'):
            lab.send(letter,scheme,headers=['1.0.0.1'],status=403)
            lab.send(letter,scheme,headers=[', '.join(['1.0.0.1']+[lab.peer]*15)],status=403)
            lab.send(letter,scheme,headers=[', '.join([lab.peer]*16)])
            lab.send(letter,scheme,headers=['1.0.0.1'],path='/shop/other')
            lab.send(letter,scheme,headers=['81.2.69.160'])
    country=rule('Country fixture',countries=['GB','JP'])
    exception=dict(rule('Resolved clients',cidrs=['0.0.0.0/0','::/0']),action='allow')
    active=lab.deploy(policy([country,exception],mode='enforce',unknown='block'))
    since=int(time.time()*1000)
    for letter in ('a','b'):
        for scheme in ('http','https'):
            lab.send(letter,scheme,headers=['81.2.69.160'],status=403)
            lab.send(letter,scheme,headers=['1.0.0.1'])
            lab.send(letter,scheme,headers=['::ffff:1.0.0.1'])
            for headers in [[],['unknown'],['1.0.0.1:443'],['001.0.0.1'],['2130706433'],['81.2.69.160abc'],['::ffff:1.0.0.1:443'],['0.0.0.0'],['::'],['224.0.0.1'],['ff00::1'],['81.2.69.160','1.0.0.1']]:
                lab.send(letter,scheme,headers=headers,status=403)
    wait_for('recorded unknown blocks',lambda:recent_requests(lab.query,since,traffic_rule='unknown',traffic_decision='block'))
    counts=wait_for('rule match counts',lambda:(v if (v:=lab.api.request(lab.path+'/'+active['application_id']+'/versions/'+active['id']+'/traffic-rules?range=15m'))['matches'] else None))
    evidence['counts']={k:counts[k] for k in ['collection_complete','expected_nodes','reporting_nodes','matches']}
    if counts['expected_nodes']!=2 or not any(r['rule_id']=='unknown' and r['decision']=='block' for r in counts['matches']):
        raise RuntimeError('Unknown decision counts were not attributed to this exact policy')
    # This policy blocks direct application probes too. Request verification
    # explicitly, including when the source app has not enabled those checks.
    lab.deploy(policy([country],mode='enforce',unknown='block'),outcome='rolled_back',
               verification={'http':True,'https':True})
    lab.send('a','http',headers=['1.0.0.1'])
    lab.deploy(None)
    lab.source('peer')
    lab.deploy(policy([rule('Fixture IP',cidrs=[lab.peer+'/32'])],mode='enforce'))
    lab.management_outage()
    lab.deploy(None)
    lab.proxy_protocol(image)
    rows=recent_requests(lab.query,since,method='OPTIONS')
    if any(s in json.dumps(rows) for s in ['81.2.69.160','1.0.0.1','2001:218::',lab.peer,'forwarded_for','client_ip']):
        raise RuntimeError('Client-address input escaped into stored rule evidence')
    if [c['document'] for c in lab.inventory()]!=[c['document'] for c in lab.before]:
        raise RuntimeError('Traffic scenario changed original routing, listeners or TLS')
    evidence.update(asn_path_scope=True,unknown_handling=True,automatic_rollback=True,offline_enforcement=True,proxy_protocol=True,privacy=True,configuration_restored=True)
    print('PASS: ASN/path scope, unknown fallback, automatic rollback, offline enforcement and private evidence.',flush=True)

def run(setup_only=False,extended_only=False):
    lab=TrafficLab()
    image=docker('image','inspect',os.getenv('HAPROXY_TRAFFIC_IMAGE','converge-haproxy:local'),'--format','{{.Id}}')
    stop=Event();failures=[];success={'requests':0};completed=False;setup_complete=False
    evidence={'status':'running','fixture':'synthetic local Country/ASN data; not production Geo data','started_at_ms':int(time.time()*1000)}
    def continuous():
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users','app-b');lab.https('/shop/admin/v2/users',backend='app-b');success['requests']+=2
            except Exception as e:
                if len(failures)<32: failures.append(type(e).__name__)
            stop.wait(.15)
    with ThreadPoolExecutor(max_workers=1) as workers:
        running=workers.submit(continuous)
        try:
            lab.setup(image)
            setup_complete=True
            if setup_only:
                completed=True;return
            if extended_only:
                extended(lab,image,evidence);completed=True;return
            blocked=rule('Fixture IP',cidrs=[lab.peer+'/32'])
            first=lab.deploy(policy([blocked]))
            for letter in ('a','b'):
                for scheme in ('http','https'): lab.send(letter,scheme,headers=['81.2.69.160'])
            wait_for('recorded observe decision',lambda:any(r.get('traffic',{}).get('decision')=='would_block' for r in lab.query()['requests']))
            enforced=lab.deploy(policy([blocked],mode='enforce'))
            for letter in ('a','b'):
                for scheme in ('http','https'):
                    lab.send(letter,scheme,headers=['1.0.0.1'],status=403)
                    lab.send(letter,scheme,path='/shop/admin/v2/users')
            exception=dict(blocked,id=str(uuid.uuid4()),name='Allowed exception',action='allow')
            lab.deploy(policy([exception,blocked],mode='enforce'))
            for letter in ('a','b'):
                for scheme in ('http','https'):lab.send(letter,scheme)
            current=lab.latest('Store demo');rolled=lab.start(enforced['spec'],current,rollback=enforced);lab.complete(rolled)
            lab.send('a','http',status=403)
            lab.deploy(None)
            lab.source('xff',[lab.peer+'/32'])
            countries=rule('Country fixture',countries=['GB','JP'])
            lab.deploy(policy([countries],mode='enforce'))
            for letter in ('a','b'):
                for scheme in ('http','https'):
                    for header in ('81.2.69.160','2001:218::','1.0.0.1, 81.2.69.160'):
                        lab.send(letter,scheme,headers=[header],status=403)
                    for header in ('1.0.0.1','unknown','1.0.0.1:443','81.2.69.160, 1.0.0.1'):
                        lab.send(letter,scheme,headers=[header])
                    lab.send(letter,scheme,headers=['81.2.69.160','1.0.0.1'])
            print('PASS: native peer/XFF, first-match exceptions, observe/enforce and app B path isolation.',flush=True)
            evidence['peer_xff']=True
            lab.deploy(None)
            lab.source('peer')
            extended(lab,image,evidence)
            completed=True
        finally:
            try:
                if not setup_only and setup_complete:
                        lab.deploy(None)
                        lab.source('peer')
            finally:
                try:docker('rm','-f',lab.fixture)
                except Exception:pass
                stop.set();running.result(timeout=12)
                evidence.update(status='passed' if completed and not failures else 'failed',app_b_successful_requests=success['requests'],app_b_failures=len(failures),failure_types=failures)
                if hasattr(lab,'identities'):
                    after={letter:identity(lab.folder/('agent-'+letter)) for letter in lab.identities}
                    if after!=lab.identities:raise RuntimeError('Node identity/key changed')
                    evidence['identities_preserved']=True
                write_private(lab.state/'evidence.json',json.dumps(evidence,indent=2)+'\n')
    if failures:raise RuntimeError('Application B traffic failed: '+','.join(failures))
    print(f'PASS: {success["requests"]} app B HTTP/HTTPS requests with zero failures; identities preserved.',flush=True)

if __name__=='__main__':
    import argparse
    parser=argparse.ArgumentParser();parser.add_argument('--setup-only',action='store_true');parser.add_argument('--extended-only',action='store_true');args=parser.parse_args();run(args.setup_only,args.extended_only)
