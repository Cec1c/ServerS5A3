import os
from pathlib import Path
import tempfile
import unittest
import patch_guild_member_ranking_v179 as p
from test_patch_fatigue_count_v164 import native, windows_image


class GuildMemberRankingGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        paths = os.environ.get('DNF_MEMBER_RANKING_GATE_INPUT_EXES','').split(os.pathsep)
        cls.paths = [Path(x) for x in paths if x]
        if not cls.paths:
            raise unittest.SkipTest('requires exact V179 social and controls cumulative candidate')

    def test_only_ranking_response_changes(self):
        for path in self.paths:
            with self.subTest(input=path.name):
                before = path.read_bytes()
                after, _ = p.build(before)
                a = native(before,(0,1),range(65536))
                b = native(after,(0,1),range(65536))
                self.assertEqual([(i//65536,i%65536) for i in range(len(a)) if a[i]!=b[i]],[(1,1505)])
                self.assertEqual(native(before,(2,255),(0,303,377,434,1297,1350,1505,65535)),native(after,(2,255),(0,303,377,434,1297,1350,1505,65535)))

    def test_exact_diff_and_windows_image(self):
        for source in self.paths:
            before = source.read_bytes()
            after, receipt = p.build(before)
            cursor = 0
            for lo,hi in receipt['changedRanges']:
                self.assertEqual(before[cursor:lo],after[cursor:lo])
                cursor=hi
            self.assertEqual(before[cursor:],after[cursor:])
            with tempfile.TemporaryDirectory() as folder:
                target=Path(folder)/'DNF.exe'
                p.write_candidate(source,target)
                self.assertEqual(target.read_bytes(),after)
                if os.name=='nt': windows_image(target)
                with self.assertRaises(ValueError): p.write_candidate(source,target)
            with self.assertRaises(ValueError): p.build(after)
            with self.assertRaises(ValueError): p.write_candidate(source,source)


if __name__=='__main__': unittest.main()
