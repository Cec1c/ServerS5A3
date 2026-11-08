#!/usr/bin/env python3
"""Build an isolated scene-render leader authority experiment from the ACK image.

Only the proven 0x17E985B getter call is redirected. No network, game process,
production source, or existing image is changed. Output must be a new path.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import struct

HERE = Path(__file__).resolve().parent
PE_UTIL = HERE.parent / "quiet-profile-refresh" / "build_candidate.py"
spec = importlib.util.spec_from_file_location("host_authority_pe", PE_UTIL)
peutils = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peutils)

INPUT_SHA = "0e4a99cb278bb4ec37fc90c027903417eb2f790915941000538fcf2209b81872"
INPUT_SIZE = 112607232
CALLSITE = 0x17E985B
GETTER = 0x2973140
LOCAL_GETTER = 0x296E590
UID_ACTOR = 0x296D730
MANAGER_GLOBAL = 0x56A3768  # 0x20180A4, immediately before 0x2974CD0
HELPER = 0x6F9DC40
CAVE_END = 0x6F9E000


class X86:
    def __init__(self):
        self.code = bytearray()
        self.labels = {}
        self.fixups = []

    def at(self, label):
        peutils.require(label not in self.labels, f"duplicate label: {label}")
        self.labels[label] = len(self.code)

    def raw(self, hexbytes):
        self.code.extend(bytes.fromhex(hexbytes))

    def rel(self, opcode, target):
        self.raw(opcode)
        displacement = len(self.code)
        self.code.extend(b"\x00" * 4)
        self.fixups.append((displacement, target))

    def finish(self):
        for offset, target in self.fixups:
            destination = HELPER + self.labels[target] if isinstance(target, str) else target
            struct.pack_into("<i", self.code, offset, destination - (HELPER + offset + 4))
        return bytes(self.code)


def helper():
    x = X86()
    x.raw("55 53 56 57 89 cf 85 ff")  # preserve nonvolatile regs; edi=query record
    x.rel("0f84", "fallback")
    x.rel("e8", LOCAL_GETTER)
    x.raw("85 c0")
    x.rel("0f84", "fallback")
    x.raw("89 c6 39 fe")  # esi=local record; self passes through
    x.rel("0f84", "fallback")
    x.raw("8b 5e 4c 83 fb 07")  # local leader slot, unsigned 0..7
    x.rel("0f87", "fallback")
    x.raw("a1" + struct.pack("<I", MANAGER_GLOBAL).hex() + "85 c0")
    x.rel("0f84", "fallback")
    x.raw("39 58 68")  # manager+68 must match committed self slot
    x.rel("0f85", "fallback")
    x.raw("0f b7 44 9e 2c 66 3d ff ff")  # leader slot UID
    x.rel("0f84", "fallback")
    x.raw("66 85 c0")
    x.rel("0f84", "fallback")
    x.raw("50")
    x.rel("e8", UID_ACTOR)
    x.raw("83 c4 04 85 c0")
    x.rel("0f84", "fallback")
    x.raw("8b 80 c8 04 00 00 85 c0")  # leader actor's live record
    x.rel("0f84", "fallback")
    # A stale/foreign leader attachment must not authorize a peer redirect.
    # The leader record may keep an old leader slot, but its eight UID roster
    # must still name exactly this local party in the same slot order.
    x.raw("31 d2")
    x.at("leader_roster")
    x.raw("8b 4c 96 2c 39 4c 90 2c")
    x.rel("0f85", "fallback")
    x.raw("42 83 fa 08")
    x.rel("0f82", "leader_roster")
    x.raw("31 db 31 ed")  # ebx=index; ebp=matching actor count
    x.at("outer")
    x.raw("0f b7 44 9e 2c 66 85 c0")
    x.rel("0f84", "next")
    x.raw("66 3d ff ff")
    x.rel("0f84", "next")
    x.raw("8d 53 01")  # reject duplicate nonempty UIDs in the fixed 8 slots
    x.at("inner")
    x.raw("83 fa 08")
    x.rel("0f83", "lookup")
    x.raw("66 39 44 96 2c")
    x.rel("0f84", "fallback")
    x.raw("42")
    x.rel("e9", "inner")
    x.at("lookup")
    x.raw("50")
    x.rel("e8", UID_ACTOR)
    x.raw("83 c4 04 85 c0")
    x.rel("0f84", "next")
    x.raw("39 b8 c8 04 00 00")  # actor+4C8 == queried record
    x.rel("0f85", "next")
    x.raw("45 83 fd 01")
    x.rel("0f87", "fallback")
    x.at("next")
    x.raw("43 83 fb 08")
    x.rel("0f82", "outer")
    x.raw("83 fd 01")
    x.rel("0f85", "fallback")
    x.raw("89 f1")  # exactly one live member owns query; use local record
    x.rel("e9", "original")
    x.at("fallback")
    x.raw("89 f9")  # original record for all unproven cases
    x.at("original")
    x.rel("e8", GETTER)
    x.raw("5f 5e 5b 5d c3")
    return x.finish()


def build(data):
    require = peutils.require
    require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA,
            "exact ACK candidate input required")
    pe = peutils.PE(data)
    pe.require_contiguous()
    before_call = b"\xe8" + struct.pack("<i", GETTER - CALLSITE - 5)
    require(pe.read(CALLSITE, 5) == before_call, "render getter call changed")
    require(pe.read(GETTER, 3) == bytes.fromhex("8b414c"), "original getter changed")
    require(pe.read(0x20180A4, 6) == bytes.fromhex("8b0d68376a05"), "manager source changed")
    section = next(s for s in pe.sections if s["name"] == ".krtext")
    require(section["virtual_size"] == 0xCC21 and section["raw_size"] == 0xD000
            and section["characteristics"] == 0x60000020, "ACK RX layout changed")
    code = helper()
    require(HELPER + len(code) <= CAVE_END, "helper exceeds 960-byte cave")
    delta = HELPER - pe.base - section["rva"]
    require(delta >= section["virtual_size"] and delta + len(code) <= section["raw_size"],
            "helper outside unused RX padding")
    cave = pe.offset(HELPER, len(code))
    require(data[cave:cave + len(code)] == bytes(len(code)), "helper cave occupied")
    call = pe.offset(CALLSITE, 5)
    header = pe.st + section["index"] * 40 + 8
    output = bytearray(data)
    output[cave:cave + len(code)] = code
    output[call:call + 5] = b"\xe8" + struct.pack("<i", HELPER - CALLSITE - 5)
    struct.pack_into("<I", output, header, delta + len(code))
    struct.pack_into("<I", output, pe.opt + 64, peutils.pe_checksum(output, pe.opt + 64))
    ranges = sorted([(cave, cave + len(code)), (call, call + 5),
                     (header, header + 4), (pe.opt + 64, pe.opt + 68)])
    unchanged = bytearray(output)
    for lo, hi in ranges:
        unchanged[lo:hi] = data[lo:hi]
    require(unchanged == data, "unreviewed byte changed")
    final = peutils.PE(output)
    final.require_contiguous()
    return bytes(output), {"input_sha256": INPUT_SHA,
        "output_sha256": hashlib.sha256(output).hexdigest(),
        "callsite": hex(CALLSITE), "original_getter": hex(GETTER),
        "helper": hex(HELPER), "helper_bytes": len(code),
        "cave_capacity": CAVE_END - HELPER, "changed_file_ranges": ranges,
        "installed": False, "visual_acceptance": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    peutils.require(args.input.resolve() != args.output.resolve(), "input may not be overwritten")
    result, receipt = build(args.input.read_bytes())
    with args.output.open("xb") as stream:
        stream.write(result)
    peutils.require(hashlib.sha256(args.output.read_bytes()).hexdigest() == receipt["output_sha256"],
                    "candidate disk readback failed")
    print(json.dumps(receipt, indent=2))
