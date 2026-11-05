#!/usr/bin/env python3
"""Prepare the release lifecycle fixture; full feature qualification runs separately."""
import acme
import subprocess
import sys
from applications import Applications, spec
from listeners import Listeners, migrate_entrypoint
from pair import ROOT


def run():
    subprocess.run([sys.executable, str(ROOT / 'scripts/pair.py'), 'up'], check=True)
    acme.up()
    app = Applications()
    for name, path, backend in (
        ('Store demo', '/shop', 'pair-backend'),
        ('Admin demo', '/shop/admin/v2', 'pair-backend-2'),
    ):
        app.complete(app.start(spec(name, path, backend), app.latest(name)))
    app.verify('/shop/items', 'app-a')
    app.verify('/shop/admin/v2/users', 'app-b')
    migrate_entrypoint()
    tls = Listeners()
    tls.complete(tls.start_tls('demo-v1'))
    tls.verify_tls('demo-v1')
    plan = acme.action('enable')
    acme.verify(acme.ready(plan['generation']))


if __name__ == '__main__':
    run()
