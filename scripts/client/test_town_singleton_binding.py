"""Current EXE kind0 singleton member binding without a full actor.

Runs the native member-slot callsite, retained context guard, member writer,
lookup, party-field getter and slot state update. Namespace decoding, locks,
actor table, scene query and local actor lookup are bounded adapters. No game/GPU run.
"""
import argparse
import json
from pathlib import Path

from unicorn.x86_const import *
from town_display_native_memory import NativeMemory as Memory
import patch_receivers_v128 as pe

EXPECTED = 'a5f1d5ef432064cccaeac6362d5f1405fceddd62739da85661154fc008d47f7d'


class Native(Memory):
    PARTY = 0x30024000

    def invalid(self, _u, access, address, size, _value, _ctx):
        self.invalid_access = (hex(self.reg(UC_X86_REG_EIP)), access, hex(address), size,
                               [hex(at) for at in self.trace])
        return False

    def hook(self, uc, at, size, context):
        self.trace.append(at)
        self.trace = self.trace[-24:]
        super().hook(uc, at, size, context)

    def __init__(self, data):
        self.trace = []
        super().__init__(data)
        self.hooks = {}
        for address in (0x37E3940, 0x37E3970, 0x37DF550, 0x37DF5C0, 0x37DF630,
                        0x37DF6A0, 0x37DF7F0, 0x296D730, 0x296DD80, 0x1FA2790, 0x2526B50,
                        0x22BE730, 0x2965BD0, 0x2527EB0):
            self.hooks[address] = lambda address=address: self.adapt(address)

    def adapt(self, address):
        if address in (0x37E3940, 0x37E3970):
            self.ret()
        elif address in (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0):
            role = self.reg(UC_X86_REG_ECX)
            assert role in (0x56A1E80, 0x56A1E74)
            self.put(self.arg(1), 10 if role == 0x56A1E80 else 1)
            self.ret(8)
        elif address == 0x296D730:
            self.ret(value=self.ACTOR if self.arg() == 23 else 0)
        elif address == 0x296DD80:
            self.ret(value=0)
        elif address == 0x1FA2790:
            self.ret(value=self.ACTOR + 0x20000)
        elif address == 0x2526B50:
            self.ret(value=1)
        else:
            raise AssertionError(('singleton binding invoked a model/LOADING loader', hex(address)))

    def run(self, slot, full):
        self.u.mem_write(self.ACTOR, bytes(0x1000))
        self.u.mem_write(self.PARTY, bytes(0x1000))
        self.u.mem_write(self.STACK - 0x8000, bytes(0x10000))
        self.put(self.ACTOR + 0x20, 23)
        self.put(self.ACTOR + 0x4F8, self.ACTOR + 0x10000 if full else 0)
        self.put(self.PARTY + 0x70, 1)
        self.put(self.PARTY + 0x74, 10)
        for index in range(8):
            self.put(self.PARTY + 0x2C + index * 4, 0xFFFF)
        self.put(self.ROOT + slot * 2 - 0x28, 23)
        self.u.mem_write(self.ROOT + slot - 0x18, b'\1')
        self.put(0x56A3758, self.ACTOR + 0x20000)
        self.put(0x55B3BDC, 0x12345678)
        self.put(self.ROOT - 0xC, 0)
        self.u.reg_write(UC_X86_REG_EBP, self.ROOT)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EBX, self.PARTY)
        self.u.reg_write(UC_X86_REG_ESI, slot)
        self.stop = 0x2017466
        self.invalid_access = None
        try:
            self.u.emu_start(0x2017350, self.stop, count=10000)
        except Exception as error:
            raise AssertionError(('native fixture failure', slot, full, self.invalid_access)) from error
        assert self.reg(UC_X86_REG_EIP) == self.stop
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        assert self.reg(UC_X86_REG_EBX) == self.PARTY and self.reg(UC_X86_REG_ESI) == slot
        assert self.rd(self.PARTY + 0x2C + slot * 4) == 23
        assert self.rd(self.ACTOR + 0x4B4 + 0x14) == self.PARTY
        assert bytes(self.u.mem_read(self.ACTOR + 0x4B4 + 0x1A, 1)) == b'\1'
        assert self.rd(self.ACTOR + 0x4F4) == 0
        assert self.rd(self.ACTOR + 0x4F8) == (self.ACTOR + 0x10000 if full else 0)
        return dict(slot=slot, full=full, native_party_pointer=True, full_actor_unchanged=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise RuntimeError('preserve existing proof')
    data = args.input.read_bytes()
    if pe.sha(data) != EXPECTED:
        raise ValueError('exact current EXE required')
    native = Native(data)
    cases = [native.run(slot, full) for slot in range(8) for full in (False, True)]
    result = dict(status='passed', client_sha256=EXPECTED, cases=cases, count=len(cases),
                  native=['kind0 slot callsite', 'existing context guard', '29733B0 member writer',
                          '29730E0 lookup', '29614D0 party field', 'kind0 member-state store'],
                  adapters=['global namespace decode', 'namespace locks', 'actor table', 'scene query', 'local actor lookup'],
                  whole_handler_or_rendering=False, client_modified=False, gameplay_acceptance='pending')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2), encoding='utf-8')
    print(json.dumps(dict(status='passed', cases=len(cases), client_modified=False)))


if __name__ == '__main__':
    main()
