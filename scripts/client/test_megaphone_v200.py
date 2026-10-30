"""V200 CMD17 producer and NOTI118 consumer; no game process is started.

Transport, strings, inventory getters and final UI effects are adapters.
Native type/slot selection, wire writes/reads, route choice and local
inventory prediction execute the original machine code.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest

from test_guild_social_channel_v179 import SocialSender
from unicorn.x86_const import *

SHA256 = '478611607f7b6567fcaff7af1ca53c4700eec93b9285ac75d47bd5465d535586'


class MegaphoneNative(SocialSender):
    ITEM, VTABLE, DEFINITION = 0x20018000, 0x20019000, 0x2001A000

    def __init__(self, data):
        super().__init__(data)
        self.channel, self.group, self.slot, self.count = 5, 1, 68, 3
        self.lookups, self.lines = [], []
        self.u.mem_write(self.NAME, '发送者'.encode('utf-16le') + b'\0\0')
        self.put(self.NAME + 0x100, self.NAME)
        self.put(self.ITEM, self.VTABLE)
        self.put(self.VTABLE + 0x28, self.STOP + 0x100)
        self.put(self.VTABLE + 0x48, self.STOP + 0x110)
        self.put(self.VTABLE + 0x50, self.STOP + 0x120)
        self.put(self.MENU, self.VTABLE)
        self.put(self.VTABLE + 0xA8, self.STOP + 0x130)
        self.hooks.update({
            0x2961D40: lambda: self.ret(value=1),
            0x25ECA10: self.item_slot,
            0x29A0370: lambda: self.ret(value=0),
            0x25255F0: lambda: self.ret(4, 0),
            0x2525720: lambda: self.ret(value=0),
            0x1B4A020: lambda: self.ret(value=self.MENU),
            0x1B37730: lambda: self.ret(value=self.MENU),
            self.STOP + 0x130: lambda: self.ret(value=self.group),
            0x8EF130: self.format_message,
            0x3810650: lambda: self.ret(value=self.CHAT),
            0x2A00710: lambda: self.line('same_channel', 48),
            0x2A00580: lambda: self.line('other_channel', 32),
            0x3C1BEA0: lambda: self.ret(value=self.NAME + 0x100),
            0x25EB680: lambda: self.ret(4, self.ITEM if self.count else 0),
            self.STOP + 0x100: lambda: self.ret(value=self.DEFINITION),
            self.STOP + 0x110: lambda: self.ret(value=self.count),
            self.STOP + 0x120: self.decrement,
            0x994E30: lambda: self.ret(value=1),
            0x25EE980: self.remove,
        })

    def directory_group(self):
        self.put(self.arg(1), self.channel)
        self.ret(8)

    def item_slot(self):
        self.lookups.append(self.arg())
        self.ret(4, self.slot)

    def format_message(self):
        self.u.mem_write(self.arg(), self.wide(self.arg(3)) + b'\0\0')
        self.ret(value=0)

    def decrement(self):
        self.count -= self.arg()
        self.ret(4)

    def remove(self):
        self.count = 0
        self.ret(4)

    def line(self, route, pop):
        same = route == 'same_channel'
        self.lines.append((route, self.arg(1 if same else 2),
                           self.wide(self.arg(3 if same else 3)).decode('utf-16le'),
                           self.wide(self.arg()).decode('utf-16le')))
        self.ret(pop)

    def send(self, mode):
        frame = self.STACK + 0x2000
        self.put(self.CHAT + 0x1E4, self.MENU)
        self.put(frame - 0x348, mode)
        self.put(frame - 0x364, mode - 11)
        self.put(frame - 0x340, self.CHAT)
        self.u.mem_write(frame - 0x210, 'hello'.encode('utf-16le') + b'\0\0')
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_EDI, self.CHAT)
        self.u.reg_write(UC_X86_REG_EBX, 0)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.emu_start(0x2A03782, 0x2A039EE, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x2A039EE
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.sent[-1]

    def receive_horn(self, body):
        self.u.mem_write(self.PACKET, body)
        self.put(0x57F9AD0, self.PACKET, len(body), 0, 0)
        self.run(0x200C9A0)
        assert self.read(0x57F9AD4) == 0


def dstr(value):
    raw = value.encode('gb18030')
    return struct.pack('<I', len(raw)) + raw


class MegaphoneV200Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_MEGAPHONE_CLIENT')
        if not path:
            raise unittest.SkipTest('set DNF_MEGAPHONE_CLIENT to reviewed V200')
        cls.data = Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest() == SHA256

    def test_sender_type_slot_and_exact_body(self):
        for mode, item in ((11, 36), (13, 37)):
            lab = MegaphoneNative(self.data)
            opcode, body = lab.send(mode)
            self.assertEqual((opcode, body), (17, struct.pack('<BHI', mode, 0, lab.slot) + dstr('hello')))
            self.assertEqual(lab.lookups, [item])
            directory = os.environ.get('DNF_MEGAPHONE_NATIVE_DIR')
            if directory:
                Path(directory).mkdir(parents=True, exist_ok=True)
                Path(directory, f'sender-{mode}.bin').write_bytes(body)

    def test_receiver_named_routes_and_self_prediction(self):
        for mode in (11, 13):
            for channel in (5, 7):
                for sender in ('发送者', '其他人'):
                    lab = MegaphoneNative(self.data)
                    body = struct.pack('<BBBBH', mode, 1, 0, channel, 1001) + dstr(sender) + dstr('hello') + dstr('')
                    lab.receive_horn(body)
                    self.assertEqual(lab.lines, [('same_channel' if channel == 5 else 'other_channel', mode, sender, 'hello')])
                    self.assertEqual(lab.count, 2 if sender == '发送者' else 3)

    def test_go_notice_consumed_exactly(self):
        directory = os.environ.get('DNF_MEGAPHONE_NATIVE_DIR')
        if not directory:
            self.skipTest('set DNF_MEGAPHONE_NATIVE_DIR to Go output directory')
        for mode in (11, 13):
            body = Path(directory, f'notice-{mode}.bin').read_bytes()
            for channel in (body[3], body[3]+1):
                lab = MegaphoneNative(self.data)
                lab.channel = channel
                lab.receive_horn(body)
                self.assertEqual(len(lab.lines), 1)
                self.assertEqual(lab.lines[0][1:], (mode, '角色29', 'hello'))
                self.assertEqual(lab.count, 3)


if __name__ == '__main__':
    unittest.main()
