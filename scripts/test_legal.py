"""Legal packaging must preserve notices and exclude private working state."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import legal


class LegalPackaging(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in legal.INPUTS:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('{"license":"AGPL-3.0-only"}' if name.endswith('package.json') else 'pinned input\n')
        (self.root / 'LICENSE').write_bytes((legal.ROOT / 'LICENSE').read_bytes())
        (self.root / 'README.md').write_text('Build instructions fixture\n')
        (self.root / 'legal/notices').mkdir(parents=True)
        body = b'Copyright fixture author\nUnmodified license and NOTICE text\n'
        digest = legal.sha(body)
        self.notice = 'notices/' + digest + '.txt'
        (self.root / 'legal' / self.notice).write_bytes(body)
        self.manifest = {'schema': 1, 'inputs': {p: legal.sha((self.root / p).read_bytes()) for p in legal.INPUTS},
                         'components': [{'name': 'fixture', 'version': '1', 'license': 'MIT', 'source': 'https://example.test/source',
                                         'notices': [{'file': self.notice, 'sha256': digest, 'upstream_path': 'LICENSE'}]}]}
        legal.dump(self.root / 'legal/dependencies.json', self.manifest)
        self.names = sorted([*legal.INPUTS, 'LICENSE', 'README.md', 'legal/dependencies.json',
                             'legal/source-files.json', 'legal/' + self.notice])
        legal.dump(self.root / 'legal/source-files.json', self.names)

    def test_source_is_repeatable_exact_and_does_not_walk_private_state(self):
        (self.root / 'identity.key').write_text('private fixture excluded')
        (self.root / 'private.local').mkdir()
        (self.root / 'private.local/token').write_text('private fixture excluded')
        first, second = self.root / 'out-a', self.root / 'out-b'
        legal.bundle(first, self.root)
        legal.bundle(second, self.root)
        self.assertEqual((first / 'source.tar.gz').read_bytes(), (second / 'source.tar.gz').read_bytes())
        info = json.loads((first / 'source.json').read_text())
        self.assertEqual(info['sha256'], hashlib.sha256((first / 'source.tar.gz').read_bytes()).hexdigest())
        with tarfile.open(fileobj=io.BytesIO((first / 'source.tar.gz').read_bytes()), mode='r:gz') as archive:
            self.assertEqual(archive.getnames(), ['haproxy-manager/' + p for p in self.names])
            for member in archive:
                self.assertTrue(member.isfile())
                body = archive.extractfile(member).read()
                self.assertEqual(body, (self.root / member.name.removeprefix('haproxy-manager/')).read_bytes())
        self.assertIn('Unmodified license and NOTICE text', (first / 'THIRD_PARTY_NOTICES.txt').read_text())

    def test_changed_locks_or_notices_require_review(self):
        for name in ['go.sum', 'web/pnpm-lock.yaml', 'legal/' + self.notice]:
            with self.subTest(name=name):
                path = self.root / name
                original = path.read_bytes()
                path.write_bytes(original + b'changed')
                with self.assertRaisesRegex(ValueError, 'changed'):
                    legal.check(self.root)
                path.write_bytes(original)

    def test_source_rejects_secrets_paths_and_symlinks(self):
        for name in ['../outside', '/etc/passwd', '.env', 'state.local/identity.json',
                     'node_modules/package/index.js', 'node.key', 'node.pem', '.build.local/state.json']:
            with self.subTest(name=name):
                self.assertFalse(legal.safe_source(name))
        (self.root / 'README.md').unlink()
        (self.root / 'README.md').symlink_to(self.root / 'LICENSE')
        with self.assertRaisesRegex(ValueError, 'Unsafe'):
            legal.check(self.root)

    def test_missing_notice_and_unknown_notice_paths_fail(self):
        self.manifest['components'][0]['notices'][0]['file'] = '../LICENSE'
        legal.dump(self.root / 'legal/dependencies.json', self.manifest)
        with self.assertRaisesRegex(ValueError, 'Invalid notice path'):
            legal.check(self.root)

    def test_new_source_cannot_silently_disappear_from_the_offer(self):
        (self.root / 'cmd').mkdir()
        (self.root / 'cmd/new.go').write_text('package main\n')
        with self.assertRaisesRegex(ValueError, 'Source files changed'):
            legal.check(self.root)

    def test_rebuilding_an_extracted_source_offer_does_not_include_its_previous_output(self):
        output = self.root / 'web/public/legal'
        legal.bundle(output, self.root)
        original = (output / 'source.tar.gz').read_bytes()
        legal.bundle(output, self.root)
        self.assertEqual((output / 'source.tar.gz').read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
