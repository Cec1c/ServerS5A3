"""Native dispatcher -> CMD215 callback -> reset/settlement regression.

Set VENDING_ACK_SOURCE to the exact V186 EXE. Transport decoding, callback
lookup, UI, timers, item resources and inventory storage are adapters. The
opcode classifier, byte readers, callback, reset, late tick and settlement
decisions execute the matched x86. No game process or network is used.
"""
import gc
import os
import struct
import unittest
from pathlib import Path

from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_ESI, UC_X86_REG_EFLAGS, UC_X86_REG_EIP
from test_vending_settlement_v179 import SettlementLab
import patch_vending_ack_error as patch


class AckLab(SettlementLab):
    def __init__(self, data):
        super().__init__(data)
        assert 0x8BAB80 not in self.hooks, 'classifier must execute native code'
        # Dispatcher failure logging initializes network-owned scratch space.
        # Keep fixture heap objects outside that object, as real allocations are.
        self.heap = 0x24000000
        self.u.mem_map(self.heap, 0x1000000)
        self.reset_calls = self.finish_calls = 0
        self.windows, self.timers, self.callback_args = [], [], []
        self.insertions, self.coin_updates = [], []
        self.slots = {121: self.coin}
        self.has_reward, self.reward_count = False, 0
        self.put(0x56A3798, self.MANAGER)
        self.put(self.MANAGER + 0x8C, self.CONTROL)
        self.put(0x2000C000, 0x1FE64D0, 0)
        for field in (0x100, 0x108):
            obj, vt = self.alloc(32), self.alloc(32)
            self.put(obj, vt)
            self.put(vt + 8, self.STOP + 0x320, self.STOP + 0x320)
            self.put(self.CONTROL + field, obj)
        self.hooks.update({
            0x3816100: lambda: self.timer('mode', 4),
            0x38160C0: lambda: self.timer('clear'),
            0x3816810: lambda: self.timer('range', 8),
            0x38169E0: lambda: self.timer('reset'),
            0x3816960: lambda: self.timer('start'),
            0x3816310: lambda: self.ret(value=7000),
            0x1AD59D0: lambda: self.ret(value=200),
            0x209BBE0: lambda: self.ret(value=10),
            0x37E9F60: lambda: self.ret(value=100),
            0x37E9F40: lambda: self.ret(value=20),
            0x3819940: lambda: self.ret(), 0x2531940: lambda: self.ret(),
            self.STOP + 0x320: lambda: self.ret(4),
            0x32BEA50: self.window_call,
            0x25ECE10: self.find_slot, 0x25EE980: self.remove_slot,
        })

    def hook(self, u, at, size, ctx):
        if at == 0x3305920:
            self.reset_calls += 1
        if at == 0x33044F0:
            self.finish_calls += 1
        if at == 0x1FE64D0:
            self.callback_args.append([self.arg(), self.arg(1) & 255, self.arg(2) & 255])
        super().hook(u, at, size, ctx)

    def timer(self, name, pop=0):
        self.timers.append(name)
        self.ret(pop)

    def window_call(self):
        self.windows.append(tuple(self.arg(i) for i in range(3)))
        self.ret(12, self.window)

    def lookup_slot(self):
        self.lookups.append(self.arg())
        self.ret(4, self.slots.get(self.arg(), 0))

    def find_slot(self):
        slot = next((k for k, v in self.slots.items() if v == self.arg()), -1)
        self.ret(4, slot & 0xFFFFFFFF)

    def remove_slot(self):
        self.effect.append(('remove_coin', self.arg()))
        self.slots.pop(self.arg(), None)
        self.ret(4)

    def set_coin(self):
        self.coin_updates.append(self.arg())
        self.coin_count = self.arg()
        super().set_coin()

    def insert_reward(self):
        args = tuple(self.arg(i) for i in range(4))
        assert args[0] != 0, 'phantom wallet-slot reward'
        self.insertions.append(args)
        self.slots[args[0]] = args[1]
        self.has_reward = True
        self.ret(16, 1)

    def pending(self, state=6):
        self.put(self.CONTROL + 0x174, 99, 50, 77)
        self.put(self.CONTROL + 0x180, 1, state)
        self.put(self.CONTROL + 0x1C0, 0, 0, 4500, 0, 599859, 0)
        self.u.mem_write(self.CONTROL + 0x1EC, b'\x01')

    def receive_wire(self, wire):
        self.put(self.CONTROL + 0x1D8, 0, 0, 0, 0)
        self.u.mem_write(self.PACKET, bytes(wire))
        self.run(0x25239A0, (0, self.PACKET, 0), ecx=self.NETWORK)
        return self.read(0x57F9AD4)

    def receive_native(self, body, opcode=215):
        wire = bytearray(16) + body
        wire[0] = 1
        struct.pack_into('<HI', wire, 1, opcode, len(wire))
        return self.receive_wire(wire)

    def tick(self):
        self.put(self.STACK, self.STOP)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_ESI, self.CONTROL)
        self.u.reg_write(UC_X86_REG_EFLAGS, 2)
        self.u.emu_start(0x3306691, 0x3306757, count=1000000)
        assert self.reg(UC_X86_REG_EIP) == 0x3306757
        assert self.reg(UC_X86_REG_ESP) == self.STACK


