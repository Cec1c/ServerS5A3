"""Repair the frozen v162 PE section gap without moving or modifying code.

The Windows probe creates SEC_IMAGE_NO_EXECUTE only. It never starts a process,
loads a DLL or invokes an image entrypoint. Input is never overwritten.
"""
import argparse
import array
import ctypes
import hashlib
import json
import os
from pathlib import Path
import struct
import sys

BASE_SHA = "04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad"
BASE_SIZE = 111894528


def sha(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def u32(data, offset):
    return struct.unpack_from("<I", data, offset)[0]


def align(value, alignment):
    require(alignment > 0 and alignment & (alignment - 1) == 0, "invalid alignment")
    return (value + alignment - 1) & ~(alignment - 1)


def parse(data):
    require(len(data) >= 0x40 and data[:2] == b"MZ", "invalid DOS header")
    nt = u32(data, 0x3C)
    require(nt + 24 <= len(data) and data[nt:nt + 4] == b"PE\0\0", "invalid NT header")
    machine, count = struct.unpack_from("<HH", data, nt + 4)
    opt, opt_size = nt + 24, struct.unpack_from("<H", data, nt + 20)[0]
    require(machine == 0x14C and opt_size >= 224 and opt + opt_size <= len(data), "expected PE32 x86")
    require(struct.unpack_from("<H", data, opt)[0] == 0x10B, "expected PE32 optional header")
    sa, fa = struct.unpack_from("<II", data, opt + 32)
    align(0, sa)
    align(0, fa)
    headers, table = u32(data, opt + 60), opt + opt_size
    require(count > 0 and table + count * 40 <= headers <= len(data), "truncated section table")
    sections = []
    for i in range(count):
        at = table + i * 40
        vs, rva, rs, raw = struct.unpack_from("<IIII", data, at + 8)
        require(rva % sa == 0, "unaligned section RVA")
        require(rs % fa == 0 and (not rs or raw % fa == 0), "unaligned raw section")
        require(not rs or headers <= raw and raw + rs <= len(data), "raw section outside file")
        sections.append(dict(name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                             header=at, virtualSize=vs, rva=rva, rawSize=rs,
                             raw=raw, characteristics=u32(data, at + 36)))
    return dict(opt=opt, sectionAlignment=sa, fileAlignment=fa, headers=headers,
                imageSize=u32(data, opt + 56), sections=sections)


def discontinuities(pe):
    expected = align(pe["headers"], pe["sectionAlignment"])
    issues = []
    for section in pe["sections"]:
        if section["rva"] != expected:
            issues.append(dict(section=section["name"], expected=expected, actual=section["rva"]))
        expected = align(section["rva"] + max(section["virtualSize"], section["rawSize"]), pe["sectionAlignment"])
    if expected != pe["imageSize"]:
        issues.append(dict(section="SizeOfImage", expected=expected, actual=pe["imageSize"]))
    return issues


def checksum(data, offset):
    work = bytearray(data)
    struct.pack_into("<I", work, offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H", work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF


def build(data):
    require(len(data) == BASE_SIZE and sha(data) == BASE_SHA, "input must match frozen v162 SHA256")
    pe = parse(data)
    require(len(pe["sections"]) == 44 and pe["sectionAlignment"] == pe["fileAlignment"] == 0x1000, "unexpected layout")
    require(discontinuities(pe) == [dict(section=".xscene", expected=0x6AB3000, actual=0x6AC0000)], "unexpected gap")
    before, after = pe["sections"][40:42]
    require((before["name"], before["virtualSize"], before["rva"], before["rawSize"], before["raw"], before["characteristics"]) ==
            (".imeup", 0x6B, 0x6AB2000, 0x1000, 0x6AB2000, 0x60000020), "unexpected preceding section")
    virtual_offset, checksum_offset = before["header"] + 8, pe["opt"] + 64
    result = bytearray(data)
    struct.pack_into("<I", result, virtual_offset, after["rva"] - before["rva"])
    struct.pack_into("<I", result, checksum_offset, checksum(result, checksum_offset))
    require(not discontinuities(parse(result)), "repaired layout is not contiguous")
    allowed = [(checksum_offset, 4), (virtual_offset, 4)]
    normalized = bytearray(result)
    for offset, size in allowed:
        normalized[offset:offset + size] = data[offset:offset + size]
    require(normalized == data, "change outside two approved header fields")
    preserved = []
    for section in pe["sections"]:
        start, size = section["raw"], section["rawSize"]
        require(data[start:start + size] == result[start:start + size], "section content changed")
        preserved.append(dict(name=section["name"], sha256=sha(data[start:start + size])))
    receipt = dict(inputSha256=BASE_SHA, outputSha256=sha(result), size=len(result),
                   previousVirtualSize=before["virtualSize"], newVirtualSize=0xE000,
                   gapStart=0x6AB3000, gapEnd=0x6AC0000, all44RawSectionsPreserved=True,
                   entrypointAndRvasPreserved=True, sections=preserved,
                   changes=[dict(offset=offset, size=size, before=data[offset:offset + size].hex(),
                                 after=result[offset:offset + size].hex()) for offset, size in allowed])
    return bytes(result), receipt


def probe_image(path):
    require(os.name == "nt", "Windows is required for native image validation")
    from ctypes import wintypes as w
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateFileW.argtypes = [w.LPCWSTR, w.DWORD, w.DWORD, w.LPVOID, w.DWORD, w.DWORD, w.HANDLE]
    kernel.CreateFileW.restype = w.HANDLE
    kernel.CreateFileMappingW.argtypes = [w.HANDLE, w.LPVOID, w.DWORD, w.DWORD, w.DWORD, w.LPCWSTR]
    kernel.CreateFileMappingW.restype = w.HANDLE
    kernel.CloseHandle.argtypes, kernel.CloseHandle.restype = [w.HANDLE], w.BOOL
    handle = kernel.CreateFileW(str(Path(path).resolve()), 0x80000000, 7, None, 3, 0, None)
    if handle == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        # PAGE_READONLY | SEC_IMAGE_NO_EXECUTE: validates image, no entrypoint execution.
        mapping = kernel.CreateFileMappingW(handle, None, 0x11000002, 0, 0, None)
        error = 0 if mapping else ctypes.get_last_error()
        if mapping:
            kernel.CloseHandle(mapping)
        return dict(mappingSucceeded=bool(mapping), win32Error=error, executed=False)
    finally:
        kernel.CloseHandle(handle)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--verify-windows", action="store_true")
    args = parser.parse_args()
    output = args.output_dir.resolve()
    target = output / "DNF.exe"
    require(target != args.input.resolve(), "input and output must differ")
    candidate, receipt = build(args.input.read_bytes())
    output.mkdir(parents=True, exist_ok=True)
    if target.exists():
        require(target.read_bytes() == candidate, "refuse overwrite of different output")
    else:
        with target.open("xb") as stream:
            stream.write(candidate)
    require(sha(args.input.read_bytes()) == BASE_SHA, "input changed during build")
    require(sha(target.read_bytes()) == receipt["outputSha256"], "output readback mismatch")
    if args.verify_windows:
        receipt["windowsBefore"] = probe_image(args.input)
        receipt["windowsAfter"] = probe_image(target)
        require(receipt["windowsBefore"]["win32Error"] == 193, "expected frozen input error 193")
        require(receipt["windowsAfter"]["mappingSucceeded"], "fixed image rejected by Windows")
    (output / "build-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in receipt.items() if k != "sections"}))


if __name__ == "__main__":
    main()
