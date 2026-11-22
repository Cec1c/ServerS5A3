"""Check the rebuilt PE and all 65,536 receiver decisions on the actual input."""
import argparse
import hashlib
from pathlib import Path

import pefile

from build_client import INPUTS

EXPECTED = {
    "a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf":
        "fb2db71ee7cb4956e91891eea9731a2999cbb3694e681801e139239833458927",
    "e3e1b2bb2fdf689a82216923d1516505c606cc4ec56cfc81fa1bab527dd52ef8":
        "21a9ed0b0c86e8e3a95f7b6c84270025c91219d3df3e9143bdd6629264c9166e",
}


def check(before, after):
    source = hashlib.sha256(before).hexdigest()
    assert source in INPUTS and hashlib.sha256(after).hexdigest() == EXPECTED[source]
    pe = pefile.PE(data=after, fast_load=True)
    def offset(va):
        return pe.get_offset_from_rva(va - pe.OPTIONAL_HEADER.ImageBase)
    bitmap = offset(0x06e92300)
    def accepted(image, opcode):
        return opcode <= 1474 and bool(image[bitmap + opcode // 8] & (1 << (opcode % 8)))
    assert [n for n in range(65536) if accepted(before, n) != accepted(after, n)] == [427]
    assert accepted(after, 427)
    assert after[offset(0x028e817f):offset(0x028e817f)+2] == bytes.fromhex("eb53")
    guard = offset(0x028e81d2)
    assert after[guard:guard+14] == bytes.fromhex("eb1b 85c0 7417 8d888c000000 eba5")
    assert pe.verify_checksum()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    check(args.input.read_bytes(), args.output.read_bytes())
    print("Verified input, candidate, receiver bitmap, frame guard and PE checksum")
