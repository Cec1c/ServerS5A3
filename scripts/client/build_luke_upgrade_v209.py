"""Build a separate cumulative Luke/coupon candidate from official V208."""
import argparse
import hashlib
import json
from pathlib import Path

from patch_luke_selector_progress import build as enable_luke
from patch_upgrade_equipped_warning import build as fix_coupon

INPUT_SHA256 = "5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c"
INTERMEDIATE_SHA256 = "27f66d19ea80f18abbe7d342d78cbc908b79d9c9c3691b109c1a958cd2492b85"
OUTPUT_SHA256 = "478f4f5dc070253e7679aa7d8f8fda3b71ea6376569fc794865c44d0107098f4"


def build(data):
    if len(data) != 112549888 or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError("exact official V208 input required")
    intermediate, luke = enable_luke(data)
    if hashlib.sha256(intermediate).hexdigest() != INTERMEDIATE_SHA256:
        raise ValueError("Luke intermediate identity changed")
    output, coupon = fix_coupon(intermediate)
    if hashlib.sha256(output).hexdigest() != OUTPUT_SHA256:
        raise ValueError("cumulative output identity changed")
    return output, {"input_sha256": INPUT_SHA256, "output_sha256": OUTPUT_SHA256,
                    "bytes": len(output), "luke": luke, "coupon": coupon,
                    "gameplay_verified": False}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--input", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    args = p.parse_args()
    if args.input.resolve() == args.output.resolve() or args.output.exists():
        raise ValueError("output must be a separate new file")
    original = args.input.read_bytes()
    output, receipt = build(original)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("xb") as stream:
        stream.write(output)
    if args.input.read_bytes() != original or args.output.read_bytes() != output:
        raise RuntimeError("input/output readback mismatch")
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    main()
