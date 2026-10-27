"""Explicit preflight for a candidate Imperial Arena channel directory.

Usage: python scripts/check_empire_channel.py BASE CANDIDATE
This is deliberately not a server-startup requirement. It proves that CANDIDATE
is exactly the approved BASE plus the single reviewed channel insertion, then
checks the resulting type-39 row and derived game port.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re

from migrate_empire_channel import CHANNEL_GROUP, CHANNEL_ID, CHANNEL_NAME, CHANNEL_PORT, CHANNEL_TYPE, migrate


ROW = re.compile(rb"(?m)^[ \t]*(\d+)[ \t]+`([^`\r\n]*)`[ \t]+(\d+)[ \t]+`([^`\r\n]*)`[^\r\n]*\r?$")


def validate(base: bytes, candidate: bytes):
    expected = migrate(base)
    if candidate != expected:
        raise ValueError("candidate is not exactly BASE plus the reviewed Imperial Arena insertion")
    rows = [(int(match[1]), match[2].decode("utf-8"), int(match[3]), match[4].decode("utf-8"))
            for match in ROW.finditer(candidate)]
    if len(rows) != 119 or len({row[0] for row in rows}) != 119:
        raise ValueError("candidate must contain 119 unique channel rows")
    group_token = "[" + CHANNEL_GROUP + "]"
    empire = [row for row in rows if row[2] == CHANNEL_TYPE or row[3] == group_token]
    if empire != [(CHANNEL_ID, CHANNEL_NAME, CHANNEL_TYPE, group_token)]:
        raise ValueError("candidate must contain exactly one reviewed type-39 Imperial Arena row")
    return {
        "baseRows": 118,
        "candidateRows": len(rows),
        "channelId": CHANNEL_ID,
        "port": CHANNEL_PORT,
        "type": CHANNEL_TYPE,
        "group": CHANNEL_GROUP,
        "name": CHANNEL_NAME,
        "startupRequirement": False,
        "explicitPreflight": True,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base", type=Path, help="approved 118-row external channel_info.etc")
    parser.add_argument("candidate", type=Path, help="complete 119-row candidate")
    args = parser.parse_args(argv)
    try:
        base = args.base.read_bytes()
        candidate = args.candidate.read_bytes()
        result = validate(base, candidate)
    except (OSError, ValueError, UnicodeDecodeError) as err:
        message = str(err) if isinstance(err, ValueError) else type(err).__name__
        parser.exit(2, "preflight refused: " + message + "\n")
    print(json.dumps({
        "baseSha256": hashlib.sha256(base).hexdigest(),
        "candidateSha256": hashlib.sha256(candidate).hexdigest(),
        **result,
    }, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
