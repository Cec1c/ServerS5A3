"""Add the native mileage balance receiver and protect the optional Rosch bubble frame."""
import argparse
import hashlib
import struct
from pathlib import Path

import pefile

INPUTS = {
    "a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf",
    "e3e1b2bb2fdf689a82216923d1516505c606cc4ec56cfc81fa1bab527dd52ef8",
}
CLEANUP = bytes.fromhex("8b4df4 64890d00000000 59 5f 5e 8be5 5d c20400")


def build(data):
    if hashlib.sha256(data).hexdigest() not in INPUTS:
        raise ValueError("unknown client version")
    pe = pefile.PE(data=data, fast_load=True)
    image = bytearray(data)

    def offset(va):
        return pe.get_offset_from_rva(va - pe.OPTIONAL_HEADER.ImageBase)

    def check(va, expected):
        start = offset(va)
        if data[start:start + len(expected)] != expected:
            raise ValueError(f"unexpected layout at {va:#x}")
        return start

    def replace(va, before, after):
        if len(before) != len(after):
            raise ValueError("invalid edit size")
        start = check(va, before)
        image[start:start + len(before)] = after

    check(0x06e83610, bytes.fromhex("0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400"))
    check(0x0376e6c0, bytes.fromhex("e94b4f7103"))
    check(0x0203221b, bytes.fromhex("6a006860f7000268ab0100008bcee872204f00"))
    replace(0x06e92300 + 427 // 8, b"\x00", bytes([1 << (427 % 8)]))

    # Reuse the identical second epilogue when the optional fourth frame is absent.
    check(0x028e81ef, CLEANUP)
    field = bytes.fromhex("8d888c000000")
    replace(0x028e817f, field, bytes.fromhex("eb53 90909090"))
    guard = bytes.fromhex("eb1b 85c0 7417") + field + bytes.fromhex("eba5")
    replace(0x028e81d2, CLEANUP, guard + CLEANUP[len(guard):])

    check_offset = pe.OPTIONAL_HEADER.get_field_absolute_offset("CheckSum")
    struct.pack_into("<I", image, check_offset, 0)
    view = memoryview(image)
    checksum = sum(view[::2]) + (sum(view[1::2]) << 8)
    while checksum >> 16:
        checksum = (checksum & 0xffff) + (checksum >> 16)
    struct.pack_into("<I", image, check_offset, checksum + len(image))
    return image


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    data = build(args.input.read_bytes())
    with args.out.open("xb") as stream:
        stream.write(data)
    print(hashlib.sha256(data).hexdigest())
