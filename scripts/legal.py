#!/usr/bin/env python3
"""Collect reviewed runtime notices and build a deterministic, local source offer."""
import argparse
import gzip
import hashlib
import html
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
INPUTS = ('go.mod', 'go.sum', 'web/package.json', 'web/pnpm-lock.yaml')
FILES = ('LICENSE', 'THIRD_PARTY_NOTICES.txt', 'dependencies.json', 'source.tar.gz', 'source.json')
NOTICE = re.compile(r'^(?:licen[cs]e|copying|notice|patents|copyright)(?:[._-].*)?$', re.I)
GO_LICENSES = {
    'github.com/oschwald/maxminddb-golang/v2': 'ISC',
    'github.com/cespare/xxhash/v2': 'MIT',
    **{name: 'MIT' for name in ('github.com/jackc/pgx/v5', 'github.com/jackc/pgpassfile',
                               'github.com/jackc/pgservicefile', 'github.com/jackc/puddle/v2')},
    **{name: 'Apache-2.0' for name in ('google.golang.org/grpc', 'google.golang.org/genproto/googleapis/rpc',
                                      'github.com/prometheus/client_model', 'github.com/prometheus/common',
                                      'github.com/prometheus/prometheus')},
    **{name: 'BSD-3-Clause' for name in ('golang.org/x/crypto', 'golang.org/x/net', 'golang.org/x/sys',
                                        'golang.org/x/text', 'golang.org/x/sync', 'google.golang.org/protobuf',
                                        'github.com/golang/snappy', 'github.com/munnerz/goautoneg',
                                        'github.com/gogo/protobuf', 'github.com/grafana/regexp', 'github.com/miekg/dns')},
}
NPM_LICENSES = {'MIT', 'Apache-2.0', '0BSD', 'OFL-1.1'}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def dump(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + '\n')


def objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        yield value
        raw = raw.lstrip()[end:]


def run(args, root=ROOT, **kwargs):
    return subprocess.check_output(args, cwd=root, text=True, **kwargs)


def npm_package(name, start):
    for parent in (start, *start.parents):
        path = parent / 'node_modules' / name / 'package.json'
        if path.is_file():
            return path.resolve()
    raise ValueError(f'Missing installed dependency: {name}; run make web-install')


def npm_runtime(root):
    found = {}

    def visit(name, start):
        path = npm_package(name, start)
        package = json.loads(path.read_text())
        key = package['name'] + '@' + package['version']
        if key in found:
            return
        if package.get('license') not in NPM_LICENSES:
            raise ValueError(f'Review the license of {key} before distribution')
        found[key] = (package, path.parent)
        for child in sorted(set(package.get('dependencies', {})) | set(package.get('peerDependencies', {}))):
            try:
                visit(child, path.parent)
            except ValueError:
                if not package.get('peerDependenciesMeta', {}).get(child, {}).get('optional'):
                    raise

    for name in json.loads((root / 'web/package.json').read_text())['dependencies']:
        visit(name, root / 'web')
    return found


def notices(directory):
    return sorted(p for p in directory.rglob('*') if p.is_file() and NOTICE.fullmatch(p.name)
                  and 'node_modules' not in p.relative_to(directory).parts)


