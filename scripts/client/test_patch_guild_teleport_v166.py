"""Exact receive gate delta; all other class/opcode pairs stay unchanged."""
import os
from pathlib import Path
import tempfile
import unittest
import patch_guild_teleport_v166 as p
from test_patch_fatigue_count_v164 import native,windows_image

class GuildTeleportGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('DNF_GUILD_TELEPORT_INPUT_EXE')
        if not path:raise unittest.SkipTest('requires exact lifecycle candidate')
        cls.path=Path(path);cls.before=cls.path.read_bytes();cls.after,cls.receipt=p.build(cls.before)
    def test_full_gate_delta(self):
        before=native(self.before,(0,1),range(65536));after=native(self.after,(0,1),range(65536))
        self.assertEqual([(i//65536,i%65536) for i in range(len(before)) if before[i]!=after[i]],[(0,377),(1,434)])
        self.assertEqual(native(self.before,(2,255),(0,377,434,435,65535)),native(self.after,(2,255),(0,377,434,435,65535)))
    def test_exact_changes_and_image_readback(self):
        cursor=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
        self.assertEqual(self.before[cursor:],self.after[cursor:]);self.assertEqual(len(self.before),len(self.after))
        with tempfile.TemporaryDirectory()as directory:
            path=Path(directory)/'DNF.exe';p.write_candidate(self.path,path);self.assertEqual(path.read_bytes(),self.after)
            if os.name=='nt':windows_image(path)
        with self.assertRaises(ValueError):p.build(self.after)
        with self.assertRaises(ValueError):p.write_candidate(self.path,self.path)

if __name__=='__main__':unittest.main()
