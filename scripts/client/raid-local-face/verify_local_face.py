"""Verify the external local-own raid-face candidate without running the client."""
from __future__ import annotations

import argparse
import json
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
for item in (str(HERE), str(HERE.parent / "raid-partyface")):
    if item not in sys.path:
        sys.path.insert(0, item)

import build_local_face as builder
import pe_support as pe
from capstone import Cs, CS_ARCH_X86, CS_MODE_32
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_EDI, UC_X86_REG_EFLAGS, UC_X86_REG_EIP,
    UC_X86_REG_ESI, UC_X86_REG_ESP,
)


SENTINEL = 0x7100000
STACK = 0x7012000
FRAME = 0x30208000
SCENE = 0x30020000
RAID_MANAGER = 0x30024000
ACTOR = 0x30028000
PEER = 0x3002C000
CACHE = 0x30030000
RAID = 0x30034000
CONTROL = 0x30038000
IMAGE_KEY = 0x3003C000

SCENE_PTR = 0x56A3758
RAID_PTR = 0x56A3778
INITIAL_ESI = 0x11111111
INITIAL_EDI = 0x22222222
INITIAL_FLAGS = 0x246


def u32(data: bytes, off: int = 0) -> int:
    return struct.unpack_from("<I", data, off)[0]


def p32(value: int) -> bytes:
    return struct.pack("<I", value & 0xFFFFFFFF)


def snapshot(uc: Uc, address: int, size: int) -> bytes:
    return bytes(uc.mem_read(address, size))


