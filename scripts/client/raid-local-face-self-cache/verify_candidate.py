#!/usr/bin/env python3
"""Pinned A03 self-cache candidate v2, plus the old-helper control.

The original 38 gate/geometry/flags cases remain; ten candidate sequence cases
and two old-helper negative controls add repeated calls on the same objects.
Expected totals: 48 candidate cases / 58 invocations, two baseline cases / four
invocations. Only an explicit --receipt NEW_PATH may write a new result file.

Real code: dispatch, helper, 296DD80 including its exact 0xD0-byte code/table
window, 3C1BEA0 and both coordinate getters. Protected-key decoders, active
actor lookup, raid/aux lookup and image drawing are explicit fixtures. The
fixed address 57B7218 selects decoder 37DF630; mapping all five fixture entries
does not mean all five decoder branches execute. This is not GUI acceptance.

Run with --input V160_EXE --candidate NEW_EXE --build-receipt BUILD_JSON
  --receipt NEW_RESULT_JSON. All inputs remain fixed by SHA-256.
No build, installation, process access, old-result update or network operation.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from dataclasses import dataclass, replace
from pathlib import Path

sys.dont_write_bytecode = True
if not __debug__:
    raise SystemExit("optimized Python is unsupported")

try:
    import unicorn
    if unicorn.__version__ != "2.1.4":
        raise ImportError("unicorn==2.1.4 required")
    from unicorn import (
        Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE,
    )
    from unicorn.x86_const import (
        UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
        UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
        UC_X86_REG_EIP, UC_X86_REG_EFLAGS,
    )
except ImportError as exc:
    print(json.dumps(dict(status="blocked", code="unicorn_dependency",
                          error=str(exc))), file=sys.stderr)
    raise SystemExit(2)

ROOT = Path(".")
WANT_SIZE = 111890432
WANT_CHECKSUM = 111922510
BUILD_RECEIPT = ROOT / "a03-self-cache-candidate-v2/build-receipt.json"
SITE, HELPER = 0x110B16A, 0x6EAE000
NORMAL, DONE = 0x110B16F, 0x110B199
RAID_GET, LOOKUP, IMAGE_DRAW = 0x1A553E0, 0x1A5A5E0, 0x2966C60
ACTOR_INFO, Y_GET, X_GET = 0x3C1BEA0, 0x37E9F40, 0x209BBE0
SELF_GET, SELF_GET_SIZE = 0x296DD80, 0xD0
ACTIVE_GET, CACHE_STORE = 0x296D730, 0x296DE1F
PROTECTED_THIS, PROTECTED_CELL = 0x57B7218, 0x57B721C
DECODER_RETURNS = {
    0x37DF550: 0x296DDBF,
    0x37DF5C0: 0x296DDD4,
    0x37DF630: 0x296DDE9,
    0x37DF6A0: 0x296DDFE,
    0x37DF7F0: 0x296DE13,
}
SELECTED_DECODER = 0x37DF630
DECODER_EVENT = f"protected-decoder-fixture:{SELECTED_DECODER:#x}"
SCENE_GLOBAL, RAID_GLOBAL = 0x56A3758, 0x56A3778
ACTOR, AUX, SCENE, RAID, RAID_INFO, CONTROL, VTABLE, OTHER = range(
    0x30004000, 0x30014000, 0x2000)
STACK, FRAME = 0x31004000, 0x31008000
KEY, OWNER, ROUTE, IMAGE_VALUE = 0x1234, 7, 9, 0x05102030
MASK32 = 0xFFFFFFFF

# Exact static read from the fixed PE, not assembled/replaced native code.
# Includes the dword table at 296DE2C and byte table at 296DE40.
SELF_GET_HEX = (
    "55 8b ec 51 56 8b f1 83 7e 70 00 0f 85 91 00 00"
    " 00 b8 18 72 7b 05 83 e0 0f 83 f8 0c 77 62 0f b6"
    " 80 40 de 96 02 ff 24 85 2c de 96 02 8d 4d fc 51"
    " 68 1c 72 7b 05 b9 18 72 7b 05 e8 91 17 e7 00 eb"
    " 52 8d 55 fc 52 68 1c 72 7b 05 b9 18 72 7b 05 e8"
    " ec 17 e7 00 eb 3d 8d 45 fc 50 68 1c 72 7b 05 b9"
    " 18 72 7b 05 e8 47 18 e7 00 eb 28 8d 4d fc 51 68"
    " 1c 72 7b 05 b9 18 72 7b 05 e8 a2 18 e7 00 eb 13"
    " 8d 55 fc 52 68 1c 72 7b 05 b9 18 72 7b 05 e8 dd"
    " 19 e7 00 8b 45 fc 50 e8 14 f9 ff ff 83 c4 04 89"
    " 46 70 8b 46 70 5e 8b e5 5d c3 8b ff ac dd 96 02"
    " c1 dd 96 02 d6 dd 96 02 eb dd 96 02 00 de 96 02"
    " 00 04 04 04 01 04 04 04 02 04 04 04 03 cc cc cc"
)


class Blocked(Exception):
    def __init__(self, code, **detail):
        super().__init__(code)
        self.payload = dict(status="blocked", code=code, **detail)


def require(condition, code, **detail):
    if not condition:
        raise Blocked(code, **detail)


def p32(value):
    return struct.pack("<I", value & MASK32)


@dataclass(frozen=True)
class Layout:
    name: str
    exe: Path
    sha256: str
    helper_size: int
    helper_sha256: str
    canonical_getter: bool

    def address(self, old_address):
        # Old MOV occupies [03A,03D); new CALL occupies [03A,03F).
        # Only old helper instruction addresses after that replacement move.
        require(HELPER <= old_address < HELPER+249, "layout_address",
                address=hex(old_address))
        delta = 2 if self.canonical_getter and old_address >= HELPER+0x3D else 0
        return old_address+delta


BASELINE = Layout(
    "baseline-v160",
    ROOT / "hud-public-rebuild-v3/DNF-hud-local-candidate.exe",
    "a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1",
    249, "0e80a4f78cb79810acc4c670be6bfd754f4a854a8fee8e4652a5aaf2cfc4573d", False)
CANDIDATE = Layout(
    "self-cache-candidate-v2",
    ROOT / "a03-self-cache-candidate-v2/DNF-a03-self-cache-candidate.exe",
    "5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc",
    251, "e5ca9cf17105d75d7dae1d73bbe09b5bfd704af941e3f1aa010995ec3fa61634", True)


class PeImage:
    """Read only, SHA-pinned PE32 section mapping of explicit windows."""
    def __init__(self, layout):
        self.layout = layout
        raw = layout.exe.read_bytes()
        self.sha256 = hashlib.sha256(raw).hexdigest()
        require(len(raw) == WANT_SIZE and self.sha256 == layout.sha256,
                "fixed_input", path=str(layout.exe), size=len(raw), sha256=self.sha256,
                expectedSize=WANT_SIZE, expectedSha256=layout.sha256)
        require(len(raw) >= 0x40 and raw[:2] == b"MZ", "pe_dos")
        nt = struct.unpack_from("<I", raw, 0x3C)[0]
        require(nt+24 <= len(raw) and raw[nt:nt+4] == b"PE\0\0", "pe_nt")
        machine, count = struct.unpack_from("<HH", raw, nt+4)
        opt_size = struct.unpack_from("<H", raw, nt+20)[0]
        opt = nt+24
        require(machine == 0x14C and count > 0 and opt_size >= 68
                and opt+opt_size <= len(raw), "pe_header_bounds")
        require(struct.unpack_from("<H", raw, opt)[0] == 0x10B, "pe32_magic")
        self.base = struct.unpack_from("<I", raw, opt+28)[0]
        self.checksum = struct.unpack_from("<I", raw, opt+64)[0]
        require(self.base == 0x400000, "image_base", actual=hex(self.base))
        if layout.canonical_getter:
            require(self.checksum == WANT_CHECKSUM, "candidate_checksum_field",
                    actual=self.checksum, expected=WANT_CHECKSUM)
        table = opt+opt_size
        require(table+count*40 <= len(raw), "section_table_bounds")
        self.sections = []
        for index in range(count):
            at = table+index*40
            vsize, rva, raw_size, raw_ptr = struct.unpack_from("<IIII", raw, at+8)
            self.sections.append(dict(
                name=raw[at:at+8].split(b"\0", 1)[0].decode("ascii", "replace"),
                vsize=vsize, rva=rva, rawSize=raw_size, rawPtr=raw_ptr))
        section = [s for s in self.sections if self.base+s["rva"] == HELPER]
        require(len(section) == 1 and section[0]["name"] == ".locface"
                and section[0]["vsize"] == layout.helper_size
                and section[0]["rawSize"] == 4096, "helper_section")
        self.raw = raw

    def window(self, va, size):
        rva = va-self.base
        require(size > 0 and rva >= 0, "window_address", va=hex(va), size=size)
        for section in self.sections:
            extent = section["vsize"] or section["rawSize"]
            if section["rva"] <= rva and rva+size <= section["rva"]+extent:
                delta = rva-section["rva"]
                offset = section["rawPtr"]+delta
                require(delta+size <= section["rawSize"] and offset+size <= len(self.raw),
                        "window_raw_bounds", va=hex(va), size=size)
                blob = self.raw[offset:offset+size]
                return blob, dict(va=hex(va), size=size, section=section["name"],
                                  fileOffset=hex(offset), sha256=hashlib.sha256(blob).hexdigest())
        raise Blocked("window_unmapped", va=hex(va), size=size)


def read_build_receipt():
    raw = BUILD_RECEIPT.read_bytes()
    data = json.loads(raw.decode("utf-8-sig"))
    require(isinstance(data, dict), "build_receipt_object")
    expected = dict(baseSha256=BASELINE.sha256, candidateSha256=CANDIDATE.sha256,
                    size=WANT_SIZE, helperAddress=hex(HELPER), helperSize=251,
                    helperSha256=CANDIDATE.helper_sha256, oldHelperSize=249,
                    oldHelperSha256=BASELINE.helper_sha256, changedByteCount=187,
                    checksum=WANT_CHECKSUM)
    for key, value in expected.items():
        require(data.get(key) == value, "build_receipt_field", field=key,
                actual=data.get(key), expected=value)
    return dict(path=str(BUILD_RECEIPT), sha256=hashlib.sha256(raw).hexdigest(),
                matchedFields=expected,
                scope="receipt fields checked; no whole-image diff or checksum recomputation")


def native_windows(image):
    layout = image.layout
    specs = [
        (SITE, 5, "e9912eda05"),
        (HELPER, layout.helper_size, None),
        (ACTOR_INFO, 4, "8d4130c3"),
        (Y_GET, 8, "8b8180000000c3cc"),
        (X_GET, 4, "8b417cc3"),
    ]
    if layout.canonical_getter:
        specs.append((SELF_GET, SELF_GET_SIZE, SELF_GET_HEX))
    blobs, metadata = {}, []
    for va, size, expected in specs:
        blob, info = image.window(va, size)
        if expected is None:
            require(info["sha256"] == layout.helper_sha256, "helper_sha256", **info)
        else:
            require(blob == bytes.fromhex(expected), "window_bytes", va=hex(va),
                    expected=bytes.fromhex(expected).hex(), actual=blob.hex())
        blobs[va] = blob
        metadata.append(info)
    if layout.canonical_getter:
        call = blobs[HELPER][0x3A:0x3F]
        require(call[0] == 0xE8
                and HELPER+0x3F+struct.unpack_from("<i", call, 1)[0] == SELF_GET,
                "canonical_getter_call")
        blob = blobs[SELF_GET]
        selector = PROTECTED_THIS & 0xF
        table_index = blob[0xC0+selector]
        branch = struct.unpack_from("<I", blob, 0xAC+table_index*4)[0]
        require(selector == 8 and table_index == 2 and branch == 0x296DDD6,
                "fixed_address_jump_tables")
    else:
        require(blobs[HELPER][0x3A:0x3D] == bytes.fromhex("8b4170"),
                "baseline_cached_self_load")
    return blobs, metadata


# Original-helper instruction addresses. Layout.address relocates each once.
REJECTS = {
    "control_null": (0x6EAE00B, []),
    "model_present": (0x6EAE019, []),
    "actor_not_sentinel": (0x6EAE026, []),
    "scene_null": (0x6EAE034, []),
    "cached_self_null": (0x6EAE03F, []),
    "cached_self_other": (0x6EAE047, []),
    "raid_null": (0x6EAE055, []),
    "raid_info_null": (0x6EAE062, ["raid-fixture"]),
    "aux_miss": (0x6EAE081, ["raid-fixture", "lookup-fixture"]),
    "aux_key_mismatch": (0x6EAE091, ["raid-fixture", "lookup-fixture"]),
    "aux_sentinel": (0x6EAE09B, ["raid-fixture", "lookup-fixture"]),
    "aux_payload_zero": (0x6EAE0A6, ["raid-fixture", "lookup-fixture"]),
}
EARLY_GATES = {"control_null", "model_present", "actor_not_sentinel", "scene_null"}
HIT_EVENTS = ["raid-fixture", "lookup-fixture", "actor-info-native",
              "y-getter-native", "x-getter-native", "draw-fixture"]


@dataclass(frozen=True)
class Case:
    name: str
    variant: str
    x: int = 101
    y: int = 202
    flags: int = 0x246
    initial_cache: int = ACTOR
    active_result: int = OTHER  # Poison value when a valid cache should bypass it.
    repeats: int = 1
    group: str = "retained-38"


def make_cases():
    retained = []
    for xy_name, x, y in (("positive", 101, 202), ("negative", -20, -30),
                          ("zero", 0, 0), ("wrap", 0x80000008, 0x80000001)):
        for flags in (0x202, 0x246, 0xAD7):
            retained.append(Case(f"hit-{xy_name}-{flags:x}", "hit", x, y, flags))
    for variant in REJECTS:
        for flags in (0x246, 0xAD7):
            cache = 0 if variant == "cached_self_null" else (
                OTHER if variant == "cached_self_other" else ACTOR)
            retained.append(Case(f"reject-{variant}-{flags:x}", variant, flags=flags,
                                 initial_cache=cache, active_result=0))
    retained.extend((Case("hit-zero-namespace", "zero_namespace"),
                     Case("hit-zero-image-value", "zero_image")))
    require(len(retained) == 38, "retained_case_count")
    additions, controls = [], []
    for flags in (0x246, 0xAD7):
        for label, cached, active in (
            ("cold-active-actor", 0, ACTOR),
            ("cold-active-zero", 0, 0),
            ("cold-active-other", 0, OTHER),
            ("cached-actor", ACTOR, OTHER),
            ("cached-other", OTHER, ACTOR),
        ):
            variant = "cached_self_null" if cached == 0 else (
                "cached_self_other" if cached == OTHER else "hit")
            additions.append(Case(f"{label}-{flags:x}", variant, flags=flags,
                                  initial_cache=cached, active_result=active,
                                  repeats=2, group="cache-sequence"))
        # Exact same synthetic inputs as candidate cold-active-actor, but the
        # pinned old helper must reject before any active actor lookup/draw.
        controls.append(Case(f"baseline-cold-active-actor-{flags:x}", "cached_self_null",
                             flags=flags, initial_cache=0, active_result=ACTOR,
                             repeats=2, group="baseline-negative-control"))
    candidate = retained+additions
    require(len(candidate) == 48 and len(controls) == 2
            and len({c.name for c in candidate+controls}) == 50, "case_matrix")
    return candidate, controls


class Machine:
    def __init__(self, layout, blobs, case):
        self.layout, self.case = layout, case
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        # Load the full getter/table window, but never permit executing tables.
        self.native_ranges = [
            (va, 0x296DE2A if va == SELF_GET else va+len(blob))
            for va, blob in blobs.items()
        ]
        for va, blob in blobs.items():
            self.put(va, blob)
        self.boundaries = {RAID_GET, LOOKUP, IMAGE_DRAW, NORMAL, DONE,
                           ACTIVE_GET, *DECODER_RETURNS}
        for va in self.boundaries:
            self.put(va, b"\xcc")
        for base in (ACTOR, AUX, SCENE, RAID, RAID_INFO, CONTROL, VTABLE, OTHER):
            self.put(base, bytes(0x1000))
        self.put(0x56A3000, bytes(0x1000))
        self.put(0x57B7000, bytes(0x1000))
        self.put(STACK-0x2000, bytes(0x8000))
        self.w(ACTOR, VTABLE)
        self.w(ACTOR+0x20, 0xABCD0000 | KEY)
        self.w(ACTOR+0x34, 0x10)
        self.w(SCENE_GLOBAL, SCENE)
        self.w(SCENE+0x70, case.initial_cache)
        self.w(RAID_GLOBAL, RAID)
        self.w(PROTECTED_THIS, 0x13579BDF)  # Fixture storage, not real ciphertext.
        self.w(PROTECTED_CELL, 0x2468ACE0)
        self.w(AUX+0x20, 0xBEEF0000 | KEY)
        self.w(AUX+0x30, 0x11223344)  # Nonzero gate, deliberately not a pointer.
        self.image_value = 0 if case.variant == "zero_image" else IMAGE_VALUE
        self.w(AUX+0x34, self.image_value)
        self.owner = 0 if case.variant == "zero_namespace" else OWNER
        self.route = 0 if case.variant == "zero_namespace" else ROUTE
        self.put(RAID_INFO+2, bytes((self.owner, self.route)))
        self.w(CONTROL+0x7C, case.x)
        self.w(CONTROL+0x80, case.y)
        self.control = 0 if case.variant == "control_null" else CONTROL
        self.w(FRAME-0x74, self.control)
        edits = {
            "model_present": (ACTOR+0x4F4, OTHER),
            "actor_not_sentinel": (ACTOR+0x34, IMAGE_VALUE),
            "scene_null": (SCENE_GLOBAL, 0),
            "raid_null": (RAID_GLOBAL, 0),
            "aux_key_mismatch": (AUX+0x20, 0xBEEF0000 | (KEY+1)),
            "aux_sentinel": (AUX+0x34, 0x10),
            "aux_payload_zero": (AUX+0x30, 0),
        }
        if case.variant in edits:
            self.w(*edits[case.variant])
        self.stack_guard = bytes(range(64))
        self.put(STACK, self.stack_guard)
        self.watched = [(base, 0x1000) for base in (
            ACTOR, AUX, SCENE, RAID, RAID_INFO, CONTROL, VTABLE, OTHER,
            0x56A3000, 0x57B7000)] + [(FRAME-0x1000, 0x2000)]
        self.saved = {UC_X86_REG_EBX: ACTOR, UC_X86_REG_EDI: 0x77773333,
                      UC_X86_REG_EBP: FRAME, UC_X86_REG_ESP: STACK}
        self.invocation = 0
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_WRITE, self.memory_write)

    def check(self, condition, code, **detail):
        require(condition, code, layout=self.layout.name, case=self.case.name,
                invocation=self.invocation, **detail)

    def put(self, va, blob):
        for page in range(va & ~0xFFF, (va+len(blob)+0xFFF) & ~0xFFF, 0x1000):
            if page not in self.pages:
                self.u.mem_map(page, 0x1000)
                self.pages.add(page)
        self.u.mem_write(va, blob)

    def w(self, va, value):
        self.put(va, p32(value))

    def r(self, va):
        return struct.unpack("<I", bytes(self.u.mem_read(va, 4)))[0]

    def fixture_return(self, expected_return, value, pop_bytes=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        self.check(self.r(sp) == expected_return, "fixture_return_address",
                   expected=hex(expected_return), actual=hex(self.r(sp)))
        self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_ECX, 0xCCCCDDDD)
        self.u.reg_write(UC_X86_REG_EDX, 0xEEEEFFFF)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x202)
        self.u.reg_write(UC_X86_REG_ESP, sp+4+pop_bytes)
        self.u.reg_write(UC_X86_REG_EIP, expected_return)

    def memory_write(self, _u, _access, address, size, value, _data):
        # PUSH/CALL and the getter's local output use caller scratch below ESP.
        # Outside that stack, allow only the real getter's single cache store.
        if STACK-0x1000 <= address and address+size <= STACK:
            return
        pc = self.u.reg_read(UC_X86_REG_EIP)
        self.check(self.expect_active and pc == CACHE_STORE
                   and address == SCENE+0x70 and size == 4
                   and (value & MASK32) == self.case.active_result,
                   "unexpected_native_data_write", eip=hex(pc), address=hex(address),
                   size=size, value=hex(value & MASK32))
        self.cache_writes.append(dict(eip=hex(pc), address=hex(address), size=size,
                                      value=hex(value & MASK32)))

    def hook(self, _u, address, size, _data):
        previous, self.previous = self.previous, address
        self.check(address in self.boundaries or any(
            lo <= address and address+size <= hi for lo, hi in self.native_ranges),
            "unexpected_execution", eip=hex(address), size=size)
        relocated = self.layout.address
        if address == relocated(0x6EAE0ED):
            self.misses.append(previous)
        sp = self.u.reg_read(UC_X86_REG_ESP)
        ecx = self.u.reg_read(UC_X86_REG_ECX)
        if address == SELF_GET:
            self.check(self.reached_self and ecx == SCENE
                       and self.r(sp) == HELPER+0x3F, "self_getter_call")
            self.events.append("self-getter-native")
        elif self.layout.canonical_getter and address == HELPER+0x3F:
            actual = self.u.reg_read(UC_X86_REG_EAX)
            self.check(actual == self.expected_cache, "self_getter_result",
                       expected=hex(self.expected_cache), actual=hex(actual))
            self.self_results.append(actual)
        elif address in DECODER_RETURNS:
            self.check(self.expect_active and address == SELECTED_DECODER,
                       "protected_decoder_selection", actual=hex(address))
            first, output = self.r(sp+4), self.r(sp+8)
            self.check(ecx == PROTECTED_THIS and first == PROTECTED_CELL,
                       "protected_decoder_arguments", this=hex(ecx), first=hex(first))
            expected_output = self.u.reg_read(UC_X86_REG_EBP)-4
            self.check(output == expected_output and STACK-0x1000 <= output
                       and output+4 <= STACK, "protected_decoder_output", output=hex(output))
            # The only fixture memory write: emulate the decoder's out-parameter.
            self.u.mem_write(output, p32(KEY))
            self.decoder_calls.append(dict(entry=hex(address), this=hex(ecx),
                                           args=[hex(first), hex(output)], outputKey=KEY))
            self.events.append(DECODER_EVENT)
            self.fixture_return(DECODER_RETURNS[address], 0xDEC0DE01, pop_bytes=8)
        elif address == ACTIVE_GET:
            self.check(self.expect_active and self.r(sp+4) == KEY,
                       "active_getter_argument", actual=self.r(sp+4), expected=KEY)
            self.events.append("active-getter-fixture")
            self.active_calls.append(dict(key=self.r(sp+4), result=hex(self.case.active_result)))
            self.fixture_return(0x296DE1C, self.case.active_result)
        elif address == RAID_GET:
            self.check(ecx == RAID, "raid_this")
            self.events.append("raid-fixture")
            value = 0 if self.case.variant == "raid_info_null" else RAID_INFO
            self.fixture_return(relocated(0x6EAE060), value)
        elif address == LOOKUP:
            args = [self.r(sp+4*i) for i in range(1, 4)]
            self.check(args == [KEY, self.owner, self.route], "lookup_arguments", actual=args)
            self.events.append("lookup-fixture")
            value = 0 if self.case.variant == "aux_miss" else AUX
            self.fixture_return(relocated(0x6EAE07C), value)
        elif address in (ACTOR_INFO, Y_GET, X_GET):
            receiver, label, caller = {
                ACTOR_INFO: (AUX, "actor-info-native", 0x6EAE0B3),
                Y_GET: (CONTROL, "y-getter-native", 0x6EAE0BE),
                X_GET: (CONTROL, "x-getter-native", 0x6EAE0CB),
            }[address]
            self.check(ecx == receiver and self.r(sp) == relocated(caller),
                       "native_getter_call", address=hex(address), this=hex(ecx),
                       returnAddress=hex(self.r(sp)))
            self.events.append(label)  # Observe; execute the pinned native bytes.
        elif address == IMAGE_DRAW:
            args = [self.r(sp+4*i) for i in range(1, 8)]
            expected = [(self.case.x-17) & MASK32, (self.case.y-2) & MASK32,
                        14, 14, self.image_value, 0, 255]
            self.check(args == expected, "draw_arguments", actual=args, expected=expected)
            self.draws.append(args)
            self.events.append("draw-fixture")
            self.fixture_return(relocated(0x6EAE0E1), 0xAAAABBBB)
        elif address in (NORMAL, DONE):
            self.exits.append(address)
            self.u.emu_stop()

    def run_once(self):
        self.invocation += 1
        self.events, self.draws, self.exits, self.misses = [], [], [], []
        self.decoder_calls, self.active_calls, self.cache_writes, self.self_results = [], [], [], []
        self.previous = None
        cache_before = self.r(SCENE+0x70)
        self.reached_self = self.layout.canonical_getter and self.case.variant not in EARLY_GATES
        self.expect_active = self.reached_self and cache_before == 0
        self.expected_cache = self.case.active_result if self.expect_active else cache_before
        if self.case.variant in EARLY_GATES:
            gate = self.case.variant
        elif self.expected_cache == 0:
            gate = "cached_self_null"
        elif self.expected_cache != ACTOR:
            gate = "cached_self_other"
        elif self.case.variant in REJECTS and self.case.variant not in {
            "cached_self_null", "cached_self_other"
        }:
            gate = self.case.variant
        else:
            gate = None
        prefix = ["self-getter-native"] if self.reached_self else []
        if self.expect_active:
            prefix += [DECODER_EVENT, "active-getter-fixture"]
        expected_events = prefix+(REJECTS[gate][1] if gate else HIT_EVENTS)
        snapshots = [(va, bytes(self.u.mem_read(va, size))) for va, size in self.watched]
        for reg, value in self.saved.items():
            self.u.reg_write(reg, value)
        for reg, value in ((UC_X86_REG_EAX, 0x11112222), (UC_X86_REG_ECX, 0x33334444),
                           (UC_X86_REG_EDX, 0x55556666), (UC_X86_REG_ESI, 0x9999AAAA)):
            self.u.reg_write(reg, value)
        self.u.reg_write(UC_X86_REG_EFLAGS, self.case.flags)
        self.check(self.u.reg_read(UC_X86_REG_EFLAGS) == self.case.flags, "initial_flags")
        try:
            self.u.emu_start(SITE, 0, count=1000)
        except UcError as exc:
            raise Blocked("unicorn_execution", layout=self.layout.name, case=self.case.name,
                          invocation=self.invocation, eip=hex(self.u.reg_read(UC_X86_REG_EIP)),
                          error=str(exc), events=self.events) from exc
        target = NORMAL if gate else DONE
        self.check(self.exits == [target] and self.u.reg_read(UC_X86_REG_EIP) == target,
                   "continuation", actual=[hex(x) for x in self.exits], expected=hex(target))
        if gate:
            branch = self.layout.address(REJECTS[gate][0])
            self.check(self.misses == [branch], "rejection_gate",
                       actual=[hex(x) for x in self.misses], expected=hex(branch))
            self.check(self.draws == [], "rejected_case_drew")
            self.check(self.u.reg_read(UC_X86_REG_ECX) == self.control, "replayed_ecx")
        else:
            self.check(not self.misses and len(self.draws) == 1, "hit_draw_count")
        self.check(self.events == expected_events, "call_order",
                   actual=self.events, expected=expected_events)
        count = int(self.expect_active)
        self.check(len(self.active_calls) == len(self.decoder_calls) == len(self.cache_writes) == count,
                   "active_lookup_and_cache_write_count")
        self.check(self.self_results == ([self.expected_cache] if self.reached_self else []),
                   "self_getter_return_count")
        self.check(self.r(SCENE+0x70) == self.expected_cache, "cache_after",
                   actual=hex(self.r(SCENE+0x70)), expected=hex(self.expected_cache))
        for reg, value in self.saved.items():
            self.check(self.u.reg_read(reg) == value, "preserved_register", register=reg,
                       expected=hex(value), actual=hex(self.u.reg_read(reg)))
        # MOV ESI,[EBX] precedes the helper save; caller's incoming ESI is replaced.
        self.check(self.u.reg_read(UC_X86_REG_ESI) == VTABLE, "replayed_esi")
        self.check(self.u.reg_read(UC_X86_REG_EFLAGS) == self.case.flags, "preserved_flags",
                   expected=hex(self.case.flags), actual=hex(self.u.reg_read(UC_X86_REG_EFLAGS)))
        self.check(bytes(self.u.mem_read(STACK, 64)) == self.stack_guard, "stack_guard")
        for va, before in snapshots:
            expected = bytearray(before)
            if va == SCENE:
                expected[0x70:0x74] = p32(self.expected_cache)
            self.check(bytes(self.u.mem_read(va, len(before))) == bytes(expected),
                       "unexpected_data_or_frame_change", address=hex(va))
        return dict(invocation=self.invocation, passed=True, exit=hex(target), gate=gate,
                    rejectionBranch=hex(self.misses[0]) if gate else None,
                    cacheBefore=hex(cache_before), cacheAfter=hex(self.r(SCENE+0x70)),
                    activeLookupExpected=self.expect_active, activeLookupCalls=self.active_calls,
                    decoderCalls=self.decoder_calls, nativeCacheWrites=self.cache_writes,
                    selfGetterResults=[hex(x) for x in self.self_results],
                    events=self.events, drawArguments=self.draws,
                    preserved=["EBX", "EDI", "EBP", "ESP", "EFLAGS"], replayedESI=hex(VTABLE),
                    missECX=hex(self.control) if gate else None, stackGuardUnchanged=True,
                    dataAndFrameUnchangedExceptExpectedScene70=True)

    def run(self):
        # One Uc instance and unchanged synthetic objects across both calls.
        # Only entry registers are reset; scene+70 is never reset by the fixture.
        rows = [self.run_once() for _ in range(self.case.repeats)]
        return dict(name=self.case.name, group=self.case.group, layout=self.layout.name,
                    variant=self.case.variant, controlXY=[self.case.x, self.case.y],
                    flags=hex(self.case.flags), initialCache=hex(self.case.initial_cache),
                    activeFixtureResult=hex(self.case.active_result), passed=True,
                    invocations=rows)


def main():
    global BASELINE, CANDIDATE, BUILD_RECEIPT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True,
                        help="new JSON result in an existing directory; exclusive create only")
    parser.add_argument("--input", type=Path, required=True, help="fixed published v160 EXE")
    parser.add_argument("--candidate", type=Path, required=True, help="fixed candidate EXE")
    parser.add_argument("--build-receipt", type=Path, required=True)
    args = parser.parse_args()
    BASELINE = replace(BASELINE, exe=args.input.resolve())
    CANDIDATE = replace(CANDIDATE, exe=args.candidate.resolve())
    BUILD_RECEIPT = args.build_receipt.resolve()
    receipt = args.receipt.resolve()
    require(not receipt.exists(), "receipt_exists", path=str(receipt))
    require(receipt.parent.is_dir(), "receipt_parent_missing", path=str(receipt.parent))
    build = read_build_receipt()
    candidate_cases, baseline_cases = make_cases()
    images, all_rows = [], []
    for layout, cases in ((CANDIDATE, candidate_cases), (BASELINE, baseline_cases)):
        image = PeImage(layout)
        blobs, windows = native_windows(image)
        rows = [Machine(layout, blobs, case).run() for case in cases]
        images.append(dict(layout=layout.name, exe=str(layout.exe), sha256=image.sha256,
                           size=WANT_SIZE, checksumField=image.checksum, windows=windows,
                           caseCount=len(rows), invocationCount=sum(len(r["invocations"]) for r in rows)))
        all_rows.extend(rows)
    invocations = [v for row in all_rows for v in row["invocations"]]
    require(len(all_rows) == 50 and len(invocations) == 62, "executed_matrix_count")
    require([i["invocationCount"] for i in images] == [58, 4], "executed_layout_counts")
    result = dict(
        passed=True, candidateVersion="v2", buildReceipt=build, images=images,
        caseCount=len(all_rows), invocationCount=len(invocations), retainedGateCaseCount=38,
        hitInvocationCount=sum(v["exit"] == hex(DONE) for v in invocations),
        rejectionInvocationCount=sum(v["exit"] == hex(NORMAL) for v in invocations),
        verifierSha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), cases=all_rows,
        actual=["110B16A dispatch", "candidate 6EAE000 helper: exactly 251 bytes",
                "old-helper negative control: exactly 249 bytes from pinned v160",
                "296DD80 native getter: exact 0xD0 bytes including 296DE2C/296DE40 tables",
                "296DE1F native scene+70 store (including active result zero)",
                "3C1BEA0 actor-info address getter", "37E9F40 and 209BBE0 coordinate getters"],
        fixtures=[
            "37DF550/37DF5C0/37DF630/37DF6A0/37DF7F0 protected-key decoder boundaries: "
            "check ECX=57B7218, first arg=57B721C, write KEY through second arg, ret 8; "
            "fixed native selector 8 executes only 37DF630",
            "296D730 active actor lookup: check cdecl KEY argument; return ACTOR/0/OTHER",
            "1A553E0 raid-context result; 1A5A5E0 aux lookup result",
            "2966C60 image draw leaf: check seven arguments, no rendering",
            "synthetic actor/aux/control/scene/raid objects, protected storage and caller frame"],
        boundaries=[
            "cached self ACTOR or OTHER bypasses protected decode/active lookup",
            "cold ACTOR draws and stores only scene+70 outside stack; second call uses cache",
            "cold OTHER rejects with scene+70=OTHER; second call does not query active actor",
            "cold zero rejects and stores zero; second call queries again because cache remains empty",
            "successful repeats still perform raid/aux lookup and draw; only active actor lookup is skipped",
            "baseline same-input cold ACTOR cases reject, do not query active actor, and do not draw"],
        limitations=[
            "no protected decoder implementation or active/aux registry lookup implementation executed",
            "only fixed-address-selected decoder branch executes; five fixture entries are not five native paths",
            "stops before 110B16F/110B199 continuations; no model creation, registration or lifecycle",
            "does not test actor-null caller branch before the helper",
            "no real cache contents, resource loading, GPU, live process or GUI acceptance",
            "zero image success means forwarding the argument, not visible pixels",
            "checksum field and fixed SHA checked; PE checksum arithmetic and 187-byte diff not recomputed"],
        processAccess=False, productModified=False, guiAccepted=False)
    # Open only after all checks pass. Exclusive creation also rejects a race.
    with receipt.open("x", encoding="utf-8") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print(json.dumps(dict(passed=True, cases=len(all_rows), invocations=len(invocations),
                          receipt=str(receipt))))


if __name__ == "__main__":
    try:
        main()
    except Blocked as exc:
        print(json.dumps(exc.payload), file=sys.stderr)
        raise SystemExit(2)
    except (OSError, ValueError) as exc:
        print(json.dumps(dict(status="blocked", code="input_or_file_io", error=str(exc))),
              file=sys.stderr)
        raise SystemExit(2)
