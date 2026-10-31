"""Bounded V201 machine-code regression. No Windows game process is started.

Executes the real switch, five-byte body consumption, slot validators, normal
equipment getter, proxy getter and whole opcode-28 branch up to common cleanup.
Scene lookup, typed-object registry, packet readers, item-value codec and final
UI effects are explicit adapters; this is not an end-to-end multiplayer test.
"""
import argparse
import json
from pathlib import Path
import struct

from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import *
import patch_peer28_null_v201 as patch


class Native:
    FRAME, STACK = 0x21018000, 0x21010000
    ACTOR, TABLE, ITEM, ITEM_TABLE, INFO = 0x20010000, 0x20020000, 0x20024000, 0x20025000, 0x20026000
    NETWORK, READERS, PACKET, STUB = 0x23000000, 0x20030000, 0x20031000, 0x22000000

    def __init__(self, data, slot=12, state='empty', local=False, broken=False, proxy=None):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        self.u.mem_map(patch.pe.IMAGE_BASE, struct.unpack_from('<I', data, opt + 56)[0])
        for va, size, raw in sections:
            if size:
                self.u.mem_write(va, data[raw:raw + size])
        # Page zero stays unmapped: NULL reads must really fault in the baseline.
        for at, size in ((0x20000000, 0x40000), (0x21000000, 0x40000), (self.STUB, 4096), (self.NETWORK, 0x20B000)):
            self.u.mem_map(at, size)
        self.body = struct.pack('<HHB', 0, 123, slot)
        self.cursor, self.effects, self.lookups, self.faults = 0, [], [], []
        self.visited, self.resume = set(), None
        self.local, self.broken, self.state = local, broken, state
        self.put(self.PACKET, 28 << 8)
        self.put(self.NETWORK, self.READERS)
        self.put(self.NETWORK + 0x20A028, self.ACTOR)
        self.put(self.READERS + 0x54, self.STUB + 0x10)
        self.put(self.READERS + 0x58, self.STUB + 0x20)
        self.put(self.ACTOR, self.TABLE)
        self.put(self.TABLE + 0x44, self.STUB + 0x30)
        self.put(self.TABLE + 0xB54, 0x2771FB0)
        self.put(self.ITEM, self.ITEM_TABLE)
        self.put(self.ITEM_TABLE + 0x160, self.STUB + 0x40)
        self.put(self.ITEM_TABLE + 0x178, self.STUB + 0x50)
        if slot < 35 and state in ('valid', 'placeholder'):
            self.put(self.ACTOR + 0x3230 + slot * 4, self.ITEM)
            if state == 'placeholder':
                self.put(self.ACTOR + 0x5F70 + slot * 4, self.ITEM)
        if proxy:
            self.put(self.TABLE + 0xB54, proxy)
        self.hooks = {
            self.STUB + 0x10: lambda: self.reader(1),
            self.STUB + 0x20: lambda: self.reader(2),
            self.STUB + 0x30: lambda: self.ret(value=0),
            self.STUB + 0x40: self.item_info,
            self.STUB + 0x50: lambda: self.ret(value=int(broken)),
            0xC23A00: lambda: self.ret(value=self.ACTOR),
            0x287A000: self.actor_lookup,
            0x1E36150: lambda: self.ret(value=0 if state == 'missing_actor' else self.ACTOR),
            0x29610E0: lambda: self.ret(value=self.ACTOR if local else self.ACTOR + 0x100),
            0x9982B0: lambda: self.ret(value=self.INFO),
            0x994D00: lambda: self.ret(value=0),
            0x1C5B5E0: lambda: self.ret(value=self.ITEM if state == 'valid' else 0),
            0x2570C40: lambda: self.ret(value=0),
            0x37DE240: lambda: self.ret(value=self.INFO),
            0x2531940: self.message,
            0x2326440: lambda: (self.effects.append('refresh'), self.ret()),
        }
        for ea in (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0):
            self.hooks[ea] = self.decode_value
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.bad_memory)

    def put(self, at, *values):
        self.u.mem_write(at, struct.pack('<' + 'I' * len(values), *values))

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

    def reader(self, width):
        assert self.cursor + width <= len(self.body)
        self.u.mem_write(self.arg(), self.body[self.cursor:self.cursor + width])
        self.cursor += width
        self.ret(4)

    def actor_lookup(self):
        self.lookups.append([self.arg(i) for i in range(3)])
        self.ret(12, self.ACTOR)

    def item_info(self):
        assert self.reg(UC_X86_REG_ECX) == self.ITEM
        self.effects.append('item_info')
        self.ret(value=self.INFO)

    def decode_value(self):
        self.put(self.arg(1), 10)
        self.ret(8)

    def message(self):
        self.effects.append('message')
        self.ret()

    def bad_memory(self, _u, access, address, size, _value, _context):
        self.faults.append(dict(ip=hex(self.reg(UC_X86_REG_EIP)), address=hex(address), size=size))
        return False

    def hook(self, _u, at, _size, _context):
        self.visited.add(at)
        if at == patch.CONTINUE:
            self.resume = [self.reg(r) for r in (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
                                                UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI,
                                                UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EFLAGS)]
        if at == patch.MISSING:
            self.u.emu_stop()
        elif at in self.hooks:
            self.hooks[at]()

    def run(self):
        for reg, value in ((UC_X86_REG_EBP, self.FRAME), (UC_X86_REG_ESP, self.STACK),
                           (UC_X86_REG_ESI, self.NETWORK), (UC_X86_REG_EDI, self.PACKET),
                           (UC_X86_REG_EBX, 0), (UC_X86_REG_EFLAGS, 0x246)):
            self.u.reg_write(reg, value)
        try:
            self.u.emu_start(0x329C0BC, 0xFFFFFFFF, count=10000)
        except UcError:
            if not self.faults:
                raise
        assert self.cursor == 5
        assert self.lookups == [[0, 123, 1]]
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return dict(end=hex(self.reg(UC_X86_REG_EIP)), effects=self.effects,
                    faults=self.faults, consumed=self.cursor, resume=self.resume)


