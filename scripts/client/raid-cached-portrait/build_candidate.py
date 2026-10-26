"""Standalone, deterministic P34 experimental candidate builder (never installs).

The default '.rport' is a valid PE section name. Names are checked against the
eight-byte image-header limit and are never silently truncated.
"""
from __future__ import annotations

import argparse
from array import array
import hashlib
import json
import os
from pathlib import Path
import struct
import sys


INPUT_SHA256 = "922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b"
INPUT_SIZE = 111906816
IMAGE_BASE = 0x00400000
SITE = 0x0110B12E
SITE_OFFSET = 0x00D0B12E
ORIGINAL_JE = bytes.fromhex("0f 84 1e 02 00 00")
FALLTHROUGH = 0x0110B134
MISS = 0x0110B352
REQUESTED_SECTION_NAME = ".rport"
SECTION_FLAGS = 0x60000020  # code, read, execute; no write
MAX_PE_SECTIONS = 96
SECTION_TABLE_SHA256 = "83c18f91f9df42dbc0bcecbc99663ec9b9ca7a6e952dd25dc988ca9785b17dc8"
REFERENCE_BASE = 0x33010000
REFERENCE_HELPER_SIZE = 196
REFERENCE_HELPER_SHA256 = "45e05eaff1c12b23c36e7811c3b6e96818861d1a81875342175e4fe68ae84c28"
PROVENANCE = {
    "p34-null-actor-helper.py": "cbbb5721bf02b91b1b38b8ffda1cf6b410185b4208eb02f5a9d0f61ce5c3d328",
    "peer-safe-helpers-v2.py": "af7b4ffe67528cea2d150b7225b133a6e8febfe9fa95edfb0123758fcfd17193",
    "scripts/client/dungeon-peer-retire/build_candidate.py": "2898540e4855e8352efa2fcab77ec4701886bac9fc47f232f0e2dd8edf7a2890",
}
OPEN_ISSUES = [
    "The current raid/member identity is not proven to match the old 584 row context.",
    "Exact category/key/owner/route matches do not prove freshness, ownership or actor lifetime.",
    "Real model/resource creation, rendering leaves, UI resource lifetime and GUI acceptance remain unverified.",
    "Earlier bounded v166 harness evidence is not validation of this v167 candidate or proof that P34 is fixed.",
]


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def sha(data: bytes | bytearray) -> str:
    return hashlib.sha256(data).hexdigest()


def align(value: int, alignment: int) -> int:
    require(alignment > 0 and alignment & (alignment - 1) == 0, "invalid alignment")
    return (value + alignment - 1) & ~(alignment - 1)


def field(data: bytes | bytearray, offset: int, fmt: str) -> int:
    require(0 <= offset <= len(data) - struct.calcsize(fmt), "truncated PE field")
    return struct.unpack_from(fmt, data, offset)[0]


def u16(data: bytes | bytearray, offset: int) -> int:
    return field(data, offset, "<H")


def u32(data: bytes | bytearray, offset: int) -> int:
    return field(data, offset, "<I")


