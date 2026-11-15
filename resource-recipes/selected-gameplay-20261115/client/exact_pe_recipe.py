#!/usr/bin/env python3
"""Exact-input PE32 static recipe v3; never executes the generated image."""
import argparse
import hashlib
import json
import struct
from datetime import datetime, timezone
from pathlib import Path

import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_16, CS_MODE_32

ROOT = Path.cwd()
DEFAULT_INPUT = Path("DNF.exe")
DEFAULT_OUTPUT = ROOT / "client-candidate"
PROFILE = Path(__file__).with_name("profile.json")
INPUT_SHA = "6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4"
INPUT_SIZE = 112812032
STUB_HEX = ("9c6085f60f84210000008b068bceff904403000085c00f840f000000"
            "8d88100400008d55c852e875f44afb619d3bf30f8487b6eafa6a01e96cb6eafa")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def number(value):
    return int(value, 0) if isinstance(value, str) else value


def aligned(value, alignment):
    return (value + alignment - 1) // alignment * alignment


def parse(data):
    return pefile.PE(data=data, fast_load=True)


def offset(pe, va, length):
    rva = va - pe.OPTIONAL_HEADER.ImageBase
    start = pe.get_offset_from_rva(rva)
    cursor = rva
    while cursor < rva + length:
        matches = [s for s in pe.sections if s.VirtualAddress <= cursor
                   < s.VirtualAddress + s.SizeOfRawData]
        require(len(matches) == 1, "VA must map to raw-backed sections")
        section = matches[0]
        require(section.PointerToRawData + cursor - section.VirtualAddress ==
                start + cursor - rva, "RVA span is not raw-contiguous")
        cursor = min(rva + length, section.VirtualAddress + section.SizeOfRawData)
    return start


def disassemble(code, va, mode=CS_MODE_32):
    rows = [{"va": hex(i.address), "bytes": i.bytes.hex(),
             "asm": (i.mnemonic + " " + i.op_str).strip()}
            for i in Cs(CS_ARCH_X86, mode).disasm(code, va)]
    require(sum(len(bytes.fromhex(r["bytes"])) for r in rows) == len(code),
            "Capstone did not consume all bytes")
    return rows


def build_stub(profile, new_va):
    # Exact reviewed v2 body: pushfd/pushad, native getter, vector push_back,
    # restore, displaced cmp/je/push and resume. No absolute-address relocation.
    require(new_va == 0x714C000, "stub VA changed")
    code = bytes.fromhex(STUB_HEX)
    require(code.hex() == profile["stub"]["hex"] and len(code) == 60, "stub pin")
    require(sha(code) == profile["stub"]["sha256"], "stub digest pin")
    require(new_va + 43 + struct.unpack_from("<i", code, 39)[0] == 0x25FB4A0,
            "push_back rel32 target")
    require(new_va + 53 + struct.unpack_from("<i", code, 49)[0] == 0x1FF76BC,
            "equal branch rel32 target")
    require(new_va + 60 + struct.unpack_from("<i", code, 56)[0] == 0x1FF76A8,
            "resume rel32 target")
    return code


