"""Bounded native mail click/query producer with UI, role lookup and byte-sink adapters.

The existing scalar writer and query producer execute original x86. UTF16 to
GB18030 conversion, widgets, current-role lookup and socket effects are adapters.
This does not launch a game or send mail to a player.
"""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

from unicorn.x86_const import *
from test_guild_social_channel_v179 import SocialSender
from test_patch_fatigue_count_v164 import native
import patch_mail_recipient_default_region as patch


class MailNative(SocialSender):
    WINDOW, WIDGET, VTABLE = 0x20018000, 0x20019000, 0x2001A000

    def __init__(self, data, group=0, selection=0, manager=True, actor=True):
        super().__init__(data)
        self.put(patch.MANAGER, self.MANAGER if manager else 0)
        self.put(patch.SELECTION, selection)
        self.put(self.ACTOR+0x3AC, group)
        self.hooks[patch.GET_CHARACTER] = lambda: self.ret(value=self.ACTOR if actor else 0)
        self.put(self.WIDGET, self.VTABLE)
        self.put(self.VTABLE+8, self.STOP+0x200)
        self.put(self.VTABLE+0xE4, self.STOP+0x210)
        self.hooks[self.STOP+0x200] = lambda: self.ret(4)
        self.hooks[self.STOP+0x210] = lambda: self.ret(value=self.NAME)
        self.hooks[0x29D3190] = lambda: self.ret()  # Result-wait UI projection.
        for field in (0x1B8, 0x138, 0x17C, 0x184, 0x1A0, 0x1FC, 0x1A8, 0x1E4):
            self.put(self.WINDOW+field, self.WIDGET)
        self.initial = {UC_X86_REG_EBX: 0x12340001, UC_X86_REG_ESI: self.WINDOW,
                        UC_X86_REG_EDI: 0x12340002, UC_X86_REG_EBP: self.STACK+0x1000}
        self.exit = None
        self.queried, self.query_errors = [], []
        self.hooks[0x3752120] = self.read_query_name
        self.hooks[0x29D2F90] = lambda: self.ret(value=1)
        self.hooks[0x29D6270] = self.query_projection
        self.hooks[0x29D6410] = self.query_error
        self.hooks[0x2531F90] = lambda: self.ret(value=self.NAME)
        self.hooks[0x2534710] = lambda: self.ret(value=self.NAME)
        self.put(0x4588560, self.STOP+0x220)
        self.hooks[self.STOP+0x220] = self.format_query_message
        self.hooks[0x45FDA0] = self.copy_recipient_name
        self.confirmed_name = None

    def click(self):
        for register, value in self.initial.items():
            self.u.reg_write(register, value)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x202)

        def stop(_u, at, _size, _ctx):
            if at in (patch.EXPLICIT, patch.WARNING, patch.DONE):
                self.exit = at
                self.u.emu_stop()

        from unicorn import UC_HOOK_CODE
        self.u.hook_add(UC_HOOK_CODE, stop)
        self.u.emu_start(patch.SITE, self.STOP, count=30000)
        assert self.exit is not None
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        for register, value in self.initial.items():
            assert self.reg(register) == value
        return self.sent

    def read_query_name(self):
        # Windows character conversion is an adapter; packet cursor and all
        # scalar readers/dispatcher/error classifier are original native code.
        pointer, remaining = self.read(0x57F9AD0), self.read(0x57F9AD4)
        length = self.read(pointer)
        assert remaining >= length+4
        text = bytes(self.u.mem_read(pointer+4, length)).decode('gb18030')
        raw = text.encode('utf-16le')+b'\0\0'
        assert len(raw) <= 2*self.arg(1)
        self.u.mem_write(self.arg(), raw)
        self.put(0x57F9AD0, pointer+length+4, remaining-length-4)
        self.ret(value=len(text))

    def query_projection(self):
        self.queried.append((self.wide(self.arg()).decode('utf-16le'),
                             [self.arg(i) for i in range(1, 7)]))
        # Continue the actual native UI-state update; only formatting/widgets
        # and wstring storage below are adapters.

    def query_error(self):
        self.query_errors.append(self.arg())

    def format_query_message(self):
        self.u.mem_write(self.arg(), b'\0\0')
        self.ret(value=0)

    def copy_recipient_name(self):
        self.confirmed_name = self.wide(self.arg()).decode('utf-16le')
        self.ret(8, self.reg(UC_X86_REG_ECX))

    def receive_query(self, success=True, error_code=2):
        raw = '来来来'.encode('gb18030')
        body = b'\1'+struct.pack('<I', len(raw))+raw+bytes([2, 90, 0, 3, 0, 1, 0, 0]) if success else bytes([0, error_code])
        wire = bytearray(16)+body
        wire[0] = 1
        struct.pack_into('<H', wire, 1, 324)
        struct.pack_into('<I', wire, 3, len(wire))
        self.u.mem_write(self.PACKET, bytes(wire))
        self.put(0x2000C000, 0x1FBC790, 0)
        self.run(0x25239A0, (0, self.PACKET, 0), ecx=self.NETWORK)
        return self.read(0x57F9AD4)


class MailRecipientDefaultRegionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_MAIL_RECIPIENT_CLIENT')
        if not path:
            raise unittest.SkipTest('set DNF_MAIL_RECIPIENT_CLIENT to the exact reviewed client')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.profile = dict(inputSha256=patch.pe.sha(cls.before), inputSize=len(cls.before))
        cls.after, cls.receipt = patch.build(cls.before, cls.profile)

    def test_red_original_zero_selection_stops_before_query(self):
        lab = MailNative(self.before)
        self.assertEqual(lab.click(), [])
        self.assertEqual(lab.exit, patch.WARNING)

    def test_default_current_region_including_zero_uses_native_query(self):
        name = '来来来'.encode('gb18030')
        for group in (0, 1, 118, 249):
            lab = MailNative(self.after, group=group)
            self.assertEqual(lab.click(), [(324, struct.pack('<I', len(name))+name+bytes([group]))])
            self.assertEqual(lab.exit, patch.DONE)
            self.assertEqual(lab.read(patch.SELECTION), group)

    def test_missing_current_context_keeps_original_warning(self):
        for options in (dict(manager=False), dict(actor=False), dict(group=250), dict(group=0xFFFFFFFF)):
            lab = MailNative(self.after, **options)
            self.assertEqual(lab.click(), [])
            self.assertEqual(lab.exit, patch.WARNING)

    def test_explicit_other_region_remains_on_original_ui_path(self):
        for selection in (1, 42, 249):
            lab = MailNative(self.after, selection=selection, manager=False)
            self.assertEqual(lab.click(), [])
            self.assertEqual(lab.exit, patch.EXPLICIT)
            self.assertEqual(lab.read(patch.SELECTION), selection)

    def test_repeat_default_zero_does_not_invent_another_region(self):
        for _ in range(3):
            lab = MailNative(self.after, group=0)
            self.assertEqual(lab.click()[0][1][-1], 0)
            self.assertEqual(lab.read(patch.SELECTION), 0)

    def test_complete_receive_gate_only_adds_query_ack_and_account_roles(self):
        before = native(self.before, (0, 1), range(65536))
        after = native(self.after, (0, 1), range(65536))
        self.assertEqual([(i//65536, i%65536) for i in range(len(before))
                          if before[i] != after[i]], [(0, 718), (1, 324)])
        self.assertEqual(native(self.before, (2, 255), (0, 324, 325, 718, 789, 65535)),
                         native(self.after, (2, 255), (0, 324, 325, 718, 789, 65535)))

    def test_native_query_ack_success_and_missing_recipient_consumed(self):
        lab = MailNative(self.after)
        self.assertEqual(lab.receive_query(), 0)
        self.assertEqual(lab.queried, [('来来来', [90, 3, 0, 1, 2, 0])])
        self.assertEqual(bytes(lab.u.mem_read(0x57B891D, 1)), b'\1')
        self.assertEqual(lab.confirmed_name, '来来来')
        for error_code in (1, 2, 19):
            lab = MailNative(self.after)
            lab.u.mem_write(0x57B891D, b'\1')
            self.assertEqual(lab.receive_query(False, error_code), 0)
            self.assertEqual(lab.queried, [])
            self.assertEqual(lab.query_errors, [error_code])
            self.assertEqual(bytes(lab.u.mem_read(0x57B891D, 1)), b'\0')

    def test_only_declared_original_ranges_and_new_rx_section_change(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:len(self.before)])
        self.assertEqual(len(self.after), len(self.before)+4096)
        opt, sections = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0],
                         patch.pe.checksum(self.after, opt+64))
        nt = opt-24
        table = opt+struct.unpack_from('<H', self.after, nt+20)[0]
        header = table+(len(sections)-1)*40
        self.assertEqual(self.after[header:header+8], b'.mailrg\0')
        self.assertEqual(struct.unpack_from('<I', self.after, header+36)[0], 0x60000020)

    def test_exact_input_output_and_no_overwrite_contract(self):
        with self.assertRaises(ValueError):
            patch.build(self.before, dict(inputSha256='0'*64, inputSize=len(self.before)))
        damaged = bytearray(self.before)
        damaged[patch.pe.offset(patch.pe.headers(self.before)[1], patch.SITE)] ^= 1
        with self.assertRaises(ValueError):
            patch.build(bytes(damaged), self.profile)
        profile = dict(self.profile, outputSha256=self.receipt['outputSha256'],
                       outputSize=len(self.after))
        with tempfile.TemporaryDirectory() as folder:
            destination = Path(folder)/'DNF.exe'
            proof = patch.write_candidate(self.source, destination, profile)
            self.assertEqual(destination.read_bytes(), self.after)
            self.assertEqual(proof, self.receipt)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, destination, profile)
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source, profile)

    def test_export_real_native_query_bodies(self):
        target = os.environ.get('DNF_MAIL_RECIPIENT_NATIVE_EVIDENCE')
        if not target:
            self.skipTest('set DNF_MAIL_RECIPIENT_NATIVE_EVIDENCE to export native request evidence')
        result = {'inputSha256': self.profile['inputSha256'], 'outputSha256': self.receipt['outputSha256']}
        for group in (0, 1, 249):
            opcode, body = MailNative(self.after, group=group).click()[0]
            result[str(group)] = dict(opcode=opcode, body=body.hex())
        Path(target).write_text(json.dumps(result, indent=2))


if __name__ == '__main__':
    unittest.main()
