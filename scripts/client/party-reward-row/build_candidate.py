#!/usr/bin/env python3
"""Build the pinned A02 row-gate v2 layout directly from the fixed v161 image.

Standard library only. No workspace imports or intermediate v1 image. The
native predicate/wrappers retain the existing probe algorithm byte for byte.
Runtime parent acquisition/release writes reference counts and may dispose an
object at zero; the gate is not a completely mutation-free runtime operation.
"""
from __future__ import annotations

import argparse
import array
import hashlib
import json
import os
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True

BASE_SHA = "5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc"
BASE_SHA256 = BASE_SHA
INPUT_SIZE = 111890432
OUTPUT_SHA256 = "04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad"
OUTPUT_SIZE = 111894528
OUTPUT_NAME = "DNF-row-gate-experiment.exe"
RECEIPT_NAME = "build-receipt.json"

VA = 0x6EC2000
DRAW = VA + 0x300
INPUT = VA + 0x340
PASS = VA + 0x380
PAYLOAD_SIZE = 0x39A
PATCHES = (
    (0x491A078, 0x3829190, DRAW),
    (0x491A0C4, 0x3826930, INPUT),
    (0x491A074, 0x3829F50, PASS),
)
# Half-open file-offset ranges from the existing final v2 receipt. The new
# section's appended raw bytes are outside the original file, listed separately.
ORIGINAL_BYTE_WHITELIST = (
    (310, 312), (332, 336), (384, 388), (392, 396), (2272, 2312),
    (72458356, 72458360), (72458360, 72458364), (72458436, 72458440),
)

# Frozen final helper bytes from the existing v2 receipt. These are validation
# expectations; the four helpers below are still encoded from the original
# predicate/wrapper algorithm rather than copied from an intermediate EXE.
EXPECTED_HELPER_HEX = {
    VA: (
        "558bec83ec1853565733c08945f88945fc8b81e40300008945f485c00f84e0000000"
        "3dffff00000f83d50000008d45f850e8aa8a05fb8b75f885f60f84c100000083bebc"
        "030000000f85b4000000e8ad07a0fa84c00f85a7000000a158376a0585c00f849a"
        "0000008b787085ff0f848f000000897df00fb7472085c00f84800000003dffff0000"
        "0f83750000003b45f40f846c0000008b9fc804000085db0f8468000000508bcbe864"
        "10abfb83f8ff0f844d0000008b4df0e8c325aafb0fb6c085c00f843a0000008b5374"
        "85d20f84080000003bd00f85270000008b4df0e87d24aafb0fb6c03943700f851300"
        "0000ff75f48bcbe81710abfb83f8ff0f840a000000b801000000e90200000033c089"
        "45ec8b75fc85f60f842d0000008d460483c9fff00fc1080f851d0000008b068bce"
        "ff108d460883c9fff00fc1080f85070000008b068bceff50048b45ec5f5e5b8be55dc3"
    ),
    DRAW: "51e8fafcffff5985c00f8405000000e97c6e96fcb801000000c3",
    INPUT: "51e8bafcffff5985c00f8405000000e9dc4596fcb800000000c3",
    PASS: "51e87afcffff5985c00f8405000000e9bc7b96fcb801000000c3",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]


def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]


def align(value, alignment):
    require(alignment > 0 and not alignment & (alignment - 1),
            "alignment must be a positive power of two")
    return (value + alignment - 1) & ~(alignment - 1)


class PE:
    """Small bounded PE32 reader; all image reads use section raw mapping."""
    def __init__(self, data):
        self.data = data
        require(len(data) >= 0x40 and data[:2] == b"MZ", "missing MZ header")
        self.nt = u32(data, 0x3C)
        require(self.nt + 24 <= len(data), "truncated NT header")
        require(data[self.nt:self.nt + 4] == b"PE\0\0", "missing PE signature")
        require(u16(data, self.nt + 4) == 0x14C, "input is not x86 PE32")
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        optional_size = u16(data, self.nt + 20)
        require(optional_size >= 136 and self.opt + optional_size <= len(data),
                "optional header is truncated")
        require(u16(data, self.opt) == 0x10B, "optional header is not PE32")
        require(u32(data, self.opt + 92) >= 5, "certificate directory is absent")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + optional_size
        require(self.count > 0 and self.st + self.count * 40 <= len(data),
                "section table is truncated")
        self.sections = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from("<IIII", data, at + 8)
            require(raw + raw_size <= len(data), "section raw range exceeds file")
            self.sections.append(dict(
                index=index,
                name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                virtual_size=virtual_size, rva=rva, raw_size=raw_size, raw=raw,
                characteristics=u32(data, at + 36),
            ))

    def offset(self, va, size=1):
        require(size > 0, "empty VA window")
        rva = va - self.base
        matches = [s["raw"] + rva - s["rva"] for s in self.sections
                   if s["rva"] <= rva and rva + size <= s["rva"] + s["raw_size"]]
        require(len(matches) == 1, "VA has no unique raw mapping: 0x{:x}".format(va))
        return matches[0]

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]


