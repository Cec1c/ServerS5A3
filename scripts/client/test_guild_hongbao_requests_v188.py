"""Execute native senders and raw writer. Only the byte sink/UI are adapters."""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

from unicorn.x86_const import *
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import windows_image
import patch_guild_hongbao_requests_v188 as patch


class HongbaoSender(WarNative):
    OBJECT, ROW = 0x20010000, 0x20011000

    def __init__(self, data):
        super().__init__(data)
        self.u.mem_map(self.NETWORK + 0x100000, 0x200000)
        self.put(self.NETWORK + 8, 1)
        self.sent, self.copies = [], []
        self.hooks.update({
            0x374F280: self.start,
            # 374EEB0 executes unchanged; capture at its downstream byte sink.
            0x38C7F70: self.sink,
            0x3750AC0: lambda: self.ret(),
            0x32B94A0: lambda: self.ret(12),
            0xF49030: lambda: self.ret(4, self.ROW),
            0x11B1990: lambda: self.ret(),
        })

    def start(self):
        self.sent.append(dict(opcode=self.arg(), body=''))
        self.put(self.NETWORK + 0x2BCC2C, 0)
        self.ret(4)

    def sink(self):
        self.copies.append(self.arg(1))
        self.sent[-1]['body'] += bytes(self.u.mem_read(self.arg(), self.arg(1))).hex()
        self.ret(8, 0)

    def issue(self, item=490003266, portions=5, confirm=0, poison=0xA5):
        self.u.mem_write(self.STACK - 0x400, bytes([poison]) * 0x400)
        self.put(self.OBJECT + 0x2C, portions, item)
        self.run(0x1091E00, (confirm,), ecx=self.OBJECT)
        return self.sent

    def claim(self, record=123, confirm=0, poison=0xA5):
        self.u.mem_write(self.STACK - 0x400, bytes([poison]) * 0x400)
        self.put(self.OBJECT + 0x2C, record, 0)
        self.run(0x11A6130, (confirm,), ecx=self.OBJECT)
        return self.sent

    def history(self, record=123, poison=0xA5):
        # The bounded original event slice selects the record from its model.
        self.u.mem_write(self.STACK - 0x400, bytes([poison]) * 0x400)
        bp = self.STACK + 0x1000
        self.put(bp - 0x48, self.DATA)
        self.put(self.DATA + 0x4C, 1)
        self.put(self.OBJECT + 0x12A4, 1)
        self.put(self.ROW, record)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_EBP, bp)
        self.u.reg_write(UC_X86_REG_ESI, 0)
        self.u.reg_write(UC_X86_REG_EBX, self.OBJECT)
        self.u.mem_write(bp - 0x44, bytes([poison]) * 17)
        self.u.emu_start(0x11B3ED7, 0x11B3F2E, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x11B3F2E
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        assert self.reg(UC_X86_REG_EBP) == bp
        assert self.reg(UC_X86_REG_EBX) == self.OBJECT
        return self.sent


class HongbaoRequestTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_HONGBAO_REQUEST_CLIENT')
        if not path:
            raise unittest.SkipTest('set DNF_HONGBAO_REQUEST_CLIENT to cumulative V188')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_original_raw_writer_includes_uninitialized_prefix(self):
        for method, tail in [('issue', struct.pack('<IB', 490003266, 5)), ('claim', struct.pack('<I', 123)), ('history', struct.pack('<I', 123))]:
            for poison in (0xA5, 0x5A):
                lab = HongbaoSender(self.before)
                raw = bytes.fromhex(getattr(lab, method)(poison=poison)[0]['body'])
                self.assertEqual(raw, bytes([poison]) * 13 + tail)
                self.assertEqual(lab.copies, [13 + len(tail)])
                self.assertEqual(lab.read(lab.NETWORK + 0x2BCC2C), len(raw))

    def test_candidate_four_items_and_portion_boundaries(self):
        for item in range(490003263, 490003267):
            for portions in (4, 5, 30, 31):
                for confirm in (0, 1):
                    lab = HongbaoSender(self.after)
                    got = lab.issue(item, portions, confirm)
                    want = [dict(opcode=1303, body=struct.pack('<IB', item, portions).hex())] if confirm == 0 and 5 <= portions <= 30 else []
                    self.assertEqual(got, want)
                    self.assertEqual(lab.copies, [5] if want else [])

    def test_candidate_claim_history_and_cancel(self):
        for record in (1, 123, 0x12345678):
            for method, opcode in [('claim', 1304), ('history', 1302)]:
                lab = HongbaoSender(self.after)
                self.assertEqual(getattr(lab, method)(record), [dict(opcode=opcode, body=struct.pack('<I', record).hex())])
                self.assertEqual(lab.copies, [4])
        self.assertEqual(HongbaoSender(self.after).claim(confirm=1), [])

    def test_export_real_sender_bodies(self):
        path = os.environ.get('DNF_HONGBAO_NATIVE_REQUESTS')
        if not path:
            self.skipTest('set DNF_HONGBAO_NATIVE_REQUESTS to export native bodies')
        result = dict(inputSha256=patch.INPUT_SHA256, outputSha256=self.receipt['outputSha256'])
        for name, data in [('before', self.before), ('after', self.after)]:
            result[name] = {kind: getattr(HongbaoSender(data), kind)()[0] for kind in ('issue', 'claim', 'history')}
        Path(path).write_text(json.dumps(result, indent=2), encoding='utf-8')

    def test_exact_diff_preserves_notice_and_previous_patches(self):
        ranges = self.receipt['changedRanges']
        actual = [i for i, (a, b) in enumerate(zip(self.before, self.after)) if a != b]
        self.assertEqual(len(self.before), len(self.after))
        self.assertTrue(all(any(lo <= i < hi for lo, hi in ranges) for i in actual))
        self.assertEqual(sum(hi-lo for lo, hi in ranges), 10)  # Six instruction bytes and checksum.
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / 'candidate.exe'
            self.assertEqual(patch.write_candidate(self.source, out), self.receipt)
            if os.name == 'nt':
                windows_image(out)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, out)
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source)

    def test_real_dispatcher_status_error_and_callback(self):
        for opcode in range(1300, 1305):
            for ok in (False, True):
                lab = WarNative(self.after)
                called = []
                def callback():
                    called.append((lab.arg(1), lab.arg(2), lab.read(0x57F9AD4)))
                    lab.ret()
                lab.put(0x2000C000, lab.STOP + 0x100, 0)
                lab.hooks[lab.STOP + 0x100] = callback
                tail = bytes(12 if ok else 4 if opcode < 1303 else 0)
                body = (b'\1' if ok else b'\0\x22') + tail
                wire = bytearray(16) + body
                wire[0] = 1
                struct.pack_into('<H', wire, 1, opcode)
                struct.pack_into('<I', wire, 3, len(wire))
                lab.u.mem_write(lab.PACKET, bytes(wire))
                lab.run(0x25239A0, (0, lab.PACKET, 0), ecx=lab.NETWORK)
                self.assertEqual(called, [(int(ok), 0 if ok else 34, len(tail))])


if __name__ == '__main__':
    unittest.main()
