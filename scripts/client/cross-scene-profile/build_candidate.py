#!/usr/bin/env python3
"""Rebuild the fixed R195 cross-scene client candidate from reviewed PE edits.

Self-contained: Python standard library plus the four manifests stored beside
this file. The input must be the exact fixed base image; the output is written
to a new or empty directory and checked against the reviewed candidate hash.
No game process, network, deployment or release action is performed.
"""
from pathlib import Path
import argparse
import array
import hashlib
import json
import struct
import sys
import tempfile

sys.dont_write_bytecode = True

BASE_SHA256 = "a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6"
INPUT_SIZE = 111882240
OUTPUT_SHA256 = "db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0"
OUTPUT_NAME = "DNF-cross-scene-candidate.exe"
RECEIPT_NAME = "build-receipt.json"
PAYLOAD_VA = 0x6EC0000
EXPECTED_PATCH_COUNT = 8
MANIFEST_PATHS = (
    Path("manifests/menu-candidate/windows.json"),
    Path("manifests/detail-candidate/windows-v1.json"),
    Path("manifests/portrait-candidate/windows-v1.json"),
    Path("manifests/mode8-route-candidate/windows-v1.json"),
)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def patch_label(patch):
    return patch.get("name") or patch["sourceManifest"]


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]


def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]


def align(value, alignment):
    require(alignment and not alignment & (alignment - 1), "alignment must be a power of two")
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
        require(u16(data, self.opt) == 0x10B, "input is not a PE32 optional header")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from("<IIII", data, at + 8)
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


def load_patches(root):
    patches = []
    hashes = {}
    for relative in MANIFEST_PATHS:
        path = root / relative
        raw = path.read_bytes()
        manifest = json.loads(raw.decode("utf-8"))
        require(manifest.get("baseSha256") == BASE_SHA256,
                "{}: baseSha256 does not match the fixed input".format(relative.as_posix()))
        for row in manifest.get("patches", [manifest]):
            patches.append(dict(row, sourceManifest=relative.as_posix()))
        hashes[relative.as_posix()] = sha256_hex(raw)
    require(len(patches) == EXPECTED_PATCH_COUNT,
            "expected {} reviewed patches, found {}".format(EXPECTED_PATCH_COUNT, len(patches)))
    return patches, hashes


