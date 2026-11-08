"""Native initializer regression; does not launch a game client.

Owner/skill lookup, base initialization and scene helpers are explicit adapters.
The NULL skill traverses the actual initializer, level reader and faulting codec.
Valid skills stop at the unchanged continuation: no full effect/render claim.
"""
import argparse
import json
from pathlib import Path
import struct

from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import *
import patch_object_init_skill_null as patch


class Native:
    OBJECT, OWNER, SKILL = 0x20000000, 0x20010000, 0x20020000
    VTABLE, STUB, RETURN, STACK = 0x20030000, 0x21000000, 0x21000F00, 0x22008000
    REGS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
            UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
            UC_X86_REG_EFLAGS)

    def __init__(self, data, state, job, flags):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        self.u.mem_map(patch.pe.IMAGE_BASE, struct.unpack_from('<I', data, opt + 56)[0])
        for va, size, raw in sections:
            if size:
                self.u.mem_write(va, data[raw:raw + size])
        for va, size in ((self.OBJECT, 0x40000), (self.STUB, 0x1000), (0x22000000, 0x10000)):
            self.u.mem_map(va, size)
        # The hook stops here before execution. A real terminator prevents the
        # emulator from decoding zero-filled fallthrough beyond the mapped page
        # before delivering that hook; this is not a client-code modification.
        self.u.mem_write(self.RETURN, b'\xc3')
        self.state, self.job, self.flags = state, job, flags
        self.put(self.OWNER, self.VTABLE)
        self.put(self.VTABLE + 0x67C, self.STUB + 0x100)
        self.put(self.VTABLE + 0xBDC, self.STUB + 0x200)
        self.u.mem_write(self.OBJECT + 0x16C0, bytes([state == 'initialized']))
        self.faults, self.effects, self.visited = [], [], set()
        self.finished, self.continuation = False, None
        self.hooks = {
            0x2697210: self.base,
            0x1E36150: lambda: self.ret(value=0 if state == 'missing_owner' else self.OWNER),
            self.STUB + 0x100: lambda: self.ret(value=job),
            self.STUB + 0x200: self.skill,
            0x3E73CB6: lambda: self.ret(value=self.OWNER),
            0x142ADB0: lambda: self.ret(value=self.OWNER),
            0x1424520: lambda: self.ret(value=0),
            0x1FA2790: lambda: self.ret(value=self.OWNER),
            0x2047360: lambda: self.ret(value=self.OWNER),
            0x2046D70: lambda: self.ret(4, 0),
        }
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.bad_memory)

    def put(self, at, value):
        self.u.mem_write(at, struct.pack('<I', value))

    def read(self, at):
        return struct.unpack('<I', self.u.mem_read(at, 4))[0]

    def reg(self, reg):
        return self.u.reg_read(reg)

    def arg(self, index=0):
        return self.read(self.reg(UC_X86_REG_ESP) + 4 + index * 4)

    def ret(self, pop=0, value=None):
        sp = self.reg(UC_X86_REG_ESP)
        if value is not None:
            self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    def base(self):
        self.effects.append(['base', self.arg()])
        self.ret(4)

    def skill(self):
        assert self.reg(UC_X86_REG_ECX) == self.OWNER
        expected = 20 if self.job in (0, 9) else 0xFFFFFFFF
        assert [self.arg(), self.arg(1)] == [expected, 1]
        self.effects.append(['skill', expected, 1])
        self.u.reg_write(UC_X86_REG_EFLAGS, self.flags)
        self.ret(8, self.SKILL if self.state == 'valid' else 0)

    def bad_memory(self, _u, access, address, size, _value, _context):
        self.faults.append(dict(ip=hex(self.reg(UC_X86_REG_EIP)), address=hex(address), size=size))
        return False

    def hook(self, _u, at, _size, _context):
        self.visited.add(at)
        if at == patch.CONTINUE and self.state == 'valid':
            sp = self.reg(UC_X86_REG_ESP)
            self.continuation = dict(registers=[self.reg(r) for r in self.REGS],
                                     stack=bytes(self.u.mem_read(sp, self.STACK + 8 - sp)).hex())
            self.u.emu_stop()
        elif at == self.RETURN:
            self.finished = True
            self.u.emu_stop()
        elif at in self.hooks:
            self.hooks[at]()
        elif at == 0x37DF6A0 and self.reg(UC_X86_REG_ECX) == 0x56A3D5C:
            # Only this unrelated global float is adapted; NULL+F9C is real.
            self.put(self.arg(1), 0x3F800000)
            self.ret(8)

    def run(self):
        saved = [(UC_X86_REG_EBX, 0xBADF00D), (UC_X86_REG_ESI, 0xCAFE0100),
                 (UC_X86_REG_EDI, 0xCAFE0200), (UC_X86_REG_EBP, 0xCAFE0300)]
        for reg, value in saved + [(UC_X86_REG_ESP, self.STACK),
                                   (UC_X86_REG_ECX, self.OBJECT), (UC_X86_REG_EFLAGS, self.flags)]:
            self.u.reg_write(reg, value)
        self.put(self.STACK, self.RETURN)
        self.put(self.STACK + 4, 0x11223344)
        try:
            self.u.emu_start(0x2100880, 0xFFFFFFFF, count=2000)
        except UcError:
            if not self.faults:
                raise
        assert self.finished or self.continuation or self.faults, 'instruction budget exhausted'
        if self.finished:
            assert not self.faults and self.reg(UC_X86_REG_ESP) == self.STACK + 8
            assert all(self.reg(r) == v for r, v in saved)
            assert bytes(self.u.mem_read(self.OBJECT + 0x16C0, 1)) == b'\x01'
        return dict(finished=self.finished, continuation=self.continuation, faults=self.faults,
                    effects=self.effects, object=bytes(self.u.mem_read(self.OBJECT, 0x1700)).hex())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    if args.report.exists():
        raise ValueError('report must be a new file')
    original = args.input.read_bytes()
    candidate, receipt = patch.build(original)
    cases = []
    for job in (0, 9, 1):
        for flags in (0x202, 0x246, 0xAD7):
            for state in ('missing_skill', 'missing_owner', 'initialized', 'valid'):
                old = Native(original, state, job, flags).run()
                new = Native(candidate, state, job, flags).run()
                if state == 'missing_skill':
                    assert old['faults'] == [{'ip': '0x37df6a3', 'address': '0xf9c', 'size': 4}]
                    assert new['finished'] and old['effects'] == new['effects']
                else:
                    assert old == new, (state, job, flags)
                cases.append(dict(state=state, job=job, flags=flags, passed=True))
    with args.report.open('x', encoding='utf8') as stream:
        json.dump(dict(cases=cases, candidate=receipt, gameAcceptance='pending'), stream, indent=2)
    print(json.dumps(dict(passed=len(cases), gameAcceptance='pending')))


if __name__ == '__main__':
    main()