def checksum(data: bytes | bytearray, offset: int) -> int:
    # Same PE checksum method as the repository builder, with explicit endian handling.
    require(len(data) % 2 == 0 and offset % 2 == 0, "unaligned checksum input")
    require(0 <= offset <= len(data) - 4, "checksum outside file")
    words = array("H", data)
    require(words.itemsize == 2, "unsupported word size")
    if sys.byteorder != "little":
        words.byteswap()
    value = sum(words) - words[offset // 2] - words[offset // 2 + 1]
    while value >> 16:
        value = (value & 0xFFFF) + (value >> 16)
    return (value + len(data)) & 0xFFFFFFFF


class Code:
    """Minimal byte-for-byte codec extraction; unused 'ja' is omitted."""

    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[int, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        if name in self.labels:
            raise ValueError(f"duplicate label: {name}")
        self.labels[name] = len(self.buf)

    def branch(self, opcode: str, target: str) -> None:
        self.raw(opcode)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append((offset, 4, target))

    def call(self, target: int | str) -> None:
        if isinstance(target, str):
            self.branch("e8", target)
        else:
            self.buf.append(0xE8)
            self.buf.extend(struct.pack("<i", target - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        if isinstance(target, str):
            self.branch("e9", target)
        else:
            self.buf.append(0xE9)
            self.buf.extend(struct.pack("<i", target - (self.base + len(self.buf) + 4)))

    def je(self, target: str) -> None:
        self.branch("0f84", target)

    def jne(self, target: str) -> None:
        self.branch("0f85", target)

    def finish(self) -> bytes:
        for offset, size, label in self.fixups:
            destination = self.labels[label]
            struct.pack_into("<i", self.buf, offset, destination - offset - size)
        return bytes(self.buf)


def emit(base: int) -> bytes:
    # Exact instruction/label sequence from p34-null-actor-helper.py.
    # Only the four external rel32 displacements depend on the chosen base.
    c = Code(base)
    c.raw("9c 60 0fb7d7 85d2"); c.je("miss")
    c.raw("81faffff0000"); c.je("miss")
    c.raw("8b0d78376a05 85c9"); c.je("miss")
    c.call(0x1A553E0); c.raw("85c0"); c.je("miss")
    c.raw("8bf0 0fb65003 52 6a01 0fb65002 52 0fb7d7 52")
    c.call(0x1A5A550); c.raw("83c410 85c0"); c.je("miss")
    c.raw("8138dc d1 9e 04"); c.jne("miss")
    c.raw("0fb75020 0fb7cf 3bd1"); c.jne("miss")
    c.raw("0fb69004060000 3a5602"); c.jne("miss")
    c.raw("0fb69005060000 3a5603"); c.jne("miss")
    c.raw("83b8f404000000"); c.jne("ready")
    c.raw("83783410"); c.je("miss")
    c.raw("83783000"); c.je("miss")
    c.label("ready"); c.raw("89442410 61 9d"); c.jump(0x110B134)
    c.label("miss"); c.raw("61 9d"); c.jump(0x110B352)
    return c.finish()


class PE:
    def __init__(self, data: bytes | bytearray):
        self.data = data
        require(data[:2] == b"MZ", "MZ missing")
        self.nt = u32(data, 0x3C)
        require(self.nt == 0x130 and data[self.nt:self.nt + 4] == b"PE\0\0", "PE header differs")
        self.opt = self.nt + 24
        require(u16(data, self.nt + 4) == 0x14C and u16(data, self.opt) == 0x10B, "requires x86 PE32")
        require(u16(data, self.nt + 20) == 224, "optional header size differs")
        require(u32(data, self.opt + 28) == IMAGE_BASE, "image base differs")
        self.count = u16(data, self.nt + 6)
        require(1 <= self.count <= MAX_PE_SECTIONS, "section count outside loader limit")
        self.table = self.opt + 224
        self.sa, self.fa = u32(data, self.opt + 32), u32(data, self.opt + 36)
        self.headers = u32(data, self.opt + 60)
        self.image_size = u32(data, self.opt + 56)
        require(self.sa == self.fa == self.headers == 0x1000, "fixed alignment/header layout differs")
        require(self.table + self.count * 40 <= self.headers <= len(data), "section table outside headers")
        require(u16(data, self.opt + 70) & 0x40 == 0, "DYNAMIC_BASE input unsupported")
        require(u32(data, self.opt + 92) == 16, "data directory count differs")
        require(data[self.opt + 128:self.opt + 136] == bytes(8), "certificate directory unsupported")
        self.sections: list[dict[str, int | bytes]] = []
        raw_end = virtual_end = self.headers
        for index in range(self.count):
            at = self.table + index * 40
            vs, rva, rs, raw = struct.unpack_from("<IIII", data, at + 8)
            require(vs > 0 and rs > 0, "empty section unsupported in fixed image")
            require(raw % self.fa == rs % self.fa == rva % self.sa == 0, "unaligned section")
            require(raw >= raw_end and raw + rs <= len(data), "overlapping/out-of-file raw section")
            require(rva >= virtual_end, "overlapping/out-of-order virtual section")
            virtual_end = align(rva + max(vs, rs), self.sa)
            require(IMAGE_BASE + virtual_end <= 0xFFFFFFFF, "32-bit image overflow")
            raw_end = raw + rs
            self.sections.append(dict(name=bytes(data[at:at + 8]), vsize=vs, rva=rva,
                                      raw_size=rs, raw=raw, flags=u32(data, at + 36)))
        require(raw_end == len(data), "overlay or trailing bytes unsupported")
        require(virtual_end == self.image_size, "image end mismatch")

    def offset(self, va: int, size: int) -> int:
        require(size > 0, "empty VA request")
        matches = []
        for section in self.sections:
            delta = va - IMAGE_BASE - section["rva"]
            if 0 <= delta and delta + size <= section["raw_size"]:
                matches.append(section["raw"] + delta)
        require(len(matches) == 1, "VA must have exactly one file-backed section")
        return matches[0]

    def read(self, va: int, size: int) -> bytes:
        offset = self.offset(va, size)
        return bytes(self.data[offset:offset + size])


def section_name(name: str) -> bytes:
    encoded = name.encode("ascii")
    require(1 <= len(encoded) <= 8,
            "PE image section name must be 1..8 ASCII bytes; truncation is forbidden.")
    require(name.startswith(".") and all(c.isalnum() or c in "._" for c in name),
            "section name must start with '.' and contain only ASCII letters/digits/._")
    return encoded.ljust(8, b"\0")


def build(data: bytes, name: str) -> tuple[bytes, dict]:
    encoded_name = section_name(name)
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA256, "fixed v167 input differs")
    pe = PE(data)
    require(pe.count == 47 and pe.image_size == 0x06AC6000, "fixed section count/image size differs")
    require(sha(data[pe.table:pe.table + pe.count * 40]) == SECTION_TABLE_SHA256, "section layout differs")
    require(u32(data, pe.opt + 4) == 0x041AC000, "SizeOfCode differs")
    require(u32(data, pe.opt + 64) == checksum(data, pe.opt + 64), "input checksum differs")
    require(pe.offset(SITE, 6) == SITE_OFFSET and pe.read(SITE, 6) == ORIGINAL_JE, "original six-byte JE differs")
    require(pe.read(SITE - 4, 4) == bytes.fromhex("8b d8 85 db"), "mov ebx,eax / test ebx,ebx differs")
    require(SITE + 6 + struct.unpack("<i", ORIGINAL_JE[2:])[0] == MISS, "original JE target differs")

    header = pe.table + pe.count * 40
    first_raw = min(s["raw"] for s in pe.sections)
    header_capacity = (min(first_raw, pe.headers) - pe.table) // 40
    require(pe.count + 1 <= min(MAX_PE_SECTIONS, header_capacity), "section/header limit reached")
    require(header == 0x980 and header + 40 <= first_raw and header + 40 <= pe.headers, "no header space")
    require(data[header:header + 40] == bytes(40), "nonempty header padding")
    require(all(s["name"] != encoded_name for s in pe.sections), "section name already present")

    reference = emit(REFERENCE_BASE)
    require(len(reference) == REFERENCE_HELPER_SIZE and sha(reference) == REFERENCE_HELPER_SHA256,
            "embedded helper differs from the recorded original emitter")
    rva = pe.image_size
    base = IMAGE_BASE + rva
    payload = emit(base)
    require(len(payload) == REFERENCE_HELPER_SIZE, "relocated helper size differs")
    raw = align(len(data), pe.fa)
    raw_size = align(len(payload), pe.fa)
    image_size = align(rva + max(len(payload), raw_size), pe.sa)
    require(raw == INPUT_SIZE and raw_size == 0x1000, "append layout differs")
    require(IMAGE_BASE + image_size <= 0xFFFFFFFF, "new image overflows 32-bit address space")
    new_code_size = u32(data, pe.opt + 4) + raw_size
    require(new_code_size <= 0xFFFFFFFF, "SizeOfCode overflow")
    # Keep JE, rather than an unconditional branch: non-null EBX never enters helper.
    patch = b"\x0f\x84" + struct.pack("<i", base - (SITE + 6))
    out = bytearray(data) + payload + bytes(raw_size - len(payload))
    out[SITE_OFFSET:SITE_OFFSET + 6] = patch
    struct.pack_into("<8sIIIIIIHHI", out, header, encoded_name, len(payload), rva,
                     raw_size, raw, 0, 0, 0, 0, SECTION_FLAGS)
    struct.pack_into("<H", out, pe.nt + 6, pe.count + 1)
    struct.pack_into("<I", out, pe.opt + 4, new_code_size)
    struct.pack_into("<I", out, pe.opt + 56, image_size)
    struct.pack_into("<I", out, pe.opt + 64, checksum(out, pe.opt + 64))

    edits = sorted([
        (SITE_OFFSET, SITE_OFFSET + 6, "sole existing-code patch: JE to helper"),
        (header, header + 40, "new section header in verified zero padding"),
        (pe.nt + 6, pe.nt + 8, "NumberOfSections"),
        (pe.opt + 4, pe.opt + 8, "SizeOfCode"),
        (pe.opt + 56, pe.opt + 60, "SizeOfImage"),
        (pe.opt + 64, pe.opt + 68, "CheckSum"),
    ])
    cursor = 0
    changes = []
    for start, end, reason in edits:
        require(cursor <= start < end <= len(data), "invalid/overlapping allowed edit range")
        require(out[cursor:start] == data[cursor:start], "unapproved original bytes changed")
        changes.append(dict(offset=start, end_exclusive=end, reason=reason,
                            before_hex=data[start:end].hex(), after_hex=out[start:end].hex()))
        cursor = end
    require(out[cursor:len(data)] == data[cursor:], "unapproved trailing original bytes changed")
    final = PE(out)
    require(final.count == 48 and final.image_size == image_size, "final layout readback differs")
    require(final.sections[:-1] == pe.sections, "existing sections changed")
    require(final.sections[-1] == dict(name=encoded_name, vsize=len(payload), rva=rva,
                                     raw_size=raw_size, raw=raw, flags=SECTION_FLAGS), "new section differs")
    require(final.read(base, len(payload)) == payload, "helper readback differs")
    require(final.read(SITE, 6) == patch and final.read(SITE - 4, 4) == pe.read(SITE - 4, 4), "branch readback differs")
    require(out[raw + len(payload):] == bytes(raw_size - len(payload)), "nonzero appended padding")
    require(u32(out, pe.opt + 64) == checksum(out, pe.opt + 64), "output checksum differs")
    manifest = dict(
        schema="p34-candidate-source-v1", candidate_only=True,
        input=dict(sha256=INPUT_SHA256, size=INPUT_SIZE),
        output=dict(sha256=sha(out), size=len(out)),
        source_provenance_sha256=PROVENANCE,
        requested_section_name=REQUESTED_SECTION_NAME, pe_section_name=name,
        section_name_override=(name != REQUESTED_SECTION_NAME),
        layout=dict(image_base=hex(IMAGE_BASE), section_count_before=pe.count, section_count_after=final.count,
                    loader_section_limit=MAX_PE_SECTIONS, header_section_capacity=header_capacity,
                    section_alignment=pe.sa, file_alignment=pe.fa, section_header_offset=header,
                    original_section_table_sha256=SECTION_TABLE_SHA256,
                    image_size_before=pe.image_size, image_size_after=image_size),
        appended_section=dict(rva=hex(rva), va=hex(base), raw_offset=raw, raw_size=raw_size,
                              virtual_size=len(payload), flags=hex(SECTION_FLAGS), padding="zero"),
        helper=dict(size=len(payload), sha256=sha(payload), reference_base=hex(REFERENCE_BASE),
                    reference_sha256=REFERENCE_HELPER_SHA256, category=1,
                    call_targets=["0x01a553e0", "0x01a5a550"],
                    accepted_return=hex(FALLTHROUGH), miss_return=hex(MISS)),
        branch=dict(va=hex(SITE), file_offset=SITE_OFFSET, before_hex=ORIGINAL_JE.hex(),
                    after_hex=patch.hex(), target=hex(base), non_null_fallthrough=hex(FALLTHROUGH)),
        allowed_original_byte_ranges=changes,
        validation=dict(input_and_layout=True, helper_reference_bytes=True, allowed_byte_changes=True,
                        checksum=True, runtime_tested=False, unicorn_tested=False, game_tested=False),
        p34_fixed=False, installed=False, released=False, deployed=False, open_issues=OPEN_ISSUES,
    )
    return bytes(out), manifest


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path, help="fixed peer-v167-clean.exe; opened read-only")
    parser.add_argument("--output-dir", required=True, type=Path, help="new directory; existing paths are refused")
    parser.add_argument("--pe-section-name", default=REQUESTED_SECTION_NAME,
                        help="PE section name of at most 8 ASCII bytes; default '.rport'")
    args = parser.parse_args()
    section_name(args.pe_section_name)  # Validate the name before any file writes.
    source = args.input.expanduser().resolve(strict=True)
    destination = args.output_dir.expanduser().absolute()
    require(source.is_file() and source.stat().st_size == INPUT_SIZE, "input file/size differs")
    require(destination.parent.is_dir(), "output parent must already exist")
    require(not os.path.lexists(destination), "output directory already exists; no overwrites")
    data = source.read_bytes()
    candidate, manifest = build(data, args.pe_section_name)
    manifest["builder_sha256"] = sha(Path(__file__).read_bytes())
    # Recheck the read-only input before writing; do not replace an existing output.
    require(sha(source.read_bytes()) == INPUT_SHA256, "input changed during construction")
    destination.mkdir(exist_ok=False)
    output = destination / "peer-v167-p34-candidate.exe"
    receipt = destination / "changes.json"
    with output.open("xb") as stream:
        stream.write(candidate)
    require(output.stat().st_size == len(candidate) and sha(output.read_bytes()) == manifest["output"]["sha256"],
            "candidate disk readback differs; retain partial directory for inspection")
    manifest["validation"]["disk_readback"] = True
    manifest_bytes = (json.dumps(manifest, ensure_ascii=True, sort_keys=True, indent=2) + "\n").encode("ascii")
    with receipt.open("xb") as stream:
        stream.write(manifest_bytes)
    require(receipt.read_bytes() == manifest_bytes, "manifest disk readback differs")
    print(json.dumps(dict(candidate=str(output), manifest=str(receipt),
                          output_sha256=manifest["output"]["sha256"],
                          manifest_sha256=sha(manifest_bytes), p34_fixed=False), sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, struct.error) as error:
        raise SystemExit(f"refused: {error}") from error
