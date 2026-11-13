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
