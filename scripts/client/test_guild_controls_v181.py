"""Replay guild controls against the exact raw V180 input and V181 candidate.

The inherited native scenarios retain their explicit STL, UI and scene adapters.
Final anti-RE compatibility and rendered gameplay require separate validation.
"""
import os
from pathlib import Path
import tempfile
import unittest

import patch_guild_controls_v181 as patch
import test_guild_controls_v179 as controls


class CurrentControlsTests(controls.ControlsTests):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ['GUILD_CONTROLS_RAW_V180'])
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_exact_gate_delta(self):
        for packet_class in (0, 1, 2, 3, 127, 255):
            before = controls.native(self.before, (packet_class,), range(65536))
            after = controls.native(self.after, (packet_class,), range(65536))
            changed = [i for i, (a, b) in enumerate(zip(before, after)) if a != b]
            self.assertEqual(changed, [838, 839, 1505] if packet_class == 1 else [])
            for opcode in changed:
                self.assertEqual((before[opcode], after[opcode]), (0, 1))

    def test_boundary_and_image(self):
        end = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[end:lo], self.after[end:lo])
            end = hi
        self.assertEqual(self.before[end:], self.after[end:])
        for invalid in (self.after, self.before[:-1], b'X' + self.before[1:]):
            with self.assertRaises(ValueError):
                patch.build(invalid)
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / 'DNF.exe'
            patch.write_candidate(self.source, output)
            self.assertEqual(output.read_bytes(), self.after)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, output)
            if os.name == 'nt':
                controls.windows_image(output)


if __name__ == '__main__':
    unittest.main()
