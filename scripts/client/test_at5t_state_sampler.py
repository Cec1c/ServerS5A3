from __future__ import annotations

import ctypes
import json
import os
from pathlib import Path
import struct
import tempfile
import time
import unittest
from unittest import mock

import at5t_state_sampler as sampler


AT5T_NATIVE_ACTION_CASES = (
    (0, 4294865573, 1923445413, 0),
    (0, 4294560572, 3409759037, 1),
    (0, 4262207237, 520429323, 14),
    (0, 4293536157, 698165641, 20),
    (0, 4293182237, 3273507595, 22),
    (0, 4294885114, 1090863365, -1),
    (0, 1721804550, 3040014718, 305419896),
    (0, 2147390109, 312316573, -2147483648),
    (0, 2147229237, 1640686026, 2147483647),
    (4, 1754622101, 1754622101, 0),
    (4, 2746852280, 2746786745, 1),
    (4, 2313062864, 2312145374, 14),
    (4, 1904701843, 1905488263, 20),
    (4, 2952441836, 2951524346, 22),
    (4, 3153771514, 1141195781, -1),
    (4, 3517027813, 2278671261, 305419896),
    (4, 1053146821, 1053146821, -2147483648),
    (4, 713337476, 3581629819, 2147483647),
    (8, 876164153, 876164153, 0),
    (8, 3445083479, 3445083478, 1),
    (8, 3589002731, 3589002725, 14),
    (8, 2858920551, 2858920563, 20),
    (8, 855978757, 855978771, 22),
    (8, 835269064, 3459763767, -1),
    (8, 26673655, 331569039, 305419896),
    (8, 2741347173, 593863525, -2147483648),
    (8, 431397303, 3863504456, 2147483647),
    (12, 56925, 3730693725, 0),
    (12, 225941, 1922396820, 1),
    (12, 8291256, 2209907638, 14),
    (12, 1317322, 432675294, 20),
    (12, 2768523, 1049312925, 22),
    (12, 1974704, 3729776207, -1),
    (12, 1803326955, 2279588755, 305419896),
    (12, 2147506771, 3662895699, -2147483648),
    (12, 2147655443, 3773587692, 2147483647),
    (1, 2193457853, 2193457853, 0),
    (1, 2130804480, 2130738945, 1),
    (1, 3985370501, 3984977291, 14),
    (1, 39256643, 37945943, 20),
    (1, 2359135371, 2357955741, 22),
    (1, 789958891, 789851924, -1),
    (1, 178020912, 1879323720, 305419896),
    (1, 1483987059, 1483987059, -2147483648),
    (1, 30441006, 30507473, 2147483647),
)


class SparseMemory:
    def __init__(self) -> None:
        self.segments: list[tuple[int, bytearray]] = []

    def add(self, address: int, size: int, fill: int = 0) -> None:
        self.segments.append((address, bytearray([fill]) * size))

    def _locate(self, address: int, size: int) -> tuple[bytearray, int]:
        for start, data in self.segments:
            if start <= address and address + size <= start + len(data):
                return data, address - start
        raise sampler.MemoryReadError(address, size, "unmapped_test_range")

    def read(self, address: int, size: int) -> bytes:
        if address < 0 or size < 0:
            raise sampler.MemoryReadError(address, size, "invalid_test_range")
        data, offset = self._locate(address, size)
        return bytes(data[offset : offset + size])

    def write(self, address: int, data: bytes) -> None:
        target, offset = self._locate(address, len(data))
        target[offset : offset + len(data)] = data

    def u8(self, address: int, value: int) -> None:
        self.write(address, bytes([value & 0xFF]))

    def u32(self, address: int, value: int) -> None:
        self.write(address, struct.pack("<I", value & sampler.MAX_U32))


class UnstableKeyMemory(SparseMemory):
    def __init__(self, key_address: int) -> None:
        super().__init__()
        self.key_address = key_address
        self.key_reads = 0

    def read(self, address: int, size: int) -> bytes:
        data = super().read(address, size)
        if address == self.key_address and size == 4:
            self.key_reads += 1
            if self.key_reads == 2:
                changed = struct.unpack("<I", data)[0] ^ 1
                return struct.pack("<I", changed)
        return data


