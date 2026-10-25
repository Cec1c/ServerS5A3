#!/usr/bin/env python3
"""Read-only AT-5T client state sampler.

The parser is platform independent and accepts any object exposing
``read(address, size) -> bytes``. Windows process access is isolated in
``WindowsProcess`` and requests only query-limited-information and VM-read.
"""

from __future__ import annotations

import argparse
import ctypes
from ctypes import wintypes
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import struct
import sys
import time
from typing import Any, Iterable, Protocol


PREFERRED_IMAGE_BASE = 0x400000
PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
PROCESS_VM_READ = 0x0010
PROCESS_ACCESS = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_VM_READ
TH32CS_SNAPMODULE = 0x00000008
TH32CS_SNAPMODULE32 = 0x00000010
STILL_ACTIVE = 259
MAX_U32 = 0xFFFFFFFF

GLOBAL_PRIMARY = 0x057B6FB0
GLOBAL_OVERRIDE = 0x057B6FB4
GLOBAL_VIRTUAL_CHARACTER = 0x057B6FB8
PRIVATE_INPUT_FALLBACK = 0x056A3C50
GLOBAL_KEY_MANAGER = 0x057F8A18
PROTECTION_BANK_OFFSET = 0x24
PROTECTION_KEY_OFFSET = 0x2114

ACTOR_SCENE = 0x0C8
ACTOR_DESCRIPTOR = 0x3DE0
ACTOR_PRIVATE_INPUT = 0x643C
ACTOR_JOYSTICK = 0x6440
ACTOR_RIDE = 0x6710
ACTOR_RIDE_LOCK = 0x6970
ACTOR_PROTECTED_FIELD = 0x32D8

VECTOR_BEGIN_END = {
    "scene": (0x0C, 0x10),
    "shared_pointer_rows": (0x0070, 0x0074),
    "hardware_rows": (0x0038, 0x003C),
}

MAX_SCENE_ELEMENTS = 4096
MAX_VEHICLES = 8
MAX_SHARED_POINTER_ROWS = 4096
MAX_HARDWARE_ROWS = 1024
SCENE_CACHE_MS = 250
VEHICLE_MARKER = 61235

ACCEPTED_IMAGES = {
    "8de790c1974fe74d3b74ec6095b7281ea337759a4adcdfebdd874bf67a94126f": (
        "V135",
        111812608,
    ),
    "26abf551753c2745c2e5b349fb24336422a189187ff847c995e0df9a68871079": (
        "V141",
        111812608,
    ),
}


class SamplerError(RuntimeError):
    """A user-facing sampler failure."""


class MemoryReadError(OSError):
    """A failed or invalid target-memory read."""

    def __init__(
        self,
        address: int,
        size: int,
        reason: str,
        *,
        winerror: int | None = None,
    ) -> None:
        self.address = address
        self.size = size
        self.reason = reason
        self.winerror = winerror
        super().__init__(reason)


class MemoryReader(Protocol):
    def read(self, address: int, size: int) -> bytes:
        """Return exactly ``size`` bytes starting at the 32-bit address."""


@dataclass(frozen=True)
class ImageVersion:
    version: str
    size: int
    sha256: str


@dataclass(frozen=True)
class PE32Identity:
    machine: int
    optional_magic: int
    image_base: int


@dataclass
class SceneLayout:
    refreshed_at: float
    scene_address: int
    begin: int
    end: int
    actor_address: int
    virtual_character_address: int
    role_matches: list[dict[str, Any]]
    vehicle_entries: list[dict[str, Any]]
    vehicle_total: int
    complete: bool


def utc_timestamp() -> str:
    return (
        datetime.now(timezone.utc)
        .isoformat(timespec="milliseconds")
        .replace("+00:00", "Z")
    )


def i32(value: int) -> int:
    value &= MAX_U32
    return value if value < 0x80000000 else value - 0x100000000


def translate_preferred_address(module_base: int, preferred_address: int) -> int:
    address = module_base + (preferred_address - PREFERRED_IMAGE_BASE)
    if not 0 < address <= MAX_U32:
        raise SamplerError(
            f"translated address is outside 32-bit range: "
            f"base={module_base:#x} preferred={preferred_address:#x}"
        )
    return address


def preferred_address_map(module_base: int) -> dict[str, int]:
    return {
        "primary": translate_preferred_address(module_base, GLOBAL_PRIMARY),
        "override": translate_preferred_address(module_base, GLOBAL_OVERRIDE),
        "virtual_character": translate_preferred_address(
            module_base, GLOBAL_VIRTUAL_CHARACTER
        ),
        "private_input_fallback": translate_preferred_address(
            module_base, PRIVATE_INPUT_FALLBACK
        ),
        "key_manager": translate_preferred_address(module_base, GLOBAL_KEY_MANAGER),
    }


def choose_actor(primary: int | None, override: int | None) -> tuple[int | None, str]:
    if override is None:
        return None, "override_unavailable"
    if override:
        return override, "override"
    if primary is None:
        return None, "primary_unavailable"
    if primary:
        return primary, "primary"
    return None, "none"


