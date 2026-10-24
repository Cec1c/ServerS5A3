#!/usr/bin/env python3
"""Build the fixed v162 quiet-mode8 profile-popup client candidate.

Standard library only. The builder reads the exact v162 image, redirects the
five-byte site at 0x22c2539 to one helper in a new RX section, and checks every
byte outside the reviewed whitelist. It does not launch the game, run native
code, contact a server, publish a release, or read credentials.

The output SHA-256 is pinned to the reviewed fixed-image candidate.
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

BASE_SHA256 = "04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad"
INPUT_SIZE = 111_894_528
EXPECTED_OUTPUT_SHA256 = "d3d46a344c56f45f4104945b0fb804ccb426b3f9bd124743e8d536d4d093ce59"

OUTPUT_NAME = "DNF-prquiet-experiment.exe"
RECEIPT_NAME = "build-receipt.json"

HOOK_VA = 0x22C2539
HOOK_ORIGINAL = bytes.fromhex("8b750c8bce")
ORDINARY_CONTINUATION_VA = 0x22C253E
QUIET_CONTINUATION_VA = 0x22C25B8
MODE3_RETURN_ADDRESS = 0x22C461E
MODE8_RETURN_ADDRESS = 0x22C8ACD

SECTION_VA = 0x6EC3000
SECTION_NAME = b".prquiet"
SECTION_CHARACTERISTICS = 0x60000020  # executable, readable, initialized data
PAYLOAD_SIZE = 0x1000


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, offset):
    return struct.unpack_from("<H", data, offset)[0]


def u32(data, offset):
    return struct.unpack_from("<I", data, offset)[0]


def align(value, alignment):
    require(alignment > 0 and not alignment & (alignment - 1),
            "alignment must be a positive power of two")
    return (value + alignment - 1) & ~(alignment - 1)


class PE:
    """Bounded PE32 reader with unique VA-to-file mappings."""

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
        require(u16(data, self.opt) == 0x10B,
                "optional header is not PE32")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + optional_size
        require(self.count > 0 and self.st + self.count * 40 <= len(data),
                "section table is truncated")
        self.sections = []
        raw_ranges = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from(
                "<IIII", data, at + 8)
            require(raw + raw_size <= len(data),
                    "section raw range exceeds file")
            if raw_size:
                raw_ranges.append((raw, raw + raw_size))
            self.sections.append(dict(
                index=index,
                name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                virtual_size=virtual_size,
                rva=rva,
                raw_size=raw_size,
                raw=raw,
                characteristics=u32(data, at + 36),
            ))
        raw_ranges.sort()
        require(all(left[1] <= right[0]
                    for left, right in zip(raw_ranges, raw_ranges[1:])),
                "section raw ranges overlap")

    def offset(self, va, size=1):
        require(size > 0, "empty VA window")
        rva = va - self.base
        matches = [s["raw"] + rva - s["rva"] for s in self.sections
                   if s["rva"] <= rva and rva + size <= s["rva"] + s["raw_size"]]
        require(len(matches) == 1,
                "VA has no unique raw mapping: 0x{:x}".format(va))
        return matches[0]

    def read(self, va, size):
        offset = self.offset(va, size)
        return self.data[offset:offset + size]


def pe_checksum(data, checksum_offset):
    require(0 <= checksum_offset <= len(data) - 4,
            "invalid checksum offset")
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


class Asm:
    """Tiny x86 encoder for the reviewed helper only."""

    def __init__(self, base):
        self.base = base
        self.buf = bytearray()
        self.labels = {}
        self.fixups = []

    def raw(self, text):
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name):
        require(name not in self.labels, "duplicate helper label: " + name)
        self.labels[name] = len(self.buf)

    def jne(self, label):
        self.buf.extend(b"\x0f\x85")
        relative = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append((relative, label))

    def jmp(self, target):
        self.buf.append(0xE9)
        relative = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append((relative, target))
        else:
            struct.pack_into("<i", self.buf, relative,
                             target - (self.base + relative + 4))

    def finish(self):
        for relative, label in self.fixups:
            require(label in self.labels, "unresolved helper label: " + label)
            target = self.base + self.labels[label]
            struct.pack_into("<i", self.buf, relative,
                             target - (self.base + relative + 4))
        return bytes(self.buf)


def build_helper():
    """Encode the exact quiet-mode8 predicate after the validated hook."""
    code = Asm(SECTION_VA)
    code.raw("9c50")  # pushfd; push eax

    # First layer: the immediate return address of the shared parse routine.
    code.raw("817d04" + struct.pack("<I", MODE3_RETURN_ADDRESS).hex())
    code.jne("ordinary")

    # Second layer: follow the saved frame pointer once and read its return.
    code.raw("8b4500")
    code.raw("817804" + struct.pack("<I", MODE8_RETURN_ADDRESS).hex())
    code.jne("ordinary")

    # Quiet mode8: parsing has completed; continue at cleanup without replaying
    # the original mov instructions. Restore the caller-visible EAX and flags.
    code.raw("589d")
    code.jmp(QUIET_CONTINUATION_VA)

    code.label("ordinary")
    code.raw("589d")
    code.raw(HOOK_ORIGINAL.hex())
    code.jmp(ORDINARY_CONTINUATION_VA)
    helper = code.finish()
    require(len(helper) == 50, "reviewed helper length changed")

    # Pin the encoded constants and both branch destinations independently of
    # the label fixup implementation. Offsets below are part of the reviewed
    # 50-byte helper layout.
    require(helper[5:9] == struct.pack("<I", MODE3_RETURN_ADDRESS),
            "first return-address immediate changed")
    require(helper[21:25] == struct.pack("<I", MODE8_RETURN_ADDRESS),
            "second return-address immediate changed")
    require(helper[40:45] == HOOK_ORIGINAL,
            "ordinary replay bytes changed")

    def rel32_target(relative_offset):
        return (SECTION_VA + relative_offset + 4
                + struct.unpack_from("<i", helper, relative_offset)[0])

    require(helper[9:11] == b"\x0f\x85", "first conditional branch changed")
    require(helper[25:27] == b"\x0f\x85", "second conditional branch changed")
    require(helper[33] == 0xE9 and helper[45] == 0xE9,
            "helper return jump opcode changed")
    require(rel32_target(11) == SECTION_VA + 38,
            "first conditional branch no longer targets ordinary path")
    require(rel32_target(27) == SECTION_VA + 38,
            "second conditional branch no longer targets ordinary path")
    require(rel32_target(34) == QUIET_CONTINUATION_VA,
            "quiet branch no longer targets cleanup continuation")
    require(rel32_target(46) == ORDINARY_CONTINUATION_VA,
            "ordinary branch no longer targets original continuation")
    return helper


def build(data):
    """Return (candidate_bytes, receipt); performs no file I/O or execution."""
    require(__debug__, "optimized Python is unsupported")
    require(len(data) == INPUT_SIZE, "fixed v162 input size mismatch")
    require(sha256_hex(data) == BASE_SHA256,
            "fixed v162 input SHA-256 mismatch")

    pe = PE(data)
    require(pe.base == 0x400000, "unexpected image base")
    require(pe.sa == 0x1000 and pe.fa == 0x1000,
            "unexpected PE alignment")
    require(pe.count == 44, "fixed v162 must contain 44 sections")
    require(pe.sections[-1]["name"] == ".rrow",
            "fixed v162 last section is not .rrow")
    require(pe.sections[-1]["rva"] == SECTION_VA - pe.base - pe.sa,
            "fixed v162 .rrow placement changed")
    require(pe.sections[-1]["raw"] + pe.sections[-1]["raw_size"] == INPUT_SIZE,
            "fixed v162 .rrow does not end at EOF")
    require(u32(data, pe.opt + 56) == SECTION_VA - pe.base,
            "fixed v162 SizeOfImage does not end at the new section VA")
    require(not u16(data, pe.opt + 70) & 0x40,
            "dynamic base needs relocation handling")
    require(data[pe.opt + 128:pe.opt + 136] == bytes(8),
            "PE certificate directory is not empty")
    require(len(data) == max(s["raw"] + s["raw_size"] for s in pe.sections),
            "input contains an overlay")

    # Map the hook through the fixed input's own section table. Do not guess a
    # file offset for 0x22c2539.
    hook_offset = pe.offset(HOOK_VA, len(HOOK_ORIGINAL))
    require(pe.read(HOOK_VA, len(HOOK_ORIGINAL)) == HOOK_ORIGINAL,
            "original hook bytes changed")

    helper = build_helper()
    require(len(helper) == 50, "reviewed helper length changed")
    payload = bytearray(PAYLOAD_SIZE)
    payload[:len(helper)] = helper
    require(payload[len(helper):] == bytes(PAYLOAD_SIZE - len(helper)),
            "helper tail padding is not zero")

    header = pe.st + pe.count * 40
    require(header + 40 <= u32(data, pe.opt + 60),
            "new section header exceeds SizeOfHeaders")
    require(data[header:header + 40] == bytes(40),
            "new section header area is not empty")
    require(header + 40 <= min(s["raw"] for s in pe.sections if s["raw_size"]),
            "new section header overlaps existing raw data")
    new_rva = SECTION_VA - pe.base
    raw = align(len(data), pe.fa)
    raw_size = align(len(payload), pe.fa)
    require(raw == len(data), "input has unexpected pre-section padding")
    require(raw == 111_894_528, "fixed append offset changed")
    require(raw_size == PAYLOAD_SIZE, "fixed section raw size changed")

    replacement = b"\xE9" + struct.pack(
        "<i", SECTION_VA - (HOOK_VA + 5))
    require(len(replacement) == len(HOOK_ORIGINAL),
            "hook replacement length changed")
    require(HOOK_VA + 5 + struct.unpack_from("<i", replacement, 1)[0]
            == SECTION_VA, "hook does not target .prquiet helper")

    out = bytearray(data)
    windows = []

    def record_window(name, offset, size):
        body = data[offset:offset + size]
        require(len(body) == size, "truncated original window: " + name)
        windows.append(dict(
            name=name,
            fileOffset=offset,
            size=size,
            expected=body.hex(),
            sha256=sha256_hex(body),
        ))

    for name, offset, size in (
        ("NumberOfSections", pe.nt + 6, 2),
        ("SizeOfCode", pe.opt + 4, 4),
        ("SizeOfImage", pe.opt + 56, 4),
        ("CheckSum", pe.opt + 64, 4),
        ("new-section-header", header, 40),
        ("quiet-mode8-hook", hook_offset, len(HOOK_ORIGINAL)),
    ):
        record_window(name, offset, size)

    out[hook_offset:hook_offset + len(HOOK_ORIGINAL)] = replacement
    out.extend(payload)
    struct.pack_into(
        "<8sIIIIIIHHI", out, header,
        SECTION_NAME, len(helper), new_rva, raw_size, raw,
        0, 0, 0, 0, SECTION_CHARACTERISTICS)
    struct.pack_into("<H", out, pe.nt + 6, pe.count + 1)
    struct.pack_into("<I", out, pe.opt + 4,
                     u32(data, pe.opt + 4) + raw_size)
    struct.pack_into("<I", out, pe.opt + 56,
                     align(new_rva + len(helper), pe.sa))
    struct.pack_into("<I", out, pe.opt + 64,
                     pe_checksum(out, pe.opt + 64))

    edits = ([(hook_offset, hook_offset + len(HOOK_ORIGINAL)),
              (header, header + 40),
              (pe.nt + 6, pe.nt + 8),
              (pe.opt + 4, pe.opt + 8),
              (pe.opt + 56, pe.opt + 60),
              (pe.opt + 64, pe.opt + 68)])
    edits.sort()
    cursor = 0
    for left, right in edits:
        require(cursor <= left < right <= len(data),
                "invalid original-byte range")
        require(out[cursor:left] == data[cursor:left],
                "original bytes changed before 0x{:x}".format(left))
        cursor = right
    require(out[cursor:len(data)] == data[cursor:],
            "original bytes changed after the final allowed range")

    final = PE(out)
    require(final.count == pe.count + 1, "output section count mismatch")
    section = final.sections[-1]
    require(section["name"] == ".prquiet", "output section name mismatch")
    require(section["rva"] == new_rva and section["raw"] == raw,
            "output section placement mismatch")
    require(section["virtual_size"] == len(helper)
            and section["raw_size"] == PAYLOAD_SIZE
            and section["characteristics"] == SECTION_CHARACTERISTICS,
            "output RX section layout mismatch")
    require(final.read(SECTION_VA, len(helper)) == helper,
            "helper read-back failed")
    require(final.read(HOOK_VA, 5) == replacement,
            "hook read-back failed")
    require(out[raw + len(helper):raw + PAYLOAD_SIZE]
            == bytes(PAYLOAD_SIZE - len(helper)),
            "section tail padding changed")
    require(u32(out, pe.opt + 64) == pe_checksum(out, pe.opt + 64),
            "output PE checksum does not verify")

    digest = sha256_hex(out)
    if EXPECTED_OUTPUT_SHA256 is not None:
        require(len(EXPECTED_OUTPUT_SHA256) == 64
                and all(ch in "0123456789abcdef"
                        for ch in EXPECTED_OUTPUT_SHA256),
                "pinned output SHA-256 must be 64 lowercase hex characters")
        require(digest == EXPECTED_OUTPUT_SHA256,
                "candidate SHA-256 does not match the pinned value: " + digest)

    receipt = dict(
        schema="luke-profile-popup-prquiet-v1",
        baseSha256=BASE_SHA256,
        inputSha256=BASE_SHA256,
        inputSize=INPUT_SIZE,
        candidateSha256=digest,
        outputSha256=digest,
        outputSize=len(out),
        outputName=OUTPUT_NAME,
        expectedOutputSha256=EXPECTED_OUTPUT_SHA256,
        expectedOutputPinned=(EXPECTED_OUTPUT_SHA256 is not None),
        generationStatus=("pinned-reproduction" if EXPECTED_OUTPUT_SHA256
                          else "first-generation-unpinned"),
        directFromV162=True,
        hook=dict(
            site=hex(HOOK_VA),
            fileOffset=hook_offset,
            original=HOOK_ORIGINAL.hex(),
            replacement=replacement.hex(),
            ordinaryContinuation=hex(ORDINARY_CONTINUATION_VA),
            quietContinuation=hex(QUIET_CONTINUATION_VA),
        ),
        helper=dict(
            address=hex(SECTION_VA),
            size=len(helper),
            code=helper.hex(),
            sha256=sha256_hex(helper),
            firstReturnAddress=hex(MODE3_RETURN_ADDRESS),
            secondReturnAddress=hex(MODE8_RETURN_ADDRESS),
            recognition=("cmp [ebp+4] then mov eax,[ebp]; cmp [eax+4]"),
            preservesFlags=True,
            preservesEax=True,
            ordinaryReplay=HOOK_ORIGINAL.hex(),
            quietPathSkipsReplay=True,
        ),
        newSection=section,
        sectionReservedSize=PAYLOAD_SIZE,
        helperTailPaddingZero=True,
        originalByteWhitelist=[list(pair) for pair in edits],
        appendedByteRange=[raw, raw + PAYLOAD_SIZE],
        windowExpectations=windows,
        checksum=u32(out, pe.opt + 64),
        checksumVerified=True,
        existingBytesPreservedOutsideWhitelist=True,
        serverInspectionRequired=True,
        serverRequirement=(
            "explicitly inspect quiet mode8 followed by normal mode3 before "
            "client release; do not describe the server contract as unchanged"
        ),
        experimental=True,
        deployed=False,
        guiAccepted=False,
        proofScope=(
            "fixed-image PE construction, helper and hook read-back, checksum, "
            "padding and exact byte whitelist only; no native execution"
        ),
        limitations=[
            "helper assumes the reviewed EBP frame chain is valid at the fixed hook",
            "no native or simulator run was performed by this builder",
            "server inspect evidence and GUI acceptance are recorded separately",
        ],
    )
    return bytes(out), receipt


def file_sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            block = handle.read(1024 * 1024)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()


def main(argv=None):
    require(__debug__, "optimized Python is unsupported")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--client-exe", type=Path, required=True,
        help="exact v162 EXE; SHA-256 04d0725f... is required")
    parser.add_argument(
        "--output-dir", type=Path, required=True,
        help="new destination directory under an existing parent")
    args = parser.parse_args(argv)

    source = args.client_exe.resolve(strict=True)
    require(source.is_file(), "client EXE is not a file")
    require(source.stat().st_size == INPUT_SIZE,
            "fixed v162 input size mismatch")
    require(file_sha256(source) == BASE_SHA256,
            "fixed v162 input SHA-256 mismatch")

    require(not os.path.lexists(args.output_dir),
            "output directory already exists")
    target = args.output_dir.resolve(strict=False)
    require(not os.path.lexists(target), "resolved output directory already exists")
    require(target.parent.is_dir(), "output parent directory does not exist")
    require(target != source.parent, "output directory cannot equal input parent")

    data = source.read_bytes()
    candidate, receipt = build(data)
    receipt["builderSha256"] = sha256_hex(Path(__file__).read_bytes())

    # Reserve the destination only after all in-memory checks pass. The
    # exclusive mkdir and xb writes ensure neither an existing directory nor an
    # existing artifact is overwritten. A failed new directory is retained for
    # diagnosis and must not be reused.
    target.mkdir(exist_ok=False)
    candidate_path = target / OUTPUT_NAME
    with candidate_path.open("xb") as handle:
        handle.write(candidate)
        handle.flush()
        os.fsync(handle.fileno())
    disk = candidate_path.read_bytes()
    disk_digest = sha256_hex(disk)
    require(len(disk) == receipt["outputSize"]
            and disk_digest == receipt["candidateSha256"],
            "EXE disk read-back mismatch; partial output retained")

    receipt["diskReadBackVerified"] = True
    receipt_path = target / RECEIPT_NAME
    with receipt_path.open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(receipt, handle, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    require(json.loads(receipt_path.read_text(encoding="utf-8")) == receipt,
            "receipt disk read-back mismatch; partial output retained")

    result = dict(
        status=("passed" if receipt["expectedOutputPinned"]
                else "unpinned-output-sha"),
        candidateSha256=receipt["candidateSha256"],
        outputSize=receipt["outputSize"],
        helperSize=receipt["helper"]["size"],
        outputDir=str(target),
        expectedOutputPinned=receipt["expectedOutputPinned"],
        experimental=True,
        deployed=False,
        guiAccepted=False,
        serverInspectionRequired=True,
    )
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, struct.error) as exc:
        print(json.dumps(dict(status="blocked", error=str(exc))),
              file=sys.stderr)
        raise SystemExit(2)
