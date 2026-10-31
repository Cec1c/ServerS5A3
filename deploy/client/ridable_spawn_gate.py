"""Build a pinned V128 or v141 client candidate admitting native NOTI296.

The source stays unchanged. Only the receive bit and PE checksum may change.
The server must provide the validated 20-byte packet and lifecycle ownership.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from story_pause_gate import file_offset, pe_layout

SOURCE_SHA256 = "ea2f763927c8c993cc9f5cf41eb85ea37d5a50760ca310706497e5419bbc5912"
V141_SOURCE_SHA256 = "26abf551753c2745c2e5b349fb24336422a189187ff847c995e0df9a68871079"
SOURCE_PROFILES = {
    SOURCE_SHA256: ("v128", 111808512, 23),
    V141_SOURCE_SHA256: ("v141", 111812608, 24),
}
GATE_VA = 0x06E83610
BITMAP_VA = 0x06E92300
OPCODE = 296


def build(data):
    source_sha256 = hashlib.sha256(data).hexdigest()
    profile = SOURCE_PROFILES.get(source_sha256)
    if profile is None:
        raise ValueError("input is not a pinned V128 or v141 client")
    base, _, sections = pe_layout(data)
    if base != 0x00400000:
        raise ValueError("unexpected image base")
    source_profile, expected_size, expected_sections = profile
    if len(data) != expected_size or len(sections) != expected_sections:
        raise ValueError("pinned client layout mismatch")
    gate = file_offset(sections, GATE_VA, 27)
    fingerprint = bytes.fromhex("0fb74424043dc205000077100fa305") + struct.pack("<I", BITMAP_VA)
    if data[gate:gate + len(fingerprint)] != fingerprint:
        raise ValueError("receive gate fingerprint mismatch")
    bound = struct.unpack_from("<I", data, gate + 6)[0]
    bitmap = file_offset(sections, BITMAP_VA, bound // 8 + 1)
    offset = bitmap + OPCODE // 8
    if data[offset] != 0:
        raise ValueError("unexpected NOTI296 gate byte")
    after = bytearray(data)
    after[offset] = 1
    pe_offset = struct.unpack_from("<I", data, 0x3c)[0]
    check = pe_offset + 24 + 64
    checksum = pefile.PE(data=bytes(after), fast_load=True).generate_checksum()
    struct.pack_into("<I", after, check, checksum)
    allowed = lambda raw, op: op <= bound and bool(raw[bitmap + op // 8] & (1 << (op % 8)))
    admissions = [op for op in range(65536) if allowed(data, op) != allowed(after, op)]
    restore = bytearray(after)
    restore[offset] = data[offset]
    restore[check:check + 4] = data[check:check + 4]
    output = bytes(after)
    if admissions != [OPCODE] or bytes(restore) != data:
        raise ValueError("change outside NOTI296 and checksum")
    if not pefile.PE(data=output, fast_load=True).verify_checksum():
        raise ValueError("PE checksum verification failed")
    changes = [{"offset": i, "before": a, "after": b}
               for i, (a, b) in enumerate(zip(data, output)) if a != b]
    return output, {
        "source_sha256": source_sha256,
        "source_profile": source_profile,
        "candidate_sha256": hashlib.sha256(output).hexdigest(),
        "size": len(output), "checksum": checksum,
        "changes": changes, "changed_admissions": admissions,
        "all_other_original_bytes_preserved": True,
        "published": False, "native_gameplay_acceptance": False,
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("source", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--receipt", type=Path, required=True)
    args = p.parse_args()
    paths = [x.resolve() for x in (args.source, args.output, args.receipt)]
    if len(set(paths)) != 3 or args.output.exists() or args.receipt.exists():
        p.error("source and new outputs must be distinct; outputs cannot exist")
    output, receipt = build(args.source.read_bytes())
    with args.output.open("xb") as stream:
        stream.write(output)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
