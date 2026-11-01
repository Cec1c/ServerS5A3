"""Bounded native callback/scene/lookup regression; never launches the game.

Runs the complete callback, real owner/scene getters, packet integer reader,
scene object finder, and NULL cast/SEH return. Container lookup, packet binding,
valid-target cast and effect construction are explicit adapters. This does not
emulate the renderer, networking, or a full multiplayer session.
"""
import argparse
import json
from pathlib import Path
import struct

from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import *
import patch_cleansing_thunder_null as patch


class Native:
    OBJ, OWNER, SCENE, TARGET, PASSIVE, VTABLE, PACKET, EFFECT = [0x20000000 + i * 0x10000 for i in range(8)]
    STUB, STACK, RETURN = 0x21000000, 0x22008000, 0x21000F00
    REGS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
            UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EFLAGS)

    def __init__(self, data, command, state, alignment, event=29, object_code=26119):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        self.u.mem_map(patch.pe.IMAGE_BASE, struct.unpack_from('<I', data, opt + 56)[0])
        for va, size, raw in sections:
            if size:
                self.u.mem_write(va, data[raw:raw + size])
        for va, size in ((self.OBJ, 0x80000), (self.STUB, 0x1000), (0x22000000, 0x10000), (0, 0x1000)):
            self.u.mem_map(va, size)
        self.obj = self.OBJ + alignment
        self.u.mem_write(self.RETURN, b'\xCC')
        self.state, self.command, self.event, self.object_code = state, command, event, object_code
        self.put(0, 0xABCDE000)
        self.put(self.obj, self.VTABLE)
        self.put(self.VTABLE + 532, 0x020A8450)  # Actual owner getter.
        self.put(self.obj + 40, 0 if state == 'missing_owner' else self.OWNER)
        self.put(self.OWNER, self.VTABLE + 0x1000)
        self.put(self.VTABLE + 0x1000 + 124, 0x0276BC50)  # Actual scene getter thunk.
        self.put(self.OWNER + 0xC8, 0 if state == 'missing_scene' else self.SCENE)
        self.put(self.SCENE + 12, self.SCENE + 0x1000)
        self.put(self.SCENE + 16, self.SCENE + 0x1000 + (0 if state == 'empty_scene' else 4))
        self.put(self.SCENE + 0x1000, self.TARGET)
        self.put(self.TARGET, self.VTABLE + 0x2000)
        self.put(self.PASSIVE, self.VTABLE + 0x3000)
        self.put(self.EFFECT, self.VTABLE + 0x4000)
        self.put(0x056830CC, self.OWNER)  # Existing singleton; no bootstrap allocation.
        self.put(self.PACKET, command)
        self.put(self.PACKET + 4, 2)  # Non-player type: avoid unrelated fallback.
        self.put(self.PACKET + 8, 123)
        self.put(self.PACKET + 12, 456)
        self.put(self.obj + 0x800, 2)
        self.effects, self.faults, self.visited, self.resumes = [], [], set(), []
        self.finished, self.zero_mapped = False, True
        self.hooks = {
            0x026D2B60: lambda: self.ret(16, 0),  # Base callback.
            0x00D387E0: lambda: self.ret(4, self.obj + 0x800),  # Container lookup.
            0x038BBD60: self.bind_packet,
            0x02047360: lambda: self.ret(value=self.OWNER),  # Global context singleton.
            0x01FA2790: lambda: self.ret(value=self.OWNER),
            0x02046CC0: lambda: self.ret(4, 0),  # No remote fallback world.
            0x02995E80: lambda: self.ret(value=0),  # Empty removable-effect list.
            0x037DE240: lambda: self.ret(value=903),
            0x01931ED0: lambda: self.ret(value=self.EFFECT),
            0x01B1BD00: lambda: self.ret(4, self.EFFECT),
            0x008CCF60: self.vector_init,
        }
        self.stub(0x100, self.VTABLE + 0x3000 + 76, lambda: self.ret(value=object_code))
        self.stub(0x110, self.VTABLE + 0x2000 + 80, lambda: self.ret(value=2))
        self.stub(0x120, self.VTABLE + 0x2000 + 84, lambda: self.ret(value=999 if state == 'wrong_target' else 123))
        self.stub(0x130, self.VTABLE + 0x2000 + 1860, lambda: self.ret(value=self.TARGET + 0x100))
        self.stub(0x140, self.VTABLE + 0x2000 + 2380, lambda: self.effect('notify', 4))
        self.stub(0x150, self.VTABLE + 0x4000 + 152, lambda: self.ret(value=self.EFFECT + 0x100))
        self.stub(0x160, self.VTABLE + 0x4000 + 136, lambda: self.effect('apply', 36))
        for address in (0x37DF130, 0x37DF1B0, 0x37DF230, 0x37DF2B0, 0x37DF010):
            self.hooks[address] = lambda: self.effect('encoded_value', 8)
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.bad_memory)

    def put(self, at, value):
        self.u.mem_write(at, struct.pack('<I', value))

    def read(self, at):
        return struct.unpack('<I', self.u.mem_read(at, 4))[0]

    def reg(self, register):
        return self.u.reg_read(register)

    def arg(self, index=0):
        return self.read(self.reg(UC_X86_REG_ESP) + 4 + index * 4)

    def ret(self, pop=0, value=None):
        sp = self.reg(UC_X86_REG_ESP)
        if value is not None:
            self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    def stub(self, offset, slot, callback):
        self.put(slot, self.STUB + offset)
        self.hooks[self.STUB + offset] = callback

    def bind_packet(self):
        assert self.arg() == self.PACKET
        self.put(0x056A3D84 + 12, self.PACKET)
        self.put(0x056A3D84 + 16, 0)
        self.ret(4)

    def vector_init(self):
        self.put(self.reg(UC_X86_REG_ECX), 0)
        self.put(self.reg(UC_X86_REG_ECX) + 4, 0)
        self.ret(4)

    def effect(self, name, pop):
        self.effects.append([name, [self.arg(i) for i in range(pop // 4)]])
        self.ret(pop, 0)

    def bad_memory(self, _u, _access, address, size, _value, _context):
        self.faults.append(dict(ip=hex(self.reg(UC_X86_REG_EIP)), address=hex(address), size=size))
        return False

    def hook(self, _u, at, _size, _context):
        self.visited.add(at)
        if at in patch.SITES:
            self.seh = self.read(0)
            self.u.mem_unmap(0, 0x1000)
            self.zero_mapped = False
        if at in [site + 7 for site in patch.SITES]:
            self.resumes.append([self.reg(r) for r in self.REGS])
            self.u.mem_map(0, 0x1000)
            self.put(0, self.seh)
            self.zero_mapped = True
        if at == 0x01E36070 and self.arg() != 0:
            # Type-registry conversion is outside this defect. NULL runs the
            # real cast and native SEH restoration, not this adapter.
            assert self.arg() == self.TARGET
            self.ret(value=self.TARGET)
        elif at == self.RETURN:
            self.finished = True
            self.u.emu_stop()
        elif at in self.hooks:
            self.hooks[at]()

    def run(self):
        saved = [(UC_X86_REG_EBX, 0xBADF00D), (UC_X86_REG_ESI, 0xCAFE0100),
                 (UC_X86_REG_EDI, 0xCAFE0200), (UC_X86_REG_EBP, 0xCAFE0300)]
        for reg, value in saved + [(UC_X86_REG_ESP, self.STACK), (UC_X86_REG_ECX, self.obj), (UC_X86_REG_EFLAGS, 0x246)]:
            self.u.reg_write(reg, value)
        for i, value in enumerate((self.RETURN, self.PASSIVE, self.event, self.PACKET, 0)):
            self.put(self.STACK + 4 * i, value)
        try:
            self.u.emu_start(0x00D39420, 0xFFFFFFFF, count=10000)
        except UcError:
            if not self.faults:
                raise
        if self.finished:
            assert not self.faults
            assert self.reg(UC_X86_REG_ESP) == self.STACK + 20
            assert all(self.reg(r) == v for r, v in saved)
            assert self.read(0) == 0xABCDE000, 'native SEH not restored'
        else:
            assert self.faults, 'instruction budget exhausted'
        return dict(finished=self.finished, faults=self.faults, effects=self.effects,
                    resumes=self.resumes, registers=[self.reg(r) for r in self.REGS])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    original = args.input.read_bytes()
    candidate, receipt = patch.build(original)
    cases = []
    for command in (0, 1):
        for alignment in (0, 4, 1):
            for state in ('missing_owner', 'missing_scene', 'empty_scene', 'wrong_target', 'valid'):
                old = Native(original, command, state, alignment).run()
                new = Native(candidate, command, state, alignment).run()
                assert new['finished'], (command, state, new)
                if state in ('missing_owner', 'missing_scene'):
                    assert old['faults'] == [dict(ip='0x287a022', address='0x10', size=4)], old
                    assert not new['effects']
                else:
                    assert old == new, (command, state, old, new)
                    if state == 'valid':
                        assert new['effects']
                cases.append(dict(command=command, state=state, alignment=alignment, passed=True, oldFaults=old['faults']))
    for event, code in ((28, 26119), (29, 26118)):
        assert Native(original, 0, 'missing_scene', 0, event, code).run() == Native(candidate, 0, 'missing_scene', 0, event, code).run()
        cases.append(dict(event=event, objectCode=code, passed=True))
    for invalid in (original[:-1], bytes([original[0] ^ 1]) + original[1:], candidate):
        try:
            patch.build(invalid)
        except ValueError:
            pass
        else:
            raise AssertionError('invalid/already patched input accepted')
    args.report.write_text(json.dumps(dict(cases=cases, passed=len(cases), rejectedInputs=3, candidate=receipt,
                                          gameAcceptance='pending'), indent=2), encoding='utf-8')
    print(json.dumps(dict(passed=len(cases), rejectedInputs=3, outputSha256=receipt['outputSha256'])))


if __name__ == '__main__':
    main()
