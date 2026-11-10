#!/usr/bin/env python3
"""Build exact-version, reproducible Linux release files; signing is separate."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from agentctl import validate_compatibility

from legal import FILES as LEGAL_FILES, check as check_legal

ROOT = Path(__file__).resolve().parents[1]


def load_compatibility(path, version):
    value = json.loads(Path(path).read_text())
    if (not isinstance(value, dict) or set(value) != {'version', 'manager', 'agent'} or
            value.get('version') != version):
        raise ValueError('A reviewed compatibility entry for this exact release is required')
    return validate_compatibility({k: v for k, v in value.items() if k != 'version'}, version)


def build(output, version, revision, image, compatibility_file):
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.-]+)?', version):
        raise ValueError('An exact release tag is required')
    if not re.fullmatch(r'[a-f0-9]{40}', revision):
        raise ValueError('An exact source revision is required')
    if not re.fullmatch(r'(?:ghcr\.io/ecraela/fleet@)?sha256:[a-f0-9]{64}', image):
        raise ValueError('An immutable Docker digest is required')
    if subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip() != revision:
        raise ValueError('Release revision must match the source checkout')
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'], cwd=ROOT, text=True).strip():
        raise ValueError('Commit and review source changes before building a release')
    check_legal()
    compatibility = load_compatibility(compatibility_file, version)
    # Native artifacts and Docker images must embed the same console bundle.
    subprocess.run(['pnpm', 'build'], cwd=ROOT / 'web', check=True)
    output.mkdir(parents=True, exist_ok=False)
    module = subprocess.check_output(['go', 'list', '-m'], cwd=ROOT, text=True).strip()
    flags = f'-s -w -X {module}/internal/buildinfo.Version={version} -X {module}/internal/buildinfo.Revision={revision}'
    for arch in ('amd64', 'arm64'):
        env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
        for name in ('agent', 'manager'):
            subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags', flags,
                            '-o', str(output / f'haproxy-{name}-linux-{arch}'), f'./cmd/haproxy-{name}'],
                           cwd=ROOT, env=env, check=True)
    shutil.copyfile(ROOT / 'scripts/agentctl.py', output / 'agentctl.py')
    shutil.copyfile(ROOT / 'scripts/selfhostctl.py', output / 'selfhostctl.py')
    shutil.copyfile(ROOT / 'deploy/selfhost.json', output / 'selfhost.json')
    for name in LEGAL_FILES:
        shutil.copyfile(ROOT / 'internal/webui/dist/legal' / name, output / name)
    manifest = {'schema': 1, 'repository': 'ecraela/fleet', 'version': version,
                'revision': revision, 'protocol': 1, 'state_schema': 1, 'image': image,
                'compatibility': compatibility, 'managed_upgrades': True,
                'artifacts': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(output.iterdir())}}
    (output / 'release.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--version', required=True)
    p.add_argument('--revision', required=True)
    p.add_argument('--image', required=True)
    p.add_argument('--compatibility-file', type=Path, required=True)
    args = p.parse_args()
    build(args.output.resolve(), args.version, args.revision, args.image, args.compatibility_file)