class MutatingReadMemory:
    def __init__(
        self,
        backing: SparseMemory,
        trigger_address: int,
        field_address: int,
        replacement_encoded: int,
    ) -> None:
        self.backing = backing
        self.trigger_address = trigger_address
        self.field_address = field_address
        self.replacement_encoded = replacement_encoded
        self.fired = False

    def read(self, address: int, size: int) -> bytes:
        data = self.backing.read(address, size)
        if not self.fired and address == self.trigger_address:
            self.fired = True
            self.backing.u32(self.field_address + 4, self.replacement_encoded)
        return data


def install_protection(
    memory: SparseMemory,
    manager: int,
    field_address: int,
    encoded: int = 0x12345678,
) -> dict[str, int]:
    handle = 0x00010002
    key_low = 0xABCD
    plain = encoded ^ (key_low * 0x10001)
    plain_low = plain & 0xFFFF
    plain_high = (plain >> 16) & 0xFFFF
    variant = field_address & 0xF
    if variant == 0:
        key_high = ((plain_high - key_low + plain_low) & 0xFFFF) ^ key_low
    elif variant == 4:
        key_high = ((plain_low - plain_high) & 0xFFFF) ^ key_low
    elif variant == 8:
        key_high = ((plain_high * plain_low) & 0xFFFF) ^ key_low
    elif variant == 12:
        key_high = (
            (plain_high + plain_low + key_low) & 0xFFFF
        ) ^ key_low
    else:
        key_high = ((plain_high + plain_low) & 0xFFFF) ^ key_low
    key = (key_high << 16) | key_low
    bank = manager + 0x24 + (handle >> 16) * 4
    key_address = bank + 0x2114 + (handle & 0xFFFF) * 4
    memory.u32(manager + 0x24 + 4, bank)
    memory.u32(key_address, key)
    memory.u32(field_address, handle)
    memory.u32(field_address + 4, encoded)
    return {
        "handle": handle,
        "encoded": encoded,
        "key": key,
        "key_address": key_address,
        "action": sampler.i32(plain),
    }


