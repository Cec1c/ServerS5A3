"""Offline current-EXE checks. Set DNF_LUKE_CLIENT to the exact input file.

Uses original receive gate, op602 reader and individual-mask conversion loop.
Packet reads, STL lookups and UI effects are explicit adapters; this is not a
claim of live tooltip or login acceptance. No running process is accessed.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED
from unicorn import x86_const as X
from patch_luke_selector_progress import build, APPROVED_INPUTS


class Native:
    STACK = 0x20010000
    STOP = 0x12000000

    def __init__(self, data):
        self.pe = pefile.PE(data=data, fast_load=True)
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        self.hooks = {}
        self.u.hook_add(UC_HOOK_MEM_UNMAPPED, self.unmapped)
        self.u.hook_add(UC_HOOK_CODE, self.code)
        self.ensure(0, 4096)
        self.ensure(self.STACK - 0x20000, 0x30000)
        self.ensure(self.STOP, 4096)

    def ensure(self, address, size):
        for page in range(address & ~4095, (address + size + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.u.mem_map(page, 4096)
                self.pages.add(page)
                if 0x400000 <= page < 0x400000 + self.pe.OPTIONAL_HEADER.SizeOfImage:
                    self.u.mem_write(page, self.pe.get_data(page - 0x400000, 4096))

    def unmapped(self, u, access, address, size, value, user):
        if 0x400000 <= address < 0x400000 + self.pe.OPTIONAL_HEADER.SizeOfImage:
            self.ensure(address, size)
            return True
        raise AssertionError("unexpected memory " + hex(address))

    def put(self, address, *values):
        self.ensure(address, 4 * len(values))
        self.u.mem_write(address, struct.pack("<" + "I" * len(values), *values))

    def read(self, address):
        return struct.unpack("<I", self.u.mem_read(address, 4))[0]

    def arg(self, index=0):
        return self.read(self.u.reg_read(X.UC_X86_REG_ESP) + 4 + 4 * index)

    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(X.UC_X86_REG_ESP)
        self.u.reg_write(X.UC_X86_REG_EAX, value)
        self.u.reg_write(X.UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(X.UC_X86_REG_ESP, sp + 4 + pop)

    def code(self, u, address, size, user):
        if address == self.STOP:
            u.emu_stop()
        elif address in self.hooks:
            self.hooks[address]()

    def call(self, address, *args, this=0):
        self.put(self.STACK, self.STOP, *args)
        self.u.reg_write(X.UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(X.UC_X86_REG_EBP, self.STACK + 0x1000)
        self.u.reg_write(X.UC_X86_REG_ECX, this)
        self.u.emu_start(address, 0, count=100000)
        assert self.u.reg_read(X.UC_X86_REG_EIP) == self.STOP
        return self.u.reg_read(X.UC_X86_REG_EAX)


@unittest.skipUnless(os.environ.get("DNF_LUKE_CLIENT"), "exact client fixture not supplied")
class Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.before = Path(os.environ["DNF_LUKE_CLIENT"]).read_bytes()
        assert APPROVED_INPUTS.get(hashlib.sha256(cls.before).hexdigest()) == len(cls.before)
        cls.after, cls.receipt = build(cls.before)

    def test_only_receive_bit_and_checksum_change(self):
        allowed = {self.receipt["bitmap_offset"], *range(self.receipt["checksum_offset"], self.receipt["checksum_offset"] + 4)}
        self.assertEqual(len(self.before), len(self.after))
        self.assertTrue(all(i in allowed for i, pair in enumerate(zip(self.before, self.after)) if pair[0] != pair[1]))
        with self.assertRaises(ValueError):
            build(self.after)

    def test_original_gate_blocks_602_and_candidate_accepts_only_that_addition(self):
        a, b = Native(self.before), Native(self.after)
        changed = []
        for opcode in range(1476):
            before, after = a.call(0x376E6C0, opcode), b.call(0x376E6C0, opcode)
            if before != after:
                changed.append((opcode, before, after))
        self.assertEqual(changed, [(602, 0, 1)])

    def test_native_reader_consumes_exact_9_bytes(self):
        n = Native(self.after)
        packet = struct.pack("<IBI", 42, 4, 31)
        cursor = 0
        captured = []
        def read(size):
            nonlocal cursor
            n.u.mem_write(n.arg(), packet[cursor:cursor + size])
            cursor += size
            n.ret(1)
        n.hooks[0x37E83C0] = lambda: read(4)
        # byte helper takes (destination, count), both cdecl.
        n.hooks[0x37E82E0] = lambda: read(n.arg(1))
        n.hooks[0x18C0D90] = lambda: n.ret(0x11000000)
        def consume():
            captured.append((n.arg(), n.arg(1), n.arg(2)))
            n.ret(0, 12)
        n.hooks[0x18C0310] = consume
        n.call(0x20076D0)
        self.assertEqual(cursor, 9)
        self.assertEqual(captured, [(42, 4, 31)])

    def test_native_individual_mask_projects_pvf_order(self):
        for mask, expected in [(0, []), (1, [5101]), (10, [5102, 5104]), (31, [5101,5102,5103,5104,5105]), (63, [5101,5102,5103,5104,5105,5106])]:
            n = Native(self.after)
            # Native STL-debug iterator helpers are adapted, not the bit loop.
            manager, definitions, cache = 0x11000000, 0x11001000, 0x11002000
            proxy, definition_node, cache_node = 0x11003000, 0x11004000, 0x11005000
            sentinel, vector = 0x11006000, 0x11007000
            n.put(manager, definitions)
            n.put(proxy, 0x11008000)
            n.put(0x11008000, 0, 0, sentinel)
            n.put(sentinel)
            n.put(cache_node + 0x10, 42)
            n.put(definition_node + 0x10 + 4, vector, vector + 24)
            n.put(vector, 5101,5102,5103,5104,5105,5106)
            def find():
                dest = n.arg()
                node = definition_node if n.u.reg_read(X.UC_X86_REG_ECX) == definitions else cache_node
                n.put(dest, proxy, 0, node)
                n.ret(dest, 8)
            def end():
                dest = n.arg()
                n.put(dest, proxy, 0, sentinel)
                n.ret(dest, 4)
            n.hooks[0x279F660] = find
            n.hooks[0x16C5630] = find
            n.hooks[0xA8F6C0] = end
            n.hooks[0x2142F90] = lambda: n.ret()
            selected = []
            def insert():
                selected.append(n.read(n.arg(1)))
                n.ret(n.arg(), 8)
            n.hooks[0x8D4A70] = insert
            n.call(0x18BE810, 42, mask, this=manager)
            self.assertEqual(selected, expected)


if __name__ == "__main__":
    unittest.main()
