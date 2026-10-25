"""Run original receiver tests on the composition and check all receive gates."""
import os
from pathlib import Path
import tempfile
import unittest
import struct

import patch_guild_economy_war_v173 as combined
import test_guild_war_refresh_v173 as war_tests
from test_patch_fatigue_count_v164 import native, windows_image


class CombinedGuildV173Tests(war_tests.GuildWarV173Tests):
    @classmethod
    def setUpClass(cls):
        name = os.environ.get('DNF_GUILD_WAR_V173_CLIENT')
        if not name:
            raise unittest.SkipTest('set DNF_GUILD_WAR_V173_CLIENT to exact V173')
        cls.source = Path(name)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = combined.build(cls.before)
        cls.original, cls.fixed = war_tests.WarNative(cls.before), war_tests.WarNative(cls.after)

    def test_07_exact_patch_and_original_gate_matrix(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:len(self.before)])
        for packet_class in (0, 1, 2, 255):
            before = native(self.before, (packet_class,), range(65536))
            after = native(self.after, (packet_class,), range(65536))
            changed = [op for op in range(65536) if before[op] != after[op]]
            self.assertEqual(changed, [1302, 1304] if packet_class == 1 else [])
            for op in changed:
                self.assertEqual((before[op], after[op]), (0, 1))

    def test_08_independent_output_and_windows_mapping(self):
        for invalid in (self.after, self.before[:-1], b'X' + self.before[1:]):
            with self.assertRaises(ValueError):
                combined.build(invalid)
        with self.assertRaises(ValueError):
            combined.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'DNF.exe'
            receipt = combined.write_candidate(self.source, path)
            self.assertEqual(path.read_bytes(), self.after)
            self.assertEqual(receipt, self.receipt)
            with self.assertRaises(ValueError):
                combined.write_candidate(self.source, path)
            if os.name == 'nt':
                windows_image(path)
        opt, _ = combined.hongbao.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt + 64)[0],
                         combined.hongbao.pe.checksum(self.after, opt + 64))


if __name__ == '__main__':
    unittest.main(defaultTest='CombinedGuildV173Tests')
