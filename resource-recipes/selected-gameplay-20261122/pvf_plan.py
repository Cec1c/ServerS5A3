"""Create a PVF-CLI schema 2 plan against the published raid-shop resource."""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

BASE = "6aaca082329fa4134d25cb2375df36b6bfcda15e6f668fb7578155a1a0eadd71"
MILEAGE = "etc/mileageshop_chn.etc"
TROPHY = "itemshop/raidshop_trophy.shp"
TAROT = "stackable/10159001/10159653.stk"


def read_tokens(cli, pvf, path):
    return json.loads(subprocess.check_output([
        cli, "read", "--pvf", str(pvf), "--path", path, "--format", "tokens", "--json"
    ]))


def locate(tokens, values):
    fields = [token.get("text", token["payload_u32"]) for token in tokens]
    matches = [i for i in range(len(fields)) if fields[i:i + len(values)] == values]
    if len(matches) != 1:
        raise ValueError(f"expected one occurrence of {values}")
    return matches[0]


def changes(path, original):
    tokens = [dict(t) for t in original]
    if path == MILEAGE:
        start = locate(tokens, [1000064, 490702726, 1, 0, 666, 0, "1个", 101, 0])
        del tokens[start:start + 9]
        start = locate(tokens, [1000064, 1, 0, 0, 0, 0, "[/purchasing limit]"])
        del tokens[start:start + 6]
    elif path == TROPHY:
        index = locate(tokens, [10157776, 10159652, 10159653]) + 1
        del tokens[index]
    elif path == TAROT:
        index = locate(tokens, ["[need material]", 10304529, 1000, "[/need material]"]) + 2
        tokens[index]["payload_u32"] = 500
    else:
        raise ValueError("unexpected resource path")
    return [{k: token[k] for k in ("type", "text" if "text" in token else "payload_u32")}
            for token in tokens]


def plan(cli, pvf):
    with pvf.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != BASE:
            raise ValueError("expected the published raid-shop PVF")
    edits = []
    for path in (MILEAGE, TROPHY, TAROT):
        doc = read_tokens(cli, pvf, path)
        edits.append(dict(path=path, data_type=1, before_sha256=doc["raw_sha256"],
                          tokens=changes(path, doc["tokens"])))
    return dict(schema=2, input_sha256=BASE, edits=edits)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pvf-cli", default="pvf")
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    selection = plan(args.pvf_cli, args.input)
    with args.out.open("x", encoding="utf8") as stream:
        json.dump(selection, stream, ensure_ascii=False)
