"""V197 real guild channel gate and cross-channel adventure routing checks."""
import os
from pathlib import Path
import struct
import unittest

import patch_title_guild_v197 as patch
from test_guild_member_profile import GuildProfileNative

VIEWER_CHANNEL = 13
TARGETS = (11, 13)
CHANNEL_TYPES = (11, 23, 32, 25, 45, 0)
RAID_TYPES = frozenset((23, 32, 25, 45))
ADVENTURE_CROSS = (819, b'\x0b\x11\x00\x04\x01')
ADVENTURE_SAME = (1401, b'\x11\x00')


class TitleGuildV197Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name = os.environ.get('DNF_CLIENT_V196')
        if not name:
            raise unittest.SkipTest('requires exact V196 cumulative client')
        cls.before = Path(name).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)
        if cls.receipt['inputSha256'] != patch.INPUT_SHA256:
            raise AssertionError('unexpected V196 input')

    def gate(self, image, channel_type, target):
        lab = GuildProfileNative(image)
        lab.channel = VIEWER_CHANNEL
        # Keep the image's real 0x25254f0 channel-type predicate. Only the
        # network accessor is a fixture, so the predicate reads NETWORK+0xfc8.
        lab.hooks.pop(0x25254F0)
        lab.put(lab.NETWORK + 0xFC8, channel_type)
        lab.hooks[0x1FA2790] = lambda: lab.ret(value=lab.NETWORK)
        lab.prepare_row(uid=17, channel=target, directory=1)
        self.assertEqual(lab.row_decision(), 'actor')
        self.assertEqual(lab.build_menu(), [(17, target, 1)])
        return lab

    def request(self, image, channel_type, target, kind=4):
        return self.gate(image, channel_type, target).request(kind)

    def test_01_ordinary_cross_channel_red_then_green(self):
        self.assertEqual(self.request(self.before, 11, 11), ADVENTURE_SAME)
        self.assertEqual(self.request(self.after, 11, 11), ADVENTURE_CROSS)

    def test_02_channel_types_preserve_raid_and_same_channel(self):
        for channel_type in CHANNEL_TYPES:
            old_cross = ADVENTURE_CROSS if channel_type in RAID_TYPES else ADVENTURE_SAME
            with self.subTest(channelType=channel_type, target=11):
                self.assertEqual(self.request(self.before, channel_type, 11), old_cross)
                self.assertEqual(self.request(self.after, channel_type, 11), ADVENTURE_CROSS)
            with self.subTest(channelType=channel_type, target=13):
                self.assertEqual(self.request(self.before, channel_type, 13), ADVENTURE_SAME)
                self.assertEqual(self.request(self.after, channel_type, 13), ADVENTURE_SAME)

    def test_03_profile_requests_identical_before_and_after(self):
        for channel_type in CHANNEL_TYPES:
            for target in TARGETS:
                with self.subTest(channelType=channel_type, target=target):
                    before = self.request(self.before, channel_type, target, kind=0)
                    after = self.request(self.after, channel_type, target, kind=0)
                    self.assertEqual(before, after)
                    self.assertEqual(self.request(self.after, channel_type, target, kind=0), after)

    def test_04_already_patched_or_mutated_input_rejected(self):
        for image in (self.after, self.before[:-1], b'X' + self.before[1:]):
            with self.assertRaises(ValueError):
                patch.build(image)

    def test_05_exact_changed_ranges_and_appended_section(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:len(self.before)])
        self.assertEqual(self.receipt['appendRange'], [len(self.before), len(self.after)])
        self.assertEqual(self.receipt['size'], len(self.after))
        self.assertEqual(patch.pe.sha(self.after), self.receipt['outputSha256'])
        _, opt, _, _ = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt + 64)[0],
                         patch.checksum(self.after, opt + 64))


if __name__ == '__main__':
    unittest.main()
