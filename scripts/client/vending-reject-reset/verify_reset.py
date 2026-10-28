#!/usr/bin/env python3
"""Verify a profile-driven vending reset candidate.

Static checks always run and do not require a client binary beyond the exact
local baseline/candidate paths.  A complete native RED/GREEN proof requires a
local fixture module implementing the contract below; that fixture is not part
of this repository and is deliberately supplied only by the operator.

Fixture contract:

    verify_reset_fixture(profile, baseline_bytes, candidate_bytes) -> dict

The returned object must contain ``passed: true``, ``tests: 8``, no failures,
errors or skips, and explicit boolean outcomes ``redFailureAdvanced``,
``greenFailureReset``, ``repeatedFailureReset``, ``successParity``,
``retrySuccessParity``, ``nullManagerSafe``, ``nullControllerSafe`` and
``helperMapped``.  The fixture must execute only the intended client control
flow with adapters for UI, timing, inventory and transport; it must not launch
the game or inspect another process.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys
import types

from build_candidate import build, load_profile, sha256


REQUIRED_FIXTURE_RESULTS = (
    "redFailureAdvanced",
    "greenFailureReset",
    "repeatedFailureReset",
    "successParity",
    "retrySuccessParity",
    "nullManagerSafe",
    "nullControllerSafe",
    "helperMapped",
)


def load_module(path: Path, source: bytes) -> types.ModuleType:
    module = types.ModuleType("vending_reset_local_fixture")
    module.__file__ = str(path)
    directory = str(path.parent)
    added = directory not in sys.path
    if added:
        sys.path.insert(0, directory)
    try:
        exec(compile(source, str(path), "exec"), module.__dict__)
    finally:
        if added:
            sys.path.remove(directory)
    return module


def verify(profile: dict, baseline: bytes, candidate: bytes, fixture_path: Path | None, fixture_sha256: str | None) -> dict:
    if sha256(baseline) != profile["input_sha256"] or len(baseline) != profile["input_size"]:
        raise ValueError("baseline does not match the profile")
    if profile.get("expected_output_sha256") is None or profile.get("expected_output_size") is None:
        raise ValueError("verification requires a profile with the reviewed output digest")
    expected, build_receipt = build(baseline, profile)
    if candidate != expected:
        raise ValueError("candidate differs from the deterministic profile build")
    if profile["expected_output_sha256"] != sha256(candidate):
        raise ValueError("candidate does not match expected_output_sha256")
    if profile["expected_output_size"] != len(candidate):
        raise ValueError("candidate does not match expected_output_size")

    report = {
        "schema": "dnf90-vending-reject-reset-verification/v1",
        "passed": False,
        "baselineSha256": sha256(baseline),
        "candidateSha256": sha256(candidate),
        "profileSha256": build_receipt["profileSha256"],
        "build": build_receipt,
        "static": {
            "deterministicBuildMatched": True,
            "declaredRangesPreserved": True,
            "newExecutableSection": True,
        },
        "native": {"executed": False, "passed": None, "fixturePathRecorded": False, "fixtureSha256": None},
    }
    if fixture_path is None:
        if fixture_sha256 is not None:
            raise ValueError("fixture SHA256 requires a fixture module")
        report["status"] = "static-only-native-not-run"
        return report

    if not isinstance(fixture_sha256, str) or len(fixture_sha256) != 64:
        raise ValueError("native fixture requires an exact 64-character fixture SHA256")
    fixture_source = fixture_path.read_bytes()
    actual_fixture_sha256 = hashlib.sha256(fixture_source).hexdigest()
    if actual_fixture_sha256 != fixture_sha256.lower():
        raise ValueError("native fixture SHA256 mismatch")

    module = load_module(fixture_path, fixture_source)
    fixture = getattr(module, "verify_reset_fixture", None)
    if not callable(fixture):
        raise ValueError("fixture module lacks verify_reset_fixture")
    result = fixture(profile, baseline, candidate)
    if not isinstance(result, dict) or result.get("passed") is not True or result.get("tests") != 8:
        raise ValueError("fixture did not report the complete 8-case pass")
    for key in REQUIRED_FIXTURE_RESULTS:
        if result.get(key) is not True:
            raise ValueError("fixture did not prove " + key)
    for key in ("failures", "errors", "skipped"):
        if result.get(key, 0) != 0:
            raise ValueError("fixture reported " + key)
    report["native"] = {
        "executed": True,
        "passed": True,
        "fixturePathRecorded": False,
        "fixtureSha256": actual_fixture_sha256,
        "tests": result["tests"],
        "outcomes": {key: True for key in REQUIRED_FIXTURE_RESULTS},
        "limitations": result.get("limitations", []),
    }
    report["status"] = "passed-static-and-native-fixture"
    report["passed"] = True
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--fixture-module", type=Path)
    parser.add_argument("--fixture-sha256")
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    if args.receipt is not None and args.receipt.exists():
        raise SystemExit(f"receipt already exists: {args.receipt}")
    profile = load_profile(args.profile)
    baseline = args.baseline.read_bytes()
    candidate = args.candidate.read_bytes()
    report = verify(profile, baseline, candidate, args.fixture_module, args.fixture_sha256)
    payload = json.dumps(report, indent=2) + "\n"
    if args.receipt is None:
        print(payload, end="")
    else:
        with args.receipt.open("x", encoding="utf8") as stream:
            stream.write(payload)
        print(json.dumps({"status": report["status"], "passed": report["passed"], "receipt": str(args.receipt)}))
    raise SystemExit(0 if report["passed"] or report["status"] == "static-only-native-not-run" else 1)


if __name__ == "__main__":
    main()
