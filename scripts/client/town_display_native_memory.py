"""Shared memory/ABI harness for the bounded town-display native checks.

Maps a caller-verified PE and isolated fixtures. This module does not patch an
image, load game resources, run a GPU or access a live game process.
"""
from collections import Counter
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EIP, UC_X86_REG_ESP
import patch_receivers_v128 as pe


class NativeMemory:
    ACTOR = 0x30010000
    STACK = 0x31008000
    ROOT = 0x31008400
    STUB = 0x32000000
    STOP = 0x3200FFF0

    def __init__(self, data):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = pe.headers(data)
        self.u.mem_map(pe.IMAGE_BASE, struct.unpack_from('<I', data, opt + 56)[0])
        for address, size, raw in sections:
            if size:
                self.u.mem_write(address, data[raw:raw + size])
        for address, size in ((0, 0x1000), (0x30000000, 0x40000),
                              (self.STACK - 0x8000, 0x10000),
                              (self.STUB, 0x10000), (0x34000000, 0x200000)):
            self.u.mem_map(address, size)
        self.hooks = {}
        self.count = Counter()
        self.stop = 0
        self.steps = 0
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.invalid)

    def put(self, address, value):
        self.u.mem_write(address, struct.pack('<I', value & 0xFFFFFFFF))

    def rd(self, address):
        return struct.unpack('<I', self.u.mem_read(address, 4))[0]

    def reg(self, register):
        return self.u.reg_read(register)

    def arg(self, index=0):
        return self.rd(self.reg(UC_X86_REG_ESP) + 4 + 4 * index)

    def ret(self, pop=0, value=0):
        sp = self.reg(UC_X86_REG_ESP)
        target = self.rd(sp)
        self.u.reg_write(UC_X86_REG_EAX, value & 0xFFFFFFFF)
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        self.u.reg_write(UC_X86_REG_EIP, target)

    def hook(self, _uc, address, _size, _context):
        self.steps += 1
        if address == self.stop:
            self.u.emu_stop()
        elif address in self.hooks:
            self.hooks[address]()

    def invalid(self, _uc, access, address, size, _value, _context):
        raise AssertionError(('invalid native memory', hex(self.reg(UC_X86_REG_EIP)), access, hex(address), size))
