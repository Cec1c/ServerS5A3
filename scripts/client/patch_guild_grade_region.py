"""Accept the authoritative 0..249 region in the guild grade sender.

Build a separate candidate from the exact audited client; preserve every other
instruction, receive gate and PE section. Publication is a separate operation.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from patch_guild_member_menu_v118 import headers, offset_for_rva

INPUT_SHA256 = "0e16090f0e4e3ac830f70183c814f5b24724cec235935e6c1221bf2d865d4243"
IMAGE_BASE = 0x400000
RANGE_VA = 0x29794F7
BEFORE = bytes.fromhex("8d47ff3df8000000")
AFTER = bytes.fromhex("8d47003df9000000")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def checksum(data, field):
    # The PE checksum is a folded sum of little-endian words, excluding itself.
    import array
    import sys
    words = array.array("H", data + (b"\0" if len(data) % 2 else b""))
    if sys.byteorder != "little":
        words.byteswap()
    words[field // 2] = words[field // 2 + 1] = 0
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return total + len(data)


def build(data):
    if sha(data) != INPUT_SHA256:
        raise ValueError("input must match the audited complete client SHA256")
    _, opt, _, sections = headers(data)
    if struct.unpack_from("<I", data, opt + 28)[0] != IMAGE_BASE:
        raise ValueError("unexpected image base")
    if any(struct.unpack_from("<II", data, opt + 96 + 4 * 8)):
        raise ValueError("signed image requires separate signing")
    at = offset_for_rva(sections, RANGE_VA - IMAGE_BASE, len(BEFORE))
    if data[at:at + len(BEFORE)] != BEFORE:
        raise ValueError("guild sender instructions do not match")
    out = bytearray(data)
    out[at:at + len(AFTER)] = AFTER
    struct.pack_into("<I", out, opt + 64, checksum(out, opt + 64))
    result = bytes(out)
    return result, {
        "input_sha256": INPUT_SHA256, "output_sha256": sha(result),
        "allowed_changed_ranges": [[at + 2, at + 3], [at + 4, at + 5], [opt + 64, opt + 68]],
        "changed_instruction_bytes": 2, "section_count": len(sections),
        "accepted_regions": [0, 249], "enabled_opcodes": [],
    }


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError("output must be a separate new file")
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("xb") as stream:
        stream.write(result)
    if sha(source.read_bytes()) != INPUT_SHA256 or sha(destination.read_bytes()) != receipt["output_sha256"]:
        raise RuntimeError("input or output changed")
    return receipt


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