def build(raw, profile):
    require(profile["schema"] == "dnf90-blackdiamond-secretshop-candidate-profile/v3", "profile schema")
    require(len(raw) == INPUT_SIZE and sha(raw) == INPUT_SHA, "full input SHA/size")
    require(profile["input"]["sha256"] == INPUT_SHA and profile["input"]["size"] == INPUT_SIZE, "input profile")
    before = parse(raw)
    nt = before.DOS_HEADER.e_lfanew
    opt = nt + 24
    table = opt + before.FILE_HEADER.SizeOfOptionalHeader
    require(nt == 0x80 and raw[nt:nt+4] == b"PE\0\0", "original NT offset/signature")
    require(before.FILE_HEADER.Machine == 0x14C and before.OPTIONAL_HEADER.Magic == 0x10B, "PE32 x86")
    require(before.OPTIONAL_HEADER.ImageBase == 0x400000, "image base")
    require(before.FILE_HEADER.NumberOfSections == len(before.sections) == 93 <= 95, "section count")
    require(table + 93 * 40 == 0x1000, "original NT/table end")
    require(before.OPTIONAL_HEADER.SizeOfHeaders == 0x1000, "original SizeOfHeaders")
    require(before.OPTIONAL_HEADER.FileAlignment == before.OPTIONAL_HEADER.SectionAlignment == 0x1000, "alignments")
    require(before.get_overlay_data_start_offset() is None, "overlay forbidden")
    security = before.OPTIONAL_HEADER.DATA_DIRECTORY[4]
    require(security.VirtualAddress == security.Size == 0, "security forbidden")
    require(max(s.PointerToRawData+s.SizeOfRawData for s in before.sections) == len(raw), "original raw tail")
    require(min(s.VirtualAddress for s in before.sections) == 0x1000, "first section RVA")
    require(min(s.PointerToRawData for s in before.sections if s.SizeOfRawData) == 0x1000, "first raw section")
    require(struct.unpack_from("<HH", raw, 0x14) == (0, 0) and
            struct.unpack_from("<H", raw, 8)[0] == 4, "DOS CS:IP/header paragraphs")

    def fingerprint(row, key):
        want = bytes.fromhex(row[key])
        at = offset(before, number(row["va"]), len(want))
        require(raw[at:at+len(want)] == want, "fingerprint: " + row.get("name", key))
        return at, want

    hook_at, old_hook = fingerprint(profile["hook"], "original_bytes")
    require(number(profile["hook"]["va"]) == 0x1FF76A2 and old_hook.hex() == "3bf374166a01", "hook pin")
    require(number(profile["vector_push_back"]["va"]) == 0x25FB4A0, "vector helper pin")
    fingerprint(profile["vector_push_back"], "prologue_bytes")
    for row in profile["preserved"]:
        fingerprint(row, "fingerprint_bytes")
    bitmap = profile["bitmap"]
    require(number(bitmap["va"]) == 0x6E92300 and bitmap["size"] == 8192, "bitmap pin")
    require(bitmap["set_bits"] == [279, 280, 296], "bitmap bit allowlist")
    bitmap_at = offset(before, number(bitmap["va"]), bitmap["size"])
    require(sha(raw[bitmap_at:bitmap_at+bitmap["size"]]) == bitmap["sha256"], "bitmap fingerprint")

    new_rva = aligned(max(s.VirtualAddress+max(s.Misc_VirtualSize, s.SizeOfRawData)
                          for s in before.sections), 0x1000)
    new_va = before.OPTIONAL_HEADER.ImageBase + new_rva
    require(new_rva == number(profile["new_section"]["rva"]) == 0x6D4C000, "new RVA pin")
    require(before.OPTIONAL_HEADER.SizeOfImage == new_rva, "old image extent")
    code = build_stub(profile, new_va)
    hook = b"\xe9" + struct.pack("<i", new_va-(0x1FF76A2+5)) + b"\x90"
    require(hook.hex() == profile["hook"]["patched_bytes"], "hook replacement pin")
    expected = bytearray(raw)
    expected[hook_at:hook_at+6] = hook
    changed_bitmap = []
    for bit in bitmap["set_bits"]:
        at, mask = bitmap_at + bit // 8, 1 << (bit % 8)
        require(not raw[at] & mask, "allowlisted bitmap bit already enabled")
        expected[at] |= mask
        changed_bitmap.append({"bit": bit, "offset": at, "before": raw[at], "after": expected[at]})
    require(len(set(r["offset"] for r in changed_bitmap)) == 3, "three bitmap bytes")

    out = bytearray(expected)
    out[0x58:0xFD8] = raw[0x80:0x1000]
    out[0x40:0x58] = bytes.fromhex("b8014ccd21") + bytes(19)
    struct.pack_into("<I", out, 0x3C, 0x58)
    new_opt = 0x58 + 24
    struct.pack_into("<H", out, 0x58+6, 94)
    struct.pack_into("<I", out, new_opt+4, before.OPTIONAL_HEADER.SizeOfCode+0x1000)
    struct.pack_into("<I", out, new_opt+56, new_rva+0x1000)
    struct.pack_into("<I", out, new_opt+60, 0x1000)
    struct.pack_into("<I", out, new_opt+64, 0)
    header = struct.pack("<8sIIIIIIHHI", b".npcsync", 0x1000, new_rva,
                         0x1000, len(raw), 0, 0, 0, 0, 0x60000020)
    out[0xFD8:0x1000] = header
    out.extend(code + bytes(0x1000-len(code)))
    require(len(out) == profile["outputs"]["expected_output_size"] == 112816128, "output size")
    parsed = parse(out)
    checksum = parsed.generate_checksum()
    struct.pack_into("<I", out, new_opt+64, checksum)
    after = parse(out)

    require(after.DOS_HEADER.e_lfanew == 0x58 and len(after.sections) == 94, "reparse count/NT")
    require(after.FILE_HEADER.NumberOfSections == 94, "reparse COFF section count")
    end = 0x58+24+after.FILE_HEADER.SizeOfOptionalHeader+94*40
    require(end == after.OPTIONAL_HEADER.SizeOfHeaders == 0x1000, "NT headers end/SizeOfHeaders")
    require(end <= min(s.VirtualAddress for s in after.sections), "headers overlap mapped section")
    require(out[:0x3C] == raw[:0x3C] and out[0x40:0x58] == bytes.fromhex("b8014ccd21")+bytes(19), "DOS preservation")
    require(after.verify_checksum(), "checksum reparse")
    require(after.FILE_HEADER.Machine == 0x14C and after.OPTIONAL_HEADER.Magic == 0x10B, "output PE32")
    require(after.OPTIONAL_HEADER.AddressOfEntryPoint == before.OPTIONAL_HEADER.AddressOfEntryPoint, "entry point")
    require(after.OPTIONAL_HEADER.ImageBase == before.OPTIONAL_HEADER.ImageBase and
            after.OPTIONAL_HEADER.SizeOfImage == new_rva+0x1000, "output image geometry")
    original_directories = [(d.VirtualAddress, d.Size) for d in before.OPTIONAL_HEADER.DATA_DIRECTORY]
    require(original_directories == [(d.VirtualAddress, d.Size) for d in after.OPTIONAL_HEADER.DATA_DIRECTORY], "directory RVA/size changed")
    section_evidence = []
    for old, new in zip(before.sections, after.sections[:93]):
        require(old.__pack__() == new.__pack__(), "original section header changed")
        start, stop = old.PointerToRawData, old.PointerToRawData+old.SizeOfRawData
        require(out[start:stop] == expected[start:stop], "original section bytes changed outside allowlist")
        section_evidence.append({"name": old.Name.rstrip(b"\0").decode("ascii", "replace"),
            "rva": old.VirtualAddress, "raw": start, "raw_size": old.SizeOfRawData,
            "before_sha256": sha(raw[start:stop]), "after_sha256": sha(out[start:stop])})
    require(out[0x1000:len(raw)] == expected[0x1000:], "original raw tail outside exact patches")
    require(out[len(raw):] == code+bytes(0x1000-len(code)), "appended section bytes")
    relocated_nt = bytearray(raw[0x80:0x1000])
    for at, size in ((6, 2), (24+4, 4), (24+56, 4), (24+64, 4)):
        relocated_nt[at:at+size] = out[0x58+at:0x58+at+size]
    require(out[0x58:0xFD8] == relocated_nt, "unexpected relocated NT metadata change")
    require(after.sections[-1].__pack__() == header, "new section header")
    require(after.get_overlay_data_start_offset() is None, "output overlay")
    resources = {}
    for name, index in (("imports", 1), ("resources", 2), ("relocations", 5), ("IAT", 12)):
        d = before.OPTIONAL_HEADER.DATA_DIRECTORY[index]
        require(before.get_data(d.VirtualAddress, d.Size) == after.get_data(d.VirtualAddress, d.Size), name+" bytes/RVA")
        resources[name] = {"rva": d.VirtualAddress, "size": d.Size,
                           "sha256": sha(before.get_data(d.VirtualAddress, d.Size))}
    for row in profile["preserved"] + [profile["vector_push_back"]]:
        key = "fingerprint_bytes" if "fingerprint_bytes" in row else "prologue_bytes"
        want = bytes.fromhex(row[key])
        require(out[offset(after, number(row["va"]), len(want)):][:len(want)] == want, "output preserved helper")
    digest = sha(out)
    require(digest == profile["outputs"]["expected_output_sha256"], "output SHA pin: "+digest)
    receipt = {"schema": "dnf90-blackdiamond-secretshop-candidate-receipt/v3",
        "status": "static-verified", "generated_at": datetime.now(timezone.utc).isoformat(),
        "input": {"sha256": sha(raw), "size": len(raw)},
        "output": {"sha256": digest, "size": len(out), "checksum": checksum},
        "header_move": {"old_nt": 128, "new_nt": 88, "old_end": 4096,
                        "old_table_new_end": 4056, "new_table_end": end, "SizeOfHeaders": 4096},
        "new_section": {"name": ".npcsync", "rva": new_rva, "va": new_va,
                        "raw": len(raw), "size": 4096, "characteristics": "0x60000020"},
        "hook": {"offset": hook_at, "before": old_hook.hex(), "after": hook.hex(),
                 "disassembly": disassemble(hook, 0x1FF76A2)},
        "bitmap_changes": changed_bitmap,
        "stub": {"hex": code.hex(), "sha256": sha(code), "size": len(code),
                 "disassembly": disassemble(code, new_va)},
        "dos_stub_disassembly": disassemble(bytes.fromhex("b8014ccd21"), 0x40, CS_MODE_16),
        "directory_evidence": resources, "original_sections": section_evidence,
        "checks": {"original_section_headers_identical": True, "original_raw_tail_exact_except_hook_and_bitmap": True,
                   "all_directory_rvas_and_sizes_identical": True, "SizeOfHeaders_le_first_RVA": True,
                   "checksum_valid": True, "reparsed_section_count": 94, "nt_headers_end": end},
        "execution": {"exe": False, "UI": False, "server": False, "Go": False}}
    return bytes(out), receipt


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--input", type=Path, default=DEFAULT_INPUT)
    ap.add_argument("--output-dir", type=Path, default=DEFAULT_OUTPUT)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    require(args.input.is_absolute() and args.output_dir.is_absolute(), "absolute paths required")
    require(args.output_dir.resolve() == DEFAULT_OUTPUT.resolve(), "output must be dedicated v3 directory")
    require(not args.output_dir.exists(), "output directory already exists; no overwrite")
    profile_raw = PROFILE.read_bytes()
    profile = json.loads(profile_raw)
    data, receipt = build(args.input.read_bytes(), profile)
    receipt["profile_sha256"] = sha(profile_raw)
    receipt["builder_sha256"] = sha(Path(__file__).read_bytes())
    receipt["input"]["path"] = str(args.input.resolve())
    receipt["output"]["path"] = str(args.output_dir / profile["outputs"]["candidate_name"])
    if not args.dry_run:
        args.output_dir.mkdir(exist_ok=False)
        with (args.output_dir / profile["outputs"]["candidate_name"]).open("xb") as stream:
            stream.write(data)
        with (args.output_dir / "build-receipt.json").open("x", encoding="utf8") as stream:
            json.dump(receipt, stream, indent=2)
            stream.write("\n")
    print(json.dumps({k: receipt[k] for k in ("status", "output", "header_move", "checks")}, indent=2))


if __name__ == "__main__":
    main()
