import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_hongbao_v173 as p
from test_patch_fatigue_count_v164 import native, windows_image


class GuildHongbaoGateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_HONGBAO_CLIENT')
        if not path:
            raise unittest.SkipTest('requires exact cumulative V173 image')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = p.build(cls.before)

    def test_only_two_pairs_in_complete_gate_matrix_and_abi(self):
        before = native(self.before, (0, 1), range(65536))
        after = native(self.after, (0, 1), range(65536))
        changes = [(i // 65536, i % 65536) for i in range(len(before)) if before[i] != after[i]]
        self.assertEqual(changes, [(1, 1302), (1, 1304)])
        for op in (1302, 1304):
            self.assertEqual((before[65536 + op], after[65536 + op]), (0, 1))
        for cls, op in ((0, 1284), (0, 1297), (1, 1350), (1, 1503), (1, 1504),
                        (1, 1300), (1, 1301), (1, 1303)):
            self.assertEqual((before[cls * 65536 + op], after[cls * 65536 + op]), (1, 1))
        self.assertEqual(native(self.before, (2, 255), range(65536)),
                         native(self.after, (2, 255), range(65536)))

    def test_exact_ranges_checksum_and_rebuild(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        self.assertEqual(p.build(self.before)[0], self.after)
        opt, sections = p.pe.headers(self.after)
        self.assertEqual(p.pe.headers(self.before)[1], sections)
        self.assertEqual(struct.unpack_from('<I', self.after, opt + 64)[0], p.pe.checksum(self.after, opt + 64))

    def test_strict_input_readback_and_windows_mapping(self):
        for bad in (self.before[:-1], self.after, bytes([0]) + self.before[1:]):
            with self.assertRaises(ValueError):
                p.build(bad)
        with self.assertRaises(ValueError):
            p.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / 'DNF.exe'
            receipt = p.write_candidate(self.source, target)
            self.assertEqual(target.read_bytes(), self.after)
            self.assertEqual(receipt['outputSha256'], p.pe.sha(self.after))
            with self.assertRaises(ValueError):
                p.write_candidate(self.source, target)
            if os.name == 'nt':
                windows_image(target)


if __name__ == '__main__':
    unittest.main()
