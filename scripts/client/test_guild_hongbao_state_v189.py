"""Native list decoding, status controls and history sender for claimed rows."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

from unicorn.x86_const import *
from test_guild_hongbao_requests_v188 import HongbaoSender
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import windows_image
import patch_guild_hongbao_state_v189 as patch


class HongbaoList(WarNative):
    ROW, CONTROL, VT = 0x20010000, 0x20011000, 0x20012000

    def __init__(self, data):
        super().__init__(data)
        self.rows, self.visible, self.texts = {}, {}, {}
        self.next = self.ROW
        self.hooks.update({
            0x2303380: lambda: self.ret(value=self.GUILD),
            0x22FCAA0: lambda: self.ret(4, self.GUILD),
            0x32BA120: lambda: self.ret(4, 0),
            0x2193CD0: self.clear, 0x9E87B0: self.clear,
            0xF49030: self.row, 0x11B0BF0: self.row,
            0x11B1990: lambda: self.ret(),
            self.STOP+0x100: self.visibility,
            self.STOP+0x110: self.text,
        })

    def clear(self):
        self.rows[self.reg(UC_X86_REG_ECX)] = []
        self.ret()

    def row(self):
        key, index = self.reg(UC_X86_REG_ECX), self.read(self.arg())
        rows = self.rows.setdefault(key, [])
        while len(rows) <= index:
            rows.append(self.next)
            self.next += 40
        self.ret(4, rows[index])

    def visibility(self):
        self.visible[self.reg(UC_X86_REG_ECX)] = self.arg()
        self.ret(4)

    def text(self):
        at, value = self.arg(), bytearray()
        for _ in range(32):
            b = bytes(self.u.mem_read(at, 2)); at += 2
            if b == b'\0\0':
                break
            value.extend(b)
        else:
            raise AssertionError('unterminated status text')
        self.texts[self.reg(UC_X86_REG_ECX)] = value.decode('utf-16le')
        self.ret(4)

    def receive(self, rows):
        body = struct.pack('<HIII', 30, 5000, 1, len(rows)) + b''.join(rows)
        self.u.mem_write(self.PACKET, body)
        self.put(0x57F9AD0, self.PACKET, len(body), 0, 0)
        self.run(0xF491B0, (0, 1), pop=0)
        assert self.read(0x57F9AD4) == 0
        return {off: [bytes(self.u.mem_read(p, 40)) for p in self.rows.get(self.GUILD+off, [])] for off in (0x2C, 0x40)}

    def render(self, row):
        self.rows[self.GUILD+0x40] = [self.ROW]
        self.u.mem_write(self.ROW, row)
        bp, slot = self.STACK+0x1000, self.DATA
        self.put(bp-0x68, 0)
        self.put(bp-0x90, self.GUILD)
        self.put(slot+0x18, self.CONTROL)
        self.put(slot+0x20, self.CONTROL+0x100)
        for control in (self.CONTROL, self.CONTROL+0x100):
            self.put(control, self.VT)
        self.put(self.VT+8, self.STOP+0x100)
        self.put(self.VT+0xE4, self.STOP+0x110)
        regs = {UC_X86_REG_EBP: bp, UC_X86_REG_EBX: slot, UC_X86_REG_ESI: 0x1234, UC_X86_REG_EDI: 0x5678, UC_X86_REG_ESP: self.STACK}
        for reg, value in regs.items():
            self.u.reg_write(reg, value)
        self.u.emu_start(patch.SITE, 0x11B23E0, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x11B23E0
        assert all(self.reg(reg) == value for reg, value in regs.items())
        return self.visible, self.texts


def wire(ident=1, remaining=4, state=2):
    return struct.pack('<II', ident, 490003266) + b'Sender'.ljust(30, b'\0') + bytes([remaining, state])


class HongbaoStateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_HONGBAO_STATE_CLIENT')
        if not path:
            raise unittest.SkipTest('set DNF_HONGBAO_STATE_CLIENT to cumulative V189')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_real_receiver_separates_claimable_and_history(self):
        rows = [wire(1, 5, 0), wire(2, 0, 1), wire(3, 4, 2)]
        old = HongbaoList(self.before).receive(rows)
        self.assertEqual(old[0x2C], [rows[0], rows[2]])
        new = HongbaoList(self.after).receive(rows)
        self.assertEqual(new, {0x2C: [rows[0]], 0x40: rows[1:]})

    def test_original_claimed_row_shows_exhausted_stamp(self):
        lab = HongbaoList(self.before)
        shown, text = lab.render(wire())
        self.assertEqual(shown, {lab.CONTROL: 0, lab.CONTROL+0x100: 1})
        self.assertEqual(text, {})

    def test_claimed_remaining_and_completed_controls(self):
        for count in (1, 4, 9, 10, 29, 30):
            lab = HongbaoList(self.after)
            shown, text = lab.render(wire(remaining=count))
            self.assertEqual(shown, {lab.CONTROL: 1, lab.CONTROL+0x100: 0})
            self.assertEqual(text, {lab.CONTROL: f'已领 余{count}份'})
        for count in (0, 4):  # Exhausted and expired envelopes retain original UI.
            lab = HongbaoList(self.after)
            shown, text = lab.render(wire(remaining=count, state=1))
            self.assertEqual(shown, {lab.CONTROL: 0, lab.CONTROL+0x100: 1})
            self.assertEqual(text, {})

    def test_claimed_history_uses_original_corrected_sender(self):
        self.assertEqual(HongbaoSender(self.after).history(123), [dict(opcode=1302, body='7b000000')])

    def test_diff_and_os_image_mapping(self):
        changes = self.receipt['changedRanges']
        self.assertTrue(all(any(lo <= i < hi for lo, hi in changes) for i, (a,b) in enumerate(zip(self.before, self.after)) if a != b))
        self.assertEqual(len(self.after)-len(self.before), 4096)
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder)/'candidate.exe'
            self.assertEqual(patch.write_candidate(self.source, out), self.receipt)
            if os.name == 'nt':
                windows_image(out)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, out)
        with self.assertRaises(ValueError):
            patch.build(self.after)


if __name__ == '__main__':
    unittest.main()
