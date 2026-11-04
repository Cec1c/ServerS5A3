"""Add explicit dungeon material rules to an independent PVF candidate."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

SECTION = "servers5a3 dungeon material drop"
TARGET = "etc/independent_drop.etc"
ITEM = 10163136
DUNGEONS = {
    310: "metrocenter/time_plaza/time_plaza.dgn",
    311: "metrocenter/canyon_of_beast/canyon_of_beast.dgn",
    312: "metrocenter/hideout_of_fear/hideout_of_fear.dgn",
    313: "metrocenter/gust_zone/gust_zone.dgn",
    314: "metrocenter/forest_of_redwitch/forest_of_redwitch.dgn",
    315: "metrocenter/special/special.dgn",
    316: "metrocenter/hell/hell.dgn",
    7539: "infernomode/inferno_metrocenter1.dgn",
}


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--tool", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    baseline = sha(args.base)
    if baseline != args.expected_sha256.lower():
        raise ValueError("baseline SHA256 differs from reviewed input")
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=False)

    def run(label, argv):
        result = subprocess.run([args.tool, *argv], capture_output=True, timeout=600,
                                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        (out / (label + ".stdout")).write_bytes(result.stdout)
        (out / (label + ".stderr")).write_bytes(result.stderr)
        if result.returncode:
            raise RuntimeError(label + " failed; see preserved logs")
        return result.stdout

    def read(label, path, fmt="text", pvf=None):
        target = out / (label + "." + ("json" if fmt == "tokens" else "txt"))
        run(label, ["read", "--pvf", str(pvf or args.base), "--path", path,
                    "--format", fmt, "--out", str(target)])
        text = target.read_text("utf-8-sig")
        return json.loads(text) if fmt == "tokens" else text

    dungeon_list = read("dungeon-list", "dungeon/dungeon.lst")
    mappings = {int(i): path.lower() for i, path in re.findall(r"(\d+)\s+`([^`]+)`", dungeon_list)}
    for item_id, path in DUNGEONS.items():
        if mappings.get(item_id) != path:
            raise ValueError(f"dungeon {item_id} mapping changed")
    stackable_list = read("stackable-list", "stackable/stackable.lst")
    mapping = {int(i): path.lower() for i, path in re.findall(r"(\d+)\s+`([^`]+)`", stackable_list)}
    if mapping.get(ITEM) != "10163001/10163136.stk":
        raise ValueError("material mapping changed")
    material = read("material", "stackable/" + mapping[ITEM])
    if not re.search(r"\[name\]\s*`次元光辉`", material) or not re.search(r"\[stackable type\]\s*`\[material\]`", material):
        raise ValueError("material identity or type changed")
    before = read("before", TARGET, "tokens")
    old = before["tokens"]
    if any(t.get("text", "").strip("[]").lower() == SECTION for t in old):
        raise ValueError("rules already exist; review instead of appending duplicates")
    if not old or old[-1].get("text", "").lower() != "[/independent drop]":
        raise ValueError("unexpected independent-drop ending; review before appending")
    preserved = [{"type": t["type"], "payload_u32": t["payload_u32"]} for t in old]
    added = [{"type": 3, "text": "[" + SECTION + "]"}]
    rows = []
    for dungeon in DUNGEONS:
        rate = 10000 if dungeon == 7539 else 0
        row = [dungeon, ITEM, rate, rate, rate, 100000]
        rows.append(row)
        added.extend({"type": 0, "payload_u32": v} for v in row)
    added.append({"type": 3, "text": "[/" + SECTION + "]"})
    plan = {"schema": 2, "input_sha256": baseline, "edits": [{
        "path": TARGET, "data_type": 1, "before_sha256": before["raw_sha256"],
        "tokens": preserved + added}]}
    plan_path = out / "plan.json"
    plan_path.write_text(json.dumps(plan, ensure_ascii=False, indent=2) + "\n", "utf8")
    candidate = out / "Script.pvf"
    patch = json.loads(run("patch", ["patch", "--pvf", args.base, "--plan", str(plan_path), "--out", str(candidate)]))
    verify = json.loads(run("verify", ["verify", "--pvf", args.base, "--plan", str(plan_path), "--out", str(candidate)]))
    after = read("after", TARGET, "tokens", candidate)["tokens"]
    if after[:len(old)] != old or len(after) != len(old) + len(added):
        raise ValueError("original independent-drop tokens changed")
    for actual, expected in zip(after[len(old):], added):
        if actual["type"] != expected["type"] or any(actual.get(k) != v for k, v in expected.items()):
            raise ValueError("new rule token mismatch")
    output_sha = sha(candidate)
    for receipt in (patch, verify):
        if receipt["input_sha256"] != baseline or receipt["output_sha256"] != output_sha or receipt["changed_entries"] != 1:
            raise ValueError("unexpected verification receipt")
    if sha(args.base) != baseline:
        raise ValueError("baseline changed while generating candidate")
    result = {"passed": True, "inputSHA256": baseline, "candidateSHA256": output_sha,
              "toolSHA256": sha(args.tool), "planSHA256": sha(plan_path),
              "changedEntries": 1, "rows": rows, "probabilityDenominator": 1000000,
              "preservedTokens": len(old), "addedTokens": len(added),
              "unchangedEntriesVerified": verify["unchanged_entries_verified"],
              "published": False}
    (out / "result.json").write_text(json.dumps(result, indent=2) + "\n", "utf8")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