class Harness:
    def __init__(self, candidate: bytes, helper_va: int, helper: bytes):
        self.uc = Uc(UC_ARCH_X86, UC_MODE_32)
        self.mapped: set[int] = set()
        self.helper_va = helper_va
        self.events: list[dict] = []
        self.raid_return = RAID
        self.cache_return = 0
        self.lazy_called = False
        self._map_zero()
        self._map_objects()
        self.put(0x110B16A, candidate[
            pe.offset(pe.headers(candidate)[1], 0x110B16A, 5):
            pe.offset(pe.headers(candidate)[1], 0x110B16A, 5) + 5
        ])
        self.put(helper_va, helper)
        for va, size in (
            (0x37E9F40, 8), (0x209BBE0, 4), (0x3C1BEA0, 4),
        ):
            off = pe.offset(pe.headers(candidate)[1], va, size)
            self.put(va, candidate[off:off + size])
        self.put(0x1A553E0, b"\xC3")
        self.put(0x1A5A5E0, b"\xC3")
        self.put(0x2966C60, b"\xC3")
        self.put(0x296DD80, b"\xC3")
        self._hooks()

    def put(self, va: int, blob: bytes) -> None:
        first = va & ~0xFFF
        last = (va + len(blob) + 0xFFF) & ~0xFFF
        for page in range(first, last, 0x1000):
            if page not in self.mapped:
                self.uc.mem_map(page, 0x1000)
                self.mapped.add(page)
        self.uc.mem_write(va, blob)

    def zeros(self, va: int, size: int) -> None:
        self.put(va, bytes(size))

    def r32(self, va: int) -> int:
        return u32(bytes(self.uc.mem_read(va, 4)))

    def w32(self, va: int, value: int) -> None:
        self.uc.mem_write(va, p32(value))

    def w8(self, va: int, value: int) -> None:
        self.uc.mem_write(va, bytes([value & 0xFF]))

    def _map_zero(self) -> None:
        self.zeros(0, 0x1000)
        self.zeros(SENTINEL, 0x1000)
        self.zeros(STACK - 0x1000, 0x3000)

    def _map_objects(self) -> None:
        self.zeros(FRAME - 0x1000, 0x3000)
        for base in (SCENE, RAID_MANAGER, ACTOR, PEER, CACHE, RAID, CONTROL, IMAGE_KEY):
            self.zeros(base, 0x4000)
        self.zeros(0x56A3000, 0x2000)

    def _hook(self, address: int, callback) -> None:
        self.uc.hook_add(UC_HOOK_CODE, callback, begin=address, end=address + 1)

    def _hooks(self) -> None:
        def stop_original(emu, address, size, _):
            self.events.append({"kind": "endpoint", "value": "original"})
            emu.emu_stop()

        def stop_hit(emu, address, size, _):
            self.events.append({"kind": "endpoint", "value": "hit"})
            emu.emu_stop()

        def raid_hook(emu, address, size, _):
            self.events.append({"kind": "raid_ecx", "value": emu.reg_read(UC_X86_REG_ECX)})
            emu.reg_write(UC_X86_REG_EAX, self.raid_return)

        def cache_hook(emu, address, size, _):
            esp = emu.reg_read(UC_X86_REG_ESP)
            self.events.append({
                "kind": "cache_args",
                "key": self.r32(esp + 4) & 0xFFFF,
                "owner": self.r32(esp + 8) & 0xFF,
                "route": self.r32(esp + 12) & 0xFF,
            })
            emu.reg_write(UC_X86_REG_EAX, self.cache_return)

        def draw_hook(emu, address, size, _):
            esp = emu.reg_read(UC_X86_REG_ESP)
            self.events.append({
                "kind": "draw_args",
                "x": self.r32(esp + 4),
                "y": self.r32(esp + 8),
                "width": self.r32(esp + 12),
                "height": self.r32(esp + 16),
                "image": self.r32(esp + 20),
                "target": self.r32(esp + 24),
                "color": self.r32(esp + 28),
            })

        def lazy_hook(emu, address, size, _):
            self.lazy_called = True
            self.events.append({"kind": "unexpected_lazy_getter", "value": 1})
            emu.emu_stop()

        self._hook(0x110B16F, stop_original)
        self._hook(0x110B199, stop_hit)
        self._hook(0x1A553E0, raid_hook)
        self._hook(0x1A5A5E0, cache_hook)
        self._hook(0x2966C60, draw_hook)
        self._hook(0x296DD80, lazy_hook)

    def setup(self, case: dict) -> None:
        actor_key = 0x2201
        self.w32(SCENE_PTR, case.get("scene", SCENE))
        self.w32(RAID_PTR, case.get("raid_manager", RAID_MANAGER))
        self.w32(SCENE + 0x70, case.get("scene_local", ACTOR))

        self.w32(ACTOR, 0x49ED1DC)
        self.uc.mem_write(ACTOR + 0x20, struct.pack("<H", actor_key))
        self.w32(ACTOR + 0x34, case.get("actor_gate", 0x10))
        self.w32(ACTOR + 0x4F4, case.get("drawable", 0))
        self.w8(ACTOR + 0x604, 0x99)
        self.w8(ACTOR + 0x605, 0xAA)

        self.w32(PEER, 0x49ED1DC)
        self.uc.mem_write(PEER + 0x20, struct.pack("<H", 0x3301))

        self.w32(CACHE, 0x49ED1DC)
        self.uc.mem_write(CACHE + 0x20, struct.pack("<H", case.get("cache_key", actor_key)))
        self.w32(CACHE + 0x30, IMAGE_KEY if case.get("cache_image", True) else 0)
        self.w32(CACHE + 0x34, case.get("cache_gate", 0x20))
        self.cache_return = CACHE if case.get("cache", True) else 0
        self.raid_return = RAID if case.get("raid", True) else 0

        self.w8(RAID + 2, case.get("owner", 0x56))
        self.w8(RAID + 3, case.get("route", 0x78))
        self.w32(CONTROL + 0x7C, 101)
        self.w32(CONTROL + 0x80, 202)
        self.w32(FRAME - 0x74, case.get("control", CONTROL))

    def run(self, case: dict) -> dict:
        self.setup(case)
        watched = {
            "actor": snapshot(self.uc, ACTOR, 0x700),
            "peer": snapshot(self.uc, PEER, 0x100),
            "cache": snapshot(self.uc, CACHE, 0x100),
            "scene": snapshot(self.uc, SCENE, 0x100),
            "raid_manager": snapshot(self.uc, RAID_MANAGER, 0x100),
            "raid": snapshot(self.uc, RAID, 0x100),
            "control": snapshot(self.uc, CONTROL, 0x100),
            "image_key": snapshot(self.uc, IMAGE_KEY, 0x100),
            "globals": snapshot(self.uc, 0x56A3000, 0x1000),
        }
        self.w32(STACK, SENTINEL)
        self.uc.reg_write(UC_X86_REG_ESP, STACK)
        self.uc.reg_write(UC_X86_REG_EBP, FRAME)
        self.uc.reg_write(UC_X86_REG_EAX, 0xAAAAAAAA)
        self.uc.reg_write(UC_X86_REG_ECX, 0xCCCCCCCC)
        self.uc.reg_write(UC_X86_REG_EDX, 0xDDDDDDDD)
        self.uc.reg_write(UC_X86_REG_EBX, case.get("actor", ACTOR))
        self.uc.reg_write(UC_X86_REG_ESI, INITIAL_ESI)
        self.uc.reg_write(UC_X86_REG_EDI, INITIAL_EDI)
        self.uc.reg_write(UC_X86_REG_EFLAGS, INITIAL_FLAGS)
        initial_esp = self.uc.reg_read(UC_X86_REG_ESP)
        self.uc.emu_start(0x110B16A, SENTINEL, count=100000)
        regs = {
            "eax": self.uc.reg_read(UC_X86_REG_EAX),
            "ecx": self.uc.reg_read(UC_X86_REG_ECX),
            "edx": self.uc.reg_read(UC_X86_REG_EDX),
            "ebx": self.uc.reg_read(UC_X86_REG_EBX),
            "esi": self.uc.reg_read(UC_X86_REG_ESI),
            "edi": self.uc.reg_read(UC_X86_REG_EDI),
            "ebp": self.uc.reg_read(UC_X86_REG_EBP),
            "esp": self.uc.reg_read(UC_X86_REG_ESP),
            "eflags": self.uc.reg_read(UC_X86_REG_EFLAGS),
            "eip": self.uc.reg_read(UC_X86_REG_EIP),
        }
        after = {
            "actor": snapshot(self.uc, ACTOR, 0x700),
            "peer": snapshot(self.uc, PEER, 0x100),
            "cache": snapshot(self.uc, CACHE, 0x100),
            "scene": snapshot(self.uc, SCENE, 0x100),
            "raid_manager": snapshot(self.uc, RAID_MANAGER, 0x100),
            "raid": snapshot(self.uc, RAID, 0x100),
            "control": snapshot(self.uc, CONTROL, 0x100),
            "image_key": snapshot(self.uc, IMAGE_KEY, 0x100),
            "globals": snapshot(self.uc, 0x56A3000, 0x1000),
        }
        endpoint = next((e["value"] for e in reversed(self.events) if e["kind"] == "endpoint"), None)
        return {
            "endpoint": endpoint,
            "events": list(self.events),
            "regs": regs,
            "stack_preserved": regs["esp"] == initial_esp,
            "flags_preserved": regs["eflags"] == INITIAL_FLAGS,
            "objects_unchanged": watched == after,
            "lazy_getter_called": self.lazy_called,
        }


