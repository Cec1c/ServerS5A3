import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_war_missions_v194 as patch
from test_patch_fatigue_count_v164 import native, windows_image


class GuildWarMissionGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_WAR_V194_CLIENT')
        if not path:
            raise unittest.SkipTest('exact cumulative V194 image required')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_complete_class_zero_one_gate_and_abi(self):
        before = native(self.before, (0,1), range(65536))
        after = native(self.after, (0,1), range(65536))
        self.assertEqual([(i//65536,i%65536) for i in range(len(before)) if before[i] != after[i]], [(0,1283)])
        self.assertEqual((before[1283],after[1283]),(0,1))
        self.assertEqual(native(self.before,(2,255),(0,1283,1297,1346,65535)),
                         native(self.after,(2,255),(0,1283,1297,1346,65535)))

    def test_cumulative_bytes_checksum_and_image_mapping(self):
        cursor = 0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:],self.after[cursor:])
        self.assertEqual(len(self.before),len(self.after))
        opt, sections = patch.pe.headers(self.after)
        self.assertEqual(sections,patch.pe.headers(self.before)[1])
        self.assertEqual(struct.unpack_from('<I',self.after,opt+64)[0],patch.pe.checksum(self.after,opt+64))
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder)/'DNF.exe'
            patch.write_candidate(self.source,target)
            self.assertEqual(target.read_bytes(),self.after)
            if os.name == 'nt': windows_image(target)
            with self.assertRaises(ValueError): patch.write_candidate(self.source,target)
        with self.assertRaises(ValueError): patch.build(self.after)
        with self.assertRaises(ValueError): patch.build(self.before[:-1])
        with self.assertRaises(ValueError): patch.write_candidate(self.source,self.source)


if __name__ == '__main__': unittest.main()
