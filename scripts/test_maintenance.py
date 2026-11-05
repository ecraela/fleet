"""Restart drills must observe the new proxy before asserting traffic isolation."""
import copy
import unittest

from maintenance import rejoin_target_ready, restarted_entrypoint_ready


class MaintenanceRestartTest(unittest.TestCase):
    def ready(self):
        return {
            'backend': 'nodes', 'server': 'node-a',
            'maintenance': {'status': 'maintenance'},
            'controller': {'discovery': {
                'observed_at_ms': 2001,
                'configuration': {'observed_at_ms': 2001},
                'servers': [
                    {'backend': 'nodes', 'name': 'node-a', 'state': 'maint', 'health': 'DOWN'},
                    {'backend': 'nodes', 'name': 'node-b', 'state': 'ready', 'health': 'UP'},
                ],
            }},
        }

    def test_recent_but_pre_restart_observations_are_not_ready(self):
        for field in ('runtime', 'configuration'):
            view = self.ready()
            discovery = view['controller']['discovery']
            target = discovery if field == 'runtime' else discovery['configuration']
            target['observed_at_ms'] = 1999
            with self.subTest(field=field):
                self.assertFalse(restarted_entrypoint_ready(view, 2000))

    def test_healthy_peer_is_required_before_strict_traffic_assertions(self):
        view = self.ready()
        view['controller']['discovery']['servers'][1]['health'] = 'DOWN'
        self.assertFalse(restarted_entrypoint_ready(view, 2000))
        self.assertTrue(restarted_entrypoint_ready(self.ready(), 2000))

    def test_lost_withdrawal_is_a_failure_instead_of_a_readiness_retry(self):
        ready = self.ready()
        wrong_state = copy.deepcopy(ready)
        wrong_state['controller']['discovery']['servers'][0]['state'] = 'ready'
        wrong_reservation = copy.deepcopy(ready)
        wrong_reservation['maintenance']['status'] = 'attention'
        for view in (wrong_state, wrong_reservation):
            with self.assertRaisesRegex(RuntimeError, 'did not preserve node withdrawal'):
                restarted_entrypoint_ready(view, 2000)


class MaintenanceRejoinTest(unittest.TestCase):
    def ready(self):
        return {'fresh': True, 'configuration_enabled': True, 'control': {'discovery': {
            'configuration': {'document': {'backends': [{'name': 'application', 'servers': [
                {'name': 'app', 'weight': 100, 'maintenance': False},
            ]}]}},
            'proxies': [{'kind': 'frontend', 'status': 'OPEN'}],
            'servers': [{'backend': 'application', 'name': 'app', 'weight': 100,
                         'state': 'ready', 'health': 'UP', 'check_status': 'L7OK'}],
        }}}

    def test_up_servers_do_not_make_stale_saved_configuration_ready(self):
        for flag in ('fresh', 'configuration_enabled'):
            view = self.ready()
            view[flag] = False
            with self.subTest(flag=flag):
                self.assertFalse(rejoin_target_ready(view))
        self.assertTrue(rejoin_target_ready(self.ready()))

    def test_closed_listener_or_incomplete_health_check_prevents_rejoin(self):
        view = self.ready()
        view['control']['discovery']['proxies'][0]['status'] = 'STOP'
        self.assertFalse(rejoin_target_ready(view))
        view = self.ready()
        view['control']['discovery']['servers'][0]['check_status'] = 'INI'
        self.assertFalse(rejoin_target_ready(view))


if __name__ == '__main__':
    unittest.main()
