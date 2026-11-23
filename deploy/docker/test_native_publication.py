import importlib.util
from pathlib import Path
import unittest
import manage

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('native_pairing', ROOT / 'deploy/remote/client_pairing.py')
pairing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pairing)
EXE = '19233532acda5b6cea91e70662157a34dac769f6a5755222fe04fbd8cd41019f'


class NativePublication(unittest.TestCase):
    def test_cumulative_client_keeps_automatic_protocol_features(self):
        self.assertEqual(manage.BUFF_GROUP_CLIENTS.get(EXE), 'on')
        self.assertIn(EXE, manage.BLACK_DIAMOND_SECRET_SHOP_CLIENTS)
        self.assertIn(EXE, manage.TAG_BUFF_CLIENTS)
        self.assertIn(EXE, manage.RAID_SHOP_CLIENTS)

    def test_cumulative_release_requires_atlas_and_original_components(self):
        pins = pairing.FEATURE_CLIENT_VARIANTS.get(EXE)
        self.assertIsNotNone(pins)
        pair = dict(kind='paired', exeSha256=EXE, clientFiles=pins)
        self.assertEqual(pairing.checked_client_files(pair), pins)
        self.assertEqual(set(pins), {'DNF.exe', 'AtlasWorkers101.dll', 'DpsMeter.dll',
                         'CombatPower.dll', 'ImagePacks2/sprite_interface_private_features20261113.NPK'})
        for missing in ('AtlasWorkers101.dll', 'DpsMeter.dll', 'CombatPower.dll'):
            with self.subTest(missing=missing), self.assertRaises(ValueError):
                pairing.checked_client_files(pair | {'clientFiles': {k: v for k, v in pins.items() if k != missing}})
        with self.assertRaises(ValueError):
            pairing.checked_client_files(pair | {'clientFiles': pins | {'arbitrary.dll': pins['AtlasWorkers101.dll']}})


if __name__ == '__main__':
    unittest.main()
