"""Run the exact shipped peer senders and 13-byte header/u16 writers offline.

The byte sink, allocator locks, network singleton getter and final network send
are explicit adapters. No socket or running game process is used.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

INPUT_SHA256 = '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112'


def verify(data):
    if hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact deployed cumulative image required')
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from test_guild_war_refresh_v173 import WarNative
    from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_ESI, UC_X86_REG_EIP

    class Sender(WarNative):
        WINDOW, BUTTON = 0x20010000, 0x20011000

        def __init__(self):
            super().__init__(data)
            self.u.mem_map(self.NETWORK + 0x100000, 0x200000)
            self.put(self.NETWORK + 8, 1)
            self.put(self.WINDOW + 0x38C, 17)
            self.put(self.WINDOW + 0x3CC, self.BUTTON)
            self.u.mem_write(self.BUTTON + 0x61, b'\1')
            self.put(self.BUTTON + 0x10C, 123)
            self.put(0x55ADAD4, 123)
            self.u.mem_write(self.BUTTON + 0x578, b'\1')
            self.put(0x4588200, self.STOP + 0x100)
            self.put(0x45881FC, self.STOP + 0x110)
            self.parts, self.frames = [], []
            self.hooks.update({
                self.STOP + 0x100: lambda: self.ret(4),
                self.STOP + 0x110: lambda: self.ret(4),
                0x38C7F70: self.sink,
                0x3750AC0: self.finish,
                0x9982B0: lambda: self.ret(),
                0x32B94A0: lambda: self.ret(12),
            })

        def sink(self):
            self.parts.append(bytes(self.u.mem_read(self.arg(), self.arg(1))))
            self.ret(8, 1)

        def finish(self):
            # Native 0374F355 writes 13 header bytes; 0374F3DF writes the u16.
            # Only final length/sequence/checksum and socket delivery are adapted.
            raw = bytearray(b''.join(self.parts))
            assert [len(x) for x in self.parts] == [13, 2]
            assert self.read(self.NETWORK + 0x2BCC2C) == 2
            struct.pack_into('<I', raw, 3, len(raw))
            struct.pack_into('<IH', raw, 7, 0, 1)
            self.frames.append(bytes(raw))
            self.ret()

        def buff(self, clicked=True, opened=False):
            self.u.mem_write(self.BUTTON + 0x61, bytes([clicked]))
            self.u.mem_write(self.BUTTON + 0x578, bytes([not opened]))
            self.put(self.STACK, self.STOP)
            self.u.reg_write(UC_X86_REG_ESP, self.STACK)
            self.u.reg_write(UC_X86_REG_ESI, self.WINDOW)
            self.u.emu_start(0x3376BC4, 0x3376C0B, count=10000)
            assert self.reg(UC_X86_REG_EIP) == 0x3376C0B
            assert self.reg(UC_X86_REG_ESP) == self.STACK
            return self.frames

        def title(self):
            self.put(self.STACK, self.STOP)
            self.u.reg_write(UC_X86_REG_ESP, self.STACK)
            self.u.reg_write(UC_X86_REG_ESI, self.WINDOW)
            self.u.emu_start(0x3377A9E, 0x3377AC8, count=10000)
            assert self.reg(UC_X86_REG_EIP) == 0x3377AC8
            assert self.reg(UC_X86_REG_ESP) == self.STACK
            return self.frames

    frames = {}
    for name, opcode in [('buff', 1497), ('title', 423)]:
        lab = Sender()
        result = getattr(lab, name)()
        assert len(result) == 1
        raw = result[0]
        assert raw[0] == 1 and struct.unpack_from('<H', raw, 1)[0] == opcode
        assert len(raw) == 15 and raw[13:] == b'\x11\0'
        frames[name] = dict(opcode=opcode, cmd=1, headerBytes=13,
                            bodyHex=raw[13:].hex(), frameHex=raw.hex())
    assert Sender().buff(clicked=False) == []
    assert Sender().buff(opened=True) == []
    return dict(passed=True, inputSha256=INPUT_SHA256, frames=frames,
                buffClickAndCheckedStateExecuted=True,
                titleSameNamespaceSendSliceExecuted=True,
                nativeHeaderAndTargetWritersExecuted=True,
                finalTransportAndExternalAPIsStubbed=True,
                completeTitleClickEventExecuted=False, gameplayAcceptance=False)


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--exe', type=Path, required=True)
    p.add_argument('--python-deps', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    sys.path.insert(0, str(a.python_deps))
    result = verify(a.exe.read_bytes())
    with a.output.open('x', encoding='utf8') as out:
        json.dump(result, out, indent=2)
    print(json.dumps(result))
