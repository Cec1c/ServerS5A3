"""Offline native-window regression; never opens or starts a game process.

The owner lookup, row accessor and successful effect creation are explicit
fixtures. The NULL baseline executes the original caller, level reader and
faulting decoder. Tests do not claim live rendering or full effect behavior.
"""
import argparse
import json
from pathlib import Path
import struct

from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import *
import patch_effect_source_null as patch


class Native:
    OWNER, VT, SOURCE, ROW = 0x20000000, 0x20001000, 0x20002000, 0x20004000
    LOOKUP, RETURN, STACK = 0x21000000, 0x21000100, 0x22008000
    REGS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
            UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
            UC_X86_REG_EFLAGS)

    def __init__(self, data, mode, flags, result=0x20005000):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        self.u.mem_map(patch.pe.IMAGE_BASE, struct.unpack_from('<I', data, opt + 56)[0])
        for va, size, raw in sections:
            if size:
                self.u.mem_write(va, data[raw:raw + size])
        for va, size in ((self.OWNER, 0x10000), (self.LOOKUP, 0x1000), (0x22000000, 0x10000)):
            self.u.mem_map(va, size)
        self.u.mem_write(self.LOOKUP, b'\xc3')
        self.u.mem_write(self.RETURN, b'\xc3')
        self.put(self.OWNER, self.VT)
        self.put(self.VT + 0x138, self.LOOKUP)
        self.put(self.ROW + 0x18, 0xBADF00D)  # Must be cleared on missing source.
        self.mode, self.flags, self.result = mode, flags, result
        self.calls, self.faults, self.tail, self.finished = [], [], None, False
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.bad)

    def put(self, address, value):
        self.u.mem_write(address, struct.pack('<I', value))

    def read(self, address):
        return struct.unpack('<I', self.u.mem_read(address, 4))[0]

    def reg(self, register):
        return self.u.reg_read(register)

    def ret(self, pop=0, value=0):
        sp = self.reg(UC_X86_REG_ESP)
        self.u.reg_write(UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        self.u.reg_write(UC_X86_REG_EAX, value)

    def bad(self, _u, access, address, size, _value, _context):
        self.faults.append(dict(ip=hex(self.reg(UC_X86_REG_EIP)), address=hex(address), size=size))
        return False

    def hook(self, _u, at, _size, _context):
        if at == self.LOOKUP:
            assert self.reg(UC_X86_REG_ECX) == self.OWNER
            self.u.reg_write(UC_X86_REG_EFLAGS, self.flags)
            self.ret(value=0 if self.mode == 'null' else self.SOURCE)
        elif at == patch.CALLEE:
            sp = self.reg(UC_X86_REG_ESP)
            entry = dict(this=hex(self.reg(UC_X86_REG_ECX)), flags=self.reg(UC_X86_REG_EFLAGS),
                         args=[self.read(sp + 4 + i * 4) for i in range(5)])
            assert entry['args'][0] == 1 and entry['args'][2:] == [0xFFFFFFFF, 0, 0xFFFFFFFF]
            self.calls.append(entry)
            if self.mode == 'valid':
                # Successful effect implementation is outside this test.
                self.u.reg_write(UC_X86_REG_ECX, 0x12345678)
                self.u.reg_write(UC_X86_REG_EDX, 0x23456789)
                self.u.reg_write(UC_X86_REG_EFLAGS, 0x246)
                self.ret(20, self.result)
        elif at == 0x2930A20 and self.mode == 'native_no_owner':
            # Only the non-null scalar lookup is adapted; the callee's real
            # missing-owner branch and ret 14h remain executable.
            self.ret(4, 1)
        elif at in (0x142ADB0, 0x1424520, 0x1FA2790, 0x2047360):
            self.ret(value=0)
        elif at == 0x2046D70:
            self.ret(4, 0)
        elif at == patch.CONTINUE:
            self.tail = dict(registers=[self.reg(r) for r in self.REGS],
                             stack=bytes(self.u.mem_read(self.reg(UC_X86_REG_ESP), 32)).hex())
        elif at == 0xE33B30:
            assert self.reg(UC_X86_REG_ECX) == self.OWNER + 0xD8
            self.ret(4, self.ROW)
        elif at == self.RETURN:
            self.finished = True
            self.u.emu_stop()

    def run(self):
        bp = self.STACK
        self.put(bp - 12, 0x11111111)  # saved EDI, ESI, EBX, EBP
        self.put(bp - 8, 0x22222222)
        self.put(bp - 4, 0x33333333)
        self.put(bp, 0x44444444)
        self.put(bp + 4, self.RETURN)
        self.put(bp + 8, 0x55555555)
        self.put(bp + 12, 0x66666666)
        self.put(bp + 16, 0x77777777)
        for r, value in ((UC_X86_REG_EBP, bp), (UC_X86_REG_ESP, bp - 12),
                         (UC_X86_REG_ESI, self.OWNER), (UC_X86_REG_EDI, self.OWNER + 0xD8),
                         (UC_X86_REG_EBX, 0x88888888), (UC_X86_REG_EFLAGS, self.flags)):
            self.u.reg_write(r, value)
        try:
            self.u.emu_start(0x101648F, 0xFFFFFFFF, count=1000)
        except UcError:
            if not self.faults:
                raise
        assert self.finished or self.faults, 'instruction budget exhausted'
        if self.finished:
            assert self.reg(UC_X86_REG_ESP) == bp + 20
            for r, v in ((UC_X86_REG_EDI, 0x11111111), (UC_X86_REG_ESI, 0x22222222),
                         (UC_X86_REG_EBX, 0x33333333), (UC_X86_REG_EBP, 0x44444444)):
                assert self.reg(r) == v
        return dict(finished=self.finished, faults=self.faults, calls=self.calls, tail=self.tail,
                    rowResult=hex(self.read(self.ROW + 0x18)),
                    registers=[self.reg(r) for r in self.REGS])


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--report', type=Path, required=True)
    a = p.parse_args()
    if a.report.exists():
        raise ValueError('report must be a new file')
    original = a.input.read_bytes()
    candidate, receipt = patch.build(original)
    cases = []
    for flags in (0x202, 0x246, 0xAD7):
        old = Native(original, 'null', flags).run()
        new = Native(candidate, 'null', flags).run()
        assert old['faults'] == [dict(ip='0x37df6a3', address='0xf9c', size=4)]
        assert new['finished'] and not new['faults'] and new['rowResult'] == '0x0' and not new['calls']
        assert new['tail']['registers'][-1] == flags
        cases.append(dict(mode='null', flags=flags, baselineFault=old['faults'], passed=True))
        for mode, result in [('valid', 0x20005000), ('valid', 0), ('native_no_owner', 0)]:
            old = Native(original, mode, flags, result).run()
            new = Native(candidate, mode, flags, result).run()
            assert old == new and new['finished'] and not new['faults'], (mode, flags)
            cases.append(dict(mode=mode, result=hex(result), flags=flags, passed=True))
    corrupted = bytearray(original)
    corrupted[-1] ^= 1
    for bad in (bytes(corrupted), candidate):
        try:
            patch.build(bad)
        except ValueError:
            pass
        else:
            raise AssertionError('modified or already-patched input accepted')
    result = dict(cases=cases, rejectedInputs=2, candidate=receipt,
                  scope='bounded original instructions and explicit external fixtures; no game process',
                  liveAcceptance='pending', modelVisibilityFixed=False)
    with a.report.open('x', encoding='utf8') as f:
        json.dump(result, f, indent=2)
    print(json.dumps(dict(passed=len(cases), rejectedInputs=2, liveAcceptance='pending')))


if __name__ == '__main__':
    main()
