"""Full receive gate/ABI, PE preservation and independent Windows image mapping."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_disband_v166 as p
from test_patch_fatigue_count_v164 import native, windows_image


class GuildDisbandGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_DISBAND_INPUT_EXE')
        if not path:
            raise unittest.SkipTest('requires exact V166 guild-grade candidate')
        cls.source=Path(path)
        cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=p.build(cls.before)

    def test_exact_ranges_headers_and_checksum(self):
        cursor=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo])
            cursor=hi
        self.assertEqual(self.before[cursor:],self.after[cursor:])
        self.assertEqual(len(self.before),len(self.after))
        opt,sections=p.pe.headers(self.before)
        self.assertEqual(sections,p.pe.headers(self.after)[1])
        self.assertEqual(struct.unpack_from('<I',self.after,opt+64)[0],p.pe.checksum(self.after,opt+64))

    def test_full_native_gate_and_abi(self):
        before=native(self.before,(0,1),range(65536))
        after=native(self.after,(0,1),range(65536))
        self.assertEqual([(i//65536,i%65536) for i in range(len(before)) if before[i]!=after[i]],[(1,303)])
        self.assertEqual((before[65536+303],after[65536+303]),(0,1))
        self.assertEqual(native(self.before,(2,255),(0,151,153,303,541,830,65535)),native(self.after,(2,255),(0,151,153,303,541,830,65535)))

    def test_input_guard_and_output_readback(self):
        for data in (self.after,self.before[:-1]):
            with self.assertRaises(ValueError): p.build(data)
        with self.assertRaises(ValueError): p.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as directory:
            target=Path(directory)/'DNF.exe'
            p.write_candidate(self.source,target)
            self.assertEqual(target.read_bytes(),self.after)
            with self.assertRaises(ValueError): p.write_candidate(self.source,target)
            if os.name=='nt': windows_image(target)


if __name__=='__main__': unittest.main()
