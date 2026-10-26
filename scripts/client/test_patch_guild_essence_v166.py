"""Exact image, receive matrix, ABI and non-executable PE mapping contracts."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_essence_v166 as patch
from test_patch_fatigue_count_v164 import native, windows_image


class EssenceV166Tests(unittest.TestCase):
    input_variable = 'DNF_ESSENCE_V166_CLIENT'
    expected_input = patch.INPUT_SHA256
    expected_output = '37b2810b03ef3c249a0ec6c680e74e74f1712d3f9c78f93472386399df47048e'

    @classmethod
    def setUpClass(cls):
        path = os.environ.get(cls.input_variable)
        if not path:
            raise unittest.SkipTest('exact V166 image required')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        if patch.pe.sha(cls.before) != cls.expected_input:
            raise ValueError('test input does not match its profile')
        cls.after, cls.receipt = patch.build(cls.before)

    def test_only_declared_ranges_and_checksum(self):
        if self.expected_output is not None:
            self.assertEqual(self.receipt['outputSha256'], self.expected_output)
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        opt, sections = patch.pe.headers(self.before)
        self.assertEqual(sections, patch.pe.headers(self.after)[1])
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0],
                         patch.pe.checksum(self.after, opt+64))

    def test_complete_receive_matrix_preserves_every_other_message(self):
        before = native(self.before, (0,1), range(65536))
        after = native(self.after, (0,1), range(65536))
        self.assertEqual([(i//65536,i%65536) for i in range(len(before)) if before[i]!=after[i]],
                         [(0,1284)])
        self.assertEqual([before[1284],after[1284]], [0,1])
        ops = (0,541,741,742,777,778,779,784,1261,1280,1281,1283,1284,1293,1297,1337,1345,1350,1355,65535)
        self.assertEqual(native(self.before,(2,255),ops), native(self.after,(2,255),ops))

    def test_wrong_hash_and_output_guards(self):
        modified = bytearray(self.before)
        modified[-1] ^= 1
        for data in (bytes(modified),self.before[:-1],self.after):
            with self.assertRaises(ValueError):
                patch.build(data)
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as td:
            dst=Path(td)/'DNF.exe'
            dst.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source,dst)
            self.assertEqual(dst.read_bytes(),b'preserve')

    @unittest.skipUnless(os.name=='nt','Windows SEC_IMAGE_NO_EXECUTE required')
    def test_disk_readback_and_windows_mapping(self):
        windows_image(self.source)
        with tempfile.TemporaryDirectory() as td:
            dst=Path(td)/'DNF.exe'
            patch.write_candidate(self.source,dst)
            self.assertEqual(patch.pe.sha(dst.read_bytes()),self.receipt['outputSha256'])
            windows_image(dst)


if __name__=='__main__':
    unittest.main()
