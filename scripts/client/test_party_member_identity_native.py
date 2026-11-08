"""Unrun bounded native NOTI9 identity-reader regression.

Consumes synthetic Go-exported member bytes through the actual five field and
two DSTR reads, then the actual new-wrapper identity assignments. Packet I/O,
memset and the protected level store are explicit adapters. Does not emulate
the full dispatcher, window system, factory, networking or rendering.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

SUPPORTED = {
    '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112',
    '735ed3e96f178bd2b546be18ff38ba4a578c951092b1d97a352ab73db7333d8b',
    '3fef7a071333b25e9086fcc79174b58998aa092978d414258a7c5709d61b5e29',
    '8d38d4afef31292a5544a93fcd53a418fcb1fd1a4cd9e21ef6d3a4333e81c227',
}


def read_native(data, identity):
    pe = pefile.PE(data=data, fast_load=True)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    u.mem_map(0x400000, pe.OPTIONAL_HEADER.SizeOfImage)
    for section in pe.sections:
        if section.SizeOfRawData:
            u.mem_write(0x400000 + section.VirtualAddress, section.get_data())
    frame, profile = 0x20008000, 0x21000000
    u.mem_map(0x20000000, 0x10000)
    u.mem_map(profile, 0x1000)
    u.reg_write(UC_X86_REG_EBP, frame)
    u.reg_write(UC_X86_REG_ESP, frame - 0x500)
    position, level, stop = 0, None, None

    def word(at):
        return struct.unpack('<I', u.mem_read(at, 4))[0]

    def arg(n=0):
        return word(u.reg_read(UC_X86_REG_ESP) + 4 + 4 * n)

    def ret(pop=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EIP, word(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    def hook(_u, at, _size, _context):
        nonlocal position, level, stop
        if at in (0x2016C8D, 0x2017065):
            stop = at
            u.emu_stop()
        elif at in (0x37E82E0, 0x37E83C0, 0x37E8430):
            count = 4 if at == 0x37E83C0 else arg(1)
            assert 0 <= count <= len(identity) - position, 'native packet overread'
            if count:
                u.mem_write(arg(), identity[position:position + count])
            position += count
            ret()
        elif at == 0x3E734F0:
            assert arg(2) <= 256
            u.mem_write(arg(), bytes([arg(1) & 255]) * arg(2))
            ret()
        elif at in (0x37DF130, 0x37DF1B0, 0x37DF230, 0x37DF2B0, 0x37DF010):
            assert u.reg_read(UC_X86_REG_ECX) == profile + 0x14
            assert arg(1) == profile + 0x18
            level = word(arg())
            ret(8)

    u.hook_add(UC_HOOK_CODE, hook)
    u.emu_start(0x2016BC1, 0xFFFFFFFF, count=2000)
    assert stop == 0x2016C8D and position == len(identity)
    # Object allocation and name formatting do not determine these identity
    # fields. Continue at the native assignments with the allocated profile.
    stop = None
    u.reg_write(UC_X86_REG_ESI, profile)
    u.emu_start(0x2016FC3, 0xFFFFFFFF, count=1000)
    assert stop == 0x2017065 and level is not None
    return dict(level=level, job=word(profile + 4), grow=word(profile + 8),
                awakening=word(profile + 12), consumed=position)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--exe', type=Path, required=True)
    p.add_argument('--fixtures', type=Path, required=True)
    p.add_argument('--report', type=Path, required=True)
    args = p.parse_args()
    if args.report.exists():
        raise ValueError('report must be a new file')
    data = args.exe.read_bytes()
    digest = hashlib.sha256(data).hexdigest()
    if digest not in SUPPORTED:
        raise ValueError('exact supported cumulative EXE required')
    cases = json.loads(args.fixtures.read_text('utf8'))
    assert cases, 'Go-exported fixtures required'
    results = []
    for case in cases:
        identity = bytes.fromhex(case['identityHex'])
        new = read_native(data, identity)
        assert (new['level'], new['job'], new['grow'], new['awakening']) == (
            case['level'], case['job'], case['grow'] & 15, (case['grow'] >> 4) & 7)
        old = read_native(data, identity[1:2] + identity[:1] + identity[2:])
        assert old['job'] == case['level'] and old['level'] == case['job']
        results.append(dict(kind=case['kind'], old=old, new=new))
    with args.report.open('x', encoding='utf8') as stream:
        json.dump(dict(exeSha256=digest, cases=results, passed=len(results),
                       fullHandlerExecuted=False, gameAcceptance='pending'), stream, indent=2)
    print(json.dumps(dict(passed=len(results), gameAcceptance='pending')))


if __name__ == '__main__':
    main()
