"""Lab helpers resolve Compose services without assuming a project name."""
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import lab


class LabServicesTest(unittest.TestCase):
    def test_container_uses_the_requested_checkout_and_compose_service(self):
        root = Path('/tmp/customer checkout')
        with patch('lab.subprocess.run', return_value=subprocess.CompletedProcess([], 0, b'container-id\n')) as run:
            self.assertEqual(lab.container(root, 'postgres'), 'container-id')
        args = run.call_args.args[0]
        self.assertEqual(args[args.index('--project-directory') + 1], str(root.resolve()))
        self.assertEqual(args[args.index('--env-file') + 1], str(root.resolve() / 'deploy/local/state.local/compose.env'))
        self.assertEqual(args[-3:], ['ps', '-q', 'postgres'])

    def test_stopped_or_scaled_services_are_not_guessed(self):
        for ids in (b'', b'node-a\nnode-b\n'):
            with self.subTest(ids=ids), patch('lab.subprocess.run', return_value=subprocess.CompletedProcess([], 0, ids)):
                with self.assertRaisesRegex(RuntimeError, 'Expected one running demo manager'):
                    lab.container('/tmp/checkout', 'manager')

    def test_network_comes_from_the_resolved_service(self):
        with patch('lab.container', return_value='isolated-manager'), patch('lab.subprocess.run', return_value=subprocess.CompletedProcess([], 0, b'{"customer-lab_default":{}}')) as run:
            self.assertEqual(lab.network('/tmp/checkout'), 'customer-lab_default')
        self.assertEqual(run.call_args.args[0][2], 'isolated-manager')

    def test_ambiguous_network_does_not_silently_choose_a_destination(self):
        for networks in ({}, {'a': {}, 'b': {}}, []):
            with self.subTest(networks=networks), patch('lab.container', return_value='manager'), patch('lab.subprocess.run', return_value=subprocess.CompletedProcess([], 0, json.dumps(networks).encode())):
                with self.assertRaisesRegex(RuntimeError, 'exactly one Docker network'):
                    lab.network('/tmp/checkout')


if __name__ == '__main__':
    unittest.main()
