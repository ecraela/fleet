#!/usr/bin/env python3
"""Country/ASN evidence on the existing synthetic HTTP/HTTPS pair."""
from concurrent.futures import ThreadPoolExecutor
from collections import Counter
import json
import os
from pathlib import Path
import ssl
import subprocess
from threading import Event
import time
import urllib.parse
import urllib.request
import uuid

from agentctl import identity
from demo import wait_for, write_private
from lab import network
from listeners import Listeners, local_dpa
from pair import ROOT, compose, selected_state
from requests import LOG_FORMAT, PATH_LOG_FORMAT, GEO_LOG_FORMAT, LEGACY_GEO_LOG_FORMAT, TRAFFIC_LOG_FORMAT, maintenance

SOURCE = Path(__file__).resolve().parents[1]


def untrusted_observations(rows, since, expected):
    # Earlier trusted GET batches may arrive late. Inspect the new OPTIONS
    # requests themselves rather than a changing aggregate over all traffic.
    current = [r for r in rows if r['method'] == 'OPTIONS' and r['at_ms'] >= since]
    for row in current:
        geo = row.get('geo') or {}
        if geo.get('source') != 'peer' or geo.get('status') != 'non_public' or geo.get('country') or geo.get('asn'):
            raise RuntimeError('An untrusted entrypoint header overrode the transport peer')
    received = {(r['status'], r['node_id']) for r in current}
    return current if set(expected.items()) <= received else None


def untrusted_requests(query, since, expected):
    rows, filters = [], dict(method='OPTIONS')
    for _ in range(6):
        page = query('requests', **filters)
        rows.extend(page['requests'])
        if result := untrusted_observations(rows, since, expected):
            return result
        if not page['next_cursor'] or not page['requests'] or page['requests'][-1]['at_ms'] < since:
            return None
        filters.update(end=page['end_ms'], before=page['next_cursor'])
    return None


def recent_requests(query, since, **filters):
    rows=[]
    for _ in range(6):
        page=query('requests',**filters)
        rows.extend(r for r in page['requests'] if r['at_ms']>=since)
        if not page['next_cursor'] or not page['requests'] or page['requests'][-1]['at_ms']<since:
            break
        filters.update(end=page['end_ms'],before=page['next_cursor'])
    return rows


def docker(*args):
    return subprocess.run(['docker', *args], check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60).stdout.strip()


def check_geo(image, directory):
    result = subprocess.run([
        'docker', 'run', '--rm', '--user', f'{os.getuid()}:{os.getgid()}',
        '--network', 'none', '--read-only', '--cap-drop=ALL',
        '--volume', str(directory) + ':/run/haproxy-geo:ro',
        '--entrypoint', '/usr/local/bin/haproxy-agent', image, 'check-geo',
        '--request-geo-policy', '/run/haproxy-geo/policy-a.json',
    ], capture_output=True, text=True, timeout=20)
    try:
        status = json.loads(result.stdout)
    except json.JSONDecodeError:
        raise RuntimeError('Read-only Geo check did not return a structured status') from None
    if not isinstance(status, dict):
        raise RuntimeError('Read-only Geo check returned an invalid status')
    return result.returncode, status


