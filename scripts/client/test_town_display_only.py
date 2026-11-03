"""Bounded native same-area placement, visual factory/writer/reset checks.

Resources, allocator, virtual leaves, protected values and scene tables are
explicit adapters. The real NOTI23 branch, lookup, factory, writer, retained
registration guard and reset execute; this is not a GPU or game acceptance.
"""
import argparse
import itertools
import json
from pathlib import Path
import struct

from unicorn.x86_const import *
import patch_town_display_only as patch
from town_display_native_memory import NativeMemory as Memory


class Native(Memory):
    MODEL = 0x32001000
    MVT = 0x30024000
    META = 0x30025000
    MANAGER = 0x30027000
    SCENE = 0x30028000
    LOAD, META_FN, DESTROY = 0x32008000, 0x32008010, 0x32008020

    def __init__(self, data):
        super().__init__(data)
        self.u.mem_write(self.STOP, b'\xcc')
        self.hooks = {}
        for address in (0x296D730, 0x296D940, 0x2964340, 0x37F2170, 0x26BD1E0,
                        0x37DF130, 0x37DF1B0, 0x37DF230, 0x37DF2B0, 0x37DF010,
                        0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0,
                        0x269E170, 0x26A1440, 0x269E700, 0x26CFB40, 0x352F8B0,
                        0x2971210, 0x32B4680, 0x288A260, 0x26A1CB0, 0x2972140,
                        0x2047360, 0x2046E80, self.LOAD, self.META_FN, self.DESTROY,
                        0x22BE730, 0x2527EB0):
            self.hooks[address] = lambda address=address: self.adapt(address)

    def prepare(self, existing=False, full=False, auxiliary=False, world=True, key=23):
        self.u.mem_write(self.ACTOR, bytes(0x1000))
        self.u.mem_write(self.MODEL, bytes(0x2000))
        self.u.mem_write(self.STACK - 0x8000, bytes(0x10000))
        self.key, self.auxiliary = key, auxiliary
        self.count.clear()
        self.put(self.ACTOR + 0x20, key)
        self.put(self.ACTOR + 0x4F4, self.MODEL if existing else 0)
        self.put(self.ACTOR + 0x4F8, self.ACTOR + 0x10000 if full else 0)
        self.put(self.MODEL, self.MVT)
        for offset, fn in ((0, self.DESTROY), (0x34C, self.LOAD), (0x38, self.META_FN)):
            self.put(self.MVT + offset, fn)
        self.put(0x56A3758, self.MANAGER)
        self.put(0x57B6FB4, self.ACTOR + 0x20000 if world else 0)
        self.put(0x57B6FB0, 0)
        self.put(0x55B3BDC, 0x12345678)
        self.put(self.ROOT - 0x14, key)
        self.put(self.ROOT - 0x1C, 120)
        self.put(self.ROOT - 0x20, 240)
        self.u.mem_write(self.ROOT - 0xF, bytes([1]))
        self.u.mem_write(self.ROOT - 0x10, bytes([3]))
        self.actor_before = bytes(self.u.mem_read(self.ACTOR, 0x1000))

    def adapt(self, address):
        this = self.reg(UC_X86_REG_ECX)
        if address == 0x296D730:
            assert self.arg() == self.key
            self.ret(value=self.ACTOR)
        elif address == 0x296D940:
            assert self.arg() == self.key
            self.ret(4, 1)
        elif address == 0x2964340:
            assert this == self.ACTOR
            self.ret(value=int(self.auxiliary))
        elif address == 0x37F2170:
            assert self.arg() == 0x13CC
            self.count['allocate'] += 1
            self.ret(value=self.MODEL)
        elif address == 0x26BD1E0:
            assert this == self.MODEL
            self.count['constructor_adapter'] += 1
            self.ret(value=self.MODEL)
        elif address == self.LOAD:
            assert this == self.MODEL and [self.arg(i) for i in range(4)] == [self.ACTOR + 0x30, 0, 0, 0]
            self.count['appearance_adapter'] += 1
            self.ret(16)
        elif address == self.META_FN:
            self.ret(value=self.META)
        elif address in (0x37DF130, 0x37DF1B0, 0x37DF230, 0x37DF2B0, 0x37DF010):
            assert this == self.META + 4
            self.ret(8)
        elif address in (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0):
            self.put(self.arg(1), self.key + 1)
            self.ret(8)
        elif address in (0x269E170, 0x26CFB40):
            assert this == self.MODEL
            self.ret(4)
        elif address in (0x26A1440, 0x269E700):
            assert this == self.MODEL
            self.ret()
        elif address == 0x352F8B0:
            assert [self.arg(i) for i in range(4)] == [self.ACTOR, self.MODEL, 0, 1]
            self.ret()
        elif address == 0x2971210:
            assert this == self.MANAGER and self.arg() == self.MODEL
            self.count['model_registration_adapter'] += 1
            self.ret(4)
        elif address == 0x288A260:
            assert this == self.SCENE
            assert [self.arg(i) for i in range(8)] == [0, 0x12, self.key, self.MODEL, 0, 0, 0, 1]
            self.count['place'] += 1
            self.ret(32)
        elif address == 0x26A1CB0:
            assert this == self.MODEL
            assert bytes(self.u.mem_read(self.arg(), 8)) == struct.pack('<HHHBB', self.key, 120, 240, 1, 3)
            self.count['position'] += 1
            self.ret(4)
        elif address == 0x2972140:
            assert this == self.MANAGER and self.arg() == self.key
            self.count['pending'] += 1
            self.ret(8)
        elif address == 0x2047360:
            self.ret(4, self.MODEL)
        elif address == 0x2046E80:
            self.ret(value=0)
        elif address == self.DESTROY:
            assert this == self.MODEL and self.arg() == 1
            self.count['destroy_adapter'] += 1
            self.ret(4)
        else:
            raise AssertionError(('unexpected UI/full-loader boundary', hex(address)))

    def area(self):
        guard = bytes(range(64))
        self.u.mem_write(self.STACK, guard)
        registers = {UC_X86_REG_EBP: self.ROOT, UC_X86_REG_ESP: self.STACK,
                     UC_X86_REG_EBX: self.SCENE, UC_X86_REG_EDI: 0, UC_X86_REG_ESI: 0x12345678}
        for register, value in registers.items():
            self.u.reg_write(register, value)
        self.stop = 0x203CC95
        self.u.emu_start(0x203CB86, 0, count=10000)
        assert self.reg(UC_X86_REG_EIP) == self.stop
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        assert bytes(self.u.mem_read(self.STACK, 64)) == guard
        assert self.reg(UC_X86_REG_EBP) == self.ROOT and self.reg(UC_X86_REG_EBX) == self.SCENE
        assert self.reg(UC_X86_REG_EDI) == 0
        after = bytes(self.u.mem_read(self.ACTOR, 0x1000))
        assert after[:0x4F4] == self.actor_before[:0x4F4] and after[0x4F8:] == self.actor_before[0x4F8:]

    def destroy(self):
        self.put(self.STACK, self.STOP)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_ECX, self.ACTOR)
        self.u.reg_write(UC_X86_REG_EBP, self.ROOT)
        self.stop = self.STOP
        try:
            self.u.emu_start(0x2965750, self.STOP, count=10000)
        except Exception:
            raise AssertionError(dict(phase='native reset', eip=hex(self.reg(UC_X86_REG_EIP)),
                                      esp=hex(self.reg(UC_X86_REG_ESP)), model=hex(self.rd(self.ACTOR + 0x4F4)),
                                      vtable=hex(self.rd(self.MODEL)), destructor=hex(self.rd(self.MVT)), counters=dict(self.count)))
        assert self.reg(UC_X86_REG_EIP) == self.STOP and self.reg(UC_X86_REG_ESP) == self.STACK + 4
        assert self.rd(self.ACTOR + 0x4F4) == 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise RuntimeError('preserve existing evidence')
    before = args.input.read_bytes()
    after, report = patch.build(before)
    original, candidate = Native(before), Native(after)
    cases = []
    for existing, full, auxiliary, world in itertools.product((False, True), repeat=4):
        for key in (0, 23, 65535):
            original.prepare(existing, full, auxiliary, world, key)
            candidate.prepare(existing, full, auxiliary, world, key)
            original.area()
            candidate.area()
            creates = not existing and not full and not auxiliary and world and key != 0
            assert candidate.count['allocate'] == int(creates)
            assert candidate.count['place'] == int(existing or creates)
            assert original.count['place'] == int(existing)
            assert candidate.count['pending'] == int(not (existing or creates))
            assert candidate.count['appearance_adapter'] == int(creates)
            if not creates:
                assert candidate.count == original.count
            candidate.area()
            assert candidate.count['allocate'] == int(creates)
            if creates:
                candidate.destroy()
                candidate.destroy()  # Native repeated reset is a no-op.
                assert candidate.count['destroy_adapter'] == 1
                candidate.area()
                assert candidate.count['allocate'] == 2 and candidate.count['place'] == 3
            cases.append(dict(existing=existing, full=full, auxiliary=auxiliary, world=world, key=key,
                              created=creates, counters=dict(candidate.count)))
    rejected = 0
    for invalid in (before[:-1], bytes([before[0] ^ 1]) + before[1:], after):
        try:
            patch.build(invalid)
        except ValueError:
            rejected += 1
        else:
            raise AssertionError('bad/repeated input accepted')
    import pefile
    assert pefile.PE(data=after, fast_load=True).verify_checksum()
    result = dict(status='passed', cases=len(cases), vectors=cases, wrong_input_rejections=rejected,
                  output_sha256=report['output_sha256'],
                  native=['same-area NOTI23 placement/pending branch', 'visual getter', 'visual factory',
                          'visual writer', 'retained registration guard', 'visual reset'],
                  adapters=['scene/namespace lookup', 'allocator', 'resource constructor and appearance load',
                            'protected identity values', 'visual initialization leaves', 'scene table registration',
                            'position application', 'destructor leaf'],
                  whole_frame_timing_tested=False, gameplay_acceptance='pending', added_dll=False)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2), encoding='utf-8')
    print(json.dumps({key: result[key] for key in ('status', 'cases', 'output_sha256')}))


if __name__ == '__main__':
    main()
