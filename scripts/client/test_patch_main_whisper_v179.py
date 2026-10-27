"""Outgoing native type-1 record, direction, failure and cumulative diff checks."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_main_whisper_v179 as patch
from test_main_whisper_v179 import MainWhisperNative
from test_patch_fatigue_count_v164 import windows_image
from unicorn.x86_const import *


class MainWhisperPatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_MAIN_WHISPER_CUMULATIVE_CLIENT')
        if not path: raise unittest.SkipTest('exact cumulative candidate required')
        cls.before = Path(path).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_native_prepared_record_red_green_and_transport_failure(self):
        for group in (0, 7, 249):
            for uid in (5, 65535):
                for source_group in (0, 7):
                    for result in (0, 0x80000054, 0x80000057):
                        before, after = MainWhisperNative(self.before), MainWhisperNative(self.after)
                        self.assertEqual(before.send(group, uid, True, result, source_group), after.send(group, uid, True, result, source_group))
                        self.assertEqual(before.shown, [])
                        self.assertEqual(after.shown, [] if result else [dict(type=1, uid=1, name='来来来', message='hello')])

    def test_display_is_limited_to_op17_type1(self):
        # Other message types and item-link opcode424 keep the native path.
        for op, typ in ((17, 1), (17, 7), (17, 2), (424, 1), (17, 0x24)):
            lab = MainWhisperNative(self.after)
            frame = lab.STACK+0x2000
            lab.put(frame-0x33C, op)
            lab.put(frame-0x348, typ)
            called = []
            lab.hooks[0x29FFDC0] = lambda: (called.append(lab.arg()), lab.ret(4))
            lab.u.reg_write(UC_X86_REG_EBP, frame)
            lab.u.reg_write(UC_X86_REG_EBX, lab.CHAT)
            lab.u.reg_write(UC_X86_REG_ESP, lab.STACK)
            lab.u.emu_start(patch.DISPLAY_SITE, patch.DISPLAY_SITE+5, count=1000)
            self.assertEqual(called, [frame-0x278] if (op, typ) == (17, 1) else [])
            self.assertEqual(lab.reg(UC_X86_REG_ESP), lab.STACK)
            self.assertEqual(lab.reg(UC_X86_REG_EDX), 424)

    def test_exact_diff_checksum_and_windows_mapping(self):
        ranges = self.receipt['changedRanges']
        self.assertEqual(len(self.before), len(self.after))
        for i, (a, b) in enumerate(zip(self.before, self.after)):
            if a != b: self.assertTrue(any(start <= i < end for start, end in ranges), hex(i))
        opt, sections = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0], patch.checksum(self.after, opt+64))
        for va, size in ((0x2523A13, 5), (0x6EAD000, 0xD1D), (0x29C2E0E, 6), (0x29C2FD8, 6)):
            at = patch.pe.offset(sections, va, size)
            self.assertEqual(self.before[at:at+size], self.after[at:at+size])
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder, 'candidate.exe')
            path.write_bytes(self.after)
            windows_image(path)


if __name__ == '__main__': unittest.main()
