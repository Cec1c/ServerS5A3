"""Artifact-backed checks of the single-bit candidate and native gate."""
import os
from pathlib import Path
import struct
import unittest

import pefile
import ridable_spawn_gate as patch
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ESP


class RidableGate(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get("DNF_RIDABLE_CLIENT_FIXTURE")
        if not source:
            raise unittest.SkipTest("set DNF_RIDABLE_CLIENT_FIXTURE to the pinned V128 artifact")
        cls.before = Path(source).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_exact_delta_checksum_and_preserved_original_content(self):
        self.assertEqual(self.receipt["changed_admissions"], [296])
        self.assertEqual(len(self.before), len(self.after))
        pe = pefile.PE(data=self.after, fast_load=True)
        self.assertTrue(pe.verify_checksum())
        offset = pe.get_offset_from_rva(patch.BITMAP_VA - pe.OPTIONAL_HEADER.ImageBase + 296 // 8)
        check = pe.OPTIONAL_HEADER.get_field_absolute_offset("CheckSum")
        restored = bytearray(self.after)
        restored[offset] = self.before[offset]
        restored[check:check+4] = self.before[check:check+4]
        self.assertEqual(restored, self.before)

    def test_wrong_hash_truncation_and_reapplication_are_rejected(self):
        bad = bytearray(self.before); bad[-1] ^= 1
        for candidate in (bytes(bad), self.before[:-1], self.after):
            with self.assertRaises(ValueError):
                patch.build(candidate)

    def test_native_gate_all_u16_opcodes_only_296_changes(self):
        pe = pefile.PE(data=self.before, fast_load=True)
        base = pe.OPTIONAL_HEADER.ImageBase
        u = Uc(UC_ARCH_X86, UC_MODE_32)
        for va in (patch.GATE_VA & ~4095, patch.BITMAP_VA & ~4095):
            u.mem_map(va, 4096)
            u.mem_write(va, pe.get_data(va-base, 4096))
        stack, stop = 0x10000000, 0x12000000
        u.mem_map(stack, 4096); u.mem_map(stop, 4096)
        u.hook_add(UC_HOOK_CODE, lambda uc, address, size, user: uc.emu_stop() if address == stop else None)
        u.mem_write(stack, struct.pack("<I", stop))
        def check(op):
            u.mem_write(stack+4, struct.pack("<I", op))
            u.reg_write(UC_X86_REG_ESP, stack)
            u.reg_write(UC_X86_REG_EAX, 0xdeadbeef)
            u.emu_start(patch.GATE_VA, 0, count=30)
            return bool(u.reg_read(UC_X86_REG_EAX))
        before = [check(op) for op in range(65536)]
        u.mem_write(patch.BITMAP_VA+296//8, b"\x01")
        after = [check(op) for op in range(65536)]
        self.assertEqual([op for op in range(65536) if before[op] != after[op]], [296])
        self.assertTrue(after[296]); self.assertFalse(after[316]); self.assertFalse(after[317])
        self.assertTrue(after[475])


if __name__ == "__main__":
    unittest.main(verbosity=2)
