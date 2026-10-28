"""V179 member-menu main whisper selection, writer and NOTI65 receive path.

Actor lookup, selected region, strings and UI are adapters. Menu decisions,
type/UID selection, scalar writers and the complete receiver execute native.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest

from test_guild_social_channel_v179 import SocialSender
from unicorn.x86_const import *


class MainWhisperNative(SocialSender):
    INPUT, CONFIG = 0x20015000, 0x20018800

    def __init__(self, data):
        super().__init__(data)
        self.put(self.CHAT+0x1E4, self.INPUT)
        self.put(0x5678A90, self.CONFIG)
        self.put(self.CONFIG, 0x48995C4)
        self.group, self.shown = 0, []
        self.hooks.update({
            0x1B4A020: lambda: self.ret(value=0x5678E38),
            0x1C5B350: self.wstring_init, 0x45FDA0: self.assign_chars,
            0x3810650: lambda: self.ret(value=self.CHAT),
            0x2A00710: self.show,
            0x225F6C0: lambda: self.ret(4, 0),
            0x225A0F0: lambda: self.ret(value=0),
        })

    def directory_group(self):
        # Selected-region object's protected group. Native 1B37730 and its
        # real vtable+A8 getter remain active, including a valid group zero.
        self.put(self.arg(1), 1 if self.reg(UC_X86_REG_ECX) == 0x57B7218 else self.group)
        self.ret(8)

    def assign_chars(self):
        at = self.reg(UC_X86_REG_ECX)
        self.string(at, bytes(self.u.mem_read(self.arg(), self.arg(1)*2)))
        self.ret(8, at)

    def show(self):
        self.shown.append(dict(message=self.wide(self.arg()).decode('utf-16le'),
            type=self.arg(1), name=self.wide(self.arg(3)).decode('utf-16le')))
        self.ret(48)

    def receive(self, body):
        self.u.mem_write(self.PACKET, body)
        self.put(0x57F9AD0, self.PACKET, len(body), 0, 0)
        self.run(0x201B510)
        assert self.read(0x57F9AD4) == 0
        return self.shown

    def receive_main_ui(self, body, own_name):
        # Preserve native 2A00710 -> 29FFFA0, including its real self-name
        # filter. Stop at the main chat enqueue; rendering is a UI adapter.
        self.hooks.pop(0x2A00710)
        self.u.mem_write(0x20019000, own_name.encode('utf-16le')+b'\0\0')
        self.put(self.ACTOR, 0x20019400)
        self.put(0x20019400+0x3AC, 0x2961400)
        self.put(0x56A1C7C, 0x20019800)
        self.put(0x20019800, 0x20019900)
        self.put(0x20019900+0x18, 0x2961410)
        def enqueue():
            record = self.arg()
            self.shown.append(dict(message=self.wide(self.read(record+4)).decode('utf-16le'),
                type=self.read(record+0x18), name=self.wide(self.read(record+8)).decode('utf-16le')))
            self.ret(4)
        self.hooks.update({
            0x296DF30: lambda: self.ret(4, 0),
            0x29C18A0: lambda: self.ret(8, 0),
            0x29610E0: lambda: self.ret(value=self.ACTOR),
            0x2961400: lambda: self.ret(value=0x20019000),
            0x2961410: lambda: self.ret(4),
            0x2526480: lambda: self.ret(value=0),
            0x29F8440: lambda: self.ret(4, 0),
            0x29EFC20: lambda: self.ret(value=0),
            0x2526B50: lambda: self.ret(value=0),
            0x29F71E0: lambda: self.ret(4),
            0x29FFDC0: enqueue,
            0x2531940: lambda: self.ret(),
            0x29F0180: lambda: self.ret(4),
        })
        return self.receive(body)

    def open_menu_input(self, group):
        opened = []
        self.put(self.ACTOR+0x3AC, group)
        self.put(0x20018A00, self.NAME)
        self.hooks.update({
            0x2A09620: lambda: (opened.append(['mode', self.arg()]), self.ret(4)),
            0x2A09640: lambda: (opened.append(['open', self.arg()]), self.ret(4)),
            0x3C1BEA0: lambda: self.ret(value=0x20018A00),
        })
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_ESI, self.ACTOR)
        # Stop inside the target setter after name/group storage; subsequent
        # font/layout work is outside this bounded menu stage.
        self.u.emu_start(0x353B66A, 0x2A08E1C, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x2A08E1C
        return opened

    def select(self, group, found):
        frame = self.STACK+0x2000
        self.string(self.INPUT+0x1F8, '来来来'.encode('utf-16le'))
        self.put(self.INPUT+0x218, group)
        self.put(self.ACTOR+0x20, 5)
        self.put(frame-0x8A0, self.INPUT)
        self.hooks.update({0x25256A0: lambda: self.ret(value=0),
            0x296D810: lambda: self.ret(8, self.ACTOR if found else 0)})
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_EDI, self.INPUT+0x1F8)
        self.u.emu_start(0x2A0BF86, 0x2A0C21F, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x2A0C21F
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.reg(UC_X86_REG_ESI), self.read(frame-0x894) & 0xFFFF

    def send(self, group, uid, through_display=False, send_result=0, source_group=None):
        self.group = group if source_group is None else source_group
        frame = self.STACK+0x2000
        self.put(self.ACTOR+0x3AC, self.group)
        self.put(self.INPUT+0x218, group)
        for offset, value in ((0x35C, self.ACTOR), (0x348, 1), (0x364, -10),
                              (0x360, uid), (0x34C, self.NAME), (0x340, self.CHAT)):
            self.put(frame-offset, value)
        self.u.mem_write(frame-0x210, 'hello\0'.encode('utf-16le'))
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EDI, self.CHAT)
        self.u.reg_write(UC_X86_REG_EBX, 0)
        end = 0x2A03C5E if through_display else 0x2A039EE
        if through_display:
            self.u.mem_write(frame-0x341, b'\x01')  # message-history adapter
            def finish():
                self.finish()
                self.u.reg_write(UC_X86_REG_EAX, send_result)
            def enqueue():
                record = self.arg()
                self.shown.append(dict(type=self.read(record+0x18), uid=self.read(record+0xC)&65535,
                    name=self.wide(self.read(record+8)).decode('utf-16le'),
                    message=self.wide(self.read(record+4)).decode('utf-16le')))
                self.ret(4)
            self.hooks.update({0x3750AC0: finish, 0x29FFDC0: enqueue,
                0x2526480: lambda: self.ret(value=0), 0x25260A0: lambda: self.ret(value=0)})
        self.u.emu_start(0x2A03782, end, count=10000)
        assert self.reg(UC_X86_REG_EIP) == end
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.sent


class MainWhisperV179Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_SOCIAL_V179_CLIENT')
        if not path: raise unittest.SkipTest('exact V179 client required')
        cls.data = Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest() == '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'

    def test_menu_input_selection_and_native_writer(self):
        for group in (0, 7, 249):
            lab = MainWhisperNative(self.data)
            self.assertEqual(lab.open_menu_input(group), [['mode', 0], ['open', 1]])
            self.assertEqual(lab.read(lab.INPUT+0x218), group)
            self.assertEqual(lab.wide(lab.INPUT+0x1FC).decode('utf-16le'), '来来来')
            for found in (True, False):
                lab = MainWhisperNative(self.data)
                message_type, uid = lab.select(group, found)
                self.assertEqual((message_type, uid), (1, 5 if found else 65535))
                lab = MainWhisperNative(self.data)
                sent = lab.send(group, uid)
                name = '来来来'.encode('gb18030')
                expected = struct.pack('<BHII', 1, uid, 0, 5)+b'hello'+struct.pack('<I', len(name))+name+bytes((group,))
                self.assertEqual(sent, [(17, expected)])
                folder = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
                if folder: Path(folder, f'main-whisper-group{group}-uid{uid}.bin').write_bytes(expected)

    def test_go_main_input_message_reaches_correct_ui_with_exact_consumption(self):
        folder = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
        if not folder: self.skipTest('Go packet export directory required')
        for group in (0, 7, 249):
            for uid in (5, 65535):
                body = Path(folder, f'main-whisper-group{group}-uid{uid}-noti65.bin').read_bytes()
                lab = MainWhisperNative(self.data)
                self.assertEqual(lab.receive_main_ui(body, '来来来'), [dict(message='hello', type=1, name='pouut')])

    def test_native_main_ui_filters_own_name(self):
        def ds(text):
            raw = text.encode('gb18030')
            return struct.pack('<I', len(raw))+raw
        for own, peer in (('pouut', '来来来'), ('来来来', 'pouut')):
            for name in (own, peer):
                body = b'\x01\x00'+ds(name)+b'\0'+ds('hello')+ds('')
                got = MainWhisperNative(self.data).receive_main_ui(body, own)
                self.assertEqual(got, [] if name == own else [dict(message='hello', type=1, name=peer)])

    def test_native_outgoing_wording_requires_local_uid(self):
        # 44369 is the outgoing suffix (Korean "to ..."; current Chinese
        # translation "player "). NOTI65 always supplies UID zero, so merely
        # echoing the peer name cannot represent an outgoing message.
        for typ in (1, 7):
            for uid in (0, 1):
                lab = MainWhisperNative(self.data)
                frame, record = lab.STACK+0x2000, 0x20018B00
                lab.put(record+0xC, uid)
                lab.put(record+0x18, typ)
                lab.hooks[0x37DF630] = lambda: (lab.put(lab.arg(1), 1), lab.ret(8))
                lab.hooks[0x3833670] = lambda: lab.ret(12)
                lab.u.reg_write(UC_X86_REG_ESP, lab.STACK-4)
                lab.u.reg_write(UC_X86_REG_EBP, frame)
                lab.u.reg_write(UC_X86_REG_ESI, record)
                lab.u.reg_write(UC_X86_REG_EBX, 0)
                lab.u.emu_start(0x21E72AD, 0x21E738B, count=1000)
                self.assertEqual(lab.reg(UC_X86_REG_EIP), 0x21E738B)
                self.assertEqual(lab.reg(UC_X86_REG_ESP), lab.STACK)
                self.assertEqual(lab.messages, [44369] if uid == 1 else [])


if __name__ == '__main__': unittest.main()
