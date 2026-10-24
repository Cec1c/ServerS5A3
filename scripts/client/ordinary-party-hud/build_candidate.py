#!/usr/bin/env python3
"""Portable fixed-byte builder for the reviewed v4 ordinary-HUD candidate.

The input is the exact published v159 image. This tool contains the reviewed
PE support, Code encoder, lookup helper, two draw helpers, count helper and v4
patch layout. It writes a new directory through a staging directory and checks
the candidate again from disk before publishing it. It does not execute the
game or install the result.
"""
from __future__ import annotations

import argparse
import array
import hashlib
import json
import struct
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True

BASE_SHA256 = "db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0"
INPUT_SIZE = 111886336
OUTPUT_SHA256 = "a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1"
OUTPUT_SIZE = 111890432
OUTPUT_NAME = "DNF-hud-local-candidate.exe"
RECEIPT_NAME = "build-receipt.json"

VA = 0x6EC1000
SITES = (0x221F3B3, 0x224A3BB, 0x224AF6E)
DRAW = (
    dict(site=0x224A647, entry=VA + 0x200, actor=-0x678, slot=-0x66C,
         next=0x224A64D, done=0x224A679, reg="edx", saved_y=-0x690, tail=0x224AE07),
    dict(site=0x224B282, entry=VA + 0x300, actor=-0x66C, slot=-0x684,
         next=0x224B288, done=0x224B2B4, reg="ecx", saved_y=-0x688, tail=0x224B718),
)
COUNT_SITE = 0x224A2D3
COUNT_ENTRY = VA + 0x400


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
    require(alignment and not alignment & (alignment - 1),
            "alignment must be a power of two")
    return (value + alignment - 1) & ~(alignment - 1)


class PE:
    """Minimal PE32 reader used for validation and VA-to-file mapping."""

    def __init__(self, data):
        self.data = data
        require(data[:2] == b"MZ", "input is not an MZ image")
        self.nt = u32(data, 0x3C)
        require(data[self.nt:self.nt + 4] == b"PE\0\0", "missing PE signature")
        require(u16(data, self.nt + 4) == 0x14C, "input is not PE32")
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        require(u16(data, self.opt) == 0x10B,
                "input is not a PE32 optional header")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from(
                "<IIII", data, at + 8)
            self.sections.append(dict(
                index=index,
                name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                virtual_size=virtual_size,
                rva=rva,
                raw_size=raw_size,
                raw=raw,
                characteristics=u32(data, at + 36),
            ))

    def offset(self, va, size=1):
        rva = va - self.base
        for section in self.sections:
            if (section["rva"] <= rva and
                    rva + size <= section["rva"] + section["raw_size"]):
                return section["raw"] + rva - section["rva"]
        raise ValueError("VA has no raw backing: 0x{:x} / 0x{:x}".format(va, size))

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]


def pe_checksum(data, checksum_offset):
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
            struct.pack_into("<i", self.buf, offset,
                             self.labels[label] - offset - 4)
        return bytes(self.buf)


def helper():
    c = Code(VA)

    def branch(op, label):
        c.raw(op)
        at = len(c.buf)
        c.raw("00000000")
        c.fixups.append(("branch", at, label))

    # thiscall(manager, slot), returning an actor without publishing it to the
    # scene/party model tables. Preserve the original nonvolatile registers.
    c.raw("55 8bec 83ec0c 53 56 57 8b5d08 53")
    c.call(0x2974890)
    c.raw("85c0")
    c.jne("done")
    c.raw("83fb07")
    branch("0f87", "miss")
    c.call(0x18C2800)
    c.raw("84c0")
    c.jne("miss")
    c.raw("8b0d58376a05 85c9")
    c.je("miss")
    # Existing cached local actor only. Do not lazily create/rebind anything.
    c.raw("8b4170 85c0")
    c.je("miss")
    c.raw("8945fc 8bb0c8040000 85f6")
    c.je("miss")
    # The currently associated party must still contain the local identity.
    c.raw("0fb74020 50 8bce")
    c.call(0x2973110)
    c.raw("83f8ff")
    c.je("miss")
    c.raw("8b7c9e2c 85ff")
    c.je("miss")
    c.raw("81ffffff0000")
    branch("0f83", "miss")
    # Resolve the zero scene namespace through the existing native getters;
    # verify the party namespace agrees before looking up mode8 data.
    c.raw("8b4dfc")
    c.call(0x2964680)
    c.raw("0fb6c0 85c0")
    c.je("miss")
    c.raw("8945f8 8b5674 85d2")
    c.je("owner_ok")
    c.raw("3bd0")
    c.jne("miss")
    c.label("owner_ok")
    c.raw("8b4dfc")
    c.call(0x2964560)
    c.raw("0fb6c0 8945f4 394670")
    c.jne("miss")
    c.raw("50 ff75f8 57")
    c.call(0x1A5A5E0)
    c.raw("83c40c 85c0")
    c.je("miss")
    c.raw("0fb75020 3bd7")
    c.jne("miss")
    c.raw("0fb69004060000 3b55f8")
    c.jne("miss")
    c.raw("0fb69005060000 3b55f4")
    c.jne("miss")
    # Both renderers dereference stats. Metadata-only entries are insufficient.
    c.raw("83b8f804000000")
    c.je("miss")
    c.jump("done")
    c.label("miss")
    c.raw("33c0")
    c.label("done")
    c.raw("5f 5e 5b 8be5 5d c20400")
    return c.finish()