def install_profile(lab, letter):
    frontend, headers = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true', letter=letter)
    original = json.dumps(frontend, sort_keys=True)
    target = dict(address='/run/haproxy/access.sock', facility='local0', format='raw', length=2048)
    if frontend.get('log_format') not in ('', None, LOG_FORMAT, PATH_LOG_FORMAT, GEO_LOG_FORMAT, LEGACY_GEO_LOG_FORMAT, TRAFFIC_LOG_FORMAT) or frontend.get('log_target_list') not in ([target], [dict(nolog=True), target]):
        raise RuntimeError('Preserve customized frontend logging before running the Geo demo')
    tcp = dict(type='connection', action='set-var', var_scope='sess', var_name='cv_peer', expr='src')
    accepted = dict(type='session', action='set-var', var_scope='sess', var_name='cv_client', expr='src')
    http = [dict(type='set-var', var_scope='txn', var_name=name, var_expr=expr) for name, expr in (
        ('cv_path', 'path'), ('cv_xff_count', 'req.fhdr_cnt(X-Forwarded-For)'), ('cv_xff', 'req.fhdr(X-Forwarded-For)'))]
    for key, captures in [('tcp_request_rule_list', [tcp, accepted]), ('http_request_rule_list', http)]:
        rules = frontend.get(key, [])
        for capture in captures:
            matching = [r for r in rules if r.get('var_name') == capture['var_name']]
            if matching and matching != [capture]:
                raise RuntimeError('Preserve customized local Geo capture rules')
        retained = [r for r in rules if r.get('var_name') not in {c['var_name'] for c in captures}]
        # Shared TLS guards must stay the HTTP prefix. TCP peer capture must
        # precede connection source rewriting. This lab has no header rewrites.
        frontend[key] = retained + captures if key == 'http_request_rule_list' else captures + retained
    frontend['log_format'] = TRAFFIC_LOG_FORMAT if frontend.get('log_format') == TRAFFIC_LOG_FORMAT else GEO_LOG_FORMAT
    frontend['log_target_list'] = [dict(nolog=True), target]
    if json.dumps(frontend, sort_keys=True) == original:
        return
    transaction, _ = local_dpa('/services/haproxy/transactions?version='+headers['configuration-version'], {}, 'POST', letter=letter)
    tid, committed = transaction['id'], False
    try:
        local_dpa('/services/haproxy/configuration/frontends/public?full_section=true&transaction_id='+tid, frontend, 'PUT', letter=letter)
        _, h = local_dpa('/services/haproxy/transactions/'+tid+'?force_reload=true', method='PUT', letter=letter)
        committed = True
        if reload := h.get('reload-id'):
            def loaded():
                value, _ = local_dpa('/services/haproxy/reloads/'+reload, letter=letter)
                if value['status'] == 'failed':
                    raise RuntimeError('Geo profile failed HAProxy validation/reload')
                return value['status'] == 'succeeded'
            wait_for('Geo capture profile reload', loaded)
    finally:
        if not committed:
            local_dpa('/services/haproxy/transactions/'+tid, method='DELETE', letter=letter)
    _, headers = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true', letter=letter)
    version = int(headers['configuration-version'])
    def discovered():
        node = lab.nodes['haproxy-'+letter]
        d = lab.api.request('/v1/nodes/'+node['id']+'/configuration')['control'].get('discovery') or {}
        cfg = d.get('configuration') or {}
        return cfg.get('document') and cfg.get('request_logs_allowed') and cfg.get('version',0)>=version
    wait_for('discovery of the loaded Geo profile', discovered)


def change_section(letter, resource, mutate):
    path = '/services/haproxy/configuration/'+resource
    section, headers = local_dpa(path+'?full_section=true', letter=letter)
    mutate(section)
    transaction, _ = local_dpa('/services/haproxy/transactions?version='+headers['configuration-version'], {}, 'POST', letter=letter)
    tid, committed = transaction['id'], False
    try:
        local_dpa(path+'?full_section=true&transaction_id='+tid, section, 'PUT', letter=letter)
        _, headers = local_dpa('/services/haproxy/transactions/'+tid+'?force_reload=true', method='PUT', letter=letter)
        committed = True
        if reload := headers.get('reload-id'):
            def loaded():
                result, _ = local_dpa('/services/haproxy/reloads/'+reload, letter=letter)
                if result['status']=='failed': raise RuntimeError('Client IP profile reload failed')
                return result['status']=='succeeded'
            wait_for('client IP profile reload', loaded)
    finally:
        if not committed: local_dpa('/services/haproxy/transactions/'+tid, method='DELETE', letter=letter)


def entrypoint_probe_batch(lab):
    for _ in range(8):
        for app,path in [('app-a','/shop/items'),('app-b','/shop/admin/v2/users')]:
            # Each application/protocol needs consecutive requests so a quiet
            # round-robin entrypoint cannot pin each application to one node.
            for _ in range(2):
                lab.request(path,app)
            for _ in range(2):
                lab.https(path,backend=app)


