import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import sync_client_releases as sync


class SelectedFeedbackReleaseAdmission(unittest.TestCase):
    def test_reviewed_new_receiver_requires_the_complete_exact_sidecar_set(self):
        exe = b'MZselected arena and wedding receiver'
        dependency = b'fixed selected sidecar'
        pin = {'sha256': hashlib.sha256(exe).hexdigest(), 'length': len(exe)}
        sidecars = {'CombatPower.dll': {'sha256': hashlib.sha256(dependency).hexdigest(), 'length': len(dependency)}}
        with tempfile.TemporaryDirectory() as directory, patch.object(sync, 'PAIRED_FEEDBACK_EXES', (pin,)), patch.object(sync, 'NATIVE_CLIENT_SIDECARS', sidecars):
            for mode in ('complete', 'missing', 'wrong-bytes', 'unknown-exe'):
                path = Path(directory) / (mode+'.zip')
                with zipfile.ZipFile(path, 'w') as archive:
                    archive.writestr('DNF.exe', exe if mode != 'unknown-exe' else b'MZunreviewed')
                    if mode != 'missing':
                        archive.writestr('CombatPower.dll', dependency if mode != 'wrong-bytes' else b'bad')
                if mode == 'complete':
                    self.assertEqual(sync.inspect_zip(path), pin)
                else:
                    with self.subTest(mode=mode), self.assertRaises(sync.SyncError):
                        sync.inspect_zip(path)

    def test_release_and_paired_resource_identity_lists_agree(self):
        import importlib.util
        root = Path(__file__).resolve().parents[2]
        spec = importlib.util.spec_from_file_location('selected_pairing_identity', root/'deploy/remote/client_pairing.py')
        pairing = importlib.util.module_from_spec(spec);spec.loader.exec_module(pairing)
        for row in sync.PAIRED_FEEDBACK_EXES:
            self.assertEqual(pairing.FEATURE_CLIENT_VARIANTS[row['sha256']]['DNF.exe'],
                             {'sha256': row['sha256'], 'bytes': row['length']})


if __name__ == '__main__':
    unittest.main()
