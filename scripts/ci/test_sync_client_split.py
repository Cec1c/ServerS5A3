import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import sync_client_releases as sync


class SplitReleaseTests(unittest.TestCase):
    def fixture(self, directory, members=None):
        root = Path(directory)
        archive = root / 'original.zip'
        with zipfile.ZipFile(archive, 'w') as z:
            for name, data in (members or {'client/DNF.exe': b'MZsample', 'client/Script.pvf': b'pvf bytes'}).items():
                z.writestr(name, data)
        data = archive.read_bytes()
        half = len(data) // 2
        assets = {}
        for i, chunk in enumerate((data[:half], data[half:]), 1):
            name = f'client-bundle.zip.{i:03}'
            path = root / name
            path.write_bytes(chunk)
            assets[name] = {'length': len(chunk), 'sha256': hashlib.sha256(chunk).hexdigest(), 'path': str(path),
                            'split_archive': {'count': 2, 'index': i, 'length': len(data), 'sha256': sync.digest(archive)}}
        return assets

    def test_split_pvf_bundle_and_standalone_roundtrip(self):
        with tempfile.TemporaryDirectory() as directory:
            assets = self.fixture(directory)
            assets['client.zip'] = {'exe': {'sha256': hashlib.sha256(b'MZsample').hexdigest(), 'length': 8}}
            sync.inspect_split_archives(assets, directory)
            item = {'tag': 'client-v146', 'commit': 'a' * 40, 'source': 'https://example.test', 'assets': assets}
            record = sync.release_record(item)
            self.assertEqual(record['assets']['client-bundle.zip.001']['split_archive'], assets['client-bundle.zip.001']['split_archive'])
            self.assertNotIn('path', record['assets']['client-bundle.zip.001'])
            self.assertFalse(list(Path(directory).glob('split-*')))

    def test_identity_keeps_parts_and_validates_combined_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            assets = self.fixture(directory)
            item = {'tag': 'client-v146', 'commit': 'a' * 40, 'source': 'https://example.test', 'assets': assets}
            record = sync.release_record(item)
            row = {'tag_name': item['tag'], 'commit': {'id': item['commit']}, 'description': sync.MARKER + json.dumps(record) + ' -->',
                   '_links': {'self': item['source']}, 'assets': {'links': [{'url': 'https://gitgud.io/api/v4/projects/1/packages/generic/a/' + n} for n in assets]}}
            with patch.object(sync, 'download', side_effect=lambda url, *args: Path(assets[url.rsplit('/', 1)[-1]]['path'])):
                observed = sync.Repository('up', 'gitlab', 1).identity(row, directory)
                self.assertEqual(sync.release_record(observed), record)

    def test_missing_conflicting_or_tampered_parts_refused(self):
        for case in ('missing', 'count', 'length', 'digest', 'part-bytes', 'renamed-index', 'bool', 'huge', 'extra-key'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                assets = self.fixture(directory)
                a = assets['client-bundle.zip.002']
                if case == 'missing': del assets['client-bundle.zip.002']
                elif case == 'count': a['split_archive']['count'] = 3
                elif case == 'length': a['split_archive']['length'] += 1
                elif case == 'digest':
                    for v in assets.values(): v['split_archive']['sha256'] = '0' * 64
                elif case == 'part-bytes': Path(a['path']).write_bytes(b'x')
                elif case == 'renamed-index': a['split_archive']['index'] = 1
                elif case == 'bool': a['split_archive']['index'] = True
                elif case == 'huge': a['split_archive']['length'] = sync.MAX_ASSET + 1
                elif case == 'extra-key': a['split_archive']['extra'] = 1
                with self.assertRaises(sync.SyncError): sync.inspect_split_archives(assets, directory)

    def test_split_without_provenance_rejected_before_download(self):
        row = {'tag_name': 'client-v146', 'commit': {'id': 'a'*40}, 'description': '', '_links': {'self': 'https://example.test'},
               'assets': {'links': [{'url': 'https://gitgud.io/api/v4/projects/1/packages/generic/a/client.zip.001'}]}}
        with patch.object(sync, 'download') as download:
            with self.assertRaises(sync.SyncError): sync.Repository('up', 'gitlab', 1).identity(row, '.')
            download.assert_not_called()

    def test_bundle_keeps_path_type_pvf_and_executable_guards(self):
        for members in (
            {'client/DNF.exe': b'MZsample'},
            {'client/DNF.exe': b'MZsample', 'client/Script.pvf': b''},
            {'client/DNF.exe': b'bad', 'client/Script.pvf': b'pvf'},
            {'client/DNF.exe': b'MZsample', '../Script.pvf': b'pvf'},
            {'client/DNF.exe': b'MZsample', 'other/Script.pvf': b'pvf'},
            {'client/DNF.exe': b'MZsample', 'client/Script.pvf': b'pvf', 'scripts/run.exe': b'MZbad'},
        ):
            with self.subTest(members=list(members)), tempfile.TemporaryDirectory() as directory:
                assets = self.fixture(directory, members)
                with self.assertRaises(sync.SyncError): sync.inspect_split_archives(assets, directory)

    def test_bundle_exe_must_match_standalone(self):
        with tempfile.TemporaryDirectory() as directory:
            assets = self.fixture(directory)
            assets['client.zip'] = {'exe': {'length': 8, 'sha256': 'a'*64}}
            with self.assertRaisesRegex(sync.SyncError, 'identities differ'): sync.inspect_split_archives(assets, directory)

    def test_regular_exe_archive_still_rejects_pvf(self):
        with tempfile.TemporaryDirectory() as directory:
            self.fixture(directory)
            with self.assertRaisesRegex(sync.SyncError, 'Unexpected client ZIP member'):
                sync.inspect_zip(Path(directory)/'original.zip')


if __name__ == '__main__':
    unittest.main()