def helper_calls(code: bytes, va: int) -> list[int]:
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    return [
        int(ins.op_str, 16) & 0xFFFFFFFF
        for ins in md.disasm(code, va)
        if ins.mnemonic == "call" and ins.op_str.startswith("0x")
    ]


def verify_candidate(input_data: bytes, candidate: bytes) -> tuple[dict, bytes, int, bytes]:
    expected, build_receipt = builder.build(input_data)
    if candidate != expected:
        raise AssertionError("candidate is not the deterministic builder output")
    if len(candidate) != len(input_data) + 4096:
        raise AssertionError("candidate size is not input+one section")
    opt, sections = pe.headers(candidate)
    input_opt, input_sections = pe.headers(input_data)
    if struct.unpack_from("<I", candidate, opt + 16)[0] != struct.unpack_from("<I", input_data, input_opt + 16)[0]:
        raise AssertionError("entry point changed")
    if candidate[opt + 96:opt + 224] != input_data[input_opt + 96:input_opt + 224]:
        raise AssertionError("data directories changed")
    if struct.unpack_from("<I", candidate, opt + 64)[0] != pe.checksum(candidate, opt + 64):
        raise AssertionError("candidate checksum invalid")
    if struct.unpack_from("<I", candidate, opt + 56)[0] != len(candidate):
        raise AssertionError("SizeOfImage mismatch")
    original_table = opt + struct.unpack_from("<H", candidate, opt - 4)[0]
    if candidate[original_table:original_table + 36 * 40] != input_data[original_table:original_table + 36 * 40]:
        raise AssertionError("existing section headers changed")
    pe_off = struct.unpack_from("<I", candidate, 0x3C)[0]
    count = struct.unpack_from("<H", candidate, pe_off + 6)[0]
    if count != 37:
        raise AssertionError("new section count changed")
    header = opt + struct.unpack_from("<H", candidate, opt - 4)[0] + 36 * 40
    name = candidate[header:header + 8]
    vsize, va, rawsize, raw = struct.unpack_from("<IIII", candidate, header + 8)
    if name != b".locface":
        raise AssertionError("helper section name changed")
    if (va, raw, rawsize) != (builder.INPUT_SIZE, builder.INPUT_SIZE, 4096):
        raise AssertionError("helper section placement changed")
    if struct.unpack_from("<I", candidate, header + 36)[0] != 0x60000020:
        raise AssertionError("helper section is not read/execute code")
    helper_va = pe.IMAGE_BASE + va
    helper = candidate[raw:raw + vsize]
    if any(candidate[raw + vsize:raw + rawsize]):
        raise AssertionError("helper section padding is not zero")
    site = pe.offset(sections, builder.PATCH_SITE, 5)
    if candidate[site] != 0xE9 or candidate[site + 1:site + 5] != struct.pack(
        "<i", helper_va - builder.PATCH_SITE - 5
    ):
        raise AssertionError("patch site does not jump to helper")

    calls = helper_calls(helper, helper_va)
    allowed_calls = {0x1A553E0, 0x1A5A5E0, 0x3C1BEA0, 0x37E9F40, 0x209BBE0, 0x2966C60}
    if set(calls) != allowed_calls:
        raise AssertionError(f"unexpected helper calls: {[hex(x) for x in calls]}")
    if 0x296DD80 in calls or 0x2965BD0 in calls or 0x1A62AA0 in calls:
        raise AssertionError("helper gained a lazy/create call")
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    instructions = list(md.disasm(helper, helper_va))
    scene_loads = [ins for ins in instructions if "dwordptr[0x56a3758]" in ins.op_str.replace(" ", "")]
    raid_loads = [ins for ins in instructions if "dwordptr[0x56a3778]" in ins.op_str.replace(" ", "")]
    if len(scene_loads) != 1 or len(raid_loads) != 1:
        raise AssertionError("scene/raid manager loads are not one each")
    raid_index = instructions.index(raid_loads[0])
    if not any(
        ins.mnemonic == "call" and ins.op_str == "0x1a553e0"
        for ins in instructions[raid_index + 1:raid_index + 5]
    ):
        raise AssertionError("1A553E0 is not called from the raid-manager load")
    jumps = {
        int(ins.op_str, 16) & 0xFFFFFFFF
        for ins in instructions
        if ins.mnemonic == "jmp" and ins.op_str.startswith("0x")
    }
    if jumps != {0x110B16F, 0x110B199}:
        raise AssertionError(f"unexpected helper exits: {[hex(x) for x in jumps]}")
    if not (
        helper.count(bytes.fromhex("8b483085c9")) == 1
        and helper.count(bytes.fromhex("8b7804")) == 1
    ):
        raise AssertionError("cache image-key/+34 load sequence changed")

    allowed = set()
    for lo, hi in build_receipt["allowed_original_ranges"]:
        allowed.update(range(lo, hi))
    changed = [index for index, (left, right) in enumerate(zip(input_data, candidate)) if left != right]
    if any(index not in allowed for index in changed):
        raise AssertionError("candidate changed bytes outside declared original ranges")
    return build_receipt, helper, helper_va, expected


