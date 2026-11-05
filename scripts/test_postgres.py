"""Run database tests with an isolated, durable PostgreSQL 18 cluster."""
import argparse
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import tempfile
from urllib.parse import quote


def execute(args):
    command = args.command[1:] if args.command[:1] == ['--'] else args.command
    if not command:
        raise ValueError('Supply the test command after --')
    if not re.fullmatch(r'[A-Z][A-Z0-9_]*', args.variable):
        raise ValueError('Invalid database environment variable')
    if not re.fullmatch(r'[a-z][a-z0-9_]*', args.database):
        raise ValueError('Invalid test database name')
    env = os.environ.copy()
    if args.reuse_env and env.get(args.variable):
        print('Using the explicitly configured test database.', flush=True)
        return subprocess.run(command, cwd=args.cwd, env=env).returncode

    for key in ('PGHOST', 'PGHOSTADDR', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD',
                'PGPASSFILE', 'PGSERVICE', 'PGSERVICEFILE', 'PGOPTIONS'):
        env.pop(key, None)

    bindir = os.getenv('POSTGRES_BIN')
    if not bindir:
        pg_config = shutil.which('pg_config')
        if not pg_config:
            raise ValueError('PostgreSQL 18 tools are required; set POSTGRES_BIN or configure a dedicated test database.')
        bindir = subprocess.check_output([pg_config, '--bindir'], text=True).strip()
    pg = Path(bindir)
    version = subprocess.check_output([str(pg / 'postgres'), '--version'], text=True).strip()
    if not re.search(r'\(PostgreSQL\) 18\.', version):
        raise ValueError('The isolated test cluster requires PostgreSQL 18.')

    # A short private path also fits macOS's Unix-socket pathname limit.
    with tempfile.TemporaryDirectory(prefix='ha-test-pg-', dir='/tmp') as temporary:
        root = Path(temporary)
        password = secrets.token_urlsafe(32)
        password_file = root / 'password'
        password_file.write_text(password + '\n')
        password_file.chmod(0o600)
        data = root / 'data'
        try:
            with (root / 'bootstrap.log').open('w') as log:
                subprocess.run([str(pg / 'initdb'), '-D', str(data), '-U', 'postgres',
                                '--auth=scram-sha-256', '--pwfile', str(password_file),
                                '--encoding=UTF8', '--locale=C'], check=True, stdout=log, stderr=log)
                subprocess.run([str(pg / 'pg_ctl'), '-D', str(data), '-l', str(root / 'server.log'),
                                '-o', f"-h '' -k {root} -c unix_socket_permissions=0700", '-w', 'start'],
                               check=True, stdout=log, stderr=log)
                bootstrap_env = dict(env, PGPASSWORD=password)
                subprocess.run([str(pg / 'createdb'), '-h', str(root), '-p', '5432', '-U', 'postgres', args.database],
                               check=True, env=bootstrap_env, stdout=log, stderr=log)
            env[args.variable] = (f'postgresql://postgres:{quote(password)}@localhost:5432/{args.database}'
                                  f'?{args.socket_parameter}={quote(str(root), safe="")}')
            print(f'Isolated {version}; private socket, crash durability enabled.', flush=True)
            return subprocess.run(command, cwd=args.cwd, env=env).returncode
        finally:
            if (data / 'postmaster.pid').exists():
                subprocess.run([str(pg / 'pg_ctl'), '-D', str(data), '-m', 'fast', '-w', 'stop'],
                               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--variable', required=True)
    parser.add_argument('--database', required=True)
    parser.add_argument('--socket-parameter', choices=('host', 'socket_dir'), default='host')
    parser.add_argument('--cwd', type=Path)
    parser.add_argument('--reuse-env', action='store_true', help='use an explicitly supplied test URL without managing that database')
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        return execute(args)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        # Commands contain only tool paths; credentials travel through private
        # files and the child environment, never the command line or output.
        parser.exit(1, f'Test database setup failed: {error}\n')


if __name__ == '__main__':
    raise SystemExit(main())
