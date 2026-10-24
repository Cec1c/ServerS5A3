"""Exact-image, bounded native receive-chain and Windows mapping validation."""
import ctypes
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_fatigue_count_v164 as p


def native(data, classes, opcodes):
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
    from unicorn.x86_const import (UC_X86_REG_ESP, UC_X86_REG_EAX,
        UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_ESI,
        UC_X86_REG_EDI, UC_X86_REG_EBP)
    opt, sections = p.pe.headers(data)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    u.mem_map(p.pe.IMAGE_BASE, struct.unpack_from('<I', data, opt+56)[0])
    for va, size, raw in sections:
        u.mem_write(va, data[raw:raw+size])
    stack, packet, stop = 0x20001000, 0x20008000, 0x30000000
    u.mem_map(0x20000000, 0x10000)
    u.mem_map(stop, 0x1000)
    at = p.pe.offset(sections, p.CALL_VA, 5)
    gate = p.CALL_VA + 5 + struct.unpack_from('<i', data, at+1)[0]
    keep = (UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
            UC_X86_REG_ESI, UC_X86_REG_EBP)
    values = bytearray()
    for cls in classes:
        u.mem_write(packet, bytes([cls]))
        for opcode in opcodes:
            u.mem_write(stack, struct.pack('<II', stop, opcode))
            u.reg_write(UC_X86_REG_ESP, stack)
            u.reg_write(UC_X86_REG_EDI, packet)
            for register in keep:
                u.reg_write(register, 0x12345678)
            u.emu_start(gate, stop, count=1000)
            if u.reg_read(UC_X86_REG_ESP) != stack+8 or u.reg_read(UC_X86_REG_EDI) != packet:
                raise AssertionError('receive ABI changed')
            if any(u.reg_read(reg) != 0x12345678 for reg in keep):
                raise AssertionError('preserved register changed')
            value = u.reg_read(UC_X86_REG_EAX)
            if value not in (0, 1):
                raise AssertionError('unexpected gate return')
            values.append(value)
    return values


def windows_image(path):
    k = ctypes.WinDLL('kernel32', use_last_error=True)
    k.CreateFileW.argtypes = [ctypes.c_wchar_p, ctypes.c_uint32, ctypes.c_uint32,
                             ctypes.c_void_p, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_void_p]
    k.CreateFileW.restype = ctypes.c_void_p
    k.CreateFileMappingW.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_uint32,
                                   ctypes.c_uint32, ctypes.c_uint32, ctypes.c_wchar_p]
    k.CreateFileMappingW.restype = ctypes.c_void_p
    k.CloseHandle.argtypes = [ctypes.c_void_p]
    file = k.CreateFileW(str(path), 0x80000000, 7, None, 3, 0x80, None)
    if file == ctypes.c_void_p(-1).value:
        raise OSError(ctypes.get_last_error(), 'image input open failed')
    try:
        image = k.CreateFileMappingW(file, None, 0x11000002, 0, 0, None)
        if not image:
            raise OSError(ctypes.get_last_error(), 'SEC_IMAGE_NO_EXECUTE rejected image')
        k.CloseHandle(image)
    finally:
        k.CloseHandle(file)


class FatigueV164Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_FATIGUE_V164_CLIENT')
        if not path:
            raise unittest.SkipTest('requires exact V164 image')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = p.build(cls.before)

    def test_only_four_declared_ranges_change(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        opt, old = p.pe.headers(self.before)
        self.assertEqual(old, p.pe.headers(self.after)[1])
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0], p.pe.checksum(self.after, opt+64))

    def test_exact_input_and_no_overwrite(self):
        for bad in (self.after, self.before[:-1]):
            with self.assertRaises(ValueError):
                p.build(bad)
        with self.assertRaises(ValueError):
            p.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as td:
            dst = Path(td)/'DNF.exe'
            dst.write_bytes(b'keep')
            with self.assertRaises(ValueError):
                p.write_candidate(self.source, dst)
            self.assertEqual(dst.read_bytes(), b'keep')

    def test_complete_class_zero_and_one_gate_and_abi(self):
        before = native(self.before, (0, 1), range(65536))
        after = native(self.after, (0, 1), range(65536))
        self.assertEqual([(i//65536, i%65536) for i in range(len(before)) if before[i] != after[i]], [(0,541)])
        self.assertEqual((before[541],after[541]), (0,1))
        self.assertEqual(native(self.before, (2,255), (0,541,103,775,1278,1367,1368,835,864,65535)),
                         native(self.after, (2,255), (0,541,103,775,1278,1367,1368,835,864,65535)))

    @unittest.skipUnless(os.name == 'nt', 'Windows image mapper required')
    def test_real_windows_image_mapping_and_disk_readback(self):
        windows_image(self.source)
        with tempfile.TemporaryDirectory() as td:
            dst = Path(td)/'DNF.exe'
            p.write_candidate(self.source, dst)
            self.assertEqual(p.pe.sha(dst.read_bytes()),self.receipt['outputSha256'])
            windows_image(dst)


if __name__ == '__main__':
    unittest.main()