def cases() -> list[dict]:
    return [
        {"name": "local_hit"},
        {"name": "local_cache_missing", "cache": False, "endpoint": "original"},
        {"name": "peer", "actor": PEER, "endpoint": "original"},
        {"name": "drawable_present", "drawable": 0x5000000, "endpoint": "original"},
        {"name": "scene_gate_non10", "actor_gate": 0x11, "endpoint": "original"},
        {"name": "scene_manager_missing", "scene": 0, "endpoint": "original"},
        {"name": "local_cache_not_populated", "scene_local": 0, "endpoint": "original"},
        {"name": "scene_cache_is_peer", "scene_local": PEER, "endpoint": "original"},
        {"name": "control_missing", "control": 0, "endpoint": "original"},
        {"name": "raid_manager_missing", "raid_manager": 0, "endpoint": "original"},
        {"name": "raid_missing", "raid": False, "endpoint": "original"},
        {"name": "cache_key_mismatch", "cache_key": 0x3301, "endpoint": "original"},
        {"name": "cache_image_missing", "cache_image": False, "endpoint": "original"},
        {"name": "cache_gate_is_10", "cache_gate": 0x10, "endpoint": "original"},
        {"name": "cache_gate_zero_hit", "cache_gate": 0},
        {"name": "namespace_zero_hit", "owner": 0, "route": 0},
    ]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--candidate", required=True, type=Path)
    parser.add_argument("--build-receipt", required=True, type=Path)
    parser.add_argument("--receipt", required=True, type=Path)
    args = parser.parse_args()
    if args.receipt.exists():
        raise SystemExit("new receipt path required")
    input_data = args.input.read_bytes()
    candidate = args.candidate.read_bytes()
    build_receipt, helper, helper_va, expected = verify_candidate(input_data, candidate)
    stored_build = json.loads(args.build_receipt.read_text(encoding="utf-8"))
    canonical_build = json.loads(json.dumps(build_receipt))
    if stored_build != canonical_build:
        raise AssertionError("builder receipt mismatch")

    results = []
    assertions = []
    for case in cases():
        harness = Harness(candidate, helper_va, helper)
        result = harness.run(case)
        expected_endpoint = case.get("endpoint", "hit")
        draws = [e for e in result["events"] if e["kind"] == "draw_args"]
        lookups = [e for e in result["events"] if e["kind"] == "cache_args"]
        raid_calls = [e for e in result["events"] if e["kind"] == "raid_ecx"]
        checks = {
            "endpoint": result["endpoint"] == expected_endpoint,
            "stack": result["stack_preserved"],
            "flags": result["flags_preserved"],
            "objects": result["objects_unchanged"],
            "no_lazy_getter": not result["lazy_getter_called"],
            "ebx": result["regs"]["ebx"] == case.get("actor", ACTOR),
            "ebp": result["regs"]["ebp"] == FRAME,
            "esi": result["regs"]["esi"] == 0x49ED1DC,
            "edi": result["regs"]["edi"] == INITIAL_EDI,
            "draw_count": len(draws) == (1 if expected_endpoint == "hit" else 0),
        }
        if expected_endpoint == "original":
            checks["ecx"] = result["regs"]["ecx"] == case.get("control", CONTROL)
        else:
            checks["lookup_count"] = len(lookups) == 1
            checks["raid_ecx"] = len(raid_calls) == 1 and raid_calls[0]["value"] == case.get("raid_manager", RAID_MANAGER)
            expected_owner = case.get("owner", 0x56)
            expected_route = case.get("route", 0x78)
            checks["namespace"] = lookups[0]["key"] == 0x2201 and lookups[0]["owner"] == expected_owner and lookups[0]["route"] == expected_route
            checks["draw_signature"] = draws[0] == {
                "kind": "draw_args", "x": 84, "y": 200, "width": 14, "height": 14,
                "image": case.get("cache_gate", 0x20), "target": 0, "color": 0xFF,
            }
        passed = all(checks.values())
        assertions.append({"name": case["name"], "passed": passed, "checks": checks})
        results.append({"case": case["name"], **result})
    if not all(item["passed"] for item in assertions):
        bad = [item for item in assertions if not item["passed"]]
        raise AssertionError({"failed": bad})

    receipt = {
        "input_sha256": builder.INPUT_SHA,
        "candidate_sha256": pe.sha(candidate),
        "candidate_size": len(candidate),
        "patch_site": hex(builder.PATCH_SITE),
        "helper_va": hex(helper_va),
        "helper_size": len(helper),
        "helper_calls": [hex(value) for value in helper_calls(helper, helper_va)],
        "entry_boundary": {
            "patch": "0x110B16A -> .locface",
            "miss": "restore then 0x110B16F",
            "hit": "draw/cleanup then 0x110B199",
        },
        "volatile_policy": {
            "preserved": ["ebx", "esi", "edi", "ebp", "esp", "eflags"],
            "caller_clobbered": ["eax", "ecx", "edx"],
            "miss_entry_uses": "only ecx and esi are re-established before 110B16F; incoming eax/edx are dead",
            "hit_resume_uses": "ecx is overwritten at 110B199, edi at B1A4, eax via raid getter, edx at B1C3, esi at B1E8; ebx is live at B222",
        },
        "cache_image_field": (
            "3C1BEA0 returns cacheActor+0x30; helper requires the loaded image-key pointer at [+0x30], "
            "then loads cacheActor+0x34 exactly as 25136BB and rejects the 0x10 sentinel"
        ),
        "cases": results,
        "assertions": assertions,
        "assertions_passed": True,
        "published": False,
        "deployed": False,
        "installed": False,
        "native_gui_accepted": False,
        "limits": (
            "Complete helper executes against synthetic actor/raid/cache objects; "
            "registry getters and 2966C60 are controlled stubs. No GUI pixels or live state."
        ),
    }
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps({"candidate": args.candidate.name, "cases": len(results), "assertions_passed": True}))


if __name__ == "__main__":
    main()
