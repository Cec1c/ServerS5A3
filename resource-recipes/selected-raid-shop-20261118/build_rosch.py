"""Apply the three reviewed Rosch null checks to the public V216 client."""
import argparse
import hashlib
import json
from pathlib import Path


def build(source: Path) -> bytearray:
    recipe = json.loads(Path(__file__).with_name("rosch-guards.json").read_bytes())
    image = bytearray(source.read_bytes())
    if hashlib.sha256(image).hexdigest() != recipe["input"]:
        raise ValueError("expected the public V216 DNF.exe")
    for edit in recipe["edits"]:
        start = edit["offset"]
        before = bytes.fromhex(edit["before"])
        after = bytes.fromhex(edit["after"])
        if image[start:start + len(before)] != before or len(before) != len(after):
            raise ValueError(f"unexpected original bytes at {start:#x}")
        image[start:start + len(before)] = after

    # The existing callback jumps to one of three checks in the extended section.
    # Null results enter its cleanup path. Other results resume the original code.
    image.extend(bytes(recipe["appendZeroBytes"]))
    for guard in recipe["guards"]:
        code = bytes.fromhex(guard["bytes"])
        start = guard["offset"]
        image[start:start + len(code)] = code
    if hashlib.sha256(image).hexdigest() != recipe["output"]:
        raise ValueError("rebuilt client differs from the reviewed client")
    return image


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    result = build(args.input)
    with args.out.open("xb") as stream:
        stream.write(result)
