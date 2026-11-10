"""The release job cannot silently assert an unqualified mixed-version window."""
import json
from pathlib import Path
import tempfile
import unittest

import agentctl
import release


class ReleaseCompatibility(unittest.TestCase):
    def test_exact_release_entry_is_required(self):
        with tempfile.TemporaryDirectory() as temp:
            entry = Path(temp) / 'compatibility.json'
            entry.write_text(json.dumps({
                'version': 'v0.1.0-rc.22',
                'manager': {'min': 'v0.1.0-rc.21', 'max': 'v0.1.0-rc.22'},
                'agent': {'min': 'v0.1.0-rc.21', 'max': 'v0.1.0-rc.22'},
            }))
            ranges = release.load_compatibility(entry, 'v0.1.0-rc.22')
            self.assertTrue(agentctl.compatible_peer(
                {'version': 'v0.1.0-rc.22', 'compatibility': ranges},
                'agent', 'v0.1.0-rc.21'))
            with self.assertRaises(ValueError):
                release.load_compatibility(entry, 'v0.1.0-rc.23')
            entry.write_text(json.dumps({'version': 'v0.1.0-rc.22', **ranges, 'extra': True}))
            with self.assertRaises(ValueError):
                release.load_compatibility(entry, 'v0.1.0-rc.22')


if __name__ == '__main__':
    unittest.main()
