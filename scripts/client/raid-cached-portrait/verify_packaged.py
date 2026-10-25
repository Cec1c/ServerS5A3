"""Bounded Unicorn verification of the controller-built .rport PE, read-only.

Executes TEST/JE, helper, context getter and typed lookup bytes from the EXE.
Only native map find/end are stubbed. Stops before either original row endpoint.
No builder/emitter import, game, renderer, network, installation or deployment.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import struct
import sys

sys.dont_write_bytecode = True
UC = X86 = None  # Loaded only by main(), optionally from an explicit local --deps.
EXPECTED_SHA = "8663331cf2e217185dfa220a23e9fce4688a69d413f66eb34736ae347d15fd9a"
ORIGINAL_SHA = "922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b"
ORIGINAL_SIZE = 111906816
IMAGE_BASE = 0x400000
SITE, HIT, MISS = 0x110B12E, 0x110B134, 0x110B352
SITE_OFFSET = 0xD0B12E
HELPER_BASE, HELPER_SIZE = 0x6EC6000, 196
GETTER, LOOKUP = 0x1A553E0, 0x1A5A550
MAP_FIND, MAP_END = 0x224D950, 0x224D8D0
CONTEXT_GLOBAL = 0x56A3778
TABLE_BASE, TABLE_STRIDE = 0x5676898, 0x38
VTABLE = 0x49ED1DC
STACK = 0x70100000
MANAGER, ROW, ACTOR = 0x30000000, 0x30001000, 0x30002000
PROFILE, MODEL = 0x30003000, 0x30004000
ROOT, TOKEN, NODE, END = 0x30005000, 0x30005020, 0x30005040, 0x30005060
DECOY = 0x30006000
REGISTERS = ("eax", "ebx", "ecx", "edx", "esi", "edi", "ebp", "esp", "eflags", "eip")
WINDOWS = {
    GETTER: (31, "4ebe09c91b02b96c2c61ae694973382a4ce969816c7b1412782e8a7e53db8ad7"),
    LOOKUP: (144, "93cf8ce8c93a040d082242d68f47e7fbe0b98d859b2baecc4653bff85c1269b2"),
    VTABLE: (8, "9294ea045815406e9d07dc06810ac1c5df16ef592bb34ca640bbfede661d4fba"),
}
BOUNDARY = {
    "executed_from_candidate": ["0x0110b12c TEST and patched JE", ".rport helper",
                                "0x01a553e0 cached/empty context paths", "0x01a5a550 typed category lookup"],
    "stubs": {"0x0224d950": "synthetic map find + iterator, real ret 8 ABI",
              "0x0224d8d0": "synthetic map end + iterator, real ret 4 ABI"},
    "synthetic": ["manager and separate old-row context", "category maps and checked iterators",
                  "actors, model/profile pointers, registers and stack"],
    "not_executed": ["getter lazy path through 0x01a55350", "native map internals",
                     "row bodies at/after 0x0110b134 and 0x0110b352", "model factory and renderer",
                     "real resource loading, GPU/UI and game lifecycle"],
    "open_issues": ["current raid/member versus old 584 context association is unproven",
                    "matching category/key/owner/route does not prove actor freshness or ownership",
                    "real actor/model/profile/UI resource lifetime and game acceptance remain open"],
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def digest_file(path):
    with path.open("rb") as stream:
        digest = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def u32(data, at=0):
    return struct.unpack_from("<I", data, at)[0]


def packed_identity(key, owner, route):
    return (route << 24) | (owner << 16) | key


def read_package(path, manifest_path):
    require(path.stat().st_size == ORIGINAL_SIZE + 4096, "candidate size differs")
    data = path.read_bytes()
    require(sha(data) == EXPECTED_SHA, "candidate differs from controller-approved SHA256")
    receipt_bytes = manifest_path.read_bytes()
    receipt = json.loads(receipt_bytes)
    require(receipt["input"] == dict(sha256=ORIGINAL_SHA, size=ORIGINAL_SIZE), "manifest input differs")
    require(receipt["output"] == dict(sha256=EXPECTED_SHA, size=len(data)), "manifest output differs")
    require(receipt["pe_section_name"] == ".rport", "manifest section differs")
    require(data[:2] == b"MZ" and u32(data, 0x3C) == 0x130 and data[0x130:0x134] == b"PE\0\0", "PE header differs")
    machine, count = struct.unpack_from("<HH", data, 0x134)
    require(machine == 0x14C and count == 48 and struct.unpack_from("<H", data, 0x144)[0] == 224,
            "PE architecture/section count/optional size differs")
    require(struct.unpack_from("<H", data, 0x148)[0] == 0x10B and u32(data, 0x164) == IMAGE_BASE, "PE32/base differs")
    require(u32(data, 0x168) == u32(data, 0x16C) == u32(data, 0x184) == 4096, "PE alignment/header size differs")
    require(u32(data, 0x180) == 0x6AC7000, "SizeOfImage differs")
    require(sha(data[0x228:0x980]) == "83c18f91f9df42dbc0bcecbc99663ec9b9ca7a6e952dd25dc988ca9785b17dc8",
            "original section table differs")
    sections = []
    raw_end = virtual_end = 4096
    for index in range(count):
        at = 0x228 + 40 * index
        vs, rva, rs, raw = struct.unpack_from("<IIII", data, at + 8)
        require(vs > 0 and rs > 0 and rva % 4096 == raw % 4096 == rs % 4096 == 0, "invalid section layout")
        require(raw >= raw_end and raw + rs <= len(data) and rva >= virtual_end, "overlapping/unbacked section")
        raw_end = raw + rs
        virtual_end = (rva + max(vs, rs) + 4095) & ~4095
        sections.append(dict(name=data[at:at + 8], vs=vs, rva=rva, raw=raw, rs=rs, flags=u32(data, at + 36)))
    require(raw_end == len(data) and virtual_end == 0x6AC7000, "overlay/image extent differs")
    require(sections[-1] == dict(name=b".rport\0\0", vs=196, rva=0x6AC6000, raw=ORIGINAL_SIZE,
                                 rs=4096, flags=0x60000020), "appended .rport differs")

    def read(va, size):
        matches = [s["raw"] + va - IMAGE_BASE - s["rva"] for s in sections
                   if 0 <= va - IMAGE_BASE - s["rva"] and va - IMAGE_BASE - s["rva"] + size <= s["rs"]]
        require(len(matches) == 1, f"ambiguous/unbacked VA {va:#x}")
        return data[matches[0]:matches[0] + size]

    branch = read(SITE, 6)
    require(read(SITE - 4, 4) == bytes.fromhex("8b d8 85 db"), "original MOV/TEST differs")
    require(branch[:2] == b"\x0f\x84" and SITE + 6 + struct.unpack("<i", branch[2:])[0] == HELPER_BASE,
            "actual packaged JE does not target .rport entry")
    helper = read(HELPER_BASE, HELPER_SIZE)
    require(receipt["branch"]["after_hex"] == branch.hex() and int(receipt["branch"]["target"], 16) == HELPER_BASE,
            "manifest branch differs from EXE")
    require(receipt["helper"]["size"] == HELPER_SIZE and receipt["helper"]["sha256"] == sha(helper),
            "manifest helper differs from EXE")
    require(data[ORIGINAL_SIZE + HELPER_SIZE:] == bytes(4096 - HELPER_SIZE), "nonzero appended padding")
    # Hash-only normalization of actual packaged bytes, NEVER an executable blob.
    # These four sites are the original emitter's two calls and two outer jumps.
    normalized = bytearray(helper)
    for at, opcode, target in ((0x27, 0xE8, GETTER), (0x46, 0xE8, LOOKUP),
                               (0xB8, 0xE9, HIT), (0xBF, 0xE9, MISS)):
        require(helper[at] == opcode and HELPER_BASE + at + 5 + struct.unpack_from("<i", helper, at + 1)[0] == target,
                f"packaged helper external branch differs at +{at:#x}")
        struct.pack_into("<i", normalized, at + 1, target - (0x33010000 + at + 5))
    require(sha(normalized) == "45e05eaff1c12b23c36e7811c3b6e96818861d1a81875342175e4fe68ae84c28",
            "packaged helper instruction bytes differ from recorded reference")
    # Independently undo only the six allowed edit ranges in a temporary bytearray.
    # Nothing is written to the candidate or passed from this array to Unicorn.
    original = bytearray(data[:ORIGINAL_SIZE])
    restorations = [(0x136, bytes.fromhex("2f00")), (0x14C, bytes.fromhex("00c01a04")),
                    (0x180, bytes.fromhex("0060ac06")), (0x188, bytes.fromhex("4ab4ab06")),
                    (0x980, bytes(40)), (SITE_OFFSET, bytes.fromhex("0f841e020000"))]
    require(len(receipt["allowed_original_byte_ranges"]) == len(restorations), "manifest edit count differs")
    for (at, before), edit in zip(restorations, receipt["allowed_original_byte_ranges"]):
        require(edit["offset"] == at and edit["end_exclusive"] == at + len(before)
                and edit["before_hex"] == before.hex() and edit["after_hex"] == data[at:at + len(before)].hex(),
                "manifest edit range differs from fixed original / packaged bytes")
        original[at:at + len(before)] = before
    require(sha(original) == ORIGINAL_SHA, "unapproved original-byte changes")
    blobs = {va: read(va, size) for va, (size, _) in WINDOWS.items()}
    for va, (_, expected) in WINDOWS.items():
        require(sha(blobs[va]) == expected, f"native window changed at {va:#x}")
    blobs[HELPER_BASE] = helper
    blobs[SITE - 2] = read(SITE - 2, MISS + 16 - (SITE - 2))
    evidence = dict(candidate_sha256=sha(data), candidate_size=len(data), manifest_sha256=sha(receipt_bytes),
                    section=".rport", section_count=48, helper_va=hex(HELPER_BASE), helper_size=HELPER_SIZE,
                    helper_sha256=sha(helper), je_va=hex(SITE), je_hex=branch.hex(), je_target=hex(HELPER_BASE),
                    restored_original_sha256=sha(original), normalized_reference_match=True,
                    byte_source="all executable windows except two map stubs read from this candidate",
                    windows=[dict(va=hex(va), size=len(blob), sha256=sha(blob)) for va, blob in blobs.items()])
    return blobs, evidence


def scenarios():
    rows = [
        ("nonnull_fallthrough", False, dict(ebx=ACTOR, manager=False)),
        ("nonnull_highbit_fallthrough", False, dict(ebx=0x80000001, manager=False)),
        ("null_lookup_miss", False, dict(map_mode="empty")),
        ("category1_hit_same_key_other_categories", True, {}),
        ("wrong_actor_key", False, dict(actor_key=0x2202)),
        ("wrong_actor_owner", False, dict(actor_owner=1)),
        ("wrong_actor_route", False, dict(actor_route=0)),
        ("wrong_actor_type_vptr", False, dict(actor_vptr=VTABLE + 4)),
        ("manager_absent", False, dict(manager=False)),
        ("cached_context_absent", False, dict(context=False)),
        ("key_zero", False, dict(key=0)),
        ("key_ffff", False, dict(key=0xFFFF)),
        ("map_wrong_key", False, dict(map_mode="wrong_key")),
        ("map_wrong_owner", False, dict(map_mode="wrong_owner")),
        ("map_wrong_route", False, dict(map_mode="wrong_route")),
        ("same_key_only_other_categories", False, dict(map_mode="other_categories")),
        ("model_empty_profile_absent", False, dict(model=0, profile=0)),
        ("model_empty_class16", False, dict(model=0, career=0x10)),
        ("model_empty_profile_fallback", True, dict(model=0)),
        ("nondefault_exact_namespace_hit", True, dict(owner=7, route=249, actor_owner=7, actor_route=249)),
    ]
    for name, hit, overrides in rows:
        for flags in (0x246, 0xED7):
            case = dict(name=f"{name}_flags_{flags:x}", hit=hit, ebx=0, key=0x2201, manager=True,
                        context=True, owner=0, route=1, actor_key=0x2201, actor_owner=0, actor_route=1,
                        actor_vptr=VTABLE, model=MODEL, profile=PROFILE, career=0x20, map_mode="exact", flags=flags)
            case.update(overrides)
            yield case


class Harness:
    def __init__(self, blobs, case):
        self.uc = UC.Uc(UC.UC_ARCH_X86, UC.UC_MODE_32)
        self.case, self.blobs = case, blobs
        self.pages, self.code_pages = set(), set()
        self.trace, self.events, self.writes = [], [], []
        self.branch_entry = self.endpoint = None
        self.reg_ids = {name: getattr(X86, "UC_X86_REG_" + name.upper()) for name in REGISTERS}
        for va, blob in blobs.items():
            self.put(va, blob, code=(va != VTABLE))
        self.put(MAP_FIND, bytes.fromhex("c2 08 00"), code=True)
        self.put(MAP_END, bytes.fromhex("c2 04 00"), code=True)
        self.put(STACK - 4096, b"\xa5" * 8192)
        for va in (MANAGER, ROW, ACTOR, PROFILE, MODEL, ROOT, DECOY):
            self.put(va, bytes(4096))
        self.put(CONTEXT_GLOBAL & ~4095, bytes(4096))
        self.put(TABLE_BASE & ~4095, bytes(4096))
        self.w32(CONTEXT_GLOBAL, MANAGER if case["manager"] else 0)
        self.w32(MANAGER + 4, 0xFFFFFFFF)  # Excludes the unverified lazy getter path.
        self.w32(MANAGER + 8, ROW if case["context"] else 0)
        self.uc.mem_write(ROW + 2, bytes((case["owner"], case["route"])))
        self.w32(ACTOR, case["actor_vptr"])
        self.uc.mem_write(ACTOR + 0x20, struct.pack("<H", case["actor_key"]))
        self.w32(ACTOR + 0x30, case["profile"])
        self.w32(ACTOR + 0x34, case["career"])
        self.w32(ACTOR + 0x4F4, case["model"])
        self.uc.mem_write(ACTOR + 0x604, bytes((case["actor_owner"], case["actor_route"])))
        self.w32(PROFILE, 0x41424344)
        self.w32(MODEL, 0x51525354)
        self.w32(ROOT, TOKEN)
        self.w32(TOKEN, 0x52535455)
        self.w32(TOKEN + 4, 0)
        # A valid-looking actor in other categories must never rescue category1 miss.
        self.uc.mem_write(DECOY, bytes(self.uc.mem_read(ACTOR, 0x700)))
        composite = packed_identity(case["key"], case["owner"], case["route"])
        self.maps = {n: {composite: DECOY} for n in range(5)}
        self.maps[1] = {composite: ACTOR}
        mode = case["map_mode"]
        if mode == "empty":
            self.maps = {n: {} for n in range(5)}
        elif mode == "other_categories":
            self.maps[1] = {}
        elif mode.startswith("wrong_"):
            changed = composite ^ {"wrong_key": 1, "wrong_owner": 0x10000, "wrong_route": 0x1000000}[mode]
            self.maps[1] = {changed: ACTOR}
        for page in self.code_pages:
            self.uc.mem_protect(page, 4096, UC.UC_PROT_READ | UC.UC_PROT_EXEC)
        self.allowed_code = [(SITE - 2, SITE + 6), (HELPER_BASE, HELPER_BASE + HELPER_SIZE),
                             (GETTER, GETTER + 31), (LOOKUP, LOOKUP + 144),
                             (MAP_FIND, MAP_FIND + 3), (MAP_END, MAP_END + 3)]
        self.hooks = [self.uc.hook_add(UC.UC_HOOK_CODE, self.on_code),
                      self.uc.hook_add(UC.UC_HOOK_MEM_WRITE, self.on_write)]

    def put(self, va, blob, code=False):
        for page in range(va & ~4095, (va + len(blob) + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.uc.mem_map(page, 4096, UC.UC_PROT_READ | UC.UC_PROT_WRITE)
                self.pages.add(page)
            if code:
                self.code_pages.add(page)
        self.uc.mem_write(va, blob)

    def r32(self, va):
        return u32(self.uc.mem_read(va, 4))

    def w32(self, va, value):
        self.uc.mem_write(va, struct.pack("<I", value & 0xFFFFFFFF))

    def regs(self):
        return {name: self.uc.reg_read(reg) for name, reg in self.reg_ids.items()}

    def watched(self):
        ranges = [(MANAGER, 4096), (ROW, 4096), (ACTOR, 4096), (PROFILE, 4096), (MODEL, 4096),
                  (DECOY, 4096), (CONTEXT_GLOBAL & ~4095, 4096), (TABLE_BASE & ~4095, 4096), (VTABLE, 8)]
        return {hex(va): bytes(self.uc.mem_read(va, size)) for va, size in ranges}

    def on_write(self, emu, access, address, size, value, user_data):
        self.writes.append(dict(address=hex(address), size=size))
        require(STACK - 1024 <= address and address + size <= STACK, "CPU write outside private stack scratch")

    def on_code(self, emu, address, size, user_data):
        self.trace.append(address)
        if address in (HIT, MISS):
            self.endpoint = address
            emu.emu_stop()
            return
        require(any(left <= address and address + size <= right for left, right in self.allowed_code),
                f"unexpected executable path {address:#x}")
        if address == SITE:
            require(self.branch_entry is None, "JE visited more than once")
            self.branch_entry = self.regs()
        elif address == GETTER:
            require(emu.reg_read(self.reg_ids["ecx"]) == MANAGER, "getter receiver differs")
            self.events.append(dict(kind="getter", receiver=MANAGER))
        elif address == LOOKUP:
            esp = emu.reg_read(self.reg_ids["esp"])
            self.events.append(dict(kind="lookup", key=self.r32(esp + 4), owner=self.r32(esp + 8),
                                    category=self.r32(esp + 12), route=self.r32(esp + 16)))
        elif address in (MAP_FIND, MAP_END):
            esp = emu.reg_read(self.reg_ids["esp"])
            out = self.r32(esp + 4)
            table = emu.reg_read(self.reg_ids["ecx"])
            delta = table - TABLE_BASE
            require(delta >= 0 and delta % TABLE_STRIDE == 0 and delta // TABLE_STRIDE < 5, "bad map table")
            category = delta // TABLE_STRIDE
            require(STACK - 1024 <= out and out + 12 <= STACK, "stub iterator outside stack scratch")
            node = END
            if address == MAP_FIND:
                key_ptr = self.r32(esp + 8)
                require(STACK - 1024 <= key_ptr < STACK - 3, "stub key outside stack scratch")
                composite = self.r32(key_ptr)
                actor = self.maps[category].get(composite, 0)
                if actor:
                    node = NODE
                    self.w32(NODE + 12, actor)
                self.events.append(dict(kind="find", category=category, table=hex(table),
                                        composite=composite, actor=actor))
            else:
                self.events.append(dict(kind="end", category=category, table=hex(table)))
            self.w32(out, ROOT)
            self.w32(out + 4, 0)
            self.w32(out + 8, node)
            emu.reg_write(self.reg_ids["eax"], out)
            # The packaged typed lookup executes after these real ret 8 / ret 4 stubs.

    def run(self):
        case = self.case
        initial = dict(eax=0x11112222, ebx=case["ebx"], ecx=0x33334444, edx=0x55556666,
                       esi=0x77778888, edi=0xA5A50000 | case["key"], ebp=STACK + 0x800,
                       esp=STACK, eflags=case["flags"])
        for name, value in initial.items():
            self.uc.reg_write(self.reg_ids[name], value)
        before = self.watched()
        upper_guard = bytes(self.uc.mem_read(STACK, 4096))
        lower_guard = bytes(self.uc.mem_read(STACK - 4096, 3072))
        error = None
        try:
            self.uc.emu_start(SITE - 2, 0, timeout=1_000_000, count=4096)
        except Exception as exc:
            error = f"{type(exc).__name__}: {exc}"
        final = self.regs()
        entry = self.branch_entry or {}
        nonnull = bool(case["ebx"])
        expected_endpoint = HIT if nonnull or case["hit"] else MISS
        expected_ebx = case["ebx"] if nonnull else (ACTOR if case["hit"] else 0)
        getters = int(not nonnull and case["key"] not in (0, 0xFFFF) and case["manager"])
        lookups = getters * int(case["context"])
        expected_actor = ACTOR if lookups and case["map_mode"] == "exact" else 0
        expected_composite = packed_identity(case["key"], case["owner"], case["route"])
        kinds = [event["kind"] for event in self.events]
        expected_events = (["getter"] if getters else []) + (["lookup", "find", "end"] if lookups else [])
        test_flags = ((0x40 if not nonnull else 0) | (0x80 if case["ebx"] & 0x80000000 else 0)
                      | (0x4 if (case["ebx"] & 0xFF).bit_count() % 2 == 0 else 0))
        checks = dict(
            no_emulation_error=error is None,
            bounded_run_completed=self.endpoint is not None and not self.uc.query(UC.UC_QUERY_TIMEOUT),
            actual_test_and_je_executed=self.trace[:2] == [SITE - 2, SITE],
            test_defined_flags=(entry.get("eflags", -1) & 0x8C5) == test_flags,
            endpoint=final["eip"] == self.endpoint == expected_endpoint,
            helper_entry_count=self.trace.count(HELPER_BASE) == (0 if nonnull else 1),
            ebx_contract=final["ebx"] == expected_ebx,
            flags_restored_to_je_entry=final["eflags"] == entry.get("eflags"),
            upper_caller_stack_preserved=bytes(self.uc.mem_read(STACK, 4096)) == upper_guard,
            lower_stack_guard_preserved=bytes(self.uc.mem_read(STACK - 4096, 3072)) == lower_guard,
            watched_objects_unchanged=self.watched() == before,
            mapped_candidate_bytes_unchanged=all(bytes(self.uc.mem_read(va, len(blob))) == blob for va, blob in self.blobs.items()),
            exact_call_order=kinds == expected_events,
            nonnull_has_no_calls_or_writes=not nonnull or (not self.events and not self.writes),
            lookup_arguments=all(e == dict(kind="lookup", key=case["key"], owner=case["owner"], category=1, route=case["route"])
                                 for e in self.events if e["kind"] == "lookup"),
            category1_only=all(e["category"] == 1 for e in self.events if "category" in e),
            map_identity_and_result=all(e["composite"] == expected_composite and e["actor"] == expected_actor
                                        for e in self.events if e["kind"] == "find"),
        )
        for name in ("eax", "ecx", "edx", "esi", "edi", "ebp", "esp"):
            checks[name + "_preserved"] = final[name] == entry.get(name) == initial[name]
        result = dict(name=case["name"], passed=all(checks.values()), checks=checks, error=error,
                      fixture=case, initial=initial, je_entry=entry, final=final,
                      expected_endpoint=hex(expected_endpoint), expected_ebx=hex(expected_ebx),
                      events=self.events, instruction_visits=len(self.trace), cpu_stack_write_count=len(self.writes),
                      trace_tail=[hex(va) for va in self.trace[-12:]])
        for hook in self.hooks:
            self.uc.hook_del(hook)
        return result


def main():
    global UC, X86
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path, help="actual controller-built .rport candidate EXE")
    parser.add_argument("--manifest", type=Path, help="defaults to changes.json beside the candidate")
    parser.add_argument("--deps", type=Path, help="optional local directory containing the installed unicorn package")
    parser.add_argument("--report", required=True, type=Path, help="new result JSON; never overwrite an existing path")
    args = parser.parse_args()
    candidate = args.input.resolve(strict=True)
    manifest_path = (args.manifest or candidate.with_name("changes.json")).resolve(strict=True)
    require(not os.path.lexists(args.report) and args.report.parent.is_dir(), "report must be a new file in an existing directory")
    report = dict(schema="p34-packaged-unicorn-v1", passed=False, boundary=BOUNDARY, cases=[],
                  candidate=str(candidate), manifest=str(manifest_path), verifier_sha256=digest_file(Path(__file__)),
                  p34_fixed=False, real_lifecycle_verified=False, game_accepted=False, installed=False, deployed=False)
    try:
        blobs, report["package"] = read_package(candidate, manifest_path)
        if args.deps:
            sys.path.insert(0, str(args.deps.resolve(strict=True)))
        import unicorn as UC
        from unicorn import x86_const as X86
        require(UC.__version__ == "2.1.4", "reviewed Unicorn version is 2.1.4")
        report["unicorn_version"] = UC.__version__
        report["limits"] = dict(instructions_per_case=4096, timeout_microseconds_per_case=1_000_000, serial=True)
        for case in scenarios():
            report["cases"].append(Harness(blobs, case).run())
        report["input_unchanged"] = digest_file(candidate) == report["package"]["candidate_sha256"]
        report["manifest_unchanged"] = digest_file(manifest_path) == report["package"]["manifest_sha256"]
        report["passed"] = (len(report["cases"]) == 40 and all(c["passed"] for c in report["cases"])
                            and report["input_unchanged"] and report["manifest_unchanged"])
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    report["summary"] = dict(total=len(report["cases"]), passed=sum(c["passed"] for c in report["cases"]),
                             failed=sum(not c["passed"] for c in report["cases"]))
    with args.report.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(report, stream, ensure_ascii=True, indent=2, sort_keys=True)
        stream.write("\n")
    print(json.dumps(dict(report=str(args.report), passed=report["passed"], summary=report["summary"],
                          error=report.get("error"), failed_cases=[c["name"] for c in report["cases"] if not c["passed"]])))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