def entrypoint_proxy_observations(query, since, nodes):
    seen=set(); rows=[]
    for name,app in [('Store demo','app-a'),('Admin demo','app-b')]:
        for letter in ('a','b'):
            node=nodes['haproxy-'+letter]['id']
            for row in recent_requests(query,since,app=name,node=node):
                if row['at_ms']>=since and row.get('geo',{}).get('source')=='proxy_protocol':
                    seen.add((app,letter,row['scheme']));rows.append(row)
    expected={(app,letter,scheme) for app in ('app-a','app-b') for letter in ('a','b') for scheme in ('http','https')}
    return rows if expected <= seen else None


def proxy_protocol(lab, fixture, untrusted, image, peer, policy, collection, query, trust):
    """Exercise the real shared TCP entrypoint (v2) and a controlled v1 sender."""
    entrypoint = json.loads(docker('inspect','converge-haproxy-pair-entrypoint-1','--format','{{json .NetworkSettings.Networks}}'))
    ingress = next(iter(entrypoint.values()))['IPAddress']
    original = {}
    for letter in ('a','b'):
        f, _ = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true',letter=letter)
        rules = f.get('tcp_request_rule_list',[])
        if any(r.get('action') in ('expect-proxy','set-src','set-src-port') for r in rules) or any(b.get('accept_proxy') for b in f.get('binds',{}).values()):
            raise RuntimeError('Preserve customized client address rules before running this synthetic test')
        original[letter] = rules
    backend,_ = local_dpa('/services/haproxy/configuration/backends/haproxy_nodes?full_section=true')
    if any(s.get('send-proxy')=='enabled' or s.get('send-proxy-v2')=='enabled' for s in backend['servers'].values()):
        raise RuntimeError('Preserve customized entrypoint PROXY configuration')
    docker('run','-d','--name',untrusted,'--network',network(ROOT),'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--memory=24m','--volume',str(trust)+':/trust.pem:ro','--entrypoint','sleep',image,'1800')
    changed_nodes = []
    evidence = {}
    entry_cluster=(selected_state()/'entrypoint-cluster-id').read_text().strip()
    entry_id=next(n['id'] for n in lab.api.request('/v1/nodes')['nodes'] if n['cluster_id']==entry_cluster and not n.get('revoked_at'))
    def entrypoint(letter, withdrawn, proxy_enabled):
        if not withdrawn:
            healthy_since=None
            def healthy():
                nonlocal healthy_since
                node=lab.nodes['haproxy-'+letter]
                value=lab.api.request('/v1/nodes/'+node['id']+'/configuration')
                d=value['control'].get('discovery') or {}
                servers=d.get('servers',[])
                if not value['fresh'] or not servers or any(s['health']!='UP' or s.get('check_status','').removeprefix('* ') not in ('L4OK','L6OK','L7OK','L7OKC') for s in servers):
                    healthy_since=None;return False
                healthy_since=healthy_since or time.monotonic()
                return time.monotonic()-healthy_since>=5
            wait_for('verified backend health before host rejoin',healthy)
        def mutate(b):
            server=b['servers']['haproxy-'+letter]
            server['maintenance']='enabled' if withdrawn else 'disabled'
            if proxy_enabled: server['send-proxy-v2']='enabled'
            else: server.pop('send-proxy-v2',None)
        change_section('entrypoint','backends/haproxy_nodes',mutate)
        def observed():
            d=lab.api.request('/v1/nodes/'+entry_id+'/configuration')['control'].get('discovery') or {}
            return any(s['backend']=='haproxy_nodes' and s['name']=='haproxy-'+letter and s['state']==('maint' if withdrawn else 'ready') and (withdrawn or s['health']=='UP') for s in d.get('servers',[]))
        wait_for('host-owned entrypoint fixture update',observed)
        if withdrawn:
            zero_since=None
            def drained():
                nonlocal zero_since
                node=lab.nodes['haproxy-'+letter]
                d=lab.api.request('/v1/nodes/'+node['id']+'/configuration')['control'].get('discovery') or {}
                front=next((p for p in d.get('proxies',[]) if p['name']=='public' and p['kind']=='frontend'),{})
                if time.time()*1000-d.get('observed_at_ms',0)>5000 or front.get('active_connections')!=0:
                    zero_since=None;return False
                zero_since=zero_since or time.monotonic()
                return time.monotonic()-zero_since>=3
            wait_for('zero live connections before host PROXY update',drained)
    def call(container, letter, scheme, claim=None, header='1.0.0.1', status=200):
        port=8443 if scheme=='https' else 8080
        args=['docker','exec',container,'curl','--silent','--show-error','--max-time','3','--noproxy','*','--cacert','/trust.pem','--connect-to',f'apps.demo.test:{port}:haproxy-{letter}:{port}','--request','OPTIONS','-H','Host: apps.demo.test','-H','X-Forwarded-For: '+header,'-w','\n%{http_code}']
        if claim: args+=['--haproxy-clientip',claim]
        args += [f'{scheme}://apps.demo.test:{port}/shop/items?status={status}&client-ip-secret=withheld']
        return subprocess.run(args,text=True,capture_output=True,timeout=8)
    def fresh_records(since, expected):
        # Distinct status codes isolate this batch from unrelated local traffic.
        found={}
        for letter in ('a','b'):
            node=lab.nodes['haproxy-'+letter]['id']
            rows=recent_requests(query,since,node=node,method='OPTIONS')
            for row in rows:
                key=(row['status'],node,row['scheme'])
                if row['at_ms']>=since and key in expected: found[key]=row
        if set(found)!=set(expected): return None
        for key, row in found.items():
            g=row.get('geo') or {}
            want=expected[key]
            if any(g.get(k)!=v for k,v in want.items()):
                raise RuntimeError('PROXY address source or spoof-resistance assertion failed')
        return list(found.values())
    try:
        for letter in ('a','b'):
            node=lab.nodes['haproxy-'+letter]
            entrypoint(letter,True,False)
            changed_nodes.append(letter)
            def receiver(f):
                f['tcp_request_rule_list']=original[letter]+[dict(type='connection',action='expect-proxy',cond='if',cond_test='{ var(sess.cv_peer) -m ip '+ingress+'/32 '+peer+'/32 }')]
            change_section(letter,'frontends/public',receiver)
            changed=int(time.time()*1000)
            policy(letter,'proxy_protocol',[ingress+'/32',peer+'/32'])
            wait_for('PROXY profile on '+node['name'],lambda:collection('proxy_protocol',node=node['id'],after=changed))
            entrypoint(letter,False,True)
        # The shared entrypoint sends v2 before HTTP or TLS. Observe both apps,
        # protocols and nodes without replacing its single round-robin pool.
        start=int(time.time()*1000)
        entrypoint_probe_batch(lab)
        evidence['entrypoint_v2']=wait_for('v2 through all eight local traffic groups',lambda:entrypoint_proxy_observations(query,start,lab.nodes),seconds=45)
        write_private(selected_state()/'geo/proxy-progress.json',json.dumps(evidence)+'\n')
        start=int(time.time()*1000); expected={}
        for letter in ('a','b'):
            node=lab.nodes['haproxy-'+letter]['id']
            for scheme in ('http','https'):
                for claim,status,want in [('81.2.69.160',234,dict(source='proxy_protocol',country='GB',status='partial')),('2001:218::',235,dict(source='proxy_protocol',country='JP',status='partial')),('1.0.0.1',236,dict(source='proxy_protocol',asn=15169,status='partial'))]:
                    result=call(fixture,letter,scheme,claim,status=status)
                    if result.returncode or not result.stdout.endswith('\n'+str(status)): raise RuntimeError('Trusted PROXY v1 fixture did not serve')
                    expected[(status,node,scheme)]=want
                # A normal HTTP/TLS request from a non-PROXY peer still serves,
                # but its forged header cannot supply missing PROXY evidence.
                result=call(untrusted,letter,scheme,status=237)
                if result.returncode or not result.stdout.endswith('\n237'): raise RuntimeError('Direct non-PROXY probe did not serve')
                expected[(237,node,scheme)]=dict(source='unknown',status='unresolved')
                result=call(untrusted,letter,scheme,'81.2.69.160',status=238)
                if result.stdout.endswith('\n238') or result.stdout.endswith('\n200'): raise RuntimeError('Untrusted peer injected a PROXY identity')
                result=call(fixture,letter,scheme,status=239)
                if result.stdout.endswith('\n239') or result.stdout.endswith('\n200'): raise RuntimeError('Trusted PROXY peer omitted the required envelope')
        evidence['v1_and_spoof_resistance']=wait_for('PROXY v1 and fail-closed records',lambda:fresh_records(start,expected),seconds=45)
        # Mixed trust must not fall back to an attacker-selected XFF value.
        start=int(time.time()*1000)
        for letter in ('a','b'): policy(letter,'xff',[ingress+'/32',peer+'/32'])
        wait_for('mixed-chain policy',lambda:collection('xff',after=start))
        expected={}; start=int(time.time()*1000)
        for letter in ('a','b'):
            for scheme in ('http','https'):
                result=call(fixture,letter,scheme,'81.2.69.160',status=240)
                if result.returncode or not result.stdout.endswith('\n240'): raise RuntimeError('Mixed-chain fixture did not serve')
                expected[(240,lab.nodes['haproxy-'+letter]['id'],scheme)]=dict(source='unknown',status='unresolved')
        evidence['mixed_chain']=wait_for('mixed PROXY/XFF remains unresolved',lambda:fresh_records(start,expected),seconds=45)
        print('PASS: real PROXY v1/v2 on HTTP/HTTPS, both nodes/apps; untrusted envelopes, missing envelopes, forged XFF and mixed trust cannot supply client identity.',flush=True)
        return evidence
    finally:
        for letter in reversed(changed_nodes):
            node=lab.nodes['haproxy-'+letter]
            entrypoint(letter,True,True)
            change_section(letter,'frontends/public',lambda f:f.update(tcp_request_rule_list=original[letter]))
            policy(letter,'xff',[peer+'/32'])
            entrypoint(letter,False,False)
        subprocess.run(['docker','rm','-f',untrusted],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)