def build(base):
    require(len(base) == INPUT_SIZE, "fixed base size mismatch")
    require(sha256_hex(base) == BASE_SHA256, "input SHA-256 does not match the fixed base")
    pe = PE(base)
    require(pe.base == 0x400000, "unexpected image base")
    require(not u16(base, pe.opt + 70) & 0x40,
            "dynamic base needs relocation handling; refusing this input")
    require(u32(base, pe.opt + 128) == 0 and u32(base, pe.opt + 132) == 0,
            "signed input needs a separate certificate-table design")
    require(len(base) == max(s["raw"] + s["raw_size"] for s in pe.sections),
            "input contains an overlay")

    patches, manifest_hashes = load_patches(Path(__file__).resolve().parent)
    start = PAYLOAD_VA
    rva = start - pe.base
    require(rva % pe.sa == 0 and rva >= u32(base, pe.opt + 56),
            "candidate section does not follow the fixed image")
    end = max(int(p["candidateAddress"], 16) + len(bytes.fromhex(p["code"]))
              for p in patches)
    require(end >= start, "candidate payload precedes its section")

    payload = bytearray(end - start)
    ranges = []
    edits = []
    out = bytearray(base)
    for patch in patches:
        at = int(patch["candidateAddress"], 16)
        code = bytes.fromhex(patch["code"])
        require(at >= start, "candidate code falls outside the appended section")
        require(sha256_hex(code) == patch["codeSha256"],
                "{}: candidate code SHA-256 mismatch".format(patch_label(patch)))
        payload[at - start:at - start + len(code)] = code
        ranges.append((at, at + len(code)))

        site = int(patch["site"], 16)
        expected = bytes.fromhex(patch["expected"])
        replacement = bytes.fromhex(patch["replacement"])
        require(len(expected) == len(replacement),
                "{}: call-site replacement length changed".format(patch_label(patch)))
        require(pe.read(site, len(expected)) == expected,
                "{}: call site does not contain the reviewed bytes".format(patch_label(patch)))
        require(len(replacement) >= 5 and replacement[0] in (0xE8, 0xE9),
                "{}: call site is not a near call or jump".format(patch_label(patch)))
        relative_target = site + 5 + struct.unpack_from("<i", replacement, 1)[0]
        require(relative_target == at,
                "{}: replacement does not target its candidate code".format(patch_label(patch)))
        offset = pe.offset(site, len(expected))
        edits.append((offset, offset + len(expected)))
        out[offset:offset + len(expected)] = replacement
        ranges.append((site, site + len(expected)))

    ranges.sort()
    require(all(left[1] <= right[0] for left, right in zip(ranges, ranges[1:])),
            "reviewed edit ranges overlap")

    raw = align(len(out), pe.fa)
    raw_size = align(len(payload), pe.fa)
    header = pe.st + pe.count * 40
    require(header + 40 <= u32(base, pe.opt + 60),
            "new section header exceeds SizeOfHeaders")
    require(base[header:header + 40] == bytes(40),
            "new section header would overwrite existing header bytes")
    require(header + 40 <= min(s["raw"] for s in pe.sections if s["raw_size"]),
            "new section header overlaps the first section body")

    out.extend(bytes(raw - len(out)))
    out.extend(payload)
    out.extend(bytes(raw + raw_size - len(out)))
    struct.pack_into("<8sIIIIIIHHI", out, header,
                     b".xscene", len(payload), rva, raw_size, raw, 0, 0, 0, 0, 0x60000020)
    struct.pack_into("<H", out, pe.nt + 6, pe.count + 1)
    struct.pack_into("<I", out, pe.opt + 4, u32(base, pe.opt + 4) + raw_size)
    struct.pack_into("<I", out, pe.opt + 56, align(rva + len(payload), pe.sa))
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
        require(out[cursor:left] == base[cursor:left],
                "original bytes changed before 0x{:x}".format(left))
        cursor = right
    require(out[cursor:len(base)] == base[cursor:],
            "original bytes changed after the final listed edit")

    final = PE(out)
    require(u32(out, pe.opt + 64) == pe_checksum(out, pe.opt + 64),
            "output PE checksum does not verify")
    for patch in patches:
        require(final.read(int(patch["site"], 16), len(bytes.fromhex(patch["expected"]))) ==
                bytes.fromhex(patch["replacement"]),
                "{}: call-site read-back failed".format(patch_label(patch)))
        require(final.read(int(patch["candidateAddress"], 16), len(bytes.fromhex(patch["code"]))) ==
                bytes.fromhex(patch["code"]),
                "{}: candidate-code read-back failed".format(patch_label(patch)))

    output_sha256 = sha256_hex(out)
    require(output_sha256 == OUTPUT_SHA256,
            "candidate SHA-256 mismatch: {}".format(output_sha256))
    receipt = dict(
        inputSha256=BASE_SHA256,
        outputSha256=output_sha256,
        inputSize=len(base),
        outputSize=len(out),
        patches=patches,
        patchCount=len(patches),
        newSection=final.sections[-1],
        fixedBase=True,
        existingBytesPreservedOutsideListedSitesAndHeaders=True,
        checksumVerified=True,
        manifestHashes=manifest_hashes,
        builderSha256=sha256_hex(Path(__file__).read_bytes()),
        expectedOutputSha256=OUTPUT_SHA256,
        proofScope=("PE structure, patch read-back and byte preservation only. "
                    "Not full-client execution, GUI acceptance or deployment."),
        deployed=False,
        guiAccepted=False,
        pairedServerRequired=True,
    )
    return bytes(out), receipt


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Rebuild the fixed R195 cross-scene client candidate.")
    parser.add_argument("--input", type=Path, required=True,
                        help="exact fixed base EXE (SHA-256 {})".format(BASE_SHA256))
    parser.add_argument("--output-dir", type=Path, required=True,
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
    # Publish the EXE and receipt together. A failed staging directory remains
    # available for diagnosis; this tool never recursively removes output.
    stage = Path(tempfile.mkdtemp(prefix="." + output_dir.name + "-", dir=output_dir.parent))
    staged_target = stage / OUTPUT_NAME
    with staged_target.open("xb") as handle:
        handle.write(candidate)

    require(sha256_hex(staged_target.read_bytes()) == OUTPUT_SHA256,
            "candidate disk read-back SHA-256 mismatch")

    receipt["inputName"] = source.name
    receipt["outputName"] = OUTPUT_NAME
    receipt["diskReadbackVerified"] = True
    with (stage / RECEIPT_NAME).open("x", encoding="utf8") as handle:
        handle.write(json.dumps(receipt, indent=2) + "\n")

    # rmdir only succeeds for an empty directory. If another writer populated
    # it since the initial check, leave both directories and fail explicitly.
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
        pairedServerRequired=True,
    ), indent=2))


if __name__ == "__main__":
    main()
