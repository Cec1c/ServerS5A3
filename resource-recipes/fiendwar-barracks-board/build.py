"""Apply the barracks repair animation fix to the public raid client."""
import argparse
import hashlib
import struct
from pathlib import Path

BASE_SHA256 = "a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf"


def file_offset(data, address):
    pe = struct.unpack_from("<I", data, 0x3c)[0]
    sections = struct.unpack_from("<H", data, pe + 6)[0]
    opt = pe + 24
    image_base = struct.unpack_from("<I", data, opt + 28)[0]
    table = opt + struct.unpack_from("<H", data, pe + 20)[0]
    for index in range(sections):
        start, size, raw = struct.unpack_from("<III", data, table + 40 * index + 12)
        pos = address - image_base - start
        if 0 <= pos < size:
            return raw + pos
    raise ValueError("address outside the paired image")


def build(source):
    if hashlib.sha256(source).hexdigest() != BASE_SHA256:
        raise ValueError("the input does not match the reviewed client")
    out = bytearray(source)

    def change(va, before, after):
        offset = file_offset(source, va)
        if len(before) != len(after) or source[offset:offset + len(before)] != before:
            raise ValueError("unexpected layout at " + hex(va))
        out[offset:offset + len(before)] = after

    # The renderer selects the repair widget only for a cleared command
    # center. Encode that selection in its local radar-visible flag: 0 stays
    # hidden, 1 becomes 2. All later visibility tests compare it with zero.
    change(0xc29e93, bytes.fromhex("385df3"), bytes.fromhex("d065f3"))

    # The original visible-radar path freezes the widget when the battery
    # flag is 1. For a repairing camp, always seek to its elapsed fraction.
    # Keep the battery check and original result for every other widget.
    original = bytes.fromhex("8bc8 e8f5fef5ff 8b4db0 84c0 0f8482000000")
    repaired = bytes.fromhex("91 e8f6fef5ff 8b4db0 3a45f3 0f8582000000")
    change(0xc2a014, original, repaired)

    # Windows uses a folded little-endian word sum plus the file length.
    pe = struct.unpack_from("<I", out, 0x3c)[0]
    checksum_field = pe + 24 + 64
    struct.pack_into("<I", out, checksum_field, 0)
    view = memoryview(out)
    total = sum(view[::2]) + (sum(view[1::2]) << 8)
    while total >> 16:
        total = (total & 0xffff) + (total >> 16)
    struct.pack_into("<I", out, checksum_field, total + len(out))
    if hashlib.sha256(out).hexdigest() != "e3e1b2bb2fdf689a82216923d1516505c606cc4ec56cfc81fa1bab527dd52ef8":
        raise ValueError("unexpected rebuilt board client")
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("original", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    data = build(args.original.read_bytes())
    with args.candidate.open("xb") as output:
        output.write(data)
    print(hashlib.sha256(data).hexdigest(), args.candidate)


if __name__ == "__main__":
    main()