def verify(data, candidate):
    built, receipt = patch.build(data)
    assert built == candidate
    rows = []
    for state, slots in (('empty', (0, 12, 13, 34, 35, 255)), ('placeholder', (12, 13, 34))):
        for slot in slots:
            before = Native(data, slot, state).run()
            after = Native(candidate, slot, state).run()
            assert before['faults'] == [dict(ip='0x32a5f58', address='0x0', size=4)]
            assert after['end'] == hex(patch.MISSING) and not after['faults'] and not after['effects']
            rows.append(dict(state=state, slot=slot, before=before, after=after))
    for proxy in (0x13654F0, 0x166E740, 0x174C0C0):
        before, after = Native(data, proxy=proxy).run(), Native(candidate, proxy=proxy).run()
        assert before['faults'] == [dict(ip='0x32a5f58', address='0x0', size=4)]
        assert after['end'] == hex(patch.MISSING) and not after['faults'] and not after['effects']
        rows.append(dict(proxy=hex(proxy), before=before, after=after))
    for local in (False, True):
        for broken in (False, True):
            for slot in (12, 13, 31, 34):
                before = Native(data, slot, 'valid', local, broken).run()
                after = Native(candidate, slot, 'valid', local, broken).run()
                assert not before['faults'] and before == after
                assert before['effects'].count('item_info') == (2 if local else 1)
                assert ('message' in before['effects']) == (local and broken)
                rows.append(dict(state='valid', local=local, broken=broken, slot=slot, result=after))
    for local in (False, True):
        before, after = Native(data, state='missing_actor', local=local).run(), Native(candidate, state='missing_actor', local=local).run()
        assert before == after and not after['effects'] and not after['faults']
        rows.append(dict(state='missing_actor', local=local, result=after))
    for invalid in (data[:-1], data[:4096] + bytes([data[4096] ^ 1]) + data[4097:], candidate):
        try:
            patch.build(invalid)
        except ValueError:
            pass
        else:
            raise AssertionError('unknown/truncated/already-patched input accepted')
    return dict(receipt=receipt, cases=len(rows), rows=rows, rejectedInputs=3,
                scope='bounded x86 emulation; no game process; multiplayer acceptance pending')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--candidate', type=Path, required=True)
    p.add_argument('--report', type=Path, required=True)
    a = p.parse_args()
    result = verify(a.input.read_bytes(), a.candidate.read_bytes())
    a.report.write_text(json.dumps(result, indent=2), encoding='utf-8')
    print('PASS', result['cases'], 'native cases and 3 rejected inputs')