def build_state_memory(
    *,
    vehicle_count: int = 2,
    open_override: bool = True,
) -> tuple[SparseMemory, dict[str, int], dict[str, int]]:
    memory = SparseMemory()
    addresses = {
        "primary": 0x01000000,
        "override": 0x01000004,
        "virtual_character": 0x01000008,
        "private_input_fallback": 0x60000000,
        "key_manager": 0x01000010,
    }
    actor = 0x20000000
    virtual_character = 0x30000000
    scene = 0x40000000
    scene_elements = 0x41000000
    descriptor = 0x50000000
    private_input = 0x60000000
    shared_rows = 0x61000000
    hardware_rows = 0x62000000
    pedestrian = 0x31000000
    manager = 0x0A000000

    for address in (addresses["primary"], addresses["override"], addresses["virtual_character"], addresses["key_manager"]):
        memory.add(address, 4)
    memory.u32(addresses["primary"], actor)
    memory.u32(addresses["override"], actor if open_override else 0)
    memory.u32(addresses["virtual_character"], virtual_character)
    memory.u32(addresses["key_manager"], manager)
    memory.add(manager, 0x3000)

    memory.add(actor, 0x8000)
    memory.add(virtual_character, 0x8000)
    memory.add(pedestrian, 0x8000)
    vtable = 0x70000000
    vtable_entries = {
        "eligibility": 0x74000000,
        "normal_command": 0x74000001,
        "action_set": 0x74000002,
    }
    memory.u32(actor, vtable)
    memory.add(vtable, 0x1000)
    memory.u32(vtable + 0xCAC, vtable_entries["eligibility"])
    memory.u32(vtable + 0xE0C, vtable_entries["normal_command"])
    memory.u32(vtable + 0x428, vtable_entries["action_set"])
    memory.u32(actor + sampler.ACTOR_SCENE, scene)
    memory.u32(actor + sampler.ACTOR_DESCRIPTOR, descriptor)
    memory.u32(actor + sampler.ACTOR_PRIVATE_INPUT, 0)
    memory.u32(actor + sampler.ACTOR_JOYSTICK, 0x71000000)
    memory.u32(actor + sampler.ACTOR_RIDE, 0x72000000)
    memory.u8(actor + sampler.ACTOR_RIDE_LOCK, 1)

    memory.add(scene, 0x100)
    vehicles = [0x08000000 + index * 0x10000 for index in range(vehicle_count)]
    candidates = [actor, pedestrian, virtual_character, *vehicles]
    memory.add(scene_elements, len(candidates) * 4)
    for index, candidate in enumerate(candidates):
        memory.u32(scene_elements + index * 4, candidate)
    memory.u32(scene + 0x0C, scene_elements)
    memory.u32(scene + 0x10, scene_elements + len(candidates) * 4)
    for index, vehicle in enumerate(vehicles):
        memory.add(vehicle, 0x8000)
        memory.u32(vehicle, 0x73000000 + index * 0x100)
        memory.u32(vehicle + 0x400, sampler.VEHICLE_MARKER)
        memory.u32(vehicle + 0x908, actor)
        memory.u32(vehicle + 0x5C84, actor)
        memory.u32(vehicle + 0x1D8, 2)
        memory.u8(vehicle + 0x5F64, 1)
        memory.u32(vehicle + 0x5F60, 7)

    memory.add(descriptor, 0x100)
    memory.u8(descriptor + 0x30, 1)
    memory.u32(descriptor + 0x2C, 5)

    memory.add(private_input, 0x100)
    memory.add(shared_rows, 3 * 8)
    memory.u32(shared_rows + 0, descriptor)
    memory.u32(shared_rows + 16, descriptor)
    memory.u32(private_input + 0x70, shared_rows)
    memory.u32(private_input + 0x74, shared_rows + 3 * 8)

    memory.add(hardware_rows, 4 * 24)
    memory.u32(hardware_rows + 0 * 24, 1)
    memory.u32(hardware_rows + 1 * 24, 4)
    memory.u8(hardware_rows + 1 * 24 + 0x12, 1)
    memory.u8(hardware_rows + 1 * 24 + 0x13, 1)
    memory.u32(hardware_rows + 2 * 24, 3)
    memory.u32(private_input + 0x38, hardware_rows)
    memory.u32(private_input + 0x3C, hardware_rows + 4 * 24)

    actor_protection = install_protection(
        memory, manager, actor + sampler.ACTOR_PROTECTED_FIELD
    )
    vehicle_protection = [
        install_protection(
            memory, manager, vehicle + sampler.ACTOR_PROTECTED_FIELD
        )
        for vehicle in vehicles
    ]
    expected = {
        **actor_protection,
        "actor": actor,
        "virtual_character": virtual_character,
        "descriptor": descriptor,
        "private_input": private_input,
        "vtable_entries": vtable_entries,
        "vehicle_addresses": vehicles,
        "vehicle_actions": [item["action"] for item in vehicle_protection],
        "manager": manager,
    }
    return memory, addresses, expected


def iter_dict_keys(value: object):
    if isinstance(value, dict):
        for key, child in value.items():
            yield str(key)
            yield from iter_dict_keys(child)
    elif isinstance(value, list):
        for child in value:
            yield from iter_dict_keys(child)


