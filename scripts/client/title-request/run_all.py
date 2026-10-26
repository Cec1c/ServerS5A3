"""Run ADD07 static analysis, candidate build, and bounded native ABI contrast."""
import argparse
import json
import subprocess
import sys
from pathlib import Path

from build_candidate import build
from pe_support import INPUT_SHA256, INPUT_SIZE, Image, available_memory


ROOT = Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    args = parser.parse_args()
    baseline_path = args.input.resolve()
    output_dir = args.output_dir.resolve()
    evidence_dir = args.evidence_dir.resolve()
    available = available_memory()
    baseline = Image(baseline_path, INPUT_SHA256, INPUT_SIZE)
    analysis = subprocess.run(
        [
            sys.executable,
            "-B",
            str(ROOT / "analyze_native.py"),
            "--image",
            str(baseline_path),
            "--evidence-dir",
            str(evidence_dir),
        ],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    analysis_json = json.loads(analysis.stdout)

    candidate_path = output_dir / "DNF-v170-title-request.exe"
    receipt_path = output_dir / "build.json"
    verification_path = output_dir / "verification.json"
    output_dir.mkdir(parents=True, exist_ok=True)
    if candidate_path.exists() != receipt_path.exists():
        raise SystemExit("candidate/receipt half-exists; preserve and inspect before retry")
    if candidate_path.exists():
        receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
        candidate = candidate_path.read_bytes()
        if (
            receipt.get("inputSha256") != INPUT_SHA256
            or receipt.get("outputSha256") != Image.from_bytes(candidate).sha256
        ):
            raise SystemExit("existing title-request candidate receipt mismatch")
    else:
        candidate, receipt = build(baseline_path.read_bytes())
        candidate_path.write_bytes(candidate)
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    image = Image.from_bytes(candidate)

    verify = subprocess.run(
        [
            sys.executable,
            "-B",
            str(ROOT / "verify_candidate.py"),
            "--baseline",
            str(baseline_path),
            "--candidate",
            str(candidate_path),
            "--output",
            str(verification_path),
        ],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    verification = json.loads(verification_path.read_text(encoding="utf-8"))
    result = {
        "availableBytes": available,
        "baseline": {
            "path": str(baseline_path),
            "sha256": baseline.sha256,
            "size": len(baseline.data),
        },
        "candidate": {
            "path": str(candidate_path),
            "sha256": image.sha256,
            "size": len(image.data),
        },
        "static": analysis_json,
        "build": receipt,
        "verification": verification,
    }
    (output_dir / "run.json").write_text(
        json.dumps(result, indent=2) + "\n", encoding="utf-8"
    )
    print(json.dumps({"passed": True, "candidate": str(candidate_path), "sha256": image.sha256}))


if __name__ == "__main__":
    main()
