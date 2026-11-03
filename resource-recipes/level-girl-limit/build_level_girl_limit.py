"""Build and verify the level-girl purchase-limit PVF candidate.

This recipe changes exactly one token in each of five existing NKPI type-1
documents: the string immediately after [purchase limit] changes from
"account" to "character". Every other token keeps its exported type and
payload_u32. The script creates a new evidence directory and never overwrites
an existing output. It can also verify an already-generated candidate without
running patch again; the source patch receipt and candidate are checked first.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from typing import Any


HERE = Path(__file__).resolve().parent
MANIFEST_PATH = HERE / "manifest.json"

TARGET_IDS = ("490009269", "490009270", "490009271", "490009274", "490009275")
EXPECTED_PATHS = tuple(f"stackable/490009001/chn_{item_id}.stk" for item_id in TARGET_IDS)
EXPECTED_TOKEN_FILES = tuple(f"chn_{item_id}.stk.tokens.json" for item_id in TARGET_IDS)
EXPECTED_CHANGES = 5
OLD_TEXT = "account"
NEW_TEXT = "character"
PURCHASE_LIMIT = "[purchase limit]"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_json(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as stream:
        return json.load(stream)


def write_json(path: Path, value: Any) -> None:
    text = json.dumps(value, ensure_ascii=False, indent=2) + "\n"
    path.write_text(text, encoding="utf-8", newline="\n")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def require_int(value: Any, message: str) -> int:
    require(type(value) is int, message)
    return value


def require_sha256(value: Any, message: str) -> str:
    require(
        isinstance(value, str)
        and len(value) == 64
        and all(character in "0123456789abcdef" for character in value),
        message,
    )
    return value


def require_file_identity(path: Path, identity: dict, label: str) -> None:
    require(path.is_file(), f"{label} is not a file: {path}")
    require(path.stat().st_size == require_int(identity.get("bytes"), f"{label} bytes missing"),
            f"{label} byte count mismatch")
    require(sha256(path) == require_sha256(identity.get("sha256"), f"{label} sha256 invalid"),
            f"{label} sha256 mismatch")


def token_identity(token: Any, label: str) -> dict:
    require(isinstance(token, dict), f"{label} token must be an object")
    token_type = require_int(token.get("type"), f"{label} type missing")
    payload = require_int(token.get("payload_u32"), f"{label} payload_u32 missing")
    require(0 <= token_type <= 6, f"{label} type out of range")
    require(0 <= payload <= 0xFFFFFFFF, f"{label} payload_u32 out of range")
    expected_keys = {"type", "payload_u32"}
    if token_type in (3, 6):
        require("text" in token and isinstance(token["text"], str),
                f"{label} exported text missing")
        expected_keys.add("text")
    require(set(token) == expected_keys, f"{label} has unexpected or missing fields")
    return {"type": token_type, "payload_u32": payload}


def planned_identity(token: Any, label: str) -> dict:
    require(isinstance(token, dict), f"{label} planned token must be an object")
    token_type = require_int(token.get("type"), f"{label} type missing")
    require(0 <= token_type <= 6, f"{label} type out of range")
    keys = set(token)
    if keys == {"type", "payload_u32"}:
        payload = require_int(token["payload_u32"], f"{label} payload_u32 missing")
        require(0 <= payload <= 0xFFFFFFFF, f"{label} payload_u32 out of range")
    elif keys == {"type", "text"}:
        require(token_type == 6 and isinstance(token["text"], str),
                f"{label} text replacement must be type 6")
        payload = None
    else:
        raise ValueError(f"{label} has unexpected planned fields")
    return {"type": token_type, "payload_u32": payload}


def validate_manifest(manifest: Any) -> list[dict]:
    require(isinstance(manifest, dict), "manifest must be an object")
    require(set(manifest) == {
        "schema", "classification", "task", "baseline", "tool", "plan",
        "expected_entries", "expected_changes", "requirements", "scope",
    }, "manifest has unexpected or missing fields")
    require(manifest["schema"] == 1, "manifest schema must be 1")
    require(manifest["classification"] == "功能修复", "classification mismatch")
    require(manifest["task"] == "20261102-level-girl-limit", "task mismatch")

    baseline = manifest["baseline"]
    require(isinstance(baseline, dict) and set(baseline) == {"sha256", "bytes"},
            "baseline identity fields mismatch")
    require_sha256(baseline["sha256"], "baseline sha256 invalid")
    require_int(baseline["bytes"], "baseline bytes invalid")

    tool = manifest["tool"]
    require(isinstance(tool, dict)
            and set(tool) == {"name", "sha256", "bytes", "patch_schema"},
            "tool identity fields mismatch")
    require(isinstance(tool["name"], str) and bool(tool["name"]), "tool name missing")
    require_sha256(tool["sha256"], "tool sha256 invalid")
    require_int(tool["bytes"], "tool bytes invalid")
    require(tool["patch_schema"] == 2, "tool patch schema must be 2")

    plan = manifest["plan"]
    require(isinstance(plan, dict) and set(plan) == {"file", "sha256", "bytes"},
            "plan identity fields mismatch")
    require(plan["file"] == "level-girl-limit.schema2.json", "plan filename mismatch")
    require_sha256(plan["sha256"], "plan sha256 invalid")
    require_int(plan["bytes"], "plan bytes invalid")

    expected_entries = manifest["expected_entries"]
    require(isinstance(expected_entries, list) and len(expected_entries) == EXPECTED_CHANGES,
            "manifest must contain exactly five expected entries")
    require([entry.get("path") for entry in expected_entries] == list(EXPECTED_PATHS),
            "manifest expected path order mismatch")
    require([entry.get("token_file") for entry in expected_entries] == list(EXPECTED_TOKEN_FILES),
            "manifest token filenames mismatch")
    for index, entry in enumerate(expected_entries):
        require(isinstance(entry, dict)
                and set(entry) == {"path", "token_file", "token_index", "raw_sha256"},
                f"expected entry {index} fields mismatch")
        require_int(entry["token_index"], f"expected entry {index} token index invalid")
        require_sha256(entry["raw_sha256"], f"expected entry {index} raw sha256 invalid")

    expected_changes = manifest["expected_changes"]
    require(isinstance(expected_changes, list) and len(expected_changes) == EXPECTED_CHANGES,
            "manifest must contain exactly five expected changes")
    for index, (change, entry) in enumerate(zip(expected_changes, expected_entries)):
        require(isinstance(change, dict)
                and set(change) == {"path", "token_index", "from_text", "to_text"},
                f"expected change {index} fields mismatch")
        require(change["path"] == entry["path"], f"expected change {index} path mismatch")
        require(change["token_index"] == entry["token_index"],
                f"expected change {index} token index mismatch")
        require(change["from_text"] == OLD_TEXT and change["to_text"] == NEW_TEXT,
                f"expected change {index} text contract mismatch")

    requirements = manifest["requirements"]
    require(isinstance(requirements, dict), "requirements must be an object")
    require(requirements.get("changed_entries") == EXPECTED_CHANGES,
            "changed entry requirement mismatch")
    require(requirements.get("changed_text_tokens") == EXPECTED_CHANGES,
            "changed text token requirement mismatch")
    require(requirements.get("unchanged_entries_verified_minimum") == 1,
            "unchanged entry minimum mismatch")
    for key in (
        "preserve_all_other_token_types_and_payload_bits",
        "preserve_purchase_limit_context",
        "preserve_all_unchanged_archive_entries",
        "no_new_paths",
        "reject_existing_output",
    ):
        require(requirements.get(key) is True, f"required flag is not true: {key}")

    scope = manifest["scope"]
    require(isinstance(scope, dict), "scope must be an object")
    require(scope.get("gameplay_changes") is True, "purchase policy is a gameplay change")
    for key in (
        "go_changes", "deployment_changes",
        "resource_publish_changes", "index_build_by_builder", "network_operations",
    ):
        require(scope.get(key) is False, f"scope flag must be false: {key}")
    return expected_entries


def expected_appended_strings(manifest: dict) -> int:
    """Return the string-pool append count required by the change set.

    A text token replacement adds a string to the pool only once. Five edits
    whose target text is the same ``character`` string therefore append one
    pool value, even though they change five entries and five text tokens.
    """

    targets = {change["to_text"] for change in manifest["expected_changes"]}
    require(bool(targets), "no replacement text strings were planned")
    return len(targets)


def load_token_documents(token_dir: Path, expected_entries: list[dict]) -> dict[str, dict]:
    require(token_dir.is_dir(), f"tokens directory does not exist: {token_dir}")
    files = sorted(
        path for path in token_dir.rglob("*.tokens.json") if path.is_file()
    )
    require(len(files) == EXPECTED_CHANGES,
            "tokens directory must contain exactly five .tokens.json files "
            f"recursively, found {len(files)}")
    require({path.name for path in files} == set(EXPECTED_TOKEN_FILES),
            "tokens directory filename set mismatch")

    entries_by_file = {entry["token_file"]: entry for entry in expected_entries}
    documents: dict[str, dict] = {}
    for path in files:
        expected = entries_by_file[path.name]
        document = load_json(path)
        require(isinstance(document, dict), f"token export is not an object: {path.name}")
        require(set(document) == {"schema", "path", "raw_sha256", "tokens"},
                f"token export fields mismatch: {path.name}")
        require(document["schema"] == 1, f"token export schema mismatch: {path.name}")
        require(document["path"] == expected["path"], f"token export path mismatch: {path.name}")
        require(require_sha256(document["raw_sha256"], f"raw sha256 invalid: {path.name}")
                == expected["raw_sha256"], f"baseline raw sha256 mismatch: {path.name}")
        require(isinstance(document["tokens"], list), f"baseline tokens missing: {path.name}")
        for token_index, token in enumerate(document["tokens"]):
            token_identity(token, f"{path.name}[{token_index}]")
        require(document["path"] not in documents, f"duplicate token export path: {document['path']}")
        documents[document["path"]] = document

    require(set(documents) == set(EXPECTED_PATHS), "token export path set mismatch")
    return documents


def validate_recipe(manifest: dict, expected_entries: list[dict],
                    token_dir: Path) -> tuple[Path, dict[str, dict], int]:
    plan_path = HERE / manifest["plan"]["file"]
    require(plan_path.is_file(), f"plan does not exist: {plan_path}")
    require(plan_path.stat().st_size == manifest["plan"]["bytes"], "plan byte count mismatch")
    require(sha256(plan_path) == manifest["plan"]["sha256"], "plan sha256 mismatch")
    plan = load_json(plan_path)
    require(isinstance(plan, dict) and set(plan) == {"schema", "input_sha256", "edits"},
            "plan fields mismatch")
    require(plan["schema"] == 2, "plan schema must be 2")
    require(plan["input_sha256"] == manifest["baseline"]["sha256"],
            "plan input sha256 mismatch")
    edits = plan["edits"]
    require(isinstance(edits, list) and len(edits) == EXPECTED_CHANGES,
            "plan must edit exactly five entries")
    require([edit.get("path") for edit in edits] == list(EXPECTED_PATHS),
            "plan path order mismatch")

    documents = load_token_documents(token_dir, expected_entries)
    preserved_tokens = 0
    for edit, expected in zip(edits, expected_entries):
        path = expected["path"]
        require(isinstance(edit, dict)
                and set(edit) == {"path", "data_type", "before_sha256", "tokens"},
                f"plan edit fields mismatch: {path}")
        require(edit["data_type"] == 1, f"edited entry data type must remain 1: {path}")
        require(edit["before_sha256"] == expected["raw_sha256"],
                f"plan before_sha256 mismatch: {path}")
        source_tokens = documents[path]["tokens"]
        planned_tokens = edit["tokens"]
        require(isinstance(planned_tokens, list) and len(planned_tokens) == len(source_tokens),
                f"planned token count mismatch: {path}")

        target_index = require_int(expected["token_index"], f"target index invalid: {path}")
        require(0 < target_index < len(source_tokens) - 2, f"target index out of range: {path}")
        source_target = source_tokens[target_index]
        require(source_target["type"] == 6 and source_target["text"] == OLD_TEXT,
                f"source target must be the account string: {path}")
        require(source_tokens[target_index - 1]["type"] == 3
                and source_tokens[target_index - 1]["text"] == PURCHASE_LIMIT,
                f"purchase limit marker mismatch: {path}")
        require(source_tokens[target_index + 1]["type"] == 6
                and source_tokens[target_index + 1]["text"] == "accumulate",
                f"accumulate token mismatch: {path}")
        require(source_tokens[target_index + 2]["type"] == 0
                and source_tokens[target_index + 2]["payload_u32"] == 1,
                f"purchase limit value mismatch: {path}")
        require([token.get("text") for token in source_tokens].count(OLD_TEXT) == 1,
                f"account string must occur exactly once: {path}")
        require([token.get("text") for token in source_tokens].count(PURCHASE_LIMIT) == 1,
                f"purchase limit marker must occur exactly once: {path}")

        for token_index, (source, planned) in enumerate(zip(source_tokens, planned_tokens)):
            planned_value = planned_identity(planned, f"{path}[{token_index}]")
            if token_index == target_index:
                require(planned == {"type": 6, "text": NEW_TEXT},
                        f"target replacement must be exactly character: {path}")
            else:
                require(planned_value == token_identity(source, f"{path}[{token_index}]"),
                        f"non-target token type/payload changed: {path}[{token_index}]")
                preserved_tokens += 1
    require(preserved_tokens > 0, "no preserved tokens were validated")
    return plan_path, documents, preserved_tokens


def run_success(command: list[str], stdout_path: Path, stderr_path: Path,
                label: str) -> subprocess.CompletedProcess:
    result = subprocess.run(
        command,
        capture_output=True,
        timeout=3600,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    stdout_path.write_bytes(result.stdout)
    stderr_path.write_bytes(result.stderr)
    require(result.returncode == 0, f"{label} failed; inspect {stderr_path.name}")
    return result


def run_json(command: list[str], stdout_path: Path, stderr_path: Path,
             label: str) -> dict:
    result = run_success(command, stdout_path, stderr_path, label)
    try:
        value = json.loads(result.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"{label} did not return UTF-8 JSON") from error
    require(isinstance(value, dict), f"{label} receipt must be an object")
    return value


def validate_tool_receipt(receipt: dict, label: str, manifest: dict,
                          candidate_path: Path, candidate_sha256: str,
                          candidate_bytes: int, appended_strings: int) -> None:
    require(require_int(receipt.get("schema"), f"{label} receipt schema invalid") == 2,
            f"{label} receipt schema must be 2")
    require(receipt.get("input_sha256") == manifest["baseline"]["sha256"],
            f"{label} input sha256 mismatch")
    require(receipt.get("output_sha256") == candidate_sha256,
            f"{label} output sha256 mismatch")
    require(require_int(receipt.get("output_bytes"), f"{label} output bytes invalid")
            == candidate_bytes, f"{label} output byte count mismatch")
    require(require_int(receipt.get("changed_entries"),
                        f"{label} changed_entries invalid") == EXPECTED_CHANGES,
            f"{label} changed_entries must be 5")
    require(require_int(receipt.get("appended_strings"),
                        f"{label} appended_strings invalid") == appended_strings,
            f"{label} appended_strings must equal the unique replacement count "
            f"({appended_strings})")
    require(require_int(receipt.get("unchanged_entries_verified"),
                        f"{label} unchanged_entries_verified invalid") > 0,
            f"{label} unchanged_entries_verified must be positive")

    entry_hashes = receipt.get("entry_sha256")
    require(isinstance(entry_hashes, dict), f"{label} entry_sha256 must be an object")
    require(set(entry_hashes) == set(EXPECTED_PATHS),
            f"{label} entry path set mismatch")
    for path, digest in entry_hashes.items():
        require_sha256(digest, f"{label} entry sha256 invalid: {path}")
    require(isinstance(receipt.get("verification"), str)
            and bool(receipt["verification"].strip()),
            f"{label} verification text missing")
    receipt_out = receipt.get("out")
    require(isinstance(receipt_out, str) and bool(receipt_out.strip()),
            f"{label} output path missing")
    require(os.path.normcase(str(Path(receipt_out).expanduser().resolve()))
            == os.path.normcase(str(candidate_path.resolve())),
            f"{label} output path does not identify the candidate")


def validate_candidate_tokens(document: dict, baseline: dict, change: dict,
                              candidate_entry_sha256: str) -> dict:
    require(isinstance(document, dict), f"candidate token export is not an object: {change['path']}")
    require(set(document) == {"schema", "path", "raw_sha256", "tokens"},
            f"candidate token export fields mismatch: {change['path']}")
    require(document["schema"] == 1, f"candidate token export schema mismatch: {change['path']}")
    require(document["path"] == change["path"], f"candidate token path mismatch: {change['path']}")
    require(document["raw_sha256"] == candidate_entry_sha256,
            f"candidate raw sha256 does not match entry receipt: {change['path']}")
    require(isinstance(document["tokens"], list), f"candidate tokens missing: {change['path']}")
    baseline_tokens = baseline["tokens"]
    candidate_tokens = document["tokens"]
    require(len(candidate_tokens) == len(baseline_tokens),
            f"candidate token count changed: {change['path']}")

    target_index = change["token_index"]
    changed_text = 0
    preserved_payloads = 0
    for token_index, (before, after) in enumerate(zip(baseline_tokens, candidate_tokens)):
        before_identity = token_identity(
            before, f"{change['path']} baseline[{token_index}]"
        )
        after_identity = token_identity(
            after, f"{change['path']} candidate[{token_index}]"
        )
        require(after_identity["type"] == before_identity["type"],
                f"candidate token type changed: {change['path']}[{token_index}]")
        if token_index == target_index:
            require(after_identity["type"] == 6 and after.get("text") == NEW_TEXT,
                    f"candidate target text must be character: {change['path']}")
            changed_text += 1
        else:
            require(after_identity["payload_u32"] == before_identity["payload_u32"],
                    f"candidate non-target payload changed: {change['path']}[{token_index}]")
            require(after.get("text") == before.get("text"),
                    f"candidate non-target text changed: {change['path']}[{token_index}]")
            preserved_payloads += 1
    require(changed_text == 1, f"candidate must change exactly one target text: {change['path']}")
    require(preserved_payloads == len(baseline_tokens) - 1,
            f"candidate preserved token count mismatch: {change['path']}")
    return {
        "changed_token_index": target_index,
        "changed_text": NEW_TEXT,
        "token_count": len(candidate_tokens),
        "preserved_non_target_tokens": preserved_payloads,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, type=Path)
    parser.add_argument("--pvfcli", required=True, type=Path)
    parser.add_argument("--tokens-dir", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument(
        "--verify-existing", type=Path,
        help="verify this existing candidate and do not run patch",
    )
    parser.add_argument(
        "--patch-receipt", type=Path,
        help="source patch.stdout for --verify-existing; defaults beside the candidate",
    )
    parser.add_argument(
        "--patch-stderr", type=Path,
        help="source patch.stderr for --verify-existing; defaults beside patch.stdout",
    )
    args = parser.parse_args()

    raw_output_dir = args.output_dir.expanduser()
    require(not os.path.lexists(raw_output_dir),
            f"output already exists; refusing to overwrite: {raw_output_dir}")

    manifest = load_json(MANIFEST_PATH)
    expected_entries = validate_manifest(manifest)
    appended_strings = expected_appended_strings(manifest)
    require_file_identity(args.base, manifest["baseline"], "baseline PVF")
    require_file_identity(args.pvfcli, manifest["tool"], "PVF-CLI")
    tokens_dir = args.tokens_dir.expanduser().resolve()
    plan_path, baseline_documents, preserved_tokens = validate_recipe(
        manifest, expected_entries, tokens_dir
    )

    output_dir = raw_output_dir.resolve()
    require(output_dir.parent.is_dir(), f"output parent does not exist: {output_dir.parent}")
    require(not os.path.lexists(output_dir),
            f"output appeared before creation; refusing to overwrite: {output_dir}")
    output_dir.mkdir()

    base_path = args.base.expanduser().resolve()
    cli_path = args.pvfcli.expanduser().resolve()
    patch_reused = args.verify_existing is not None
    if patch_reused:
        require(args.patch_receipt is not None or args.patch_stderr is None,
                "--patch-stderr requires --patch-receipt in verify-existing mode")
        candidate_path = args.verify_existing.expanduser().resolve()
        require(candidate_path.is_file(), f"existing candidate is not a file: {candidate_path}")
        source_patch_receipt = (
            args.patch_receipt.expanduser().resolve()
            if args.patch_receipt is not None
            else candidate_path.parent / "patch.stdout"
        )
        source_patch_stderr = (
            args.patch_stderr.expanduser().resolve()
            if args.patch_stderr is not None
            else source_patch_receipt.parent / "patch.stderr"
        )
        require(source_patch_receipt.is_file(),
                f"source patch receipt is not a file: {source_patch_receipt}")
        require(source_patch_stderr.is_file(),
                f"source patch stderr is not a file: {source_patch_stderr}")
        require(not source_patch_stderr.read_bytes(),
                f"source patch stderr is not empty: {source_patch_stderr}")
        patch_receipt = load_json(source_patch_receipt)
    else:
        require(args.patch_receipt is None and args.patch_stderr is None,
                "--patch-receipt/--patch-stderr require --verify-existing")
        candidate_path = output_dir / "Script.pvf"
        require(not os.path.lexists(candidate_path), "candidate already exists")

        patch_command = [
            str(cli_path), "patch", "--pvf", str(base_path), "--plan", str(plan_path),
            "--out", str(candidate_path),
        ]
        patch_receipt = run_json(
            patch_command,
            output_dir / "patch.stdout",
            output_dir / "patch.stderr",
            "patch",
        )
        source_patch_receipt = output_dir / "patch.stdout"
        source_patch_stderr = output_dir / "patch.stderr"
        require(not source_patch_stderr.read_bytes(), "patch stderr is not empty")

    candidate_sha256 = sha256(candidate_path)
    candidate_bytes = candidate_path.stat().st_size
    validate_tool_receipt(
        patch_receipt, "patch", manifest, candidate_path, candidate_sha256,
        candidate_bytes, appended_strings,
    )
    write_json(output_dir / "patch.receipt.json", patch_receipt)

    verify_command = [
        str(cli_path), "verify", "--pvf", str(base_path), "--plan", str(plan_path),
        "--out", str(candidate_path),
    ]
    verify_receipt = run_json(
        verify_command,
        output_dir / "verify.stdout",
        output_dir / "verify.stderr",
        "verify",
    )
    require(sha256(candidate_path) == candidate_sha256,
            "candidate sha256 changed while verify was running")
    require(candidate_path.stat().st_size == candidate_bytes,
            "candidate byte count changed while verify was running")
    validate_tool_receipt(
        verify_receipt, "verify", manifest, candidate_path, candidate_sha256,
        candidate_bytes, appended_strings,
    )
    require(patch_receipt == verify_receipt, "patch and verify receipts differ")
    write_json(output_dir / "verify.receipt.json", verify_receipt)

    changes_by_path = {
        change["path"]: change for change in manifest["expected_changes"]
    }
    readbacks = []
    for item_id, path in zip(TARGET_IDS, EXPECTED_PATHS):
        token_path = output_dir / f"candidate-{item_id}.tokens.json"
        read_result = run_success(
            [
                str(cli_path), "read", "--pvf", str(candidate_path),
                "--path", path, "--format", "tokens", "--out", str(token_path), "--json",
            ],
            output_dir / f"read-{item_id}-tokens.stdout",
            output_dir / f"read-{item_id}-tokens.stderr",
            f"read {item_id}",
        )
        require(bool(read_result.stdout or token_path.is_file()),
                f"read {item_id} produced no result")
        candidate_document = load_json(token_path)
        change = changes_by_path[path]
        readback = validate_candidate_tokens(
            candidate_document,
            baseline_documents[path],
            change,
            patch_receipt["entry_sha256"][path],
        )
        readback.update({
            "path": path,
            "baseline_raw_sha256": baseline_documents[path]["raw_sha256"],
            "candidate_raw_sha256": candidate_document["raw_sha256"],
            "tokens_sha256": sha256(token_path),
            "tokens_bytes": token_path.stat().st_size,
        })
        readbacks.append(readback)

    require(sha256(candidate_path) == candidate_sha256,
            "candidate sha256 changed during token readback")
    require(candidate_path.stat().st_size == candidate_bytes,
            "candidate byte count changed during token readback")
    require({readback["path"] for readback in readbacks} == set(EXPECTED_PATHS),
            "candidate readback path set mismatch")
    require(sum(readback["preserved_non_target_tokens"] for readback in readbacks)
            == preserved_tokens,
            "candidate non-target token total differs from the plan")

    receipt = {
        "schema": 1,
        "passed": True,
        "execution_mode": "verify_existing" if patch_reused else "build_and_verify",
        "patch_reused": patch_reused,
        "classification": manifest["classification"],
        "candidate": {
            "path": str(candidate_path),
            "sha256": candidate_sha256,
            "bytes": candidate_bytes,
        },
        "baseline": manifest["baseline"],
        "tool": manifest["tool"],
        "plan": manifest["plan"],
        "source_patch_receipt": str(source_patch_receipt),
        "baseline_tokens_directory": str(tokens_dir),
        "changed_entries": EXPECTED_CHANGES,
        "changed_text_tokens": EXPECTED_CHANGES,
        "unique_appended_strings": appended_strings,
        "preserved_non_target_tokens": preserved_tokens,
        "unchanged_entries_verified": patch_receipt["unchanged_entries_verified"],
        "entry_sha256": patch_receipt["entry_sha256"],
        "tool_receipt_validation": {
            "schema": patch_receipt["schema"],
            "input_sha256": patch_receipt["input_sha256"],
            "output_sha256": patch_receipt["output_sha256"],
            "output_bytes": patch_receipt["output_bytes"],
            "changed_entries": patch_receipt["changed_entries"],
            "appended_strings": patch_receipt["appended_strings"],
            "appended_strings_expected_unique": appended_strings,
            "unchanged_entries_verified": patch_receipt["unchanged_entries_verified"],
            "entry_path_set_exact": True,
            "candidate_out_exact": True,
            "patch_and_verify_receipts_identical": True,
            "verification": patch_receipt["verification"],
        },
        "token_readbacks": readbacks,
        "checks": {
            "actual_candidate_sha256_rehashed_after_verify_and_readback": True,
            "only_one_target_text_changed_per_entry": True,
            "all_non_target_token_types_and_payloads_preserved": True,
            "purchase_limit_accumulate_and_value_1_preserved": True,
            "unique_appended_strings_matches_plan": True,
            "candidate_file_not_mutated_by_verification": True,
        },
        "indexes_generated": False,
        "resource_publish_configuration_changed": False,
        "deployed": False,
        "published": False,
        "game_acceptance": False,
    }
    write_json(output_dir / "candidate-receipt.json", receipt)
    print(json.dumps({
        "passed": True,
        "candidate_sha256": candidate_sha256,
        "candidate_bytes": candidate_bytes,
        "changed_entries": EXPECTED_CHANGES,
        "changed_text_tokens": EXPECTED_CHANGES,
        "unique_appended_strings": appended_strings,
        "unchanged_entries_verified": patch_receipt["unchanged_entries_verified"],
        "preserved_non_target_tokens": preserved_tokens,
        "patch_reused": patch_reused,
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        raise SystemExit(1)
