#!/usr/bin/env python3
"""CI: match each published image executable to the release file before signing."""
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import tarfile

from agentctl import LEGAL_ARTIFACTS, MAX_BINARY, InstallError, regular, run


def verify(folder):
    manifest=json.loads(regular(folder/'release.json',65536))
    for arch in ('amd64','arm64'):
        run(['docker','pull','--platform','linux/'+arch,manifest['image']])
        cid=run(['docker','create','--platform','linux/'+arch,'--entrypoint','/bin/false',manifest['image']],capture=True).decode().strip()
        try:
            with tempfile.TemporaryDirectory(prefix='release-image-check-') as temp:
                for name in ('agent','manager'):
                    binary=Path(temp)/('haproxy-'+name)
                    run(['docker','cp',cid+':/usr/local/bin/haproxy-'+name,binary])
                    digest=hashlib.sha256(regular(binary,MAX_BINARY)).hexdigest()
                    if digest!=manifest['artifacts'][f'haproxy-{name}-linux-{arch}']:
                        raise InstallError('Release image and Linux executable hashes differ; refusing to sign')
                for name in LEGAL_ARTIFACTS:
                    path = Path(temp) / name
                    run(['docker', 'cp', cid + ':/usr/share/haproxy-manager/' + name, path])
                    if hashlib.sha256(regular(path, MAX_BINARY)).hexdigest() != manifest['artifacts'].get(name):
                        raise InstallError('Image and release legal/source artifacts differ; refusing to sign')
                for name in ('CONTAINER_NOTICES.txt', 'container-packages.json', 'container-source.tar.gz'):
                    run(['docker', 'cp', cid + ':/usr/share/haproxy-manager/' + name, Path(temp) / name])
                packages = json.loads(regular(Path(temp) / 'container-packages.json'))
                source = Path(temp) / 'container-source.tar.gz'
                with source.open('rb') as stream:
                    if hashlib.file_digest(stream, 'sha256').hexdigest() != packages['source_sha256']:
                        raise InstallError('Container upstream source checksum differs')
                with tarfile.open(source, 'r:gz') as archive:
                    if set(archive.getnames()) != set(packages['files']):
                        raise InstallError('Container source file inventory differs')
                    for entry in archive:
                        if not entry.isfile() or entry.name.startswith('/') or '..' in Path(entry.name).parts:
                            raise InstallError('Unsafe container source archive member')
                        with archive.extractfile(entry) as stream:
                            if hashlib.file_digest(stream, 'sha256').hexdigest() != packages['files'][entry.name]:
                                raise InstallError('Container upstream source file differs')
        finally:
            run(['docker','rm',cid])
    print('Both architectures: executable, license, notice and source checksums match')


if __name__=='__main__':
    verify(Path(sys.argv[1]))
