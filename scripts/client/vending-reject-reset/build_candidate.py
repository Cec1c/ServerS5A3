#!/usr/bin/env python3
"""Build a profile-driven CMD215 failure-reset candidate.

This tool is intentionally independent of any local client checkout.  A caller
provides an exact input digest and a local JSON profile describing the verified
client layout.  It performs no network, process, integrity-wrapping, or
release-packaging work.

The default invocation computes and prints a complete plan, including the
deterministic output SHA256.  Writing is enabled only with --write and requires
the profile to pin expected_output_sha256 and expected_output_size.
"""

from __future__ import annotations

import argparse
import array
import hashlib
import json
import struct
import sys
from pathlib import Path
from typing import Any


PROFILE_SCHEMA = "dnf90-vending-reject-reset-profile/v1"
MAX_SECTION_SIZE = 0x100000


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def profile_digest(profile: dict) -> str:
    canonical = json.dumps(profile, sort_keys=True, separators=(",", ":"), default=lambda value: value.hex())
    return sha256(canonical.encode("utf8"))


def integer(value: Any, name: str) -> int:
    if isinstance(value, bool):
        raise ValueError(name + " must be an integer")
    if isinstance(value, int):
        result = value
    elif isinstance(value, str):
        try:
            result = int(value, 0)
        except ValueError as error:
            raise ValueError(name + " is not an integer") from error
    else:
        raise ValueError(name + " must be an integer")
    if result < 0:
        raise ValueError(name + " must be non-negative")
    return result


def byte_string(value: Any, name: str) -> bytes:
    if not isinstance(value, str) or len(value) % 2:
        raise ValueError(name + " must be an even-length hexadecimal string")
    try:
        return bytes.fromhex(value)
    except ValueError as error:
        raise ValueError(name + " is not hexadecimal") from error


def digest(value: Any, name: str) -> str:
    if not isinstance(value, str) or len(value) != 64:
        raise ValueError(name + " must be a 64-character SHA256 string")
    try:
        bytes.fromhex(value)
    except ValueError as error:
        raise ValueError(name + " is not hexadecimal") from error
    return value.lower()


def strict_object(value: Any, name: str, required: set[str], optional: set[str] = frozenset()) -> dict:
    if not isinstance(value, dict):
        raise ValueError(name + " must be an object")
    missing = required - set(value)
    extra = set(value) - required - optional
    if missing:
        raise ValueError(name + " missing fields: " + ", ".join(sorted(missing)))
    if extra:
        raise ValueError(name + " unknown fields: " + ", ".join(sorted(extra)))
    return value


