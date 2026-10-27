"""Bounded V179 dispatcher and 804 receiver, with transport/UI/string adapters."""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_social_channel_v179 as patch
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import native, windows_image
from unicorn.x86_const import *


class SocialNative(WarNative):
    MENU = 0x20010000

    def __init__(self, data):
        super().__init__(data)
        self.put(0x2000C000, 0x3539F50, 0)
        self.put(self.MENU + 0xA4, -2)
        self.received = []
        self.hooks.update({
            0x261E440: lambda: self.ret(value=self.MENU),
            0x3752120: self.read_dstr,
            0x45FEA0: self.wstring_init,
            0x45F9B0: self.wstring_copy,
        })

    def hook(self, u, at, size, ctx):
        if at == 0x3539F50:
            self.received.append([self.arg(), self.arg(1) & 255, self.arg(2) & 255])
        super().hook(u, at, size, ctx)

    def read_dstr(self):
        start, remaining = self.read(0x57F9AD0), self.read(0x57F9AD4)
        length = self.read(start)
        assert 4 + length <= remaining
        value = bytes(self.u.mem_read(start + 4, length)).decode('gb18030')
        assert len(value) < self.arg(1)
        self.u.mem_write(self.arg(), value.encode('utf-16le') + b'\0\0')
        self.put(0x57F9AD0, start + 4 + length, remaining - 4 - length)
        self.ret(value=length)

    def wide(self, at):
        result = bytearray()
        while True:
            pair = bytes(self.u.mem_read(at, 2)); at += 2
            if pair == b'\0\0': return bytes(result)
            result.extend(pair)

    def string(self, at, value):
        assert len(value) <= 14
        self.u.mem_write(at, bytes(32))
        self.u.mem_write(at+4, value+b'\0\0')
        self.put(at+20, len(value)//2, 7)

    def wstring_init(self):
        at = self.reg(UC_X86_REG_ECX)
        self.string(at, self.wide(self.arg()))
        self.ret(4, at)

    def wstring_copy(self):
        at, source = self.reg(UC_X86_REG_ECX), self.arg()
        assert self.arg(1) == 0 and self.arg(2) == 0xFFFFFFFF
        self.string(at, self.wide(source+4))
        self.ret(12, at)

    def lookup_reply(self, body):
        wire = bytearray(16) + body; wire[0] = 1
        struct.pack_into('<H', wire, 1, 804)
        struct.pack_into('<I', wire, 3, len(wire))
        self.u.mem_write(self.PACKET, bytes(wire))
        self.run(0x25239A0, (0, self.PACKET, 0), ecx=self.NETWORK)
        return self.read(self.MENU+0xA4)

    def follow_decision(self):
        result = []
        def ready():
            result.append('directory_lookup')
            self.u.reg_write(UC_X86_REG_EIP, self.STOP)
        def message():
            result.append(self.arg())
            self.u.reg_write(UC_X86_REG_EIP, self.STOP)
        self.hooks[0x3538FE0] = ready
        self.hooks[0x39A6620] = message
        self.u.reg_write(UC_X86_REG_ESI, self.MENU)
        self.u.reg_write(UC_X86_REG_EBP, self.STACK+0x2000)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EBX, 0xFFFFFFFF)
        self.u.emu_start(0x355131F, self.STOP, count=1000)
        assert len(result) == 1
        return result[0]


