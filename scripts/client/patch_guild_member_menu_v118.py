"""Build an exact V118 derivative fixing the guild member menu's null lookup.

The input must be the complete approved V118 image. The output is a separate,
new file; this tool never installs a client or changes an update channel.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

INPUT_SHA256 = "269a7b6a4c8df4cc537780c947da22baf1a02839bc4cbc6bbb446f5f8f392026"
CALL_RVA = 0x03143C70
GETTER_RVA = 0x02561D40
IMAGE_BASE = 0x400000
# Preserve the original getter's flags and all registers except its EAX result.
# A menu opened by name may have no scene actor. Its existing server identity
# remains zero in that case; a non-null actor follows the original getter.
THUNK = bytes.fromhex("9c85c974088b81ac0300009dc331c09dc3")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def align(value, alignment):
    if alignment <= 0 or alignment & (alignment - 1):
        raise ValueError("invalid PE alignment")
    return (value + alignment - 1) & -alignment


def headers(data):
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[:2] != b"MZ" or data[pe:pe + 4] != b"PE\0\0":
        raise ValueError("not a PE image")
    machine, count = struct.unpack_from("<HH", data, pe + 4)
    optsize = struct.unpack_from("<H", data, pe + 20)[0]
    opt = pe + 24
    if machine != 0x14C or struct.unpack_from("<H", data, opt)[0] != 0x10B:
        raise ValueError("not the expected x86 PE32 image")
    table = opt + optsize
    sections = []
    for i in range(count):
        at = table + 40 * i
        name, size, rva, rawsize, offset, _, _, _, _, flags = struct.unpack_from("<8sIIIIIIHHI", data, at)
        sections.append(dict(name=name, size=size, rva=rva, rawsize=rawsize, offset=offset, flags=flags))
    return pe, opt, table, sections


def offset_for_rva(sections, rva, size=1):
    for s in sections:
        delta = rva - s["rva"]
        if 0 <= delta and delta + size <= s["rawsize"]:
            return s["offset"] + delta
    raise ValueError("RVA is outside backed sections")


def checksum(data, field):
    value = 0
    for offset in range(0, len(data), 2):
        if field <= offset < field + 4:
            continue
        word = data[offset] | ((data[offset + 1] if offset + 1 < len(data) else 0) << 8)
        value += word
        value = (value & 0xFFFF) + (value >> 16)
    value = (value & 0xFFFF) + (value >> 16)
    return value + len(data)


def build(data):
    if sha(data) != INPUT_SHA256:
        raise ValueError("input SHA256 is not the approved V118 image")
    pe, opt, table, sections = headers(data)
    imagebase = struct.unpack_from("<I", data, opt + 28)[0]
    section_alignment, file_alignment = struct.unpack_from("<II", data, opt + 32)
    image_size, header_size = struct.unpack_from("<II", data, opt + 56)
    if imagebase != IMAGE_BASE or len(sections) != 16:
        raise ValueError("unexpected V118 layout")
    if any(struct.unpack_from("<II", data, opt + 96 + 4 * 8)):
        raise ValueError("signed PE requires a separate signing workflow")
    section_header = table + len(sections) * 40
    if section_header + 40 > header_size or any(data[section_header:section_header + 40]):
        raise ValueError("no unused section header")
    end = max(s["offset"] + s["rawsize"] for s in sections)
    if end != len(data) or align(end, file_alignment) != end:
        raise ValueError("unexpected overlay or file alignment")
    rva = align(max(s["rva"] + max(s["size"], s["rawsize"]) for s in sections), section_alignment)
    if rva != image_size:
        raise ValueError("unexpected SizeOfImage")
    call = offset_for_rva(sections, CALL_RVA, 5)
    expected = b"\xe8" + struct.pack("<i", GETTER_RVA - CALL_RVA - 5)
    if data[call:call + 5] != expected:
        raise ValueError("unexpected member-menu call")
    context = offset_for_rva(sections, CALL_RVA - 8, 16)
    if data[context:context + 16] != bytes.fromhex("837b7000750a8bcfe8cbe041ff894370"):
        raise ValueError("unexpected cached-identity/menu context")
    getter = offset_for_rva(sections, GETTER_RVA, 7)
    if data[getter:getter + 7] != bytes.fromhex("8b81ac030000c3"):
        raise ValueError("unexpected original actor getter")
    rawsize = align(len(THUNK), file_alignment)
    result = bytearray(data) + THUNK + bytes(rawsize - len(THUNK))
    result[call:call + 5] = b"\xe8" + struct.pack("<i", rva - CALL_RVA - 5)
    result[section_header:section_header + 40] = struct.pack(
        "<8sIIIIIIHHI", b".gmenu", len(THUNK), rva, rawsize, end, 0, 0, 0, 0, 0x60000020)
    struct.pack_into("<H", result, pe + 6, len(sections) + 1)
    struct.pack_into("<I", result, opt + 4, struct.unpack_from("<I", data, opt + 4)[0] + rawsize)
    struct.pack_into("<I", result, opt + 56, align(rva + len(THUNK), section_alignment))
    struct.pack_into("<I", result, opt + 64, checksum(result, opt + 64))
    return bytes(result), {"input_sha256": INPUT_SHA256, "output_sha256": sha(result),
        "call_rva": hex(CALL_RVA), "thunk_rva": hex(rva), "thunk_hex": THUNK.hex(),
        "new_section_offset": end, "new_section_size": rawsize,
        "allowed_changed_ranges": [[call, call + 5], [section_header, section_header + 40],
            [pe + 6, pe + 8], [opt + 4, opt + 8], [opt + 56, opt + 60], [opt + 64, opt + 68]]}


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError("output must be a separate new file")
    before = source.read_bytes()
    result, receipt = build(before)
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
