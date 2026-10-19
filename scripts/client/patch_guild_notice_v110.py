"""Keep NOTI70's notice cache current while preserving its one-time chat toast.

Input is the exact member-menu-fixed V110 derivative. Output is a new file.
"""
import argparse
import json
from pathlib import Path
import struct

from patch_guild_member_menu_v110 import align, checksum, headers, offset_for_rva, sha

INPUT_SHA256 = "413bbe6b900c419ec430ec10ba9acd3307d678351d423be6ae6007ce91078bd4"
HOOK_RVA = 0x1C3ED78
RETURN_RVA = HOOK_RVA + 6
SETTER_RVA = 0x33E3000
DISPLACED = bytes.fromhex("8d8d38f4ffff")


def thunk(rva):
    code = bytearray.fromhex("9c608b0d88376a0585c97400")
    code.extend(bytes.fromhex("8d9520fdffff5283c118"))
    code.extend(b"\xe8" + struct.pack("<i", SETTER_RVA - (rva + len(code) + 5)))
    code[11] = len(code) - 12
    code.extend(bytes.fromhex("619d"))
    code.extend(DISPLACED)
    code.extend(b"\xe9" + struct.pack("<i", RETURN_RVA - (rva + len(code) + 5)))
    return bytes(code)


def build(data):
    if sha(data) != INPUT_SHA256:
        raise ValueError("requires the exact member-menu-fixed V110 derivative")
    pe, opt, table, sections = headers(data)
    if len(sections) != 16 or struct.unpack_from("<I", data, opt + 28)[0] != 0x400000:
        raise ValueError("unexpected image layout")
    sa, fa = struct.unpack_from("<II", data, opt + 32)
    size, headsize = struct.unpack_from("<II", data, opt + 56)
    header = table + 40 * len(sections)
    if header + 40 > headsize or any(data[header:header+40]):
        raise ValueError("no unused section header")
    end = max(s["offset"] + s["rawsize"] for s in sections)
    if end != len(data) or align(end, fa) != end:
        raise ValueError("unexpected overlay/alignment")
    rva = align(max(s["rva"] + max(s["size"], s["rawsize"]) for s in sections), sa)
    if rva != size:
        raise ValueError("unexpected SizeOfImage")
    hook = offset_for_rva(sections, HOOK_RVA, 6)
    if data[hook:hook+6] != DISPLACED:
        raise ValueError("unexpected NOTI70 continuation")
    body = thunk(rva)
    rawsize = align(len(body), fa)
    result = bytearray(data) + body + bytes(rawsize-len(body))
    result[hook:hook+6] = b"\xe9" + struct.pack("<i", rva-HOOK_RVA-5) + b"\x90"
    result[header:header+40] = struct.pack("<8sIIIIIIHHI", b".gnotice", len(body), rva, rawsize, end, 0, 0, 0, 0, 0x60000020)
    struct.pack_into("<H", result, pe+6, len(sections)+1)
    struct.pack_into("<I", result, opt+4, struct.unpack_from("<I", data, opt+4)[0]+rawsize)
    struct.pack_into("<I", result, opt+56, align(rva+len(body), sa))
    struct.pack_into("<I", result, opt+64, checksum(result,opt+64))
    return bytes(result), {"input_sha256":INPUT_SHA256,"output_sha256":sha(result),
        "hook_rva":hex(HOOK_RVA),"thunk_rva":hex(rva),"thunk_hex":body.hex(),
        "allowed_changed_ranges":[[hook,hook+6],[header,header+40],[pe+6,pe+8],[opt+4,opt+8],[opt+56,opt+60],[opt+64,opt+68]]}


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
    print(json.dumps(write_candidate(args.source,args.destination),indent=2))
