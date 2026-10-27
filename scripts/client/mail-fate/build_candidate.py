#!/usr/bin/env python3
"""Dry-run/apply planner for the two V179 client patches. No EXE is emitted by default."""

from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path


EXPECTED_SHA256 = "3d6f3c989b6407676ff8ce2b4a7b189a6dc079f2c84740313070826641f1f6b1"
EXPECTED_SIZE = 111919104
IMAGE_BASE = 0x00400000

PATCH_RVA = 0x01C43C7A
PATCH_VA = IMAGE_BASE + PATCH_RVA
PATCH_ORIGINAL = bytes.fromhex("8B CF E8 DF F3 99 00")
RESUME_VA = 0x02043C81

CAVE_RVA = 0x06AC6272
CAVE_VA = IMAGE_BASE + CAVE_RVA
CAVE_CAPACITY = 142

UI_GLOBAL = 0x056A3798
UI_GET = 0x032B5C90
SELECTED_SETTER = 0x029E7A90
CLEAR_ONE_MANAGER = 0x029E3060

GATE_FILE_OFFSET = 0x06A923B6
GATE_OPCODE = 1456
GATE_MASK = 1 << (GATE_OPCODE & 7)
GATE_CALL_VA = 0x02523A13
PREVIOUS_GATE_VA = 0x06EAD900
GATE_CAVE_VA = CAVE_VA + 80


class PatchError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise PatchError(message)


def rel32(target: int, next_address: int) -> bytes:
    delta = target - next_address
    require(-(1 << 31) <= delta < (1 << 31), f"relative target {target:#x} out of range")
    return struct.pack("<i", delta)


def build_hook() -> bytes:
    code = bytearray()

    def emit(*values: int) -> None:
        code.extend(values)

    def call(target: int) -> None:
        emit(0xE8)
        code.extend(rel32(target, CAVE_VA + len(code) + 4))

    emit(0x9C)              # pushfd
    emit(0x60)              # pushad
    emit(0x83, 0xEC, 0x0C)  # sub esp,12; preserve 16-byte stack phase
    # The native op95 path loads the manager pointer stored at UI_GLOBAL.
    # Passing the address of that global as this reads an unrelated selector.
    emit(0x8B, 0x0D)
    code.extend(struct.pack("<I", UI_GLOBAL))
    emit(0x6A, 0x00)
    emit(0x68)
    code.extend(struct.pack("<I", 0xBE))
    call(UI_GET)
    emit(0x85, 0xC0)
    je_at = len(code)
    emit(0x74, 0x00)
    emit(0x6A, 0x00)
    emit(0x8B, 0xC8)
    call(SELECTED_SETTER)
    no_window = len(code)
    code[je_at + 1] = no_window - (je_at + 2)
    require(0 <= code[je_at + 1] < 128, "short JE range changed")
    emit(0x83, 0xC4, 0x0C)
    emit(0x61)
    emit(0x9D)
    emit(0x8B, 0xCF)
    call(CLEAR_ONE_MANAGER)
    emit(0xE9)
    code.extend(rel32(RESUME_VA, CAVE_VA + len(code) + 4))
    require(len(code) <= CAVE_CAPACITY, f"hook {len(code)} > cave {CAVE_CAPACITY}")
    return bytes(code)


def pe_checksum(data: bytes) -> int:
    require(data[:2] == b"MZ", "not a PE file")
    pe_offset = struct.unpack_from("<I", data, 0x3C)[0]
    require(data[pe_offset : pe_offset + 4] == b"PE\0\0", "missing PE signature")
    checksum_offset = pe_offset + 24 + 64
    total = 0
    for index in range(0, len(data), 2):
        low = data[index]
        high = data[index + 1] if index + 1 < len(data) else 0
        if checksum_offset <= index < checksum_offset + 4:
            low = 0
        if checksum_offset <= index + 1 < checksum_offset + 4:
            high = 0
        total += low | (high << 8)
        total = (total & 0xFFFF) + (total >> 16)
    total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF


