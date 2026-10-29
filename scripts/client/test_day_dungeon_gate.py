"""周常接收门补丁：真实输入、字节范围、ABI及拒绝覆盖回归。"""
import os
from pathlib import Path
import struct
import tempfile
import unittest

import capstone
import pefile
import patch_day_dungeon_gate as patch


class DayDungeonGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        raw_path = os.environ.get('DNF_DAY_DUNGEON_INPUT_EXE')
        if not raw_path:
            raise unittest.SkipTest('需要DNF_DAY_DUNGEON_INPUT_EXE指向已核对的原始EXE')
        cls.source = Path(raw_path)
        cls.original = cls.source.read_bytes()
        cls.candidate, cls.ranges = patch.build(cls.original)

    def test_complete_u16_and_class_admission_delta(self):
        proof = patch.verify(self.original, self.candidate)
        self.assertTrue(proof['成功'])
        self.assertEqual(proof['全量编号验证'][0]['变化'], [[855, 0, 1]])
        self.assertEqual(proof['全量编号验证'][1]['变化'], [])
        self.assertEqual(proof['全量编号验证'][2]['变化'], [])

    def test_pe_layout_exact_diff_and_adapter_abi(self):
        pe = pefile.PE(data=self.candidate, fast_load=True)
        self.assertEqual(pe.FILE_HEADER.NumberOfSections, 67)
        self.assertEqual(pe.sections[-1].Name.rstrip(b'\0'), b'.wk855')
        self.assertEqual(pe.sections[-1].Characteristics, 0x60000020)
        self.assertEqual(pe.OPTIONAL_HEADER.CheckSum, pe.generate_checksum())
        cursor = 0
        for start, end in self.ranges:
            self.assertEqual(self.original[cursor:start], self.candidate[cursor:start])
            cursor = end
        self.assertEqual(self.original[cursor:], self.candidate[cursor:len(self.original)])
        md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_32)
        entry = patch.BASE + pe.sections[-1].VirtualAddress
        instructions = list(md.disasm(pe.get_data(entry - patch.BASE, pe.sections[-1].Misc_VirtualSize), entry))
        self.assertEqual([i.mnemonic for i in instructions], ['cmp', 'jne', 'movzx', 'cmp', 'jne', 'mov', 'ret'])
        self.assertEqual(instructions[0].op_str, 'byte ptr [edi], 0')
        self.assertEqual(instructions[2].op_str, 'eax, word ptr [esp + 4]')
        self.assertEqual(instructions[3].op_str, 'eax, 0x357')
        self.assertEqual(instructions[-1].op_str, '4')
        self.assertTrue(all(i.op_str == hex(patch.OLD) for i in instructions if i.mnemonic == 'jne'))
        offset = pe.get_offset_from_rva(patch.CALL - patch.BASE)
        self.assertEqual(patch.CALL + 5 + struct.unpack_from('<i', self.candidate, offset + 1)[0], entry)
        self.assertEqual(patch.sha(self.candidate), patch.OUTPUT_SHA)

    def test_unknown_repeat_in_place_and_existing_output_rejected(self):
        for data in (self.candidate, self.original[:-1] + bytes([self.original[-1] ^ 1])):
            with self.assertRaises(ValueError):
                patch.build(data)
        with tempfile.TemporaryDirectory() as folder:
            destination = Path(folder) / 'candidate.exe'
            receipt = Path(folder) / 'receipt.json'
            destination.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, destination, receipt)
            self.assertEqual(destination.read_bytes(), b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, self.source, receipt)
            receipt.write_text('preserve', encoding='utf-8')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, Path(folder) / 'new.exe', receipt)
            self.assertEqual(receipt.read_text('utf-8'), 'preserve')
        self.assertEqual(patch.sha(self.source.read_bytes()), patch.SOURCE_SHA)


if __name__ == '__main__':
    unittest.main(verbosity=2)
