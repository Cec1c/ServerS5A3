"""Verify CMD215 failure and a real nonzero-slot settlement control."""

from pathlib import Path
import gc
import hashlib
import json
import os
import struct
import sys
import unittest

CLIENT_SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CLIENT_SCRIPTS))

from test_vending_settlement_v179 import SettlementLab
from unicorn import UC_HOOK_CODE


FINAL_PROTECTED_SHA256 = (
    "15dfd6a8adf6f4d0f815bb3313642c2a2971d5ff6bd2aefb44830776a522b68e"
)
COIN_SLOT = 121
REWARD_SLOT = 79
REWARD_ITEM_ID = 7433


class RecordingSettlementLab(SettlementLab):
    def __init__(self, data):
        super().__init__(data)
        self.lookups = []
        self.insertions = []
        self.has_reward = False
        self.reward_count = 0
        self.slots = {COIN_SLOT: self.coin}
        self.coin_updates = []
        self.ack_arguments = []
        self.u.hook_add(UC_HOOK_CODE, self.record_ack,
                        begin=0x1fe64d0, end=0x1fe64d0)
        self.hooks[0x25ee980] = self.remove_slot
        self.hooks[0x25ece10] = self.find_slot

    def record_ack(self, _uc, _address, _size, _user_data):
        self.ack_arguments.append(tuple(self.arg(i) for i in range(3)))

    def find_slot(self):
        # Native FindSlot scans the same inventory vector as lookup_slot.
        # Returning -1 for its own stored coin incorrectly bypasses removal.
        pointer = self.arg()
        slot = next((key for key, value in self.slots.items() if value == pointer), -1)
        self.ret(4, slot & 0xffffffff)

    def remove_slot(self):
        slot = self.arg()
        self.effect.append(("remove_coin", slot))
        self.slots.pop(slot, None)
        self.ret(4)

    def receive_native(self, body):
        body = bytes(body)
        # Execute the native ACK dispatcher, including status/error parsing.
        # Only decoded transport and the callback-map lookup are adapters.
        wire = bytearray(16) + body
        wire[0] = 1
        struct.pack_into('<H', wire, 1, 215)
        struct.pack_into('<I', wire, 3, len(wire))
        self.put(0x2000c000, 0x1fe64d0, 0)
        self.put(self.CONTROL + 0x1d8, 0, 0, 0, 0)
        self.u.mem_write(self.PACKET, bytes(wire))
        self.run(0x25239a0, (0, self.PACKET, 0), ecx=self.NETWORK)
        return dict(effects=self.effect, remaining=self.read(0x57f9ad4),
                    reward=(self.read(self.CONTROL + 0x1c0),
                            self.read(self.CONTROL + 0x1c4)))

    def set_coin(self):
        self.coin_updates.append((self.arg(), self.arg(1)))
        self.coin_count = self.arg()
        super().set_coin()

    def lookup_slot(self):
        slot = self.arg()
        self.lookups.append(slot)
        self.ret(4, self.slots.get(slot, 0))

    def insert_reward(self):
        args = tuple(self.arg(i) for i in range(4))
        self.insertions.append(args)
        if args[0] == 0:
            raise AssertionError("settlement inserted a phantom wallet-slot item")
        self.slots[args[0]] = args[1]
        self.has_reward = True
        self.ret(16, 1)


