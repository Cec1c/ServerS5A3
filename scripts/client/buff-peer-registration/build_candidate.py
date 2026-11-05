"""Scope the native inventory-busy guard to the local Buff owner.

Fixed cumulative input only. Builds a separate offline candidate; no install.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("buff_pe", ROOT / "quiet-profile-refresh/build_candidate.py")
peutils = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peutils)
INPUT_SHA = "e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4"
SUPPORTED_INPUTS = {
    INPUT_SHA,
    "fbb0b5d6c12387242745c15ea947e4cf2651aa2d80ce99d9e3256b18c3e8100d",
}
INPUT_SIZE = 112607232
SITE = 0x22C3787
ORIGINAL = bytes.fromhex("8bcfe8826b3200")
HELPER = 0x6F9DB00


def helper():
    code = bytearray(b"\x50")  # preserve the incoming EAX across local actor lookup
    def rel(op, target):
        code.extend(op)
        code.extend(struct.pack("<i", target - (HELPER + len(code) + 4)))
    rel(b"\xe8", 0x29610E0)
    code.extend(bytes.fromhex("398578feffff58"))  # cmp [ebp-188],eax; pop eax
    rel(b"\x0f\x85", 0x22C3796)  # a remote owner must consume its transmitted row
    code.extend(b"\x8b\xcf")
    rel(b"\xe8", 0x25EA310)  # retain original local inventory-busy policy
    rel(b"\xe9", 0x22C378E)
    return bytes(code)


def build(data):
    require = peutils.require
    input_sha = hashlib.sha256(data).hexdigest()
    require(len(data) == INPUT_SIZE and input_sha in SUPPORTED_INPUTS,
            "exact cumulative input SHA256 required")
    pe = peutils.PE(data)
    pe.require_contiguous()
    require(pe.read(SITE, len(ORIGINAL)) == ORIGINAL, "native admission instructions differ")
    section = next(s for s in pe.sections if s["name"] == ".krtext")
    code = helper()
    offset = pe.offset(HELPER, len(code))
    delta = HELPER - pe.base - section["rva"]
    require(section["virtual_size"] <= delta and delta + len(code) <= section["raw_size"], "helper is not in unused raw padding")
    require(data[offset:offset+len(code)] == bytes(len(code)), "padding is not empty")
    require(section["characteristics"] == 0x60000020, "helper section must be RX")
    header = pe.st + section["index"] * 40 + 8
    out = bytearray(data)
    site = pe.offset(SITE, len(ORIGINAL))
    out[site:site+len(ORIGINAL)] = b"\xe9" + struct.pack("<i", HELPER-SITE-5) + b"\x90\x90"
    out[offset:offset+len(code)] = code
    struct.pack_into("<I", out, header, delta+len(code))
    struct.pack_into("<I", out, pe.opt+64, peutils.pe_checksum(out, pe.opt+64))
    ranges = sorted([(site,site+len(ORIGINAL)),(offset,offset+len(code)),(header,header+4),(pe.opt+64,pe.opt+68)])
    normalized = bytearray(out)
    for lo,hi in ranges:
        normalized[lo:hi] = data[lo:hi]
    require(normalized == data, "unapproved byte changed")
    final = peutils.PE(out)
    final.require_contiguous()
    require(final.count == pe.count and len(out) == len(data), "image grew")
    return bytes(out), dict(inputSha256=input_sha,outputSha256=hashlib.sha256(out).hexdigest(),
        size=len(out),hook=hex(SITE),helper=hex(HELPER),helperBytes=code.hex(),
        changedRanges=ranges,newSections=0,installed=False,released=False,
        scope="Remote Buff rows are independent of the viewer's local inventory-busy flag; local path preserved")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result, receipt = build(args.input.read_bytes())
    with args.output.open("xb") as stream:
        stream.write(result)
    peutils.require(hashlib.sha256(args.output.read_bytes()).hexdigest() == receipt["outputSha256"], "output readback differs")
    print(json.dumps(receipt, indent=2))
