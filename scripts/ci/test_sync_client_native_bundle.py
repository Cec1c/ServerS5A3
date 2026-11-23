import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import sync_client_releases as sync


class NativeClientBundleTests(unittest.TestCase):
    def setUp(self):
        self.exe = b'MZpaired-native-client-fixture'
        self.sidecars = {'CombatPower.dll': b'power', 'DpsMeter.dll': b'dps',
                         'ImagePacks2/sprite_interface_private_features20261113.NPK': b'skin'}
        def pin(raw):
            return {'sha256': hashlib.sha256(raw).hexdigest(), 'length': len(raw)}
        self.exe_pin = pin(self.exe)
        self.pins = {name: pin(raw) for name, raw in self.sidecars.items()}
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.archive = Path(self.temp.name) / 'client.zip'
        self.patch_exe = patch.object(sync, 'NATIVE_CLIENT_EXE', self.exe_pin)
        self.patch_sidecars = patch.object(sync, 'NATIVE_CLIENT_SIDECARS', self.pins)
        self.patch_exe.start(); self.patch_sidecars.start()
        self.addCleanup(self.patch_exe.stop); self.addCleanup(self.patch_sidecars.stop)

    def write(self, files):
        with zipfile.ZipFile(self.archive, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
            for name, raw in files:
                archive.writestr(name, raw)

    def files(self):
        return [('DNF.exe', self.exe), *self.sidecars.items(), ('manifest.json', b'{}')]

    def test_complete_exact_bundle_is_accepted(self):
        self.write(self.files())
        self.assertEqual(sync.inspect_zip(self.archive), self.exe_pin)

    def test_cumulative_bundle_requires_exact_atlas_and_pvf(self):
        raw = b'MZcumulative-fixture'
        exe = {'sha256': hashlib.sha256(raw).hexdigest(), 'length': len(raw)}
        extra = {'AtlasWorkers101.dll': b'workers', 'Script.pvf': b'public-pvf'}
        pins = {name: {'sha256': hashlib.sha256(value).hexdigest(), 'length': len(value)}
                for name, value in extra.items()}
        files = [('DNF.exe', raw), *self.sidecars.items(), *extra.items()]
        with patch.object(sync, 'CUMULATIVE_NATIVE_EXE', exe), patch.object(sync, 'CUMULATIVE_NATIVE_EXTRA', pins):
            self.write(files)
            self.assertEqual(sync.inspect_zip(self.archive), exe)
            for name, value in extra.items():
                with self.subTest(missing=name):
                    self.write([(n, v) for n, v in files if n != name])
                    with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
                        sync.inspect_zip(self.archive)
                with self.subTest(changed=name):
                    self.write([(n, b'x' + v[1:] if n == name else v) for n, v in files])
                    with self.assertRaisesRegex(sync.SyncError, 'sidecar'):
                        sync.inspect_zip(self.archive)
            self.write([('DNF.exe', self.exe), *self.sidecars.items(), *extra.items()])
            with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
                sync.inspect_zip(self.archive)

    def test_recognized_exe_requires_all_dependencies_even_when_none_supplied(self):
        self.write([('DNF.exe', self.exe)])
        with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
            sync.inspect_zip(self.archive)

    def test_formal_derivative_uses_the_same_exact_sidecars(self):
        raw = b'MZformal-derivative-fixture'
        pin = {'sha256': hashlib.sha256(raw).hexdigest(), 'length': len(raw)}
        with patch.object(sync, 'FORMAL_BLACKDIAMOND_EXE', pin):
            self.write([(n, raw if n == 'DNF.exe' else v) for n, v in self.files()])
            self.assertEqual(sync.inspect_zip(self.archive), pin)
            self.write([('DNF.exe', raw)])
            with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
                sync.inspect_zip(self.archive)

    def test_legacy_exe_only_bundle_keeps_existing_behavior(self):
        self.write([('DNF.exe', b'MZlegacy'), ('README.txt', b'legacy')])
        self.assertEqual(sync.inspect_zip(self.archive)['length'], 8)

    def test_missing_paired_file_is_rejected(self):
        for name in self.sidecars:
            with self.subTest(name=name):
                self.write([(n, raw) for n, raw in self.files() if n != name])
                with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
                    sync.inspect_zip(self.archive)

    def test_sidecar_length_and_digest_are_checked(self):
        for name, raw in self.sidecars.items():
            for changed in (raw + b'x', bytes([raw[0] ^ 1]) + raw[1:]):
                with self.subTest(name=name, changed=changed):
                    self.write([(n, changed if n == name else value) for n, value in self.files()])
                    with self.assertRaisesRegex(sync.SyncError, 'sidecar'):
                        sync.inspect_zip(self.archive)

    def test_sidecars_cannot_pair_with_another_exe(self):
        self.write([(n, b'MZother' if n == 'DNF.exe' else raw) for n, raw in self.files()])
        with self.assertRaisesRegex(sync.SyncError, 'complete exact'):
            sync.inspect_zip(self.archive)

    def test_other_dll_and_skin_paths_stay_rejected(self):
        for path in ('Other.dll', 'x/CombatPower.dll', 'DPSMETER.dll',
                     'ImagePacks2/other.NPK', 'ImagePacks2/../CombatPower.dll'):
            with self.subTest(path=path):
                self.write(self.files() + [(path, b'x')])
                with self.assertRaises(sync.SyncError):
                    sync.inspect_zip(self.archive)

    def test_duplicate_case_alias_stays_rejected(self):
        self.write(self.files() + [('combatpower.DLL', self.sidecars['CombatPower.dll'])])
        with self.assertRaisesRegex(sync.SyncError, 'Duplicate'):
            sync.inspect_zip(self.archive)


if __name__ == '__main__':
    unittest.main()
