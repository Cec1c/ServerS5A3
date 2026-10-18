"""Bounded offline full OP19 ACK handler regression (requires Unicorn 2.x).

Usage: python test_wear_unlock_full_handler_abi.py --baseline OLD.exe
       --candidate NEW.exe --output receipt.json
Alternatively set OP19_ABI_BASELINE_EXE / OP19_ABI_CANDIDATE_EXE.
No image is modified or launched. UI leaves and the cookie checker are ABI
models; parser, guard, accessor, vector pop and handler epilogue execute image
instructions. This is not a receive-admission or live UI integration test.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
    UC_X86_REG_ESP, UC_X86_REG_EIP, UC_X86_REG_EFLAGS)

BASELINE_SHA = '153903d7e76c9e2feed6fd19c053742365f04484d6dadcf24b78d5b58daf0391'
CANDIDATE_SHA = '29a3f7f141841483f492e63834aac6c3f0eeb50baf373c0100ba6a3e33ec32b8'
ENTRY, GUARD, TAIL, EPILOGUE = 0x1fdece0, 0x6e9d000, 0x1fdfca8, 0x1fe0214
STACK, ENTRY_SP, HEAP, STOP = 0x10000000, 0x10030000, 0x11000000, 0x12000000
OBJECTS, VECTORS = (HEAP+0x1000, HEAP+0x2000), (HEAP+0x4000, HEAP+0x5000)
BODY, COOKIE, SEH = HEAP+0x8000, 0x1a2b3c4d, 0xabcdef00
REGS = {'ebx': UC_X86_REG_EBX, 'esi': UC_X86_REG_ESI,
        'edi': UC_X86_REG_EDI, 'ebp': UC_X86_REG_EBP}
SENTINELS = dict(ebx=0x11112222, esi=0x33334444, edi=0x55556666, ebp=0x77778888)
STATES = ([7], [], [4], [4, 7], [7, 4])
# Audited native returns: UI initialization ret12, singleton ret0,
# UI refresh(container=3) ret4, final singleton/refresh ret0, cookie check ret0.
STUBS = {0x32b94a0: 12, 0x22efc20: 0, 0x22ebab0: 4,
         0x12f3140: 0, 0x12f4090: 0, 0x3e72ce4: 0}
RANGES = ((ENTRY, 0x1fded90), (TAIL, 0x1fdfcbb), (0x1fe0208, 0x1fe0230),
          (GUARD, GUARD+0x100), (0x2531a80, 0x2531b57),
          (0x25f0290, 0x25f02a8), (0x37e82e0, 0x37e8343),
          (0x37e8350, 0x37e83b8), (0x37e83c0, 0x37e8426))


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def u32(data, at=0):
    return struct.unpack_from('<I', data, at)[0]


class Image:
    def __init__(self, path, expected):
        self.data = Path(path).read_bytes()
        self.sha = hashlib.sha256(self.data).hexdigest()
        require(self.sha == expected, 'unexpected image SHA256: '+self.sha)
        pe = u32(self.data, 0x3c)
        require(self.data[pe:pe+4] == b'PE\0\0', 'PE signature')
        require(struct.unpack_from('<H', self.data, pe+4)[0] == 0x14c, 'x86 image')
        opt = pe+24
        self.base, self.size = u32(self.data, opt+28), u32(self.data, opt+56)
        require(self.base == 0x400000, 'image base mismatch')
        table = opt+struct.unpack_from('<H', self.data, pe+20)[0]
        count = struct.unpack_from('<H', self.data, pe+6)[0]
        self.sections = []
        for i in range(count):
            q = table+i*40
            vs, rva, size, raw = struct.unpack_from('<IIII', self.data, q+8)
            require(raw+size <= len(self.data), 'section raw bounds')
            require(rva+max(vs, size) <= self.size, 'section virtual bounds')
            self.sections.append((self.base+rva, size, raw))

    def bytes_at(self, address, size):
        for va, length, raw in self.sections:
            if va <= address and address+size <= va+length:
                return self.data[raw+address-va:raw+address-va+size]
        raise AssertionError('unbacked code range')

    def map(self, uc):
        uc.mem_map(self.base, (self.size+0xfff)&~0xfff)
        for va, size, raw in self.sections:
            if size:
                uc.mem_write(va, self.data[raw:raw+size])


def run_case(image, src, states):
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    image.map(uc)
    uc.mem_map(0, 0x1000)
    uc.mem_map(STACK, 0x40000)
    uc.mem_map(HEAP, 0x10000)
    uc.mem_map(STOP, 0x1000)
    def put(address, value):
        uc.mem_write(address, struct.pack('<I', value))
    def get(address):
        return u32(uc.mem_read(address, 4))
    for i, obj in enumerate(OBJECTS):
        uc.mem_write(obj, bytes([0x91+i])*0x100)
        put(obj+0x68, HEAP+0x6000+i*16)
        put(HEAP+0x6000+i*16, obj+0x68)
        values = list(states) if i == src else [0x1234, 7]
        uc.mem_write(VECTORS[i], bytes([0xcc])*0x100)
        for j, value in enumerate(values):
            put(VECTORS[i]+j*4, value)
        put(obj+0x6c, VECTORS[i])
        put(obj+0x70, VECTORS[i]+len(values)*4)
        put(obj+0x74, VECTORS[i]+0x100)
    put(0x56a379c, OBJECTS[0])
    put(0x56a37b0, OBJECTS[1])
    put(0x56a3798, HEAP+0xa000)
    put(0x55b3bdc, COOKIE)
    put(0, SEH)
    # Literal ten-byte payload after dispatcher consumes success=01.
    payload = bytes.fromhex('00310000000000030c00' if src == 0 else '01310000000000030c00')
    uc.mem_write(BODY, payload)
    put(0x57f9ad0, BODY)
    put(0x57f9ad4, len(payload))
    uc.mem_write(ENTRY_SP, struct.pack('<IIIII', STOP, 19, 1, 0, 0))
    uc.reg_write(UC_X86_REG_ESP, ENTRY_SP)
    uc.reg_write(UC_X86_REG_EFLAGS, 0x202)
    for name, reg in REGS.items():
        uc.reg_write(reg, SENTINELS[name])
    before_heap = bytes(uc.mem_read(HEAP, 0x10000))
    events, writes, parser_calls, frames = [], [], [], {}
    instructions = 0

    def on_write(machine, access, address, size, value, _):
        writes.append((address, size, value))
        allowed = (STACK <= address and address+size <= STACK+0x40000 or
                   address == 0 and size == 4 or
                   address in (0x57f9ad0, 0x57f9ad4, OBJECTS[src]+0x70) and size == 4)
        require(allowed, 'unexpected native write '+hex(address))

    def on_code(machine, address, size, _):
        nonlocal instructions
        instructions += 1
        if address == STOP:
            events.append('returned')
            machine.emu_stop()
            return
        require(address in STUBS or any(lo <= address < hi for lo, hi in RANGES),
                'unexpected execution '+hex(address))
        if address in (0x37e82e0, 0x37e8350, 0x37e83c0):
            parser_calls.append(hex(address))
        if address == GUARD:
            ebp = machine.reg_read(UC_X86_REG_EBP)
            frames['ebp'] = ebp
            frames['guard_esp'] = machine.reg_read(UC_X86_REG_ESP)
            frames['guard_flags'] = machine.reg_read(UC_X86_REG_EFLAGS)
            parsed = (machine.mem_read(ebp-0xe0, 1)[0], get(ebp-0xe4),
                      get(ebp-0xf4), machine.mem_read(ebp-0xd5, 1)[0], get(ebp-0xdc))
            require(parsed == (src, 49, 0, 3, 12), 'native parsed fields')
            require(get(0x57f9ad0) == BODY+10 and get(0x57f9ad4) == 0, 'payload consumption')
            events.append('native_parser_complete')
        if address == TAIL:
            frames['tail_esp'] = machine.reg_read(UC_X86_REG_ESP)
            frames['tail_flags'] = machine.reg_read(UC_X86_REG_EFLAGS)
        if address == EPILOGUE:
            frames['epilogue_esp'] = machine.reg_read(UC_X86_REG_ESP)
            events.append('native_epilogue')
        if address == 0x2531a80:
            require(get(machine.reg_read(UC_X86_REG_ESP)+4) == src, 'native source accessor arg')
            events.append('native_source_accessor')
        if address == 0x25f0290:
            require(machine.reg_read(UC_X86_REG_ECX) == OBJECTS[src], 'native pop this')
            events.append('native_pop')
        if address not in STUBS:
            return
        sp = machine.reg_read(UC_X86_REG_ESP)
        if address == 0x32b94a0:
            require(tuple(get(sp+i) for i in (4, 8, 12)) == (0x332, 0xffffffff, 0), 'UI init args')
        if address == 0x22ebab0:
            require(get(sp+4) == 3 and machine.reg_read(UC_X86_REG_ECX) == HEAP+0xa100, 'UI container refresh args')
        if address == 0x12f4090:
            require(machine.reg_read(UC_X86_REG_ECX) == HEAP+0xa200, 'UI final refresh this')
        if address == 0x3e72ce4:
            require(machine.reg_read(UC_X86_REG_ECX) == COOKIE, 'security cookie')
        events.append('abi_leaf_'+hex(address)+'_ret'+str(STUBS[address]))
        # Deliberately clobber volatile registers; preserve callee-saved registers.
        result = {0x22efc20: HEAP+0xa100, 0x12f3140: HEAP+0xa200}.get(address, 0)
        machine.reg_write(UC_X86_REG_EAX, result)
        machine.reg_write(UC_X86_REG_ECX, 0xc1c1c1c1)
        machine.reg_write(UC_X86_REG_EDX, 0xd2d2d2d2)
        machine.reg_write(UC_X86_REG_ESP, sp+4+STUBS[address])
        machine.reg_write(UC_X86_REG_EIP, get(sp))

    uc.hook_add(UC_HOOK_CODE, on_code)
    uc.hook_add(UC_HOOK_MEM_WRITE, on_write)
    uc.emu_start(ENTRY, STOP+1, timeout=2000000, count=20000)
    require('returned' in events, 'instruction/time bound reached before native return')
    require(parser_calls == ['0x37e82e0', '0x37e8350', '0x37e83c0', '0x37e82e0', '0x37e8350'], 'exact parser calls')
    require(get(0) == SEH, 'SEH restoration')
    require(uc.reg_read(UC_X86_REG_ESP) == ENTRY_SP+4, 'native cdecl return stack')
    after_heap = bytes(uc.mem_read(HEAP, 0x10000))
    end_offset = OBJECTS[src]+0x70-HEAP
    require(before_heap[:end_offset] == after_heap[:end_offset] and
            before_heap[end_offset+4:] == after_heap[end_offset+4:], 'asset/canary memory changed')
    expected = list(states[:-1] if states and states[-1] == 7 else states)
    actual_count = (get(OBJECTS[src]+0x70)-VECTORS[src])//4
    require(0 <= actual_count <= len(states), 'vector bounds')
    actual = [get(VECTORS[src]+i*4) for i in range(actual_count)]
    registers = {name: uc.reg_read(reg) for name, reg in REGS.items()}
    abi_ok = registers == SENTINELS
    stack_delta = frames['epilogue_esp']-(frames['ebp']-0x128)
    flags_ok = frames['guard_flags'] == frames['tail_flags']
    state_ok = actual == expected
    pop_writes = [w for w in writes if w[0] == OBJECTS[src]+0x70]
    return dict(src=src, before=list(states), expected=expected, after=actual,
        passed=abi_ok and stack_delta == 0 and state_ok and flags_ok,
        abi_ok=abi_ok, state_ok=state_ok, flags_preserved=flags_ok,
        registers={k: hex(v) for k,v in registers.items()},
        expected_registers={k: hex(v) for k,v in SENTINELS.items()},
        epilogue_stack_delta=stack_delta, return_stack_ok=True, seh_restored=True,
        payload_consumed=10, assets_and_canaries_unchanged=True,
        state_end_writes=[dict(address=hex(a), size=s, value=hex(v)) for a,s,v in pop_writes],
        instructions=instructions, events=events)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', default=os.environ.get('OP19_ABI_BASELINE_EXE'))
    parser.add_argument('--candidate', default=os.environ.get('OP19_ABI_CANDIDATE_EXE'))
    parser.add_argument('--candidate-sha256', default=CANDIDATE_SHA)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not args.baseline:
        parser.error('--baseline or OP19_ABI_BASELINE_EXE is required')
    require(args.output.resolve() not in [Path(p).resolve() for p in (args.baseline, args.candidate) if p], 'report must not replace image')
    report = dict(scope='offline full OP19 success handler; ABI-modeled UI leaves; no admission/live UI claim', images={})
    old = Image(args.baseline, BASELINE_SHA)
    images = [('baseline', old)]
    if args.candidate:
        candidate = Image(args.candidate, args.candidate_sha256.lower())
        for lo, hi in RANGES:
            if lo != GUARD:
                require(old.bytes_at(lo, hi-lo) == candidate.bytes_at(lo, hi-lo), 'non-guard native code changed')
        images.append(('candidate', candidate))
    for label, image in images:
        cases = []
        for src in (0, 1):
            for states in STATES:
                case = run_case(image, src, states)
                cases.append(case)
                print(label, 'src='+str(src), states, 'PASS' if case['passed'] else 'FAIL',
                      'ABI='+str(case['abi_ok']), 'state='+str(case['state_ok']),
                      'epilogue_delta='+str(case['epilogue_stack_delta']), flush=True)
        report['images'][label] = dict(sha256=image.sha, size=len(image.data), cases=cases)
    old_cases = report['images']['baseline']['cases']
    report['old_regression_reproduced'] = all(not c['abi_ok'] and c['epilogue_stack_delta'] == -28 and c['after'] == c['before'] for c in old_cases)
    report['candidate_passed'] = all(c['passed'] and len(c['state_end_writes']) == int(bool(c['before']) and c['before'][-1] == 7) for c in report['images']['candidate']['cases']) if args.candidate else None
    report['passed'] = report['old_regression_reproduced'] and report['candidate_passed'] is not False
    # Verify originals remain identical after every offline case.
    for label, path in [('baseline', args.baseline), ('candidate', args.candidate)]:
        if path:
            require(hashlib.sha256(Path(path).read_bytes()).hexdigest() == report['images'][label]['sha256'], 'input changed during run')
    args.output.write_text(json.dumps(report, indent=2)+'\n', encoding='utf-8')
    require(report['passed'], 'full-handler ABI regression failed; see receipt')
    print('PASS: old regression reproduced; candidate='+str(report['candidate_passed']))


if __name__ == '__main__':
    main()
