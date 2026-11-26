#!/usr/bin/env python3
"""Build the selected sky-only PVF candidate."""

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
    spec_path = (HERE / manifest["spec"]["path"]).resolve()
    if sha256(spec_path) != manifest["spec"]["sha256"]:
        raise SystemExit("sky spec SHA256 mismatch")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--input-sha256", required=True)
    parser.add_argument("--tool", type=Path, help="prebuilt sky-cubes-pvf binary")
    args = parser.parse_args()

    input_path = args.input.resolve()
    output_path = args.output.resolve()
    report_path = args.report.resolve()
    tool_path = args.tool.resolve() if args.tool else None

    if output_path.exists():
        parser.error(f"output already exists: {output_path}")
    if not input_path.is_file():
        parser.error(f"input does not exist: {input_path}")
    if sha256(input_path) != args.input_sha256.lower():
        parser.error("input SHA256 mismatch")

    command = [str(tool_path)] if tool_path else ["go", "run", "./cmd/tools/sky-cubes-pvf"]
    command += [
        "-in", str(input_path),
        "-out", str(output_path),
        "-spec", str(spec_path),
        "-report", str(report_path),
        "-sha256", args.input_sha256,
    ]
    print("running:", " ".join(command), flush=True)
    subprocess.run(command, cwd=GO_SERVER, check=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
