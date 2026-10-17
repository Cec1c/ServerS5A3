"""Regression checks against the actual V118 image; no game process is run."""
import hashlib
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_member_menu_v118 as patch


@unittest.skipUnless(os.environ.get("DNF_GUILD_V118_EXE"), "requires exact V118 fixture")
class MemberMenuPatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path = Path(os.environ["DNF_GUILD_V118_EXE"])
        cls.before = cls.path.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)
        cls.sections = patch.headers(cls.before)[3]

    def test_changes_only_one_call_and_new_rx_section(self):
        ranges = self.receipt["allowed_changed_ranges"]
        for lo, hi in zip([0] + [hi for lo, hi in sorted(ranges)], [lo for lo, hi in sorted(ranges)] + [len(self.before)]):
            self.assertEqual(self.before[lo:hi], self.after[lo:hi], (lo, hi))
        sections = patch.headers(self.after)[3]
        self.assertEqual(sections[:-1], self.sections)
        self.assertEqual(sections[-1]["flags"], 0x60000020)
        self.assertEqual(sections[-1]["name"].rstrip(b"\0"), b".gmenu")
        self.assertEqual(len(self.after) - len(self.before), sections[-1]["rawsize"])
        self.assertEqual(self.after[len(self.before):len(self.before)+len(patch.THUNK)], patch.THUNK)
        opt = patch.headers(self.after)[1]
        self.assertEqual(struct.unpack_from("<I", self.after, opt + 64)[0], patch.checksum(self.after, opt + 64))

    def test_entire_receive_bitmap_and_original_getter_unchanged(self):
        # The existing gate accepts opcodes only through 1474. Preserve every
        # bitmap bit, including the author's opcode65 and previous guild work.
        for rva, size in [(0x6A92300, 185), (patch.GETTER_RVA, 7)]:
            pos = patch.offset_for_rva(self.sections, rva, size)
            self.assertEqual(self.before[pos:pos+size], self.after[pos:pos+size])

    def test_unknown_input_and_overwrite_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(b"not an approved executable")
        with self.assertRaises(ValueError):
            patch.write_candidate(self.path, self.path)
        with tempfile.TemporaryDirectory() as tmp:
            existing = Path(tmp) / "existing.exe"
            existing.write_bytes(b"keep")
            with self.assertRaises(ValueError):
                patch.write_candidate(self.path, existing)
            self.assertEqual(existing.read_bytes(), b"keep")

    def emulate(self, image, pointer, group, cached_group, flags):
        from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32
        from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
            UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
            UC_X86_REG_ESP, UC_X86_REG_EFLAGS)
        registers = [UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
            UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EFLAGS]
        uc = Uc(UC_ARCH_X86, UC_MODE_32)
        sections = patch.headers(image)[3]
        mapped = set()
        def code(rva, size):
            address = patch.IMAGE_BASE + rva
            page = address & ~0xFFF
            if page not in mapped:
                uc.mem_map(page, 0x1000); mapped.add(page)
            off = patch.offset_for_rva(sections, rva, size)
            uc.mem_write(address, image[off:off+size])
        code(0x3143C68, 0x10)
        code(patch.GETTER_RVA, 7)
        if image is self.after:
            code(int(self.receipt["thunk_rva"], 16), len(patch.THUNK))
        stack, menu, actor = 0x100000, 0x200000, 0x300000
        for address in [stack, menu, actor]: uc.mem_map(address, 0x1000)
        uc.mem_write(menu+0x70, struct.pack("<I", cached_group))
        uc.mem_write(actor+0x3AC, struct.pack("<I", group))
        initial = [0x13579, menu, 0x123456, 0x2468, 0x345678, actor if pointer else 0, 0x456789, stack+0x800, flags]
        for register, value in zip(registers, initial): uc.reg_write(register, value)
        uc.emu_start(0x3543C68, 0x3543C78, count=32)
        return ([uc.reg_read(reg) for reg in registers], bytes(uc.mem_read(menu+0x70,4)))

    def test_actual_crash_path_before_fails_after_keeps_zero_identity(self):
        from unicorn import UcError
        # Live crash: EDI/ECX==0, cached menu server==0, call return 0x3543C75.
        with self.assertRaises(UcError): self.emulate(self.before, False, 0, 0, 0x246)
        regs, group = self.emulate(self.after, False, 0, 0, 0x246)
        self.assertEqual(group, bytes(4))
        self.assertEqual(regs[0], 0)
        self.assertEqual(regs[1:8], [0x200000, 0, 0x2468, 0x345678, 0, 0x456789, 0x100800])
        self.assertEqual(regs[-1], 0x246)

    def test_valid_actor_and_cached_identity_preserve_original_results(self):
        for group in [0, 1, 91, 249]:
            for cached in [0, 1, 249]:
                for flags in [0x202, 0x246, 0xA93]:
                    with self.subTest(group=group, cached=cached, flags=flags):
                        self.assertEqual(self.emulate(self.before, True, group, cached, flags),
                                         self.emulate(self.after, True, group, cached, flags))
        for cached in [1, 249]:
            self.assertEqual(self.emulate(self.before, False, 0, cached, 0x246),
                             self.emulate(self.after, False, 0, cached, 0x246))


if __name__ == "__main__":
    unittest.main()
