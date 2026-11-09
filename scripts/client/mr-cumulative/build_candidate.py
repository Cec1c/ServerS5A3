"""Build the reviewed PVP/wedding admission layer on the exact paired Buff client.

This recipe only creates an independent local-test candidate. It neither runs
the game nor installs/releases files. Unknown inputs and existing outputs fail.
"""
from pathlib import Path
import argparse
import hashlib
import json
import struct
import sys

SCRIPTS = Path(__file__).resolve().parent.parent
sys.path[:0] = [str(SCRIPTS / "local-patches"), str(SCRIPTS / "local-patches/patches")]
from pe_image import Image, pe_checksum, rel32
import imeup_all_keys

INPUT_SHA = "0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f"
ADMISSION_SHA = "99c4cd6d840eb9a61caadeb4da13bd6f5ac8672494a7e39525bda7430fbac657"
OUTPUT_SHA = "748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82"
SIZE = 112607232
CALL, FALLBACK, ENTRY = 0x2523A13, 0x6F88500, 0x6F88AC0
CLASS0 = (112, 113, 997, 998, 999, 1000, 1001, 1002, 1003, 1004, 1005,
          1006, 1007, 1008, 1009, 1011, 1012, 1013, 1014, 1015, 1016)
CLASS1 = tuple(range(1032, 1054))


def sha(data):
    return hashlib.sha256(data).hexdigest()


def helper(base):
    code, labels, fixes = bytearray(), {}, []

    def branch(op, label):
        code.extend(bytes.fromhex(op))
        fixes.append((len(code), label))
        code.extend(bytes(4))

    def compares(ids):
        for opcode in ids:
            code.extend(b"\x3d" + struct.pack("<I", opcode))
            branch("0f84", "allow")

    code.extend(bytes.fromhex("803f00"))
    branch("0f84", "class0")
    code.extend(bytes.fromhex("803f01"))
    branch("0f85", "fallback")
    code.extend(bytes.fromhex("0fb7442404"))
    compares(CLASS1)
    branch("e9", "fallback")
    labels["class0"] = len(code)
    code.extend(bytes.fromhex("0fb7442404"))
    compares(CLASS0)
    branch("e9", "fallback")
    labels["allow"] = len(code)
    code.extend(bytes.fromhex("b801000000c20400"))
    labels["fallback"] = len(code)
    code.extend(b"\xe9" + rel32(base + len(code), 5, FALLBACK))
    for offset, label in fixes:
        struct.pack_into("<i", code, offset, labels[label] - offset - 4)
    return bytes(code)


def build(raw):
    if len(raw) != SIZE or sha(raw) != INPUT_SHA:
        raise ValueError("exact reviewed paired Buff client required")
    image = Image(raw)
    if image.rel32_target(CALL) != FALLBACK:
        raise ValueError("existing admission fallback differs")
    section = image.section(".gld127")
    if section is None or section["flags"] != 0x60000020:
        raise ValueError("reviewed RX section missing")
    offset = image.offset(ENTRY, len(helper(ENTRY)))
    delta = ENTRY - 0x400000 - section["rva"]
    if delta != ((section["vsize"] + 15) & ~15):
        raise ValueError("owned RX padding moved")
    body = helper(ENTRY)
    if delta + len(body) > section["rsize"] or raw[offset:offset + len(body)] != bytes(len(body)):
        raise ValueError("owned RX padding unavailable")
    out, ranges = bytearray(raw), []

    def write(at, value):
        out[at:at + len(value)] = value
        ranges.append((at, at + len(value)))

    write(image.offset(CALL, 5), b"\xe8" + rel32(CALL, 5, ENTRY))
    write(offset, body)
    write(section["header"] + 8, struct.pack("<I", delta + len(body)))
    write(image.checksum_at, struct.pack("<I", pe_checksum(out, image.checksum_at)))
    admission = bytes(out)
    if sha(admission) != ADMISSION_SHA:
        raise ValueError("admission candidate identity differs")
    state, _ = imeup_all_keys.inspect(Image(out))
    if state != "absent":
        raise ValueError("expected reviewed direction-only release helper")
    ranges.extend((lo, hi) for lo, hi, _ in imeup_all_keys.apply(out))
    struct.pack_into("<I", out, image.checksum_at, pe_checksum(out, image.checksum_at))
    restored = bytearray(out)
    for lo, hi in ranges:
        restored[lo:hi] = raw[lo:hi]
    if restored != raw or sha(out) != OUTPUT_SHA:
        raise ValueError("candidate differs from reviewed byte ranges or identity")
    receipt = dict(inputSHA256=INPUT_SHA, admissionSHA256=ADMISSION_SHA,
                   candidateSHA256=OUTPUT_SHA, bytes=len(out), helperVA=hex(ENTRY),
                   helperBytes=len(body), fallback=hex(FALLBACK), section=section["name"],
                   class0=CLASS0, class1=CLASS1, changedRanges=ranges,
                   allOtherBytesPreserved=True, installed=False, gameExecuted=False,
                   testOnly=True)
    return admission, bytes(out), receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    if args.output_dir.exists():
        parser.error("new independent output directory required")
    admission, candidate, receipt = build(args.input.read_bytes())
    if sha(args.input.read_bytes()) != INPUT_SHA:
        raise ValueError("input changed during build")
    args.output_dir.mkdir(parents=True, exist_ok=False)
    for name, data in (("DNF-admission.exe", admission), ("DNF.exe", candidate)):
        with (args.output_dir / name).open("xb") as stream:
            stream.write(data)
    with (args.output_dir / "build.json").open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps(receipt, ensure_ascii=False))


if __name__ == "__main__":
    main()
