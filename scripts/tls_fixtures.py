"""Private, synthetic TLS material for the local two-node lab only."""
import json
from pathlib import Path
import subprocess


def private(path, value):
    path.write_bytes(value if isinstance(value, bytes) else value.encode())
    path.chmod(0o600)


def openssl(*args):
    subprocess.run(['openssl', *map(str, args)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def prepare_assets(folder):
    tls = Path(folder) / 'tls'
    tls.mkdir(mode=0o700, exist_ok=True)
    if not (tls / 'ca.crt').exists():
        openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '3650', '-subj', '/CN=Converge local HAProxy demo CA', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-addext', 'subjectKeyIdentifier=hash', '-keyout', tls / 'ca.key', '-out', tls / 'ca.crt')
        (tls / 'ca.key').chmod(0o600)
    for name, host, days in [('demo-v1', 'apps.demo.test', 30), ('demo-v2', 'apps.demo.test', 60), ('demo-expired', 'apps.demo.test', -1), ('demo-wrong-host', 'other.demo.test', 30)]:
        if (tls / f'{name}.pem').exists():
            continue
        key, csr, cert = [tls / f'{name}.{ext}' for ext in ('key', 'csr', 'crt')]
        openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', f'/CN={host}', '-keyout', key, '-out', csr)
        key.chmod(0o600)
        extension = tls / 'extension.cnf'
        private(extension, f'subjectAltName=DNS:{host}\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n')
        if days < 0:
            for filename, initial in [('index', ''), ('serial', '1000\n')]:
                if not (tls / filename).exists():
                    private(tls / filename, initial)
            config = tls / 'expired-ca.cnf'
            private(config, f'[ca]\ndefault_ca=local\n[local]\ndatabase={tls}/index\nserial={tls}/serial\nnew_certs_dir={tls}\ncertificate={tls}/ca.crt\nprivate_key={tls}/ca.key\ndefault_md=sha256\npolicy=names\n[names]\ncommonName=supplied\n')
            openssl('ca', '-config', config, '-batch', '-notext', '-startdate', '200101000000Z', '-enddate', '210101000000Z', '-extfile', extension, '-in', csr, '-out', cert)
        else:
            openssl('x509', '-req', '-in', csr, '-CA', tls / 'ca.crt', '-CAkey', tls / 'ca.key', '-CAcreateserial', '-days', days, '-extfile', extension, '-out', cert)
        private(tls / f'{name}.pem', cert.read_bytes() + (tls / 'ca.crt').read_bytes() + key.read_bytes())
    if not (tls / 'demo-bad-key.pem').exists():
        private(tls / 'demo-bad-key.pem', (tls / 'demo-v1.crt').read_bytes() + (tls / 'demo-v2.key').read_bytes())
    for letter in ('a', 'b'):
        policy = {
            'bindings': [{'name': 'public', 'address': '*', 'port': 8443}],
            'probe_host': f'haproxy-{letter}',
            'certificates': [{'reference': p.stem, 'path': f'/var/lib/haproxy/certificates/{p.name}'} for p in sorted(tls.glob('demo-*.pem'))],
        }
        private(Path(folder) / f'tls-policy-{letter}.json', json.dumps(policy) + '\n')
