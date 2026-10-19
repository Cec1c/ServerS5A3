"""Verify v128/v129 bytes and both native receive gate entry points."""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
import patch_receivers_v128 as patch
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EIP, UC_X86_REG_ESP


def vm(image):
    opt, sections = patch.headers(image)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    size = struct.unpack_from('<I', image, opt + 56)[0]
    u.mem_map(patch.IMAGE_BASE, (size + 4095) & -4096)
    for va, count, raw in sections:
        if count:
            u.mem_write(va, image[raw:raw + count])
    u.mem_map(0x10000000, 4096)
    u.mem_map(0x20000000, 4096)
    return u


class ReceiverTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ['DNF_RECEIVERS_INPUT_EXE'])
        cls.before = cls.source.read_bytes()
        cls.after = Path(os.environ['DNF_RECEIVERS_CANDIDATE_EXE']).read_bytes()

    def test_all_65536_native_gate_decisions_change_only_requested(self):
        machines = [vm(self.before), vm(self.after), vm(self.before), vm(self.after)]
        changes, admitted = [], []
        for opcode in range(65536):
            values = []
            for index, u in enumerate(machines):
                u.mem_write(0x10000000, struct.pack('<II', 0x20000000, opcode))
                u.reg_write(UC_X86_REG_ESP, 0x10000000)
                u.emu_start(patch.GATE_VA if index < 2 else patch.ORIGINAL_GATE_VA, 0x20000000, count=40)
                self.assertEqual(u.reg_read(UC_X86_REG_EIP), 0x20000000)
                self.assertEqual(u.reg_read(UC_X86_REG_ESP), 0x10000008)
                values.append(u.reg_read(UC_X86_REG_EAX))
            self.assertEqual(values[:2], values[2:])
            if values[0] != values[1]:
                self.assertEqual(values, [0, 1, 0, 1])
                changes.append(opcode)
            if values[1]:
                admitted.append(opcode)
        self.assertEqual(changes, list(patch.OPCODES))
        output = os.environ.get('DNF_RECEIVERS_GATE_RECEIPT')
        if output:
            Path(output).write_text(json.dumps(dict(passed=True, native_executions=262144, entry_points=[hex(patch.GATE_VA), hex(patch.ORIGINAL_GATE_VA)],
                changed_decisions=changes, admitted_after=admitted, old_requested_rejected=True,
                new_requested_admitted=True, full_ui_acceptance=False), indent=2), encoding='utf-8')

    def test_only_five_bitmap_bytes_and_checksum_change(self):
        result, receipt = patch.build(self.before)
        self.assertEqual(result, self.after)
        self.assertEqual(len(self.before), len(self.after))
        ranges = [(e['file_offset'], e['file_offset'] + 1) for e in receipt['edits']]
        ranges.append(tuple(receipt['checksum_range']))
        last = 0
        for start, end in sorted(ranges):
            self.assertEqual(self.before[last:start], self.after[last:start])
            last = end
        self.assertEqual(self.before[last:], self.after[last:])
        opt, _ = patch.headers(self.after)
        self.assertEqual(patch.checksum(self.after, opt + 64), struct.unpack_from('<I', self.after, opt + 64)[0])

    def test_wrong_input_and_existing_output_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1] + bytes([self.before[-1] ^ 1]))
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with tempfile.TemporaryDirectory() as d:
            target = Path(d) / 'DNF.exe'
            target.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, target)
            self.assertEqual(target.read_bytes(), b'preserve')
        self.assertEqual(patch.sha(self.source.read_bytes()), patch.INPUT_SHA256)


if __name__ == '__main__':
    unittest.main(verbosity=2)
