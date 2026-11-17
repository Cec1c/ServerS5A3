from pathlib import Path
import shutil
import tempfile
import unittest

from check_channel_table import CSV, HISTORICAL, TABLE, check

ROOT = Path(__file__).resolve().parents[1]


class CommonChannelBaseline(unittest.TestCase):
    def test_complete_source_and_reference(self):
        result = check(ROOT)
        self.assertEqual(result['channels'], 120)
        self.assertEqual(result['fiendwar'], [244, 245, 246])
        self.assertTrue({10244, 10245, 10246}.issubset(result['ports']))
        self.assertTrue({10261, 10262, 10263}.isdisjoint(result['ports']))

    def stage(self, root):
        for name in (TABLE, HISTORICAL, CSV):
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)

    def test_csv_cannot_drift_from_runtime_table(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.stage(root)
            path = root / CSV
            path.write_bytes(path.read_bytes().replace(b'244,fiendwar,42,10244', b'261,fiendwar,42,10261'))
            with self.assertRaisesRegex(ValueError, 'CSV channel'):
                check(root)

    def test_either_table_cannot_drift(self):
        for name in (TABLE, HISTORICAL):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.stage(root)
                path = root / name
                path.write_bytes(path.read_bytes() + b'\r\n')
                with self.assertRaises(ValueError):
                    check(root)


if __name__ == '__main__':
    unittest.main()
