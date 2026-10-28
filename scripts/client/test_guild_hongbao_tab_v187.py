"""Native callback, tab lookup and ownership; UI effects and CRT are adapters."""
import ctypes
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_hongbao_tab_v187 as patch
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import native, windows_image
from unicorn.x86_const import *


class HongbaoTabNative(WarNative):
    WINDOW, GATE, TAB, CHILD = 0x20010000, 0x20011000, 0x20012000, 0x20013000
    BLOCK, COLLECTION, ROWS = 0x20015000, 0x20016000, 0x20017000

    def __init__(self, data, controls=2, release_owner=False):
        super().__init__(data)
        self.calls, self.finished, self.destroyed = [], 0, []
        self.release_owner = release_owner
        self.put(0, 0xFFFFFFFF)
        self.put(self.GATE + 0x18, 0)  # Execute real empty-set IsExist branch.
        self.put(self.WINDOW + 0x280, self.TAB if controls else 0)
        self.put(self.WINDOW + 0x288, self.TAB)
        self.put(self.TAB, 0x20014000)
        self.put(self.CHILD, 0x20014000)
        self.put(0x20014008, self.STOP + 0x100)
        self.put(self.TAB + 0x3D4, self.COLLECTION)
        self.put(self.COLLECTION + 0x24, self.ROWS, self.ROWS + (32 if controls == 2 else 0))
        self.put(self.ROWS + 24, self.CHILD, self.BLOCK)
        self.put(self.BLOCK, 0x20014100, 1, 1)
        self.put(0x20014100, self.STOP + 0x120, self.STOP + 0x130)
        self.hooks = {
            0x2303380: lambda: self.ret(value=self.GATE),
            self.STOP + 0x100: self.enable,
            self.STOP + 0x120: lambda: self.destroy('object'),
            self.STOP + 0x130: lambda: self.destroy('block'),
            0x11B1990: self.finish,
            0x8A9480: self.allocate,
            0x3E70882: lambda: self.ret(),
        }

    def allocate(self):
        assert self.arg() == 16
        self.ret(value=0x20018000)

    def enable(self):
        target = self.reg(UC_X86_REG_ECX)
        self.calls.append((target, self.arg() & 255))
        if self.release_owner and target == self.CHILD:
            assert self.read(self.BLOCK + 4) == 2
            self.put(self.BLOCK + 4, 1)  # External owner releases during UI effect.
        self.ret(4)

    def destroy(self, kind):
        assert self.reg(UC_X86_REG_ECX) == self.BLOCK
        self.destroyed.append(kind)
        self.ret()

    def finish(self):
        assert self.reg(UC_X86_REG_ECX) == self.WINDOW
        self.finished += 1
        self.ret()

    def callback(self):
        self.run(0x11B3730, ecx=self.WINDOW)


class HongbaoTabTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name = os.environ.get('DNF_HONGBAO_TAB_CLIENT')
        if not name:
            raise unittest.SkipTest('set DNF_HONGBAO_TAB_CLIENT to exact V187')
        cls.source = Path(name)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_original_hidden_and_candidate_enabled_with_native_ownership(self):
        for data, enabled in ((self.before, 0), (self.after, 1)):
            for controls in (0, 1, 2):
                with self.subTest(enabled=enabled, controls=controls):
                    lab = HongbaoTabNative(data, controls)
                    lab.callback()
                    self.assertEqual(len(lab.calls), controls)
                    self.assertTrue(all(value == enabled for _, value in lab.calls))
                    self.assertEqual(lab.finished, 1)
                    self.assertEqual(lab.read(lab.BLOCK + 4), 1)
                    self.assertEqual(lab.destroyed, [])

    def test_old_nop_patch_fails_callee_saved_abi(self):
        for controls in (0, 1, 2):
            with self.subTest(controls=controls):
                lab = HongbaoTabNative(self.before, controls)
                lab.u.mem_write(patch.SITE_VA, bytes.fromhex('b001909090'))
                with self.assertRaisesRegex(AssertionError, 'callee-saved ABI'):
                    lab.callback()
                self.assertEqual(lab.reg(UC_X86_REG_ESI), 0x12345672)
                self.assertEqual(lab.reg(UC_X86_REG_ESP), lab.STACK + 4)

    def test_repeated_callback_and_released_control(self):
        lab = HongbaoTabNative(self.after)
        for _ in range(20):
            lab.callback()
            self.assertEqual(lab.read(lab.BLOCK + 4), 1)
        self.assertEqual(lab.calls, [(lab.TAB, 1), (lab.CHILD, 1)] * 20)
        self.assertEqual(lab.finished, 20)
        lab = HongbaoTabNative(self.after, release_owner=True)
        lab.callback()
        self.assertEqual(lab.destroyed, ['object', 'block'])
        self.assertEqual(lab.read(lab.BLOCK + 4), 0)
        # Model the UI removing its released child before the next callback.
        lab.put(lab.COLLECTION + 0x28, lab.ROWS)
        lab.callback()
        self.assertEqual(lab.destroyed, ['object', 'block'])
        self.assertEqual(lab.finished, 2)

    def test_exact_ranges_and_independent_windows_checksum(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        self.assertEqual(patch.pe.headers(self.before), patch.pe.headers(self.after))
        if os.name == 'nt':
            buffer = ctypes.create_string_buffer(self.after)
            stored, computed = ctypes.c_uint32(), ctypes.c_uint32()
            check = ctypes.WinDLL('imagehlp').CheckSumMappedFile
            check.argtypes = [ctypes.c_void_p, ctypes.c_uint32, ctypes.POINTER(ctypes.c_uint32), ctypes.POINTER(ctypes.c_uint32)]
            check.restype = ctypes.c_void_p
            self.assertTrue(check(buffer, len(self.after), ctypes.byref(stored), ctypes.byref(computed)))
            self.assertEqual(stored.value, computed.value)

    def test_receive_gates_preserve_hongbao_and_existing_features(self):
        for kind in (0, 1, 2, 255):
            before = native(self.before, (kind,), range(65536))
            after = native(self.after, (kind,), range(65536))
            self.assertEqual(before, after)
            if kind == 1:
                for op in range(1300, 1305):
                    self.assertEqual(after[op], 1)

    def test_strict_input_new_output_readback_and_mapping(self):
        for bad in (self.after, self.before[:-1], b'X' + self.before[1:]):
            with self.assertRaises(ValueError):
                patch.build(bad)
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'DNF.exe'
            self.assertEqual(patch.write_candidate(self.source, path), self.receipt)
            self.assertEqual(path.read_bytes(), self.after)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, path)
            if os.name == 'nt':
                windows_image(path)


if __name__ == '__main__':
    unittest.main()
