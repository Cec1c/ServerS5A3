"""Native helper branch/stack contracts for the V189 display-name patch."""
import struct
import unittest

import patch_client_display_menu_v189 as patch
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *


def quoted_ascii_name(length):
    """Build a quoted name whose visible character count is exactly length."""
    return b'`' + b'X' * length + b'`'


class DisplayNameHelperTests(unittest.TestCase):
    def test_native_branches_preserve_registers_flags_and_stack(self):
        literals = [
            b'`E`',
            b'`Empire`',
            quoted_ascii_name(16),
            quoted_ascii_name(127),
            quoted_ascii_name(128),
            '`帝国竞技场`'.encode('utf8'),
            '`帝国竞技场`'.encode('gbk'),
        ]
        legacy = [
            b'``',
            b'`<chn_channel_info_028>`',
            b'`<4::name>`',
            b'`<broken`',
            b'``Empire``',
        ]
        for site, address, literal_pointer, stolen in patch.SITES:
            for token in literals + legacy:
                for flags in (0x202, 0x246, 0x297):
                    with self.subTest(site=hex(site), token=token, flags=flags):
                        vm = Uc(UC_ARCH_X86, UC_MODE_32)
                        code_pages = {
                            site & ~4095,
                            (site + len(stolen)) & ~4095,
                            literal_pointer & ~4095,
                            address & ~4095,
                        }
                        for page in code_pages:
                            vm.mem_map(page, 4096)
                        vm.mem_map(0x10000000, 0x3000)
                        vm.mem_write(
                            site,
                            patch.rel(b'\xe9', site, address) + b'\x90' * 5,
                        )
                        vm.mem_write(
                            address,
                            patch.helper(site, address, literal_pointer, stolen),
                        )
                        vm.mem_write(0x10000000, token + b'\0')
                        before = {
                            UC_X86_REG_EAX: 0x10000000,
                            UC_X86_REG_ECX: len(token) - 1,
                            UC_X86_REG_EDX: len(token),
                            UC_X86_REG_EBX: 0x12345678,
                            UC_X86_REG_ESI: 0x22334455,
                            UC_X86_REG_EDI: 0x33445566,
                            UC_X86_REG_EBP: 0x10002000,
                            UC_X86_REG_ESP: 0x10001800,
                            UC_X86_REG_EFLAGS: flags,
                        }
                        for reg, value in before.items():
                            vm.reg_write(reg, value)

                        reached = []

                        def stop(cpu, at, size, context):
                            if at in (site + len(stolen), literal_pointer):
                                reached.append(at)
                                cpu.emu_stop()

                        vm.hook_add(UC_HOOK_CODE, stop)
                        vm.emu_start(site, literal_pointer + 1, count=80)
                        expected = before.copy()
                        if token in legacy:
                            self.assertEqual(reached, [site + len(stolen)])
                            expected[UC_X86_REG_ESP] -= 8
                            expected[UC_X86_REG_EDX] = (
                                before[UC_X86_REG_EBP]
                                + struct.unpack_from('<i', stolen, 6)[0]
                            )
                            self.assertEqual(
                                bytes(vm.mem_read(expected[UC_X86_REG_ESP], 8)),
                                struct.pack('<II', 1, 0xffffffff),
                            )
                        else:
                            self.assertEqual(reached, [literal_pointer])
                        self.assertEqual(
                            {reg: vm.reg_read(reg) for reg in expected}, expected
                        )

    def test_rejects_nonbaseline(self):
        with self.assertRaisesRegex(ValueError, 'exact published V189'):
            patch.build(b'not an executable')


if __name__ == '__main__':
    unittest.main()
