"""Cold lab readiness must depend on agent-reported authority, not startup order."""
import unittest
from unittest.mock import patch

import acme
from rollout import backends_ready


class Readiness(unittest.TestCase):
    def test_configuration_discovery_is_not_completed_backend_health(self):
        expected={'name':'web','weight':100,'maintenance':False}
        observation={'backend':'application','name':'web','state':'ready','health':'UP','check_status':'L7OK','weight':100}
        discovery={'configuration':{'document':{'backends':[{'name':'application','servers':[expected]}]}},'servers':[observation]}
        self.assertTrue(backends_ready(discovery))
        for field, value in (('weight', 1), ('health', 'DOWN'), ('check_status', 'L4CON'), ('state', 'drain')):
            with self.subTest(field=field):
                self.assertFalse(backends_ready({**discovery,'servers':[{**observation,field:value}]}))
        self.assertFalse(backends_ready({**discovery,'servers':[]}))

    def test_volume_provisioning_retains_dependency_and_requires_success(self):
        success = b'{"Status":"exited","ExitCode":0}'
        for observations in ([success], [b'{"Status":"running","ExitCode":0}', success], [b'{"Status":"exited","ExitCode":1}']):
            def poll(description, operation):
                return operation() or operation()
            with self.subTest(observations=observations), patch.object(acme, 'compose') as compose, patch('recovery.source_command', return_value=b'fixture-cid'), patch('recovery.command', side_effect=observations), patch.object(acme, 'wait_for', side_effect=poll):
                if observations[-1] == success:
                    acme.provision_volumes()
                else:
                    with self.assertRaisesRegex(RuntimeError, 'initialization failed'):
                        acme.provision_volumes()
                self.assertEqual(compose.call_args_list[0].args, ('up', '-d', '--no-deps', 'acme-volume-init'))
                self.assertEqual(compose.call_count, 1)

    def test_waits_for_actual_acme_option_without_writing(self):
        option = {'issuer': 'demo-acme', 'binding': 'reported-grant'}
        observations = [{'options': []}, {'options': [{'issuer': 'other'}]}, {'options': [option]}]
        def poll(description, operation, seconds):
            self.assertEqual(seconds, 120)
            self.assertIsNone(operation())
            self.assertIsNone(operation())
            return operation()
        with patch.object(acme, 'plan', side_effect=[(None, None, v) for v in observations]), patch.object(acme, 'wait_for', side_effect=poll):
            self.assertEqual(acme.wait_for_grants(), option)


if __name__ == '__main__':
    unittest.main()
