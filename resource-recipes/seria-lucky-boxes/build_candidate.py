#!/usr/bin/env python3
"""Build the three Seria lucky-box PVF changes from a pinned cumulative archive.

This recipe creates new files only. It never replaces the input PVF and never
deploys, publishes, or generates resource indexes.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import struct
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
MANIFEST_PATH = HERE / "manifest.json"


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def load_json(path: Path):
    return json.loads(path.read_text("utf-8"))


def write_json(path: Path, value) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", "utf-8")


def token_bytes(token: dict) -> bytes:
    return bytes([int(token["type"])]) + struct.pack("<I", int(token["payload_u32"]))


def tokens_bytes(tokens: list[dict]) -> bytes:
    return b"".join(token_bytes(token) for token in tokens)


def run_capture(command: list[str], stdout_path: Path, stderr_path: Path) -> dict:
    result = subprocess.run(
        command,
        capture_output=True,
        timeout=900,
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    stdout_path.write_bytes(result.stdout)
    stderr_path.write_bytes(result.stderr)
    if result.returncode != 0:
        detail = result.stderr.decode("utf-8", "replace").strip()
        raise RuntimeError(
            "command failed (%d): %s: %s"
            % (result.returncode, " ".join(command), detail)
        )
    try:
        return json.loads(result.stdout.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise RuntimeError("command returned non-JSON stdout: %s" % exc) from exc


def pvf_export_tokens(
    pvf: Path,
    tool: Path,
    paths: list[str],
    output_dir: Path,
    log_prefix: str,
) -> dict[str, dict]:
    output_dir.mkdir(parents=True, exist_ok=True)
    command = [
        str(tool),
        "export",
        "--pvf",
        str(pvf),
        *sum((["--path", path] for path in paths), []),
        "--format",
        "tokens",
        "--out",
        str(output_dir),
        "--json",
    ]
    result = run_capture(
        command,
        output_dir.parent / (log_prefix + ".export.stdout"),
        output_dir.parent / (log_prefix + ".export.stderr"),
    )
    expected = sorted(path + ".tokens.json" for path in paths)
    if sorted(result.get("results", [])) != expected or result.get("truncated"):
        raise RuntimeError("token export result mismatch: %r" % result)
    docs = {}
    for path in paths:
        doc_path = output_dir / (path + ".tokens.json")
        docs[path] = load_json(doc_path)
    return docs


def locate_sections(tokens: list[dict], name: str) -> list[tuple[int, int]]:
    name = name[1:-1] if name.startswith("[") and name.endswith("]") else name
    open_name = "[" + name + "]"
    close_name = "[/" + name + "]"
    starts = []
    pairs = []
    for index, token in enumerate(tokens):
        if token.get("type") == 3 and token.get("text") == open_name:
            starts.append(index)
        elif token.get("type") == 3 and token.get("text") == close_name:
            if not starts:
                raise RuntimeError("section close without open: " + name)
            pairs.append((starts.pop(), index))
    if starts or len(pairs) != 3:
        raise RuntimeError("expected three complete %s sections, got %d" % (name, len(pairs)))
    return pairs


def patch_tokens(tokens: list[dict]) -> list[dict]:
    plan_tokens = []
    for token in tokens:
        if "payload_u32" not in token:
            raise RuntimeError("token export lacks payload_u32")
        plan_tokens.append(
            {"type": int(token["type"]), "payload_u32": int(token["payload_u32"])}
        )
    return plan_tokens


def add_token(value: int) -> dict:
    return {"Type": 0, "Value": int(value)}


def str_token(value: str) -> dict:
    return {"Type": 6, "Text": value}


def tag_token(value: str) -> dict:
    return {"Type": 3, "Text": value}


def expected_new_item_tokens(spec: dict) -> list[dict]:
    tokens = [
        tag_token("[name]"), str_token(spec["name"]),
        tag_token("[explain]"), str_token(spec["explain"]),
        tag_token("[grade]"), add_token(spec["grade"]),
        tag_token("[rarity]"), add_token(spec["rarity"]),
        tag_token("[usable job]"), str_token("[all]"), tag_token("[/usable job]"),
        tag_token("[attach type]"), str_token("[trade]"),
        tag_token("[minimum level]"), add_token(spec["minimumLevel"]),
        tag_token("[icon]"), str_token(spec["iconImage"]), add_token(spec["iconIndex"]),
        tag_token("[stackable type]"), str_token("[booster selection]"), add_token(0),
        tag_token("[move wav]"), str_token(spec["moveWav"]),
        tag_token("[booster category num]"), add_token(0),
        tag_token("[booster selection num]"), add_token(spec["selectionCount"]),
        tag_token("[booster select category]"), add_token(0), add_token(0),
        tag_token("[stackable]"),
    ]
    for row in spec["contents"]:
        tokens.extend([add_token(row["itemId"]), add_token(1)])
    tokens.extend([
        tag_token("[/stackable]"), tag_token("[/booster select category]"),
    ])
    return tokens


def equivalent_token(want: dict, got: dict) -> bool:
    if int(want["Type"]) != int(got["type"]):
        return False
    if int(want["Type"]) == 3 or int(want["Type"]) == 6:
        return want["Text"] == got.get("text")
    if int(want["Type"]) == 0:
        return int(want["Value"]) == int(got.get("payload_u32", -1))
    raise RuntimeError("verification helper does not support token type %d" % want["Type"])


def contains_contiguous(values: list[int], needle: list[int]) -> bool:
    if not needle or len(needle) > len(values):
        return False
    width = len(needle)
    return any(values[index:index + width] == needle for index in range(len(values) - width + 1))


def verify_final(
    manifest: dict,
    tool: Path,
    candidate: Path,
    output: Path,
    *,
    suffix: str,
) -> dict:
    readback = output / ("readback-" + suffix)
    changed_paths = [
        manifest["blessedBox"]["path"],
        manifest["stackableList"]["path"],
        manifest["newItem"]["path"],
    ]
    docs = pvf_export_tokens(
        candidate, tool, changed_paths, readback, "final-" + suffix
    )
    info = run_capture(
        [str(tool), "info", "--pvf", str(candidate), "--json"],
        readback / "final-info.stdout",
        readback / "final-info.stderr",
    )
    expected_files = manifest["baseline"]["archiveFiles"] + 1
    if info["file_count"] != expected_files or info["checksum"] != sha256_file(candidate):
        raise RuntimeError("final archive info mismatch: %r" % info)

    expected_box = expected_box_tokens = load_json(
        output / "generated-box-tokens.json"
    )
    if "tokens" in expected_box:
        expected_box = expected_box["tokens"]
    got_box = docs[manifest["blessedBox"]["path"]]["tokens"]
    if [(
        int(t["type"]),
        int(t["payload_u32"]),
    ) for t in got_box] != [(
        int(t["type"]),
        int(t["payload_u32"]),
    ) for t in expected_box]:
        raise RuntimeError("blessed-box token readback differs from the frozen plan")

    expected_item = load_json(output / "generated-item-tokens.json")["append_tokens"]
    got_item = docs[manifest["newItem"]["path"]]["tokens"]
    if len(got_item) != len(expected_item):
        raise RuntimeError("new item token count mismatch")
    for index, (want, got) in enumerate(zip(expected_item, got_item)):
        if not equivalent_token(want, got):
            raise RuntimeError("new item token mismatch at %d" % index)

    source_list = pvf_export_tokens(
        manifest["_base"], tool, [manifest["stackableList"]["path"]],
        output / ("base-list-" + suffix), "base-list-" + suffix
    )[manifest["stackableList"]["path"]]["tokens"]
    final_list = docs[manifest["stackableList"]["path"]]["tokens"]
    if [(
        int(t["type"]),
        int(t["payload_u32"]),
    ) for t in source_list] != [(
        int(t["type"]),
        int(t["payload_u32"]),
    ) for t in final_list[:-2]]:
        raise RuntimeError("stackable.lst original prefix changed")
    tail = final_list[-2:]
    if tail[0].get("type") != 0 or tail[0].get("payload_u32") != manifest["newItem"]["itemId"]:
        raise RuntimeError("stackable.lst new item ID missing at exact append position")
    if tail[1].get("type") != 6 or tail[1].get("text") != manifest["newItem"]["listValue"]:
        raise RuntimeError("stackable.lst new path missing at exact append position")

    box_sections = locate_sections(got_box, "int data")
    first_two = [
        hashlib.sha256(tokens_bytes(got_box[start + 1:end])).hexdigest()
        for start, end in box_sections[:2]
    ]
    if first_two != manifest["blessedBox"]["sectionTokenSHA256"][:2]:
        raise RuntimeError("first two lucky-box reward slots changed")
    tail_values = [
        int(t["payload_u32"])
        for t in got_box[box_sections[2][1] - 12:box_sections[2][1]]
    ]
    expected_tail = []
    for row in manifest["blessedBox"]["appendedRewards"]:
        expected_tail.extend([row["itemId"], row["weight"], row["count"], row["flag"]])
    if tail_values != expected_tail:
        raise RuntimeError("third reward slot append readback mismatch: %r" % tail_values)

    return {
        "passed": True,
        "candidate": str(candidate),
        "candidateSHA256": sha256_file(candidate),
        "bytes": candidate.stat().st_size,
        "archiveFiles": info["file_count"],
        "changedPaths": sorted(changed_paths),
        "newItemId": manifest["newItem"]["itemId"],
        "newItemPath": manifest["newItem"]["path"],
        "selectionItemIds": [row["itemId"] for row in manifest["newItem"]["contents"]],
        "blessedRewardTail": [
            manifest["blessedBox"]["appendedRewards"],
        ],
        "firstTwoLuckySlotsPreserved": True,
        "stackableListOriginalPrefixPreserved": True,
        "tokenReadbackExact": True,
        "indexesGenerated": False,
        "deployed": False,
        "published": False,
        "gameAcceptance": False,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--tool", type=Path, required=True)
    parser.add_argument("--addition-tool", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    manifest = load_json(MANIFEST_PATH)
    if sha256_file(args.base) != manifest["baseline"]["sha256"] or args.base.stat().st_size != manifest["baseline"]["bytes"]:
        raise RuntimeError("baseline PVF identity mismatch")
    if sha256_file(args.tool) != manifest["tools"]["pvfCli"]["sha256"]:
        raise RuntimeError("PVF-CLI identity mismatch")
    if sha256_file(args.addition_tool) != manifest["tools"]["privateAdditions"]["sha256"]:
        raise RuntimeError("private additions tool identity mismatch")
    if args.output.exists():
        raise RuntimeError("output directory must not exist")

    args.output.mkdir(parents=False)
    source = pvf_export_tokens(
        args.base,
        args.tool,
        [manifest["blessedBox"]["path"], manifest["stackableList"]["path"]],
        args.output / "source-tokens",
        "source",
    )
    box_doc = source[manifest["blessedBox"]["path"]]
    list_doc = source[manifest["stackableList"]["path"]]
    if (
        box_doc["raw_sha256"] != manifest["blessedBox"]["rawSHA256"]
        or len(box_doc["tokens"]) != manifest["blessedBox"]["tokenCount"]
        or list_doc["raw_sha256"] != manifest["stackableList"]["rawSHA256"]
        or len(list_doc["tokens"]) != manifest["stackableList"]["tokenCount"]
    ):
        raise RuntimeError("reviewed source token identities changed")

    box_sections = locate_sections(box_doc["tokens"], "int data")
    section_hashes = [
        hashlib.sha256(tokens_bytes(box_doc["tokens"][start + 1:end])).hexdigest()
        for start, end in box_sections
    ]
    if section_hashes != manifest["blessedBox"]["sectionTokenSHA256"]:
        raise RuntimeError("lucky-box reward slot fingerprints changed")

    third = box_doc["tokens"][box_sections[2][0] + 1:box_sections[2][1]]
    third_values = [int(t["payload_u32"]) for t in third]
    reference_rows = []
    for row in manifest["blessedBox"]["referenceWeights"]:
        reference_rows.extend([row["itemId"], row["weight"], row["count"], row["flag"]])
    if not contains_contiguous(third_values, reference_rows):
        raise RuntimeError("reviewed reference weights are absent from the third slot")

    existing_ids = set(third_values[0::4])
    inserted = []
    for row in manifest["blessedBox"]["appendedRewards"]:
        if row["itemId"] in existing_ids:
            raise RuntimeError("reward already exists: %d" % row["itemId"])
        if row["weightSourceItemId"] == 490703089:
            expected_weight = manifest["blessedBox"]["referenceWeights"][0]["weight"]
        elif row["weightSourceItemId"] == 490703090:
            expected_weight = manifest["blessedBox"]["referenceWeights"][1]["weight"]
        else:
            raise RuntimeError("unknown weight source item")
        if row["weight"] != expected_weight:
            raise RuntimeError("manifest weight does not match its source")
        inserted.extend([row["itemId"], row["weight"], row["count"], row["flag"]])

    new_box_tokens = (
        box_doc["tokens"][:box_sections[2][1]]
        + [{"type": 0, "payload_u32": value} for value in inserted]
        + box_doc["tokens"][box_sections[2][1]:]
    )
    write_json(args.output / "generated-box-tokens.json", {"tokens": new_box_tokens})
    patch_plan = {
        "schema": 2,
        "input_sha256": manifest["baseline"]["sha256"],
        "edits": [{
            "path": manifest["blessedBox"]["path"],
            "data_type": 1,
            "before_sha256": box_doc["raw_sha256"],
            "tokens": patch_tokens(new_box_tokens),
        }],
    }
    write_json(args.output / "lucky-box-patch.plan.json", patch_plan)

    intermediate = args.output / "intermediate.pvf"
    patch_receipt = run_capture(
        [str(args.tool), "patch", "--pvf", str(args.base), "--plan", str(args.output / "lucky-box-patch.plan.json"), "--out", str(intermediate)],
        args.output / "patch.stdout", args.output / "patch.stderr",
    )
    verify_receipt = run_capture(
        [str(args.tool), "verify", "--pvf", str(args.base), "--plan", str(args.output / "lucky-box-patch.plan.json"), "--out", str(intermediate)],
        args.output / "verify.stdout", args.output / "verify.stderr",
    )
    for receipt in (patch_receipt, verify_receipt):
        if (
            receipt.get("input_sha256") != manifest["baseline"]["sha256"]
            or receipt.get("output_sha256") != sha256_file(intermediate)
            or receipt.get("changed_entries") != 1
            or receipt.get("unchanged_entries_verified") != manifest["baseline"]["archiveFiles"] - 1
        ):
            raise RuntimeError("standard PVF patch/verify preservation mismatch")

    # Ensure inventory ID mapping and source paths are unambiguous before adding.
    list_tokens = list_doc["tokens"]
    refs = {}
    if len(list_tokens) % 2:
        raise RuntimeError("stackable.lst is not an ID/path token stream")
    for index in range(0, len(list_tokens), 2):
        id_token, path_token = list_tokens[index:index + 2]
        if id_token.get("type") != 0 or path_token.get("type") != 6:
            raise RuntimeError("unexpected stackable.lst row shape")
        refs[int(id_token["payload_u32"])] = path_token["text"]
    all_rows = list(manifest["targetItems"]) + list(manifest["newItem"]["contents"])
    for row in all_rows:
        expected_path = row["sourcePath"][len("stackable/"):]
        if refs.get(row["itemId"]) != expected_path:
            raise RuntimeError("source item/path mismatch for %d" % row["itemId"])
    if manifest["newItem"]["itemId"] in refs or manifest["newItem"]["listValue"] in refs.values():
        raise RuntimeError("new item ID or path already exists")

    item_tokens = expected_new_item_tokens(manifest["newItem"])
    write_json(args.output / "generated-item-tokens.json", {"append_tokens": item_tokens})
    addition_plan = {
        "input_sha256": sha256_file(intermediate),
        "additions": [
            {
                "Path": manifest["newItem"]["path"],
                "TemplatePath": manifest["blessedBox"]["path"],
                "ReplaceTokens": True,
                "AppendTokens": item_tokens,
            },
            {
                "Path": manifest["stackableList"]["path"],
                "AppendToExisting": True,
                "AppendTokens": [
                    add_token(manifest["newItem"]["itemId"]),
                    str_token(manifest["newItem"]["listValue"]),
                ],
            },
        ],
    }
    write_json(args.output / "additions.plan.json", addition_plan)
    candidate = args.output / "Script.pvf"
    run_capture(
        [
            str(args.addition_tool), "--pvf", str(intermediate),
            "--plan", str(args.output / "additions.plan.json"),
            "--out", str(candidate),
            "--report", str(args.output / "additions.report.json"),
        ],
        args.output / "additions.stdout", args.output / "additions.stderr",
    )
    addition_receipt = load_json(args.output / "additions.report.json")
    if (
        addition_receipt.get("input_sha256") != sha256_file(intermediate)
        or addition_receipt.get("output_sha256") != sha256_file(candidate)
        or addition_receipt.get("added_paths") != [manifest["newItem"]["path"]]
        or addition_receipt.get("changed_paths") != [manifest["stackableList"]["path"]]
        or addition_receipt.get("unchanged_files_preserved") != manifest["baseline"]["archiveFiles"] - 1
    ):
        raise RuntimeError("private additions preservation mismatch")

    manifest["_base"] = str(args.base)
    readback = verify_final(manifest, args.tool, candidate, args.output, suffix="build")
    expected = manifest.get("expectedCandidate")
    if expected and (
        expected.get("sha256") != readback["candidateSHA256"]
        or expected.get("bytes") != readback["bytes"]
    ):
        raise RuntimeError("candidate differs from frozen expected identity")

    result = dict(readback)
    result.update({
        "baselineSHA256": manifest["baseline"]["sha256"],
        "intermediateSHA256": sha256_file(intermediate),
        "patchReceipt": patch_receipt,
        "verifyReceipt": verify_receipt,
        "additionReceipt": addition_receipt,
        "weights": {
            "referenceSilverSingle": 300,
            "referenceSilverSet": 10,
            "holyCloneSingle490702678": 300,
            "holyCloneSet490702679": 10,
            "dreamCompositorSelectionBox990032001": 300,
        },
        "knownLimit": "The two expired duplicate variants 490702922/490702923 are excluded from the selection. Their source entries are not modified; the other nine current, distinct synthesizers form the selectable set.",
    })
    write_json(args.output / "result.json", result)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("FAILED: %s" % exc, file=sys.stderr)
        raise
