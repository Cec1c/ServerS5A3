"""Build the current-member/row guarded P34 candidate from exactly v168.

Writes only a new output directory. The historical v167 builder remains intact.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path

import build_candidate as pe
from guarded_payload import emit

INPUT_SHA256 = "4474c31236f2f38de1c772f0ac7f1f267d5d52b9dbed33c4925c1df91353cc62"
LEGACY_BUILDER_SHA256 = "a4accee30ca97fa8269bf1afd544d7290d9e9efa50c038a9a24287f90cf4fd99"
REFERENCE_SHA256 = "3c78eaa81e5efd5eef397d8af55c27abc1922a75cb7b30a9ecd5557574e5029f"
PACKAGED_SHA256 = "78c48cc5ed4b8227d40a0d078109d71f7a6cc3d0059184def76c932293855a7c"
HELPER_SIZE = 993


def build(data):
    pe.require(pe.sha(Path(pe.__file__).read_bytes()) == LEGACY_BUILDER_SHA256,
               "reviewed historical PE writer changed")
    pe.INPUT_SHA256 = INPUT_SHA256
    pe.REFERENCE_HELPER_SIZE = HELPER_SIZE
    pe.REFERENCE_HELPER_SHA256 = REFERENCE_SHA256
    pe.emit = emit
    candidate, manifest = pe.build(data, ".rport")
    pe.require(pe.sha(pe.PE(candidate).read(0x6EC6000, HELPER_SIZE)) == PACKAGED_SHA256,
               "packaged current-member/row helper differs")
    manifest.update(schema="p34-current-row-v168-v2", source_provenance_sha256={
        "build_candidate.py": LEGACY_BUILDER_SHA256,
        "guarded_payload.py": pe.sha(Path(__file__).with_name("guarded_payload.py").read_bytes()),
        "build_v168.py": pe.sha(Path(__file__).read_bytes()),
    }, open_issues=[
        "Full game/GPU rendering and repeated-window acceptance remain pending.",
        "Offline tests use explicit resource-provider, frame-acquisition and graphics boundary fixtures.",
    ])
    manifest["helper"].update(call_targets=["0x01a5a550"], lazy_raid_getter=False,
                              current_raid_membership=True, current_dungeon_row_and_group=True,
                              cache_model_must_be_null=True, default_frame_range=[0, 15],
                              row_gate="0x6ec6100", member_gate="0x6ec6300")
    return candidate, manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="read-only v168 EXE")
    parser.add_argument("--output-dir", type=Path, required=True, help="new directory")
    args = parser.parse_args()
    source = args.input.resolve(strict=True)
    destination = args.output_dir.absolute()
    pe.require(not os.path.lexists(destination) and destination.parent.is_dir(), "output must be new")
    data = source.read_bytes()
    candidate, manifest = build(data)
    pe.require(pe.sha(source.read_bytes()) == INPUT_SHA256, "source changed")
    destination.mkdir(exist_ok=False)
    output = destination / "v168-p34-guarded-candidate.exe"
    with output.open("xb") as stream:
        stream.write(candidate)
    pe.require(pe.sha(output.read_bytes()) == manifest["output"]["sha256"], "disk readback differs")
    manifest["validation"]["disk_readback"] = True
    receipt = destination / "changes.json"
    with receipt.open("x", encoding="utf-8") as stream:
        json.dump(manifest, stream, indent=2, sort_keys=True)
        stream.write("\n")
    print(json.dumps(dict(candidate=str(output), manifest=str(receipt),
                          output_sha256=manifest["output"]["sha256"], released=False)))


if __name__ == "__main__":
    main()
