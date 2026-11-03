"""Enable the existing class0/op602 consumer in official cumulative V207.

Source-only tool: reads one known cumulative input and writes a separate candidate.
No PVF, runtime process, server configuration or player file is changed.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
from patch_receivers_v128 import headers, offset, checksum

INPUT_SHA256 = "eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03"
INPUT_SIZE = 112549888
APPROVED_INPUTS = {INPUT_SHA256: INPUT_SIZE}
BITMAP_VA = 0x6E92300
GATE_VA = 0x6E83610
REGISTRATION_VA = 0x2032630
HANDLER_VA = 0x20076D0
OPCODE = 602


def build(data):
    identity = hashlib.sha256(data).hexdigest()
    if APPROVED_INPUTS.get(identity) != len(data):
        raise ValueError("official cumulative V207 SHA-256 and length required")
    opt, sections = headers(data)
    expected = b"\x6a\x00\x68" + struct.pack("<I", HANDLER_VA) + b"\x68" + struct.pack("<I", OPCODE)
    at = offset(sections, REGISTRATION_VA, len(expected))
    if data[at:at + len(expected)] != expected:
        raise ValueError("native op602 registration mismatch")
    gate = bytes.fromhex("0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400")
    at = offset(sections, GATE_VA, len(gate))
    if data[at:at + len(gate)] != gate:
        raise ValueError("native receive gate mismatch")
    at = offset(sections, BITMAP_VA + OPCODE // 8)
    if data[at] != 0x0B:
        raise ValueError("op602 receive bitmap baseline mismatch")
    result = bytearray(data)
    result[at] |= 1 << (OPCODE % 8)
    struct.pack_into("<I", result, opt + 64, checksum(result, opt + 64))
    result = bytes(result)
    return result, {"input_sha256": identity,
                    "output_sha256": hashlib.sha256(result).hexdigest(),
                    "size": len(result), "bitmap_offset": at,
                    "changed_bit": OPCODE, "checksum_offset": opt + 64,
                    "gameplay_verified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.input.resolve() == args.output.resolve() or args.output.exists():
        raise ValueError("output must be a separate new file")
    data = args.input.read_bytes()
    result, receipt = build(data)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("xb") as stream:
        stream.write(result)
    if args.input.read_bytes() != data or args.output.read_bytes() != result:
        raise RuntimeError("input/output readback mismatch")
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    main()
