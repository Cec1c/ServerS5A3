"""Build the fixed-v170 title OP19 manual-marker candidate."""
import argparse
import json
import struct
from pathlib import Path

from pe_support import INPUT_SHA256, INPUT_SIZE, Image, available_memory, pe_checksum, sha256
from title_extension import (
    CONTINUE,
    FALLBACK,
    OLD_VIRTUAL_SIZE,
    OP19UI,
    PARENT_RETURNS,
    PUSH_MARKER,
    TITLE_DEST_SLOT,
    TITLE_RETURNS,
    fallback_redirect,
    title_extension,
)


def build(data):
    available = available_memory()
    image = Image.from_bytes(data)
    if len(data) != INPUT_SIZE or image.sha256 != INPUT_SHA256:
        raise ValueError("exact fixed v170 SHA256 and size required")

    section = image.section(".op19ui")
    if (
        section["va"] != OP19UI
        or section["virtual_size"] != OLD_VIRTUAL_SIZE
        or section["raw_size"] != 0x1000
        or section["characteristics"] != 0x60000020
    ):
        raise ValueError("unexpected .op19ui section")

    fallback = bytes.fromhex("589d6a00e8b7998bfc")
    if image.bytes_at(FALLBACK, len(fallback)) != fallback:
        raise ValueError("unexpected .op19ui fallback sequence")
    if image.bytes_at(0x6E951C6, 2) != b"\x6a\x02":
        raise ValueError("unexpected marker-2 site")
    if image.bytes_at(0x6E951D2, 0x1000 - OLD_VIRTUAL_SIZE) != bytes(0x1000 - OLD_VIRTUAL_SIZE):
        raise ValueError("unexpected non-zero .op19ui tail")

    extension_va = OP19UI + OLD_VIRTUAL_SIZE
    extension = title_extension(extension_va)
    if OLD_VIRTUAL_SIZE + len(extension) > section["raw_size"]:
        raise ValueError("title extension exceeds .op19ui raw section")

    out = bytearray(data)
    jump_offset = image.file_offset(FALLBACK, 5)
    out[jump_offset : jump_offset + 5] = fallback_redirect(extension_va)
    extension_offset = image.file_offset(extension_va, len(extension))
    out[extension_offset : extension_offset + len(extension)] = extension
    new_size = OLD_VIRTUAL_SIZE + len(extension)
    struct.pack_into("<I", out, section["header"] + 8, new_size)
    struct.pack_into("<I", out, image.optional + 64, pe_checksum(out, image.optional + 64))
    result = bytes(out)

    allowed = sorted(
        [
            (section["header"] + 8, section["header"] + 12),
            (image.optional + 64, image.optional + 68),
            (jump_offset, jump_offset + 5),
            (extension_offset, extension_offset + len(extension)),
        ]
    )
    cursor = 0
    for low, high in allowed:
        if data[cursor:low] != result[cursor:low]:
            raise ValueError("change outside allowed range")
        cursor = high
    if data[cursor:] != result[cursor:]:
        raise ValueError("change outside allowed ranges")

    return result, {
        "inputSha256": INPUT_SHA256,
        "inputSize": INPUT_SIZE,
        "outputSha256": sha256(result),
        "outputSize": len(result),
        "op19uiVa": hex(OP19UI),
        "fallbackVa": hex(FALLBACK),
        "fallbackRedirect": hex(extension_va),
        "newVirtualSize": new_size,
        "titleReturns": [hex(value) for value in TITLE_RETURNS],
        "parentReturns": {
            hex(key): [hex(value) for value in values]
            for key, values in PARENT_RETURNS.items()
        },
        "allowedChangedRanges": [[hex(low), hex(high)] for low, high in allowed],
        "protectionFloorGiB": 3,
        "availableBytesBefore": available,
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
    args.output.write_bytes(image)
    args.receipt.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    main()
