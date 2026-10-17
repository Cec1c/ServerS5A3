"""Build the pinned v115 -> v116 story-pause compatibility candidate.

No client binaries are distributed in this source repository. The input must be
the exact already-published v115 client; outputs must be new files.
"""

import argparse
import hashlib
import json
from pathlib import Path
import struct

SOURCE_SHA256 = "72a18ad4a109778135a80281760ba8e8c367a1b25fa099f6591a0601e4181f01"
GATE_VA = 0x6E83610
BITMAP_VA = 0x6E92300
OPCODE = 170
PVF_SHA256 = "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb"


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def pe_layout(data):
    if data[:2] != b"MZ":
        raise ValueError("not a PE image")
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe:pe + 4] != b"PE\0\0" or struct.unpack_from("<H", data, pe + 24)[0] != 0x10B:
        raise ValueError("expected PE32")
    base = struct.unpack_from("<I", data, pe + 52)[0]
    size = struct.unpack_from("<I", data, pe + 80)[0]
    table = pe + 24 + struct.unpack_from("<H", data, pe + 20)[0]
    sections = []
    for i in range(struct.unpack_from("<H", data, pe + 6)[0]):
        virtual_size, rva, raw_size, raw_offset = struct.unpack_from("<IIII", data, table + i * 40 + 8)
        sections.append((base + rva, raw_size, raw_offset, virtual_size))
    return base, size, sections


def file_offset(sections, va, length=1):
    for address, size, offset, _ in sections:
        if address <= va and va + length <= address + size:
            return offset + va - address
    raise ValueError("address outside raw PE sections")


def build(data):
    if sha256(data) != SOURCE_SHA256:
        raise ValueError("input is not the pinned published v115 client")
    _, _, sections = pe_layout(data)
    gate = file_offset(sections, GATE_VA, 27)
    expected = bytes.fromhex("0fb74424043dc205000077100fa305") + struct.pack("<I", BITMAP_VA)
    if data[gate:gate + len(expected)] != expected:
        raise ValueError("receive gate fingerprint mismatch")
    bound = struct.unpack_from("<I", data, gate + 6)[0]
    bitmap = file_offset(sections, BITMAP_VA, bound // 8 + 1)
    allowed = lambda raw, op: op <= bound and bool(raw[bitmap + op // 8] & (1 << (op % 8)))
    offset = bitmap + OPCODE // 8
    if allowed(data, OPCODE):
        raise ValueError("170 is already admitted")
    after = bytearray(data)
    after[offset] |= 1 << (OPCODE % 8)
    delta = [op for op in range(65536) if allowed(data, op) != allowed(after, op)]
    if delta != [OPCODE] or after[:offset] != data[:offset] or after[offset + 1:] != data[offset + 1:]:
        raise ValueError("unexpected change outside the single allow bit")
    receipt = dict(source_sha256=SOURCE_SHA256, candidate_sha256=sha256(after),
                   size=len(after), gate_va=hex(GATE_VA), bitmap_va=hex(BITMAP_VA),
                   changes=[dict(opcode=OPCODE, offset=offset, before=data[offset], after=after[offset])],
                   newly_admitted_opcodes=delta, protocol_profile="90cn-decode-bypass-v1",
                   pvf_sha256=PVF_SHA256, native_acceptance=False, published=False)
    return bytes(after), receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    paths = [p.resolve() for p in (args.source, args.output, args.receipt)]
    if len(set(paths)) != 3 or args.output.exists() or args.receipt.exists():
        parser.error("source, new output and new receipt must be distinct; outputs cannot exist")
    output, receipt = build(args.source.read_bytes())
    receipt["source"] = str(paths[0])
    receipt["output"] = str(paths[1])
    # Exclusive creation preserves existing client files even if another process
    # creates the destination after the initial existence checks.
    with args.output.open("xb") as stream:
        stream.write(output)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
