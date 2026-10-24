"""Execute the complete native grade sender with bounded cache/network stubs."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_grade_region as patch

try:
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
    from unicorn.x86_const import (
        UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX, UC_X86_REG_ECX,
        UC_X86_REG_EDI, UC_X86_REG_EIP, UC_X86_REG_ESI, UC_X86_REG_ESP,
    )
except ImportError:
    Uc = None


class Sender:
    def __init__(self, data):
        _, opt, _, sections = patch.headers(data)
        self.u = u = Uc(UC_ARCH_X86, UC_MODE_32)
        size = struct.unpack_from("<I", data, opt + 56)[0]
        u.mem_map(patch.IMAGE_BASE, (size + 4095) & ~4095)
        for section in sections:
            if section["rawsize"]:
                u.mem_write(patch.IMAGE_BASE + section["rva"], data[section["offset"]:section["offset"] + section["rawsize"]])
        u.mem_map(0x10000000, 0x10000)
        u.mem_map(0x11000000, 0x10000)
        u.mem_map(0x20000000, 4096)

    def run(self, region, grade, roster=True, second=False, locked=False):
        u = self.u
        u32 = lambda at: struct.unpack("<I", u.mem_read(at, 4))[0]
        sent, lookups, messages = [], [], []
        u.mem_write(0x11001000, bytes(512))
        u.mem_write(0x11002000, bytes(128))
        u.mem_write(0x11001184, bytes([locked]))
        u.mem_write(0x11002030, bytes([locked]))
        u.mem_write(0x11000100, b"Member\0")

        def ret(value=0, pop=0):
            sp = u.reg_read(UC_X86_REG_ESP)
            target = u32(sp)
            u.reg_write(UC_X86_REG_EAX, value)
            u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
            u.reg_write(UC_X86_REG_EIP, target)

        def hook(_u, address, _size, _context):
            sp = u.reg_read(UC_X86_REG_ESP)
            if address in (0x2979120, 0x11BDA70):
                lookups.append((address, u32(sp + 4), u32(sp + 8)))
                ret((0x11001000 if roster else 0) if address == 0x2979120 else (0x11002000 if second else 0), 8 if address == 0x2979120 else 0)
            elif address == 0x374EA10:
                ret(0x11003000)
            elif address in (0x374F280, 0x374F390, 0x374F550):
                sent.append((address, u32(sp + 4)))
                ret(0x11003000, 4)
            elif address == 0x3750AC0:
                sent.append((address, 0))
                ret()
            elif address == 0x39A6620:
                ret(0x11004000)
            elif address == 0x32BEA50:
                messages.append(True)
                ret(0, 12)

        preserved = {UC_X86_REG_EBP: 0x10009000, UC_X86_REG_EBX: 0x11223344,
                     UC_X86_REG_ESI: 0x55667788, UC_X86_REG_EDI: 0x12345678}
        for register, value in preserved.items():
            u.reg_write(register, value)
        stack = 0x10008000
        u.mem_write(stack, struct.pack("<IIII", 0x20000000, region & 0xFFFFFFFF, 0x11000100, grade))
        u.reg_write(UC_X86_REG_ESP, stack)
        handle = u.hook_add(UC_HOOK_CODE, hook)
        try:
            u.emu_start(0x29794F0, 0x20000000, count=300)
            assert u.reg_read(UC_X86_REG_EIP) == 0x20000000
            assert u.reg_read(UC_X86_REG_ESP) == stack + 16
            assert all(u.reg_read(register) == value for register, value in preserved.items())
        finally:
            u.hook_del(handle)
        return sent, lookups, messages


@unittest.skipIf(Uc is None, "Unicorn is required for native execution")
class GuildGradeRegionTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get("DNF_GUILD_GRADE_INPUT_EXE")
        if not source:
            raise unittest.SkipTest("set DNF_GUILD_GRADE_INPUT_EXE")
        cls.source = Path(source)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)
        cls.old, cls.new = Sender(cls.before), Sender(cls.after)

    def test_zero_region_red_green_and_valid_boundaries(self):
        for region in (0, 1, 249):
            for grade in (2, 3, 4):
                with self.subTest(region=region, grade=grade):
                    old, _, _ = self.old.run(region, grade)
                    new, lookups, messages = self.new.run(region, grade)
                    expected = [(0x374F280, 126), (0x374F390, region), (0x374F550, 0x11000100), (0x374F390, grade), (0x3750AC0, 0)]
                    self.assertEqual(old, [] if region == 0 else expected)
                    self.assertEqual(new, expected)
                    self.assertEqual([row[2] for row in lookups], [region, region])
                    self.assertEqual(messages, [])

    def test_invalid_regions_do_not_reach_cache_or_writer(self):
        for region in (-1, 250, 255, 0x7FFFFFFF):
            for sender in (self.old, self.new):
                with self.subTest(region=region, patched=sender is self.new):
                    self.assertEqual(sender.run(region, 2), ([], [], []))

    def test_member_cache_and_special_state_checks_remain(self):
        for region in (0, 1, 249):
            with self.subTest(region=region):
                sent, _, _ = self.new.run(region, 2, roster=False)
                self.assertEqual(sent, [])
                sent, _, messages = self.new.run(region, 2, locked=True)
                self.assertEqual(sent, [])
                self.assertEqual(messages, [True])
                sent, _, _ = self.new.run(region, 2, roster=False, second=True)
                self.assertEqual(sent[0], (0x374F280, 126))
                sent, _, messages = self.new.run(region, 2, roster=False, second=True, locked=True)
                self.assertEqual(sent, [])
                self.assertEqual(messages, [True])

    def test_only_two_instruction_bytes_and_checksum_change(self):
        self.assertEqual(len(self.before), len(self.after))
        cursor = 0
        for start, end in sorted(self.receipt["allowed_changed_ranges"]):
            self.assertEqual(self.before[cursor:start], self.after[cursor:start])
            cursor = end
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        _, opt, _, _ = patch.headers(self.after)
        self.assertEqual(struct.unpack_from("<I", self.after, opt + 64)[0], patch.checksum(self.after, opt + 64))

    def test_wrong_input_and_existing_destination_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1])
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "DNF.exe"
            target.write_bytes(b"preserve")
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, target)
            self.assertEqual(target.read_bytes(), b"preserve")


if __name__ == "__main__":
    unittest.main(verbosity=2)
