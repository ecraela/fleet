#!/usr/bin/env python3
"""Private per-application logs/analytics on the real HTTP/HTTPS pair."""
from concurrent.futures import ThreadPoolExecutor
import copy
import json
import ssl
import sys
from threading import Event
import time
import urllib.error
import urllib.parse
import uuid

from demo import wait_for, write_private
from listeners import Listeners, local_dpa
from pair import compose, selected_state

LOG_FORMAT = '"cv1 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts"'
PATH_LOG_FORMAT = '"cv2 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex]"'
LEGACY_GEO_LOG_FORMAT = '"cv3 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[fc_src] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"'
GEO_LOG_FORMAT = '"cv4 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"'

TRAFFIC_LOG_FORMAT = GEO_LOG_FORMAT.replace('"cv4 ', '"cv5 ', 1)[:-1] + ' %[var(txn.cv_rule)] %[var(txn.cv_decision)]"'


def maintenance(lab, node, action):
    path = f"/v1/nodes/{node['id']}/maintenance"
    def fresh():
        v = lab.api.request(path)
        d = v['controller'].get('discovery') or {}
        cfg = d.get('configuration') or {}
        target = v['target'].get('discovery') or {}
        healthy = action != 'rejoin' or ((target.get('configuration') or {}).get('document') and not (target.get('configuration') or {}).get('error_code') and not target.get('error_code') and time.time()*1000-target.get('observed_at_ms',0)<5000 and all(s['health']=='UP' and s.get('check_status','').removeprefix('* ') in ('L4OK','L6OK','L7OK','L7OKC') for s in target.get('servers',[])))
        return v if not v['reason_code'] and cfg.get('document') and time.time()*1000-cfg.get('observed_at_ms', 0) < 5000 and healthy else None
    def submit():
        v = fresh()
        if not v: return False
        target, control = v['target']['discovery'], v['controller']['discovery']
        body = dict(request_id=str(uuid.uuid4()), action=action, binding_id=v['binding_id'], expected_generation=target['generation'], expected_entrypoint_generation=control['generation'], expected_artifact_id=control['configuration']['artifact_id'], expected_version=control['configuration']['version'], drain_timeout_seconds=30)
        current = v.get('maintenance')
        if current and current['status'] not in ('completed', 'failed'):
            body['maintenance_id'] = current['id']
        try:
            lab.api.request(path, body)
        except urllib.error.HTTPError as error:
            if error.code != 409: raise
            error.close()
            # A restart/discovery can advance between the read and write. Only
            # an unaccepted conflict is retried, with freshly bound evidence.
            return False
        return True
    wait_for(node['name']+' maintenance request accepted', submit, seconds=45)
    def done():
        m = lab.api.request(path).get('maintenance')
        if m and m['status'] in ('attention', 'failed'):
            raise RuntimeError('Maintenance needs attention: '+m['error_code'])
        return m if m and m['status'] == ('maintenance' if action == 'withdraw' else 'completed') else None
    wait_for(node['name']+' '+action, done, seconds=90)


