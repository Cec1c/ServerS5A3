"""Execute the v142/v143 receive gates and native 561 reader/UI state update.

The native updater runs with explicit preloaded UI/reward vectors. Object
lookup, protected scalar storage, clock/control methods, RTTI and active-event
lookup are fixtures. Full construction, rendering and networking are excluded.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'deploy/client'))
from online_reward_receive_gate import build, GATE_VA, BITMAP_VA

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--exe', type=Path, required=True)
p.add_argument('--candidate', type=Path, required=True)
p.add_argument('--receipt', type=Path, required=True)
a = p.parse_args()
before = a.exe.read_bytes()
after = a.candidate.read_bytes()
assert build(before)[0] == after
pe = pefile.PE(data=after, fast_load=True)
base = pe.OPTIONAL_HEADER.ImageBase
u = Uc(UC_ARCH_X86, UC_MODE_32)
u.mem_map(base, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) // 4096 * 4096)
for section in pe.sections:
    u.mem_write(base + section.VirtualAddress, section.get_data())
u.mem_map(0, 4096)
u.mem_map(0x10000000, 0x20000)
u.mem_map(0x20000000, 0x20000)
SP, STOP, OBJ, RECORDS, AUX, BUFFER = 0x10010000, 0x10001000, 0x20001000, 0x20003000, 0x20004000, 0x20006000
state = {}
protected = {}


def r(at):
    return struct.unpack('<I', u.mem_read(at, 4))[0]


def w(at, value):
    u.mem_write(at, struct.pack('<I', value & 0xffffffff))


def ret(value=0, clean=0):
    sp = u.reg_read(UC_X86_REG_ESP)
    target = r(sp)
    u.reg_write(UC_X86_REG_EAX, value)
    u.reg_write(UC_X86_REG_ESP, sp + 4 + clean)
    u.reg_write(UC_X86_REG_EIP, target)


def hook(vm, at, size, unused):
    if at == STOP:
        u.emu_stop()
        return
    if state['mode'] == 'gate':
        return
    sp, ecx = u.reg_read(UC_X86_REG_ESP), u.reg_read(UC_X86_REG_ECX)
    if at == 0x3e738b1:
        raise AssertionError('native vector fail-fast')
    if at == 0x1e80ab0:
        dest = r(sp + 4)
        w(dest, OBJ)
        w(dest + 4, 0)
        state['object_lookups'] += 1
        ret(dest)
    elif at in (0x37df550, 0x37df5c0, 0x37df630, 0x37df6a0, 0x37df7f0):
        w(r(sp + 8), protected[ecx])
        ret(0, 8)
    elif at in (0x37df130, 0x37df1b0, 0x37df230, 0x37df2b0, 0x37df010):
        protected[ecx] = r(r(sp + 4))
        ret(0, 8)
    elif at == 0x38161e0:
        ret(int(state['timer_running']))
    elif at in (0x3816960, 0x38169e0):
        state['timer_running'] = at == 0x3816960
        state['timer_calls'].append('start' if state['timer_running'] else 'stop')
        ret()
    elif at == 0x38168f0:
        state['timer_config'] = [r(sp + 4), r(sp + 8)]
        ret(0, 8)
    elif at == 0x38165e0:
        state['timer_value'] = r(sp + 4)
        ret(0, 4)
    elif at in (0x38163a0, 0x3816470):
        ret(0, 4 if at == 0x3816470 else 0)
    elif at == 0x32ba120:
        ret(0, 4)
    elif at == 0x1e7eda0:
        state['progress_display'].append(r(sp + 4))
        ret(0, 4)
    elif at == 0x3e73cb6:
        ret(0)
    elif at == 0x2303380:
        ret(OBJ)
    elif at == 0x22ff610:
        state['event_lookups'].append(r(sp + 4))
        ret(int(state['event_active']), 4)


u.hook_add(UC_HOOK_CODE, hook)


def run(start, args=(), ecx=0):
    for reg, value in ((UC_X86_REG_ESP, SP), (UC_X86_REG_EBP, 0), (UC_X86_REG_EAX, 0),
                       (UC_X86_REG_ECX, ecx), (UC_X86_REG_EDX, 0), (UC_X86_REG_EBX, 0),
                       (UC_X86_REG_ESI, 0), (UC_X86_REG_EDI, 0), (UC_X86_REG_EFLAGS, 0x202)):
        u.reg_write(reg, value)
    w(SP, STOP)
    for index, value in enumerate(args):
        w(SP + 4 + index * 4, value)
    try:
        u.emu_start(start, 0, count=100000)
    except Exception as exc:
        raise AssertionError(dict(eip=hex(u.reg_read(UC_X86_REG_EIP)),
                                  ecx=hex(u.reg_read(UC_X86_REG_ECX)),
                                  esp=hex(u.reg_read(UC_X86_REG_ESP)), state=state)) from exc
    assert u.reg_read(UC_X86_REG_EIP) == STOP
    assert u.reg_read(UC_X86_REG_ESP) == SP + 4 + (4 if state['mode'] == 'gate' else 0)
    return u.reg_read(UC_X86_REG_EAX)


state['mode'] = 'gate'
gate_results = []
for data in (before, after):
    for section in pe.sections:
        if section.VirtualAddress + base <= BITMAP_VA < section.VirtualAddress + base + section.SizeOfRawData:
            offset = section.PointerToRawData + BITMAP_VA - base - section.VirtualAddress
            u.mem_write(BITMAP_VA, data[offset:offset + 256])
    values = [bool(run(GATE_VA, (op,))) for op in range(65536)]
    gate_results.append(values)
delta = [op for op in range(65536) if gate_results[0][op] != gate_results[1][op]]
assert delta == [561] and not gate_results[0][561] and gate_results[1][561]
assert all(gate_results[1][op] for op in (108, 1206, 1303, 1386, 1455))

cases = []
for elapsed, total, claimed, account_time, account_claimed in (
        (0, 0, 0, 0, 0), (1799, 1799000, 0, 1799, 0),
        (1800, 1800000, 1, 1800, 1), (3600, 3600000, 3, 3600, 3),
        (7200, 7200000, 7, 7200, 7), (10800, 10800000, 15, 10800, 15),
        (0, 0xffffffff, 255, 0xffffffff, 0xffffffff)):
    for event_active in (False, True):
        state.clear()
        state.update(mode='receiver', object_lookups=0, timer_running=False,
                     timer_calls=[], timer_config=None, timer_value=None,
                     event_active=event_active, event_lookups=[], progress_display=[])
        protected.clear()
        u.mem_write(OBJ, b'\0' * 0x600)
        u.mem_write(RECORDS, b'\0' * 96)
        u.mem_write(AUX, b'\0' * 96)
        for off, ptr in ((0x464, RECORDS), (0x468, RECORDS + 96), (0x46c, RECORDS + 96),
                         (0x50c, AUX), (0x510, AUX + 96), (0x514, AUX + 96)):
            w(OBJ + off, ptr)
        protected[OBJ + 0x484] = 0
        for index, duration in enumerate((1800, 1800, 3600, 3600)):
            protected[RECORDS + 24 * index + 12] = duration * 1000
            protected[AUX + 24 * index + 12] = duration * 1000
        # Four u32 fields and the explicitly one-byte mask: 17 bytes total.
        body = struct.pack('<IIBII', elapsed, total, claimed, account_time, account_claimed)
        u.mem_write(BUFFER, body)
        w(0x57f9ad0, BUFFER)
        w(0x57f9ad4, len(body))
        run(0x1e80db0)
        assert r(0x57f9ad4) == 0 and r(0x57f9ad0) == BUFFER + 17
        assert u.mem_read(OBJ + 0x4dc, 1) == b'\1'
        assert r(OBJ + 0x4e0) == account_time and r(OBJ + 0x4e4) == account_claimed
        assert r(OBJ + 0x504) == 1 and state['object_lookups'] == 5
        # Native code computes readiness and masks; independent expectation.
        expected_ready = [elapsed >= value for value in (1800, 3600, 7200, 10800)]
        ready = [bool(u.mem_read(RECORDS + 24 * i + 0x15, 1)[0]) for i in range(4)]
        claimable = [bool(u.mem_read(RECORDS + 24 * i + 0x14, 1)[0]) for i in range(4)]
        assert ready == expected_ready
        assert claimable == [not bool(claimed & (1 << i)) for i in range(4)]
        aux_ready = [bool(u.mem_read(AUX + 24 * i + 0x15, 1)[0]) for i in range(4)]
        aux_claimable = [bool(u.mem_read(AUX + 24 * i + 0x14, 1)[0]) for i in range(4)]
        assert aux_ready == ([total >= v for v in (1800000, 3600000, 7200000, 10800000)] if event_active else [False] * 4), (elapsed, total, event_active, aux_ready)
        assert aux_claimable == ([not bool(account_claimed & (1 << i)) for i in range(4)] if event_active else [False] * 4)
        cases.append(dict(body_hex=body.hex(), elapsed=elapsed, event_228_active=event_active,
                          consumed=17, ready=ready, unclaimed=claimable,
                          secondary_ready=aux_ready, secondary_unclaimed=aux_claimable,
                          timer_config=state['timer_config'], timer_calls=state['timer_calls'],
                          progress_display=state['progress_display']))

receipt = dict(passed=True, source_sha256=hashlib.sha256(before).hexdigest(),
               candidate_sha256=hashlib.sha256(after).hexdigest(),
               native_gate_cases=65536, native_executions=131072,
               changed_admissions=delta, receiver_cases=cases,
               native_executed=['shared receive gate', '561 callback 0x1e80db0',
                                'native u32/byte packet readers', 'reward updater 0x1e7fd60',
                                'four-stage readiness and claimed masks', 'account event-228 visibility branch'],
               fixtures=['preloaded object/reward vectors', 'shared object getter with no reference block',
                         'protected scalar read/write storage', 'clock/control leaf methods',
                         'UI manager lookup/RTTI returns null auxiliary window', 'active event lookup',
                         'progress text/digit display sink 0x1e7eda0'],
               full_dispatch=False, ui_rendered=False, full_ui_acceptance=False,
               activity_list_crash_reproduced=False)
with a.receipt.open('x', encoding='utf-8') as stream:
    json.dump(receipt, stream, indent=2)
print(json.dumps(dict(passed=True, native_executions=131072, receiver_cases=len(cases))))
