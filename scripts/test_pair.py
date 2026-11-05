"""Guard the demo's exact recovery images across shared-tag rebuilds."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import pair


class PairImagesTest(unittest.TestCase):
    def test_explicit_demo_root_selects_existing_checkout_without_relocating_state(self):
        env = dict(os.environ, HAPROXY_DEMO_ROOT='/tmp/customer-demo')
        script = 'import pair; print(pair.ROOT); print(pair.STATE)'
        output = subprocess.check_output(
            [sys.executable, '-c', script], cwd=Path(__file__).parent, env=env, text=True)
        root = Path('/tmp/customer-demo').resolve()
        self.assertEqual(output.splitlines(), [str(root), str(root / 'deploy/local/state.local')])

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.command = ['docker', 'compose', '-p', 'isolated-demo']
        self.image = 'sha256:' + 'a' * 64

    def test_standalone_is_default_and_converge_overlay_requires_an_explicit_choice(self):
        for env, integrated in (({}, False), ({'HAPROXY_PAIR_STANDALONE': '0'}, True)):
            with self.subTest(env=env), patch.dict(os.environ, env, clear=True), patch('pair.selected_state', return_value=self.folder), patch('pair.PAIR', self.folder), patch('pair.subprocess.run') as run:
                pair.compose('ps')
                self.assertEqual(str(pair.ROOT / 'compose.console.yaml') in run.call_args.args[0], integrated)

    def test_retains_running_and_stopped_images_before_the_tag_is_rebuilt(self):
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[-3:] == ['ps', '-a', '-q']:
                output = b'container-a\ncontainer-b\n'
            elif args[1] == 'inspect':
                output = (self.image + '\n' + self.image + '\n').encode()
            else:
                output = b''
            return subprocess.CompletedProcess(args, 0, output, b'')

        with patch('pair.selected_state', return_value=self.folder), patch('pair.PAIR', self.folder), patch('pair.subprocess.run', side_effect=run), patch.dict(os.environ, {'HAPROXY_PAIR_STANDALONE': '1'}):
            pair.compose('build', 'manager')
        self.assertEqual(calls[0][-3:], ['ps', '-a', '-q'])
        self.assertIn(str(pair.ROOT / 'compose.pair.yaml'), calls[0])
        self.assertEqual(calls[1], ['docker', 'inspect', '--format', '{{.Image}}', 'container-a', 'container-b'])
        self.assertEqual(calls[2], ['docker', 'image', 'tag', self.image, 'converge-haproxy-demo-retained:' + 'a' * 64])
        self.assertEqual(calls[3][-2:], ['build', 'manager'])

    def test_missing_exact_image_aborts_before_the_build(self):
        def run(args, **kwargs):
            if args[-3:] == ['ps', '-a', '-q']:
                return subprocess.CompletedProcess(args, 0, b'container-a', b'')
            if args[1] == 'inspect':
                return subprocess.CompletedProcess(args, 0, self.image.encode(), b'')
            return subprocess.CompletedProcess(args, 1, b'', b'private diagnostic')

        with patch('pair.subprocess.run', side_effect=run) as calls:
            with self.assertRaisesRegex(RuntimeError, '^Cannot retain the local demo images before rebuilding$'):
                pair.retain_demo_images(self.command, {})
        self.assertEqual(len(calls.call_args_list), 3)

    def test_empty_project_needs_no_retention_and_read_only_calls_do_not_tag(self):
        with patch('pair.subprocess.run', return_value=subprocess.CompletedProcess([], 0, b'', b'')) as calls:
            pair.retain_demo_images(self.command, {})
        self.assertEqual(calls.call_count, 1)
        with patch('pair.selected_state', return_value=self.folder), patch('pair.PAIR', self.folder), patch('pair.subprocess.run') as calls:
            pair.compose('ps', '--services')
        self.assertEqual(calls.call_count, 1)

    def test_incomplete_image_inventory_cannot_allow_a_build(self):
        results = [subprocess.CompletedProcess([], 0, b'container-a\ncontainer-b', b''),
                   subprocess.CompletedProcess([], 0, self.image.encode(), b'')]
        with patch('pair.subprocess.run', side_effect=results) as calls:
            with self.assertRaisesRegex(RuntimeError, 'Incomplete local demo image inventory'):
                pair.retain_demo_images(self.command, {})
        self.assertEqual(calls.call_count, 2)


if __name__ == '__main__':
    unittest.main()
