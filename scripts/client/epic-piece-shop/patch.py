"""Create the exact one-byte CMD817/CMD818 receive-gate candidate."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

import pefile


BASELINE_SHA256 = "eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03"
CANDIDATE_SHA256 = "5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c"
BASELINE_SIZE = 112_549_888
IMAGE_BASE = 0x400000
FILTER_VA = 0x06E83610
BITMAP_VA = 0x06E92300
FILTER_BYTES = bytes.fromhex(
    "0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400"
)
FILTER_SIZE = len(FILTER_BYTES)
PRESERVED_OPCODE = 816
NEW_OPCODES = (817, 818)
EXPECTED_BASELINE_BYTE = 0x01
PATCH_MASK = 0x06
EXPECTED_CANDIDATE_BYTE = EXPECTED_BASELINE_BYTE | PATCH_MASK


class PatchError(ValueError):
    """Raised when the input or output violates the exact patch contract."""


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def changed_offsets(before: bytes, after: bytes) -> list[int]:
    if len(before) != len(after):
        raise PatchError("file length changed")
    changed: list[int] = []
    chunk_size = 1 << 20
    for chunk_start in range(0, len(before), chunk_size):
        chunk_end = min(chunk_start + chunk_size, len(before))
        if before[chunk_start:chunk_end] == after[chunk_start:chunk_end]:
            continue
        changed.extend(
            chunk_start + index
            for index, (old, new) in enumerate(
                zip(before[chunk_start:chunk_end], after[chunk_start:chunk_end])
            )
            if old != new
        )
    return changed


def pe_layout(data: bytes) -> dict[str, int | bytes]:
    try:
        image = pefile.PE(data=data, fast_load=True)
    except pefile.PEFormatError as exc:
        raise PatchError(f"invalid PE image: {exc}") from exc
    try:
        if image.FILE_HEADER.Machine != 0x14C:
            raise PatchError("input is not x86 PE32")
        if image.OPTIONAL_HEADER.Magic != 0x10B:
            raise PatchError("input is not PE32")
        image_base = image.OPTIONAL_HEADER.ImageBase
        if image_base != IMAGE_BASE:
            raise PatchError(
                f"unexpected image base 0x{image_base:X}; expected 0x{IMAGE_BASE:X}"
            )
        pe_offset = image.DOS_HEADER.e_lfanew
        optional_offset = pe_offset + 24
        section_table_offset = (
            optional_offset + image.FILE_HEADER.SizeOfOptionalHeader
        )
        section_table_size = image.FILE_HEADER.NumberOfSections * 40
        return {
            "image_base": image_base,
            "image_size": image.OPTIONAL_HEADER.SizeOfImage,
            "header_size": image.OPTIONAL_HEADER.SizeOfHeaders,
            "checksum_offset": optional_offset + 64,
            "section_count": image.FILE_HEADER.NumberOfSections,
            "section_table_offset": section_table_offset,
            "section_table_size": section_table_size,
            "filter_offset": image.get_offset_from_rva(FILTER_VA - image_base),
            "target_offset": image.get_offset_from_rva(
                BITMAP_VA + 817 // 8 - image_base
            ),
        }
    finally:
        image.close()


def build_candidate(data: bytes) -> tuple[bytes, dict[str, object]]:
    if len(data) != BASELINE_SIZE:
        raise PatchError(
            f"baseline length mismatch: got {len(data)}, expected {BASELINE_SIZE}"
        )
    baseline_sha256 = sha256(data)
    if baseline_sha256 != BASELINE_SHA256:
        raise PatchError(
            f"baseline SHA-256 mismatch: got {baseline_sha256}, "
            f"expected {BASELINE_SHA256}"
        )

    layout = pe_layout(data)
    filter_offset = int(layout["filter_offset"])
    target_offset = int(layout["target_offset"])
    if filter_offset < 0 or target_offset < 0:
        raise PatchError("filter or bitmap is not file-backed by the PE sections")
    if data[filter_offset : filter_offset + FILTER_SIZE] != FILTER_BYTES:
        raise PatchError("native filter machine code does not match the fixed baseline")

    old_byte = data[target_offset]
    if old_byte != EXPECTED_BASELINE_BYTE:
        raise PatchError(
            f"baseline bitmap byte mismatch at file offset {target_offset}: "
            f"got 0x{old_byte:02X}, expected 0x{EXPECTED_BASELINE_BYTE:02X}"
        )

    candidate_bytes = bytearray(data)
    candidate_bytes[target_offset] |= PATCH_MASK
    candidate = bytes(candidate_bytes)
    candidate_sha256 = sha256(candidate)
    if candidate_sha256 != CANDIDATE_SHA256:
        raise PatchError(
            f"candidate SHA-256 mismatch after one-byte edit: got {candidate_sha256}, "
            f"expected {CANDIDATE_SHA256}"
        )
    if candidate[target_offset] != EXPECTED_CANDIDATE_BYTE:
        raise PatchError("candidate bitmap byte has an unexpected value")

    edits = changed_offsets(data, candidate)
    if edits != [target_offset]:
        raise PatchError(f"candidate must change exactly one byte; changed {edits}")
    if len(candidate) != len(data):
        raise PatchError("candidate changed the file length")

    header_size = int(layout["header_size"])
    checksum_offset = int(layout["checksum_offset"])
    section_table_offset = int(layout["section_table_offset"])
    section_table_end = section_table_offset + int(layout["section_table_size"])
    if data[:header_size] != candidate[:header_size]:
        raise PatchError("PE headers changed")
    if data[section_table_offset:section_table_end] != candidate[
        section_table_offset:section_table_end
    ]:
        raise PatchError("PE section table changed")
    if candidate[filter_offset : filter_offset + FILTER_SIZE] != FILTER_BYTES:
        raise PatchError("native filter machine code changed")

    checksum_value = int.from_bytes(
        data[checksum_offset : checksum_offset + 4], "little"
    )
    candidate_checksum_value = int.from_bytes(
        candidate[checksum_offset : checksum_offset + 4], "little"
    )
    if candidate_checksum_value != checksum_value:
        raise PatchError("PE checksum field changed")

    bitmap_byte_va = BITMAP_VA + 817 // 8
    receipt: dict[str, object] = {
        "schema": "dnf90.epic-piece-shop.cmd817-818.patch/v1",
        "created_utc": datetime.now(timezone.utc).isoformat(),
        "passed": True,
        "scope": (
            "One bitmap byte only: preserve CMD816 and additionally enable CMD817 "
            "and CMD818 in the existing native receive filter. No native execution "
            "is performed by patch.py; use verify.py for the offline gate check."
        ),
        "baseline_sha256": baseline_sha256,
        "candidate_sha256": candidate_sha256,
        "baseline_size": len(data),
        "candidate_size": len(candidate),
        "image_base": f"0x{int(layout['image_base']):X}",
        "filter_va": f"0x{FILTER_VA:X}",
        "bitmap_va": f"0x{BITMAP_VA:X}",
        "edited_file_offset": target_offset,
        "edited_file_offset_hex": f"0x{target_offset:X}",
        "edited_va": f"0x{bitmap_byte_va:X}",
        "edited_rva": f"0x{bitmap_byte_va - IMAGE_BASE:X}",
        "before_byte": f"0x{old_byte:02X}",
        "after_byte": f"0x{candidate[target_offset]:02X}",
        "or_mask": f"0x{PATCH_MASK:02X}",
        "preserved_opcode": PRESERVED_OPCODE,
        "new_opcodes": list(NEW_OPCODES),
        "one_byte_same_byte_for_all_three": True,
        "changed_file_offsets": edits,
        "all_other_bytes_identical": True,
        "pe_header_bytes_identical": True,
        "pe_checksum_field_offset": checksum_offset,
        "pe_checksum_value_unchanged": f"0x{checksum_value:08X}",
        "pe_section_count": int(layout["section_count"]),
        "pe_section_table_identical": True,
        "new_section_added": False,
        "native_filter_sha256": sha256(FILTER_BYTES),
        "native_filter_bytes_identical": True,
        "notification_or_hook_bytes_changed": False,
        "native_acceptance": False,
    }
    return candidate, receipt


def write_new_file(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("xb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def run(input_path: Path, output_path: Path, receipt_path: Path) -> dict[str, object]:
    source = input_path.resolve()
    output = output_path.resolve()
    receipt_file = receipt_path.resolve()
    if output == source or receipt_file == source or output == receipt_file:
        raise PatchError("input, output, and receipt paths must be distinct")
    if output.exists():
        raise PatchError(f"refusing to overwrite existing output: {output}")
    if receipt_file.exists():
        raise PatchError(f"refusing to overwrite existing receipt: {receipt_file}")

    source_data = source.read_bytes()
    candidate, receipt = build_candidate(source_data)
    write_new_file(output, candidate)
    written = output.read_bytes()
    if written != candidate or sha256(written) != receipt["candidate_sha256"]:
        raise PatchError("output readback differs from the in-memory candidate")

    receipt["input_name"] = source.name
    receipt["output_name"] = output.name
    receipt["output_readback_sha256"] = sha256(written)
    write_new_file(
        receipt_file,
        (json.dumps(receipt, indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
    )
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="fixed baseline DNF.exe")
    parser.add_argument("--output", type=Path, required=True, help="new one-byte candidate")
    parser.add_argument("--receipt", type=Path, required=True, help="new JSON evidence file")
    args = parser.parse_args()
    try:
        receipt = run(args.input, args.output, args.receipt)
    except (OSError, PatchError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "passed": receipt["passed"],
                "baseline_sha256": receipt["baseline_sha256"],
                "candidate_sha256": receipt["candidate_sha256"],
                "changed_file_offsets": receipt["changed_file_offsets"],
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
