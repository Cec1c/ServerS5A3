"""Build and verify the item-text PVF candidate without publishing it.

The command is intentionally limited to an explicit baseline PVF, this recipe's
schema-2 plan, and a new candidate directory. It refuses an existing candidate,
preserves all non-target token payloads from the exported baseline, and records
archive/token/text readbacks only in the supplied evidence directory.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


HERE = Path(__file__).resolve().parent
MANIFEST_PATH = HERE / "manifest.json"
EXPECTED_RECEIPT_PATH = HERE / "expected-receipt.json"


def sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def load_json(path: Path):
    with path.open("r", encoding="utf-8") as stream:
        return json.load(stream)


def require(condition, message: str) -> None:
    if not condition:
        raise ValueError(message)


def require_int(value, message: str) -> int:
    require(type(value) is int, message)
    return value


def require_sha256(value, message: str) -> str:
    require(
        isinstance(value, str)
        and len(value) == 64
        and all(character in "0123456789abcdef" for character in value),
        message,
    )
    return value


def run_success(command: list[str], stdout_path: Path, stderr_path: Path) -> subprocess.CompletedProcess:
    result = subprocess.run(
        command,
        capture_output=True,
        timeout=300,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    stdout_path.write_bytes(result.stdout)
    stderr_path.write_bytes(result.stderr)
    require(result.returncode == 0, f"{command[1]} failed; inspect supplied stderr")
    return result


def run_json(command: list[str], stdout_path: Path, stderr_path: Path) -> dict:
    result = run_success(command, stdout_path, stderr_path)
    try:
        return json.loads(result.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"{command[1]} did not return UTF-8 JSON") from error


def validate_recipe(manifest: dict, plan: dict, token_dir: Path):
    require(manifest.get("schema") == 1, "manifest schema must be 1")
    require(manifest.get("classification") == "群服内容", "classification mismatch")
    require(plan.get("schema") == 2, "plan schema must be 2")
    require(plan.get("input_sha256") == manifest["baseline"]["sha256"], "baseline digest mismatch")
    edits = plan.get("edits")
    require(isinstance(edits, list) and len(edits) == 3, "plan must edit exactly three entries")
    require(len({item["path"] for item in edits}) == 3, "plan entry paths must be unique")

    changes_by_path = {}
    for change in manifest["expected_changes"]:
        changes_by_path.setdefault(change["path"], {})[change["token_index"]] = change
    require(
        sum(len(changes) for changes in changes_by_path.values()) == 4,
        "manifest must define exactly four text changes",
    )

    baseline_token_names = set(manifest["baseline_tokens"])
    require(len(baseline_token_names) == 3, "baseline token list must contain three files")
    observed_paths = set()
    baseline_names_by_path = {}
    changed_tokens = 0
    preserved_tokens = 0

    for edit in edits:
        path = edit.get("path")
        require(path in changes_by_path, "unexpected plan entry: " + str(path))
        observed_paths.add(path)
        require(set(edit) == {"path", "data_type", "before_sha256", "tokens"}, "unexpected edit fields")
        require(edit["data_type"] == 1, "edited entries must remain data type 1")
        changes = changes_by_path[path]

        baseline_matches = []
        for name in baseline_token_names:
            candidate = load_json(token_dir / name)
            if candidate.get("path") == path:
                baseline_matches.append(candidate)
        require(len(baseline_matches) == 1, "one baseline token export is required per entry")
        baseline = baseline_matches[0]
        baseline_names_by_path[path] = name
        require(baseline.get("schema") == 1, "baseline token schema mismatch")
        require(baseline.get("raw_sha256") == edit["before_sha256"], "before_sha256 mismatch")
        source_tokens = baseline.get("tokens")
        planned_tokens = edit.get("tokens")
        require(isinstance(source_tokens, list), "baseline tokens missing")
        require(isinstance(planned_tokens, list), "planned tokens missing")
        require(len(planned_tokens) == len(source_tokens), "token count changed")

        for index, (source, planned) in enumerate(zip(source_tokens, planned_tokens)):
            require(isinstance(source.get("type"), int), "baseline token type missing")
            require(isinstance(source.get("payload_u32"), int), "baseline token payload missing")
            require(planned.get("type") == source["type"], f"token type changed at {path}[{index}]")
            if index in changes:
                change = changes[index]
                require(source["type"] == 6, "target token must be a string")
                require(source.get("text") == change["old_string"], "baseline target string mismatch")
                require(planned == {"type": 6, "text": change["new_text"]}, "target text mismatch")
                previous = source_tokens[index - 1]
                key = "[name]" if index == 1 else "[explain]"
                require(previous.get("type") == 3 and previous.get("text") == key, "target section mismatch")
                changed_tokens += 1
            else:
                require(planned == {"type": source["type"], "payload_u32": source["payload_u32"]},
                        f"non-target payload changed at {path}[{index}]")
                preserved_tokens += 1
        require(set(changes) <= set(range(len(source_tokens))), "target token index out of range")

    require(observed_paths == set(changes_by_path), "plan paths do not match expected changes")
    require(changed_tokens == 4, "exactly four target text tokens must change")
    require(set(baseline_names_by_path) == observed_paths, "baseline token path mapping mismatch")
    return preserved_tokens, baseline_names_by_path


def validate_expected_receipt(manifest: dict, plan: dict, expected_receipt: dict,
                              preserved_tokens: int | None = None) -> dict:
    """Derive and cross-check the receipt contract from recipe inputs."""
    require(expected_receipt.get("schema") == 1, "expected receipt schema must be 1")
    require(expected_receipt.get("passed") is True, "expected receipt must be a passing contract")
    require(expected_receipt.get("classification") == manifest["classification"],
            "expected receipt classification mismatch")
    require(expected_receipt.get("baseline") == manifest["baseline"],
            "expected receipt baseline mismatch")
    require(expected_receipt.get("tool") == manifest["tool"],
            "expected receipt tool mismatch")
    require(expected_receipt.get("plan") == manifest["plan"],
            "expected receipt plan mismatch")
    require(expected_receipt.get("candidate") == manifest["expected_candidate"],
            "expected receipt candidate mismatch")

    expected_paths = {edit["path"] for edit in plan["edits"]}
    manifest_changes = [
        {"path": change["path"], "token_index": change["token_index"],
         "text": change["new_text"]}
        for change in manifest["expected_changes"]
    ]
    require(expected_receipt.get("changes") == manifest_changes,
            "expected receipt changes do not match manifest")

    verification = expected_receipt.get("verification")
    require(isinstance(verification, dict), "expected receipt verification must be an object")
    changed_entries = require_int(verification.get("changed_entries"),
                                  "expected changed entry count must be an integer")
    changed_text_tokens = require_int(verification.get("changed_text_tokens"),
                                      "expected changed text token count must be an integer")
    unchanged_entries = require_int(verification.get("unchanged_entries_verified"),
                                    "expected unchanged entry count must be an integer")
    require(changed_entries == len(expected_paths),
            "expected changed entry count does not match plan paths")
    require(changed_text_tokens == len(manifest_changes),
            "expected changed text token count does not match manifest")
    if "appended_strings" in verification:
        require(verification["appended_strings"] == changed_text_tokens,
                "expected appended string count mismatch")
    require(changed_text_tokens > 0, "expected changed text token count must be positive")
    require(unchanged_entries > 0, "expected unchanged entry count must be positive")
    if preserved_tokens is not None:
        require(verification.get("preserved_non_target_tokens") == preserved_tokens,
                "expected preserved token count mismatch")

    expected_candidate = manifest["expected_candidate"]
    require_sha256(expected_candidate.get("sha256"), "expected candidate digest is invalid")
    require_int(expected_candidate.get("bytes"), "expected candidate byte count is invalid")
    return {
        "paths": expected_paths,
        "changed_entries": changed_entries,
        "appended_strings": changed_text_tokens,
        "unchanged_entries_verified": unchanged_entries,
    }


def validate_tool_receipt(receipt: dict, label: str, manifest: dict, plan: dict,
                          contract: dict, candidate_sha256: str,
                          candidate_bytes: int) -> None:
    require(isinstance(receipt, dict), f"{label} receipt must be an object")
    require({edit["path"] for edit in plan["edits"]} == contract["paths"],
            f"{label} receipt plan path set mismatch")
    require_int(receipt.get("schema"), f"{label} receipt schema must be an integer")
    require(receipt["schema"] == 2, f"{label} receipt schema must be 2")
    require(receipt.get("input_sha256") == manifest["baseline"]["sha256"],
            f"{label} receipt input digest mismatch")
    require_sha256(receipt.get("output_sha256"), f"{label} receipt output digest is invalid")
    require(receipt["output_sha256"] == candidate_sha256,
            f"{label} receipt output digest mismatch")
    require_int(receipt.get("output_bytes"), f"{label} receipt output byte count is invalid")
    require(receipt["output_bytes"] == candidate_bytes,
            f"{label} receipt output byte count mismatch")
    for field in ("changed_entries", "appended_strings", "unchanged_entries_verified"):
        require_int(receipt.get(field), f"{label} receipt {field} must be an integer")
        require(receipt[field] == contract[field], f"{label} receipt {field} mismatch")

    entry_hashes = receipt.get("entry_sha256")
    require(isinstance(entry_hashes, dict), f"{label} receipt entry_sha256 must be an object")
    require(set(entry_hashes) == contract["paths"],
            f"{label} receipt entry path set mismatch")
    for path, digest in entry_hashes.items():
        require_sha256(digest, f"{label} receipt entry digest is invalid: {path}")

    manifest_entry_hashes = manifest.get("expected_entry_sha256")
    if manifest_entry_hashes is None:
        expected_candidate = manifest.get("expected_candidate")
        if isinstance(expected_candidate, dict):
            manifest_entry_hashes = expected_candidate.get("entry_sha256")
    if manifest_entry_hashes is not None:
        require(isinstance(manifest_entry_hashes, dict),
                "manifest expected_entry_sha256 must be an object")
        require(set(manifest_entry_hashes) == set(entry_hashes),
                "manifest expected entry path set mismatch")
        for path, expected_digest in manifest_entry_hashes.items():
            require_sha256(expected_digest, f"manifest expected entry digest is invalid: {path}")
            require(entry_hashes[path] == expected_digest,
                    f"{label} receipt entry digest does not match manifest: {path}")

    verification = receipt.get("verification")
    require(isinstance(verification, str) and bool(verification.strip()),
            f"{label} receipt verification must be a non-empty string")


def validate_tool_receipt_pair(patch_receipt: dict, verify_receipt: dict,
                               manifest: dict, plan: dict, contract: dict,
                               candidate_sha256: str, candidate_bytes: int) -> None:
    for label, receipt in (("patch", patch_receipt), ("verify", verify_receipt)):
        validate_tool_receipt(
            receipt, label, manifest, plan, contract, candidate_sha256, candidate_bytes
        )
    require(patch_receipt == verify_receipt, "patch and verify receipts differ")


def validate_tokens(before: dict, after: dict, changes: dict) -> None:
    require(before.get("path") == after.get("path"), "candidate token path changed")
    require(len(before["tokens"]) == len(after["tokens"]), "candidate token count changed")
    for index, (old, new) in enumerate(zip(before["tokens"], after["tokens"])):
        require(new.get("type") == old.get("type"), f"candidate token type changed at index {index}")
        if index in changes:
            require(new.get("type") == 6 and new.get("text") == changes[index]["new_text"],
                    f"candidate target text mismatch at index {index}")
        else:
            require(new.get("payload_u32") == old.get("payload_u32"),
                    f"candidate non-target payload changed at index {index}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, type=Path)
    parser.add_argument("--pvfcli", required=True, type=Path)
    parser.add_argument("--tokens-dir", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()

    manifest = load_json(MANIFEST_PATH)
    plan_path = HERE / manifest["plan"]["file"]
    plan = load_json(plan_path)
    require(len(plan_path.read_bytes()) == manifest["plan"]["bytes"], "plan byte count mismatch")
    require(sha256(plan_path) == manifest["plan"]["sha256"], "plan digest mismatch")
    require(args.base.stat().st_size == manifest["baseline"]["bytes"], "baseline byte count mismatch")
    require(sha256(args.base) == manifest["baseline"]["sha256"], "baseline digest mismatch")
    require(args.pvfcli.stat().st_size == manifest["tool"]["bytes"], "tool byte count mismatch")
    require(sha256(args.pvfcli) == manifest["tool"]["sha256"], "tool digest mismatch")
    preserved_tokens, baseline_names_by_path = validate_recipe(manifest, plan, args.tokens_dir)
    expected_receipt = load_json(EXPECTED_RECEIPT_PATH)
    contract = validate_expected_receipt(manifest, plan, expected_receipt, preserved_tokens)

    output_dir = args.output_dir.resolve()
    require(output_dir.is_dir(), "output directory must already exist")
    candidate = output_dir / "Script.pvf"
    require(not os.path.lexists(candidate), "candidate already exists; refusing to overwrite")
    evidence_names = [
        "patch.receipt.json", "generate-patch.stdout", "generate-patch.stderr",
        "verify.receipt.json", "generate-verify.stdout", "generate-verify.stderr",
        "candidate-receipt.json",
    ]
    for baseline_name in baseline_names_by_path.values():
        stem = baseline_name.removesuffix(".tokens.json").removeprefix("base-")
        evidence_names.extend([
            f"candidate-{stem}.tokens.json",
            f"candidate-{stem}.txt",
            f"read-{stem}-tokens.stdout",
            f"read-{stem}-tokens.stderr",
            f"read-{stem}-text.stdout",
            f"read-{stem}-text.stderr",
        ])
    existing = [name for name in evidence_names if os.path.lexists(output_dir / name)]
    require(not existing, "evidence target already exists: " + ", ".join(existing))

    patch_receipt = run_json(
        [str(args.pvfcli), "patch", "--pvf", str(args.base), "--plan", str(plan_path),
         "--out", str(candidate)],
        output_dir / "generate-patch.stdout",
        output_dir / "generate-patch.stderr",
    )
    candidate_digest = sha256(candidate)
    candidate_bytes = candidate.stat().st_size
    expected_candidate = manifest["expected_candidate"]
    require(candidate_digest == expected_candidate["sha256"], "candidate digest mismatch")
    require(candidate_bytes == expected_candidate["bytes"], "candidate byte count mismatch")
    validate_tool_receipt(
        patch_receipt, "patch", manifest, plan, contract, candidate_digest, candidate_bytes
    )
    (output_dir / "patch.receipt.json").write_text(
        json.dumps(patch_receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    verify_receipt = run_json(
        [str(args.pvfcli), "verify", "--pvf", str(args.base), "--plan", str(plan_path),
         "--out", str(candidate)],
        output_dir / "generate-verify.stdout",
        output_dir / "generate-verify.stderr",
    )
    candidate_digest_after_verify = sha256(candidate)
    candidate_bytes_after_verify = candidate.stat().st_size
    require(candidate_digest_after_verify == candidate_digest,
            "candidate changed while verify was running")
    require(candidate_bytes_after_verify == candidate_bytes,
            "candidate byte count changed while verify was running")
    validate_tool_receipt_pair(
        patch_receipt, verify_receipt, manifest, plan, contract,
        candidate_digest_after_verify, candidate_bytes_after_verify,
    )
    (output_dir / "verify.receipt.json").write_text(
        json.dumps(verify_receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    changes_by_path = {}
    for change in manifest["expected_changes"]:
        changes_by_path.setdefault(change["path"], {})[change["token_index"]] = change
    readbacks = []
    for edit in plan["edits"]:
        path = edit["path"]
        baseline_name = baseline_names_by_path[path]
        stem = baseline_name.removesuffix(".tokens.json").removeprefix("base-")
        token_out = output_dir / f"candidate-{stem}.tokens.json"
        text_out = output_dir / f"candidate-{stem}.txt"
        run_success(
            [str(args.pvfcli), "read", "--pvf", str(candidate), "--path", path,
             "--format", "tokens", "--out", str(token_out), "--json"],
            output_dir / f"read-{stem}-tokens.stdout",
            output_dir / f"read-{stem}-tokens.stderr",
        )
        run_success(
            [str(args.pvfcli), "read", "--pvf", str(candidate), "--path", path,
             "--format", "text", "--out", str(text_out), "--json"],
            output_dir / f"read-{stem}-text.stdout",
            output_dir / f"read-{stem}-text.stderr",
        )

        baseline = load_json(args.tokens_dir / baseline_name)
        candidate_tokens = load_json(token_out)
        validate_tokens(baseline, candidate_tokens, changes_by_path[path])
        text_bytes = text_out.read_bytes()
        text = text_bytes.decode("utf-8")
        for change in changes_by_path[path].values():
            require(text.count(change["new_text"]) == 1,
                    f"text readback mismatch at {path}[{change['token_index']}]")
        readbacks.append({
            "path": path,
            "tokens_sha256": sha256(token_out),
            "tokens_bytes": token_out.stat().st_size,
            "text_sha256": sha256(text_out),
            "text_bytes": len(text_bytes),
            "token_count": len(candidate_tokens["tokens"]),
            "changed_token_indices": sorted(changes_by_path[path]),
            "all_text_targets_present_once": True,
        })

    require(sha256(candidate) == candidate_digest, "candidate changed during readback")
    require(candidate.stat().st_size == candidate_bytes,
            "candidate byte count changed during readback")

    receipt = {
        "schema": 1,
        "passed": True,
        "classification": "群服内容",
        "candidate": {"sha256": candidate_digest, "bytes": candidate_bytes},
        "baseline": manifest["baseline"],
        "tool": {"sha256": manifest["tool"]["sha256"], "bytes": manifest["tool"]["bytes"]},
        "plan": manifest["plan"],
        "changed_entries": contract["changed_entries"],
        "changed_text_tokens": contract["appended_strings"],
        "preserved_non_target_tokens": preserved_tokens,
        "archive_unchanged_entries_verified_by_tool": True,
        "tool_receipt_validation": {
            "schema": patch_receipt["schema"],
            "input_sha256": patch_receipt["input_sha256"],
            "output_sha256": patch_receipt["output_sha256"],
            "output_bytes": patch_receipt["output_bytes"],
            "changed_entries": patch_receipt["changed_entries"],
            "appended_strings": patch_receipt["appended_strings"],
            "unchanged_entries_verified": patch_receipt["unchanged_entries_verified"],
            "entry_sha256": patch_receipt["entry_sha256"],
            "verification": patch_receipt["verification"],
            "patch_and_verify_receipts_identical": True,
        },
        "token_and_text_readbacks": readbacks,
        "patch_verification": patch_receipt["verification"],
        "verify_verification": verify_receipt["verification"],
        "deployed": False,
        "published": False,
        "game_acceptance": False,
    }
    (output_dir / "candidate-receipt.json").write_text(
        json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(json.dumps({
        "passed": True,
        "candidate_sha256": candidate_digest,
        "candidate_bytes": candidate_bytes,
        "changed_entries": contract["changed_entries"],
        "changed_text_tokens": contract["appended_strings"],
        "preserved_non_target_tokens": preserved_tokens,
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        raise SystemExit(1)
