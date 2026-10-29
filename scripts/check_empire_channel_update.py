"""Verify an already deployed 119-row Imperial directory update.

Usage: python scripts/check_empire_channel_update.py CURRENT CANDIDATE
The only allowed change is adding the two PVF-listed dungeon IDs to the
existing tagtournament area block. This does not publish or replace a file.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re

from check_empire_channel import ROW
from migrate_empire_channel import CHANNEL_DUNGEON_IDS, CHANNEL_GROUP, CHANNEL_ID, CHANNEL_NAME, CHANNEL_TYPE


def validate_update(current: bytes, candidate: bytes) -> dict:
    section = re.search(rb"(?m)^\[server\][ \t]+1\r?\n(.*?)^\[/server\]\r?\n?", current, re.S)
    if section is None:
        raise ValueError("current directory has no complete server-1 section")
    newline = b"\r\n" if b"\r\n" in section.group(0) else b"\n"
    name = CHANNEL_NAME.encode("utf-8")
    old_area = b"[dungeon]" + newline + b"`[tagtournament]`" + newline + b"`" + name + b"`" + newline + b"[/dungeon]" + newline
    new_area = (b"[dungeon]" + newline + b"`[tagtournament]`" + newline + b"`" + name + b"`" + newline +
                b"".join(str(item).encode("ascii") + newline for item in CHANNEL_DUNGEON_IDS) +
                b"[/dungeon]" + newline)
    if current.count(old_area) != 1 or current.count(b"`[tagtournament]`") != 2:
        raise ValueError("current directory is not the reviewed empty-area Imperial baseline")
    if candidate != current.replace(old_area, new_area, 1):
        raise ValueError("candidate changes bytes outside the two Imperial dungeon mappings")
    server = section.group(1)
    rows = [(int(m[1]), m[2].decode("utf-8"), int(m[3]), m[4].decode("utf-8")) for m in ROW.finditer(server)]
    if len(rows) != 119 or len({row[0] for row in rows}) != 119:
        raise ValueError("current directory must have 119 unique channel rows")
    empire = [row for row in rows if row[0] == CHANNEL_ID or row[2] == CHANNEL_TYPE or row[3] == "[" + CHANNEL_GROUP + "]"]
    if empire != [(CHANNEL_ID, CHANNEL_NAME, CHANNEL_TYPE, "[" + CHANNEL_GROUP + "]")]:
        raise ValueError("current Imperial channel row differs")
    return {
        "currentSha256": hashlib.sha256(current).hexdigest(),
        "candidateSha256": hashlib.sha256(candidate).hexdigest(),
        "channels": len(rows),
        "channelId": CHANNEL_ID,
        "dungeonIds": CHANNEL_DUNGEON_IDS,
        "unchangedBytesPreserved": True,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("current", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args(argv)
    try:
        result = validate_update(args.current.read_bytes(), args.candidate.read_bytes())
    except (OSError, ValueError, UnicodeDecodeError) as error:
        parser.exit(2, "channel update refused: " + str(error) + "\n")
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
