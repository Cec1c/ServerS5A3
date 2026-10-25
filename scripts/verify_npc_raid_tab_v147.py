"""Replay the real C2 patch and native tab selection with bounded fixtures."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EDI, UC_X86_REG_ESI, UC_X86_REG_ESP, UC_X86_REG_EIP, UC_X86_REG_EBP
sys.path.insert(0, str(Path(__file__).parent / 'client'))
import patch_npc_raid_tab_v147 as patch


def replay(data, value):
    p = pefile.PE(data=data, fast_load=True)
    v = Uc(UC_ARCH_X86, UC_MODE_32)
    v.mem_map(0x400000, (p.OPTIONAL_HEADER.SizeOfImage + 4095) // 4096 * 4096)
    for section in p.sections:
        v.mem_write(0x400000 + section.VirtualAddress, section.get_data())
    v.mem_map(0x10000000, 0x100000)
    v.mem_map(0x20000000, 0x100000)
    stack, obj, role, manager, tab = 0x100f0000, 0x20001000, 0x20002000, 0x20003000, 0x20004000
    def w(at, x):
        v.mem_write(at, struct.pack('<I', x & 0xffffffff))
    def r(at):
        return struct.unpack('<I', v.mem_read(at, 4))[0]
    def ret(result, clean):
        sp = v.reg_read(UC_X86_REG_ESP)
        v.reg_write(UC_X86_REG_EAX, result)
        v.reg_write(UC_X86_REG_EIP, r(sp))
        v.reg_write(UC_X86_REG_ESP, sp + 4 + clean)
    w(0x56a3798, manager)
    w(obj + 0x1dc, tab)
    state = dict(getter_calls=[], open_calls=[], selected=None)
    def hook(_v, at, size, _):
        sp = v.reg_read(UC_X86_REG_ESP)
        if at == patch.GETTER:
            assert v.reg_read(UC_X86_REG_ECX) == role
            assert r(sp + 4) == 0xa3
            state['getter_calls'].append(0xa3)
            ret(value, 4)
        elif at == patch.OPEN:
            assert v.reg_read(UC_X86_REG_ECX) == manager
            args = [r(sp + i) for i in (4, 8, 12)]
            assert args[:2] == [0x42f, 0]
            state['open_calls'].append(args)
            ret(0, 12)
        elif at == patch.RETURN:
            v.emu_stop()
        elif at == 0x384cf40:
            assert v.reg_read(UC_X86_REG_ECX) == tab
            state['selected'] = r(sp + 4)
            ret(0, 4)
        elif at == 0x384a580:
            ret(state['selected'], 0)
        elif at == 0x14157f0:
            assert v.reg_read(UC_X86_REG_ECX) == obj
            ret(0, 0)
        elif at in (0x141793c, 0x14177ed):
            state['native_post_selection'] = hex(at)
            v.emu_stop()
    v.hook_add(UC_HOOK_CODE, hook)
    v.reg_write(UC_X86_REG_ESI, role)
    v.reg_write(UC_X86_REG_ESP, stack)
    v.emu_start(patch.SITE, patch.RETURN + 1, count=100)
    assert v.reg_read(UC_X86_REG_ESP) == stack
    assert v.reg_read(UC_X86_REG_ESI) == role
    assert len(state['open_calls']) == 1
    arg = state['open_calls'][0][2]
    v.reg_write(UC_X86_REG_ESI, obj)
    v.reg_write(UC_X86_REG_EDI, arg)
    v.reg_write(UC_X86_REG_EBP, stack + 0x100)
    v.emu_start(0x14177bd, 0x141793d, count=100)
    assert state['selected'] == arg - 1 and r(obj + 0x1ec) == arg - 1
    assert v.reg_read(UC_X86_REG_ESP) == stack
    state.update(role_parameter=value, native_parameter=arg, stack_balanced=True)
    return state


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input', type=Path, required=True)
    ap.add_argument('--candidate', type=Path, required=True)
    ap.add_argument('--out', type=Path, required=True)
    args = ap.parse_args()
    source = args.input.read_bytes()
    expected, _ = patch.build(source)
    candidate = args.candidate.read_bytes()
    assert candidate == expected
    cases = []
    for value in (0, 1, 2, 3, 4, -1, 2147483647):
        result = replay(candidate, value)
        assert result['selected'] == (2 if value == 3 else 1)
        assert result['native_post_selection'] == ('0x14177ed' if value == 3 else '0x141793c')
        cases.append(result)
    # Baseline demonstrates that the opt-in previously still opened special.
    baseline = replay(source, 3)
    assert baseline['selected'] == 1 and not baseline['getter_calls']
    result = dict(passed=True, cases=cases, baseline=baseline,
                  candidate_sha256=hashlib.sha256(candidate).hexdigest(),
                  native_executed=['C2 arm and helper', '014177BD tab select/store', '014177E4 eligibility branch'],
                  fixtures=['role getter contract', 'window manager', 'tab widgets and refresh'],
                  player_acceptance=False)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=2), encoding='utf-8')
    print(json.dumps(dict(passed=True, cases=len(cases), baseline_special_tab=True)))


if __name__ == '__main__':
    main()
