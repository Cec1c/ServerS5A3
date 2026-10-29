"""Build the narrowly scoped title recursive-marker candidate."""

import argparse
import hashlib
import json
import struct
from pathlib import Path

from pe_support import INPUT_SHA256, INPUT_SIZE, Image, available_memory, pe_checksum, sha256
from recursive_extension import (
    APPEND_LEAF,
    CONTINUE,
    INNER_ARG4,
    INNER_ARG5,
    INNER_RETURN,
    LEGACY_EXTENSION_SHA256,
    LEGACY_EXTENSION_SIZE,
    LEGACY_EXTENSION_VA,
    LEGACY_VIRTUAL_SIZE,
    OP19UI,
    OUTER_ARG4,
    OUTER_ARG5,
    OUTER_CALL,
    OUTER_COUNT,
    OUTER_FUNCTION,
    OUTER_PARENT_CALL,
    OUTER_PARENT_RETURN,
    OUTER_RETURN,
    PRODUCER,
    PRODUCER_BOUNDARY,
    RECURSIVE_CALL,
    TARGET_SLOT,
    WRITER_GLOBAL,
    WRITER_GETTER,
    WRITER_U8,
    boundary_redirect,
    recursive_extension,
)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def relative_call_target(image, site):
    data = image.bytes_at(site, 5)
    require(data[0] == 0xE8, f"{site:#x} is not a CALL")
    return site + 5 + struct.unpack_from("<i", data, 1)[0]


def relative_jump_target(image, site):
    data = image.bytes_at(site, 5)
    require(data[0] == 0xE9, f"{site:#x} is not a near JMP")
    return site + 5 + struct.unpack_from("<i", data, 1)[0]


def validate_source(image):
    require(len(image.data) == INPUT_SIZE and image.sha256 == INPUT_SHA256,
            "exact mail/fate-v3 raw SHA256 and size required")
    require(OUTER_CALL + 5 == OUTER_RETURN, "outer CALL/return constants differ")
    require(OUTER_PARENT_CALL + 5 == OUTER_PARENT_RETURN,
            "parent CALL/return constants differ")
    require(RECURSIVE_CALL + 5 == INNER_RETURN,
            "recursive CALL/return constants differ")

    section = image.section(".op19ui")
    require(section["va"] == OP19UI, "unexpected .op19ui VA")
    require(section["virtual_size"] == LEGACY_VIRTUAL_SIZE,
            "unexpected legacy V179 .op19ui virtual size")
    require(section["raw_size"] == 0x1000, "unexpected .op19ui raw size")
    require(section["characteristics"] == 0x60000020,
            "unexpected .op19ui characteristics")

    legacy = image.bytes_at(LEGACY_EXTENSION_VA, LEGACY_EXTENSION_SIZE)
    require(hashlib.sha256(legacy).hexdigest() == LEGACY_EXTENSION_SHA256,
            "legacy 17-case title extension differs")
    require(relative_jump_target(image, PRODUCER_BOUNDARY) == OP19UI,
            "producer boundary does not target the legacy extension")
    require(relative_jump_target(image, 0x6E95050) == LEGACY_EXTENSION_VA,
            "legacy fallback does not target the legacy extension")
    require(image.bytes_at(OP19UI + LEGACY_VIRTUAL_SIZE,
                           section["raw_size"] - LEGACY_VIRTUAL_SIZE) ==
            bytes(section["raw_size"] - LEGACY_VIRTUAL_SIZE),
            ".op19ui tail is not empty")

    require(relative_call_target(image, OUTER_CALL) == PRODUCER,
            "outer title CALL does not target the producer")
    require(relative_call_target(image, OUTER_PARENT_CALL) == OUTER_FUNCTION,
            "authenticated parent call does not target the UI action function")
    require(relative_call_target(image, RECURSIVE_CALL) == PRODUCER,
            "recursive CALL does not target the producer")

    require(image.bytes_at(OUTER_CALL - 9, 9) == bytes.fromhex("6a006a006a016a0d52"),
            "outer call no longer has arg4=0,arg5=0,count=1,dst=13,source=edx")
    require(image.bytes_at(RECURSIVE_CALL - 14, 14) == bytes.fromhex("8b45106a016a0340506a0d568bcb"),
            "recursive call branch no longer has arg4=3,arg5=1 and count+1")
    require(image.bytes_at(0x25FA28B, 6) == bytes.fromhex("8b45088945e8"),
            "producer no longer saves the outer source argument")
    require(image.bytes_at(0x25FA362, 6) == bytes.fromhex("8b55e883faff"),
            "producer no longer tests the resolved source for -1")
    require(image.bytes_at(0x25FA3B2, 5) == bytes.fromhex("83fe020f84"),
            "producer arg4=2 no-recursion guard differs")

    require(image.bytes_at(WRITER_GETTER, 6) == b"\xa1" + struct.pack("<I", WRITER_GLOBAL) + b"\xc3",
            "marker getter/global differs")
    require(image.bytes_at(WRITER_U8, 5) == bytes.fromhex("558bec8079"),
            "marker writer prologue differs")
    require(any(WRITER_U8 <= row["site"] < WRITER_U8 + 0x80
                for row in image.direct_callers(APPEND_LEAF)),
            "marker writer no longer reaches the append leaf")
    require(image.bytes_at(APPEND_LEAF, 5)[0] == 0xE9,
            "append leaf is not a jmp thunk")
    return section


