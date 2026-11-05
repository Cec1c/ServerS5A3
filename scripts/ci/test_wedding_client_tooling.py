"""无客户端资源的收包工具契约检查；真实x86矩阵另用精确模块验证。"""
from pathlib import Path
import struct
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'client'))
import patch_wedding_receive_complete as patch


class WeddingClientTools(unittest.TestCase):
    def test_exact_stage_chain_and_pair_inventory(self):
        self.assertEqual(tuple(s.OUTPUT_SHA256 for s in patch.STAGES[:-1]), patch.INPUTS[1:])
        self.assertEqual(len(patch.TARGETS), 41)
        self.assertEqual(len(patch.ADDITIONS), 39)
        self.assertIn((0, 1001), patch.ADDITIONS)
        self.assertNotIn((0, 1010), patch.TARGETS)
        self.assertNotIn((1, 1054), patch.TARGETS)
        self.assertEqual(set(patch.CLASS1), set(range(1032, 1054)))

    def test_helper_sizes_and_tail_fallbacks(self):
        first, full, ring = patch.STAGES
        for code, entry, target, size in ((first.helper(), first.NEW, first.OLD, 249),
                (full.helper(), full.NEW, full.OLD, 293), (ring.adapter(True), ring.ENTRY, ring.FALLBACK, 304)):
            self.assertEqual(len(code), size)
            self.assertEqual(code[-13:-5], bytes.fromhex('b801000000c20400'))
            self.assertEqual(code[-5], 0xE9)
            self.assertEqual(entry + size + struct.unpack_from('<i', code, size-4)[0], target)
        self.assertEqual(full.helper(), ring.adapter(False))

    def test_unknown_or_malformed_input_is_rejected(self):
        for data in (b'', b'MZ', bytes(4096)):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                patch.build(data)

    def test_in_place_existing_output_and_receipt_overlap_are_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder)/'source.exe'
            source.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(source, source)
            output = Path(folder)/'output.exe'
            output.write_bytes(b'keep')
            with self.assertRaises(ValueError):
                patch.write_candidate(source, output)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, Path(folder)/'new.exe', source)
            self.assertEqual(source.read_bytes(), b'preserve')
            self.assertEqual(output.read_bytes(), b'keep')


if __name__ == '__main__':
    unittest.main()