def draw_helper(d):
    c = Code(d["entry"])

    def disp(prefix, value):
        c.raw(prefix + struct.pack("<i", value).hex())

    c.raw("9c 53 56 57")
    disp("8bb5", d["actor"])  # esi = actor already authorized by row lookup
    c.raw("85f6")
    c.je("original")
    disp("ffb5", d["slot"])
    c.raw("8b0d68376a05")
    c.call(0x2974890)
    c.raw("3bc6")
    c.je("original")
    # A different non-null scene slot is never replaced during this draw.
    c.raw("85c0")
    c.jne("original")
    # Preserve the original row Y scratch consumed by later status controls.
    c.raw("8d4302")  # eax = ebx + 2
    disp("8985", d["saved_y"])
    # Exact 24x24 surface size from 29777BC..297781B, displayed at x/y+2.
    # Lazy creation uses the already-shipped v159 auxiliary-registration guard.
    c.raw("68ff000000 6a00 6a01 6a18 6a18 8d4302 50 8d4702 50 8bce 8b06 ff5004")
    c.raw("5f 5e 5b 9d")
    c.jump(d["done"])
    c.label("original")
    c.raw("5f 5e 5b 9d")
    disp("8b95" if d["reg"] == "edx" else "8b8d", d["slot"])
    c.jump(d["next"])
    return c.finish()


def count_helper():
    c = Code(COUNT_ENTRY)
    c.raw("55 8bec 83ec04 53 56 57 8bf1 ff7508")
    c.call(0x2974390)
    c.raw("8945fc 83f802")
    c.raw("0f83")
    at = len(c.buf)
    c.raw("00000000")
    c.fixups.append(("jae", at, "done"))
    c.raw("807d0800")
    c.jne("done")
    c.raw("33db 33ff")
    c.label("slot")
    c.raw("53 8bce")
    c.call(VA)
    c.raw("85c0")
    c.je("next")
    c.raw("47")
    c.label("next")
    c.raw("43 83fb08")
    c.jne("slot")
    c.raw("8b45fc 3bf8 0f47c7")  # max(original count, UI-resolvable count)
    c.label("done")
    c.raw("5f 5e 5b 8be5 5d c20400")
    return c.finish()


