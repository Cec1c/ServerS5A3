"""Unicorn ABI checks for the V189 display-menu lifetime helper."""

import itertools
from pathlib import Path
import struct
import sys
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parent))
import patch_client_display_menu_v189 as patch


HELPER = 0x40000000
ACTIVE_LOOKUP = 0x0296D730
AUX_LOOKUP = 0x01A5A5E0
STACK = 0x10000000
MENU = 0x20000000
RETURN = 0x30000000

ACTIVE_ACTOR = 0x41001000
AUX_ACTOR = 0x41002000
CLOBBER_ECX = 0xC1C2C3C4
CLOBBER_EDX = 0xD1D2D3D4
CLOBBER_FLAGS = 0x8D7


class MenuLifetimeHelperTests(unittest.TestCase):
    def run_helper(self, uid, channel, directory, active_result=0, aux_cache=None):
        """Run only menu_helper; both lookups are deterministic synthetic hooks."""
        from unicorn import UC_ARCH_X86, UC_HOOK_CODE, UC_MODE_32, Uc
        from unicorn.x86_const import (
            UC_X86_REG_EAX,
            UC_X86_REG_EBX,
            UC_X86_REG_ECX,
            UC_X86_REG_EDX,
            UC_X86_REG_EFLAGS,
            UC_X86_REG_EBP,
            UC_X86_REG_EDI,
            UC_X86_REG_ESI,
            UC_X86_REG_ESP,
        )

        cache = dict(aux_cache or {})
        active_calls = []
        aux_calls = []
        return_hits = []
        uc = Uc(UC_ARCH_X86, UC_MODE_32)
        for page in (
            STACK,
            MENU,
            RETURN,
            HELPER,
            ACTIVE_LOOKUP & ~0xFFF,
            AUX_LOOKUP & ~0xFFF,
        ):
            uc.mem_map(page, 0x1000)

        helper_code = patch.menu_helper(HELPER)
        uc.mem_write(HELPER, helper_code)
        # Each lookup is a real CALL target whose hook supplies its result, then
        # Unicorn executes the RET instruction here.
        uc.mem_write(ACTIVE_LOOKUP, b"\xC3")
        uc.mem_write(AUX_LOOKUP, b"\xC3")
        uc.mem_write(RETURN, b"\xF4")

        # Byte loads must ignore these upper bytes. A dword load or an
        # un-zero-extended byte would expose the noise in the AUX arguments.
        uc.mem_write(
            MENU + 0x48,
            bytes((channel & 0xFF, directory & 0xFF, 0xA5, 0xFF)),
        )

        esp = STACK + 0x800
        stack_pattern = bytes((index * 29 + 7) & 0xFF for index in range(0x300))
        uc.mem_write(esp - 0x100, stack_pattern)
        uc.mem_write(esp, struct.pack("<II", RETURN, uid))
        caller_before = bytes(uc.mem_read(esp, 0x100))
        # Pushes use only the 16 bytes immediately below ESP. Keep a guard
        # beyond that scratch area to catch an ESP or cleanup error.
        lower_guard_before = bytes(uc.mem_read(esp - 0x80, 0x60))

        def read_u32(address):
            return struct.unpack("<I", uc.mem_read(address, 4))[0]

        def clobber(result):
            uc.reg_write(UC_X86_REG_ECX, CLOBBER_ECX)
            uc.reg_write(UC_X86_REG_EDX, CLOBBER_EDX)
            uc.reg_write(UC_X86_REG_EFLAGS, CLOBBER_FLAGS)
            uc.reg_write(UC_X86_REG_EAX, result)

        def active_hook(uc_handle, address, size, user_data):
            lookup_esp = uc_handle.reg_read(UC_X86_REG_ESP)
            active_calls.append(read_u32(lookup_esp + 4))
            clobber(active_result)

        def aux_hook(uc_handle, address, size, user_data):
            lookup_esp = uc_handle.reg_read(UC_X86_REG_ESP)
            key = (
                read_u32(lookup_esp + 4),
                read_u32(lookup_esp + 8),
                read_u32(lookup_esp + 12),
            )
            aux_calls.append(key)
            clobber(cache.get(key, 0))

        def stop_on_return(uc_handle, address, size, user_data):
            return_hits.append(address)
            uc_handle.emu_stop()

        uc.hook_add(
            UC_HOOK_CODE,
            active_hook,
            begin=ACTIVE_LOOKUP,
            end=ACTIVE_LOOKUP + 1,
        )
        uc.hook_add(
            UC_HOOK_CODE,
            aux_hook,
            begin=AUX_LOOKUP,
            end=AUX_LOOKUP + 1,
        )
        uc.hook_add(UC_HOOK_CODE, stop_on_return, begin=RETURN, end=RETURN + 1)

        callee_saved = {
            UC_X86_REG_EBX: 0xB3B4B5B6,
            UC_X86_REG_ESI: MENU,
            UC_X86_REG_EDI: 0xE1E2E3E4,
            UC_X86_REG_EBP: 0xF1F2F3F4,
        }
        for register, value in callee_saved.items():
            uc.reg_write(register, value)
        uc.reg_write(UC_X86_REG_EAX, 0xA1A2A3A4)
        uc.reg_write(UC_X86_REG_ECX, 0xC5C6C7C8)
        uc.reg_write(UC_X86_REG_EDX, 0xD5D6D7D8)
        uc.reg_write(UC_X86_REG_ESP, esp)
        uc.reg_write(UC_X86_REG_EFLAGS, 0x246)
        uc.emu_start(HELPER, 0, count=100)

        self.assertEqual(return_hits, [RETURN])
        self.assertEqual(uc.reg_read(UC_X86_REG_ESP), esp + 4)
        for register, value in callee_saved.items():
            self.assertEqual(uc.reg_read(register), value)
        # Lookup clobbers prove the helper uses its return value and the ESI
        # menu object rather than preserving hidden caller temporaries.
        self.assertEqual(uc.reg_read(UC_X86_REG_ECX), CLOBBER_ECX)
        self.assertEqual(uc.reg_read(UC_X86_REG_EDX), CLOBBER_EDX)
        self.assertEqual(bytes(uc.mem_read(esp, 0x100)), caller_before)
        self.assertEqual(bytes(uc.mem_read(esp - 0x80, 0x60)), lower_guard_before)
        self.assertEqual(read_u32(esp + 4), uid)  # CDECL caller still owns the arg.
        return {
            "result": uc.reg_read(UC_X86_REG_EAX),
            "active_calls": tuple(active_calls),
            "aux_calls": tuple(aux_calls),
        }

    def test_active_actor_wins_without_aux_query(self):
        values = itertools.product((1, 65534), (1, 254), (0, 249))
        for index, key in enumerate(values):
            uid, channel, directory = key
            active_result = ACTIVE_ACTOR + index * 0x40
            aux_result = AUX_ACTOR + index * 0x40
            with self.subTest(uid=uid, channel=channel, directory=directory):
                seen = self.run_helper(
                    uid,
                    channel,
                    directory,
                    active_result=active_result,
                    aux_cache={key: aux_result},
                )
                self.assertEqual(seen["result"], active_result)
                self.assertEqual(seen["active_calls"], (uid,))
                self.assertEqual(seen["aux_calls"], ())

    def test_exact_triple_falls_back_to_aux(self):
        values = itertools.product((1, 65534), (1, 254), (0, 249))
        for index, key in enumerate(values):
            uid, channel, directory = key
            aux_result = AUX_ACTOR + index * 0x40
            with self.subTest(uid=uid, channel=channel, directory=directory):
                seen = self.run_helper(
                    uid,
                    channel,
                    directory,
                    aux_cache={key: aux_result},
                )
                self.assertEqual(seen["result"], aux_result)
                self.assertEqual(seen["active_calls"], (uid,))
                self.assertEqual(seen["aux_calls"], (key,))

    def test_wrong_key_or_empty_cache_returns_zero(self):
        exact = (65534, 254, 249)
        populated = {exact: AUX_ACTOR}
        cases = (
            ("wrong-uid", (1, 254, 249), populated),
            ("wrong-channel", (65534, 1, 249), populated),
            ("wrong-directory", (65534, 254, 0), populated),
            ("empty-cache", exact, {}),
        )
        for name, key, cache in cases:
            with self.subTest(case=name):
                seen = self.run_helper(*key, aux_cache=cache)
                self.assertEqual(seen["result"], 0)
                self.assertEqual(seen["active_calls"], (key[0],))
                self.assertEqual(seen["aux_calls"], (key,))


if __name__ == "__main__":
    unittest.main()