def classify_image_identity(
    digest: str,
    size: int,
    accepted: dict[str, tuple[str, int]] | None = None,
) -> ImageVersion:
    accepted = accepted or ACCEPTED_IMAGES
    if size not in {record[1] for record in accepted.values()}:
        expected_sizes = ", ".join(str(value) for value in sorted({r[1] for r in accepted.values()}))
        raise SamplerError(f"image_size_mismatch: got {size}, expected {expected_sizes}")
    if digest not in accepted:
        raise SamplerError(f"image_hash_mismatch: {digest}")
    version, accepted_size = accepted[digest]
    return ImageVersion(version=version, size=accepted_size, sha256=digest)


def _validate_u32_range(address: int, size: int) -> None:
    if size < 0:
        raise ValueError("read size must be non-negative")
    if address < 0 or address > MAX_U32:
        raise MemoryReadError(address, size, "address_outside_u32")
    if size and address + size - 1 > MAX_U32:
        raise MemoryReadError(address, size, "read_crosses_u32_boundary")


def read_u32_raw(reader: MemoryReader, address: int) -> int:
    _validate_u32_range(address, 4)
    data = reader.read(address, 4)
    if len(data) != 4:
        raise MemoryReadError(address, 4, f"short_read:{len(data)}")
    return struct.unpack("<I", data)[0]


def read_u8_raw(reader: MemoryReader, address: int) -> int:
    _validate_u32_range(address, 1)
    data = reader.read(address, 1)
    if len(data) != 1:
        raise MemoryReadError(address, 1, f"short_read:{len(data)}")
    return data[0]


class ReadContext:
    def __init__(self, reader: MemoryReader) -> None:
        self.reader = reader
        self.errors: list[dict[str, Any]] = []

    def add_error(
        self,
        field: str,
        address: int | None,
        size: int | None,
        error: str,
        *,
        include_address: bool = True,
    ) -> None:
        item: dict[str, Any] = {"field": field, "error": error}
        if include_address and address is not None:
            item["address"] = address
        if size is not None:
            item["size"] = size
        self.errors.append(item)

    def u32(
        self,
        field: str,
        address: int | None,
        *,
        null_is_empty: bool = False,
        include_address: bool = True,
    ) -> int | None:
        if address is None:
            return None
        if address == 0 and null_is_empty:
            return 0
        try:
            return read_u32_raw(self.reader, address)
        except (MemoryReadError, OSError) as exc:
            self.add_error(
                field,
                address,
                4,
                str(exc),
                include_address=include_address,
            )
            return None

    def u8(
        self,
        field: str,
        address: int | None,
        *,
        include_address: bool = True,
    ) -> int | None:
        if address is None:
            return None
        try:
            return read_u8_raw(self.reader, address)
        except (MemoryReadError, OSError) as exc:
            self.add_error(
                field,
                address,
                1,
                str(exc),
                include_address=include_address,
            )
            return None

    def bytes(
        self,
        field: str,
        address: int | None,
        size: int,
        *,
        include_address: bool = True,
    ) -> bytes | None:
        if address is None:
            return None
        try:
            _validate_u32_range(address, size)
            value = self.reader.read(address, size)
            if len(value) != size:
                raise MemoryReadError(address, size, f"short_read:{len(value)}")
            return value
        except (MemoryReadError, OSError) as exc:
            self.add_error(
                field,
                address,
                size,
                str(exc),
                include_address=include_address,
            )
            return None

    def u32_at_offset(
        self,
        field: str,
        base: int | None,
        offset: int,
        *,
        null_is_empty: bool = False,
    ) -> int | None:
        if base is None:
            return None
        return self.u32(
            field,
            base + offset,
            null_is_empty=null_is_empty,
        )

    def u8_at_offset(self, field: str, base: int | None, offset: int) -> int | None:
        if base is None:
            return None
        return self.u8(field, base + offset)


def _sample_actor(
    ctx: ReadContext,
    addresses: dict[str, int],
) -> tuple[dict[str, Any], dict[str, Any]]:
    primary = ctx.u32("globals.primary", addresses["primary"])
    override = ctx.u32("globals.override", addresses["override"])
    virtual_character = ctx.u32(
        "globals.virtual_character", addresses["virtual_character"]
    )
    actor_address, source = choose_actor(primary, override)

    actor: dict[str, Any] = {
        "address": actor_address,
        "source": source,
        "globals": {
            "primary": primary,
            "override": override,
            "virtual_character": virtual_character,
        },
        "vtable": None,
        "vtable_entries": None,
        "scene": None,
        "descriptor": None,
        "private_input": None,
        "private_input_used_fallback": None,
        "joystick": None,
        "ride": None,
        "ride_lock": None,
        "protected_action": None,
    }

    if actor_address:
        actor["vtable"] = ctx.u32_at_offset(
            "actor.vtable", actor_address, 0
        )
        actor["scene"] = ctx.u32_at_offset(
            "actor.scene", actor_address, ACTOR_SCENE
        )
        actor["descriptor"] = ctx.u32_at_offset(
            "actor.descriptor", actor_address, ACTOR_DESCRIPTOR
        )
        private_input = ctx.u32_at_offset(
            "actor.private_input", actor_address, ACTOR_PRIVATE_INPUT
        )
        if private_input == 0:
            actor["private_input"] = addresses["private_input_fallback"]
            actor["private_input_used_fallback"] = True
        else:
            actor["private_input"] = private_input
            actor["private_input_used_fallback"] = (
                False if private_input is not None else None
            )
        actor["joystick"] = ctx.u32_at_offset(
            "actor.joystick", actor_address, ACTOR_JOYSTICK
        )
        actor["ride"] = ctx.u32_at_offset(
            "actor.ride", actor_address, ACTOR_RIDE
        )
        actor["ride_lock"] = ctx.u8_at_offset(
            "actor.ride_lock", actor_address, ACTOR_RIDE_LOCK
        )
        actor["protected_action"] = decode_protected_action(
            ctx, actor_address + ACTOR_PROTECTED_FIELD
        )
        vtable = actor["vtable"]
        if vtable:
            entries: dict[str, int | None] = {}
            for name, offset in (
                ("eligibility", 0xCAC),
                ("normal_command", 0xE0C),
                ("action_set", 0x428),
            ):
                entries[name] = ctx.u32(
                    f"actor.vtable_entries.{name}",
                    vtable + offset,
                )
            actor["vtable_entries"] = entries

    return actor, {
        "primary": primary,
        "override": override,
        "virtual_character": virtual_character,
    }