class VendingFullPageContract(unittest.TestCase):
    def tearDown(self):
        gc.collect()

    @classmethod
    def setUpClass(cls):
        path = os.environ.get("VENDING_NATIVE_EXE")
        if not path:
            raise unittest.SkipTest("set VENDING_NATIVE_EXE to final protected client")
        cls.data = Path(path).read_bytes()
        digest = hashlib.sha256(cls.data).hexdigest()
        if digest != FINAL_PROTECTED_SHA256:
            raise ValueError(
                "unexpected VENDING_NATIVE_EXE sha256=" + digest
            )

    def success_body(self):
        row = bytearray(119)
        struct.pack_into(
            "<HII", row, 0, REWARD_SLOT, REWARD_ITEM_ID, 1
        )
        return (
            struct.pack(
                "<BHIIIIIH",
                1,
                COIN_SLOT,
                2,
                REWARD_ITEM_ID,
                1,
                0,
                0,
                1,
            )
            + b"\0"
            + row
        )

    def test_real_nonzero_slot_is_inserted_and_displayed(self):
        probe = RecordingSettlementLab(self.data)
        result = probe.receive_native(self.success_body())
        self.assertEqual(result["remaining"], 0)
        self.assertIn(COIN_SLOT, probe.lookups)
        self.assertIn(REWARD_SLOT, probe.lookups)
        self.assertEqual(len(probe.insertions), 1)
        self.assertEqual(probe.insertions[0][0], REWARD_SLOT)
        self.assertEqual(probe.insertions[0][2:], (1, 1))
        self.assertEqual(len(probe.entries), 1)
        row = bytes.fromhex(probe.entries[0])
        self.assertEqual(struct.unpack_from("<I", row, 4)[0], REWARD_ITEM_ID)
        self.assertEqual(len(probe.display), 1)

    def test_existing_reward_keeps_its_real_slot(self):
        probe = RecordingSettlementLab(self.data)
        probe.slots[REWARD_SLOT] = probe.reward
        probe.has_reward = True
        probe.reward_count = 5
        body = bytearray(self.success_body())
        struct.pack_into('<I', body, 32, 6)
        result = probe.receive_native(body)
        self.assertEqual(result['remaining'], 0)
        self.assertEqual(probe.reward_count, 6)
        self.assertEqual(probe.slots[COIN_SLOT], probe.coin)
        self.assertNotIn(0, probe.slots)
        self.assertEqual(len(probe.display), 1)

    def test_actual_production_reward_packets(self):
        directory = os.environ.get('DNF_VENDING_NATIVE_EXPORT')
        if not directory:
            self.skipTest('set DNF_VENDING_NATIVE_EXPORT for actual Go replies')
        paths = sorted(Path(directory).glob('*.bin'))
        self.assertGreater(len(paths), 100)
        for path in paths:
            with self.subTest(packet=path.name):
                body = path.read_bytes()
                meta = json.loads(path.with_suffix('.json').read_text('utf8'))
                self.assertEqual(len(body), 145)
                slot = struct.unpack_from('<H', body, 26)[0]
                self.assertNotIn(slot, (0, COIN_SLOT))
                probe = RecordingSettlementLab(self.data)
                probe.reward_kind = 2 if meta['kind'] == 'equipment' else 1
                result = probe.receive_native(body)
                self.assertEqual(result['remaining'], 0)
                self.assertEqual(len(probe.insertions), 1)
                self.assertEqual(probe.insertions[0][0], slot)
                self.assertEqual(probe.slots[COIN_SLOT], probe.coin)
                self.assertNotIn(0, probe.slots)
                self.assertEqual(len(probe.entries), 1)
                row = bytes.fromhex(probe.entries[0])
                self.assertEqual(struct.unpack_from('<I', row, 4)[0], meta['itemID'])
                self.assertEqual(struct.unpack_from('<I', row, 8)[0], struct.unpack_from('<I', body, 32)[0])
                self.assertEqual(len(probe.display), 1)
                del probe
                gc.collect()

    def test_last_coin_slot_can_be_reused_by_the_reward(self):
        probe = RecordingSettlementLab(self.data)
        probe.coin_count = 1
        body = bytearray(self.success_body())
        struct.pack_into('<I', body, 3, 0)
        struct.pack_into('<H', body, 26, COIN_SLOT)
        result = probe.receive_native(body)
        self.assertEqual(result['remaining'], 0)
        self.assertEqual(probe.slots.get(COIN_SLOT), probe.reward)
        self.assertEqual(len(probe.insertions), 1)
        self.assertEqual(probe.insertions[0][0], COIN_SLOT)
        self.assertEqual(len(probe.display), 1)
        self.assertEqual(len(probe.entries), 1)
        self.assertNotIn(0, probe.slots)
        self.assertIn(('remove_coin', COIN_SLOT), probe.effect)
        self.assertEqual(probe.coin_updates, [])

    def test_protocol_failure_has_no_slot_zero_or_phantom_insert(self):
        probe = RecordingSettlementLab(self.data)
        # The matched client's real classifier exempts CMD215 from the
        # generic error-byte read. Do not mock it to force full consumption.
        self.assertEqual(probe.run(0x8bab80, (215,), pop=0) & 255, 1)
        result = probe.receive_native(bytes([0, 1]))
        self.assertEqual(result["remaining"], 1)
        self.assertEqual(probe.ack_arguments, [(215, 0, 0)])
        self.assertNotIn(0, probe.lookups)
        self.assertEqual(probe.insertions, [])
        self.assertEqual(probe.entries, [])
        self.assertEqual(probe.display, [])
        self.assertEqual(probe.coin_updates, [])
        self.assertEqual(probe.slots, {COIN_SLOT: probe.coin})
        self.assertNotIn(("remove_coin", COIN_SLOT), probe.effect)


if __name__ == "__main__":
    unittest.main(verbosity=2)