def success_body(total=1, remaining=2, reward_slot=79):
    row = bytearray(119)
    struct.pack_into('<HII', row, 0, reward_slot, 7433, total)
    return struct.pack('<BHIIIIIH', 1, 121, remaining, 7433, 1, 0, 0, 1) + b'\0' + row


class VendingAckErrorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get('VENDING_ACK_SOURCE')
        if not source:
            raise unittest.SkipTest('set VENDING_ACK_SOURCE to exact V186')
        cls.before = Path(source).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def tearDown(self):
        gc.collect()

    def assert_no_assets_changed(self, lab):
        self.assertEqual((lab.lookups, lab.insertions, lab.coin_updates,
                          lab.entries, lab.display, lab.effect), ([], [], [], [], [], []))
        self.assertEqual(lab.slots, {121: lab.coin})

    def test_live_failure_contract_red_green(self):
        for data, fixed in ((self.before, False), (self.after, True)):
            lab = AckLab(data)
            lab.pending()
            self.assertEqual(lab.receive_native(b'\0\1'), 0 if fixed else 1)
            self.assertEqual(lab.callback_args, [[215, 0, int(fixed)]])
            self.assertEqual(lab.reset_calls, int(fixed))
            self.assertEqual(lab.messages, [0xBBA] if fixed else [])
            self.assertEqual(lab.read(lab.CONTROL + 0x180) & 255, 0 if fixed else 1)
            self.assertEqual(lab.read(lab.CONTROL + 0x184), 1 if fixed else 6)
            self.assert_no_assets_changed(lab)

    def test_repeated_failure_resets_and_skips_late_empty_settlement(self):
        lab = AckLab(self.after)
        for n, state in enumerate((3, 6, 6), 1):
            lab.pending(state)
            self.assertEqual(lab.receive_native(b'\0\1'), 0)
            self.assertEqual(lab.reset_calls, n)
            for field in (0x174, 0x180, 0x1C0, 0x1C4, 0x1CC, 0x1D0, 0x1D4):
                self.assertEqual(lab.read(lab.CONTROL + field), 0, hex(field))
            self.assertEqual(lab.read(lab.CONTROL + 0x178), 5000)
            lab.tick()
            self.assertEqual(lab.finish_calls, 0)
            self.assertEqual(len(lab.windows), n)
            self.assert_no_assets_changed(lab)

    def test_success_new_merge_and_last_coin_parity(self):
        for existing, total, remaining, slot in ((None, 1, 2, 79), (5, 6, 2, 79), (None, 1, 0, 121)):
            outcomes = []
            for data in (self.before, self.after):
                lab = AckLab(data)
                if existing is not None:
                    lab.slots[slot] = lab.reward
                    lab.has_reward, lab.reward_count = True, existing
                if remaining == 0:
                    lab.coin_count = 1
                self.assertEqual(lab.receive_native(success_body(total, remaining, slot)), 0)
                self.assertEqual(lab.reset_calls, 0)
                self.assertEqual(lab.reward_count, total)
                self.assertEqual(len(lab.display), 1)
                self.assertNotIn(0, lab.slots)
                outcomes.append((lab.slots, lab.effect, lab.entries, lab.display, lab.coin_updates, lab.insertions))
            self.assertEqual(*outcomes)

    def test_rejection_then_success(self):
        lab = AckLab(self.after)
        lab.pending()
        self.assertEqual(lab.receive_native(b'\0\1'), 0)
        self.assert_no_assets_changed(lab)
        lab.pending(3)
        self.assertEqual(lab.receive_native(success_body()), 0)
        self.assertEqual((lab.reset_calls, lab.reward_count, len(lab.display)), (1, 1, 1))
        self.assertEqual(lab.coin_updates, [2])

    def test_error_reader_keeps_other_native_message_codes(self):
        for error, message in ((1, 0xBBA), (4, 255), (17, 260), (22, 1661)):
            lab = AckLab(self.after)
            self.assertEqual(lab.receive_native(bytes([0, error])), 0)
            self.assertEqual(lab.callback_args, [[215, 0, error]])
            self.assertEqual(lab.messages, [message])
            self.assert_no_assets_changed(lab)

    def test_classifier_preserves_other_opcodes(self):
        for data, special in ((self.before, (18, 20, 27, 215)), (self.after, (18, 20, 27))):
            lab = AckLab(data)
            for opcode in list(range(257)) + [32768, 65535]:
                self.assertEqual(lab.run(patch.CLASSIFIER, (opcode,), pop=0) & 255,
                                 int(opcode in special), opcode)
            for opcode in (18, 20, 27):
                # Observe arguments without executing an unrelated callback.
                lab.hooks[0x1FE64D0] = lambda: lab.ret()
                self.assertEqual(lab.receive_native(b'\0\1', opcode), 1)
                self.assertEqual(lab.callback_args[-1], [opcode, 0, 0])

    def test_diff_is_one_classification_byte_and_checksum_only(self):
        start = 0
        for lo, hi in self.receipt['changedRanges']:
            self.assertEqual(memoryview(self.before)[start:lo], memoryview(self.after)[start:lo])
            start = hi
        self.assertEqual(memoryview(self.before)[start:], memoryview(self.after)[start:])
        self.assertEqual(len(self.before), len(self.after))
        opt, _ = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt + 64)[0], patch.pe.checksum(self.after, opt + 64))
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1])

    def test_production_reward_packets(self):
        directory = os.environ.get('DNF_VENDING_NATIVE_EXPORT')
        if not directory:
            self.skipTest('set DNF_VENDING_NATIVE_EXPORT to real PVF Go reply exports')
        import json
        paths = sorted(Path(directory).glob('*.bin'))
        self.assertGreater(len(paths), 100)
        for path in paths:
            body = path.read_bytes()
            meta = json.loads(path.with_suffix('.json').read_text('utf-8'))
            self.assertEqual(len(body), 145)
            slot = struct.unpack_from('<H', body, 26)[0]
            self.assertNotIn(slot, (0, 121))
            lab = AckLab(self.after)
            lab.reward_kind = 2 if meta['kind'] == 'equipment' else 1
            self.assertEqual(lab.receive_native(body), 0)
            self.assertEqual(lab.insertions[0][0], slot)
            self.assertEqual(len(lab.display), 1)
            self.assertEqual(len(lab.entries), 1)
            row = bytes.fromhex(lab.entries[0])
            self.assertEqual(struct.unpack_from('<I', row, 4)[0], meta['itemID'])
            self.assertEqual(struct.unpack_from('<I', row, 8)[0], struct.unpack_from('<I', body, 32)[0])
            self.assertEqual(lab.slots[121], lab.coin)
            del lab
            gc.collect()


if __name__ == '__main__':
    unittest.main(verbosity=2)
