#!/usr/bin/env python3
"""Bounded load, real failures and fresh-storage recovery on the two-node demo."""
import argparse
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import re
import shutil
import socket
import ssl
from threading import Event, Lock
import time
import urllib.parse
import uuid

from demo import wait_for, write_private
from listeners import Listeners
from pair import STATE, latency, selected_state
from recovery import Restore, checkpoint, command, source_command

RECOVERY_SOURCE = ('manager','ingest','agent-a','agent-b','agent-entrypoint','victoriametrics','victorialogs')


def require_local_docker():
    override=os.getenv('DOCKER_HOST','')
    context=command(['docker','context','inspect','--format','{{.Endpoints.docker.Host}}']).decode().strip()
    if (override and not override.startswith('unix://')) or not context.startswith('unix://'):
        raise RuntimeError('Hardening drills require a local Docker Unix socket, never a remote context')


class Traffic:
    def __init__(self, context, routes, http=18081, https=18444):
        self.context, self.routes, self.ports = context, routes, {'http':http, 'https':https}

    def request(self, index):
        # Rotate the case offset too: alternating HTTP/HTTPS must not correlate
        # permanently with the entrypoint's alternating HAProxy nodes.
        index = (index+index//4) % 4
        scheme = ('http', 'https')[index % 2]
        path, backend = self.routes[(index//2) % len(self.routes)]
        raw = socket.create_connection(('127.0.0.1', self.ports[scheme]), timeout=3)
        with raw:
            connection = self.context.wrap_socket(raw, server_hostname='apps.demo.test') if scheme == 'https' else raw
            with connection:
                pin = hashlib.sha256(connection.getpeercert(binary_form=True)).hexdigest() if scheme == 'https' else ''
                connection.sendall(f'GET {path} HTTP/1.1\r\nHost: apps.demo.test\r\nConnection: close\r\n\r\n'.encode())
                response = http.client.HTTPResponse(connection)
                response.begin()
                body = response.read(1024*1024)
                node = response.getheader('X-HAProxy-Node')
                if (response.status != 200 or response.getheader('X-Demo-Backend') != backend or
                        node not in ('haproxy-a', 'haproxy-b') or b'Converge HAProxy demo backend' not in body):
                    raise RuntimeError('Unexpected demo route, status or response')
                return scheme, backend, node, pin


class Load:
    """Fixed-rate workers with no catch-up bursts and bounded latency histograms."""
    def __init__(self, traffic, workers, rps):
        self.traffic, self.workers, self.rps = traffic, workers, rps
        self.stop, self.lock = Event(), Lock()
        self.phase = 'warmup'
        self.stats = defaultdict(lambda:dict(success=0, failures=Counter(), latency=Counter(), routes=Counter()))

    def worker(self, index):
        step = self.workers/self.rps
        due = time.monotonic()+index/self.rps
        count = index
        while not self.stop.wait(max(0, due-time.monotonic())):
            with self.lock:
                phase = self.phase
            started = time.monotonic()
            try:
                route = self.traffic.request(count)
                error = None
            except Exception as exception:
                error = type(exception).__name__
            elapsed = time.monotonic()-started
            with self.lock:
                row = self.stats[phase]
                if error:
                    row['failures'][error] += 1
                else:
                    row['success'] += 1
                    row['routes']['/'.join(route[:3])] += 1
                    row['latency'][min(30_000, math.ceil(elapsed*1000))] += 1
            count += self.workers
            due = max(due+step, time.monotonic())

    def __enter__(self):
        self.pool = ThreadPoolExecutor(max_workers=self.workers)
        self.futures = [self.pool.submit(self.worker, i) for i in range(self.workers)]
        return self

    def mark(self, phase):
        with self.lock:
            self.phase = phase

    def __exit__(self, *_):
        self.stop.set()
        for future in self.futures:
            future.result(timeout=5)
        self.pool.shutdown()

    def results(self):
        with self.lock:
            rows = {}
            for name, row in self.stats.items():
                threshold, cumulative, p95 = math.ceil(row['success']*.95), 0, 0
                for milliseconds, count in sorted(row['latency'].items()):
                    cumulative += count
                    if cumulative >= threshold:
                        p95 = milliseconds
                        break
                rows[name] = dict(success=row['success'], failures=dict(row['failures']),
                                  p95_ms=p95, routes=dict(row['routes']))
            return rows


def lab_state():
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    if set(lab.nodes) != {'haproxy-a', 'haproxy-b'}:
        raise RuntimeError('Run python3 scripts/pair.py up first')
    trust = selected_state()/'acme/public/trust.pem'
    if trust.exists():
        lab.context.load_verify_locations(cafile=trust)
    configs = {}
    for name, node in lab.nodes.items():
        view = lab.api.request(f"/v1/nodes/{node['id']}/configuration")
        control = view['control']
        cfg = (control.get('discovery') or {}).get('configuration') or {}
        if (not view.get('fresh') or cfg.get('error_code') or not cfg.get('document') or
                any(control.get(k) for k in ('active_rollout_id', 'maintenance_id', 'pending_operation_id'))):
            raise RuntimeError('Finish demo operations and obtain fresh discovery before hardening')
        configs[name] = {k:cfg[k] for k in ('document', 'artifact_id', 'version')}
    applications = configs['haproxy-a']['document'].get('applications', [])
    if {a['name'] for a in applications} != {'Store demo', 'Admin demo'} or any(a['hostname'] != 'apps.demo.test' or a.get('disabled') for a in applications):
        raise RuntimeError('Hardening requires both enabled synthetic demo apps')
    routes = [(a['path_prefix'].rstrip('/')+'/hardening', 'app-a' if a['name']=='Store demo' else 'app-b') for a in sorted(applications, key=lambda a:a['name'])]
    if any(c['document'] != configs['haproxy-a']['document'] for c in configs.values()):
        raise RuntimeError('Demo nodes do not have the same effective application document')
    history = lab.revisions()
    baseline = dict(cluster=lab.cluster, configurations=configs, history=history,
                    node_ids=sorted(n['id'] for n in lab.api.request('/v1/nodes')['nodes']), routes=routes)
    sample_node = lab.nodes['haproxy-a']['id']
    baseline['metrics_history'] = lab.api.request(f'/v1/nodes/{sample_node}/history?range=15m')
    baseline['requests'] = {}
    for app in applications:
        if app.get('request_telemetry'):
            endpoint = lab.path+'/'+app['id']+'/requests?range=15m&end='+str(int(time.time()*1000))
            baseline['requests'][endpoint] = lab.api.request(endpoint)['requests']
            if not baseline['requests'][endpoint]:
                raise RuntimeError('Hardening requires historic requests for each opted-in application')
    traffic = Traffic(lab.context, routes)
    pins = {}
    for i in range(16):
        scheme, _, node, pin = traffic.request(i)
        if scheme == 'https': pins[node] = pin
    if len(pins) != 2:
        raise RuntimeError('Need verified HTTPS through both demo nodes')
    baseline['pins'] = pins
    return lab, traffic, baseline


def fresh(api, baseline, since=0):
    for cfg_name in baseline['configurations']:
        # Restore preserves enrollment identities; it must not create new nodes.
        node = next(n for n in api.request('/v1/nodes')['nodes'] if n['name']==cfg_name and n['cluster_id']==baseline['cluster'] and not n.get('revoked_at'))
        metrics = api.request(f"/v1/nodes/{node['id']}/metrics")
        if not metrics.get('observed_at'):
            return False
        observed = datetime.fromisoformat(metrics['observed_at'].replace('Z', '+00:00')).timestamp()
        view = api.request(f"/v1/nodes/{node['id']}/configuration")
        control = view['control']
        cfg = (control.get('discovery') or {}).get('configuration') or {}
        if (not metrics.get('fresh') or not metrics.get('scrape_ok') or observed <= since or
                not view.get('fresh') or cfg.get('observed_at_ms',0)<=since*1000 or
                not cfg.get('document') or cfg.get('error_code')):
            return False
        if cfg['document'] != baseline['configurations'][cfg_name]['document']:
            raise RuntimeError('Serving application configuration changed during hardening')
    return True


def resources():
    ids = source_command('ps','-q').decode().split()
    rows = command(['docker','stats','--no-stream','--format','{{json .}}',*ids]).decode().splitlines()
    result = []
    for line in rows:
        row = json.loads(line)
        result.append({k:row[k] for k in ('Name','CPUPerc','MemUsage','PIDs')})
    for cid in ids:
        state = json.loads(command(['docker','inspect','--format','{{json .State}}',cid]))
        if not state['Running'] or state['OOMKilled']:
            raise RuntimeError('A demo container exited or was killed by its memory limit')
    return result


def collection(lab,baseline):
    if not baseline['requests']:
        return None
    endpoint=next(iter(baseline['requests'])).split('/requests?')[0]+'/traffic?range=15m'
    rows=lab.api.request(endpoint)['collection']
    if len(rows)!=2 or any(not row['fresh'] or not row.get('pipeline') for row in rows):
        raise RuntimeError('Request collection is not healthy under load')
    return {row['name']:{k:row['pipeline'][k] for k in ('boot_id','received','rejected','dropped','unconfirmed')} for row in rows}


def collection_delta(before,after):
    if before is None:
        return None
    result={}
    for name,old in before.items():
        new=after[name]
        if new['boot_id']!=old['boot_id']:
            raise RuntimeError('Request collector restarted during steady load')
        result[name]={k:new[k]-old[k] for k in ('received','rejected','dropped','unconfirmed')}
        if any(v<0 for v in result[name].values()) or any(result[name][k] for k in ('rejected','dropped','unconfirmed')):
            raise RuntimeError('Request telemetry was rejected, dropped or unconfirmed under steady load')
    return result


def entrypoint_state(lab,kind):
    cluster=next(c for c in lab.api.request('/v1/clusters')['clusters'] if c['id']==lab.cluster)
    controller=cluster['topology']['control']['node_id']
    endpoint=f'/v1/nodes/{controller}'
    def ready():
        v=lab.api.request(endpoint+'/discovery')
        return v if v['fresh'] and v['management_enabled'] else None
    view=wait_for('entrypoint runtime control',ready)
    d=view['control']['discovery']
    target=next(s for s in d['servers'] if s['backend']=='haproxy_nodes' and s['name']=='haproxy-a')
    body=dict(request_id=str(uuid.uuid4()),kind=kind,backend='haproxy_nodes',server='haproxy-a',weight=0,
              expected_generation=d['generation'],expected_state=target['state'],expected_weight=target['weight'])
    op=lab.api.request(endpoint+'/operations',body)['operation']
    def done():
        current=lab.api.request(endpoint+'/discovery')
        result=next((o for o in current['operations'] if o['id']==op['id']),None)
        if result and result['status'] in ('failed','unknown'):
            raise RuntimeError('Recovery entrypoint operation failed')
        return result if result and result['status']=='succeeded' else None
    wait_for('entrypoint '+kind+' verified',done)


def healthy_recovery(lab,baseline):
    since=None
    reporting_after=time.time()
    def ready():
        nonlocal since
        node=lab.nodes['haproxy-a']
        view=lab.api.request(f"/v1/nodes/{node['id']}/discovery")
        d=view['control'].get('discovery') or {}
        servers=d.get('servers') or []
        healthy=(fresh(lab.api,baseline,reporting_after) and view['fresh'] and not d.get('error_code') and
                 servers and all(s['health']=='UP' and s.get('check_status','').removeprefix('* ') in ('L4OK','L6OK','L7OK','L7OKC') for s in servers))
        if not healthy:
            since=None
            return False
        if since is None: since=time.monotonic()
        return time.monotonic()-since>=5
    wait_for('recovered node backend health stable for five seconds',ready,seconds=60)


def failures(lab, traffic, baseline, load, seconds):
    evidence = {}
    for service in ('manager', 'ingest', 'postgres', 'victoriametrics', 'victorialogs', 'agent-a'):
        phase = service+'_outage'
        load.mark(phase)
        try:
            source_command('stop', '-t', '3', service)
            time.sleep(seconds)
            # Confirm traffic through the entrypoint even with control/storage absent.
            for i in range(8): traffic.request(i)
        finally:
            source_command('start', service)
        since = time.time()
        started = time.monotonic()
        wait_for(service+' recovery and new observations', lambda:fresh(lab.api, baseline, since), seconds=60)
        evidence[service] = dict(recovery_seconds=round(time.monotonic()-started, 3))
        print('PASS:', service, 'outage leaves HTTP/HTTPS serving; fresh telemetry recovered.', flush=True)
    # An abrupt loss can fail in-flight connections during the health-check window.
    # Measure that window separately; never disguise it as zero-downtime failover.
    load.mark('node_crash_detection')
    started = time.monotonic()
    try:
        source_command('kill', '-s', 'SIGKILL', 'haproxy-a')
        def surviving():
            try:
                return all(traffic.request(i)[2]=='haproxy-b' for i in range(8))
            except (OSError, RuntimeError, http.client.HTTPException):
                return False
        wait_for('node crash failover', surviving, seconds=10)
        evidence['node_failover'] = dict(detection_seconds=round(time.monotonic()-started, 3))
        load.mark('node_crash_converged')
        time.sleep(seconds)
        # Keep the returning proxy out of selection until its application
        # backends have warmed. The stats listener alone proves process liveness.
        entrypoint_state(lab,'drain')
    finally:
        source_command('start', 'haproxy-a')
    load.mark('node_recovery_isolated')
    healthy_recovery(lab,baseline)
    entrypoint_state(lab,'restore')
    load.mark('node_rejoined')
    wait_for('both nodes serving again', lambda:{traffic.request(i)[2] for i in range(12)}=={'haproxy-a','haproxy-b'})
    print('PASS: crashed node recovered while drained; backend health verified before rejoin.',flush=True)
    return evidence


def verify_request_history(api, expected):
    count = 0
    for endpoint, records in expected.items():
        for record in records:
            # Delivery can add older events after the initial observation. Such
            # events may displace a saved row from the first page without loss.
            query = urllib.parse.urlencode({'request_id': record['id']})
            if api.request(endpoint+'&'+query)['requests'] != [record]:
                raise RuntimeError('Historic application request was not restored unchanged')
            count += 1
    return count


def restored_servers_ready(api, baseline, since):
    if not fresh(api, baseline, since):
        return False
    cluster=next(c for c in api.request('/v1/clusters')['clusters'] if c['id']==baseline['cluster'])
    controller=cluster['topology']['control']['node_id']
    nodes=[n['id'] for n in api.request('/v1/nodes')['nodes']
           if n['cluster_id']==baseline['cluster'] and n['name'] in baseline['configurations'] and not n.get('revoked_at')]
    for node in [*nodes,controller]:
        view=api.request(f'/v1/nodes/{node}/discovery')
        discovery=view['control'].get('discovery') or {}
        servers=discovery.get('servers') or []
        if (not view.get('fresh') or discovery.get('error_code') or
                discovery.get('observed_at_ms',0)<=since*1000 or not servers or
                not all(s['health']=='UP' and s.get('check_status','').removeprefix('* ') in ('L4OK','L6OK','L7OK','L7OKC') for s in servers)):
            return False
    return True


def restore(snapshot, workspace, baseline, context, docker_context=None):
    clone = Restore(snapshot, workspace, context, docker_context)
    started = time.monotonic()
    try:
        # A cold control-plane recovery also proves serving does not depend on
        # these processes. Keep the original proxies/backends/entrypoint running.
        # This bounds the rehearsal's memory on small local Docker VMs.
        source_command('stop','-t','5',*RECOVERY_SOURCE,'postgres')
        clone.start()
        # A fenced network prevents the copied identities or delivery jobs from
        # reaching the source installation or an external notification endpoint.
        network = json.loads(clone.docker('network','inspect',clone.project+'_default'))[0]
        if not network['Internal']:
            raise RuntimeError('Recovery network is not fenced')
        api = RestoredAPI(clone)
        path = f"/v1/clusters/{baseline['cluster']}/applications"
        if api.request(path)['revisions'] != baseline['history']:
            raise RuntimeError('Restored application version/rollback history differs')
        # Prove a DB-only/partial node restore fails closed, without re-enrollment.
        node = clone.manifest['nodes']['agent-a']
        identity_file = workspace/node['state']/'identity.json'
        original = identity_file.read_bytes()
        broken = json.loads(original)
        broken.pop('key_pem')
        write_private(identity_file, json.dumps(broken))
        clone.compose('up','-d','--no-deps','agent-a')
        def rejected():
            cid = clone.compose('ps','-aq','agent-a').decode().strip()
            state = json.loads(clone.docker('inspect','--format','{{json .State}}',cid))
            return not state['Running'] and state['ExitCode'] != 0
        wait_for('missing node private key rejected', rejected, seconds=15)
        print('PASS: missing restored node key rejected without re-enrollment.',flush=True)
        if sorted(n['id'] for n in api.request('/v1/nodes')['nodes']) != baseline['node_ids']:
            raise RuntimeError('A partial restore silently re-enrolled a node')
        write_private(identity_file, original.decode())
        connected_after = time.time()
        clone.compose('up','-d','--no-deps','agent-a','agent-b','agent-entrypoint')
        wait_for('restored identities reconnect and report new metrics', lambda:fresh(api,baseline,connected_after),seconds=60)
        print('PASS: restored identities authenticated and reported new metrics/configuration.',flush=True)
        # A live agent can report while HAProxy's initial backend checks are
        # still pending. Qualify serving only after the whole restored path is up.
        healthy_since=None
        def serving_ready():
            nonlocal healthy_since
            if not restored_servers_ready(api,baseline,connected_after):
                healthy_since=None
                return False
            if healthy_since is None: healthy_since=time.monotonic()
            return time.monotonic()-healthy_since>=5
        wait_for('restored node and entrypoint backend health stable for five seconds',serving_ready,seconds=60)
        distribution = Counter()
        for i in range(80):
            scheme, backend, name, pin = restored_request(clone,baseline['routes'],i)
            if scheme=='https' and pin != baseline['pins'][name]:
                raise RuntimeError('Restored node serves a different certificate/key')
            distribution[(scheme,backend,name)] += 1
        if len(distribution)!=8:
            raise RuntimeError('Restored pair does not serve both apps/protocols on both nodes')
        # Read historic telemetry before restart, then prove restored persistence
        # and identity survive an additional manager/agent restart.
        sample_node = node['id']
        history = api.request(f'/v1/nodes/{sample_node}/history?range=15m')
        def points(view):
            return {(json.dumps(series['metric'],sort_keys=True),at,value)
                    for series in view['series'] for at,value in series['values']}
        restored_points = points(history) & points(baseline['metrics_history'])
        if not restored_points:
            raise RuntimeError('Historic metrics were not restored')
        log_count = verify_request_history(api, baseline['requests'])
        since = time.time()
        clone.compose('restart','-t','3','manager','agent-a')
        wait_for('restored manager restart',lambda:fresh(api,baseline,since),seconds=60)
        running=set(clone.compose('ps','--status','running','--services').decode().split())
        if running != set(clone.manifest['services']):
            raise RuntimeError('A restored service stopped during qualification')
        print('PASS: restored apps, TLS keys, history and manager/agent restart verified.',flush=True)
        return dict(recovery_seconds=round(time.monotonic()-started,3),verified_requests=80,
                    missing_key_rejected=True, identities_preserved=True, certificate_pins_preserved=True,
                    history_preserved=True, historic_metric_points=len(restored_points),
                    historic_requests=log_count, fenced_network=True,
                    docker_budget=json.loads(clone.docker('info','--format','{"memory_bytes":{{.MemTotal}},"cpus":{{.NCPU}}}')))
    except BaseException:
        states=[]
        for cid in clone.compose('ps','-aq').decode().split():
            container=json.loads(clone.docker('inspect',cid))[0]
            state=container['State']
            states.append(dict(service=container['Config']['Labels']['com.docker.compose.service'],
                               **{k:state[k] for k in ('Status','ExitCode','OOMKilled')}))
        print('Restore container states:',json.dumps(states),flush=True)
        raise
    finally:
        try:
            clone.close()
        finally:
            source_command('start','--wait','--wait-timeout','30','postgres')
            source_command('start',*RECOVERY_SOURCE)


class RestoredAPI:
    """Probe through docker exec; the restored manager has no published port."""
    def __init__(self,clone):
        self.clone=clone
        self.token=(clone.snapshot/clone.manifest['operator_token']).read_text().strip()

    def request(self,path):
        config='\n'.join((
            'url = '+json.dumps('https://localhost:8443'+path),
            'header = '+json.dumps('Authorization: Bearer '+self.token),
            'header = '+json.dumps('X-Organization-ID: '+self.clone.manifest['organization'])))
        try:
            raw=self.clone.compose('exec','-T','manager','curl','--http1.1','--silent','--show-error','--fail',
                                   '--max-time','3','--cacert','/pki/ca.crt','--config','-',input=config.encode())
        except RuntimeError as error:
            raise OSError('Restored API not ready') from error
        return json.loads(raw)


def restored_request(clone,routes,index):
    index=(index+index//4)%4
    scheme=('http','https')[index%2]
    path,backend=routes[(index//2)%len(routes)]
    args=['exec','-T','manager','curl','--http1.1','--silent','--show-error','--fail','--max-time','4',
          '--dump-header','-','--header','Host: apps.demo.test','--write-out','\nCV_CERT_BEGIN\n%{certs}']
    if scheme=='https':
        args+=['--cacert','/run/recovery-ca.pem','--connect-to','apps.demo.test:8443:pair-entrypoint:8443',
               'https://apps.demo.test:8443'+path]
    else:
        args+=['http://pair-entrypoint:8080'+path]
    raw=clone.compose(*args)
    response,_,certs=raw.partition(b'\nCV_CERT_BEGIN\n')
    headers,_,body=response.partition(b'\r\n\r\n')
    lines=headers.decode().split('\r\n')
    fields=dict(line.lower().split(': ',1) for line in lines[1:] if ': ' in line)
    node=fields.get('x-haproxy-node')
    if (not lines[0].startswith('HTTP/1.1 200 ') or fields.get('x-demo-backend')!=backend or
            node not in ('haproxy-a','haproxy-b') or b'Converge HAProxy demo backend' not in body):
        raise RuntimeError('Restored HTTP/HTTPS route or backend failed')
    pin=''
    if scheme=='https':
        pem=re.search(rb'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----',certs,re.S)
        if not pem: raise RuntimeError('Restored TLS peer certificate evidence missing')
        pin=hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem[0].decode())).hexdigest()
    return scheme,backend,node,pin


def run(args):
    root = STATE/'hardening'
    root.mkdir(mode=0o700, exist_ok=True)
    work = root/uuid.uuid4().hex
    work.mkdir(mode=0o700)
    report = dict(version=1, started_at=datetime.now().astimezone().isoformat(), limits=vars(args).copy())
    report['limits'].pop('report', None)
    try:
        require_local_docker()
        lab, traffic, baseline = lab_state()
        report['docker_budget'] = json.loads(command(['docker','info','--format','{"memory_bytes":{{.MemTotal}},"cpus":{{.NCPU}}}']))
        report['resources_before'] = resources()
        before_collection=collection(lab,baseline)
        with Load(traffic,args.workers,args.rps) as load:
            load.mark('steady_load')
            started=time.monotonic()
            time.sleep(args.seconds)
            wait_for('metrics under sustained load',lambda:fresh(lab.api,baseline))
            report['request_collection_delta']=collection_delta(before_collection,collection(lab,baseline))
            report['steady_seconds']=time.monotonic()-started
            if args.live_metrics:
                load.mark('live_metrics_under_load')
                report['live_metrics_seconds'] = latency()
            if args.mode in ('all','failures'):
                report['failures'] = failures(lab,traffic,baseline,load,args.outage_seconds)
            if args.mode in ('all','restore'):
                load.mark('checkpoint_source_traffic')
                started = time.monotonic()
                checkpoint(work/'checkpoint',baseline)
                report['checkpoint_seconds'] = round(time.monotonic()-started,3)
                wait_for('source after checkpoint',lambda:fresh(lab.api,baseline))
                load.mark('restore_source_traffic')
                report['restore'] = restore(work/'checkpoint',work/'restore',baseline,lab.context,args.restore_context)
            load.mark('final_traffic')
            time.sleep(2)
        report['traffic'] = load.results()
        for phase,row in report['traffic'].items():
            if phase != 'node_crash_detection' and (not row['success'] or row['failures'] or row['p95_ms']>args.max_p95_ms):
                raise RuntimeError('Serving error or latency budget exceeded in '+phase)
        steady = report['traffic']['steady_load']
        achieved = steady['success']/report['steady_seconds']
        report['achieved_rps'] = round(achieved,2)
        if achieved < args.rps*.8 or len(steady['routes']) != 8:
            raise RuntimeError('Load target or two-app/two-node HTTP/HTTPS coverage was not achieved')
        wait_for('source final health',lambda:fresh(lab.api,baseline))
        if sorted(n['id'] for n in lab.api.request('/v1/nodes')['nodes']) != baseline['node_ids'] or lab.revisions()!=baseline['history']:
            raise RuntimeError('Source enrollment identities or application history changed')
        report['resources_after'] = resources()
        report['status'] = 'passed'
    except BaseException as error:
        report['status'] = 'failed'
        report['error'] = type(error).__name__
        report['failure_phase'] = load.phase if 'load' in locals() else 'preflight'
        if 'load' in locals(): report['traffic'] = load.results()
        raise
    finally:
        # Never retain plaintext backup keys by default. Keep only sanitized evidence.
        if not (work/'restore/active').exists():
            shutil.rmtree(work)
        else:
            report['cleanup_required'] = str(work)
        write_private(args.report or root/'latest.json',json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2),flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--restore-context',help='separate local Unix-socket Docker context for the disposable restore')
    parser.add_argument('mode',choices=('all','load','failures','restore'),nargs='?',default='all')
    parser.add_argument('--seconds',type=int,default=30)
    parser.add_argument('--rps',type=int,default=80)
    parser.add_argument('--workers',type=int,default=8)
    parser.add_argument('--outage-seconds',type=int,default=5)
    parser.add_argument('--max-p95-ms',type=int,default=2000)
    parser.add_argument('--live-metrics',action='store_true',help='also enforce the independent five-second SSE marker gate under load')
    parser.add_argument('--report',type=Path)
    args = parser.parse_args()
    if not (5<=args.seconds<=3600 and 4<=args.rps<=2000 and 1<=args.workers<=64 and 2<=args.outage_seconds<=60 and 1<=args.max_p95_ms<=30_000):
        parser.error('Duration, workload or budget is outside the supported bounded range')
    run(args)


if __name__=='__main__':
    main()
