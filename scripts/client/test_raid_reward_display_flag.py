"""Execute official24.9 reward item decode and UI flag branches offline.

The native packet readers and the exact per-item NOTI600 instructions execute.
The item factory is an explicit descriptor fixture; the UI append is observed
at its native call site. Rendering, localization and game startup do not run.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EIP

SHA = '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24'
FRAME, STACK, PACKET, ITEM = 0x20002000, 0x20001000, 0x20003000, 0x20004000


def verify(source):
    data = Path(source).read_bytes()
    if hashlib.sha256(data).hexdigest() != SHA:
        raise ValueError('exact verified client SHA256 required')
    pe = pefile.PE(data=data, fast_load=True)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    u.mem_map(pe.OPTIONAL_HEADER.ImageBase, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) & ~4095)
    for s in pe.sections:
        u.mem_write(pe.OPTIONAL_HEADER.ImageBase + s.VirtualAddress, s.get_data())
    u.mem_map(0x20000000, 0x10000)
    def put(a, v):
        u.mem_write(a, struct.pack('<I', v))
    def read(a):
        return struct.unpack('<I', u.mem_read(a, 4))[0]
    state = {}
    def hook(_, address, size, __):
        if address == 0x192C730:
            sp = u.reg_read(UC_X86_REG_ESP)
            state['factory'] = [read(sp + n) for n in (4, 8, 12)]
            u.reg_write(UC_X86_REG_EAX, ITEM)
            u.reg_write(UC_X86_REG_EIP, read(sp))
            u.reg_write(UC_X86_REG_ESP, sp + 4)
        elif address == 0x1F1E2EB:
            state['appended'] = True
            u.emu_stop()
    u.hook_add(UC_HOOK_CODE, hook)
    rows = []
    for kind in (0, 1, 2):
        for quantity in (1, 57, 2000, 65535):
            for flag in (0, 1, 2, 255):
                state.clear()
                body = struct.pack('<BIHB', kind, 600, quantity, flag)
                u.mem_write(FRAME - 0x100, b'\0' * 0x200)
                u.mem_write(ITEM, b'\0' * 0x80)
                u.mem_write(PACKET, body)
                put(0x57F9AD0, PACKET)
                put(0x57F9AD4, len(body))
                u.reg_write(UC_X86_REG_EBP, FRAME)
                u.reg_write(UC_X86_REG_ESP, STACK)
                u.reg_write(UC_X86_REG_EBX, 0)
                u.emu_start(0x202FB20, 0x202FB6B, count=5000)
                assert u.reg_read(UC_X86_REG_EIP) == 0x202FB6B
                assert state['factory'] == [600, quantity, kind]
                assert read(0x57F9AD0) == PACKET + 8
                assert read(0x57F9AD4) == 0
                hidden = bytes(u.mem_read(ITEM + 0x41, 1))[0]
                assert hidden == int(flag > 0)
                put(FRAME - 0x24, ITEM)
                u.reg_write(UC_X86_REG_EAX, ITEM)
                u.reg_write(UC_X86_REG_ESP, STACK)
                u.emu_start(0x1F1E2D8, 0x1F1E2F0, count=100)
                suppressed = bytes(u.mem_read(FRAME + 0x17, 1))[0]
                assert bool(state.get('appended')) == (flag == 0)
                assert suppressed == int(flag > 0)
                if suppressed:
                    u.emu_start(0x1F1E3D2, 0x1F1E3DC, count=10)
                    assert u.reg_read(UC_X86_REG_EIP) == 0x1F1E3DC
                rows.append(dict(kind=kind, quantity=quantity, wireFlag=flag, hidden=hidden,
                                 appended=bool(state.get('appended'))))
    return dict(inputSha256=SHA, cases=len(rows), passed=True, results=rows,
                limits=['per-item parser and filtering windows only',
                        'item factory uses a descriptor fixture',
                        'renderer and localization are not executed',
                        'no running game or player memory accessed'])


if __name__ == '__main__':
    ap = argparse.ArgumentParser()
    ap.add_argument('--input', required=True)
    ap.add_argument('--report', required=True)
    args = ap.parse_args()
    result = verify(args.input)
    Path(args.report).write_text(json.dumps(result, indent=2), encoding='utf8')
    print(json.dumps({k: result[k] for k in ('inputSha256', 'cases', 'passed')}))
