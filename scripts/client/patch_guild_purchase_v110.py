"""Enable the verified guild purchase callback on the V110 hongbao-query derivative.

Requires the exact member-menu, notice, cargo, appearance and hongbao derivative; writes a new file.
"""
import argparse
import json
from pathlib import Path
import struct

from patch_guild_member_menu_v110 import checksum, headers, offset_for_rva, sha

INPUT_SHA256 = "1e0b4ae2839486e20cd5f5f12211927d94b5191c10abc97ae28192856c0cf2d6"
BITMAP_RVA = 0x6A92300
OPCODES = (761,)


def build(data):
    if sha(data) != INPUT_SHA256:
        raise ValueError("requires the exact menu, notice, cargo, appearance and hongbao V110 derivative")
    _, opt, _, sections = headers(data)
    pos = offset_for_rva(sections, BITMAP_RVA, 185)
    result = bytearray(data)
    changes = {}
    for opcode in OPCODES:
        at, mask = pos + opcode // 8, 1 << (opcode % 8)
        if data[at] & mask:
            raise ValueError("guild purchase callback is already enabled")
        result[at] |= mask
        changes[at] = {"offset": at, "before": data[at], "after": result[at]}
    struct.pack_into("<I", result, opt + 64, checksum(result, opt + 64))
    return bytes(result), {
        "input_sha256": INPUT_SHA256, "output_sha256": sha(result),
        "enabled_opcodes": list(OPCODES), "bitmap_changes": list(changes.values()),
        "allowed_changed_ranges": [[at, at + 1] for at in sorted(changes)] + [[opt + 64, opt + 68]],
    }


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError("output must be a separate new file")
    result, receipt = build(source.read_bytes())
    with destination.open("xb") as stream:
        stream.write(result)
    if sha(source.read_bytes()) != INPUT_SHA256 or sha(destination.read_bytes()) != receipt["output_sha256"]:
        raise RuntimeError("file changed during candidate creation")
    return receipt


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("destination")
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.source, args.destination), indent=2))
