#!/usr/bin/env python3
"""Preserve exact Alpine runtime recipes, verified upstream sources and notices.

Runs only in a disposable Docker build stage, without host mounts or credentials.
The installed database must be captured before installing the collection tools.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile

from legal import NOTICE


def packages(raw):
    result = []
    for block in raw.split('\n\n'):
        fields = dict(line.split(':', 1) for line in block.splitlines() if ':' in line)
        if not fields.get('P'):
            continue
        row = {name: fields.get(key, '') for key, name in
               [('P', 'name'), ('V', 'version'), ('L', 'license'), ('o', 'origin'), ('c', 'commit')]}
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9+_.-]{0,127}', row['origin']) or not re.fullmatch(r'[a-f0-9]{40}', row['commit']):
            raise ValueError('Runtime package lacks an exact, safe source recipe identity')
        result.append(row)
    if not result or len(result) > 128:
        raise ValueError('Unexpected runtime package inventory')
    return sorted(result, key=lambda row: row['name'])


def command(args, **kwargs):
    return subprocess.check_output(args, timeout=180, **kwargs)


def extract_recipe(raw, prefix, folder):
    original = folder / 'recipe.tar'
    original.write_bytes(raw)
    files = [original]
    aliases = []
    with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
        for member in archive:
            if member.isdir():
                continue
            name = Path(member.name).relative_to(prefix)
            target = folder / name
            if '..' in name.parts or not target.resolve().is_relative_to(folder.resolve()):
                raise ValueError('Invalid source recipe path')
            if member.issym():
                linked = target.parent / member.linkname
                if Path(member.linkname).is_absolute() or not linked.resolve().is_relative_to(folder.resolve()):
                    raise ValueError('Source recipe link escapes its directory')
                aliases.append((target, linked))
                continue
            if not member.isfile():
                raise ValueError('Special source recipe entries are refused')
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(archive.extractfile(member).read())
            target.chmod(member.mode & 0o777)
            files.append(target)
    for target, linked in aliases:
        # Only regular working copies are used by abuild. The original Git
        # archive above preserves each symlink and its exact source semantics.
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(linked.read_bytes())
        files.append(target)
    return files


def collect(database, output, workspace):
    rows = packages(database.read_text())
    output.mkdir(parents=True, exist_ok=True)
    workspace.mkdir(parents=True, exist_ok=False)
    repository = Path(os.environ.get('APORTS_CACHE', workspace / 'aports'))
    if not (repository / '.git').is_dir():
        command(['git', 'init', '-q', str(repository)])
        command(['git', '-C', str(repository), 'remote', 'add', 'origin', 'https://github.com/alpinelinux/aports.git'])
    text = ['Alpine container runtime components\n\nThese components are separate from Ecraela Fleet and keep their original licenses.\n']
    recipes = sorted({(row['origin'], row['commit']) for row in rows})
    recipe_files = {}
    for origin, commit in recipes:
        print(f'Fetching verified runtime sources: {origin} {commit}', flush=True)
        ref = 'refs/source-recipes/' + commit
        # Probing a missing object in a partial clone can trigger a huge implicit
        # fetch. Check our own ref instead and explicitly fetch only this commit.
        present = subprocess.run(['git', '-C', str(repository), 'rev-parse', '--verify', '--quiet', ref],
                                 capture_output=True, timeout=10)
        if present.returncode:
            command(['git', '-C', str(repository), 'fetch', '-q', '--depth=1', '--filter=blob:none', 'origin', commit + ':' + ref])
        actual = command(['git', '-C', str(repository), 'rev-parse', ref + '^{commit}'], text=True).strip()
        if actual != commit:
            raise ValueError('Source recipe revision mismatch')
        folder = workspace / 'recipes' / (origin + '-' + commit)
        folder.mkdir(parents=True)
        candidates = [p for p in command(['git', '-C', str(repository), 'ls-tree', '-r', '--name-only', commit], text=True).splitlines()
                      if p in {f'main/{origin}/APKBUILD', f'community/{origin}/APKBUILD'}]
        if len(candidates) != 1:
            raise ValueError(f'Cannot find exact runtime recipe for {origin}')
        prefix = str(Path(candidates[0]).parent)
        recipe = command(['git', '-C', str(repository), 'archive', commit, prefix])
        recipe_files[folder] = extract_recipe(recipe, prefix, folder)
        for name in command(['git', '-C', str(repository), 'ls-tree', '--name-only', commit], text=True).splitlines():
            if NOTICE.fullmatch(name):
                target = folder / ('aports-' + name)
                target.write_bytes(command(['git', '-C', str(repository), 'show', commit + ':' + name]))
                recipe_files[folder].append(target)
        sources = folder / 'source-files'
        env = dict(os.environ, SRCDEST=str(sources), REPODEST=str(workspace / 'packages'))
        # abuild checks APKBUILD's upstream checksums before unpacking. The
        # source scripts are pinned by the signed runtime package's commit.
        subprocess.run(['abuild', '-F', 'fetch', 'verify', 'unpack'], cwd=folder, env=env, check=True, timeout=600)
        for row in rows:
            if (row['origin'], row['commit']) == (origin, commit):
                row['recipe'] = f'https://github.com/alpinelinux/aports/tree/{commit}/{prefix}'
        text.append(f'\n{"=" * 72}\n{origin} — recipe {commit}\n')
        found = []
        for file in sorted(folder.rglob('*')):
            if file.is_file() and not file.is_symlink() and NOTICE.fullmatch(file.name) and file.stat().st_size < 1 << 20:
                try:
                    body = file.read_text()
                except UnicodeError:
                    continue
                found.append(file)
                text.append('\n' + str(file.relative_to(folder)) + '\n' + body)
        if not found:
            # Some data-only Alpine recipes have their notice in a shell header.
            text.append('\nAPKBUILD\n' + (folder / 'APKBUILD').read_text())
        # The original archives and recipe/patches are sufficient to repeat unpack;
        # avoid redistributing a second expanded copy of every upstream tree.
        for file in sources.rglob('*'):
            if file.is_symlink():
                raise ValueError('Symlinked upstream source archive')
    manifest = {'schema': 1, 'packages': rows, 'files': {}}
    target = output / 'container-source.tar.gz'
    with target.open('wb') as destination:
        import gzip
        with gzip.GzipFile(fileobj=destination, mode='wb', filename='', mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as archive:
                for folder in sorted((workspace / 'recipes').iterdir()):
                    include = recipe_files[folder] + [p for p in (folder / 'source-files').rglob('*')
                                                       if p.is_file() and not p.is_symlink()]
                    if not any('source-files' in p.relative_to(folder).parts for p in include):
                        # Recipes with only local files still need their full
                        # patches/scripts, already retained in recipe_files.
                        print(f'Runtime recipe uses local source files: {folder.name}', flush=True)
                    for file in sorted(set(include)):
                        data = file.read_bytes()
                        name = str(file.relative_to(workspace))
                        manifest['files'][name] = hashlib.sha256(data).hexdigest()
                        entry = tarfile.TarInfo(name)
                        entry.size, entry.mtime = len(data), 0
                        entry.mode = 0o755 if file.stat().st_mode & 0o111 else 0o644
                        archive.addfile(entry, io.BytesIO(data))
    with target.open('rb') as source:
        manifest['source_sha256'] = hashlib.file_digest(source, 'sha256').hexdigest()
    (output / 'container-packages.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
    (output / 'CONTAINER_NOTICES.txt').write_text('\n'.join(text) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--work', type=Path, required=True)
    args = parser.parse_args()
    collect(args.database, args.output, args.work)
