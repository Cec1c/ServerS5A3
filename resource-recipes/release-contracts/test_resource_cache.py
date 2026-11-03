import hashlib
from pathlib import Path
import tempfile
import unittest

from resource_cache import cached_resource


class ResourceCacheTests(unittest.TestCase):
    def test_missing_is_miss_and_does_not_write(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            self.assertEqual(cached_resource(path, path, '0' * 64, 1), {'hit': False})
            self.assertEqual(list(path.iterdir()), [])

    def test_verified_hit_and_existing_stage_preserved(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            payload = b'synthetic-resource'
            digest = hashlib.sha256(payload).hexdigest()
            (path / digest).write_bytes(payload)
            result = cached_resource(path, path, digest, len(payload))
            self.assertTrue(result['hit'])
            self.assertEqual((path / 'Script.pvf').read_bytes(), payload)
            with self.assertRaises(FileExistsError):
                cached_resource(path, path, digest, len(payload))
            self.assertEqual((path / 'Script.pvf').read_bytes(), payload)

    def test_corrupt_cache_is_retained_without_stage_write(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            (path / ('0' * 64)).write_bytes(b'wrong')
            with self.assertRaisesRegex(ValueError, 'corrupt'):
                cached_resource(path, path, '0' * 64, 5)
            self.assertTrue((path / ('0' * 64)).exists())
            self.assertFalse((path / 'Script.pvf').exists())

    def test_invalid_identity_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            for digest, size in [('../outside', 1), ('0' * 64, True), ('0' * 64, 0)]:
                with self.subTest(digest=digest, size=size), self.assertRaises(ValueError):
                    cached_resource(root, root, digest, size)


if __name__ == '__main__':
    unittest.main()