class SocialChannelV179Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name = os.environ.get('DNF_GUILD_SOCIAL_V179_CLIENT')
        if not name: raise unittest.SkipTest('requires exact V179 client')
        cls.source = Path(name)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_01_all_receive_ids_preserve_class_boundary(self):
        ids = range(65536)
        for classification in (0, 1, 2, 255):
            before = native(self.before, (classification,), ids)
            after = native(self.after, (classification,), ids)
            changes = [i for i in ids if before[i] != after[i]]
            self.assertEqual(changes, [804] if classification == 1 else [])
            if classification == 1:
                self.assertEqual((before[804], after[804]), (0, 1))

    def test_02_real_dispatcher_and_lookup_receiver_red_green(self):
        name = '来来来'.encode('gb18030')
        success = b'\x01\x00' + struct.pack('<I', len(name)) + name + b'\x01\x0b'
        for body, status in ((success, 0xFFFFFFFF), (b'\x00\x00', 0), (b'\x00\x64', 100)):
            before, after = SocialNative(self.before), SocialNative(self.after)
            self.assertEqual(before.lookup_reply(body), 0xFFFFFFFE)
            self.assertEqual(before.received, [])
            self.assertEqual(before.follow_decision(), 0x8225)  # Please try later.
            self.assertEqual(after.lookup_reply(body), status)
            self.assertEqual(after.received, [[804, body[0], body[1] if body[0] == 0 else 0]])
            self.assertEqual(after.read(0x57F9AD4), 0)
            self.assertEqual(after.follow_decision(), 'directory_lookup' if body[0]
                             else 0x8226 if body[1] == 100 else 0x821F)
            if body[0]:
                self.assertEqual(after.read(after.MENU+0x78), 0)
                self.assertEqual(after.wide(after.MENU+0x80).decode('utf-16le'), '来来来')
                self.assertEqual(after.read(after.MENU+0x9C), 1)
                self.assertEqual(after.read(after.MENU+0xA0), 11)

    def test_03_go_production_replies(self):
        folder = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
        if not folder: self.skipTest('set Go packet export directory')
        files = sorted(Path(folder).glob('*-804.bin'))
        self.assertEqual(len(files), 4)
        receipts = []
        for file in files:
            body = file.read_bytes(); lab = SocialNative(self.after)
            status = lab.lookup_reply(body)
            self.assertEqual(status, 0xFFFFFFFF if body[0] else body[1])
            self.assertEqual(lab.read(0x57F9AD4), 0)
            receipts.append(dict(file=file.name, body=body.hex(), status=status))
        Path(folder, 'native-804-replies.json').write_text(json.dumps(receipts, indent=2))

    def test_04_exact_changes_and_independent_image(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo]); cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        opt, _ = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0], patch.checksum(self.after, opt+64))
        for invalid in (self.after, self.before[:-1], b'X'+self.before[1:]):
            with self.assertRaises(ValueError): patch.build(invalid)
        with self.assertRaises(ValueError): patch.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder, 'DNF.exe')
            patch.write_candidate(self.source, output)
            self.assertEqual(output.read_bytes(), self.after)
            windows_image(output)
            with self.assertRaises(ValueError): patch.write_candidate(self.source, output)

    def test_05_native_friend_sender_keeps_explicit_zero_group(self):
        for opcode, entry in ((290, 0x29C2D50), (291, 0x29C2F60)):
            for group in (0, 1, 7, 249):
                before, after = SocialSender(self.before), SocialSender(self.after)
                for lab in (before, after):
                    lab.existing_friend = opcode == 291
                    lab.run(entry, (group, lab.NAME), ecx=lab.MANAGER)
                    self.assertEqual(len(lab.sent), 1)
                    self.assertEqual(lab.sent[0][0], opcode)
                offset = 2 if opcode == 290 else 0
                self.assertEqual(before.sent[0][1][offset], group or 1)
                self.assertEqual(after.sent[0][1][offset], group)
                if group: self.assertEqual(before.sent, after.sent)
                directory = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
                if directory:
                    Path(directory, f'friend-{opcode}-group{group}.bin').write_bytes(after.sent[0][1])
                    Path(directory, f'friend-{opcode}-group{group}-before.bin').write_bytes(before.sent[0][1])

    def test_06_native_whisper_sender_uses_group_not_owner_channel(self):
        for group in (0, 7, 249):
            lab = SocialSender(self.before)
            self.assertEqual(lab.whisper(group)[-1], group)
            self.assertEqual(lab.sent[0][0], 17)
            directory = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
            if directory:
                Path(directory, f'whisper-group{group}-17.bin').write_bytes(lab.sent[0][1])

    def test_07_cumulative_controls_candidate_preserves_all_other_receive_ids(self):
        path = os.environ.get('DNF_GUILD_SOCIAL_CONTROLS_CLIENT')
        if not path: self.skipTest('requires reviewed controls candidate')
        original = Path(path).read_bytes(); fixed, receipt = patch.build(original)
        self.assertEqual(receipt['inputSha256'], patch.CONTROLS_SHA256)
        for classification in (0, 1, 2, 255):
            before = native(original, (classification,), range(65536))
            after = native(fixed, (classification,), range(65536))
            self.assertEqual([i for i in range(65536) if before[i] != after[i]],
                             [804] if classification == 1 else [])
            if classification == 1:
                for opcode in (804, 838, 839): self.assertEqual(after[opcode], 1)
        cursor = 0
        for lo, hi in receipt['changedRanges']:
            self.assertEqual(original[cursor:lo], fixed[cursor:lo]); cursor = hi
        self.assertEqual(original[cursor:], fixed[cursor:])


