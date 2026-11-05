#!/usr/bin/env python3
"""Route-pattern analytics on the private two-node HTTP/HTTPS demo."""
from concurrent.futures import ThreadPoolExecutor
import json
import ssl
import threading
import time
import urllib.parse

from demo import wait_for, write_private
from listeners import Listeners
from pair import selected_state
from requests import prepare_logging


def run():
    lab = Listeners()
    lab.path = f'/v1/clusters/{lab.cluster}/applications'
    trust = selected_state()/'acme/public/trust.pem'
    if trust.exists():
        lab.context = ssl.create_default_context(cafile=trust)
    a, b = lab.latest('Store demo'), lab.latest('Admin demo')
    if not a or not b or any(not v['spec'].get('request_telemetry') or v['status'] != 'succeeded' for v in (a, b)):
        raise RuntimeError('Run python3 scripts/requests.py to enable the two synthetic apps first')
    before = lab.inventory()
    stop, failures = threading.Event(), []
    count = 0

    def continuous():
        nonlocal count
        while not stop.is_set():
            try:
                lab.request('/shop/admin/v2/users', 'app-b')
                lab.https('/shop/admin/v2/users', backend='app-b')
                count += 2
            except Exception as e:
                failures.append(type(e).__name__)
            stop.wait(.05)

    def query(app, kind='traffic', **filters):
        return lab.api.request(lab.path+'/'+app['application_id']+'/'+kind+'?'+urllib.parse.urlencode(dict(range='15m', **filters)))

    with ThreadPoolExecutor(max_workers=1) as pool:
        traffic = pool.submit(continuous)
        try:
            prepare_logging(lab, paths=True)
            wait_for('local sanitized policy on both nodes', lambda: all(c['fresh'] and c.get('pipeline',{}).get('path_mode') == 'sanitized' for c in query(a)['collection']))
            baseline = query(a, path='/shop/items/:id')['summary']
            per_node = {n['id']:query(a,path='/shop/items/:id',node=n['id'])['summary']['requests'] for n in lab.nodes.values()}
            started = int(time.time()*1000)
            for _ in range(12):
                lab.request('/shop/items/private-user-987?token=private-query-marker', 'app-a')
                lab.https('/shop/items/private-user-987?status=503&delay=120&token=private-query-marker', status=503)
                lab.request('/shop/not-configured/private-secret', 'app-a')
            def collected():
                t = query(a, path='/shop/items/:id')
                return t if t['summary']['requests'] >= baseline['requests']+24 and t['summary']['classes'].get('5xx',0) >= baseline['classes'].get('5xx',0)+12 else None
            result = wait_for('normalized HTTP/HTTPS path analytics', collected)
            wait_for('fresh path records from each node', lambda: all(query(a,path='/shop/items/:id',node=node)['summary']['requests'] > previous for node,previous in per_node.items()))
            if result['summary']['p95_ms'] < 100:
                raise RuntimeError('Path percentiles did not reflect injected latency')
            failures_page = query(a,'requests', path='/shop/items/:id',scheme='https',status_class='5xx')
            if not failures_page['requests'] or any(r.get('path') != '/shop/items/:id' or r.get('path_kind') != 'template' or r['status'] != 503 for r in failures_page['requests']):
                raise RuntimeError('Path search mixed routes or protocols')
            for rank in ('requests','errors','latency'):
                top = query(a,path_order=rank)
                if not any(p['path'] == '/shop/items/:id' for p in top['paths']) or len(top['paths']) > 10:
                    raise RuntimeError('Ranked path summaries missing the normalized route')
                values = [p['requests'] if rank == 'requests' else p['classes'].get('5xx',0) if rank == 'errors' else p['p95_ms'] for p in top['paths']]
                if values != sorted(values, reverse=True):
                    raise RuntimeError('Path summaries are not ranked in the requested order')
            def withheld():
                p = query(a, 'requests')
                return p if any(r['at_ms'] >= started and r.get('path_kind') == 'unmatched' and not r.get('path') for r in p['requests']) else None
            hidden = wait_for('unmatched paths withheld', withheld)
            raw = json.dumps([result,failures_page,hidden,query(a),query(b)])
            if any(s in raw for s in ('private-user-987','private-query-marker','private-secret')):
                raise RuntimeError('Private URL content escaped the local path policy')
            page = query(a,'requests',path='/shop/items/:id')
            if not page['next_cursor']:
                raise RuntimeError('Expected normalized path pagination')
            older = query(a,'requests',path='/shop/items/:id',end=page['end_ms'],before=page['next_cursor'])
            if {r['id'] for r in page['requests']} & {r['id'] for r in older['requests']} or any(r.get('path') != '/shop/items/:id' for r in older['requests']):
                raise RuntimeError('Path pagination repeated or mixed records')
            b_requests = query(b,'requests',path='/shop/admin/v2/users')
            if not b_requests['requests'] or any(r['application_id'] != b['application_id'] for r in b_requests['requests']):
                raise RuntimeError('App B paths were not isolated')
            after = lab.inventory()
            for old,new in zip(before,after):
                if old['document'] != new['document'] or old.get('listener_bindings') != new.get('listener_bindings'):
                    raise RuntimeError('Application routing or shared TLS changed during path setup')
            write_private(selected_state() / 'paths-evidence.json', json.dumps(dict(traffic=result,requests=failures_page,app_b_successful_requests=count,app_b_failures=len(failures)),indent=2)+'\n')
            print('PASS: HTTP/HTTPS route patterns, rankings, filters, request details and paging; query strings and dynamic/unmatched secrets withheld locally.',flush=True)
        finally:
            stop.set()
            traffic.result(timeout=10)
    if failures:
        raise RuntimeError(f'{len(failures)} app B serving failures')
    print(f'PASS: {count} continuous app B HTTP/HTTPS requests, zero failures; shared routing and TLS preserved.',flush=True)


if __name__ == '__main__':
    run()