def _range_header(
    ctx: ReadContext,
    field: str,
    base: int | None,
    begin_offset: int,
    end_offset: int,
    element_size: int,
    maximum: int,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "begin": None,
        "end": None,
        "byte_length": None,
        "element_size": element_size,
        "total_elements": None,
        "scanned_elements": 0,
        "truncated": False,
        "misaligned": False,
    }
    if base is None:
        return result
    begin = ctx.u32(f"{field}.begin", base + begin_offset)
    end = ctx.u32(f"{field}.end", base + end_offset)
    result["begin"] = begin
    result["end"] = end
    if begin is None or end is None:
        return result
    if end < begin:
        ctx.add_error(f"{field}.bounds", begin, None, "end_before_begin")
        return result
    length = end - begin
    result["byte_length"] = length
    if length % element_size:
        result["misaligned"] = True
        ctx.add_error(
            f"{field}.bounds",
            begin,
            length,
            f"byte_length_not_multiple_of_{element_size}",
        )
        return result
    total = length // element_size
    result["total_elements"] = total
    result["scanned_elements"] = min(total, maximum)
    result["truncated"] = total > maximum
    return result


def _read_scene_bytes(
    ctx: ReadContext,
    field: str,
    address: int,
    size: int,
) -> bytes | None:
    return ctx.bytes(field, address, size)


def _fresh_scene_layout(
    ctx: ReadContext,
    scene_address: int,
    actor_address: int | None,
    virtual_character_address: int | None,
) -> tuple[dict[str, Any], SceneLayout | None]:
    errors_before = len(ctx.errors)
    begin_offset, end_offset = VECTOR_BEGIN_END["scene"]
    header = _range_header(
        ctx,
        "scene.elements",
        scene_address,
        begin_offset,
        end_offset,
        4,
        MAX_SCENE_ELEMENTS,
    )
    role_matches: list[dict[str, Any]] = []
    vehicle_entries: list[dict[str, Any]] = []
    vehicle_total = 0
    begin = header["begin"]
    scanned = header["scanned_elements"]
    if begin is not None and scanned:
        for index in range(scanned):
            element_address = begin + index * 4
            raw_candidate = _read_scene_bytes(
                ctx, f"scene.elements[{index}]", element_address, 4
            )
            if raw_candidate is None:
                continue
            candidate = struct.unpack("<I", raw_candidate)[0]
            if not candidate:
                continue
            roles: list[str] = []
            if actor_address and candidate == actor_address:
                roles.append("actor")
            if virtual_character_address and candidate == virtual_character_address:
                roles.append("virtual_character")
            if roles:
                role_matches.append(
                    {
                        "scene_index": index,
                        "candidate": candidate,
                        "roles": roles,
                    }
                )

            marker = ctx.u32(
                f"scene.elements[{index}].marker",
                candidate + 0x400,
            )
            if marker == VEHICLE_MARKER:
                vehicle_total += 1
                if len(vehicle_entries) < MAX_VEHICLES:
                    vehicle_entries.append(
                        {
                            "scene_index": index,
                            "address": candidate,
                            "element_address": element_address,
                        }
                    )

    layout = None
    if header["total_elements"] is not None:
        layout = SceneLayout(
            refreshed_at=time.monotonic(),
            scene_address=scene_address,
            begin=header["begin"],
            end=header["end"],
            actor_address=actor_address or 0,
            virtual_character_address=virtual_character_address or 0,
            role_matches=role_matches,
            vehicle_entries=vehicle_entries,
            vehicle_total=vehicle_total,
            complete=len(ctx.errors) == errors_before,
        )
    scene = {
        "address": scene_address,
        "elements": header,
        "role_matches": role_matches,
        "vehicle_total": vehicle_total,
        "vehicles_truncated": vehicle_total > MAX_VEHICLES,
    }
    return scene, layout


