"""Optimistic concurrency in the demo must not duplicate uncertain writes."""
import unittest
from unittest.mock import Mock, patch
from urllib.error import HTTPError

from applications import Applications


class ApplicationRequests(unittest.TestCase):
    def setUp(self):
        self.app = Applications.__new__(Applications)
        self.app.path = '/v1/applications'
        self.app.api = Mock()
        self.current = {'id': 'old', 'application_id': 'app', 'number': 1,
                        'status': 'succeeded', 'spec': {'id': 'app', 'name': 'Demo', 'weight': 100}}
        self.spec = {'name': 'Demo', 'weight': 99}
        self.deployed = {**self.current, 'id': 'new', 'number': 2}
        self.app.revisions = Mock(return_value=[self.current])
        self.app.input = Mock(side_effect=[{'request_id': 'first'}, {'request_id': 'fresh'}, {'request_id': 'last'}])

    def conflict(self):
        return HTTPError('https://localhost/v1/applications', 409, 'resource conflict', {}, None)

    @patch('applications.time.sleep')
    def test_rejected_snapshot_is_refreshed_and_accepted_request_is_repeated_with_same_id(self, sleep):
        self.app.api.request.side_effect = [self.conflict(), {'revision': self.deployed}, {'revision': self.deployed}]
        self.assertEqual(self.app.start(self.spec, self.current), self.deployed)
        calls = self.app.api.request.call_args_list
        self.assertNotEqual(calls[0].args[1], calls[1].args[1])
        self.assertEqual(calls[1], calls[2])
        self.assertEqual(self.app.input.call_count, 2)

    @patch('applications.time.sleep')
    def test_concurrent_application_revision_cannot_be_rebased_implicitly(self, sleep):
        self.app.api.request.side_effect = self.conflict()
        self.app.revisions.return_value = [self.current, self.deployed]
        with self.assertRaises(HTTPError):
            self.app.start(self.spec, self.current)
        self.assertEqual(self.app.api.request.call_count, 1)
        sleep.assert_not_called()

    @patch('applications.time.sleep')
    def test_uncertain_or_unavailable_write_is_never_replayed(self, sleep):
        for error in (TimeoutError('unknown outcome'), HTTPError('https://localhost', 503, 'unavailable', {}, None)):
            self.app.api.request.reset_mock()
            self.app.input.reset_mock(side_effect=True)
            self.app.input.return_value = {'request_id': 'first'}
            self.app.api.request.side_effect = error
            with self.assertRaises(type(error)):
                self.app.start(self.spec, self.current)
            self.assertEqual(self.app.api.request.call_count, 1)
        sleep.assert_not_called()

    @patch('applications.time.sleep')
    def test_snapshot_refresh_is_bounded(self, sleep):
        self.app.api.request.side_effect = self.conflict()
        with self.assertRaises(HTTPError):
            self.app.start(self.spec, self.current)
        self.assertEqual(self.app.api.request.call_count, 3)
        self.assertEqual(sleep.call_count, 2)


if __name__ == '__main__':
    unittest.main()
