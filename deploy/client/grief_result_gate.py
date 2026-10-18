"""Build the pinned v116 grief-result compatibility candidate.

No client binaries are distributed in this source repository. The input must be
the exact published v116 client; outputs must be new files.

The receive gate is indexed by the uint16 opcode before class dispatch. Opening
1263 therefore admits it for every class. The server currently sends this
notification only through class0, but this patch cannot claim that the gate is
class0-only.
"""

import argparse
import hashlib
import json
from pathlib import Path
import struct


SOURCE_SHA256 = "0d470214c2d72c186fcd8cf33ee18e0c9f5fb4a5b336e36c125e79ec19f60920"
GATE_VA = 0x6E83610
BITMAP_VA = 0x6E92300
OPCODE = 1263
BOUND_OPCODE = 1474
BITMAP_BYTE_INDEX = OPCODE // 8
BITMAP_BYTE_OFFSET = 0x6A9239D
BITMASK = 1 << (OPCODE % 8)
PVF_SHA256 = "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb"
GATE_FINGERPRINT = bytes.fromhex(
    "0fb74424043dc205000077100fa3050023e906"
    "0f92c00fb6c0c2040031c0c20400"
)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def pe_layout(data):
    if len(data) < 0x40 or data[:2] != b"MZ":
        raise ValueError("not a PE image")
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if pe + 24 > len(data) or data[pe:pe + 4] != b"PE\0\0":
        raise ValueError("invalid PE header")
    if struct.unpack_from("<H", data, pe + 24)[0] != 0x10B:
        raise ValueError("expected PE32")
    count = struct.unpack_from("<H", data, pe + 6)[0]
    optional_size = struct.unpack_from("<H", data, pe + 20)[0]
    base = struct.unpack_from("<I", data, pe + 52)[0]
    size = struct.unpack_from("<I", data, pe + 80)[0]
    table = pe + 24 + optional_size
    if count == 0 or table + count * 40 > len(data):
        raise ValueError("invalid PE section table")
    sections = []
    for i in range(count):
        virtual_size, rva, raw_size, raw_offset = struct.unpack_from(
            "<IIII", data, table + i * 40 + 8
        )
        if raw_offset + raw_size > len(data):
            raise ValueError("PE section exceeds the input")
        sections.append((base + rva, raw_size, raw_offset, virtual_size))
    return base, size, sections


def file_offset(sections, va, length=1):
    if length <= 0:
        raise ValueError("invalid address range")
    for address, size, offset, _ in sections:
        if address <= va and va + length <= address + size:
            return offset + va - address
    raise ValueError("address outside raw PE sections")


def allowed(data, bitmap, opcode):
    return opcode <= BOUND_OPCODE and bool(
        data[bitmap + opcode // 8] & (1 << (opcode % 8))
    )


def build(data):
    if sha256(data) != SOURCE_SHA256:
        raise ValueError("input is not the pinned published v116 client")
    if (OPCODE, BOUND_OPCODE, BITMAP_BYTE_INDEX, BITMASK) != (1263, 1474, 157, 0x80):
        raise ValueError("pinned grief-result constants changed")

    _, _, sections = pe_layout(data)
    gate = file_offset(sections, GATE_VA, len(GATE_FINGERPRINT))
    if data[gate:gate + len(GATE_FINGERPRINT)] != GATE_FINGERPRINT:
        raise ValueError("receive gate fingerprint mismatch")
    if struct.unpack_from("<I", data, gate + 6)[0] != BOUND_OPCODE:
        raise ValueError("receive gate upper bound mismatch")

    bitmap = file_offset(sections, BITMAP_VA, BOUND_OPCODE // 8 + 1)
    offset = bitmap + BITMAP_BYTE_INDEX
    if offset != BITMAP_BYTE_OFFSET:
        raise ValueError("bitmap byte offset mismatch")
    if opcode_admitted(data, bitmap):
        raise ValueError("1263 is already admitted")

    after = bytearray(data)
    before = after[offset]
    if before != 0:
        raise ValueError("1263 target byte is not the pinned 0x00")
    after[offset] |= BITMASK
    if after[offset] != 0x80:
        raise ValueError("1263 target byte did not become the pinned 0x80")
    if after[:offset] != data[:offset] or after[offset + 1:] != data[offset + 1:]:
        raise ValueError("unexpected change outside the single allow bit")

    delta = [
        opcode
        for opcode in range(65536)
        if allowed(data, bitmap, opcode) != allowed(after, bitmap, opcode)
    ]
    before_allowed = sum(allowed(data, bitmap, opcode) for opcode in range(65536))
    after_allowed = sum(allowed(after, bitmap, opcode) for opcode in range(65536))
    if delta != [OPCODE] or after_allowed != before_allowed + 1:
        raise ValueError("unexpected static admission delta")

    receipt = dict(
        source_sha256=SOURCE_SHA256,
        candidate_sha256=sha256(after),
        size=len(after),
        gate_va=hex(GATE_VA),
        bitmap_va=hex(BITMAP_VA),
        bound_opcode=BOUND_OPCODE,
        opcode=OPCODE,
        bitmap_byte_index=BITMAP_BYTE_INDEX,
        changes=[
            dict(
                opcode=OPCODE,
                offset=offset,
                before=before,
                after=after[offset],
                mask=BITMASK,
            )
        ],
        newly_admitted_opcodes=delta,
        static_comparison=dict(
            compared_opcodes=65536,
            before_allowed=before_allowed,
            after_allowed=after_allowed,
        ),
        class_scope=(
            "The receive gate is indexed by number before class dispatch. "
            "Opening 1263 admits it for every class; only the server-side use "
            "of this notification is class0."
        ),
        protocol_profile="90cn-decode-bypass-v1",
        pvf_sha256=PVF_SHA256,
        native_acceptance=False,
        published=False,
    )
    return bytes(after), receipt


def opcode_admitted(data, bitmap):
    return allowed(data, bitmap, OPCODE)


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