def collect(root=ROOT):
    """Explicit update command; build/check never silently approves changed dependencies."""
    legal = root / 'legal'
    target = legal / 'notices'
    target.mkdir(parents=True, exist_ok=True)
    components = []

    def component(ecosystem, name, version, license_id, directory, files, source):
        if not files:
            raise ValueError(f'Missing license text: {name}@{version}')
        row = dict(ecosystem=ecosystem, name=name, version=version, license=license_id,
                   source=source, notices=[])
        for file in files:
            data = file.read_bytes()
            data.decode('utf-8')
            digest = sha(data)
            (target / (digest + '.txt')).write_bytes(data)
            row['notices'].append({'file': 'notices/' + digest + '.txt',
                                   'upstream_path': str(file.relative_to(directory)), 'sha256': digest})
        components.append(row)

    for key, (package, directory) in sorted(npm_runtime(root).items()):
        files = notices(directory)
        source = 'https://registry.npmjs.org/' + package['name'] + '/-/' + package['name'].split('/')[-1] + '-' + package['version'] + '.tgz'
        if key == 'client-only@0.0.1' and not files:
            # This empty React marker declares MIT but omits its license file.
            # Preserve the upstream React notice rather than inventing one.
            directory = npm_package('react', root / 'web').parent
            files = [directory / 'LICENSE']
        component('npm', package['name'], package['version'], package['license'], directory, files, source)

    modules = {}
    for arch in ('amd64', 'arm64'):
        env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
        for package in objects(run(['go', 'list', '-deps', '-json', './cmd/haproxy-manager',
                                    './cmd/haproxy-agent', './deploy/local'], root, env=env)):
            module = package.get('Module', {})
            if module and not module.get('Main'):
                if module.get('Replace'):
                    raise ValueError('Review replacement modules before distribution')
                modules[module['Path']] = module
    for name, module in sorted(modules.items()):
        if name not in GO_LICENSES:
            raise ValueError(f'Review the license of Go module {name}')
        directory = Path(module['Dir'])
        component('go', name, module['Version'], GO_LICENSES[name], directory, notices(directory),
                  'https://pkg.go.dev/' + name + '@' + module['Version'])
    goroot = Path(run(['go', 'env', 'GOROOT'], root).strip())
    component('go', 'Go runtime and standard library', run(['go', 'env', 'GOVERSION'], root).strip(),
              'BSD-3-Clause', goroot, [goroot / 'LICENSE', goroot / 'PATENTS', *notices(goroot / 'src')],
              'https://go.dev/dl/')
    component('asset', 'flag-icons', '7.5.0', 'MIT', root / 'web/public/flags',
              [root / 'web/public/flags/LICENSE.txt'], 'https://github.com/lipis/flag-icons/tree/v7.5.0')
    component('fixture', 'MaxMind test databases', 'repository fixtures', 'MIT',
              root / 'internal/accesslog/testdata', [root / 'internal/accesslog/testdata/LICENSE-MaxMind'],
              'https://github.com/maxmind/MaxMind-DB')
    manifest = dict(schema=1, scope='runtime dependency closure, included assets and source fixtures',
                    inputs={p: sha((root / p).read_bytes()) for p in INPUTS}, components=components)
    dump(legal / 'dependencies.json', manifest)
    source_files(root, update=True)
    return manifest


def safe_source(name):
    if name == 'internal/webui/dist/.keep':
        return True
    if name == 'web/public/legal' or name.startswith('web/public/legal/'):
        return False
    path = PurePosixPath(name)
    if path.is_absolute() or '..' in path.parts or not path.parts:
        return False
    if any(p.startswith('.') and p not in {'.github', '.gitignore', '.dockerignore', '.prettierignore', '.keep'}
           for p in path.parts):
        return False
    if any(p in {'node_modules', '__pycache__', 'dist', 'coverage'} or p.endswith('.local') for p in path.parts):
        return False
    if path.suffix in {'.key', '.pem', '.log', '.pyc', '.tsbuildinfo'}:
        return False
    return True


