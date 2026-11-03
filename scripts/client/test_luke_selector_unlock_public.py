"""Replay actual op602 -> manager -> visibility/admission/tooltip x86 paths.

Only packet reads, STL storage access and UI/world-map leaves are adapters.
No client file or running process is changed; gameplay acceptance is separate.
DNF_LUKE_CLIENT selects the exact reviewed V207 input.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest
from unicorn import x86_const as X
from patch_luke_selector_progress import build, APPROVED_INPUTS
from test_luke_selector_progress_native import Native


@unittest.skipUnless(os.environ.get("DNF_LUKE_CLIENT"), "exact client not supplied")
class UnlockTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        before = Path(os.environ["DNF_LUKE_CLIENT"]).read_bytes()
        assert APPROVED_INPUTS.get(hashlib.sha256(before).hexdigest()) == len(before)
        cls.client, _ = build(before)

    def project(self, cursor, mask):
        n = Native(self.client)
        manager, definitions = 0x11000000, 0x11001000
        proxy, definition_node, cache_node = 0x11003000, 0x11004000, 0x11005000
        sentinel, vector = 0x11006000, 0x11007000
        n.put(manager, definitions)
        n.put(proxy, 0x11008000)
        n.put(0x11008000, 0, 0, sentinel)
        n.put(sentinel)
        n.put(cache_node + 0x10, 42)
        n.put(definition_node + 0x14, vector, vector + 24)
        n.put(vector, 5101, 5102, 5103, 5104, 5105, 5106)
        sets = {cache_node + off: [] for off in (0x14, 0x28, 0x3c)}

        def find():
            dest = n.arg()
            self.assertEqual(n.read(n.arg(1)), 42)
            node = definition_node if n.u.reg_read(X.UC_X86_REG_ECX) == definitions else cache_node
            n.put(dest, proxy, 0, node)
            n.ret(dest, 8)

        def end():
            dest = n.arg()
            n.put(dest, proxy, 0, sentinel)
            n.ret(dest, 4)

        def always_visible_find():
            # Actual sequence42 has no [always visible dungeon] entries.
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ECX), definition_node + 0x90)
            dest = n.arg()
            n.put(dest, proxy, 0, sentinel)
            n.ret(dest, 8)

        n.hooks[0x279f660] = find
        n.hooks[0x16c5630] = find
        n.hooks[0xa8f6c0] = end
        n.hooks[0xfa1600] = always_visible_find

        def clear():
            sets[n.u.reg_read(X.UC_X86_REG_ECX)].clear()
            n.ret()

        def insert():
            target = sets[n.u.reg_read(X.UC_X86_REG_ECX)]
            value = n.read(n.arg(1))
            if value not in target:
                target.append(value)
            n.ret(n.arg(), 8)

        n.hooks[0x2142f90] = clear
        n.hooks[0x8d4a70] = insert
        # No optional world-map-specific exception items in this fixture.
        n.hooks[0x3754450] = lambda: n.ret(0, 8)
        n.hooks[0x18c0d90] = lambda: n.ret(manager)
        for va in (0x32b49c0, 0x359d0a0, 0x1fa2790, 0x13023f0, 0x371aae0):
            n.hooks[va] = lambda: n.ret()
        n.hooks[0x370dcc0] = lambda: n.ret(42)
        packet = struct.pack("<IBI", 42, cursor, mask)
        consumed = 0

        def read(size):
            nonlocal consumed
            self.assertLessEqual(consumed + size, len(packet))
            n.u.mem_write(n.arg(), packet[consumed:consumed + size])
            consumed += size
            n.ret(1)

        n.hooks[0x37e83c0] = lambda: read(4)
        n.hooks[0x37e82e0] = lambda: read(n.arg(1))
        # No adapter for the manager or either projection: actual x86 runs.
        n.call(0x20076d0)
        self.assertEqual(consumed, 9)
        self.assertEqual(n.read(manager + 0x18), cache_node + 0x10)
        return tuple(sets[cache_node + off] for off in (0x14, 0x28, 0x3c))

    def test_reported_packet_reproduces_second_visible_but_locked(self):
        visible, enterable, completed = self.project(0, 1)
        self.assertEqual(visible, [5101, 5102])
        self.assertEqual(enterable, [5101])
        self.assertEqual(completed, [5101])

    def test_corrected_first_clear_unlocks_second_without_faking_its_clear(self):
        visible, enterable, completed = self.project(1, 1)
        self.assertEqual(visible, [5101, 5102, 5103])
        self.assertEqual(enterable, [5101, 5102])
        self.assertEqual(completed, [5101])

    def test_progressive_unlock_and_saturated_throne(self):
        ids = [5101, 5102, 5103, 5104, 5105, 5106]
        for cleared, cursor, mask in [(0,0,0),(1,1,1),(2,2,3),(3,3,7),(4,4,15),(5,5,31),(6,5,63)]:
            with self.subTest(cleared=cleared):
                visible, enterable, completed = self.project(cursor, mask)
                self.assertEqual(visible, ids[:min(cursor + 2, 6)])
                self.assertEqual(enterable, ids[:cursor + 1])
                self.assertEqual(completed, ids[:cleared])

    def test_sparse_tooltip_history_does_not_skip_locked_predecessors(self):
        visible, enterable, completed = self.project(1, 37)
        self.assertEqual(enterable, [5101, 5102])
        self.assertEqual(completed, [5101, 5103, 5106])
        self.assertNotIn(5106, enterable)


if __name__ == "__main__":
    unittest.main()
