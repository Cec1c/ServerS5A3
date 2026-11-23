import copy
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import sync_client_releases as sync


class MirrorSizeTests(unittest.TestCase):
    def fixture(self, directory):
        path = Path(directory) / 'client.zip'
        path.write_bytes(b'0123456789' * 23)
        return {'tag': 'client-v217', 'commit': 'a' * 40, 'preview': True,
                'assets': {'client.zip': {'sha256': sync.digest(path), 'length': path.stat().st_size,
                                         'path': str(path), 'exe': {'sha256': 'b' * 64, 'length': 5}}}}

    def split(self, item, directory):
        with patch.object(sync, 'GITGUD_PART_BYTES', 100):
            return sync.gitgud_transport_item(item, directory)

    def test_split_preserves_original_bytes_and_logical_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            original = self.fixture(directory)
            before = copy.deepcopy(original)
            split = self.split(original, directory)
            self.assertEqual(original, before)
            self.assertEqual(list(split['assets']), ['client.zip.001', 'client.zip.002', 'client.zip.003'])
            self.assertTrue(all(a['length'] <= 100 for a in split['assets'].values()))
            joined = b''.join(Path(a['path']).read_bytes() for a in split['assets'].values())
            self.assertEqual(hashlib.sha256(joined).hexdigest(), original['assets']['client.zip']['sha256'])
            self.assertEqual(sync.content_identity(original), sync.content_identity(split))
            self.assertEqual(sync.plan({'github': [original], 'gitgud': [split]}, []), [])

    def test_source_change_is_rejected_before_parts_are_written(self):
        with tempfile.TemporaryDirectory() as directory:
            item = self.fixture(directory)
            Path(item['assets']['client.zip']['path']).write_bytes(b'changed')
            with self.assertRaises(sync.SyncError):
                self.split(item, directory)
            self.assertFalse(list(Path(directory).rglob('*.001')))

    def test_small_and_existing_split_are_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            item = self.fixture(directory)
            self.assertEqual(sync.gitgud_transport_item(item, directory), item)
            split = self.split(item, directory)
            self.assertEqual(self.split(split, directory), split)

    def test_missing_or_conflicting_part_cannot_hide_a_conflict(self):
        for defect in ('missing', 'sha', 'length', 'extra'):
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as directory:
                item = self.fixture(directory)
                split = self.split(item, directory)
                part = split['assets']['client.zip.001']
                if defect == 'missing': del split['assets']['client.zip.003']
                if defect == 'sha': part['split_archive']['sha256'] = 'c' * 64
                if defect == 'length': part['length'] += 1
                if defect == 'extra': split['assets']['client.zip'] = item['assets']['client.zip']
                with self.assertRaises(sync.SyncError): sync.content_identity(split)

    def test_different_complete_archive_remains_a_conflict(self):
        with tempfile.TemporaryDirectory() as directory:
            item = self.fixture(directory)
            split = self.split(item, directory)
            for asset in split['assets'].values(): asset['split_archive']['sha256'] = 'c' * 64
            with self.assertRaises(sync.SyncError):
                sync.plan({'github': [item], 'gitgud': [split]}, [])


if __name__ == '__main__':
    unittest.main()