def source_files(root, update=False):
    path = root / 'legal/source-files.json'
    if update:
        # Review this explicit list in Git. Docker builds have no Git directory
        # and never discover files by walking a customer/developer workspace.
        names = run(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'], root).split('\0')
        names = sorted({p for p in names if p and safe_source(p)} | {'legal/source-files.json'})
        dump(path, names)
    names = json.loads(path.read_text())
    if not isinstance(names, list) or len(names) > 4096 or names != sorted(set(names)):
        raise ValueError('Invalid source file manifest')
    for name in names:
        file = root / name
        if not safe_source(name) or file.is_symlink() or not file.is_file() or file.stat().st_size > 4 << 20:
            raise ValueError(f'Unsafe or missing source file: {name}')
        if not file.resolve().is_relative_to(root.resolve()):
            raise ValueError(f'Source path escaped repository: {name}')
    if (root / '.git').exists():
        expected = {p for p in run(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'], root).split('\0')
                    if p and safe_source(p)}
    else:
        expected = set()
        for folder in ('cmd', 'internal', 'web', 'scripts', 'deploy', 'proto', 'docs', 'legal'):
            for directory, dirs, files in os.walk(root / folder, followlinks=False):
                dirs[:] = [d for d in dirs if safe_source(str((Path(directory) / d).relative_to(root)))]
                for name in files:
                    name = str((Path(directory) / name).relative_to(root))
                    if safe_source(name):
                        expected.add(name)
    if expected - set(names):
        raise ValueError('Source files changed; review and regenerate legal/source-files.json: ' +
                         ', '.join(sorted(expected - set(names))[:5]))
    return names


def check(root=ROOT):
    manifest = json.loads((root / 'legal/dependencies.json').read_text())
    if manifest['inputs'] != {p: sha((root / p).read_bytes()) for p in INPUTS}:
        raise ValueError('Dependencies changed; review and regenerate legal notices with make legal-update')
    if json.loads((root / 'web/package.json').read_text()).get('license') != 'AGPL-3.0-only':
        raise ValueError('Unexpected project license')
    if sha((root / 'LICENSE').read_bytes()) != 'd8a6cc31abc16b6748c7a21f21611f5a1ec33f67d22ca23d7da1c19b95496bee':
        raise ValueError('The selected AGPL-3.0-only license text changed')
    for row in manifest['components']:
        for notice in row['notices']:
            if not re.fullmatch(r'notices/[a-f0-9]{64}\.txt', notice['file']):
                raise ValueError('Invalid notice path')
            if sha((root / 'legal' / notice['file']).read_bytes()) != notice['sha256']:
                raise ValueError(f"Notice changed: {row['name']}")
    source_files(root)
    return manifest


def bundle(output, root=ROOT):
    manifest = check(root)
    output.mkdir(parents=True, exist_ok=True)
    (output / 'LICENSE').write_bytes((root / 'LICENSE').read_bytes())
    (output / 'dependencies.json').write_bytes((root / 'legal/dependencies.json').read_bytes())
    sections = ['Third-party notices for Ecraela Fleet\n\nThird-party terms remain separate from AGPL-3.0-only.\n']
    for row in manifest['components']:
        sections.append(f"\n{'=' * 72}\n{row['name']} {row['version']} ({row['license']})\n{row['source']}\n")
        for notice in row['notices']:
            sections.append('\n' + notice['upstream_path'] + '\n' +
                            (root / 'legal' / notice['file']).read_text())
    (output / 'THIRD_PARTY_NOTICES.txt').write_text('\n'.join(sections) + '\n')
    info = {}
    archive = io.BytesIO()
    with gzip.GzipFile(fileobj=archive, mode='wb', filename='', mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as tar:
            for name in source_files(root):
                data = (root / name).read_bytes()
                info[name] = sha(data)
                entry = tarfile.TarInfo('haproxy-manager/' + name)
                entry.size, entry.mtime = len(data), 0
                entry.mode = 0o755 if (root / name).stat().st_mode & 0o111 else 0o644
                tar.addfile(entry, io.BytesIO(data))
    (output / 'source.tar.gz').write_bytes(archive.getvalue())
    dump(output / 'source.json', {'schema': 1, 'sha256': sha(archive.getvalue()), 'files': info})
    (output / 'index.html').write_text('''<!doctype html><html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Ecraela Fleet — License and source</title>
<style>html{color-scheme:light dark;font:16px/1.6 system-ui}body{max-width:54rem;margin:3rem auto;padding:0 1.5rem}li{margin:.6rem 0}a{color:light-dark(#0055b3,#80b7ff)}</style>
<main><h1>Ecraela Fleet</h1><p>Copyright © 2026 Ecraela and contributors.</p>
<p>This software is licensed under the GNU Affero General Public License, version 3 only
(AGPL-3.0-only), except for separately licensed third-party components.
It is provided without warranty, to the extent permitted by law.</p>
<p>The source archive below contains the matching project source and build scripts
for this manager. Public dependencies are pinned in go.mod/go.sum and the frontend lockfile;
see README.md in the archive for the build instructions.</p><ul>''' + ''.join(
        f'<li><a href="{html.escape(name)}" download>{html.escape(label)}</a></li>' for name, label in (
            ('LICENSE', 'GNU AGPL version 3'), ('source.tar.gz', 'Download matching source'),
            ('source.json', 'Source archive checksum and file hashes'),
            ('THIRD_PARTY_NOTICES.txt', 'Third-party licenses and notices'),
            ('dependencies.json', 'Dependency versions and provenance'))) + '''</ul>
<p>Optional commercial licensing and paid support are available from
<a href="https://ecraela.com/contact">Ecraela</a> under separate agreements.</p>
<p>HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet is not affiliated with or endorsed by HAProxy Technologies.</p>
<p><a href="/">Back to the console</a></p></main></html>\n''')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=('update', 'check', 'bundle'))
    p.add_argument('--output', type=Path, default=ROOT / 'web/public/legal')
    args = p.parse_args()
    if args.action == 'update':
        collect()
    elif args.action == 'check':
        check()
    else:
        bundle(args.output)


if __name__ == '__main__':
    main()
