"""Bounded V179 menu, sender and NOTI65 reader; UI/strings/transport are fixtures."""
import hashlib
import json
import os
from pathlib import Path
import struct
import unittest

from test_guild_war_refresh_v173 import WarNative
from unicorn.x86_const import *

SHA = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'


class PartyChatNative(WarNative):
    ACTOR, CHAT, INPUT = 0x20017000, 0x20014000, 0x20011000

    def __init__(self, data):
        super().__init__(data)
        self.u.mem_map(self.NETWORK+0x100000, 0x200000)
        self.put(0x57ED8D4, self.NETWORK)
        self.put(self.NETWORK+8, 1)
        self.shown, self.sent, self.chunks = [], [], []
        self.heap = 0x20018000
        self.hooks.update({
            0x3752120: self.read_dstr,
            0x45FEA0: self.init_string, 0x45FDA0: self.assign_chars,
            0x1C5B350: self.init_string,
            0x3810650: lambda: self.ret(value=self.CHAT),
            0x2A00710: self.show,
            0x225F6C0: lambda: self.ret(4, 0),
            0x225A0F0: lambda: self.ret(value=0),
            0x2526480: lambda: self.ret(value=0),
            0x25260A0: lambda: self.ret(value=0),
            0x2524F60: lambda: self.ret(value=0),
            0x374F280: self.start, 0x38C7F70: self.sink,
            0x374F550: self.write_dstr, 0x3750AC0: self.finish,
        })

    def wide(self, at):
        result = bytearray()
        while True:
            pair = bytes(self.u.mem_read(at, 2)); at += 2
            if pair == b'\0\0': return bytes(result)
            result.extend(pair)

    def string(self, at, value):
        self.u.mem_write(at, bytes(32))
        if len(value) <= 14:
            self.u.mem_write(at+4, value+b'\0\0')
            self.put(at+20, len(value)//2, 7)
        else:
            self.u.mem_write(self.heap, value+b'\0\0')
            self.put(at+4, self.heap)
            self.put(at+20, len(value)//2, len(value)//2)
            self.heap += len(value)+16

    def init_string(self):
        at = self.reg(UC_X86_REG_ECX)
        self.string(at, self.wide(self.arg())); self.ret(4, at)

    def assign_chars(self):
        at = self.reg(UC_X86_REG_ECX)
        self.string(at, bytes(self.u.mem_read(self.arg(), self.arg(1)*2)))
        self.ret(8, at)

    def read_dstr(self):
        start, remaining = self.read(0x57F9AD0), self.read(0x57F9AD4)
        length = self.read(start)
        assert 4+length <= remaining
        value = bytes(self.u.mem_read(start+4, length)).decode('gb18030')
        assert len(value) < self.arg(1)
        self.u.mem_write(self.arg(), value.encode('utf-16le')+b'\0\0')
        self.put(0x57F9AD0, start+4+length, remaining-4-length)
        self.ret(value=length)

    def show(self):
        self.shown.append(dict(message=self.wide(self.arg()).decode('utf-16le'),
            type=self.arg(1), name=self.wide(self.arg(3)).decode('utf-16le')))
        self.ret(48)

    def receive(self, body):
        self.shown = []
        self.u.mem_write(self.PACKET, body)
        self.put(0x57F9AD0, self.PACKET, len(body), 0, 0)
        self.run(0x201B510)
        assert self.read(0x57F9AD4) == 0
        return self.shown

    def select(self, index):
        frame = self.STACK+0x2000
        self.put(frame-0x8A0, self.INPUT)
        self.put(self.INPUT+0x100, index)
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.emu_start(0x2A0C036, 0x2A0C21F, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x2A0C21F
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.reg(UC_X86_REG_ESI)

    def start(self):
        self.opcode = self.arg(); self.chunks = []
        self.put(self.NETWORK+0x2BCC2C, 0)
        self.put(self.NETWORK+0x2BCC34, self.PACKET)
        self.ret(4)

    def sink(self):
        self.chunks.append(bytes(self.u.mem_read(self.arg(), self.arg(1))))
        self.ret(8, 0)

    def write_dstr(self):
        data = self.wide(self.arg()).decode('utf-16le').encode('gb18030')
        raw = struct.pack('<I', len(data))+data
        self.chunks.append(raw)
        self.put(self.NETWORK+0x2BCC2C, self.read(self.NETWORK+0x2BCC2C)+len(raw))
        self.ret(4)

    def finish(self):
        body = b''.join(self.chunks)
        assert len(body) == self.read(self.NETWORK+0x2BCC2C)
        self.sent.append((self.opcode, body)); self.ret(value=0)

    def send(self, message_type, hint=65535):
        frame = self.STACK+0x2000
        self.put(self.CHAT+0x1E4, self.INPUT)
        self.put(self.INPUT+0x4C4, 0, 0)
        self.put(frame-0x340, self.CHAT)
        self.put(frame-0x348, message_type)
        self.put(frame-0x35C, self.ACTOR)
        self.put(frame-0x360, hint)
        self.put(frame-0x364, message_type-11)
        self.u.mem_write(frame-0x210, 'hello'.encode('utf-16le')+b'\0\0')
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EDI, self.CHAT)
        self.u.reg_write(UC_X86_REG_EBX, 0)
        self.u.emu_start(0x2A03782, 0x2A039EE, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x2A039EE
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.sent[-1]


class PartyChatV179Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_PARTY_CHAT_EXE')
        if not path: raise unittest.SkipTest('set DNF_PARTY_CHAT_EXE to exact V179')
        cls.data = Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest() == SHA

    def test_menu_and_real_sender(self):
        lab = PartyChatNative(self.data)
        for index, typ in ((1, 2), (2, 6)):
            self.assertEqual(lab.select(index), typ)
            op, body = lab.send(typ)
            self.assertEqual(op, 17)
            self.assertEqual(body, bytes([typ])+struct.pack('<HI', 65535, 0)+struct.pack('<I', 5)+b'hello')
            folder = os.environ.get('DNF_PARTY_CHAT_NATIVE_DIR')
            if folder:
                Path(folder).mkdir(parents=True, exist_ok=True)
                Path(folder, f'sender-{typ}.bin').write_bytes(body)

    def test_native_receiver_and_production_body(self):
        lab = PartyChatNative(self.data)
        def ds(b): return struct.pack('<I', len(b))+b
        for typ in (2, 6):
            body = bytes([typ, 0])+(bytes(2) if typ == 6 else b'')+ds('角色29'.encode('gb18030'))+b'\x0b'+ds(b'hello')+ds(b'')
            self.assertEqual(lab.receive(body), [dict(type=typ, name='角色29', message='hello')])
        folder = os.environ.get('DNF_PARTY_CHAT_NATIVE_DIR')
        if folder and Path(folder, 'party-65.bin').exists():
            got = lab.receive(Path(folder, 'party-65.bin').read_bytes())
            self.assertEqual(got, [dict(type=2, name='角色29', message='hello')])
            Path(folder, 'native-receipt.json').write_text(json.dumps(dict(passed=True, uiArguments=got)), encoding='utf-8')


if __name__ == '__main__': unittest.main()
