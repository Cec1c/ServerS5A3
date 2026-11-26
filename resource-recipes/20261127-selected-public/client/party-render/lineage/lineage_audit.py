from __future__ import annotations

import argparse
import hashlib
import json
from dataclasses import dataclass
from pathlib import Path

import pefile


EXPECTED = {
    "swordghost_2111": "2111A6EA61395A51ED96EF5B3701F78E1B61B638D56C23F9168493CCB2DF935D",
    "c2": "C2DDF1006EF710BA7D379924C59A3F1D1C2FC98E619F5900A09A0FFA9E7BE2FF",
    "retained_47e": "47E557D5F6249F3FE526D7BAACB95EC05306EC43AF6CBCF86AFA0708F452CDCD",
    "weapon_045ff": "045FF05FC947062F417A4068F086509852C8FC3CCE85048D9D04A4D7AFFDBEB7",
    "d798": "D798B52293F8994926B2574DB3DEE075F07136A2C5096636D42F2844F8B82F1B",
    "party_d777": "D7774894A291D3596F2F3B5C3833BAFDECC9BA5C7EB95EA5E2EE98EC2A6135DA",
    "final_fe6a": "FE6A606F53FF4A496C56E78395D3288C897FBDC5A245441718414796C896BCE0",
}


@dataclass(frozen=True)
class ChangedRun:
    offset: int
    before: bytes
    after: bytes


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest().upper()


def changed_runs(before: bytes, after: bytes) -> list[ChangedRun]:
    if len(before) != len(after):
        raise ValueError("lineage inputs must have identical length")
    runs = []
    start = None
    for index, (left, right) in enumerate(zip(before, after)):
        if left != right and start is None:
            start = index
        elif left == right and start is not None:
            runs.append(ChangedRun(start, before[start:index], after[start:index]))
            start = None
    if start is not None:
        runs.append(ChangedRun(start, before[start:], after[start:]))
    return runs


def section_for(pe: pefile.PE, offset: int) -> str:
    for section in pe.sections:
        raw_start = section.PointerToRawData
        raw_end = raw_start + section.SizeOfRawData
        if raw_start <= offset < raw_end:
            return section.Name.rstrip(b"\0").decode("ascii", "replace")
    return "headers"


def summarize_pair(label: str, before_path: Path, after_path: Path) -> dict:
    before = before_path.read_bytes()
    after = after_path.read_bytes()
    before_sha = digest(before)
    after_sha = digest(after)
    if len(before) != len(after):
        return {
            "label": label,
            "before_sha256": before_sha,
            "after_sha256": after_sha,
            "before_size_bytes": len(before),
            "after_size_bytes": len(after),
            "length_delta": len(after) - len(before),
            "range_analysis": "not byte-aligned because the approved layer appends PE sections",
        }
    runs = changed_runs(before, after)
    pe = pefile.PE(data=before, fast_load=True)
    sections = {}
    for run in runs:
        name = section_for(pe, run.offset)
        bucket = sections.setdefault(name, {"runs": 0, "changed_bytes": 0})
        bucket["runs"] += 1
        bucket["changed_bytes"] += max(len(run.before), len(run.after))
    return {
        "label": label,
        "before_sha256": before_sha,
        "after_sha256": after_sha,
        "size_bytes": len(before),
        "changed_bytes": sum(max(len(run.before), len(run.after)) for run in runs),
        "changed_runs": len(runs),
        "sections": sections,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Read-only c2 -> D798 lineage audit.")
    parser.add_argument("--c2", required=True, type=Path)
    parser.add_argument("--retained-47e", required=True, type=Path)
    parser.add_argument("--weapon-045ff", required=True, type=Path)
    parser.add_argument("--d798", required=True, type=Path)
    parser.add_argument("--swordghost-2111", type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()

    paths = {
        "c2": args.c2,
        "retained_47e": args.retained_47e,
        "weapon_045ff": args.weapon_045ff,
        "d798": args.d798,
    }
    inputs = {}
    for key, path in paths.items():
        data = path.read_bytes()
        actual = digest(data)
        expected = EXPECTED[key]
        if actual != expected:
            raise SystemExit(f"{key} hash mismatch: {actual} != {expected}")
        inputs[key] = {"path": str(path), "sha256": actual, "size": len(data)}

    pairs = [
        summarize_pair("production_c2_to_retained_47e", args.c2, args.retained_47e),
        summarize_pair("retained_47e_to_weapon_045ff", args.retained_47e, args.weapon_045ff),
        summarize_pair("weapon_045ff_to_d798_owner0", args.weapon_045ff, args.d798),
        summarize_pair("production_c2_to_d798", args.c2, args.d798),
    ]
    if args.swordghost_2111 is not None:
        swordghost = args.swordghost_2111.read_bytes()
        if digest(swordghost) != EXPECTED["swordghost_2111"]:
            raise SystemExit("swordghost 2111 hash mismatch")
        pairs.insert(0, summarize_pair("swordghost_2111_to_retained_47e", args.swordghost_2111, args.retained_47e))
    result = {
        "schema": "dnf90-client-lineage-audit-v1",
        "inputs": inputs,
        "pairs": pairs,
        "notes": [
            "c2 and D798 have different intermediate bases; no full published build chain joins them.",
            "retained_47e is the exact retained pre-weapon-skin Slot 1 EXE captured by the approved weapon-skin task.",
            "weapon_045ff is the approved 58-edit weapon-skin build from retained_47e.",
            "D798 is weapon_045ff plus the approved 22-byte four-issues owner0 patch.",
            "The c2-to-D798 raw-range apply mode is deterministic only when the accepted D798 file is supplied as the range source.",
        ],
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