def prepare_logging(lab, paths=False):
    """Local lab setup through DPA; never replace files or another log profile."""
    for cfg in lab.inventory():
        apps = (cfg.get('document') or {}).get('applications', [])
        if len(apps) != 2 or {a['name'] for a in apps} != {'Store demo', 'Admin demo'} or any(a['hostname'] != 'apps.demo.test' for a in apps):
            raise RuntimeError('Preserve custom applications before running the request demo')
    for letter in ('a', 'b'):
        node = lab.nodes['haproxy-'+letter]
        f, _ = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true', letter=letter)
        logs, headers = local_dpa('/services/haproxy/configuration/global/log_targets', letter=letter)
        target = dict(address='/run/haproxy/access.sock', facility='local0', format='raw', length=2048)
        private_targets = [dict(nolog=True), target]
        existing = [v for v in logs if v.get('address') == target['address']]
        desired_format = TRAFFIC_LOG_FORMAT if f.get('log_format') == TRAFFIC_LOG_FORMAT else GEO_LOG_FORMAT if f.get('log_format') == GEO_LOG_FORMAT else PATH_LOG_FORMAT if paths or f.get('log_format') == PATH_LOG_FORMAT else LOG_FORMAT
        capture = dict(type='set-var', var_scope='txn', var_name='cv_path', var_expr='path')
        captures = [r for r in f.get('http_request_rule_list', []) if r.get('var_name') == 'cv_path']
        if captures and captures != [capture]:
            raise RuntimeError('Preserve the customized local path capture rule')
        if paths and f.get('log_target_list') not in (None, [], [target], private_targets, [{'global': True}]):
            raise RuntimeError('Preserve customized frontend log destinations')
        if f.get('log_format') == desired_format and existing == [target] and (not paths or captures == [capture] and f.get('log_target_list') == private_targets):
            continue
        if f.get('log_format') not in (None, '', LOG_FORMAT, PATH_LOG_FORMAT, GEO_LOG_FORMAT, LEGACY_GEO_LOG_FORMAT, TRAFFIC_LOG_FORMAT) or existing and existing != [target]:
            raise RuntimeError('Preserve the node’s customized logging profile')
        maintenance(lab, node, 'withdraw')
        try:
            transaction, _ = local_dpa('/services/haproxy/transactions?version='+headers['configuration-version'], {}, 'POST', letter=letter)
            tid = transaction['id']
            committed = False
            try:
                if not existing:
                    local_dpa('/services/haproxy/configuration/global/log_targets?transaction_id='+tid, logs+[target], 'PUT', letter=letter)
                f['log_format'] = desired_format
                if paths:
                    if not captures:
                        f['http_request_rule_list'] = f.get('http_request_rule_list', []) + [capture]
                    f['log_target_list'] = private_targets
                local_dpa('/services/haproxy/configuration/frontends/public?full_section=true&transaction_id='+tid, f, 'PUT', letter=letter)
                _, response_headers = local_dpa('/services/haproxy/transactions/'+tid+'?force_reload=true', method='PUT', letter=letter)
                committed = True
                if reload_id := response_headers.get('reload-id'):
                    wait_for('logging profile reload', lambda: local_dpa('/services/haproxy/reloads/'+reload_id, letter=letter)[0]['status'] == 'succeeded')
            finally:
                if not committed:
                    local_dpa('/services/haproxy/transactions/'+tid, method='DELETE', letter=letter)
            _, committed_headers = local_dpa('/services/haproxy/configuration/frontends/public?full_section=true', letter=letter)
            committed_version = int(committed_headers['configuration-version'])
            def discovered():
                cfg = lab.api.request(f"/v1/nodes/{node['id']}/configuration")['control']['discovery']['configuration']
                return cfg.get('request_logs_allowed') and cfg.get('document') and not cfg.get('error_code') and cfg.get('version', 0) >= committed_version
            wait_for('node logging capability', discovered)
        finally:
            maintenance(lab, node, 'rejoin')
        print('PASS: withdrew, drained, installed the private logging profile and rejoined '+node['name']+'.', flush=True)