class SocialSender(SocialNative):
    NAME, ACTOR, CHAT = 0x20016000, 0x20017000, 0x20014000

    def __init__(self, data):
        super().__init__(data)
        self.u.mem_map(self.NETWORK+0x100000, 0x200000)
        self.put(0x57ED8D4, self.NETWORK)
        self.put(self.NETWORK+8, 1)
        self.u.mem_write(self.NAME, '来来来'.encode('utf-16le')+b'\0\0')
        self.put(0x56A3758, self.MANAGER)
        self.chunks, self.sent, self.existing_friend = [], [], False
        self.hooks.update({
            0x29BF790: lambda: self.ret(value=0),
            0x29C1630: lambda: self.ret(12, int(self.existing_friend)),
            0x296D810: lambda: self.ret(8, 0),
            0x296DD80: lambda: self.ret(value=self.ACTOR),
            0x37E3940: lambda: self.ret(), 0x37E3970: lambda: self.ret(),
            # Fixture for the NOTI1 protobuf field7 global directory ID.
            0x37DF550: self.directory_group, 0x37DF5C0: self.directory_group,
            0x37DF630: self.directory_group, 0x37DF6A0: self.directory_group,
            0x37DF7F0: self.directory_group,
            0x374F280: self.start, 0x38C7F70: self.sink,
            0x374F550: self.write_dstr, 0x3750AC0: self.finish,
            0x2526480: lambda: self.ret(value=0),
            0x25260A0: lambda: self.ret(value=0),
        })

    def directory_group(self):
        self.put(self.arg(1), 1); self.ret(8)

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
        raw = struct.pack('<I', len(data)) + data
        self.chunks.append(raw)
        self.put(self.NETWORK+0x2BCC2C, self.read(self.NETWORK+0x2BCC2C)+len(raw))
        self.ret(4)

    def finish(self):
        body = b''.join(self.chunks)
        assert len(body) == self.read(self.NETWORK+0x2BCC2C)
        self.sent.append((self.opcode, body)); self.ret(value=0)

    def whisper(self, group):
        # Bounded producer after UTF-16 input preparation. Actor getter,
        # type selection, UID fallback and all scalar writes execute native.
        frame = self.STACK+0x2000
        self.put(self.ACTOR+0x3AC, group)
        self.put(self.CHAT+0x228, group)
        self.put(self.CHAT+0x27C, -1)
        self.u.mem_write(frame-0x38, '来来来'.encode('utf-16le')+b'\0\0')
        self.u.mem_write(frame-0x838, 'hello'.encode('utf-16le')+b'\0\0')
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_ESI, self.CHAT)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.emu_start(0x348F5E5, 0x348F701, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x348F701
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return self.sent[0][1]


if __name__ == '__main__': unittest.main()
