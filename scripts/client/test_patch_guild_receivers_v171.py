import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_receivers_v171 as p
from test_patch_fatigue_count_v164 import native, windows_image


class GuildV171Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_V171_CLIENT')
        if not path:
            raise unittest.SkipTest('requires exact V171 client')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = p.build(cls.before)

    def test_only_four_pairs_change_and_original_rejects_them(self):
        before = native(self.before, (0, 1), range(65536))
        after = native(self.after, (0, 1), range(65536))
        self.assertEqual([(i//65536, i%65536) for i in range(len(before)) if before[i] != after[i]], list(p.ADDED))
        for cls, op in p.ADDED:
            self.assertEqual((before[cls*65536+op], after[cls*65536+op]), (0, 1))
        self.assertEqual(native(self.before, (2, 255), range(65536)), native(self.after, (2, 255), range(65536)))

    def test_preserves_every_byte_outside_four_ranges(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        self.assertEqual(p.build(self.before)[0], self.after)
        opt, sections = p.pe.headers(self.after)
        self.assertEqual(p.pe.headers(self.before)[1], sections)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0], p.pe.checksum(self.after, opt+64))

    def test_exact_input_output_readback_and_mapping(self):
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
            self.assertEqual(p.pe.sha(self.source.read_bytes()), p.INPUT_SHA256)
            with self.assertRaises(ValueError):
                p.write_candidate(self.source, target)
            if os.name == 'nt':
                windows_image(target)


if __name__ == '__main__':
    unittest.main()