class StateParserTests(unittest.TestCase):
    def test_invalid_scene_ranges_remain_errors_on_repeat(self) -> None:
        for end in (0x41000006, 0x40FFFFFC):
            memory = SparseMemory()
            scene = 0x40000000
            memory.add(scene, 0x20)
            memory.u32(scene + 0x0C, 0x41000000)
            memory.u32(scene + 0x10, end)
            cache = None
            for _ in range(2):
                ctx = sampler.ReadContext(memory)
                parsed, cache = sampler.sample_scene(ctx, scene, None, None, cache)
                self.assertEqual(parsed["elements"]["scanned_elements"], 0)
                self.assertIn("scene.elements.bounds", {x["field"] for x in ctx.errors})
                self.assertIsNone(cache)

    def test_unreadable_registration_is_unknown(self) -> None:
        memory, _, expected = build_state_memory()
        memory.u32(expected["private_input"] + 0x70, 0x66000000)
        memory.u32(expected["private_input"] + 0x74, 0x66000008)
        ctx = sampler.ReadContext(memory)
        result = sampler.sample_input(ctx, expected["private_input"], expected["descriptor"])
        self.assertIsNone(result["registered"])
        self.assertFalse(result["registration_complete"])
        self.assertTrue(ctx.errors)

    def test_partial_scene_keeps_found_vehicles_and_retries(self) -> None:
        memory, addresses, expected = build_state_memory()
        scene = sampler.read_u32_raw(memory, expected["actor"] + sampler.ACTOR_SCENE)
        begin = sampler.read_u32_raw(memory, scene + 0x0C)
        memory.u32(begin, 0x66000000)
        cache = None
        for _ in range(2):
            result, cache = sampler.sample_state(memory, addresses, scene_cache=cache)
            self.assertEqual(len(result["scene"]["vehicles"]), 2)
            self.assertFalse(result["scene"]["cache_hit"])
            self.assertIn("scene.elements[0].marker", {x["field"] for x in result["errors"]})

    def test_preferred_address_translation(self) -> None:
        addresses = sampler.preferred_address_map(0x00400000)
        self.assertEqual(addresses["primary"], sampler.GLOBAL_PRIMARY)
        self.assertEqual(addresses["key_manager"], sampler.GLOBAL_KEY_MANAGER)
        shifted = sampler.preferred_address_map(0x00410000)
        self.assertEqual(shifted["primary"], sampler.GLOBAL_PRIMARY + 0x10000)

    def test_full_state_and_actor_selection(self) -> None:
        memory, addresses, expected = build_state_memory()
        sample, cache = sampler.sample_state(memory, addresses)

        self.assertEqual(sample["errors"], [])
        self.assertIsNotNone(cache)
        actor = sample["actor"]
        self.assertEqual(actor["address"], expected["actor"])
        self.assertEqual(actor["source"], "override")
        self.assertEqual(actor["private_input"], expected["private_input"])
        self.assertTrue(actor["private_input_used_fallback"])
        self.assertEqual(actor["ride"], 0x72000000)
        self.assertEqual(actor["ride_lock"], 1)
        self.assertEqual(
            actor["vtable_entries"], expected["vtable_entries"]
        )
        self.assertEqual(
            actor["protected_action"]["action"], expected["action"]
        )
        self.assertTrue(actor["protected_action"]["stable"])
        self.assertTrue(actor["protected_action"]["integrity"])

        self.assertEqual(sample["descriptor"]["enable"], 1)
        self.assertEqual(sample["descriptor"]["state"], 5)
        self.assertTrue(sample["input"]["registered"])
        self.assertEqual(sample["input"]["registration_rows"], [0, 2])
        self.assertEqual(
            sample["input"]["logical4"],
            [{"row_index": 1, "down": 1, "fresh": 1}],
        )

        scene = sample["scene"]
        self.assertFalse(scene["cache_hit"])
        self.assertEqual(
            scene["role_matches"][0],
            {
                "scene_index": 0,
                "candidate": expected["actor"],
                "roles": ["actor"],
            },
        )
        self.assertEqual(scene["role_matches"][1]["scene_index"], 2)
        self.assertEqual(
            scene["role_matches"][1]["roles"], ["virtual_character"]
        )
        self.assertEqual(scene["vehicle_total"], 2)
        self.assertEqual(len(scene["vehicles"]), 2)
        self.assertEqual(
            [item["protected_action"]["action"] for item in scene["vehicles"]],
            expected["vehicle_actions"],
        )

    def test_primary_fallback_and_empty_private_input(self) -> None:
        memory, addresses, expected = build_state_memory(open_override=False)
        memory.u32(expected["actor"] + sampler.ACTOR_PRIVATE_INPUT, 0)
        sample, _ = sampler.sample_state(memory, addresses)
        self.assertEqual(sample["actor"]["source"], "primary")
        self.assertEqual(sample["actor"]["address"], expected["actor"])
        self.assertTrue(sample["actor"]["private_input_used_fallback"])
        self.assertTrue(sample["input"]["registered"])

    def test_override_read_failure_does_not_fall_back_to_primary(self) -> None:
        memory, addresses, _ = build_state_memory()
        failed_addresses = dict(addresses)
        failed_addresses["override"] = sampler.MAX_U32
        sample, _ = sampler.sample_state(memory, failed_addresses)
        self.assertIsNone(sample["actor"]["address"])
        self.assertEqual(sample["actor"]["source"], "override_unavailable")
        self.assertIn(
            "globals.override",
            {item["field"] for item in sample["errors"]},
        )

    def test_scene_cache_hit_and_pointer_change_refresh(self) -> None:
        memory, addresses, _ = build_state_memory()
        first, cache = sampler.sample_state(memory, addresses)
        second, cache = sampler.sample_state(
            memory, addresses, scene_cache=cache
        )
        self.assertFalse(first["scene"]["cache_hit"])
        self.assertTrue(second["scene"]["cache_hit"])

        scene_begin = second["scene"]["elements"]["begin"]
        memory.u32(scene_begin, 0x3FFFFFF0)
        third, _ = sampler.sample_state(memory, addresses, scene_cache=cache)
        self.assertFalse(third["scene"]["cache_hit"])

    def test_vehicle_limit_is_eight_but_total_is_retained(self) -> None:
        memory, addresses, _ = build_state_memory(vehicle_count=10)
        sample, _ = sampler.sample_state(memory, addresses)
        scene = sample["scene"]
        self.assertEqual(scene["vehicle_total"], 10)
        self.assertTrue(scene["vehicles_truncated"])
        self.assertEqual(len(scene["vehicles"]), sampler.MAX_VEHICLES)
        self.assertEqual(
            [item["scene_index"] for item in scene["vehicles"]],
            [3, 4, 5, 6, 7, 8, 9, 10],
        )

    def test_scene_and_input_array_bounds(self) -> None:
        memory = SparseMemory()
        scene = 0x40000000
        scene_elements = 0x41000000
        candidate = 0x42000000
        scene_count = sampler.MAX_SCENE_ELEMENTS + 1
        memory.add(scene, 0x20)
        memory.add(scene_elements, scene_count * 4)
        memory.add(candidate, 0x500)
        for index in range(scene_count):
            memory.u32(scene_elements + index * 4, candidate)
        memory.u32(scene + 0x0C, scene_elements)
        memory.u32(scene + 0x10, scene_elements + scene_count * 4)
        ctx = sampler.ReadContext(memory)
        parsed_scene, _ = sampler.sample_scene(ctx, scene, None, None)
        self.assertEqual(
            parsed_scene["elements"]["scanned_elements"],
            sampler.MAX_SCENE_ELEMENTS,
        )
        self.assertTrue(parsed_scene["elements"]["truncated"])
        self.assertEqual(
            parsed_scene["elements"]["total_elements"], scene_count
        )

        input_address = 0x60000000
        shared_rows = 0x61000000
        hardware_rows = 0x62000000
        shared_count = sampler.MAX_SHARED_POINTER_ROWS + 1
        hardware_count = sampler.MAX_HARDWARE_ROWS + 1
        memory.add(input_address, 0x100)
        memory.add(shared_rows, shared_count * 8)
        memory.add(hardware_rows, hardware_count * 24)
        memory.u32(input_address + 0x70, shared_rows)
        memory.u32(input_address + 0x74, shared_rows + shared_count * 8)
        memory.u32(input_address + 0x38, hardware_rows)
        memory.u32(input_address + 0x3C, hardware_rows + hardware_count * 24)
        parsed_input = sampler.sample_input(
            sampler.ReadContext(memory), input_address, None
        )
        self.assertEqual(
            parsed_input["shared_pointer_rows"]["scanned_elements"],
            sampler.MAX_SHARED_POINTER_ROWS,
        )
        self.assertTrue(parsed_input["shared_pointer_rows"]["truncated"])
        self.assertEqual(
            parsed_input["hardware_rows"]["scanned_elements"],
            sampler.MAX_HARDWARE_ROWS,
        )
        self.assertTrue(parsed_input["hardware_rows"]["truncated"])

    def test_bad_pointer_and_misaligned_range_preserve_errors(self) -> None:
        memory, addresses, expected = build_state_memory()
        actor = expected["actor"]
        memory.u32(actor + sampler.ACTOR_SCENE, sampler.MAX_U32)
        memory.u32(actor + sampler.ACTOR_DESCRIPTOR, sampler.MAX_U32 - 1)
        memory.u32(actor + sampler.ACTOR_PRIVATE_INPUT, sampler.MAX_U32 - 2)
        sample, _ = sampler.sample_state(memory, addresses)
        self.assertIsNone(sample["descriptor"]["enable"])
        self.assertIsNone(sample["descriptor"]["state"])
        self.assertIsNone(sample["scene"]["elements"]["begin"])
        self.assertIsNone(sample["scene"]["elements"]["end"])
        self.assertEqual(sample["input"]["address"], sampler.MAX_U32 - 2)
        fields = {item["field"] for item in sample["errors"]}
        self.assertIn("descriptor.enable", fields)
        self.assertIn("scene.elements.begin", fields)
        self.assertIn("input.shared_pointer_rows.begin", fields)

        memory2 = SparseMemory()
        owner = 0x70000000
        memory2.add(owner, 0x100)
        memory2.u32(owner + 0x0C, 0x71000000)
        memory2.u32(owner + 0x10, 0x71000006)
        ctx = sampler.ReadContext(memory2)
        header = sampler._range_header(
            ctx,
            "test.range",
            owner,
            0x0C,
            0x10,
            4,
            10,
        )
        self.assertTrue(header["misaligned"])
        self.assertIn(
            "test.range.bounds", {item["field"] for item in ctx.errors}
        )

    def test_protected_action_unstable_key(self) -> None:
        base, addresses, expected = build_state_memory(vehicle_count=0)
        memory = UnstableKeyMemory(expected["key_address"])
        memory.segments = base.segments
        sample, _ = sampler.sample_state(memory, addresses)
        protection = sample["actor"]["protected_action"]
        self.assertFalse(protection["stable"])
        self.assertTrue(protection["integrity"])
        self.assertEqual(protection["action"], expected["action"])
        self.assertEqual(memory.key_reads, 2)
        self.assertIn(
            "protection.stable",
            {item["field"] for item in sample["errors"]},
        )

    def test_protected_action_checksum_mismatch(self) -> None:
        memory, addresses, expected = build_state_memory()
        field = expected["actor"] + sampler.ACTOR_PROTECTED_FIELD
        memory.u32(field + 4, expected["encoded"] ^ 1)
        sample, _ = sampler.sample_state(memory, addresses)
        protection = sample["actor"]["protected_action"]
        self.assertTrue(protection["stable"])
        self.assertFalse(protection["integrity"])
        self.assertIn(
            "protection.integrity",
            {item["field"] for item in sample["errors"]},
        )

    def test_native_action_fixtures(self) -> None:
        manager = 0x11200000
        bank = 0x11300000
        handle = 7
        self.assertEqual(len(AT5T_NATIVE_ACTION_CASES), 45)
        for alignment, key, encoded, expected_action in AT5T_NATIVE_ACTION_CASES:
            with self.subTest(
                alignment=alignment,
                encoded=encoded,
                expected_action=expected_action,
            ):
                memory = SparseMemory()
                field = 0x110032D0 + alignment
                memory.add(sampler.GLOBAL_KEY_MANAGER, 4)
                memory.add(field, 8)
                memory.add(manager, 0x1000)
                memory.add(bank, 0x3000)
                memory.u32(sampler.GLOBAL_KEY_MANAGER, manager)
                memory.u32(manager + 0x24 + (handle >> 16) * 4, bank)
                memory.u32(
                    bank + 0x2114 + (handle & 0xFFFF) * 4, key
                )
                memory.u32(field, handle)
                memory.u32(field + 4, encoded)
                ctx = sampler.ReadContext(memory)
                ctx._key_manager_address = sampler.GLOBAL_KEY_MANAGER
                result = sampler.decode_protected_action(ctx, field)
                self.assertEqual(result["action"], expected_action)
                self.assertTrue(result["integrity"], ctx.errors)
                self.assertTrue(result["stable"], ctx.errors)
                self.assertEqual(ctx.errors, [])

    def test_field_change_during_key_lookup_is_unstable(self) -> None:
        memory, addresses, expected = build_state_memory(vehicle_count=0)
        field = expected["actor"] + sampler.ACTOR_PROTECTED_FIELD
        mutating = MutatingReadMemory(
            memory,
            expected["key_address"],
            field,
            expected["encoded"] ^ 1,
        )
        ctx = sampler.ReadContext(mutating)
        ctx._key_manager_address = addresses["key_manager"]
        result = sampler.decode_protected_action(ctx, field)
        self.assertTrue(mutating.fired)
        self.assertFalse(result["stable"])
        self.assertEqual(result["action"], expected["action"])
        self.assertTrue(result["integrity"])
        self.assertIn(
            "protection.stable",
            {item["field"] for item in ctx.errors},
        )

    def test_output_has_no_key_or_private_chat_fields(self) -> None:
        memory, addresses, _ = build_state_memory()
        sample, _ = sampler.sample_state(memory, addresses)
        rendered = json.dumps(sample)
        keys = set(iter_dict_keys(sample))
        forbidden = {
            "key",
            "key_manager",
            "manager",
            "bank",
            "password",
            "chat",
            "account",
            "account_name",
        }
        self.assertTrue(keys.isdisjoint(forbidden))
        self.assertNotIn("密钥表", rendered)


