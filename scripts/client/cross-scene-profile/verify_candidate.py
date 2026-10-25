#!/usr/bin/env python3
"""Offline verifier for the reviewed eight-site cross-scene candidate draft."""
from pathlib import Path
import argparse
import hashlib
import json
import sys

sys.dont_write_bytecode = True
if sys.flags.optimize:
    raise RuntimeError("Verification requires assertions; do not use python -O or -OO")

import aux_cache
import aux_release
import detail_v2
import menu_v2
import mode0
import mode8
import portable_core
import portrait
import portrait_reset
import request_context


RUNNERS = (
    ("10-menu-candidate-v2.json", menu_v2),
    ("20-detail-candidate-v2.json", detail_v2),
    ("30-aux-cache-v2.json", aux_cache),
    ("40-portrait-candidate.json", portrait),
    ("50-mode8-route-candidate.json", mode8),
    ("60-mode0-appearance-data.json", mode0),
    ("70-request-context.json", request_context),
    ("80-portrait-reset.json", portrait_reset),
    ("90-aux-release.json", aux_release),
)


def load_manifests(root):
    manifests = {}
    for relative in portable_core.MANIFEST_PATHS:
        path = root / relative
        portable_core.require(path.is_file(),
                              "missing manifest: {}".format(relative))
        manifests[relative] = json.loads(path.read_bytes().decode("utf-8"))
    return manifests


def first_difference(left, right):
    limit = min(len(left), len(right))
    for index in range(limit):
        if left[index] != right[index]:
            return index
    return limit if len(left) != len(right) else None


def candidate_proof(baseline, candidate, root, structural):
    actual_sha256 = portable_core.sha256_hex(candidate)
    portable_core.require(
        actual_sha256 == portable_core.CANDIDATE_SHA256,
        "candidate SHA-256 is not the frozen db8 value: {}".format(
            actual_sha256))
    portable_core.require(
        len(candidate) == portable_core.CANDIDATE_SIZE,
        "candidate size is not the frozen value: {}".format(len(candidate)))
    expected, build_receipt = portable_core.build_expected_candidate(
        baseline, root)
    if candidate != expected:
        raise portable_core.VerificationError(
            "candidate differs from manifest/build_candidate reconstruction "
            "at byte 0x{:x}".format(first_difference(candidate, expected)))
    return dict(
        passed=True,
        baselineSha256=portable_core.sha256_hex(baseline),
        baselineSize=len(baseline),
        candidateSha256=actual_sha256,
        candidateSize=len(candidate),
        reconstructedCandidateSha256=build_receipt["outputSha256"],
        fullCandidateByteEquality=True,
        firstDifference=None,
        patchCount=build_receipt["patchCount"],
        manifestHashes=build_receipt["manifestHashes"],
        manifestMapping={path: len(manifest.get("patches", [manifest]))
                         for path, manifest in structural.items()},
        patches=[dict(name=patch.get("name"),
                      sourceManifest=patch["sourceManifest"],
                      site=patch["site"],
                      candidateAddress=patch["candidateAddress"],
                      codeSha256=patch["codeSha256"])
                 for patch in build_receipt["patches"]],
        checksumVerified=build_receipt["checksumVerified"],
        existingBytesPreserved=(
            build_receipt["existingBytesPreservedOutsideListedSitesAndHeaders"]),
        sourceHashes=portable_core.source_hashes(
            root, ["portable_core.py", "verify_candidate.py"]),
        exeCopied=False,
        exeExecuted=False,
        deployed=False,
        guiAccepted=False,
        scope=("Frozen a751/db8 digest checks plus full-byte comparison "
               "against the four manifests and reviewed build_candidate PE "
               "transformation. The candidate EXE is read from its explicit "
               "path and is never copied."),
    )


def declared_cases(receipt):
    for key in ("caseCount", "candidateCases", "cases"):
        if key in receipt:
            value = receipt[key]
            return len(value) if isinstance(value, list) else value
    total = 0
    for key in ("drawCases", "guardCases"):
        value = receipt.get(key)
        if isinstance(value, list):
            total += len(value)
    return total


