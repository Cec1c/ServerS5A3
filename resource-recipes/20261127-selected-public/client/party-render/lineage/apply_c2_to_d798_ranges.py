from __future__ import annotations

import argparse
import hashlib
import json
import struct
from pathlib import Path

import pefile


C2_SHA256 = "C2DDF1006EF710BA7D379924C59A3F1D1C2FC98E619F5900A09A0FFA9E7BE2FF"
D798_SHA256 = "D798B52293F8994926B2574DB3DEE075F07136A2C5096636D42F2844F8B82F1B"
EXPECTED_CHANGED_BYTES = 151532
EXPECTED_CHANGED_RUNS = 3872
OWNER0_RANGES = {
    (111688208, 9): bytes.fromhex("909090909090909090"),
    (111688225, 13): bytes.fromhex("90909090909090909090909090"),
}


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest().upper()


def changed_runs(before: bytes, after: bytes) -> list[tuple[int, bytes, bytes]]:
    if len(before) != len(after):
        raise ValueError("c2 and D798 must have identical file sizes")
    runs: list[tuple[int, bytes, bytes]] = []
    start = None
    for index, (old, new) in enumerate(zip(before, after)):
        if old != new and start is None:
            start = index
        elif old == new and start is not None:
            runs.append((start, before[start:index], after[start:index]))
            start = None
    if start is not None:
        runs.append((start, before[start:], after[start:]))
    return runs


def section_for(pe: pefile.PE, offset: int) -> str:
    for section in pe.sections:
        begin = section.PointerToRawData
        end = begin + section.SizeOfRawData
        if begin <= offset < end:
            return section.Name.rstrip(b"\0").decode("ascii", "replace")
    return "headers"


def source_owner(section: str, offset: int, length: int) -> str:
    if (offset, length) in OWNER0_RANGES:
        return "four-issues-20261125 owner0 22-byte patch (Codex implementation, user-owned task)"
    if section in {".sgtext", ".sgdata"}:
        return "retained accepted 47e predecessor (no published source chain)"
    if section == ".wstext":
        return "20261124 weapon-skin 58-edit recipe; base-sensitive compiled layout (duduqingrangyirang recipe lineage)"
    if section in {".tp3", ".xscene"}:
        return "retained accepted 47e predecessor small binary layers (no published source chain)"
    if section == ".rebuild":
        return "four-issues-20261125 owner0 22-byte patch"
    if section == "headers":
        return "PE checksum/header normalization from accepted predecessor and approved patches"
    return "unclassified; preserve for review"


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Reproduce D798 from production c2 using only strict changed ranges from the accepted D798 input."
    )
    parser.add_argument("--c2", required=True, type=Path)
    parser.add_argument("--accepted-d798", required=True, type=Path)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--output", type=Path, help="optional independent output; omit for audit-only mode")
    args = parser.parse_args()

    c2 = args.c2.read_bytes()
    d798 = args.accepted_d798.read_bytes()
    if sha256(c2) != C2_SHA256:
        raise SystemExit("production c2 hash mismatch")
    if sha256(d798) != D798_SHA256:
        raise SystemExit("accepted D798 hash mismatch")
    if len(c2) != len(d798):
        raise SystemExit("c2 and D798 size mismatch")

    pe = pefile.PE(data=d798, fast_load=True)
    runs = changed_runs(c2, d798)
    changed_bytes = sum(len(before) for _, before, _ in runs)
    if changed_bytes != EXPECTED_CHANGED_BYTES or len(runs) != EXPECTED_CHANGED_RUNS:
        raise SystemExit(f"unexpected c2->D798 scope: {changed_bytes} bytes / {len(runs)} runs")

    manifest_runs = []
    range_plan = hashlib.sha256()
    section_summary: dict[str, dict[str, int | str]] = {}
    for offset, before, after in runs:
        length = len(before)
        if (offset, length) in OWNER0_RANGES:
            expected = OWNER0_RANGES[(offset, length)]
            if after != expected:
                raise SystemExit(f"owner0 range mismatch at {offset:#x}")
        section = section_for(pe, offset)
        owner = source_owner(section, offset, length)
        manifest_runs.append([offset, length, section])
        range_plan.update(struct.pack("<II", offset, length))
        range_plan.update(before)
        range_plan.update(after)
        bucket = section_summary.setdefault(section, {"runs": 0, "bytes": 0, "source_owner": owner})
        bucket["runs"] += 1
        bucket["bytes"] += length

    result = {
        "schema": "dnf90-c2-to-d798-range-manifest-v1",
        "mode": "apply accepted c2-to-D798 file ranges only",
        "c2": {"sha256": C2_SHA256, "size": len(c2)},
        "d798": {"sha256": D798_SHA256, "size": len(d798)},
        "changed_bytes": changed_bytes,
        "changed_runs": len(runs),
        "range_plan_sha256": range_plan.hexdigest().upper(),
        "section_summary": section_summary,
        "owner0_22_byte_patch": [
            {"file_offset": offset, "length": length, "after": after.hex()}
            for (offset, length), after in OWNER0_RANGES.items()
        ],
        "ranges": manifest_runs,
        "provenance": {
            "c2": "approved 20261124 weapon-skin production build from 2111A6EA",
            "d798": "approved 101 Slot 1 EXE: same weapon-skin recipe on retained 47E557D5 plus 22-byte four-issues owner0 patch",
            "limitation": "No published build chain reconstructs retained 47E557D5. This mode preserves the accepted predecessor bytes and applies only observed changed ranges.",
        },
    }
    args.manifest.parent.mkdir(parents=True, exist_ok=True)
    args.manifest.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    if args.output is not None:
        output = bytearray(c2)
        for offset, _, after in runs:
            output[offset:offset + len(after)] = after
        if sha256(output) != D798_SHA256:
            raise SystemExit("reconstructed D798 hash mismatch")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_bytes(output)
        print(f"reconstructed D798 -> {args.output}")
    else:
        print("audit-only: exact range application verified; no EXE written")


if __name__ == "__main__":
    main()
