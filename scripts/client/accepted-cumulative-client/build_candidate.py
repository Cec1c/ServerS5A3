#!/usr/bin/env python3
"""Rebuild the reviewed cumulative client from an exact base image.

The recipe writes a new directory only. It does not install or execute the
client, contact a service, or publish files.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import sys

ROOT = Path(__file__).resolve().parents[3]
INPUT_SHA256 = "9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250"
OUTPUT_SHA256 = "69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544"
INPUT_SIZE = 112607232

STEPS = (
    (
        "scripts/client/buff-group-registration/build_candidate.py",
        "3b912e3ec3849c740860885ac3865ab8074111093425c69b74b0d2f0696b2533",
        "0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f",
    ),
    (
        "scripts/client/mr-cumulative/build_candidate.py",
        "cd2ae476c83c674440ca8af0f77a59422eee087b8398a81a65dbb4d4b32a2edd",
        "748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82",
    ),
    (
        "scripts/client/raid-live-roster/build_candidate.py",
        "3a2d53d6de48569337ee94c3951591615be328ce9dec795c9abae309bf46fd7b",
        "1bdff5b5da1bfd54a1005a555e89a905cbedee3d1884ffe7ca22a54dfad381c9",
    ),
    (
        "scripts/client/raid-live-roster/movie_prompt_guard.py",
        "0ff1d95de0b419140daad3c1b48dce09c5fafad1b4334c0fd9cb89ba47990610",
        "d323e8cba1116b87a99716b92da10f36bc29999ad740fcada2fc8143e721ceda",
    ),
    (
        "scripts/client/raid-live-roster/failure_movie_close.py",
        "a57ff922b8c280032c958ea0f418ff6f9fb5718b42573c2e3c929678dec90fdc",
        "69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544",
    ),
)

DEPENDENCIES = {
    "scripts/client/quiet-profile-refresh/build_candidate.py": "dc2b6d0c75346990a131e0705a2da592d2393fe0fd4341b980e36d05826ae85d",
    "scripts/client/buff-local-snapshot/build_candidate.py": "a1f4d3b34c0d583dfbd191e81a2002bda0a1aabb15844b726a3b7905f6561ab1",
    "scripts/client/local-patches/pe_image.py": "1e679188d747401bc15a574cb762bce430c55765214f4d1c33e09b62ca3a855c",
    "scripts/client/local-patches/patches/imeup_all_keys.py": "c7459414c498bd6032c1475108dab3464af6db8709b01ea25f8397a6ce0b4599",
}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_pinned(relative, expected):
    path = ROOT / relative
    require(path.is_file(), "required recipe file missing: " + relative)
    data = path.read_bytes()
    require(sha256(data) == expected, "recipe identity differs: " + relative)
    return path, data


def build(raw):
    require(len(raw) == INPUT_SIZE and sha256(raw) == INPUT_SHA256,
            "exact reviewed cumulative input required")
    original = raw
    steps, ranges = [], []
    for index, (relative, recipe_sha, expected_output) in enumerate(STEPS):
        path, recipe_bytes = read_pinned(relative, recipe_sha)
        sys.path.insert(0, str(path.parent))
        spec = importlib.util.spec_from_file_location("cumulative_step_" + str(index), path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        result = module.build(raw)
        raw, receipt = result[-2:]
        require(sha256(raw) == expected_output,
                "step output identity differs: " + relative)
        steps.append(dict(
            recipe=relative,
            recipeSHA256=sha256(recipe_bytes),
            outputSHA256=sha256(raw),
            receipt=receipt,
        ))
        ranges.extend(receipt.get("changedRanges", receipt.get("ranges", [])))

    for relative, expected in DEPENDENCIES.items():
        read_pinned(relative, expected)

    require(sha256(raw) == OUTPUT_SHA256, "reviewed cumulative output differs")
    restored = bytearray(raw)
    for first, last in ranges:
        require(0 <= first < last <= len(original), "invalid changed range")
        restored[first:last] = original[first:last]
    require(bytes(restored) == original, "change outside pinned cumulative ranges")
    return raw, dict(
        inputSHA256=INPUT_SHA256,
        outputSHA256=OUTPUT_SHA256,
        bytes=len(raw),
        steps=steps,
        dependencies=DEPENDENCIES,
        allOtherBytesPreserved=True,
        reviewedOutputIdentical=True,
        installed=False,
        gameExecuted=False,
        nativeValidationExecuted=False,
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    if args.output_dir.exists():
        parser.error("new output directory required")
    sys.dont_write_bytecode = True
    source = args.input.read_bytes()
    raw, receipt = build(source)
    require(sha256(args.input.read_bytes()) == INPUT_SHA256, "input changed during build")
    args.output_dir.mkdir(parents=True, exist_ok=False)
    (args.output_dir / "DNF.exe").write_bytes(raw)
    (args.output_dir / "build.json").write_text(
        json.dumps(receipt, indent=2) + "\n", encoding="utf-8"
    )
    print(json.dumps({
        key: receipt[key]
        for key in ("inputSHA256", "outputSHA256", "bytes", "allOtherBytesPreserved")
    }))


if __name__ == "__main__":
    main()
