"""Serial portable verification of the fixed HUD v4 candidate using fixtures.

Only the three verifier scripts run; the builder is imported for its PE/DRAW
definitions by those scripts. Failed runs keep their receipts and separate
stdout/stderr logs. summary.json is created only after all groups pass.
"""
import argparse
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True

UNICORN_VERSION = "2.1.4"
GROUPS = (
    ("lookup", "verify_lookup.py", 695),
    ("draw", "verify_draw.py", 48),
    ("lifecycle", "verify_lifecycle.py", 64),
)
SOURCE_FILES = (
    "verify_candidate.py", "verify_lookup.py", "verify_draw.py",
    "verify_lifecycle.py", "build_candidate.py", "aux_cache.py",
    "requirements.txt",
)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def read_group(path, expected_count, base_sha, candidate_sha):
    result = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(result, dict), "{}: receipt must be an object".format(path.name))
    require(result.get("passed") is True, "{}: passed is not true".format(path.name))
    require(result.get("candidateSha256") == candidate_sha,
            "{}: candidate SHA-256 mismatch".format(path.name))
    if "baseSha256" in result:
        require(result["baseSha256"] == base_sha,
                "{}: base SHA-256 mismatch".format(path.name))
    cases = result.get("cases")
    require(isinstance(cases, list) and len(cases) == expected_count,
            "{}: expected {} cases".format(path.name, expected_count))
    if "caseCount" in result:
        require(type(result["caseCount"]) is int and result["caseCount"] == len(cases),
                "{}: caseCount differs from cases".format(path.name))
    require(result.get("processAccess") is False,
            "{}: processAccess must be false".format(path.name))
    for flag in ("deployed", "guiAccepted"):
        require(result.get(flag, False) is False,
                "{}: {} must be false".format(path.name, flag))
    for field in ("actual", "fixtures", "limitations"):
        require(isinstance(result.get(field), list) and
                all(isinstance(item, str) for item in result[field]),
                "{}: missing or invalid {} scope".format(path.name, field))
    return dict(
        passed=True, caseCount=len(cases), receipt=path.name,
        receiptSha256=sha256_file(path),
        actual=result["actual"], fixtures=result["fixtures"],
        limitations=result["limitations"],
        processAccess=False, deployed=False, guiAccepted=False,
    )


def main(argv=None):
    require(__debug__ and not sys.flags.optimize, "optimized Python unsupported")
    parser = argparse.ArgumentParser(
        description="Run the three fixed HUD v4 verifiers serially with fixtures.",
        allow_abbrev=False,
    )
    parser.add_argument("--input", type=Path, required=True, help="fixed v159 EXE")
    parser.add_argument("--candidate", type=Path, required=True, help="fixed HUD v4 EXE")
    parser.add_argument("--build-receipt", type=Path, required=True, help="build receipt JSON")
    parser.add_argument("--output-dir", type=Path, required=True,
                        help="new directory; any existing path is rejected")
    args = parser.parse_args(argv)
    require(not os.path.lexists(args.output_dir), "output directory already exists")
    output_dir = args.output_dir.resolve()
    require(not os.path.lexists(output_dir), "output directory already exists")

    require(version("unicorn") == UNICORN_VERSION, "unicorn==2.1.4 is required")
    import unicorn
    require(unicorn.__version__ == UNICORN_VERSION, "loaded Unicorn must be version 2.1.4")
    root = Path(__file__).resolve().parent
    for name in SOURCE_FILES:
        require((root / name).is_file(), "missing source file: {}".format(root / name))
    source_hashes = {name: sha256_file(root / name) for name in SOURCE_FILES}
    args.input = args.input.resolve()
    args.candidate = args.candidate.resolve()
    args.build_receipt = args.build_receipt.resolve()
    from verify_draw import load_inputs
    base, candidate, _ = load_inputs(args)
    input_sha = hashlib.sha256(base).hexdigest()
    candidate_sha = hashlib.sha256(candidate).hexdigest()
    build_receipt_sha = sha256_file(args.build_receipt)
    del base, candidate

    # Exclusive creation also rejects an empty directory created during preflight.
    output_dir.mkdir(parents=True, exist_ok=False)
    common_args = ["--input", str(args.input), "--candidate", str(args.candidate),
                   "--build-receipt", str(args.build_receipt)]
    groups = {}
    for name, script, expected_count in GROUPS:
        receipt_path = output_dir / (name + ".json")
        stdout_path = output_dir / (name + ".stdout.log")
        stderr_path = output_dir / (name + ".stderr.log")
        command = [sys.executable, "-B", "-u", str(root / script),
                   *common_args, "--receipt", str(receipt_path)]
        with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            completed = subprocess.run(
                command, cwd=root, stdin=subprocess.DEVNULL,
                stdout=stdout, stderr=stderr, check=False,
            )
        require(completed.returncode == 0,
                "{} failed (exit {}); logs retained in {}".format(
                    script, completed.returncode, output_dir))
        group = read_group(receipt_path, expected_count, input_sha, candidate_sha)
        group.update(script=script, exitCode=completed.returncode,
                     stdout=stdout_path.name, stderr=stderr_path.name)
        groups[name] = group

    # Keep the reported hashes tied to the files used by the serial run.
    require(source_hashes == {name: sha256_file(root / name) for name in SOURCE_FILES},
            "source files changed during verification; output directory retained")
    for path, expected in ((args.input, input_sha), (args.candidate, candidate_sha),
                           (args.build_receipt, build_receipt_sha)):
        require(sha256_file(path) == expected,
                "{} changed during verification; output directory retained".format(path))
    summary = dict(
        passed=True, sourceStable=True, unicornVersion=UNICORN_VERSION,
        inputSha256=input_sha, candidateSha256=candidate_sha,
        buildReceiptSha256=build_receipt_sha, sourceHashes=source_hashes,
        groups=groups, caseCounts={name: group["caseCount"] for name, group in groups.items()},
        totalCaseCount=sum(group["caseCount"] for group in groups.values()),
        scope=("Bounded native-code verification with synthetic objects and fixtures. "
               "Each group's actual, fixtures and limitations are copied from its receipt. "
               "Model allocation, resource construction/loading, render/destructor leaves, "
               "protected-value decoding and registry callbacks remain fixtures where listed. "
               "No real fashion-resource load, GPU rendering, complete HUD, live game-process "
               "access, deployment or GUI acceptance is established."),
        processAccess=False, deployed=False, guiAccepted=False,
    )
    summary_text = json.dumps(summary, indent=2) + "\n"
    with (output_dir / "summary.json").open("x", encoding="utf-8") as handle:
        handle.write(summary_text)
    print(json.dumps(dict(passed=True, caseCounts=summary["caseCounts"],
                          output=str(output_dir / "summary.json"))))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print("verification failed: {}".format(error), file=sys.stderr)
        raise SystemExit(1)
