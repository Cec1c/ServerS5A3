"""Execute both actual receiver hook sites with narrow native UI/item adapters."""
import os
from pathlib import Path
import struct
import unittest

from unicorn import x86_const as X
from test_upgrade_equipped_warning_native import Native
import patch_disjoint_epic_display as patch


@unittest.skipUnless(os.environ.get('DNF_DISJOINT_DISPLAY_CLIENT'), 'exact V209 input required')
class Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.before = Path(os.environ['DNF_DISJOINT_DISPLAY_CLIENT']).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def exercise(self, data, site, next_row, slot, item=10000001, count=30, missing=False):
        n = Native(data)
        frame, ui, obj, table, amount = 0x11010000, 0x11020000, 0x11030000, 0x11040000, 0x11050000
        n.put(frame-0x310, *([0]*32))
        n.put(frame-0x2ec, item, slot, ui, 0, count)
        n.put(frame-4, 3)
        n.put(ui, 0)
        n.put(obj, table)
        n.put(table, 0, 0, 0, 0x12001000)
        n.ensure(0x12001000, 4096)
        n.put(amount, count)
        calls, copied, stops = [], [], []
        def construct_count():
            self.assertEqual(n.arg(), count)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ECX), frame-0x2d4)
            calls.append('count')
            n.ret(amount, 4)
        def create_item():
            self.assertEqual([n.arg(i) for i in range(4)], [item, amount, 1, 0])
            self.assertEqual(n.read(frame-4), 4)
            calls.append('item')
            n.ret(0 if missing else obj)
        def destroy_count():
            self.assertEqual(n.read(frame-4), 3)
            calls.append('count_destroy')
            n.ret()
        def copy_ui():
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ECX), ui)
            self.assertEqual([n.arg(i) for i in range(2)], [obj, count])
            copied.append((item, count))
            calls.append('copy')
            n.ret(0, 8)
        def destroy_item():
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ECX), obj)
            self.assertEqual(n.arg(), 1)
            calls.append('item_destroy')
            n.ret(obj, 4)
        def stop(label):
            stops.append(label)
            n.u.emu_stop()
        n.hooks.update({0x24319c0:construct_count, 0x2a32b30:create_item,
                        0x985650:destroy_count, 0x33c82e0:copy_ui,
                        0x12001000:destroy_item,
                        site+len(patch.ORIGINAL):lambda:stop('ordinary'),
                        next_row:lambda:stop('next')})
        # The real receiver has three unread cdecl argument slots at this site.
        n.put(n.STACK, frame-0x2dc, frame-0x2ec, frame-0x2e8, n.STOP)
        saved = {X.UC_X86_REG_EBX:2, X.UC_X86_REG_ESI:0, X.UC_X86_REG_EDI:0x11060000,
                 X.UC_X86_REG_EBP:frame, X.UC_X86_REG_ECX:0x1234, X.UC_X86_REG_EDX:0x5678}
        for reg, value in saved.items():
            n.u.reg_write(reg, value)
        n.u.reg_write(X.UC_X86_REG_ESP, n.STACK)
        n.u.emu_start(site, 0, count=10000)
        self.assertEqual(len(stops), 1)
        for reg, value in saved.items():
            self.assertEqual(n.u.reg_read(reg), value)
        self.assertEqual(n.read(frame-4), 3)
        self.assertEqual(n.u.reg_read(X.UC_X86_REG_ESP), n.STACK+(12 if stops[0]=='next' else 0))
        return calls, copied, stops

    def test_both_receivers_display_without_inventory_or_balance_write(self):
        for opcode, site, next_row in patch.SITES:
            with self.subTest(opcode=opcode):
                calls, shown, stops = self.exercise(self.after, site, next_row, 0xffff)
                self.assertEqual(calls, ['count', 'item', 'count_destroy', 'copy', 'item_destroy'])
                self.assertEqual(shown, [(10000001, 30)])
                self.assertEqual(stops, ['next'])

    def test_ordinary_rewards_keep_exact_original_path(self):
        for _, site, next_row in patch.SITES:
            for slot in (0, 121, 358, 65534):
                self.assertEqual(self.exercise(self.before, site, next_row, slot),
                                 self.exercise(self.after, site, next_row, slot))

    def test_zero_or_missing_display_item_is_skipped_without_inventory(self):
        for _, site, next_row in patch.SITES:
            for args in ({'item':0}, {'count':0}, {'missing':True}):
                calls, shown, stops = self.exercise(self.after, site, next_row, 0xffff, **args)
                self.assertEqual(shown, [])
                self.assertEqual(stops, ['next'])
                self.assertNotIn('item_destroy', calls)

    def test_input_is_exact_and_output_preserves_existing_bytes(self):
        with self.assertRaises(ValueError):
            patch.build(self.after)
        restored = bytearray(self.after[:len(self.before)])
        for lo, hi in self.receipt['changedRanges']:
            restored[lo:hi] = self.before[lo:hi]
        self.assertEqual(restored, self.before)
        self.assertEqual(len(self.after), len(self.before))


if __name__ == '__main__':
    unittest.main()
