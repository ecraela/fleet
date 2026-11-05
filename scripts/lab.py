"""Locate services in a source checkout's local Compose lab."""
import json
from pathlib import Path
import subprocess


def container(root, service):
    root = Path(root).resolve()
    result = subprocess.run([
        'docker', 'compose', '--project-directory', str(root),
        '--env-file', str(root / 'deploy/local/state.local/compose.env'),
        '-f', str(root / 'compose.yaml'), 'ps', '-q', service,
    ], check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    ids = result.stdout.decode().split()
    if len(ids) != 1:
        raise RuntimeError(f'Expected one running demo {service} service; prepare the local lab first')
    return ids[0]


def network(root):
    manager = container(root, 'manager')
    result = subprocess.run([
        'docker', 'inspect', manager, '--format', '{{json .NetworkSettings.Networks}}',
    ], check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    networks = json.loads(result.stdout)
    if not isinstance(networks, dict) or len(networks) != 1:
        raise RuntimeError('The demo manager must use exactly one Docker network')
    return next(iter(networks))