def build_gate() -> bytes:
    # The original bitset is shared across packet classes. Keep it intact;
    # extend the existing guild gate only for the class0 Fate notification.
    code = bytearray(bytes.fromhex("803f00"))  # cmp byte ptr [edi],0
    first = len(code)
    code.extend(bytes.fromhex("0f8500000000"))
    code.extend(bytes.fromhex("0fb7442404"))  # movzx eax,word ptr [esp+4]
    code.extend(b"\x3d" + struct.pack("<I", GATE_OPCODE))
    second = len(code)
    code.extend(bytes.fromhex("0f8500000000"))
    code.extend(bytes.fromhex("b801000000c20400"))
    fallback = len(code)
    code.extend(b"\xe9" + rel32(PREVIOUS_GATE_VA, GATE_CAVE_VA + fallback + 5))
    for at in (first, second):
        struct.pack_into("<i", code, at + 2, fallback - (at + 6))
    require(80 + len(code) <= CAVE_CAPACITY, "Fate gate exceeds reviewed cave")
    return bytes(code)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    if args.apply and args.output is None:
        raise SystemExit("--apply requires --output")
    if args.output and args.output.exists():
        raise SystemExit(f"output already exists: {args.output}")
    if args.receipt and args.receipt.exists():
        raise SystemExit(f"receipt already exists: {args.receipt}")

    import pefile  # type: ignore

    original = args.input.read_bytes()
    source_sha = hashlib.sha256(original).hexdigest()
    require(len(original) == EXPECTED_SIZE, f"unexpected input size {len(original)}")
    require(source_sha == EXPECTED_SHA256, f"unexpected input SHA256 {source_sha}")
    pe = pefile.PE(data=original, fast_load=True)
    require(pe.OPTIONAL_HEADER.ImageBase == IMAGE_BASE, "unexpected image base")
    native_lookup_offset = pe.get_offset_from_rva(0x029EE2CE - IMAGE_BASE)
    require(original[native_lookup_offset:native_lookup_offset+13] == bytes.fromhex("8B0D98376A056A0068BE000000"), "native op95 manager lookup contract changed")

    patch_offset = pe.get_offset_from_rva(PATCH_RVA)
    require(original[patch_offset : patch_offset + 7] == PATCH_ORIGINAL, "op97 patch-site bytes changed or already patched")
    cave_section = next((s for s in pe.sections if s.VirtualAddress <= CAVE_RVA < s.VirtualAddress + max(s.Misc_VirtualSize, s.SizeOfRawData)), None)
    require(cave_section is not None, "cave RVA outside sections")
    require(cave_section.Characteristics & 0x20000000, "cave section not executable")
    cave_offset = pe.get_offset_from_rva(CAVE_RVA)
    require(original[cave_offset : cave_offset + CAVE_CAPACITY] == b"\xCC" * CAVE_CAPACITY, "cave is not a full INT3 island")
    gate_before = original[GATE_FILE_OFFSET]
    require(gate_before & GATE_MASK == 0, "1456 bit is not initially clear")
    gate_call_offset = pe.get_offset_from_rva(GATE_CALL_VA - IMAGE_BASE)
    gate_call_before = b"\xe8" + rel32(PREVIOUS_GATE_VA, GATE_CALL_VA + 5)
    require(original[gate_call_offset:gate_call_offset+5] == gate_call_before,
            "existing guild receive gate changed")
    gate_code = build_gate()
    gate_call_after = b"\xe8" + rel32(GATE_CAVE_VA, GATE_CALL_VA + 5)

    hook = build_hook()
    site = b"\xE9" + rel32(CAVE_VA, PATCH_VA + 5) + b"\x90\x90"
    plan = {
        "schema": "dnf90-op97-unbind-1456-gate-plan/v2",
        "mode": "apply" if args.apply else "dry-run",
        "input": {"path": str(args.input.resolve()), "sha256": source_sha, "size": len(original)},
        "op97ClearUnbind": {
            "patchAt": {"va": f"0x{PATCH_VA:08X}", "rva": f"0x{PATCH_RVA:08X}", "fileOffset": f"0x{patch_offset:08X}", "original": PATCH_ORIGINAL.hex().upper(), "replacement": site.hex().upper()},
            "cave": {"va": f"0x{CAVE_VA:08X}", "rva": f"0x{CAVE_RVA:08X}", "fileOffset": f"0x{cave_offset:08X}", "capacity": CAVE_CAPACITY, "length": len(hook), "bytes": hook.hex().upper()},
            "resume": f"0x{RESUME_VA:08X}",
            "preserve": "pushfd, pushad, and 12-byte pad; getter 0x32B5C90 -> setter 0x29E7A90",
            "scope": "clearFlag==0 only; 0x2043C7A entry precedes clear(A), append/continuation untouched",
        },
        "class0_1456_gate": {
            "callVA": f"0x{GATE_CALL_VA:08X}",
            "callFileOffset": f"0x{gate_call_offset:08X}",
            "before": gate_call_before.hex(),
            "after": gate_call_after.hex(),
            "caveVA": f"0x{GATE_CAVE_VA:08X}",
            "caveFileOffset": f"0x{cave_offset + 80:08X}",
            "bytes": gate_code.hex(),
            "length": len(gate_code),
            "fallbackVA": f"0x{PREVIOUS_GATE_VA:08X}",
            "sharedBitsetUnchanged": True,
        },
        "caveSection": cave_section.Name.rstrip(b"\0").decode("latin1"),
        "notPerformed": ["anti-RE", "protected build", "runtime/GUI validation", "deployment"],
    }

    if args.apply:
        data = bytearray(original)
        data[patch_offset : patch_offset + 7] = site
        data[cave_offset : cave_offset + len(hook)] = hook
        data[gate_call_offset:gate_call_offset+5] = gate_call_after
        data[cave_offset+80:cave_offset+80+len(gate_code)] = gate_code
        checksum = pe_checksum(data)
        pe_offset = struct.unpack_from("<I", data, 0x3C)[0]
        struct.pack_into("<I", data, pe_offset + 24 + 64, checksum)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_bytes(data)
        plan["output"] = {"path": str(args.output.resolve()), "sha256": hashlib.sha256(data).hexdigest(), "size": len(data), "checksum": f"0x{checksum:08X}"}

    encoded = json.dumps(plan, ensure_ascii=False, indent=2) + "\n"
    if args.receipt:
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        with args.receipt.open("x", encoding="utf-8") as stream:
            stream.write(encoded)
    print(encoded, end="")


if __name__ == "__main__":
    main()
