"""Replay hash-pinned v143 PCRoom request-construction fragments offline.

The header initializer, stream write sink and flush are explicit fixtures.
The packet singleton getter, byte writer, two field arguments and byte counts
run as native code. Button visibility, mouse input and transport are excluded.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

SHA256 = '1ea94c7db46996cffa940ef50551fd912808f854656487c5664b388098cf7d88'
CASES = (
    (0x1e80c25, '00ff', True),
    (0x1eb4077, '01ff', True), (0x1eb40e7, '02ff', True),
    (0x1eb4157, '04ff', True), (0x1eb41c7, '08ff', True),
    (0x1eb4237, '1000', True), (0x1eb42a7, '1001', True),
    (0x1eb4317, '1002', True), (0x1eb4387, '1003', True),
    (0x1eb43f7, '1004', False), (0x3703b2c, '00ff', True),
)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--exe', type=Path, required=True)
    p.add_argument('--receipt', type=Path, required=True)
    a = p.parse_args()
    if a.receipt.exists() or a.exe.resolve() == a.receipt.resolve():
        p.error('new distinct receipt required')
    raw = a.exe.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == SHA256
    pe = pefile.PE(data=raw, fast_load=True)
    base = pe.OPTIONAL_HEADER.ImageBase
    assert base == 0x400000
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    u.mem_map(base, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) // 4096 * 4096)
    for section in pe.sections:
        u.mem_write(base + section.VirtualAddress, section.get_data())
    u.mem_map(0x10000000, 0x20000)
    u.mem_map(0x20000000, 0x400000)
    packet, sp = 0x20001000, 0x10010000

    def w(at, value):
        u.mem_write(at, struct.pack('<I', value))

    def r(at):
        return struct.unpack('<I', u.mem_read(at, 4))[0]

    def ret(clean):
        stack = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, 0)
        u.reg_write(UC_X86_REG_ESP, stack + 4 + clean)
        u.reg_write(UC_X86_REG_EIP, r(stack))

    state = {}

    def hook(vm, at, size, unused):
        stack = u.reg_read(UC_X86_REG_ESP)
        if at == 0x374f280:
            assert u.reg_read(UC_X86_REG_ECX) == packet
            state['opcode'] = r(stack + 4)
            u.mem_write(packet + 8, b'\1')
            ret(4)
        elif at == 0x38c7f70:
            assert r(stack + 8) == 1
            state['body'].extend(u.mem_read(r(stack + 4), 1))
            ret(8)
        elif at == 0x3750ac0:
            state['flushed'] = True
            assert stack == sp - 4
            u.emu_stop()

    u.hook_add(UC_HOOK_CODE, hook)
    w(0x57ed8d4, packet)
    rows = []
    for start, expected, admitted in CASES:
        state.clear()
        state.update(body=bytearray(), flushed=False)
        u.mem_write(packet, b'\0' * 16)
        w(packet + 0x2bcc2c, 0)
        u.reg_write(UC_X86_REG_ESP, sp)
        u.reg_write(UC_X86_REG_EBP, 0)
        u.emu_start(start, 0, count=1000)
        assert state['flushed'] and state['opcode'] == 632
        assert state['body'].hex() == expected and r(packet + 0x2bcc2c) == 2
        rows.append(dict(rva=hex(start - base), opcode=632, body_hex=expected,
                         native_byte_count=2, valid_for_four_period_rewards=admitted))
    receipt = dict(passed=True, module_sha256=SHA256, cases=rows,
                   native_executed=['request-construction fragments', 'packet singleton getter', 'byte writer and count'],
                   fixtures=['header initialization', 'stream write sink', 'flush sink'],
                   full_dispatch=False, button_visibility=False, ui_rendered=False,
                   networking=False, gameplay_accepted=False)
    with a.receipt.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(dict(passed=True, cases=len(rows), module_sha256=SHA256)))


if __name__ == '__main__':
    main()