def build(data):
    available = available_memory()
    image = Image.from_bytes(data)
    section = validate_source(image)

    extension_va = OP19UI + LEGACY_VIRTUAL_SIZE
    extension = recursive_extension(extension_va)
    new_virtual_size = LEGACY_VIRTUAL_SIZE + len(extension)
    require(new_virtual_size <= section["raw_size"],
            "recursive extension exceeds .op19ui raw section")

    out = bytearray(data)
    boundary_offset = image.file_offset(PRODUCER_BOUNDARY, 5)
    out[boundary_offset:boundary_offset + 5] = boundary_redirect(extension_va)
    extension_offset = image.file_offset(extension_va, len(extension))
    out[extension_offset:extension_offset + len(extension)] = extension
    struct.pack_into("<I", out, section["header"] + 8, new_virtual_size)
    struct.pack_into("<I", out, image.optional + 64,
                     pe_checksum(out, image.optional + 64))
    result = bytes(out)

    allowed = sorted([
        (section["header"] + 8, section["header"] + 12),
        (image.optional + 64, image.optional + 68),
        (boundary_offset, boundary_offset + 5),
        (extension_offset, extension_offset + len(extension)),
    ])
    cursor = 0
    for low, high in allowed:
        require(data[cursor:low] == result[cursor:low],
                f"change outside allowed range before {low:#x}")
        cursor = high
    require(data[cursor:] == result[cursor:],
            "change outside allowed ranges after extension")

    return result, {
        "schema": "title-recursive-build.v1",
        "inputSha256": INPUT_SHA256,
        "inputSize": INPUT_SIZE,
        "outputSha256": sha256(result),
        "outputSize": len(result),
        "op19uiVa": hex(OP19UI),
        "legacyVirtualSize": hex(LEGACY_VIRTUAL_SIZE),
        "newVirtualSize": hex(new_virtual_size),
        "legacyExtension": {
            "va": hex(LEGACY_EXTENSION_VA),
            "size": LEGACY_EXTENSION_SIZE,
            "sha256": LEGACY_EXTENSION_SHA256,
        },
        "recursiveExtension": {
            "va": hex(extension_va),
            "size": len(extension),
            "sha256": sha256(extension),
            "boundaryRedirect": boundary_redirect(extension_va).hex(),
        },
        "scope": {
            "outerCall": hex(OUTER_CALL),
            "outerReturn": hex(OUTER_RETURN),
            "outerParentCall": hex(OUTER_PARENT_CALL),
            "outerParentReturn": hex(OUTER_PARENT_RETURN),
            "outerSource": -1,
            "destinationSlot": TARGET_SLOT,
            "outerCount": OUTER_COUNT,
            "outerArg4": OUTER_ARG4,
            "outerArg5": OUTER_ARG5,
            "recursiveCall": hex(RECURSIVE_CALL),
            "innerReturn": hex(INNER_RETURN),
            "innerArg4": INNER_ARG4,
            "innerArg5": INNER_ARG5,
            "countRelation": "inner_count = outer_count + 1",
            "fallback": "legacy V179 extension (unchanged 17 cases and existing negatives)",
        },
        "allowedChangedRanges": [[hex(low), hex(high)] for low, high in allowed],
        "availableBytesBefore": available,
        "protectionFloorGiB": 3,
        "nativeVerified": False,
        "deployed": False,
        "guiAccepted": False,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists() or args.receipt.exists():
        raise SystemExit("candidate and receipt must be new paths")
    image, receipt = build(args.input.read_bytes())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(image)
    args.receipt.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({
        "passed": True,
        "output": str(args.output),
        "sha256": receipt["outputSha256"],
        "extensionBytes": receipt["recursiveExtension"]["size"],
    }))


if __name__ == "__main__":
    main()