class ImageIdentityTests(unittest.TestCase):
    def test_known_images_are_classified(self) -> None:
        for digest, (version, size) in sampler.ACCEPTED_IMAGES.items():
            result = sampler.classify_image_identity(digest, size)
            self.assertEqual(result.version, version)
            self.assertEqual(result.size, size)

    def test_hash_and_size_mismatch_are_rejected(self) -> None:
        valid_size = next(iter(sampler.ACCEPTED_IMAGES.values()))[1]
        with self.assertRaisesRegex(sampler.SamplerError, "image_hash_mismatch"):
            sampler.classify_image_identity("00" * 32, valid_size)
        with self.assertRaisesRegex(sampler.SamplerError, "image_size_mismatch"):
            sampler.classify_image_identity(next(iter(sampler.ACCEPTED_IMAGES)), 4)

    def test_pe32_header_check(self) -> None:
        image = bytearray(0x100)
        image[:2] = b"MZ"
        struct.pack_into("<I", image, 0x3C, 0x40)
        image[0x40:0x44] = b"PE\0\0"
        struct.pack_into("<H", image, 0x44, 0x014C)
        struct.pack_into("<H", image, 0x40 + 20, 0xE0)
        struct.pack_into("<H", image, 0x40 + 24, 0x10B)
        struct.pack_into("<I", image, 0x40 + 24 + 28, 0x400000)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fake.exe"
            path.write_bytes(image)
            identity = sampler.read_pe32_identity(path)
        self.assertEqual(identity.machine, 0x014C)
        self.assertEqual(identity.optional_magic, 0x10B)
        self.assertEqual(identity.image_base, sampler.PREFERRED_IMAGE_BASE)