def _cached_scene_is_valid(
    ctx: ReadContext,
    layout: SceneLayout,
    scene_address: int,
    actor_address: int | None,
    virtual_character_address: int | None,
    begin: int | None,
    end: int | None,
    now: float,
) -> bool:
    if not layout.complete:
        return False
    if now - layout.refreshed_at >= SCENE_CACHE_MS / 1000:
        return False
    if (
        layout.scene_address != scene_address
        or layout.actor_address != (actor_address or 0)
        or layout.virtual_character_address != (virtual_character_address or 0)
        or layout.begin != begin
        or layout.end != end
    ):
        return False
    for record in layout.role_matches:
        candidate = ctx.u32(
            f"scene.cache.roles[{record['scene_index']}]",
            record["element_address"] if "element_address" in record else begin + 4 * record["scene_index"],
        )
        if candidate != record["candidate"]:
            return False
    for record in layout.vehicle_entries:
        candidate = ctx.u32(
            f"scene.cache.vehicle[{record['scene_index']}]",
            record["element_address"],
        )
        if candidate != record["address"]:
            return False
        marker = ctx.u32(
            f"scene.cache.marker[{record['scene_index']}]",
            candidate + 0x400,
        )
        if marker != VEHICLE_MARKER:
            return False
    return True


def _protected_snapshot(
    ctx: ReadContext,
    field_address: int,
) -> dict[str, int] | None:
    ordinal = ctx._protection_snapshot_ordinal
    field = ctx.bytes(f"protection.field.{ordinal}", field_address, 8)
    if field is None:
        return None
    handle, encoded = struct.unpack("<II", field)
    manager_address = getattr(ctx, "_key_manager_address", None)
    manager = ctx.u32(
        f"protection.key_manager.{ordinal}",
        manager_address,
        include_address=False,
    )
    if manager is None:
        return None
    bank_address = (
        manager + PROTECTION_BANK_OFFSET + (handle >> 16) * 4
    )
    bank = ctx.u32(
        f"protection.key_bank.{ordinal}",
        bank_address,
        include_address=False,
    )
    if bank is None:
        return None
    key_address = bank + PROTECTION_KEY_OFFSET + (handle & 0xFFFF) * 4
    key = ctx.u32(
        f"protection.key.{ordinal}",
        key_address,
        include_address=False,
    )
    if key is None:
        return None
    return {
        "handle": handle,
        "encoded": encoded,
        "manager": manager,
        "bank": bank,
        "key": key,
    }


def decode_protected_action(
    ctx: ReadContext,
    field_address: int,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "field_address": field_address,
        "handle": None,
        "encoded": None,
        "action": None,
        "checksum_variant": field_address & 0xF,
        "integrity": None,
        "stable": False,
    }
    ctx._protection_snapshot_ordinal = "first"
    first = _protected_snapshot(ctx, field_address)
    if first is None:
        return result
    handle = first["handle"]
    encoded = first["encoded"]
    key1 = first["key"]
    result["handle"] = handle
    result["encoded"] = encoded

    # This second complete snapshot follows the first key lookup, so a field
    # or key-table mutation during resolution makes the sample unstable.
    ctx._protection_snapshot_ordinal = "second"
    second = _protected_snapshot(ctx, field_address)
    stable = second is not None and second == first
    if not stable:
        ctx.add_error(
            "protection.stable",
            field_address,
            None,
            "field_or_key_snapshot_changed_between_reads",
        )

    key_low = key1 & 0xFFFF
    plain = encoded ^ ((key_low & 0xFFFF) * 0x10001)
    plain &= MAX_U32
    result["action"] = i32(plain)
    low = plain & 0xFFFF
    high = (plain >> 16) & 0xFFFF
    variant = field_address & 0xF
    if variant == 0:
        check = ((high - key_low + low) & 0xFFFF) ^ key_low
    elif variant == 4:
        check = ((low - high) & 0xFFFF) ^ key_low
    elif variant == 8:
        check = ((high * low) & 0xFFFF) ^ key_low
    elif variant == 12:
        check = ((high + low + key_low) & 0xFFFF) ^ key_low
    else:
        check = ((high + low) & 0xFFFF) ^ key_low
    expected = (key1 >> 16) & 0xFFFF
    result["integrity"] = check == expected
    if check != expected:
        ctx.add_error(
            "protection.integrity",
            field_address,
            None,
            "checksum_mismatch",
        )
    result["stable"] = stable
    return result


def _sample_vehicle(ctx: ReadContext, record: dict[str, Any]) -> dict[str, Any]:
    address = record["address"]
    vehicle: dict[str, Any] = {
        "scene_index": record["scene_index"],
        "address": address,
        "vtable": ctx.u32_at_offset("vehicle.vtable", address, 0),
        "owner": ctx.u32_at_offset("vehicle.owner", address, 0x908),
        "rider": ctx.u32_at_offset("vehicle.rider", address, 0x5C84),
        "interaction_state": ctx.u32_at_offset(
            "vehicle.interaction_state", address, 0x1D8
        ),
        "lock": ctx.u8_at_offset("vehicle.lock", address, 0x5F64),
        "mode": ctx.u32_at_offset("vehicle.mode", address, 0x5F60),
        "protected_action": decode_protected_action(
            ctx, address + ACTOR_PROTECTED_FIELD
        ),
    }
    return vehicle


