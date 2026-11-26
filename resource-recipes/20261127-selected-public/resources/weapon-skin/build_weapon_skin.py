#!/usr/bin/env python3
"""Build a weapon-skin PVF candidate from the selected public recipe.

The wrapper resolves all caller paths before switching to the Go module root.
The Go tool appends only missing public index rows and verifies every selected
content path by canonical token SHA256. Runtime NPKs are published separately.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
GO_SERVER = REPO / "go-server"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    manifest = json.loads((HERE / "manifest.json").read_text(encoding="utf-8"))
    plan_path = (HERE / manifest["plan"]["path"]).resolve()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--sha256", default=manifest["input"]["sha256"], help="explicit input SHA256")
    parser.add_argument("--accept-input-sha256", action="append", default=[], help="additional explicitly accepted input SHA256")
    parser.add_argument("--tool", type=Path, help="prebuilt weapon-catalog-pvf binary")
    args = parser.parse_args()

    input_path = args.input.resolve()
    output_path = args.output.resolve()
    report_path = args.report.resolve()
    tool_path = args.tool.resolve() if args.tool else None

    if output_path.exists():
        parser.error(f"output already exists: {output_path}")
    if not input_path.is_file():
        parser.error(f"input does not exist: {input_path}")
    if not plan_path.is_file():
        parser.error(f"plan missing: {plan_path}")
    actual_plan = sha256(plan_path)
    if actual_plan != manifest["plan"]["sha256"]:
        parser.error(f"plan SHA256 mismatch: got {actual_plan} want {manifest['plan']['sha256']}")
    actual_input = sha256(input_path)
    accepted = {value.lower() for value in args.accept_input_sha256}
    if actual_input != args.sha256.lower() and actual_input not in accepted:
        parser.error(f"input SHA256 {actual_input} is neither the declared hash nor explicitly accepted")

    command = [str(tool_path)] if tool_path else ["go", "run", "./cmd/tools/weapon-catalog-pvf"]
    command += [
        "-in", str(input_path),
        "-out", str(output_path),
        "-plan", str(plan_path),
        "-report", str(report_path),
        "-sha256", actual_input,
    ]
    if args.accept_input_sha256:
        command += ["-accept-input-sha256", ",".join(args.accept_input_sha256)]
    print("running:", " ".join(command), flush=True)
    subprocess.run(command, cwd=GO_SERVER, check=True)
    print(f"report: {report_path}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