def run():
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    trust = selected_state()/'acme/public/trust.pem'
    if trust.exists(): lab.context = ssl.create_default_context(cafile=trust)
    def effective(name):
        rows = [r for r in lab.revisions() if r['spec']['name'] == name]
        latest = max(rows, key=lambda r:r['number'])
        source = max((r for r in rows if r['status'] == 'succeeded'), key=lambda r:r['number']) if latest['status'] in ('rolled_back','rollback_failed') else latest
        return latest, source
    a, source_a = effective('Store demo')
    b, source_b = effective('Admin demo')
    if source_a['spec']['path_prefix'] != '/shop' or source_b['spec']['path_prefix'] != '/shop/admin/v2':
        raise RuntimeError('This test requires the current synthetic two-app lab')
    before = lab.inventory()
    stop, failures = Event(), []
    traffic_count = 0
    evidence = {}
    def continuous():
        nonlocal traffic_count
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users', 'app-b')
                lab.https('/shop/admin/v2/users', backend='app-b')
                traffic_count += 2
            except Exception as e: failures.append(type(e).__name__)
            stop.wait(.05)
    def query(app, kind, **filters):
        return lab.api.request(lab.path+'/'+app['application_id']+'/'+kind+'?'+urllib.parse.urlencode(dict(range='15m', **filters)))
    with ThreadPoolExecutor(max_workers=1) as pool:
        running = pool.submit(continuous)
        try:
            prepare_logging(lab)
            if source_a['spec'].get('request_telemetry'):
                stopped_spec = copy.deepcopy(source_a['spec'])
                stopped_spec['request_telemetry'] = False
                stopped = lab.start(stopped_spec,a)
                lab.complete(stopped)
                a, source_a = effective('Store demo')
                print('PASS: disabling collection rolled across the nodes while retaining request history.',flush=True)
            for latest, source in ((a,source_a),(b,source_b)):
                spec = copy.deepcopy(source['spec'])
                spec['request_telemetry'] = True
                deployed = lab.start(spec, latest)
                lab.complete(deployed)
                print('PASS: opted '+spec['name']+' into telemetry with a verified rolling deployment.',flush=True)
            a, _ = effective('Store demo');b, _ = effective('Admin demo')
            time.sleep(2)  # Let the one-second log-store flush index warm-up probes.
            starting_requests = query(a,'traffic')['summary']['requests']
            starting_b_errors = query(b,'traffic')['summary']['classes'].get('5xx',0)
            for _ in range(24):
                lab.request('/shop/items?delay=120&token=synthetic-request-secret', 'app-a')
                lab.https('/shop/items?status=503&delay=120&token=synthetic-request-secret', status=503)
            def observed():
                t = query(a,'traffic')
                return t if t['summary']['requests'] >= starting_requests+48 and t['summary']['classes'].get('5xx',0) >= 24 and len(t['points']) >= 2 else None
            t = wait_for('real per-app request statistics', observed)
            slow = query(a,'traffic',min_ms=100)
            if not slow['summary']['p95_ms'] >= 100 or not all(c['fresh'] for c in t['collection']):
                raise RuntimeError('Request percentiles or collection status did not reflect traffic')
            evidence['traffic'] = t
            evidence['slow_traffic'] = slow
            p = wait_for('filtered HTTPS failures', lambda: (v if (v:=query(a,'requests',scheme='https',status_class='5xx',min_ms=100))['requests'] else None))
            if any(r['status'] != 503 or r['duration_ms'] < 100 or r['application_id'] != a['application_id'] or r['scheme'] != 'https' for r in p['requests']):
                raise RuntimeError('Request filters leaked another application or protocol')
            if any(not r['matching_versions'] for r in p['requests']):
                raise RuntimeError('Request fingerprint did not correlate with retained versions')
            if 'synthetic-request-secret' in json.dumps(p) or any(k in r for r in p['requests'] for k in ('url','query','headers','body','authorization','client_ip')):
                raise RuntimeError('Private request content reached the query API')
            # A preconfigured sanitized path policy can publish this fixed route.
            # Keep rejecting arbitrary/raw paths and every query/header/body field.
            if any(r.get('path') and (r['path'] != '/shop/items' or r.get('path_kind') != 'template') for r in p['requests']):
                raise RuntimeError('Unrecognized request path reached the query API')
            evidence['requests'] = p
            first = query(a,'requests')
            if not first['next_cursor']: raise RuntimeError('Request search did not paginate')
            second = query(a,'requests',end=first['end_ms'],before=first['next_cursor'])
            if not second['requests'] or set(r['id'] for r in first['requests']) & set(r['id'] for r in second['requests']):
                raise RuntimeError('Keyset pagination duplicated or lost its next page')
            for node in lab.nodes.values():
                rows = query(a,'requests',node=node['id'])['requests']
                if not rows or any(r['node_id'] != node['id'] for r in rows): raise RuntimeError('Node filter failed')
            bt = query(b,'traffic')
            if bt['summary']['classes'].get('5xx',0) != starting_b_errors or bt['summary']['requests'] == 0:
                raise RuntimeError('App A failures contaminated app B analytics')
            print('PASS: HTTP/HTTPS app attribution, measured latency, filtered failures, pagination and configuration/version correlation; no raw request secrets.',flush=True)
            compose('stop','-t','2','victorialogs')
            try:
                time.sleep(4)
                try: query(a,'traffic')
                except urllib.error.HTTPError as error:
                    if error.code != 503: raise
                else: raise RuntimeError('Missing log storage was presented as healthy analytics')
                for node in lab.nodes.values():
                    metrics = lab.api.request('/v1/nodes/'+node['id']+'/metrics')
                    if not metrics['fresh'] or not metrics['scrape_ok']: raise RuntimeError('Log storage outage starved live metrics')
            finally: compose('start','victorialogs')
            def recovered():
                v = query(a,'traffic')
                return v if all(c['fresh'] for c in v['collection']) and any(c['pipeline']['unconfirmed'] for c in v['collection']) else None
            evidence['recovered'] = wait_for('visible loss counters and recovered collection',recovered,seconds=45)
            compose('restart','manager')
            wait_for('manager restart',lambda:lab.api.request('/healthz',authorized=False))
            retained = wait_for('retained request history',lambda:query(a,'requests'))
            if not retained['requests']: raise RuntimeError('Manager restart lost request history')
            after = lab.inventory()
            for old,new in zip(before,after):
                expected = copy.deepcopy(old['document'])
                for app in expected['applications']: app['request_telemetry'] = True
                if expected != new['document']: raise RuntimeError('Request collection changed routing, shared listeners or TLS')
            print('PASS: log-store failure leaves HTTP/HTTPS serving, metrics and control independent; delivery ambiguity is visible and request history survives manager restart.',flush=True)
        finally:
            compose('start','victorialogs')
            stop.set();running.result()
    if failures: raise RuntimeError(f'{len(failures)} app B request failures: {failures[:3]}')
    evidence['app_b_successful_requests'] = traffic_count
    evidence['app_b_failures'] = 0
    write_private(selected_state() / 'requests-evidence.json', json.dumps(evidence,indent=2)+'\n')
    print(f'PASS: {traffic_count} continuous app B HTTP/HTTPS requests, 0 failures; both apps retain telemetry, routing and shared TLS.',flush=True)


if __name__ == '__main__':
    run()
