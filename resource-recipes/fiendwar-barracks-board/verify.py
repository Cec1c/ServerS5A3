"""Verify the public inputs and every changed byte in the barracks board recipe."""
import argparse
import hashlib
import struct
from pathlib import Path

from build import BASE_SHA256, file_offset


def verify(original, updated):
    if hashlib.sha256(original).hexdigest() != BASE_SHA256:
        raise ValueError("unexpected original")
    if hashlib.sha256(updated).hexdigest() != "e3e1b2bb2fdf689a82216923d1516505c606cc4ec56cfc81fa1bab527dd52ef8":
        raise ValueError("unexpected board output")
    pe = struct.unpack_from("<I", original, 0x3c)[0]
    checksum_offset = pe + 24 + 64
    edits = [(checksum_offset, 4), (file_offset(original, 0xc29e93), 3),
             (file_offset(original, 0xc2a014), 18)]
    cursor = 0
    for offset, size in sorted(edits):
        if memoryview(original)[cursor:offset] != memoryview(updated)[cursor:offset]:
            raise ValueError("unexpected change outside board edits")
        cursor = offset + size
    if memoryview(original)[cursor:] != memoryview(updated)[cursor:]:
        raise ValueError("unexpected change after board edits")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("original", type=Path)
    parser.add_argument("updated", type=Path)
    args = parser.parse_args()
    verify(args.original.read_bytes(), args.updated.read_bytes())
    print("Verified board client and edit boundaries")
