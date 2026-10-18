"""Build the hash-pinned V126 image-communication receive-gate candidate.

Only opcode 475's existing registration becomes reachable. The source is never
overwritten; the output and receipt must be new files.
"""

import argparse
import hashlib
import json
from pathlib import Path
import struct

from story_pause_gate import file_offset, pe_layout

SOURCE_SHA256 = "c175ecc93cdabe43c55d7f2ecdf3b1d3c87c7a90ec58c79b88b6f648db046939"
GATE_VA = 0x06E83610
BITMAP_VA = 0x06E92300
OPCODE = 475


def pe_checksum(data, offset):
    copy = bytearray(data)
    struct.pack_into("<I", copy, offset, 0)
    if len(copy) % 2:
        copy.append(0)
    total = sum(word[0] for word in struct.iter_unpack("<H", copy))
    while total >> 16:
        total = (total & 0xffff) + (total >> 16)
    return total + len(data)


def build(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError("input is not the pinned published V126 client")
    base, _, sections = pe_layout(data)
    if base != 0x00400000:
        raise ValueError("unexpected image base")
    gate = file_offset(sections, GATE_VA, 27)
    fingerprint = bytes.fromhex("0fb74424043dc205000077100fa305") + struct.pack("<I", BITMAP_VA)
    if data[gate:gate + len(fingerprint)] != fingerprint:
        raise ValueError("receive gate fingerprint mismatch")
    bound = struct.unpack_from("<I", data, gate + 6)[0]
    bitmap = file_offset(sections, BITMAP_VA, bound // 8 + 1)
    offset = bitmap + OPCODE // 8
    if data[offset] != 0:
        raise ValueError("unexpected communication gate byte")
    output = bytearray(data)
    output[offset] = 8
    allowed = lambda raw, op: op <= bound and bool(raw[bitmap + op // 8] & (1 << (op % 8)))
    delta = [op for op in range(65536) if allowed(data, op) != allowed(output, op)]
    if delta != [OPCODE] or output[:offset] != data[:offset] or output[offset + 1:] != data[offset + 1:]:
        raise ValueError("unexpected change outside the single receive-gate bit")
    checksum_offset = struct.unpack_from("<I", data, 0x3c)[0] + 24 + 64
    old_checksum = struct.unpack_from("<I", data, checksum_offset)[0]
    if old_checksum != pe_checksum(data, checksum_offset):
        raise ValueError("source PE checksum mismatch")
    new_checksum = pe_checksum(output, checksum_offset)
    struct.pack_into("<I", output, checksum_offset, new_checksum)
    return bytes(output), {
        "source_sha256": SOURCE_SHA256,
        "candidate_sha256": hashlib.sha256(output).hexdigest(),
        "size": len(output),
        "changes": [{"offset": offset, "rva": hex(BITMAP_VA - base + OPCODE // 8), "before": 0, "after": 8}],
        "changed_admissions": delta,
        "pe_checksum": {"offset": checksum_offset, "before": old_checksum, "after": new_checksum},
        "preserved_receive_opcodes": [447, 510, 580, 657, 809],
        "preserved_trade_reveal_section": True,
        "gate_applies_to_all_classes": True,
        "published": False,
        "native_ui_acceptance": False,
    }


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
    with args.output.open("xb") as stream:
        stream.write(output)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