def sample_scene(
    ctx: ReadContext,
    scene_address: int | None,
    actor_address: int | None,
    virtual_character_address: int | None,
    cache: SceneLayout | None = None,
) -> tuple[dict[str, Any] | None, SceneLayout | None]:
    if not scene_address:
        return None, cache
    begin_offset, end_offset = VECTOR_BEGIN_END["scene"]
    begin = ctx.u32("scene.elements.begin", scene_address + begin_offset)
    end = ctx.u32("scene.elements.end", scene_address + end_offset)

    layout: SceneLayout | None = None
    cache_hit = bool(
        cache
        and _cached_scene_is_valid(
            ctx,
            cache,
            scene_address,
            actor_address,
            virtual_character_address,
            begin,
            end,
            time.monotonic(),
        )
    )
    if cache_hit:
        layout = cache
        scene = {
            "address": scene_address,
            "elements": {
                "begin": begin,
                "end": end,
                "byte_length": (
                    end - begin if begin is not None and end is not None and end >= begin else None
                ),
                "element_size": 4,
                "total_elements": (
                    (end - begin) // 4
                    if begin is not None and end is not None and end >= begin and (end - begin) % 4 == 0
                    else None
                ),
                "scanned_elements": min((end - begin) // 4, MAX_SCENE_ELEMENTS),
                "truncated": (end - begin) // 4 > MAX_SCENE_ELEMENTS,
                "misaligned": bool(
                    begin is not None
                    and end is not None
                    and end >= begin
                    and (end - begin) % 4
                ),
            },
            "role_matches": layout.role_matches,
            "vehicle_total": layout.vehicle_total,
            "vehicles_truncated": layout.vehicle_total > MAX_VEHICLES,
            "cache_hit": True,
            "cache_age_ms": round((time.monotonic() - layout.refreshed_at) * 1000, 3),
        }
    else:
        if begin is None or end is None:
            scene, _ = _fresh_scene_layout(
                ctx, scene_address, actor_address, virtual_character_address
            )
            return scene, cache
        scene, layout = _fresh_scene_layout(
            ctx, scene_address, actor_address, virtual_character_address
        )
        scene["cache_hit"] = False
        scene["cache_age_ms"] = 0.0

    entries = layout.vehicle_entries if layout else []
    scene["vehicles"] = [_sample_vehicle(ctx, entry) for entry in entries]
    return scene, layout


def sample_descriptor(
    ctx: ReadContext,
    descriptor_address: int | None,
) -> dict[str, Any]:
    result = {
        "address": descriptor_address,
        "enable": None,
        "state": None,
    }
    if descriptor_address:
        result["enable"] = ctx.u8(
            "descriptor.enable", descriptor_address + 0x30
        )
        result["state"] = ctx.u32(
            "descriptor.state", descriptor_address + 0x2C
        )
    return result


def sample_input(
    ctx: ReadContext,
    input_address: int | None,
    descriptor_address: int | None,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "address": input_address,
        "registered": None,
        "registration_complete": False,
        "registration_rows": [],
        "logical4": [],
        "shared_pointer_rows": None,
        "hardware_rows": None,
    }
    if not input_address:
        return result

    errors_before = len(ctx.errors)
    shared_begin, shared_end = VECTOR_BEGIN_END["shared_pointer_rows"]
    shared_header = _range_header(
        ctx,
        "input.shared_pointer_rows",
        input_address,
        shared_begin,
        shared_end,
        8,
        MAX_SHARED_POINTER_ROWS,
    )
    result["shared_pointer_rows"] = shared_header
    shared_base = shared_header["begin"]
    if descriptor_address and shared_base is not None:
        for index in range(shared_header["scanned_elements"]):
            row_address = shared_base + index * 8
            row = ctx.bytes(
                f"input.shared_pointer_rows[{index}]", row_address, 8
            )
            if row is None:
                continue
            pointer = struct.unpack_from("<I", row, 0)[0]
            if pointer == descriptor_address:
                result["registration_rows"].append(index)
    result["registration_complete"] = bool(
        descriptor_address
        and shared_header["total_elements"] is not None
        and not shared_header["truncated"]
        and len(ctx.errors) == errors_before
    )
    if result["registration_rows"]:
        result["registered"] = True
    elif result["registration_complete"]:
        result["registered"] = False

    hardware_begin, hardware_end = VECTOR_BEGIN_END["hardware_rows"]
    hardware_header = _range_header(
        ctx,
        "input.hardware_rows",
        input_address,
        hardware_begin,
        hardware_end,
        24,
        MAX_HARDWARE_ROWS,
    )
    result["hardware_rows"] = hardware_header
    hardware_base = hardware_header["begin"]
    if hardware_base is not None:
        for index in range(hardware_header["scanned_elements"]):
            row_address = hardware_base + index * 24
            row = ctx.bytes(f"input.hardware_rows[{index}]", row_address, 24)
            if row is None:
                continue
            logical = struct.unpack_from("<I", row, 0)[0]
            if logical == 4:
                result["logical4"].append(
                    {
                        "row_index": index,
                        "down": row[0x0C],
                        "fresh": row[0x0D],
                    }
                )
    return result


def sample_state(
    reader: MemoryReader,
    addresses: dict[str, int],
    *,
    scene_cache: SceneLayout | None = None,
) -> tuple[dict[str, Any], SceneLayout | None]:
    ctx = ReadContext(reader)
    setattr(ctx, "_key_manager_address", addresses["key_manager"])
    actor, globals_result = _sample_actor(ctx, addresses)
    descriptor = sample_descriptor(ctx, actor["descriptor"])
    input_state = sample_input(
        ctx, actor["private_input"], actor["descriptor"]
    )
    scene, new_cache = sample_scene(
        ctx,
        actor["scene"],
        actor["address"],
        globals_result["virtual_character"],
        scene_cache,
    )
    sample = {
        "actor": actor,
        "descriptor": descriptor,
        "input": input_state,
        "scene": scene,
        "errors": ctx.errors,
    }
    return sample, new_cache


def read_file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while True:
            block = stream.read(1024 * 1024)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()


def read_pe32_identity(path: Path) -> PE32Identity:
    try:
        with path.open("rb") as stream:
            dos = stream.read(0x40)
            if len(dos) != 0x40 or dos[:2] != b"MZ":
                raise SamplerError("image_is_not_pe: missing MZ header")
            pe_offset = struct.unpack_from("<I", dos, 0x3C)[0]
            stream.seek(pe_offset)
            pe = stream.read(0x80)
    except OSError as exc:
        raise SamplerError(f"image_read_failed: {exc}") from exc
    if len(pe) < 0x80 or pe[:4] != b"PE\0\0":
        raise SamplerError("image_is_not_pe: missing PE signature")
    machine = struct.unpack_from("<H", pe, 4)[0]
    optional_size = struct.unpack_from("<H", pe, 20)[0]
    optional_magic = struct.unpack_from("<H", pe, 24)[0]
    if machine != 0x014C:
        raise SamplerError(f"target_is_not_i386: machine={machine:#x}")
    if optional_size < 32 or optional_magic != 0x10B:
        raise SamplerError(
            f"target_is_not_pe32: optional_magic={optional_magic:#x}"
        )
    image_base = struct.unpack_from("<I", pe, 24 + 28)[0]
    if image_base != PREFERRED_IMAGE_BASE:
        raise SamplerError(
            f"unsupported_preferred_image_base: {image_base:#x}"
        )
    return PE32Identity(
        machine=machine,
        optional_magic=optional_magic,
        image_base=image_base,
    )


def validate_image_file(
    path: Path,
    *,
    accepted: dict[str, tuple[str, int]] | None = None,
) -> ImageVersion:
    try:
        size = path.stat().st_size
    except OSError as exc:
        raise SamplerError(f"image_stat_failed: {exc}") from exc
    accepted = accepted or ACCEPTED_IMAGES
    if size not in {record[1] for record in accepted.values()}:
        return classify_image_identity("", size, accepted)
    read_pe32_identity(path)
    digest = read_file_sha256(path)
    return classify_image_identity(digest, size, accepted)


class MODULEENTRY32W(ctypes.Structure):
    _fields_ = [
        ("dwSize", wintypes.DWORD),
        ("th32ModuleID", wintypes.DWORD),
        ("th32ProcessID", wintypes.DWORD),
        ("GlblcntUsage", wintypes.DWORD),
        ("ProccntUsage", wintypes.DWORD),
        ("modBaseAddr", ctypes.POINTER(wintypes.BYTE)),
        ("modBaseSize", wintypes.DWORD),
        ("hModule", wintypes.HMODULE),
        ("szModule", wintypes.WCHAR * 256),
        ("szExePath", wintypes.WCHAR * 260),
    ]


def _windows_api() -> tuple[Any, Any]:
    if os.name != "nt":
        raise SamplerError("Windows process access is only available on Windows")
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [
        wintypes.DWORD,
        wintypes.BOOL,
        wintypes.DWORD,
    ]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel32.CloseHandle.restype = wintypes.BOOL
    kernel32.QueryFullProcessImageNameW.argtypes = [
        wintypes.HANDLE,
        wintypes.DWORD,
        wintypes.LPWSTR,
        ctypes.POINTER(wintypes.DWORD),
    ]
    kernel32.QueryFullProcessImageNameW.restype = wintypes.BOOL
    kernel32.ReadProcessMemory.argtypes = [
        wintypes.HANDLE,
        ctypes.c_void_p,
        ctypes.c_void_p,
        ctypes.c_size_t,
        ctypes.POINTER(ctypes.c_size_t),
    ]
    kernel32.ReadProcessMemory.restype = wintypes.BOOL
    kernel32.CreateToolhelp32Snapshot.argtypes = [
        wintypes.DWORD,
        wintypes.DWORD,
    ]
    kernel32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    kernel32.Module32FirstW.argtypes = [
        wintypes.HANDLE,
        ctypes.POINTER(MODULEENTRY32W),
    ]
    kernel32.Module32FirstW.restype = wintypes.BOOL
    kernel32.Module32NextW.argtypes = [
        wintypes.HANDLE,
        ctypes.POINTER(MODULEENTRY32W),
    ]
    kernel32.Module32NextW.restype = wintypes.BOOL
    kernel32.GetExitCodeProcess.argtypes = [
        wintypes.HANDLE,
        ctypes.POINTER(wintypes.DWORD),
    ]
    kernel32.GetExitCodeProcess.restype = wintypes.BOOL
    return kernel32, ctypes


class WindowsProcess:
    """Read-only Windows process handle.

    ``OpenProcess`` is called with exactly ``PROCESS_QUERY_LIMITED_INFORMATION``
    and ``PROCESS_VM_READ``.
    """

    def __init__(self, pid: int) -> None:
        self.pid = pid
        self._kernel32, self._ctypes = _windows_api()
        self.handle = self._kernel32.OpenProcess(PROCESS_ACCESS, False, pid)
        if not self.handle:
            error = self._ctypes.get_last_error()
            raise SamplerError(
                f"OpenProcess failed for PID {pid}: {self._ctypes.WinError(error)}"
            )

    def close(self) -> None:
        if self.handle:
            self._kernel32.CloseHandle(self.handle)
            self.handle = None

    def __enter__(self) -> "WindowsProcess":
        return self

    def __exit__(self, exc_type: Any, exc: Any, traceback: Any) -> None:
        self.close()

    def image_path(self) -> Path:
        capacity = 32768
        buffer = ctypes.create_unicode_buffer(capacity)
        size = wintypes.DWORD(capacity)
        if not self._kernel32.QueryFullProcessImageNameW(
            self.handle, 0, buffer, ctypes.byref(size)
        ):
            error = self._ctypes.get_last_error()
            raise SamplerError(
                "QueryFullProcessImageNameW failed: "
                f"{self._ctypes.WinError(error)}"
            )
        return Path(buffer.value)

    def module_base(self, image_path: Path) -> int:
        snapshot = self._kernel32.CreateToolhelp32Snapshot(
            TH32CS_SNAPMODULE | TH32CS_SNAPMODULE32,
            self.pid,
        )
        invalid_handle = ctypes.c_void_p(-1).value
        if not snapshot or snapshot == invalid_handle:
            error = self._ctypes.get_last_error()
            raise SamplerError(
                f"CreateToolhelp32Snapshot failed: "
                f"{self._ctypes.WinError(error)}"
            )
        wanted_path = os.path.normcase(os.path.abspath(str(image_path)))
        wanted_name = os.path.normcase(image_path.name)
        fallback: int | None = None
        try:
            entry = MODULEENTRY32W()
            entry.dwSize = ctypes.sizeof(MODULEENTRY32W)
            available = self._kernel32.Module32FirstW(snapshot, ctypes.byref(entry))
            if not available:
                error = self._ctypes.get_last_error()
                raise SamplerError(
                    f"Module32FirstW failed: {self._ctypes.WinError(error)}"
                )
            while available:
                module_path = os.path.normcase(
                    os.path.abspath(entry.szExePath)
                )
                base = ctypes.cast(entry.modBaseAddr, ctypes.c_void_p).value or 0
                if module_path == wanted_path:
                    return base
                if (
                    fallback is None
                    and os.path.normcase(entry.szModule) == wanted_name
                ):
                    fallback = base
                available = self._kernel32.Module32NextW(
                    snapshot, ctypes.byref(entry)
                )
        finally:
            self._kernel32.CloseHandle(snapshot)
        if fallback:
            return fallback
        raise SamplerError(f"module_snapshot_missing_exe: {image_path}")

    def read(self, address: int, size: int) -> bytes:
        _validate_u32_range(address, size)
        return self.read_native(address, size)

    def read_native(self, address: int, size: int) -> bytes:
        """Read a native pointer-sized address without weakening parser limits."""
        if not self.handle:
            raise MemoryReadError(address, size, "process_handle_closed")
        if size == 0:
            return b""
        buffer = ctypes.create_string_buffer(size)
        transferred = ctypes.c_size_t(0)
        ok = self._kernel32.ReadProcessMemory(
            self.handle,
            ctypes.c_void_p(address),
            ctypes.cast(buffer, ctypes.c_void_p),
            size,
            ctypes.byref(transferred),
        )
        if not ok or transferred.value != size:
            error = self._ctypes.get_last_error()
            reason = (
                self._ctypes.WinError(error)
                if error
                else f"partial_read:{transferred.value}/{size}"
            )
            raise MemoryReadError(
                address,
                size,
                str(reason),
                winerror=error or None,
            )
        return buffer.raw

    def has_exited(self) -> bool:
        if not self.handle:
            return True
        exit_code = wintypes.DWORD()
        if not self._kernel32.GetExitCodeProcess(
            self.handle, ctypes.byref(exit_code)
        ):
            error = self._ctypes.get_last_error()
            raise SamplerError(
                f"GetExitCodeProcess failed: {self._ctypes.WinError(error)}"
            )
        return exit_code.value != STILL_ACTIVE


def _json_line(value: dict[str, Any]) -> str:
    return json.dumps(
        value,
        ensure_ascii=False,
        separators=(",", ":"),
        allow_nan=False,
    )


def _count_sample_errors(sample: dict[str, Any]) -> int:
    return len(sample.get("errors", []))


def run_sampler(
    pid: int,
    output: Path,
    *,
    seconds: float = 20.0,
    interval_ms: int = 5,
) -> dict[str, Any]:
    if pid <= 0:
        raise SamplerError("PID must be positive")
    if not math.isfinite(seconds) or seconds <= 0 or seconds > 60:
        raise SamplerError("seconds must be greater than 0 and at most 60")
    if interval_ms <= 0:
        raise SamplerError("interval-ms must be positive")
    if output.exists():
        raise SamplerError(f"refusing_to_overwrite_output: {output}")

    with WindowsProcess(pid) as process:
        image_path = process.image_path()
        image = validate_image_file(image_path)
        module_base = process.module_base(image_path)
        addresses = preferred_address_map(module_base)

        header = {
            "record_type": "header",
            "schema_version": 1,
            "timestamp": utc_timestamp(),
            "process": {
                "pid": pid,
                "exe_name": image_path.name,
                "sha256": image.sha256,
                "size": image.size,
                "version": image.version,
            },
            "module": {
                "base": module_base,
                "preferred_base": PREFERRED_IMAGE_BASE,
            },
            "sampling": {
                "seconds": seconds,
                "interval_ms": interval_ms,
            },
            "scope": {
                "read_only": True,
                "read_interval_is_not_instruction_trace": True,
            },
        }

        try:
            stream = output.open("x", encoding="utf-8", newline="\n")
        except FileExistsError as exc:
            raise SamplerError(f"refusing_to_overwrite_output: {output}") from exc
        except OSError as exc:
            raise SamplerError(f"output_open_failed: {exc}") from exc

        started_wall_ns = time.time_ns()
        started_monotonic = time.monotonic()
        sample_count = 0
        fully_read_sample_count = 0
        sample_error_count = 0
        stop_reason = "duration_elapsed"
        sampling_error: str | None = None
        scene_cache: SceneLayout | None = None
        interval_seconds = interval_ms / 1000.0
        deadline = started_monotonic + seconds
        next_tick = started_monotonic
        try:
            stream.write(_json_line(header) + "\n")
            stream.flush()
            while True:
                now = time.monotonic()
                if now >= deadline:
                    break
                if process.has_exited():
                    stop_reason = "process_exit"
                    break
                record_timestamp = utc_timestamp()
                sample_started_ns = time.time_ns()
                sample_started_monotonic = time.monotonic()
                try:
                    fields, scene_cache = sample_state(
                        process,
                        addresses,
                        scene_cache=scene_cache,
                    )
                except Exception as exc:
                    sampling_error = f"{type(exc).__name__}: {exc}"
                    stop_reason = "error"
                    break
                record = {
                    "record_type": "sample",
                    "sequence": sample_count,
                    "timestamp": record_timestamp,
                    "wall_time_ns": sample_started_ns,
                    "elapsed_ms": round(
                        (sample_started_monotonic - started_monotonic) * 1000,
                        3,
                    ),
                    "read_duration_ms": round(
                        (time.monotonic() - sample_started_monotonic) * 1000,
                        3,
                    ),
                    **fields,
                }
                stream.write(_json_line(record) + "\n")
                stream.flush()
                sample_count += 1
                error_count = _count_sample_errors(fields)
                sample_error_count += error_count
                if error_count == 0:
                    fully_read_sample_count += 1

                next_tick += interval_seconds
                now = time.monotonic()
                if next_tick <= now:
                    missed = math.floor((now - next_tick) / interval_seconds)
                    next_tick += missed * interval_seconds
                sleep_for = min(next_tick - now, deadline - now)
                if sleep_for > 0:
                    time.sleep(sleep_for)
        except KeyboardInterrupt:
            stop_reason = "interrupted"
        except Exception as exc:
            sampling_error = f"{type(exc).__name__}: {exc}"
            stop_reason = "error"
        finally:
            finished_monotonic = time.monotonic()
            summary = {
                "record_type": "summary",
                "timestamp": utc_timestamp(),
                "started_wall_time_ns": started_wall_ns,
                "finished_wall_time_ns": time.time_ns(),
                "elapsed_ms": round(
                    (finished_monotonic - started_monotonic) * 1000,
                    3,
                ),
                "stop_reason": stop_reason,
                "sample_count": sample_count,
                "fully_read_sample_count": fully_read_sample_count,
                "sample_error_count": sample_error_count,
            }
            if sampling_error:
                summary["error"] = sampling_error
            try:
                stream.write(_json_line(summary) + "\n")
                stream.flush()
            finally:
                stream.close()
        if sampling_error:
            raise SamplerError(sampling_error)
        return summary


def positive_pid(value: str) -> int:
    try:
        pid = int(value, 0)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("PID must be an integer") from exc
    if pid <= 0 or pid > MAX_U32:
        raise argparse.ArgumentTypeError("PID must be in 1..4294967295")
    return pid


def positive_interval(value: str) -> int:
    try:
        interval = int(value, 0)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("interval-ms must be an integer") from exc
    if interval <= 0:
        raise argparse.ArgumentTypeError("interval-ms must be positive")
    return interval


def positive_seconds(value: str) -> float:
    try:
        seconds = float(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("seconds must be numeric") from exc
    if not math.isfinite(seconds) or seconds <= 0 or seconds > 60:
        raise argparse.ArgumentTypeError("seconds must be greater than 0 and at most 60")
    return seconds


def build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Read-only AT-5T client state sampler",
    )
    parser.add_argument("--pid", type=positive_pid, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seconds", type=positive_seconds, default=20.0)
    parser.add_argument("--interval-ms", type=positive_interval, default=5)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_arg_parser().parse_args(argv)
    try:
        summary = run_sampler(
            args.pid,
            args.output,
            seconds=args.seconds,
            interval_ms=args.interval_ms,
        )
    except SamplerError as exc:
        print(f"at5t_state_sampler: {exc}", file=sys.stderr)
        return 2
    print(
        json.dumps(
            {
                "output": str(args.output),
                "samples": summary["sample_count"],
                "stop_reason": summary["stop_reason"],
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
