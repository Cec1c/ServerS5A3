"""Verify the exact CMD817/CMD818 bitmap patch with the native filter."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import struct
import sys
from datetime import datetime, timezone
from pathlib import Path

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import (
    UC_X86_REG_EAX,
    UC_X86_REG_EIP,
    UC_X86_REG_ESP,
)


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
EXPECTED_TARGET_OFFSET = 111_747_942
EXPECTED_OLD_BYTE = 0x01
EXPECTED_NEW_BYTE = 0x07
NEW_OPCODES = (817, 818)
PRESERVED_OPCODE = 816
STACK = 0x10000000
STOP_SENTINEL = 0x20000000


class VerifyError(ValueError):
    """Raised when a file or native execution violates the fixed contract."""


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def align_up(value: int, alignment: int) -> int:
    return (value + alignment - 1) & ~(alignment - 1)


def changed_offsets(before: bytes, after: bytes) -> list[int]:
    if len(before) != len(after):
        return list(range(max(len(before), len(after))))
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


def static_contract(baseline: bytes, candidate: bytes) -> dict[str, object]:
    baseline_sha256 = sha256(baseline)
    candidate_sha256 = sha256(candidate)
    if len(baseline) != BASELINE_SIZE:
        raise VerifyError(
            f"baseline length mismatch: got {len(baseline)}, expected {BASELINE_SIZE}"
        )
    if baseline_sha256 != BASELINE_SHA256:
        raise VerifyError(
            f"baseline SHA-256 mismatch: got {baseline_sha256}, "
            f"expected {BASELINE_SHA256}"
        )
    if len(candidate) != BASELINE_SIZE:
        raise VerifyError(
            f"candidate length mismatch: got {len(candidate)}, expected {BASELINE_SIZE}"
        )
    if candidate_sha256 != CANDIDATE_SHA256:
        raise VerifyError(
            f"candidate SHA-256 mismatch: got {candidate_sha256}, "
            f"expected {CANDIDATE_SHA256}"
        )

    edits = changed_offsets(baseline, candidate)
    if edits != [EXPECTED_TARGET_OFFSET]:
        raise VerifyError(
            f"candidate must differ at only file offset {EXPECTED_TARGET_OFFSET}; "
            f"got {edits[:16]}"
        )
    if baseline[EXPECTED_TARGET_OFFSET] != EXPECTED_OLD_BYTE:
        raise VerifyError("baseline target byte is not 0x01")
    if candidate[EXPECTED_TARGET_OFFSET] != EXPECTED_NEW_BYTE:
        raise VerifyError("candidate target byte is not 0x07")

    layouts: list[dict[str, int]] = []
    for data in (baseline, candidate):
        try:
            image = pefile.PE(data=data, fast_load=True)
        except pefile.PEFormatError as exc:
            raise VerifyError(f"invalid PE image: {exc}") from exc
        try:
            if image.FILE_HEADER.Machine != 0x14C:
                raise VerifyError("input is not x86 PE32")
            if image.OPTIONAL_HEADER.Magic != 0x10B:
                raise VerifyError("input is not PE32")
            if image.OPTIONAL_HEADER.ImageBase != IMAGE_BASE:
                raise VerifyError(
                    f"unexpected image base 0x{image.OPTIONAL_HEADER.ImageBase:X}"
                )
            pe_offset = image.DOS_HEADER.e_lfanew
            optional_offset = pe_offset + 24
            section_table_offset = (
                optional_offset + image.FILE_HEADER.SizeOfOptionalHeader
            )
            section_table_size = image.FILE_HEADER.NumberOfSections * 40
            layouts.append(
                {
                    "header_size": image.OPTIONAL_HEADER.SizeOfHeaders,
                    "image_size": image.OPTIONAL_HEADER.SizeOfImage,
                    "checksum_offset": optional_offset + 64,
                    "section_table_offset": section_table_offset,
                    "section_table_size": section_table_size,
                    "section_count": image.FILE_HEADER.NumberOfSections,
                    "filter_offset": image.get_offset_from_rva(FILTER_VA - IMAGE_BASE),
                    "target_offset": image.get_offset_from_rva(
                        BITMAP_VA + 817 // 8 - IMAGE_BASE
                    ),
                }
            )
        finally:
            image.close()

    before_layout, after_layout = layouts
    for key in (
        "header_size",
        "image_size",
        "checksum_offset",
        "section_table_offset",
        "section_table_size",
        "section_count",
        "filter_offset",
        "target_offset",
    ):
        if before_layout[key] != after_layout[key]:
            raise VerifyError(f"PE layout changed: {key}")
    filter_offset = before_layout["filter_offset"]
    target_offset = before_layout["target_offset"]
    if target_offset != EXPECTED_TARGET_OFFSET:
        raise VerifyError(
            f"bitmap file offset mismatch: got {target_offset}, "
            f"expected {EXPECTED_TARGET_OFFSET}"
        )
    if baseline[filter_offset : filter_offset + FILTER_SIZE] != FILTER_BYTES:
        raise VerifyError("baseline native filter bytes do not match")
    if candidate[filter_offset : filter_offset + FILTER_SIZE] != FILTER_BYTES:
        raise VerifyError("candidate native filter bytes changed")

    header_size = before_layout["header_size"]
    checksum_offset = before_layout["checksum_offset"]
    section_table_offset = before_layout["section_table_offset"]
    section_table_end = section_table_offset + before_layout["section_table_size"]
    if baseline[:header_size] != candidate[:header_size]:
        raise VerifyError("PE headers changed")
    if baseline[section_table_offset:section_table_end] != candidate[
        section_table_offset:section_table_end
    ]:
        raise VerifyError("PE section table changed")
    checksum_value = int.from_bytes(
        baseline[checksum_offset : checksum_offset + 4], "little"
    )
    candidate_checksum = int.from_bytes(
        candidate[checksum_offset : checksum_offset + 4], "little"
    )
    if candidate_checksum != checksum_value:
        raise VerifyError("PE checksum field changed")

    ret_sites = [
        index for index in range(len(FILTER_BYTES) - 2) if FILTER_BYTES[index : index + 3] == b"\xC2\x04\x00"
    ]
    if len(ret_sites) != 2 or ret_sites[-1] != FILTER_SIZE - 3:
        raise VerifyError("native filter does not end in the expected `ret 4`")

    return {
        "baseline_sha256": baseline_sha256,
        "candidate_sha256": candidate_sha256,
        "baseline_size": len(baseline),
        "candidate_size": len(candidate),
        "changed_file_offsets": edits,
        "edited_file_offset_hex": f"0x{target_offset:X}",
        "edited_va": f"0x{BITMAP_VA + 817 // 8:X}",
        "edited_rva": f"0x{BITMAP_VA + 817 // 8 - IMAGE_BASE:X}",
        "before_byte": f"0x{baseline[target_offset]:02X}",
        "after_byte": f"0x{candidate[target_offset]:02X}",
        "filter_offset": filter_offset,
        "filter_sha256": sha256(FILTER_BYTES),
        "filter_bytes_identical": True,
        "filter_ret4_relative_offsets": ret_sites,
        "pe_header_bytes_identical": True,
        "pe_checksum_field_offset": checksum_offset,
        "pe_checksum_value_unchanged": f"0x{checksum_value:08X}",
        "pe_section_count": before_layout["section_count"],
        "pe_section_table_identical": True,
        "new_section_added": False,
        "all_other_bytes_identical": True,
        "notification_or_hook_bytes_changed": False,
    }


def make_vm(data: bytes) -> Uc:
    try:
        image = pefile.PE(data=data, fast_load=True)
    except pefile.PEFormatError as exc:
        raise VerifyError(f"invalid PE image: {exc}") from exc
    try:
        image_base = image.OPTIONAL_HEADER.ImageBase
        image_size = image.OPTIONAL_HEADER.SizeOfImage
        header_size = image.OPTIONAL_HEADER.SizeOfHeaders
        sections = [
            (section.VirtualAddress, section.get_data()) for section in image.sections
        ]
    finally:
        image.close()

    vm = Uc(UC_ARCH_X86, UC_MODE_32)
    vm.mem_map(image_base, align_up(image_size, 0x1000))
    vm.mem_write(image_base, data[:header_size])
    for relative_va, section_data in sections:
        if section_data:
            vm.mem_write(image_base + relative_va, section_data)
    vm.mem_map(STACK, 0x1000)
    vm.mem_map(STOP_SENTINEL, 0x1000)
    return vm


def run_filter(vm: Uc, opcode: int) -> int:
    vm.mem_write(STACK, struct.pack("<II", STOP_SENTINEL, opcode))
    vm.reg_write(UC_X86_REG_ESP, STACK)
    try:
        vm.emu_start(FILTER_VA, STOP_SENTINEL, count=64, timeout=1_000_000)
    except Exception as exc:
        raise VerifyError(f"native filter failed for opcode {opcode}: {exc}") from exc
    eip = vm.reg_read(UC_X86_REG_EIP)
    esp = vm.reg_read(UC_X86_REG_ESP)
    result = vm.reg_read(UC_X86_REG_EAX)
    if eip != STOP_SENTINEL:
        raise VerifyError(
            f"opcode {opcode}: native filter did not return to sentinel; EIP=0x{eip:X}"
        )
    if esp != STACK + 8:
        raise VerifyError(
            f"opcode {opcode}: stack not balanced after ret 4; ESP=0x{esp:X}"
        )
    if result not in (0, 1):
        raise VerifyError(f"opcode {opcode}: filter returned non-boolean EAX={result}")
    return int(result)


def decisions(data: bytes) -> bytearray:
    vm = make_vm(data)
    accepted = bytearray((65536 + 7) // 8)
    for opcode in range(65536):
        if run_filter(vm, opcode):
            accepted[opcode // 8] |= 1 << (opcode % 8)
    return accepted


def execute_native(baseline: bytes, candidate: bytes) -> dict[str, object]:
    before = decisions(baseline)
    after = decisions(candidate)
    changed = [opcode for opcode in range(65536) if before[opcode // 8] & (1 << (opcode % 8)) != after[opcode // 8] & (1 << (opcode % 8))]
    if changed != list(NEW_OPCODES):
        raise VerifyError(
            f"native filter decisions changed for {changed}, expected {list(NEW_OPCODES)}"
        )

    def bit(bitmap: bytearray, opcode: int) -> int:
        return int(bool(bitmap[opcode // 8] & (1 << (opcode % 8))))

    if bit(before, PRESERVED_OPCODE) != 1 or bit(after, PRESERVED_OPCODE) != 1:
        raise VerifyError("CMD816 was not preserved as accepted")
    for opcode in NEW_OPCODES:
        if bit(before, opcode) != 0 or bit(after, opcode) != 1:
            raise VerifyError(f"CMD{opcode} did not change from rejected to accepted")

    rows = []
    for opcode in (PRESERVED_OPCODE, *NEW_OPCODES):
        rows.append(
            {
                "opcode": opcode,
                "baseline": bit(before, opcode),
                "candidate": bit(after, opcode),
            }
        )
    return {
        "compared_opcodes": 65536,
        "newly_accepted_opcodes": changed,
        "removed_accepted_opcodes": [],
        "baseline_accepted_count": sum(byte.bit_count() for byte in before),
        "candidate_accepted_count": sum(byte.bit_count() for byte in after),
        "baseline_decision_bitmap_sha256": sha256(bytes(before)),
        "candidate_decision_bitmap_sha256": sha256(bytes(after)),
        "selected_opcode_rows": rows,
        "every_call_returned_to_sentinel": True,
        "every_call_stack_balanced": True,
        "stack_bytes_consumed_per_call": 8,
        "ret_pop_bytes": 4,
    }


def write_receipt(path: Path, receipt: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = (json.dumps(receipt, indent=2, ensure_ascii=False) + "\n").encode(
        "utf-8"
    )
    with path.open("xb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())


def run(baseline_path: Path, candidate_path: Path, receipt_path: Path) -> dict[str, object]:
    baseline_file = baseline_path.resolve()
    candidate_file = candidate_path.resolve()
    receipt_file = receipt_path.resolve()
    if baseline_file == candidate_file or receipt_file in (baseline_file, candidate_file):
        raise VerifyError("baseline, candidate, and receipt paths must be distinct")
    if receipt_file.exists():
        raise VerifyError(f"refusing to overwrite existing receipt: {receipt_file}")

    baseline = baseline_file.read_bytes()
    candidate = candidate_file.read_bytes()
    static = static_contract(baseline, candidate)
    native = execute_native(baseline, candidate)
    receipt: dict[str, object] = {
        "schema": "dnf90.epic-piece-shop.cmd817-818.verify/v1",
        "created_utc": datetime.now(timezone.utc).isoformat(),
        "passed": True,
        "scope": (
            "Actual native PE filter at 0x6E83610 executed offline under Unicorn for "
            "all 65536 opcodes before and after the one-byte candidate. No full "
            "dispatcher, GUI consumer, game process, or network behavior is tested."
        ),
        **static,
        **native,
        "native_gate_executed": True,
        "full_dispatcher_tested": False,
        "gui_consumer_tested": False,
        "game_started": False,
        "native_acceptance": False,
    }
    write_receipt(receipt_file, receipt)
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True, help="fixed released DNF.exe")
    parser.add_argument("--candidate", type=Path, required=True, help="one-byte DNF.exe candidate")
    parser.add_argument("--receipt", type=Path, required=True, help="new JSON evidence file")
    args = parser.parse_args()
    try:
        receipt = run(args.baseline, args.candidate, args.receipt)
    except (OSError, VerifyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "passed": receipt["passed"],
                "candidate_sha256": receipt["candidate_sha256"],
                "compared_opcodes": receipt["compared_opcodes"],
                "newly_accepted_opcodes": receipt["newly_accepted_opcodes"],
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
