"""Add the Black Diamond package shelf to the earlier public gameplay recipe."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

SOURCE = "2b5453f0ad56aad5a1768cb15881fdb746975a83fbc21d7fc987a5b19b226995"
PATH = "etc/cerashop.etc"
ROW = (900129, 490709931, 0, 0, 3000, "黑钻贵族礼包", 1, 0, -1, -1, -1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pvf-cli", required=True)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    with args.input.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != SOURCE:
            parser.error("expected the public selected-gameplay-20261115 recipe output")
    if args.out.exists():
        parser.error("output already exists")
    raw = subprocess.check_output([args.pvf_cli, "read", "--pvf", args.input, "--path",
                                   PATH, "--format", "tokens", "--json"])
    doc = json.loads(raw)
    tokens = doc["tokens"]
    close = [i for i, token in enumerate(tokens) if token.get("text") == "[/regular package]"]
    if len(close) != 1 or any(t["type"] == 0 and t["payload_u32"] == ROW[0] for t in tokens):
        raise ValueError("unexpected regular-package shelf")
    rows = [{"type": t["type"], "payload_u32": t["payload_u32"]} for t in tokens]
    rows[close[0]:close[0]] = [
        {"type": 6, "text": value} if isinstance(value, str) else
        {"type": 0, "payload_u32": value & 0xffffffff} for value in ROW
    ]
    plan = {"schema": 2, "input_sha256": SOURCE, "edits": [
        {"path": PATH, "data_type": 1, "before_sha256": doc["raw_sha256"], "tokens": rows}
    ]}
    with tempfile.TemporaryDirectory() as directory:
        plan_file = Path(directory) / "shelf.json"
        plan_file.write_text(json.dumps(plan, ensure_ascii=False), encoding="utf-8")
        subprocess.run([args.pvf_cli, "patch", "--pvf", args.input, "--plan", plan_file,
                        "--out", args.out], check=True)


if __name__ == "__main__":
    main()