def build(data):
    require(len(data) == INPUT_SIZE, "fixed base size mismatch")
    require(sha256_hex(data) == BASE_SHA256,
            "input SHA-256 does not match the fixed v159 base")

    pe = PE(data)
    require(pe.base == 0x400000, "unexpected image base")
    require(not u16(data, pe.opt + 70) & 0x40,
            "dynamic base needs relocation handling; refusing this input")
    require(u32(data, pe.opt + 128) == 0 and u32(data, pe.opt + 132) == 0,
            "signed input needs a separate certificate-table design")
    require(pe.base + u32(data, pe.opt + 56) == VA,
            "new section address does not match the reviewed v4 layout")
    require(len(data) == max(s["raw"] + s["raw_size"] for s in pe.sections),
            "input contains an overlay")

    parts = [
        (VA, helper()),
        (DRAW[0]["entry"], draw_helper(DRAW[0])),
        (DRAW[1]["entry"], draw_helper(DRAW[1])),
        (COUNT_ENTRY, count_helper()),
    ]
    require(all(len(code) for _, code in parts), "empty helper")
    ranges = sorted((at, at + len(code)) for at, code in parts)
    require(all(left[1] <= right[0] for left, right in zip(ranges, ranges[1:])),
            "helper ranges overlap")
    end = max(at + len(code) for at, code in parts)
    payload = bytearray(end - VA)
    for at, code in parts:
        payload[at - VA:at - VA + len(code)] = code

    patches = []
    for site in SITES:
        patches.append(dict(
            site=site,
            target=VA,
            expected=b"\xe8" + struct.pack("<i", 0x2974890 - site - 5),
            op=0xE8,
        ))
    for d in DRAW:
        expected = ((b"\x8b\x95" if d["reg"] == "edx" else b"\x8b\x8d") +
                    struct.pack("<i", d["slot"]))
        patches.append(dict(
            site=d["site"], target=d["entry"], expected=expected, op=0xE9))
    patches.append(dict(
        site=COUNT_SITE,
        target=COUNT_ENTRY,
        expected=b"\xe8" + struct.pack("<i", 0x2974390 - COUNT_SITE - 5),
        op=0xE8,
    ))
    require(len(patches) == 6, "v4 must contain exactly six patches")

    window_expectations = []

    def record_window(name, offset, size, expected=None):
        raw = data[offset:offset + size]
        require(len(raw) == size, "{}: truncated input window".format(name))
        if expected is not None:
            require(raw == expected,
                    "{}: original window does not match".format(name))
        window_expectations.append(dict(
            name=name,
            offset=hex(offset),
            size=size,
            expected=raw.hex(),
            sha256=sha256_hex(raw),
        ))

    record_window("dos-mz", 0, 2, b"MZ")
    record_window("pe-offset", 0x3C, 4)
    record_window("pe-signature", pe.nt, 4, b"PE\0\0")
    record_window("machine", pe.nt + 4, 2, struct.pack("<H", 0x14C))
    record_window("section-count", pe.nt + 6, 2)
    record_window("optional-magic", pe.opt, 2, struct.pack("<H", 0x10B))
    record_window("image-base", pe.opt + 28, 4, struct.pack("<I", 0x400000))
    record_window("section-alignment", pe.opt + 32, 8)
    record_window("size-of-image", pe.opt + 56, 4)
    record_window("checksum", pe.opt + 64, 4)
    record_window("certificate-table", pe.opt + 128, 8, bytes(8))

    out = bytearray(data)
    edits = []
    for index, patch in enumerate(patches, 1):
        site = patch["site"]
        expected = patch["expected"]
        require(pe.read(site, len(expected)) == expected,
                "patch {} call-site window does not match".format(index))
        record_window("patch-{}".format(index), pe.offset(site, len(expected)),
                      len(expected), expected)
        replacement = (bytes([patch["op"]]) +
                       struct.pack("<i", patch["target"] - site - 5) +
                       b"\x90" * (len(expected) - 5))
        patch["replacement"] = replacement
        at = pe.offset(site, len(expected))
        out[at:at + len(expected)] = replacement
        edits.append((at, at + len(expected)))

    header = pe.st + pe.count * 40
    require(header + 40 <= u32(data, pe.opt + 60),
            "new section header exceeds SizeOfHeaders")
    require(data[header:header + 40] == bytes(40),
            "new section header would overwrite existing header bytes")
    require(header + 40 <= min(s["raw"] for s in pe.sections if s["raw_size"]),
            "new section header overlaps the first section body")
    record_window("new-section-header", header, 40, bytes(40))

    raw = align(len(out), pe.fa)
    raw_size = align(len(payload), pe.fa)
    out.extend(bytes(raw - len(out)))
    out.extend(payload)
    out.extend(bytes(raw + raw_size - len(out)))
    struct.pack_into("<8sIIIIIIHHI", out, header,
                     b".hudaux", len(payload), VA - pe.base, raw_size, raw,
                     0, 0, 0, 0, 0x60000020)
    struct.pack_into("<H", out, pe.nt + 6, pe.count + 1)
    struct.pack_into("<I", out, pe.opt + 4, u32(data, pe.opt + 4) + raw_size)
    struct.pack_into("<I", out, pe.opt + 56, align(end - pe.base, pe.sa))
    struct.pack_into("<I", out, pe.opt + 64, pe_checksum(out, pe.opt + 64))

    edits += [
        (header, header + 40),
        (pe.nt + 6, pe.nt + 8),
        (pe.opt + 4, pe.opt + 8),
        (pe.opt + 56, pe.opt + 60),
        (pe.opt + 64, pe.opt + 68),
    ]
    edits.sort()
    cursor = 0
    for left, right in edits:
        require(out[cursor:left] == data[cursor:left],
                "original bytes changed before 0x{:x}".format(left))
        cursor = right
    require(out[cursor:len(data)] == data[cursor:],
            "original bytes changed after the final listed edit")

    final = PE(out)
    require(u32(out, pe.opt + 64) == pe_checksum(out, pe.opt + 64),
            "output PE checksum does not verify")
    for patch in patches:
        require(final.read(patch["site"], len(patch["expected"])) ==
                patch["replacement"], "call-site read-back failed")
    for at, code in parts:
        require(final.read(at, len(code)) == code, "helper read-back failed")

    require(len(out) == OUTPUT_SIZE, "fixed output size mismatch")
    output_sha256 = sha256_hex(out)
    require(output_sha256 == OUTPUT_SHA256,
            "candidate SHA-256 mismatch: {}".format(output_sha256))

    receipt = dict(
        baseSha256=BASE_SHA256,
        candidateSha256=output_sha256,
        size=len(out),
        helperAddress=hex(VA),
        helperSize=len(parts[0][1]),
        payloadSize=len(payload),
        helpers=[dict(
            address=hex(at),
            size=len(code),
            sha256=sha256_hex(code),
            code=code.hex(),
        ) for at, code in parts],
        patches=[dict(
            site=hex(patch["site"]),
            target=hex(patch["target"]),
            expected=patch["expected"].hex(),
            replacement=patch["replacement"].hex(),
        ) for patch in patches],
        originalByteWhitelist=[[left, right] for left, right in sorted(edits)],
        experimental=True,
        installed=False,
        limitations=[
            "real fashion-resource loading, GPU and full HUD acceptance not executed",
            "requires the matching cross-scene data provider on the server",
        ],
        inputSha256=BASE_SHA256,
        outputSha256=output_sha256,
        inputSize=len(data),
        outputSize=len(out),
        patchCount=len(patches),
        newSection=final.sections[-1],
        fixedBase=True,
        existingBytesPreservedOutsideListedSitesAndHeaders=True,
        checksumVerified=True,
        windowExpectations=window_expectations,
        builderSha256=sha256_hex(Path(__file__).read_bytes()),
        expectedOutputSha256=OUTPUT_SHA256,
        proofScope=("PE structure, fixed v4 patch read-back, helper read-back and "
                    "original-byte preservation only. Not full-client execution, "
                    "GUI acceptance or deployment."),
        deployed=False,
        guiAccepted=False,
    )
    return bytes(out), receipt


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Build the fixed v4 ordinary-HUD local candidate.")
    parser.add_argument(
        "--input", type=Path, required=True,
        help="exact v159 base EXE (SHA-256 {})".format(BASE_SHA256))
    parser.add_argument(
        "--output-dir", type=Path, required=True,
        help="new or empty directory for the candidate and receipt")
    args = parser.parse_args(argv)

    source = args.input.resolve()
    require(source.is_file(), "input file does not exist")
    output_dir = args.output_dir.resolve()
    target = output_dir / OUTPUT_NAME
    require(target != source, "output must not overwrite the input")
    if output_dir.exists():
        require(output_dir.is_dir(), "output path is not a directory")
        require(not any(output_dir.iterdir()), "output directory must be empty")

    base = source.read_bytes()
    candidate, receipt = build(base)
    output_dir.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(
        prefix="." + output_dir.name + "-", dir=output_dir.parent))
    staged_target = stage / OUTPUT_NAME
    with staged_target.open("xb") as handle:
        handle.write(candidate)
    require(sha256_hex(staged_target.read_bytes()) == OUTPUT_SHA256,
            "candidate disk read-back SHA-256 mismatch")

    receipt["inputName"] = source.name
    receipt["outputName"] = OUTPUT_NAME
    receipt["diskReadbackVerified"] = True
    receipt_text = json.dumps(receipt, indent=2) + "\n"
    staged_receipt = stage / RECEIPT_NAME
    with staged_receipt.open("x", encoding="utf8") as handle:
        handle.write(receipt_text)
    require(json.loads(staged_receipt.read_text(encoding="utf8")) == receipt,
            "receipt disk read-back mismatch")

    if output_dir.exists():
        output_dir.rmdir()
    stage.rename(output_dir)

    print(json.dumps(dict(
        inputSha256=receipt["inputSha256"],
        outputSha256=receipt["outputSha256"],
        outputName=receipt["outputName"],
        patchCount=receipt["patchCount"],
        newSectionVa=hex(receipt["newSection"]["rva"] + PE(base).base),
        deployed=False,
        guiAccepted=False,
    ), indent=2))


if __name__ == "__main__":
    if not __debug__:
        raise SystemExit("optimized Python is unsupported")
    main()