def load_profile(path: Path) -> dict:
    try:
        value = json.loads(Path(path).read_text("utf8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read profile {path}: {error}") from error
    root = strict_object(
        value,
        "profile",
        {
            "schema",
            "input_sha256",
            "input_size",
            "image_base",
            "failure_branch",
            "controller",
            "reset_call",
            "new_section",
        },
        {"expected_output_sha256", "expected_output_size"},
    )
    if root["schema"] != PROFILE_SCHEMA:
        raise ValueError("unsupported profile schema")
    root["input_sha256"] = digest(root["input_sha256"], "input_sha256")
    root["input_size"] = integer(root["input_size"], "input_size")
    root["image_base"] = integer(root["image_base"], "image_base")
    if root.get("expected_output_sha256") is not None:
        root["expected_output_sha256"] = digest(root["expected_output_sha256"], "expected_output_sha256")
    if root.get("expected_output_size") is not None:
        root["expected_output_size"] = integer(root["expected_output_size"], "expected_output_size")

    branch = strict_object(root["failure_branch"], "failure_branch", {"site_va", "original_bytes", "resume_va"})
    for key in ("site_va", "resume_va"):
        branch[key] = integer(branch[key], "failure_branch." + key)
    branch["original_bytes"] = byte_string(branch["original_bytes"], "failure_branch.original_bytes")
    if len(branch["original_bytes"]) < 5 or len(branch["original_bytes"]) > 64:
        raise ValueError("failure_branch.original_bytes must contain 5..64 bytes")
    if branch["site_va"] + len(branch["original_bytes"]) != branch["resume_va"]:
        raise ValueError("failure_branch.resume_va must immediately follow the original bytes")

    controller = strict_object(root["controller"], "controller", {"manager_global_va", "slot_offset", "getter_load", "getter_store"})
    controller["manager_global_va"] = integer(controller["manager_global_va"], "controller.manager_global_va")
    controller["slot_offset"] = integer(controller["slot_offset"], "controller.slot_offset")
    if controller["slot_offset"] > 0xFFFFFFFF:
        raise ValueError("controller.slot_offset exceeds 32-bit disp32")
    for key in ("getter_load", "getter_store"):
        check = strict_object(controller[key], "controller." + key, {"va", "bytes"})
        check["va"] = integer(check["va"], "controller." + key + ".va")
        check["bytes"] = byte_string(check["bytes"], "controller." + key + ".bytes")
        if not check["bytes"]:
            raise ValueError("controller fingerprint bytes cannot be empty")

    reset = strict_object(root["reset_call"], "reset_call", {"function_va", "argument", "prologue", "return", "callers"})
    reset["function_va"] = integer(reset["function_va"], "reset_call.function_va")
    reset["argument"] = integer(reset["argument"], "reset_call.argument")
    for key in ("prologue", "return"):
        check = strict_object(reset[key], "reset_call." + key, {"va", "bytes"})
        check["va"] = integer(check["va"], "reset_call." + key + ".va")
        check["bytes"] = byte_string(check["bytes"], "reset_call." + key + ".bytes")
        if not check["bytes"]:
            raise ValueError("reset_call fingerprint bytes cannot be empty")
    if len(reset["return"]["bytes"]) < 3 or reset["return"]["bytes"][0] != 0xC2 or struct.unpack_from("<H", reset["return"]["bytes"], 1)[0] != 4:
        raise ValueError("reset_call must be thiscall with one stack argument cleaned by ret 4")
    if not isinstance(reset["callers"], list):
        raise ValueError("reset_call.callers must be a list")
    if not reset["callers"]:
        raise ValueError("reset_call.callers must contain at least one reviewed caller")
    for index, check in enumerate(reset["callers"]):
        check = strict_object(check, f"reset_call.callers[{index}]", {"va", "bytes"})
        check["va"] = integer(check["va"], f"reset_call.callers[{index}].va")
        check["bytes"] = byte_string(check["bytes"], f"reset_call.callers[{index}].bytes")
        if not check["bytes"]:
            raise ValueError("reset caller fingerprint bytes cannot be empty")
        reset["callers"][index] = check

    section = strict_object(root["new_section"], "new_section", {"name", "virtual_size", "characteristics"})
    if not isinstance(section["name"], str):
        raise ValueError("new_section.name must be a string")
    try:
        encoded_name = section["name"].encode("ascii")
    except UnicodeEncodeError as error:
        raise ValueError("new_section.name must be ASCII") from error
    if not encoded_name or len(encoded_name) > 8:
        raise ValueError("new_section.name must contain 1..8 ASCII bytes")
    section["name_bytes"] = encoded_name
    section["virtual_size"] = integer(section["virtual_size"], "new_section.virtual_size")
    section["characteristics"] = integer(section["characteristics"], "new_section.characteristics")
    if section["virtual_size"] <= 0 or section["virtual_size"] > MAX_SECTION_SIZE:
        raise ValueError("new_section.virtual_size is outside the reviewed bound")
    if section["characteristics"] & 0x60000000 != 0x60000000:
        raise ValueError("new_section must be readable and executable")
    if section["characteristics"] & 0x80000000:
        raise ValueError("new_section must not be writable")
    return root


def u16(data: bytes, offset: int) -> int:
    return struct.unpack_from("<H", data, offset)[0]


def u32(data: bytes, offset: int) -> int:
    return struct.unpack_from("<I", data, offset)[0]


def align(value: int, alignment: int) -> int:
    if alignment <= 0 or alignment & (alignment - 1):
        raise ValueError("alignment must be a positive power of two")
    return (value + alignment - 1) & -alignment


def rel32(source_after: int, target: int) -> bytes:
    delta = target - source_after
    if not -(1 << 31) <= delta < (1 << 31):
        raise ValueError("relative branch is out of range")
    return struct.pack("<i", delta)


def pe32_layout(data: bytes, image_base: int) -> dict:
    if len(data) < 0x40 or data[:2] != b"MZ":
        raise ValueError("input is not an MZ image")
    nt = u32(data, 0x3C)
    if nt + 24 > len(data) or data[nt:nt + 4] != b"PE\0\0":
        raise ValueError("input is not a PE image")
    machine, count = struct.unpack_from("<HH", data, nt + 4)
    optional_size = u16(data, nt + 20)
    optional = nt + 24
    if machine != 0x14C or optional_size < 224 or u16(data, optional) != 0x10B:
        raise ValueError("expected PE32 i386 image")
    if u32(data, optional + 28) != image_base:
        raise ValueError("PE image base differs from profile")
    size_of_headers = u32(data, optional + 60)
    table = optional + optional_size
    table_end = table + count * 40
    if table_end > min(size_of_headers, len(data)):
        raise ValueError("invalid PE section table")
    sections = []
    for index in range(count):
        at = table + index * 40
        virtual_size, rva, raw_size, raw = struct.unpack_from("<IIII", data, at + 8)
        characteristics = u32(data, at + 36)
        if raw_size and raw + raw_size > len(data):
            raise ValueError(f"section {index} exceeds input")
        sections.append({
            "index": index,
            "name_bytes": data[at:at + 8],
            "virtual_size": virtual_size,
            "rva": rva,
            "raw_size": raw_size,
            "raw_offset": raw,
            "characteristics": characteristics,
        })
    if not sections:
        raise ValueError("PE has no sections")
    return {
        "nt": nt,
        "optional": optional,
        "image_base": image_base,
        "section_count": count,
        "section_table": table,
        "section_table_end": table_end,
        "section_alignment": u32(data, optional + 32),
        "file_alignment": u32(data, optional + 36),
        "size_of_headers": size_of_headers,
        "size_of_image": u32(data, optional + 56),
        "checksum_offset": optional + 64,
        "sections": sections,
    }


def section_for_va(layout: dict, va: int) -> dict:
    rva = va - layout["image_base"]
    for section in layout["sections"]:
        if section["rva"] <= rva < section["rva"] + section["virtual_size"]:
            return section
    raise ValueError(f"VA {va:#x} is not mapped")


def file_offset(layout: dict, va: int, length: int) -> int:
    section = section_for_va(layout, va)
    relative = va - layout["image_base"] - section["rva"]
    if relative + length > section["raw_size"]:
        raise ValueError(f"VA {va:#x} crosses uninitialized section bytes")
    return section["raw_offset"] + relative


def image_checksum(data: bytes, checksum_offset: int) -> int:
    work = bytearray(data)
    struct.pack_into("<I", work, checksum_offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H")
    words.frombytes(work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF


def helper_code(profile: dict, helper_va: int) -> bytes:
    controller = profile["controller"]
    reset = profile["reset_call"]
    code = bytearray(bytes.fromhex("9c50515253565755"))
    code += b"\xA1" + struct.pack("<I", controller["manager_global_va"])
    code += bytes.fromhex("85c0")
    skip_manager = len(code)
    code += b"\x74\x00"
    code += bytes.fromhex("8b88") + struct.pack("<I", controller["slot_offset"])
    code += bytes.fromhex("85c9")
    skip_controller = len(code)
    code += b"\x74\x00"
    if reset["argument"] <= 0x7F:
        code += bytes([0x6A, reset["argument"]])
    else:
        code += b"\x68" + struct.pack("<I", reset["argument"])
    call_at = len(code)
    code += b"\xE8\x00\x00\x00\x00"
    restore = len(code)
    struct.pack_into("<i", code, call_at + 1, reset["function_va"] - (helper_va + call_at + 5))
    struct.pack_into("<b", code, skip_manager + 1, restore - skip_manager - 2)
    struct.pack_into("<b", code, skip_controller + 1, restore - skip_controller - 2)
    code += bytes.fromhex("5d5f5e5b5a59589d")
    code += profile["failure_branch"]["original_bytes"]
    jump_at = len(code)
    code += b"\xE9\x00\x00\x00\x00"
    struct.pack_into("<i", code, jump_at + 1, profile["failure_branch"]["resume_va"] - (helper_va + jump_at + 5))
    return bytes(code)


def build_plan(data: bytes, profile: dict) -> dict:
    if len(data) != profile["input_size"] or sha256(data) != profile["input_sha256"]:
        raise ValueError("input does not match the profile's exact SHA256 and size")
    layout = pe32_layout(data, profile["image_base"])

    code_vas = [
        profile["failure_branch"]["site_va"],
        profile["failure_branch"]["resume_va"],
        profile["controller"]["getter_load"]["va"],
        profile["controller"]["getter_store"]["va"],
        profile["reset_call"]["function_va"],
        profile["reset_call"]["prologue"]["va"],
        profile["reset_call"]["return"]["va"],
        *(row["va"] for row in profile["reset_call"]["callers"]),
    ]
    for va in code_vas:
        if section_for_va(layout, va)["characteristics"] & 0x20000000 == 0:
            raise ValueError(f"VA {va:#x} is not executable")
    section_for_va(layout, profile["controller"]["manager_global_va"])

    def check_bytes(va: int, expected: bytes, label: str) -> int:
        offset = file_offset(layout, va, len(expected))
        if data[offset:offset + len(expected)] != expected:
            raise ValueError(label + " fingerprint mismatch")
        return offset

    branch = profile["failure_branch"]
    patch_offset = check_bytes(branch["site_va"], branch["original_bytes"], "failure branch")
    controller = profile["controller"]
    check_bytes(controller["getter_load"]["va"], controller["getter_load"]["bytes"], "controller getter load")
    check_bytes(controller["getter_store"]["va"], controller["getter_store"]["bytes"], "controller getter store")
    reset = profile["reset_call"]
    check_bytes(reset["prologue"]["va"], reset["prologue"]["bytes"], "reset prologue")
    check_bytes(reset["return"]["va"], reset["return"]["bytes"], "reset return")
    for index, caller in enumerate(reset["callers"]):
        check_bytes(caller["va"], caller["bytes"], f"reset caller {index}")

    target = profile["new_section"]
    if any(row["name_bytes"].rstrip(b"\0") == target["name_bytes"] for row in layout["sections"]):
        raise ValueError("new section name already exists")
    last = max(layout["sections"], key=lambda row: row["rva"])
    last_end_rva = last["rva"] + max(last["virtual_size"], last["raw_size"])
    new_rva = align(last_end_rva, layout["section_alignment"])
    new_raw_offset = align(len(data), layout["file_alignment"])
    new_raw_size = align(target["virtual_size"], layout["file_alignment"])
    new_va = profile["image_base"] + new_rva
    code = helper_code(profile, new_va)
    if len(code) > target["virtual_size"] or len(code) > new_raw_size:
        raise ValueError("helper exceeds the configured section size")
    new_header_offset = layout["section_table_end"]
    if new_header_offset + 40 > min(layout["size_of_headers"], len(data)):
        raise ValueError("no section-table header slot is available")
    if data[new_header_offset:new_header_offset + 40] != b"\0" * 40:
        raise ValueError("section-table header slot is not clear")
    if any(new_rva < row["rva"] + max(row["virtual_size"], row["raw_size"]) and new_rva + target["virtual_size"] > row["rva"] for row in layout["sections"]):
        raise ValueError("new section overlaps an existing virtual section")
    if any(new_raw_offset < row["raw_offset"] + row["raw_size"] and new_raw_offset + new_raw_size > row["raw_offset"] for row in layout["sections"]):
        raise ValueError("new section overlaps existing file data")

    hook = b"\xE9" + rel32(branch["site_va"] + 5, new_va) + b"\x90" * (len(branch["original_bytes"]) - 5)
    header = struct.pack(
        "<8sIIIIIIHHI",
        target["name_bytes"].ljust(8, b"\0"),
        target["virtual_size"],
        new_rva,
        new_raw_size,
        new_raw_offset,
        0,
        0,
        0,
        0,
        target["characteristics"],
    )
    size_of_image = align(new_rva + target["virtual_size"], layout["section_alignment"])
    changed = [
        {"offset": layout["nt"] + 6, "length": 2, "purpose": "NumberOfSections"},
        {"offset": layout["optional"] + 56, "length": 4, "purpose": "SizeOfImage"},
        {"offset": layout["checksum_offset"], "length": 4, "purpose": "PE checksum"},
        {"offset": new_header_offset, "length": 40, "purpose": "new section header"},
        {"offset": patch_offset, "length": len(hook), "purpose": "failure branch trampoline"},
    ]
    return {
        "layout": layout,
        "patchOffset": patch_offset,
        "hook": hook,
        "newRva": new_rva,
        "newRawOffset": new_raw_offset,
        "newRawSize": new_raw_size,
        "newHeaderOffset": new_header_offset,
        "newHeader": header,
        "helper": code,
        "sizeOfImage": size_of_image,
        "changedOriginalRanges": changed,
    }


def build(data: bytes, profile: dict) -> tuple[bytes, dict]:
    plan = build_plan(data, profile)
    out = bytearray(data)
    if len(out) < plan["newRawOffset"]:
        out.extend(b"\0" * (plan["newRawOffset"] - len(out)))
    out.extend(b"\0" * plan["newRawSize"])
    struct.pack_into("<H", out, plan["layout"]["nt"] + 6, plan["layout"]["section_count"] + 1)
    struct.pack_into("<I", out, plan["layout"]["optional"] + 56, plan["sizeOfImage"])
    out[plan["newHeaderOffset"]:plan["newHeaderOffset"] + 40] = plan["newHeader"]
    out[plan["newRawOffset"]:plan["newRawOffset"] + len(plan["helper"])] = plan["helper"]
    out[plan["patchOffset"]:plan["patchOffset"] + len(plan["hook"])] = plan["hook"]
    struct.pack_into("<I", out, plan["layout"]["checksum_offset"], image_checksum(out, plan["layout"]["checksum_offset"]))
    result = bytes(out)
    cursor = 0
    for row in sorted(plan["changedOriginalRanges"], key=lambda item: item["offset"]):
        low, high = row["offset"], row["offset"] + row["length"]
        if data[cursor:low] != result[cursor:low]:
            raise ValueError("change outside declared original ranges")
        cursor = high
    if data[cursor:] != result[cursor:len(data)]:
        raise ValueError("change outside declared original ranges")
    if any(result[plan["newRawOffset"] + len(plan["helper"]):]):
        raise ValueError("non-zero appended section tail")
    receipt = {
        "schema": PROFILE_SCHEMA,
        "profileSha256": profile_digest(profile),
        "input": {"sha256": profile["input_sha256"], "size": profile["input_size"]},
        "output": {"sha256": sha256(result), "size": len(result)},
        "failureBranch": {
            "fileOffset": f"0x{plan['patchOffset']:X}",
            "originalBytes": profile["failure_branch"]["original_bytes"].hex(),
            "replacementBytes": plan["hook"].hex(),
            "resumeVa": f"0x{profile['failure_branch']['resume_va']:X}",
        },
        "newSection": {
            "name": profile["new_section"]["name"],
            "rva": f"0x{plan['newRva']:X}",
            "virtualAddress": f"0x{profile['image_base'] + plan['newRva']:X}",
            "rawOffset": f"0x{plan['newRawOffset']:X}",
            "rawSize": plan["newRawSize"],
            "virtualSize": profile["new_section"]["virtual_size"],
            "characteristics": f"0x{profile['new_section']['characteristics']:X}",
            "helperBytes": plan["helper"].hex(),
        },
        "changedOriginalRanges": [
            {"offset": f"0x{row['offset']:X}", "length": row["length"], "purpose": row["purpose"]}
            for row in plan["changedOriginalRanges"]
        ],
        "appendedRange": {"offset": f"0x{plan['newRawOffset']:X}", "length": plan["newRawSize"]},
        "inputPathRecorded": False,
        "outputPathRecorded": False,
    }
    return result, receipt


def write_outputs(input_path: Path, profile_path: Path, output_path: Path, receipt_path: Path) -> dict:
    if len({input_path.resolve(), profile_path.resolve(), output_path.resolve(), receipt_path.resolve()}) != 4:
        raise ValueError("input, profile, output and receipt must be distinct")
    if output_path.exists() or receipt_path.exists():
        raise ValueError("output and receipt paths must not already exist")
    profile = load_profile(profile_path)
    if profile.get("expected_output_sha256") is None or profile.get("expected_output_size") is None:
        raise ValueError("profile must pin expected_output_sha256 and expected_output_size before --write")
    result, receipt = build(input_path.read_bytes(), profile)
    if receipt["output"]["sha256"] != profile["expected_output_sha256"] or receipt["output"]["size"] != profile["expected_output_size"]:
        raise ValueError("deterministic output differs from the profile's expected digest")
    output_path.parent.mkdir(parents=True, exist_ok=True)
    receipt_path.parent.mkdir(parents=True, exist_ok=True)
    with output_path.open("xb") as stream:
        stream.write(result)
    with receipt_path.open("x", encoding="utf8") as stream:
        stream.write(json.dumps(receipt, indent=2) + "\n")
    return receipt


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    profile = load_profile(args.profile)
    if not args.write:
        if args.output is not None or args.receipt is not None:
            raise SystemExit("--output/--receipt require --write")
        result, receipt = build(args.input.read_bytes(), profile)
        print(json.dumps(receipt, indent=2))
        return
    if args.output is None or args.receipt is None:
        raise SystemExit("--write requires --output and --receipt")
    receipt = write_outputs(args.input, args.profile, args.output, args.receipt)
    print(json.dumps({
        "outputSha256": receipt["output"]["sha256"],
        "outputSize": receipt["output"]["size"],
        "receipt": str(args.receipt),
    }, indent=2))


if __name__ == "__main__":
    main()
