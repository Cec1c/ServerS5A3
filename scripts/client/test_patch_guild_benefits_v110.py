import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_benefits_v110 as patch


@unittest.skipUnless(os.environ.get("DNF_GUILD_PURCHASE_FIXED_EXE"), "requires exact menu/notice/cargo/appearance V110 derivative")
class GuildBenefitsPatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path = Path(os.environ["DNF_GUILD_PURCHASE_FIXED_EXE"])
        cls.before = cls.path.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_only_query_receive_bits_and_checksum_change(self):
        _, opt, _, sections = patch.headers(self.before)
        pos = patch.offset_for_rva(sections, patch.BITMAP_RVA, 185)
        added, removed = [], []
        for op in range(1480):
            before = bool(self.before[pos + op // 8] & (1 << (op % 8)))
            after = bool(self.after[pos + op // 8] & (1 << (op % 8)))
            if after and not before: added.append(op)
            if before and not after: removed.append(op)
        self.assertEqual(added, [487, 488])
        self.assertEqual(removed, [])
        for op in [65, 761, 237, 261, 262, 787, 1300, 1301, 1474]:
            self.assertTrue(self.after[pos + op // 8] & (1 << (op % 8)))
        for op in [247, 248, 249, 265, 832, 1302, 1303, 1304]:
            self.assertFalse(self.after[pos + op // 8] & (1 << (op % 8)))
        expected = bytearray(self.before)
        expected[pos + 60] |= 0x80
        expected[pos + 61] |= 0x01
        expected[opt + 64:opt + 68] = self.after[opt + 64:opt + 68]
        self.assertEqual(bytes(expected), self.after)
        self.assertEqual(struct.unpack_from("<I", self.after, opt + 64)[0], patch.checksum(self.after, opt + 64))

    def test_unknown_and_reapplication_rejected(self):
        for data in [b"unknown", self.after, self.before[:-1]]:
            with self.assertRaises(ValueError): patch.build(data)

    def test_new_file_only_and_hash_readback(self):
        with self.assertRaises(ValueError): patch.write_candidate(self.path, self.path)
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "candidate.exe"
            receipt = patch.write_candidate(self.path, target)
            self.assertEqual(receipt["output_sha256"], patch.sha(target.read_bytes()))
            with self.assertRaises(ValueError): patch.write_candidate(self.path, target)
            self.assertEqual(patch.sha(self.path.read_bytes()), patch.INPUT_SHA256)


if __name__ == "__main__":
    unittest.main()
