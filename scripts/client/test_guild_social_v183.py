"""Actual V183 social regressions with existing bounded native adapters."""
import os
from pathlib import Path
import struct
import tempfile
import unittest

from unicorn import UC_HOOK_CODE
import patch_guild_social_v183 as patch
import test_guild_social_channel_v179 as social
import test_patch_main_whisper_v179 as whisper
import test_guild_controls_v179 as controls
import test_guild_town_pvp_v179 as town
import test_guild_follow_channel_v179 as follow
import test_main_whisper_v179 as main_input


class CurrentInput:
    @classmethod
    def setUpClass(cls):
        value = os.environ.get('DNF_SOCIAL_V183_CLIENT')
        if not value:
            raise unittest.SkipTest('exact published V183 required')
        cls.source = Path(value)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)
        cls.data = cls.after


class SocialV183Tests(CurrentInput, social.SocialChannelV179Tests):
    def test_01_all_receive_ids_preserve_class_boundary(self):
        for classification in (0, 1, 2, 3, 127, 255):
            before = social.native(self.before, (classification,), range(65536))
            after = social.native(self.after, (classification,), range(65536))
            self.assertEqual([i for i in range(65536) if before[i] != after[i]],
                             [804] if classification == 1 else [])
            if classification == 1:
                for opcode in (804, 838, 839, 1505):
                    self.assertEqual(after[opcode], 1)
                self.assertEqual(after[1456], 0)
            if classification == 0:
                self.assertEqual(after[1456], 1)

    def test_04_exact_changes_and_independent_image(self):
        cursor = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertLessEqual(cursor, lo)
            self.assertEqual(self.before[cursor:lo], self.after[cursor:lo])
            cursor = hi
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        self.assertEqual(len(self.before), len(self.after))
        opt, _ = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0],
                         patch.pe.checksum(self.after, opt+64))
        for invalid in (self.after, self.before[:-1], b'X'+self.before[1:]):
            with self.assertRaises(ValueError):
                patch.build(invalid)
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source)
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder, 'candidate.exe')
            patch.write_candidate(self.source, target)
            self.assertEqual(target.read_bytes(), self.after)
            controls.windows_image(target)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, target)

    def test_07_cumulative_controls_candidate_preserves_all_other_receive_ids(self):
        # Keep actual requests and complete original ACK receivers functional.
        for data in (self.before, self.after):
            for action, opcode in (('move', 839), ('remove', 838)):
                lab = controls.ControlsLab(data)
                sent = getattr(lab, action)()
                self.assertEqual(len(sent), 1)
                self.assertEqual(sent[0]['opcode'], opcode)
                body = bytes.fromhex(sent[0]['body'])
                self.assertEqual(body[:13], bytes(13))
                self.assertEqual(struct.unpack_from('<5I', body, 13), (7, 71021, 902, 276, 0))
            for opcode, entry in ((838, 0x1066A60), (839, 0x10675C0)):
                for body in (b'\x01', b'\x00\x03'):
                    lab = controls.ControlsLab(data)
                    lab.put(0x2000C000, entry, 0)
                    calls = []
                    def observe(_u, at, _size, _ctx):
                        if at == entry:
                            calls.append([lab.arg(), lab.arg(1)&255, lab.arg(2)&255])
                    lab.u.hook_add(UC_HOOK_CODE, observe)
                    wire = bytearray(16)+body
                    wire[0] = 1
                    struct.pack_into('<H', wire, 1, opcode)
                    struct.pack_into('<I', wire, 3, len(wire))
                    lab.u.mem_write(lab.PACKET, bytes(wire))
                    lab.run(0x25239A0, (0, lab.PACKET, 0), ecx=lab.NETWORK)
                    self.assertEqual(calls, [[opcode, body[0], body[1] if len(body)>1 else 0]])
                    self.assertEqual(lab.read(0x57F9AD4), 0)

    test_practice_scoreboard_has_no_opponent = controls.ControlsTests.test_practice_scoreboard_has_no_opponent


class WhisperV183Tests(CurrentInput, unittest.TestCase):
    test_native_prepared_record_red_green_and_transport_failure = whisper.MainWhisperPatchTests.test_native_prepared_record_red_green_and_transport_failure
    test_display_is_limited_to_op17_type1 = whisper.MainWhisperPatchTests.test_display_is_limited_to_op17_type1


class TownV183Tests(CurrentInput, town.TownPVPV179Tests):
    pass


class FollowV183Tests(CurrentInput, follow.GuildFollowTests):
    pass


class MainInputV183Tests(CurrentInput, main_input.MainWhisperV179Tests):
    pass


if __name__ == '__main__':
    unittest.main()
