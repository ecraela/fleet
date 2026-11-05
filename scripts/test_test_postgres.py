"""The local test fixture must not target the development database."""
import argparse
import os
from pathlib import Path
import stat
import subprocess
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

from test_postgres import execute


class PostgresFixtureTest(unittest.TestCase):
    def options(self, **overrides):
        values = dict(command=['--', 'test-command'], variable='TEST_DATABASE_URL', database='isolated_test',
                      cwd=None, reuse_env=False, socket_parameter='socket_dir')
        return argparse.Namespace(**(values | overrides))

    def test_private_cluster_overrides_ambient_database_and_cleans_up_after_test_failure(self):
        roots = []
        passwords = []

        def run(command, **options):
            if command[0].endswith('/pg_ctl') and command[-1] == 'start':
                data = Path(command[command.index('-D') + 1])
                data.mkdir()
                (data / 'postmaster.pid').write_text('123\n')
            if command == ['test-command']:
                env = options['env']
                url = urlsplit(env['TEST_DATABASE_URL'])
                root = Path(parse_qs(url.query)['socket_dir'][0])
                roots.append(root)
                passwords.append(url.password)
                self.assertEqual(url.hostname, 'localhost')
                self.assertEqual(url.path, '/isolated_test')
                self.assertEqual(stat.S_IMODE(root.stat().st_mode), 0o700)
                self.assertEqual(stat.S_IMODE((root / 'password').stat().st_mode), 0o600)
                self.assertNotIn('PGHOSTADDR', env)
                self.assertNotIn('PGSERVICE', env)
                self.assertNotIn('PGOPTIONS', env)
                return subprocess.CompletedProcess(command, 7)
            return subprocess.CompletedProcess(command, 0)

        with patch.dict(os.environ, {'POSTGRES_BIN': '/tools/postgres',
                                     'TEST_DATABASE_URL': 'postgresql://development/live',
                                     'PGHOSTADDR': '192.0.2.1', 'PGSERVICE': 'development',
                                     'PGOPTIONS': '-c search_path=development'}), \
                patch('test_postgres.subprocess.check_output', return_value='postgres (PostgreSQL) 18.6'), \
                patch('test_postgres.subprocess.run', side_effect=run) as called:
            self.assertEqual(execute(self.options()), 7)
        self.assertFalse(roots[0].exists())
        commands = [call.args[0] for call in called.call_args_list]
        self.assertTrue(any(command[-1] == 'stop' for command in commands))
        self.assertTrue(any("-h ''" in str(command) for command in commands))
        self.assertFalse(any(passwords[0] in str(command) for command in commands))

    def test_explicit_external_database_is_not_started_or_stopped(self):
        with patch.dict(os.environ, {'TEST_DATABASE_URL': 'postgresql://dedicated/test'}), \
                patch('test_postgres.subprocess.run', return_value=subprocess.CompletedProcess([], 0)) as run, \
                patch('test_postgres.subprocess.check_output') as bootstrap:
            self.assertEqual(execute(self.options(reuse_env=True)), 0)
        bootstrap.assert_not_called()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.kwargs['env']['TEST_DATABASE_URL'], 'postgresql://dedicated/test')


if __name__ == '__main__':
    unittest.main()
