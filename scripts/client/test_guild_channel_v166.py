"""Bounded native guild-channel sender, ACK and advertised identity checks."""
import os
from pathlib import Path
import struct
import unittest

import patch_guild_grade_region as image
from test_patch_guild_grade_region import Sender
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EIP, UC_X86_REG_ESP


class GuildChannelNativeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get("DNF_GUILD_GRADE_INPUT_EXE")
        if not path:
            raise unittest.SkipTest("requires the audited V166 client")
        data = Path(path).read_bytes()
        if image.sha(data) != image.INPUT_SHA256:
            raise ValueError("client SHA256 mismatch")
        cls.lab = Sender(data)

    def run_function(self, address, args=(), channel=True, identity=(0, 19)):
        u = self.lab.u
        calls = []
        read32 = lambda at: struct.unpack("<I", u.mem_read(at, 4))[0]

        def ret(value=0, pop=0):
            sp = u.reg_read(UC_X86_REG_ESP)
            u.reg_write(UC_X86_REG_EAX, value)
            u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
            u.reg_write(UC_X86_REG_EIP, read32(sp))

        def hook(_u, at, _size, _ctx):
            sp = u.reg_read(UC_X86_REG_ESP)
            if at == 0x1B4A020:
                ret(0x11003000)
            elif at == 0x1B37730:
                ret(0x11004000 if channel else 0)
            elif at == 0x374EA10:
                ret(0x11005000)
            elif at == 0x374F280:
                calls.append(("opcode", read32(sp + 4)))
                ret(0x11005000, 4)
            elif at == 0x3750AC0:
                calls.append(("flush",))
                ret()
            elif at == 0x39A6620:
                calls.append(("message", read32(sp + 4)))
                ret(0x11006000)
            elif at == 0x32BEA50:
                calls.append(("dialog", read32(sp + 4)))
                ret(0, 12)
            elif at == 0x1B378F0:
                calls.append(("lookup", read32(sp + 4), read32(sp + 8)))
                ret(0x11004000, 8)

        sp = 0x10008000
        u.mem_write(sp, struct.pack("<" + "I" * (1 + len(args)), 0x20000000, *args))
        u.mem_write(0x11001000 + 193 * 4, struct.pack("<II", *identity))
        u.reg_write(UC_X86_REG_ECX, 0x11001000)
        u.reg_write(UC_X86_REG_ESP, sp)
        handle = u.hook_add(UC_HOOK_CODE, hook)
        try:
            u.emu_start(address, 0x20000000, count=200)
            self.assertEqual(u.reg_read(UC_X86_REG_EIP), 0x20000000)
            self.assertEqual(u.reg_read(UC_X86_REG_ESP), sp + 4)
        finally:
            u.hook_del(handle)
        return calls

    def test_save_sends_empty_830_for_current_channel(self):
        self.assertEqual(self.run_function(0x1196440), [("opcode", 830), ("flush",)])
        self.assertEqual(self.run_function(0x1196440, channel=False), [])

    def test_ack_success_and_native_errors(self):
        self.assertEqual(self.run_function(0x1FBFFE0, (0, 1, 0)), [])
        for code, message in ((7, 48450), (19, 48438)):
            with self.subTest(code=code):
                self.assertEqual(self.run_function(0x1FBFFE0, (0, 0, code)),
                                 [("message", message), ("dialog", 726)])

    def test_jump_uses_advertised_region_and_channel(self):
        for identity in ((0, 19), (1, 11), (249, 118)):
            with self.subTest(identity=identity):
                self.assertEqual(self.run_function(0x2978570, identity=identity),
                                 [("lookup", *identity)])


if __name__ == "__main__":
    unittest.main()
