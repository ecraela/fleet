"""Container source recipes must be tied to exact signed-package provenance."""
import unittest
import io
from pathlib import Path
import tarfile
import tempfile

from container_sources import extract_recipe, packages


class ContainerSources(unittest.TestCase):
    def test_keeps_each_packages_actual_recipe_even_for_mixed_origin_versions(self):
        raw = ('P:ca-certificates\nV:2-r0\nL:MPL-2.0 AND MIT\no:ca-certificates\nc:' + 'a' * 40 +
               '\n\nP:ca-certificates-bundle\nV:1-r0\nL:MPL-2.0 AND MIT\no:ca-certificates\nc:' + 'b' * 40)
        result = packages(raw)
        self.assertEqual([p['commit'] for p in result], ['a' * 40, 'b' * 40])
        self.assertEqual([p['version'] for p in result], ['2-r0', '1-r0'])

    def test_missing_or_unsafe_source_provenance_is_refused(self):
        for origin, commit in [('../outside', 'a' * 40), ('busybox', ''), ('busybox', 'master')]:
            with self.subTest(origin=origin, commit=commit), self.assertRaises(ValueError):
                packages(f'P:busybox\nV:1-r0\nL:GPL-2.0-only\no:{origin}\nc:{commit}')
        with self.assertRaises(ValueError):
            packages('')

    def test_preserves_recipe_archive_and_materializes_only_local_regular_aliases(self):
        def recipe(link):
            raw = io.BytesIO()
            with tarfile.open(fileobj=raw, mode='w') as archive:
                entry = tarfile.TarInfo('main')
                entry.type = tarfile.DIRTYPE
                archive.addfile(entry)
                entry = tarfile.TarInfo('main/package/install')
                entry.size = 7
                archive.addfile(entry, io.BytesIO(b'fixture'))
                entry = tarfile.TarInfo('main/package/upgrade')
                entry.type, entry.linkname = tarfile.SYMTYPE, link
                archive.addfile(entry)
            return raw.getvalue()
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            raw = recipe('install')
            extract_recipe(raw, 'main/package', folder)
            self.assertEqual((folder / 'recipe.tar').read_bytes(), raw)
            self.assertEqual((folder / 'upgrade').read_bytes(), b'fixture')
            self.assertFalse((folder / 'upgrade').is_symlink())
            for link in ('/etc/passwd', '../../outside'):
                with self.assertRaisesRegex(ValueError, 'escapes'):
                    extract_recipe(recipe(link), 'main/package', folder)


if __name__ == '__main__':
    unittest.main()