def main(argv=None):
    import unicorn
    portable_core.require(unicorn.__version__ == "2.1.4",
                          "Verification requires unicorn==2.1.4")
    parser = argparse.ArgumentParser(
        description=("Verify the frozen R195 cross-scene candidate without "
                     "copying or executing it."))
    parser.add_argument("--baseline", type=Path, required=True,
                        help="exact frozen a751 baseline EXE")
    parser.add_argument("--candidate", type=Path, required=True,
                        help="exact frozen db8 candidate EXE")
    parser.add_argument("--output", type=Path, required=True,
                        help="new output directory; must not already exist")
    args = parser.parse_args(argv)

    root = Path(__file__).resolve().parent
    baseline_path = args.baseline.expanduser().resolve(strict=True)
    candidate_path = args.candidate.expanduser().resolve(strict=True)
    output_path = args.output.expanduser().resolve(strict=False)
    portable_core.require(baseline_path.is_file(), "baseline is not a file")
    portable_core.require(candidate_path.is_file(), "candidate is not a file")
    portable_core.require(not output_path.exists(),
                          "output directory already exists: {}".format(
                              output_path))
    portable_core.require(output_path != baseline_path.parent and
                          output_path != candidate_path.parent,
                          "output must be a new directory")

    baseline = baseline_path.read_bytes()
    portable_core.require(
        len(baseline) == portable_core.INPUT_SIZE,
        "baseline size is not the frozen a751 value: {}".format(len(baseline)))
    portable_core.require(
        portable_core.sha256_hex(baseline) == portable_core.BASE_SHA256,
        "baseline SHA-256 is not the frozen a751 value")
    candidate = candidate_path.read_bytes()

    manifests = load_manifests(root)
    # Perform the full manifest/build_candidate comparison before creating
    # output. No EXE is read from the draft tree or copied anywhere.
    byte_receipt = candidate_proof(baseline, candidate, root, manifests)

    output_path.mkdir()
    proofs_path = output_path / "proofs"
    proofs_path.mkdir()
    portable_core.write_json_exclusive(
        proofs_path / "00-candidate-byte-proof.json", byte_receipt)

    image = baseline
    summary_entries = []
    for name, module in RUNNERS:
        receipt = module.run_suite(root, image, manifests)
        portable_core.require(receipt.get("passed") is True,
                              "{} did not report passed=true".format(name))
        target = proofs_path / name
        portable_core.write_json_exclusive(target, receipt)
        summary_entries.append(dict(
            proof=name,
            caseCount=declared_cases(receipt),
            sha256=portable_core.sha256_hex(target.read_bytes()),
            sourceHashes=receipt.get("sourceHashes") or
                         receipt.get("sources") or {},
        ))

    summary = dict(
        passed=True,
        baselineName=baseline_path.name,
        baselineSha256=portable_core.BASE_SHA256,
        candidateName=candidate_path.name,
        candidateSha256=portable_core.CANDIDATE_SHA256,
        fullCandidateByteEquality=True,
        outputPath=str(output_path),
        proofs=summary_entries,
        proofCount=len(summary_entries) + 1,
        artifacts=["00-candidate-byte-proof.json"] +
                  [entry["proof"] for entry in summary_entries],
        sourceHashes=portable_core.source_hashes(
            root, [path.name for path in sorted(root.glob("*.py"))] +
                  list(portable_core.MANIFEST_PATHS) +
                  ["requirements-validation.txt"]),
        exeCopied=False,
        exeExecuted=False,
        processAccess=False,
        deployed=False,
        guiAccepted=False,
        scope=("Offline Unicorn simulations preserve the reviewed final-v2 "
               "menu/detail closure and the remaining scoped verifiers. "
               "Candidate bytes are checked against all eight manifest "
               "patches before simulation. No game process, GUI acceptance, "
               "deployment or server proof."),
    )
    portable_core.write_json_exclusive(output_path / "run-summary.json", summary)
    print(json.dumps(dict(
        passed=True,
        proofCount=summary["proofCount"],
        candidateSha256=summary["candidateSha256"],
        output=str(output_path),
    ), indent=2))


if __name__ == "__main__":
    main()