def run():
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    folder = selected_state()
    trust = folder/'acme/public/trust.pem'
    if not trust.exists():
        trust = folder/'tls/ca.crt'
    lab.context = ssl.create_default_context(cafile=trust)
    latest = {name: max((r for r in lab.revisions() if r['spec']['name'] == name), key=lambda r:r['number']) for name in ('Store demo', 'Admin demo')}
    if any(r['status'] != 'succeeded' or not r['spec'].get('request_telemetry') or r['spec']['hostname'] != 'apps.demo.test' for r in latest.values()):
        raise RuntimeError('Prepare the existing two synthetic applications with request collection first')
    lab.request('/shop/admin/v2/users','app-b')
    lab.https('/shop/admin/v2/users',backend='app-b')
    for node in lab.nodes.values():
        wait_for('fresh metrics before Geo changes: '+node['name'],lambda node=node:(v if (v:=lab.api.request('/v1/nodes/'+node['id']+'/metrics'))['fresh'] and v['scrape_ok'] else None))
    before = lab.inventory()
    identities = {letter: identity(folder/('agent-'+letter)) for letter in ('a','b','entrypoint')}
    image = docker('image', 'inspect', os.getenv('HAPROXY_GEO_IMAGE', 'converge-haproxy:local'), '--format', '{{.Id}}')
    help_text = docker('run', '--rm', '--network', 'none', '--entrypoint', '/bin/sh', image, '-c', 'haproxy-agent --help 2>&1')
    if 'request-geo-policy' not in help_text:
        raise RuntimeError('Build an image containing Geo before running this scenario')
    geo = folder/'geo'
    geo.mkdir(mode=0o700, exist_ok=True)
    for source, dest in [('GeoIP2-Country-Test.mmdb','country.mmdb'), ('GeoLite2-ASN-Test.mmdb','asn.mmdb')]:
        raw = (SOURCE/'internal/accesslog/testdata'/source).read_bytes()
        p = geo/dest
        p.write_bytes(raw);p.chmod(0o600)
    services = {s: {'image': image} for s in ('manager','ingest','agent-a','agent-b')}
    for letter in ('a','b'):
        services['agent-'+letter].update(environment={'HAPROXY_REQUEST_GEO_POLICY': '/run/haproxy-geo/policy-'+letter+'.json'}, volumes=[str(geo)+':/run/haproxy-geo:ro'])
        write_private(geo/('policy-'+letter+'.json'), json.dumps(dict(version=1,mode='peer',country_database='/run/haproxy-geo/country.mmdb',asn_database='/run/haproxy-geo/asn.mmdb'))+'\n')
    write_private(geo/'compose.json', json.dumps({'services':services})+'\n')
    code,checked=check_geo(image,geo)
    if code or checked.get('mode')!='peer' or checked.get('status')!='ready': raise RuntimeError('Read-only local Geo check did not validate the fixtures')
    compose('up','-d','--no-build','--no-deps','manager','ingest')
    wait_for('Geo manager', lambda:lab.api.request('/healthz',authorized=False))
    stop, failures, successful = Event(), [], []
    stage = {'name':'local logging profile'}
    proxy = 'haproxy-geo-fixture-'+uuid.uuid4().hex[:12]
    evidence = {'fixture': 'MaxMind synthetic test databases; not production geolocation data'}
    evidence['started_at_ms']=int(time.time()*1000)
    completed=False
    write_private(geo/'evidence.json',json.dumps(dict(status='running',**evidence))+'\n')
    def query(kind, app='Store demo', **filters):
        return lab.api.request(lab.path+'/'+latest[app]['application_id']+'/'+kind+'?'+urllib.parse.urlencode(dict(range='15m',**filters)))
    def continuous():
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users','app-b');lab.https('/shop/admin/v2/users',backend='app-b')
                successful.append(2)
            except Exception as e:
                failures.append(dict(stage=stage['name'],type=type(e).__name__,message=str(e)[:200]))
            stop.wait(.1)
    def policy(letter, mode, trusted=None):
        p=dict(version=1,mode=mode,country_database='/run/haproxy-geo/country.mmdb',asn_database='/run/haproxy-geo/asn.mmdb')
        if trusted: p['trusted_proxies']=trusted
        write_private(geo/('policy-'+letter+'.json'),json.dumps(p)+'\n')
        compose('restart','agent-'+letter)
    def collection(mode, status='ready', node=None, after=0):
        g=query('geography',**({'node':node} if node else {}))
        return g if g['collection'] and all(c['fresh'] and c['pipeline']['at_ms']>=after and c['pipeline'].get('geo',{}).get('mode')==mode and c['pipeline']['geo']['status']==status for c in g['collection']) else None
    def captured(letter, scheme, headers):
        args=['exec',proxy,'curl','--silent','--show-error','--max-time','5','--noproxy','*','--cacert','/trust.pem','--connect-to',f'apps.demo.test:{8443 if scheme=="https" else 8080}:haproxy-{letter}:{8443 if scheme=="https" else 8080}','--request','POST','-H','Host: apps.demo.test']
        for header in headers: args += ['-H','X-Forwarded-For: '+header]
        args += ['-w','\n%{http_code}',f'{scheme}://apps.demo.test:{8443 if scheme=="https" else 8080}/shop/items?geo-fixture-secret=never-export']
        if not docker(*args).endswith('\n200'):
            raise RuntimeError('Geo fixture request failed')
    try:
        with ThreadPoolExecutor(max_workers=1) as pool:
            running=pool.submit(continuous)
            try:
                for letter in ('a','b'):
                    node=lab.nodes['haproxy-'+letter]
                    maintenance(lab,node,'withdraw')
                    try:
                        previous=query('geography',node=node['id'])['collection'][0].get('pipeline') or {}
                        changed=int(time.time()*1000)
                        compose('up','-d','--no-build','--no-deps','--force-recreate','agent-'+letter)
                        install_profile(lab,letter)
                        wait_for('Geo agent ready',lambda:all(c['fresh'] and c['pipeline'] and c['pipeline']['at_ms']>=changed and c['pipeline']['boot_id']!=previous.get('boot_id') and c['pipeline'].get('geo',{}).get('status')=='ready' for c in query('geography',node=node['id'])['collection']))
                    finally:
                        maintenance(lab,node,'rejoin')
                    print('PASS: Geo profile ready and node rejoined: '+node['name'],flush=True)
                docker('run','-d','--name',proxy,'--network',network(ROOT),'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--memory=24m','--volume',str(trust)+':/trust.pem:ro','--entrypoint','sleep',image,'1800')
                address = json.loads(docker('inspect',proxy,'--format','{{json .NetworkSettings.Networks}}'))
                peer=next(iter(address.values()))['IPAddress']
                changed=int(time.time()*1000)
                for letter in ('a','b'):
                    policy(letter,'xff',[peer+'/32'])
                wait_for('trusted fixture policy on both agents',lambda:collection('xff',after=changed))
                # Untrusted peer ignores headers; a trusted last hop stops the walk
                # before forged leftmost entries. Duplicate fields stay unresolved.
                start=int(time.time()*1000)
                for letter in ('a','b'):
                    for scheme in ('http','https'):
                        for header in ('81.2.69.160','1.0.0.1','2001:218::','203.0.113.1','10.4.5.6','1.0.0.1, 81.2.69.160'):
                            for _ in range(3): captured(letter,scheme,[header])
                        captured(letter,scheme,['81.2.69.160','1.0.0.1'])
                        captured(letter,scheme,['unknown'])
                def observed():
                    # Count this batch itself. A rolling 15-minute aggregate
                    # can lose older fixture rows while a repeated run advances.
                    current=[]
                    for node in lab.nodes.values():
                        recent=recent_requests(query,start,node=node['id'],method='POST')
                        if len(recent)<40: return None
                        current.extend(recent)
                    countries=Counter(r.get('geo',{}).get('country') for r in current)
                    networks=Counter(r.get('geo',{}).get('asn') for r in current)
                    statuses=Counter(r.get('geo',{}).get('status') for r in current)
                    if countries['GB']!=24 or countries['JP']!=12 or networks[15169]!=12 or statuses['unresolved']!=8 or statuses['non_public']!=12 or statuses['not_found']!=12:
                        raise RuntimeError('Fresh XFF batch did not match the country/ASN and fail-closed expectations')
                    g=query('geography')
                    return g if g['country_known']>=36 and g['asn_known']>=12 and g['unresolved']>=8 and all(c['fresh'] for c in g['collection']) else None
                evidence['geography']=wait_for('real Country/ASN lookups and unknowns',observed,seconds=45)
                for key,value in [('country','GB'),('country','JP'),('asn','15169')]:
                    rows=query('requests',**{key:value})['requests']
                    if not rows or any(str(r.get('geo',{}).get(key,''))!=value for r in rows):
                        raise RuntimeError('Typed Geo request filter failed')
                    evidence[key+'_'+value]=rows
                rows=query('requests',country='unknown',asn='0')['requests']
                if not rows or any(r.get('geo',{}).get('country') or r.get('geo',{}).get('asn') for r in rows):
                    raise RuntimeError('Unknown request filter omitted unknowns or invented a location')
                evidence['unknown']=rows
                spoof_start=int(time.time()*1000)
                spoof_expected={}
                # Distinct synthetic response codes identify each request even
                # when a frontend logs the same request to two local targets.
                for status in range(220,232):
                    req=urllib.request.Request('http://localhost:18081/shop/items?status='+str(status),method='OPTIONS',headers={'Host':'apps.demo.test','X-Forwarded-For':'81.2.69.160'})
                    with urllib.request.urlopen(req,timeout=4) as response:
                        if response.status!=status:raise RuntimeError('Spoof fixture failed')
                        spoof_expected[status]=lab.nodes[response.headers['X-HAProxy-Node']]['id']
                        response.read(4096)
                evidence['untrusted']=wait_for('all untrusted entrypoint observations',lambda:untrusted_requests(query,spoof_start,spoof_expected),seconds=45)
                print('PASS: real IPv4/IPv6 Country/ASN lookup, HTTP/HTTPS, multi-hop trust, malformed/duplicate headers and untrusted spoof rejection.',flush=True)
                stage['name']='PROXY fixtures'
                evidence['proxy_protocol']=proxy_protocol(lab,proxy,proxy+'-untrusted',image,peer,policy,collection,query,trust)
                stage['name']='database failure and recovery'
                # A missing database must stay visible without starving metrics.
                old=(geo/'country.mmdb').read_bytes();(geo/'country.mmdb').write_bytes(b'corrupt fixture')
                changed=int(time.time()*1000)
                compose('restart','agent-a')
                wait_for('Geo database failure status',lambda:collection('xff','database_unavailable',lab.nodes['haproxy-a']['id'],changed))
                code,checked=check_geo(image,geo)
                if code!=1 or checked.get('status')!='database_unavailable': raise RuntimeError('Read-only Geo check did not fail closed on corrupt data')
                captured('a','https',['81.2.69.160'])
                wait_for('database failure evidence',lambda:query('geography')['database_unavailable']>=1,seconds=30)
                wait_for('fresh live metrics during database failure',lambda:(v if (v:=lab.api.request('/v1/nodes/'+lab.nodes['haproxy-a']['id']+'/metrics'))['fresh'] and v['scrape_ok'] else None),seconds=20)
                (geo/'country.mmdb').write_bytes(old)
                for letter in ('a','b'):policy(letter,'peer')
                compose('restart','manager')
                wait_for('Geo history after restart',lambda:query('requests',country='GB')['requests'])
                evidence['recovered']=wait_for('recovered Geo collection',lambda:collection('peer'))
                serialized=json.dumps(evidence)
                if any(secret in serialized for secret in ('81.2.69.160','1.0.0.1','2001:218::','203.0.113.1','never-export','client-ip-secret','forwarded_for','client_ip',peer)):
                    raise RuntimeError('Private local input escaped into query results')
                after=lab.inventory()
                if [v['document'] for v in before]!=[v['document'] for v in after]:
                    raise RuntimeError('Geo setup changed application routing, listeners or TLS')
                if identities!={letter:identity(folder/('agent-'+letter)) for letter in identities}:
                    raise RuntimeError('Geo setup replaced a node identity/key')
                completed=True
            finally:
                stop.set();running.result()
    finally:
        # Exact host trust is needed only for the private fixture. Return the
        # demo to direct-peer observations even if an assertion failed.
        (geo/'country.mmdb').write_bytes((SOURCE/'internal/accesslog/testdata/GeoIP2-Country-Test.mmdb').read_bytes())
        for letter in ('a','b'):
            policy(letter,'peer')
        subprocess.run(['docker','rm','-f',proxy],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        evidence.update(status='passed' if completed and not failures else 'failed',app_b_successful_requests=sum(successful),app_b_failures=len(failures),failure_details=failures[:10])
        write_private(geo/'evidence.json',json.dumps(evidence,indent=2)+'\n')
    if failures:raise RuntimeError(f'{len(failures)} neighboring app requests failed: {failures[:3]}')
    print(f'PASS: {sum(successful)} app B HTTP/HTTPS requests, zero failures; Geo history retained, database failure visible, routing/TLS/identities unchanged.',flush=True)


if __name__=='__main__':
    run()