def pe_checksum(data, checksum_offset):
    require(0 <= checksum_offset <= len(data) - 4, "invalid checksum offset")
    work = bytearray(data)
    struct.pack_into("<I", work, checksum_offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H", work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF


class Code:
    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[str, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        require(name not in self.labels, "duplicate code label: " + name)
        self.labels[name] = len(self.buf)

    def call(self, va: int) -> None:
        self.buf.append(0xE8)
        self.buf.extend(struct.pack("<i", va - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        self.buf.append(0xE9)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append(("jmp", offset, target))
        else:
            struct.pack_into("<i", self.buf, offset,
                             target - (self.base + offset + 4))

    def je(self, target: str) -> None:
        self.buf.extend(b"\x0f\x84")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("je", offset, target))

    def jne(self, target: str) -> None:
        self.buf.extend(b"\x0f\x85")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("jne", offset, target))

    def finish(self) -> bytes:
        for _, offset, label in self.fixups:
            require(label in self.labels, "unresolved code label: " + label)
            struct.pack_into("<i", self.buf, offset, self.labels[label] - offset - 4)
        return bytes(self.buf)


def predicate():
    c = Code(VA)

    def branch(op, label):
        c.raw(op)
        at = len(c.buf)
        c.raw('00000000')
        c.fixups.append(('branch', at, label))

    # Original probe algorithm. Parent acquisition/release writes refcounts;
    # zero-count release may dispatch disposal. Locals retain their old layout.
    c.raw('55 8bec 83ec18 53 56 57 33c0 8945f8 8945fc')
    c.raw('8b81e4030000 8945f4 85c0')
    c.je('allow')
    c.raw('3dffff0000')
    branch('0f83', 'allow')
    c.raw('8d45f8 50')
    c.call(0x1F1AAE0)
    c.raw('8b75f8 85f6')
    c.je('allow')
    c.raw('83bebc03000000')
    c.jne('allow')
    c.call(0x18C2800)
    c.raw('84c0')
    c.jne('allow')
    c.raw('a158376a05 85c0')
    c.je('allow')
    c.raw('8b7870 85ff')
    c.je('allow')
    c.raw('897df0 0fb74720 85c0')
    c.je('allow')
    c.raw('3dffff0000')
    branch('0f83', 'allow')
    c.raw('3b45f4')
    c.je('allow')
    c.raw('8b9fc8040000 85db')
    c.je('deny')
    c.raw('50 8bcb')
    c.call(0x2973110)
    c.raw('83f8ff')
    c.je('allow')
    c.raw('8b4df0')
    c.call(0x2964680)
    c.raw('0fb6c0 85c0')
    c.je('allow')
    c.raw('8b5374 85d2')
    c.je('owner_ok')
    c.raw('3bd0')
    c.jne('allow')
    c.label('owner_ok')
    c.raw('8b4df0')
    c.call(0x2964560)
    c.raw('0fb6c0 394370')
    c.jne('allow')
    c.raw('ff75f4 8bcb')
    c.call(0x2973110)
    c.raw('83f8ff')
    c.je('deny')
    c.label('allow')
    c.raw('b801000000')
    c.jump('release')
    c.label('deny')
    c.raw('33c0')
    c.label('release')
    c.raw('8945ec 8b75fc 85f6')
    c.je('done')
    c.raw('8d4604 83c9ff f00fc108')
    c.jne('done')
    c.raw('8b06 8bce ff10 8d4608 83c9ff f00fc108')
    c.jne('done')
    c.raw('8b06 8bce ff5004')
    c.label('done')
    c.raw('8b45ec 5f5e5b 8be5 5d c3')
    return c.finish()


def wrapper(address, original, hidden_return):
    c = Code(address)
    c.raw('51')
    c.call(VA)
    c.raw('59 85c0')
    c.je('hidden')
    c.jump(original)
    c.label('hidden')
    c.raw('b8' + struct.pack('<I', hidden_return).hex() + 'c3')
    return c.finish()


def helper_parts():
    parts = [(VA, predicate()), (DRAW, wrapper(DRAW, 0x3829190, 1)),
             (INPUT, wrapper(INPUT, 0x3826930, 0)),
             (PASS, wrapper(PASS, 0x3829F50, 1))]
    require([len(body) for _, body in parts] == [339, 26, 26, 26],
            "fixed helper sizes changed")
    for at, body in parts:
        require(body == bytes.fromhex(EXPECTED_HELPER_HEX[at]),
                "helper differs from frozen v2 bytes at 0x{:x}".format(at))
    require(all(left + len(body) <= right
                for (left, body), (right, _) in zip(parts, parts[1:])),
            "helper ranges overlap")
    return parts


def build(data):
    """Return (final_v2_bytes, receipt) without file I/O or native execution."""
    require(__debug__, "optimized Python is unsupported")
    require(len(data) == INPUT_SIZE, "fixed v161 input size mismatch")
    require(sha256_hex(data) == BASE_SHA, "fixed v161 input SHA-256 mismatch")
    pe = PE(data)
    require(pe.base == 0x400000, "unexpected image base")
    require(pe.base + u32(data, pe.opt + 56) == VA, "new section VA mismatch")
    require(not u16(data, pe.opt + 70) & 0x40,
            "dynamic base needs relocation handling")
    require(data[pe.opt + 128:pe.opt + 136] == bytes(8),
            "signed input is outside this fixed layout")
    require(len(data) == max(s["raw"] + s["raw_size"] for s in pe.sections),
            "input contains an overlay")

    parts = helper_parts()
    end = max(at + len(body) for at, body in parts)
    require(end - VA == PAYLOAD_SIZE, "final payload size changed")
    payload = bytearray(PAYLOAD_SIZE)
    for at, body in parts:
        payload[at - VA:at - VA + len(body)] = body

    out = bytearray(data)
    patches, edits, windows = [], [], []

    def record_window(name, offset, size):
        raw_bytes = data[offset:offset + size]
        require(len(raw_bytes) == size, "truncated original window: " + name)
        windows.append(dict(name=name, fileOffset=offset, size=size,
                            expected=raw_bytes.hex(), sha256=sha256_hex(raw_bytes)))

    for site, old, new in PATCHES:
        original, replacement = struct.pack("<I", old), struct.pack("<I", new)
        require(pe.read(site, 4) == original,
                "original vtable entry mismatch at 0x{:x}".format(site))
        offset = pe.offset(site, 4)
        record_window("vtable-{:x}".format(site), offset, 4)
        out[offset:offset + 4] = replacement
        edits.append((offset, offset + 4))
        patches.append(dict(site=hex(site), original=hex(old), target=hex(new),
                            fileOffset=offset, expected=original.hex(),
                            replacement=replacement.hex()))

    header = pe.st + pe.count * 40
    require(header + 40 <= u32(data, pe.opt + 60), "new section exceeds SizeOfHeaders")
    require(data[header:header + 40] == bytes(40), "new section header is not empty")
    require(header + 40 <= min(s["raw"] for s in pe.sections if s["raw_size"]),
            "new section header overlaps existing raw data")
    record_window("new-section-header", header, 40)

    raw = align(len(out), pe.fa)
    raw_size = align(len(payload), pe.fa)
    require(raw == INPUT_SIZE and raw_size == 4096, "fixed appended raw layout changed")
    out.extend(bytes(raw - len(out)))
    out.extend(payload)
    out.extend(bytes(raw + raw_size - len(out)))
    struct.pack_into("<8sIIIIIIHHI", out, header, b".rrow", len(payload),
                     VA - pe.base, raw_size, raw, 0, 0, 0, 0, 0x60000020)
    header_edits = (
        ("section-count", pe.nt + 6, 2),
        ("SizeOfCode", pe.opt + 4, 4),
        ("SizeOfImage", pe.opt + 56, 4),
        ("CheckSum", pe.opt + 64, 4),
    )
    for name, offset, size in header_edits:
        record_window(name, offset, size)
    struct.pack_into("<H", out, pe.nt + 6, pe.count + 1)
    struct.pack_into("<I", out, pe.opt + 4, u32(data, pe.opt + 4) + raw_size)
    struct.pack_into("<I", out, pe.opt + 56, align(end - pe.base, pe.sa))
    struct.pack_into("<I", out, pe.opt + 64, pe_checksum(out, pe.opt + 64))
    edits += [(header, header + 40)]
    edits += [(offset, offset + size) for _, offset, size in header_edits]
    edits.sort()
    require(tuple(edits) == ORIGINAL_BYTE_WHITELIST, "original-byte whitelist changed")
    cursor = 0
    for left, right in edits:
        require(cursor <= left < right <= len(data), "invalid original-byte range")
        require(out[cursor:left] == data[cursor:left],
                "original bytes changed before 0x{:x}".format(left))
        cursor = right
    require(out[cursor:len(data)] == data[cursor:],
            "original bytes changed after final allowed range")

    final = PE(out)
    require(final.count == pe.count + 1 and final.sections[-1]["name"] == ".rrow",
            "final section table mismatch")
    require(final.sections[-1]["virtual_size"] == PAYLOAD_SIZE
            and final.sections[-1]["raw_size"] == 4096
            and final.sections[-1]["characteristics"] == 0x60000020,
            "final RX section layout mismatch")
    require(u32(out, pe.opt + 64) == pe_checksum(out, pe.opt + 64),
            "output checksum does not verify")
    for site, _, target in PATCHES:
        require(final.read(site, 4) == struct.pack("<I", target),
                "vtable read-back failed")
    for at, body in parts:
        require(final.read(at, len(body)) == body, "helper read-back failed")
    require(final.read(VA, PAYLOAD_SIZE) == payload, "payload/gap read-back failed")
    require(out[raw + PAYLOAD_SIZE:raw + raw_size] == bytes(raw_size - PAYLOAD_SIZE),
            "new raw padding changed")
    require(len(out) == OUTPUT_SIZE, "fixed output size mismatch")
    digest = sha256_hex(out)
    require(digest == OUTPUT_SHA256, "fixed output SHA-256 mismatch: " + digest)

    receipt = dict(
        baseSha256=BASE_SHA, candidateSha256=digest, size=len(out),
        inputSha256=BASE_SHA, inputSize=INPUT_SIZE,
        outputSha256=digest, outputSize=len(out), expectedOutputSha256=OUTPUT_SHA256,
        outputName=OUTPUT_NAME, version=2, directFromV161=True, intermediateV1Built=False,
        helperAddress=hex(VA), helperSize=len(parts[0][1]), payloadSize=len(payload),
        helpers=[dict(address=hex(at), size=len(body), code=body.hex(),
                      sha256=sha256_hex(body)) for at, body in parts],
        patches=patches, patchCount=len(patches),
        originalByteWhitelist=[list(pair) for pair in edits],
        appendedByteRange=[raw, raw + raw_size], newSection=final.sections[-1],
        windowExpectations=windows, checksum=u32(out, pe.opt + 64), checksumVerified=True,
        existingBytesPreservedOutsideListedSitesAndHeaders=True,
        category=0, directInputPassGuard=True,
        mutations=(
            "The predicate temporarily acquires/releases the parent strong reference; "
            "these operations write reference counts. Normal retained-object cases "
            "restore its count, and zero-count release may call disposal. It does "
            "not write party keys, reward records, visibility, positions or a hidden-state cache."
        ),
        experimental=True, deployed=False, guiAccepted=False,
        limitations=[
            "full GUI, external consumers and resource lifecycle remain unverified",
            "physical row compaction is not implemented; original slot coordinates remain",
            "no-party policy is self-only when cached self is available",
            "unknown/mismatching context preserves original behavior",
            "not yet suitable for release",
        ],
        proofScope="fixed image construction, byte whitelist and read-back only; no native execution",
    )
    return bytes(out), receipt


def main(argv=None):
    require(__debug__, "optimized Python is unsupported")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client-exe", type=Path, required=True,
                        help="exact v161 input EXE; fixed SHA-256 is required")
    parser.add_argument("--output-dir", type=Path, required=True,
                        help="new directory under an existing parent; must not exist")
    args = parser.parse_args(argv)
    require(not os.path.lexists(args.output_dir), "output directory already exists")
    target = args.output_dir.resolve()
    require(not os.path.lexists(target), "resolved output directory already exists")
    require(target.parent.is_dir(), "output parent directory does not exist")
    source = args.client_exe.resolve(strict=True)
    require(source.is_file(), "client EXE is not a file")
    require(source.stat().st_size == INPUT_SIZE, "fixed v161 input size mismatch")
    candidate, receipt = build(source.read_bytes())
    receipt["builderSha256"] = sha256_hex(Path(__file__).read_bytes())

    # Reserve the destination only after all in-memory checks pass. mkdir is
    # exclusive even if another process created the target during construction.
    # Retain a partial NEW directory for diagnosis on I/O failure; never retry
    # into or remove an existing directory and never overwrite either artifact.
    target.mkdir(exist_ok=False)
    exe = target / OUTPUT_NAME
    with exe.open("xb") as handle:
        handle.write(candidate)
    disk = exe.read_bytes()
    require(len(disk) == OUTPUT_SIZE and sha256_hex(disk) == OUTPUT_SHA256,
            "EXE disk read-back mismatch; partial output retained")
    receipt["diskReadBackVerified"] = True
    receipt_path = target / RECEIPT_NAME
    with receipt_path.open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(receipt, handle, indent=2)
        handle.write("\n")
    require(json.loads(receipt_path.read_text(encoding="utf-8")) == receipt,
            "receipt disk read-back mismatch; partial output retained")
    print(json.dumps(dict(candidateSha256=OUTPUT_SHA256, size=OUTPUT_SIZE,
                          patchCount=3, outputDir=str(target), experimental=True,
                          deployed=False, guiAccepted=False)))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, struct.error) as exc:
        print(json.dumps(dict(status="blocked", error=str(exc))), file=sys.stderr)
        raise SystemExit(2)
