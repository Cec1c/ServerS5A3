#!/usr/bin/env python3
"""Shared byte, manifest and receipt helpers for the portable verifier draft."""
from pathlib import Path
import array
import hashlib
import json
import struct
import sys


BASE_SHA256 = "a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6"
INPUT_SIZE = 111882240
CANDIDATE_SHA256 = "db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0"
CANDIDATE_SIZE = 111886336
PAYLOAD_VA = 0x6EC0000
EXPECTED_PATCH_COUNT = 8
MANIFEST_PATHS = (
    "manifests/menu-candidate/windows.json",
    "manifests/detail-candidate/windows-v1.json",
    "manifests/portrait-candidate/windows-v1.json",
    "manifests/mode8-route-candidate/windows-v1.json",
)


class VerificationError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise VerificationError(message)


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
    """Minimal PE32 reader matching the reviewed build_candidate logic."""

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
        raise VerificationError(
            "VA has no raw backing: 0x{:x} / 0x{:x}".format(va, size))

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]


def read_windows(image, ranges):
    pe = PE(image)
    return {address: pe.read(address, size)
            for address, size in ranges.items()}


def read_window_pairs(image, pairs):
    pe = PE(image)
    return {address: pe.read(address, size) for address, size in pairs}


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


def patch_label(patch):
    return patch.get("name") or patch["sourceManifest"]


def load_manifest_bundle(root):
    root = Path(root)
    patches = []
    hashes = {}
    for relative in MANIFEST_PATHS:
        path = root / relative
        require(path.is_file(), "missing manifest: {}".format(relative))
        raw = path.read_bytes()
        manifest = json.loads(raw.decode("utf-8"))
        require(manifest.get("baseSha256") == BASE_SHA256,
                "{}: baseSha256 does not match the frozen base".format(relative))
        for row in manifest.get("patches", [manifest]):
            patches.append(dict(row, sourceManifest=relative))
        hashes[relative] = sha256_hex(raw)
    require(len(patches) == EXPECTED_PATCH_COUNT,
            "expected {} reviewed patches, found {}".format(
                EXPECTED_PATCH_COUNT, len(patches)))
    return patches, hashes


def build_expected_candidate(base, root):
    """Apply the reviewed build_candidate PE transformation in memory."""
    require(len(base) == INPUT_SIZE, "fixed base size mismatch")
    require(sha256_hex(base) == BASE_SHA256,
            "input SHA-256 does not match the frozen base")
    pe = PE(base)
    require(pe.base == 0x400000, "unexpected image base")
    require(not u16(base, pe.opt + 70) & 0x40,
            "dynamic base needs relocation handling; refusing this input")
    require(u32(base, pe.opt + 128) == 0 and u32(base, pe.opt + 132) == 0,
            "signed input needs a separate certificate-table design")
    require(len(base) == max(s["raw"] + s["raw_size"] for s in pe.sections),
            "input contains an overlay")

    patches, manifest_hashes = load_manifest_bundle(root)
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
                "{}: call-site replacement length changed".format(
                    patch_label(patch)))
        require(pe.read(site, len(expected)) == expected,
                "{}: call site does not contain the reviewed bytes".format(
                    patch_label(patch)))
        require(len(replacement) >= 5 and replacement[0] in (0xE8, 0xE9),
                "{}: call site is not a near call or jump".format(
                    patch_label(patch)))
        relative_target = site + 5 + struct.unpack_from("<i", replacement, 1)[0]
        require(relative_target == at,
                "{}: replacement does not target its candidate code".format(
                    patch_label(patch)))
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
                     b".xscene", len(payload), rva, raw_size, raw, 0, 0, 0, 0,
                     0x60000020)
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
        expected = bytes.fromhex(patch["expected"])
        replacement = bytes.fromhex(patch["replacement"])
        code = bytes.fromhex(patch["code"])
        require(final.read(int(patch["site"], 16), len(expected)) == replacement,
                "{}: call-site read-back failed".format(patch_label(patch)))
        require(final.read(int(patch["candidateAddress"], 16), len(code)) == code,
                "{}: candidate-code read-back failed".format(patch_label(patch)))

    output_sha256 = sha256_hex(out)
    require(output_sha256 == CANDIDATE_SHA256,
            "reconstructed candidate SHA-256 mismatch: {}".format(output_sha256))
    require(len(out) == CANDIDATE_SIZE,
            "reconstructed candidate size mismatch: {}".format(len(out)))
    receipt = dict(
        inputSha256=BASE_SHA256,
        outputSha256=output_sha256,
        inputSize=len(base),
        outputSize=len(out),
        patchCount=len(patches),
        patches=patches,
        newSection=final.sections[-1],
        manifestHashes=manifest_hashes,
        checksumVerified=True,
        existingBytesPreservedOutsideListedSitesAndHeaders=True,
        proofScope=("PE structure, eight manifest patches, patch read-back and "
                    "byte preservation; no EXE is copied or executed."),
    )
    return bytes(out), receipt


def source_hashes(root, relative_paths):
    root = Path(root)
    result = {}
    for relative in relative_paths:
        path = root / relative
        require(path.is_file(), "missing source: {}".format(relative))
        result[relative] = sha256_hex(path.read_bytes())
    return result


def write_json_exclusive(path, value):
    path = Path(path)
    with path.open("x", encoding="utf8", newline="\n") as handle:
        json.dump(value, handle, indent=2)
        handle.write("\n")


def proof_sources(root, module_names):
    common = ["portable_core.py", "verify_candidate.py"]
    return source_hashes(root, common + list(module_names))
