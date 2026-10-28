"""Native CMD215 rejection reason, reset and unchanged success settlement."""
import gc
import os
from pathlib import Path
import tempfile
import unittest

from test_vending_ack_error import AckLab, success_body
from test_patch_fatigue_count_v164 import windows_image
import patch_vending_premium_error as patch


class PremiumLab(AckLab):
    def __init__(self, data):
        super().__init__(data)
        self.literal_messages = []

    def window_call(self):
        if self.arg() == 726 and not self.messages:
            at, raw = self.arg(1), bytearray()
            for _ in range(100):
                b = bytes(self.u.mem_read(at, 2)); at += 2
                if b == b'\0\0':
                    break
                raw.extend(b)
            else:
                raise AssertionError('unterminated entitlement message')
            self.literal_messages.append(raw.decode('utf-16le'))
        super().window_call()


class VendingPremiumTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('VENDING_PREMIUM_SOURCE')
        if not path:
            raise unittest.SkipTest('set VENDING_PREMIUM_SOURCE to cumulative hongbao-state image')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def tearDown(self):
        gc.collect()

    def test_old_generic_message_and_missing_dedicated_case(self):
        lab = PremiumLab(self.before)
        lab.pending()
        self.assertEqual(lab.receive_native(b'\0\1'), 0)
        self.assertEqual(lab.messages, [3002])
        lab = PremiumLab(self.before)
        lab.pending()
        self.assertEqual(lab.receive_native(b'\0\x17'), 0)
        self.assertEqual(lab.windows, [])
        self.assertEqual(lab.reset_calls, 0)

    def test_entitlement_rejection_resets_without_assets_or_late_settlement(self):
        lab = PremiumLab(self.after)
        for n, state in enumerate((3, 6, 6), 1):
            lab.pending(state)
            self.assertEqual(lab.receive_native(b'\0\x17'), 0)
            self.assertEqual(lab.callback_args[-1], [215, 0, 23])
            self.assertEqual(lab.reset_calls, n)
            self.assertEqual(lab.literal_messages, [patch.MESSAGE]*n)
            self.assertEqual(lab.messages, [])
            self.assertEqual(lab.read(lab.CONTROL+0x184), 1)
            for off in (0x174, 0x180, 0x1C0, 0x1C4, 0x1CC, 0x1D0, 0x1D4):
                self.assertEqual(lab.read(lab.CONTROL+off), 0)
            lab.tick()
            self.assertEqual(lab.finish_calls, 0)
            self.assertEqual((lab.insertions, lab.coin_updates, lab.display), ([], [], []))
        lab.pending(3)
        self.assertEqual(lab.receive_native(success_body()), 0)
        self.assertEqual((lab.reward_count, len(lab.display)), (1, 1))
        self.assertEqual(lab.coin_updates, [2])

    def test_other_errors_and_success_unchanged(self):
        for code, msg in ((1, 3002), (4, 255), (17, 260), (22, 1661)):
            lab = PremiumLab(self.after)
            self.assertEqual(lab.receive_native(bytes([0, code])), 0)
            self.assertEqual(lab.messages, [msg])
            self.assertEqual(lab.literal_messages, [])
        for total, remaining, slot in ((1, 2, 79), (1, 0, 121)):
            results = []
            for data in (self.before, self.after):
                lab = PremiumLab(data)
                if remaining == 0:
                    lab.coin_count = 1
                lab.receive_native(success_body(total, remaining, slot))
                results.append((lab.slots, lab.effect, lab.entries, lab.display, lab.coin_updates, lab.insertions))
            self.assertEqual(*results)

    def test_exact_diff_and_windows_mapping(self):
        changes = self.receipt['changedRanges']
        self.assertTrue(all(any(lo <= i < hi for lo, hi in changes) for i, (a,b) in enumerate(zip(self.before,self.after)) if a != b))
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder)/'candidate.exe'
            self.assertEqual(patch.write_candidate(self.source,out), self.receipt)
            if os.name == 'nt':
                windows_image(out)
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source,out)
        with self.assertRaises(ValueError):
            patch.build(self.after)


if __name__ == '__main__':
    unittest.main()