class CommandSafetyTests(unittest.TestCase):
    def test_identity_check_time_does_not_consume_sampling_duration(self) -> None:
        clock = [0.0]

        class SlowIdentityProcess:
            def __init__(self, pid):
                pass

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def image_path(self):
                clock[0] += 2.0
                return Path("DNF.exe")

            def module_base(self, path):
                return 0x400000

            def has_exited(self):
                return True

        image = sampler.ImageVersion("V135", 111812608, next(iter(sampler.ACCEPTED_IMAGES)))
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            sampler, "WindowsProcess", SlowIdentityProcess
        ), mock.patch.object(sampler, "validate_image_file", return_value=image), mock.patch.object(
            sampler.time, "monotonic", side_effect=lambda: clock[0]
        ):
            result = sampler.run_sampler(65000, Path(directory) / "out.jsonl", seconds=1)
        self.assertEqual(result["stop_reason"], "process_exit")

    def test_end_to_end_jsonl_and_process_exit_summary(self) -> None:
        memory, addresses, expected = build_state_memory()

        class FakeProcess:
            def __init__(self, pid: int) -> None:
                self.pid = pid
                self.exit_checks = 0

            def __enter__(self):
                return self

            def __exit__(self, exc_type, exc, traceback):
                return None

            def image_path(self) -> Path:
                return Path("C:/fixture/DNF.exe")

            def module_base(self, image_path: Path) -> int:
                self.observed_image = image_path
                return 0x400000

            def read(self, address: int, size: int) -> bytes:
                return memory.read(address, size)

            def has_exited(self) -> bool:
                self.exit_checks += 1
                return self.exit_checks >= 2

        version = next(iter(sampler.ACCEPTED_IMAGES.values()))
        image = sampler.ImageVersion(
            version=version[0],
            size=version[1],
            sha256=next(iter(sampler.ACCEPTED_IMAGES)),
        )
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "sample.jsonl"
            with (
                mock.patch.object(sampler, "WindowsProcess", FakeProcess),
                mock.patch.object(
                    sampler, "validate_image_file", return_value=image
                ),
                mock.patch.object(
                    sampler,
                    "preferred_address_map",
                    return_value=addresses,
                ),
            ):
                summary = sampler.run_sampler(
                    65000,
                    output,
                    seconds=1,
                    interval_ms=1,
                )
            records = [
                json.loads(line)
                for line in output.read_text(encoding="utf-8").splitlines()
            ]

        self.assertEqual(
            [record["record_type"] for record in records],
            ["header", "sample", "summary"],
        )
        self.assertEqual(records[0]["process"]["sha256"], image.sha256)
        self.assertEqual(records[0]["process"]["exe_name"], "DNF.exe")
        self.assertNotIn("exe_path", records[0]["process"])
        self.assertEqual(records[0]["module"]["base"], 0x400000)
        self.assertEqual(records[1]["actor"]["address"], expected["actor"])
        self.assertEqual(records[2]["stop_reason"], "process_exit")
        self.assertEqual(summary["sample_count"], 1)
        self.assertEqual(summary["fully_read_sample_count"], 1)
        self.assertEqual(records[2]["sample_count"], 1)

    def test_existing_output_is_rejected_before_process_access(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "sample.jsonl"
            output.write_text("keep", encoding="utf-8")
            with self.assertRaisesRegex(
                sampler.SamplerError, "refusing_to_overwrite_output"
            ):
                sampler.run_sampler(
                    65000,
                    output,
                    seconds=0.01,
                    interval_ms=1,
                )
            self.assertEqual(output.read_text(encoding="utf-8"), "keep")

    def test_pid_is_required(self) -> None:
        with self.assertRaises(SystemExit):
            sampler.build_arg_parser().parse_args([])

    def test_requested_process_access_is_exact(self) -> None:
        self.assertEqual(
            sampler.PROCESS_ACCESS,
            sampler.PROCESS_QUERY_LIMITED_INFORMATION
            | sampler.PROCESS_VM_READ,
        )
        self.assertEqual(sampler.PROCESS_QUERY_LIMITED_INFORMATION, 0x1000)
        self.assertEqual(sampler.PROCESS_VM_READ, 0x10)

    def test_source_contains_only_read_process_primitive(self) -> None:
        source_path = Path(__file__).with_name("at5t_state_sampler.py")
        source = source_path.read_text(encoding="utf-8")
        forbidden = (
            "Write" + "ProcessMemory",
            "Virtual" + "AllocEx",
            "Create" + "RemoteThread",
            "SetWindows" + "HookEx",
            "GetAsync" + "KeyState",
            "keybd_" + "event",
            "Send" + "Input",
        )
        for name in forbidden:
            self.assertNotIn(name, source)


@unittest.skipUnless(os.name == "nt", "Windows process API test")
class WindowsReadLayerTests(unittest.TestCase):
    def test_open_and_read_current_worker_buffer(self) -> None:
        payload = b"AT5T-state-read"
        buffer = ctypes.create_string_buffer(payload, len(payload))
        process = sampler.WindowsProcess(os.getpid())
        try:
            address = ctypes.addressof(buffer)
            self.assertEqual(process.read_native(address, len(payload)), payload)
            self.assertFalse(process.has_exited())
            self.assertTrue(process.image_path().is_file())
        finally:
            process.close()
        with self.assertRaisesRegex(
            sampler.MemoryReadError, "process_handle_closed"
        ):
            process.read_native(ctypes.addressof(buffer), 1)


if __name__ == "__main__":
    unittest.main()
