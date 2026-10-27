"""Append the Imperial Arena channel to the approved 118-row external table.

Usage: python scripts/migrate_empire_channel.py INPUT OUTPUT
OUTPUT must be new. The input must contain one complete one-row-per-channel
server block. Every existing byte is preserved; only the empty tag-tournament
area block and one type-39 channel row are inserted.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re


CHANNEL_ID = 187
CHANNEL_PORT = 10187
CHANNEL_TYPE = 39
CHANNEL_GROUP = "tagtournament"
CHANNEL_NAME = "帝国竞技场"
EXPECTED_ROWS = 118
ROW = re.compile(rb"[ \t]*([0-9]+)[ \t]+(`[^`\r\n]*`)[ \t]+([0-9]+)[ \t]+(`[^`\r\n]*`)[^\r\n]*")


def migrate(data: bytes) -> bytes:
    starts = list(re.finditer(rb"(?m)^[ \t]*\[server\][ \t]+[0-9]+[ \t]*(\r?\n)", data))
    ends = list(re.finditer(rb"(?m)^[ \t]*\[/server\][ \t]*\r?\n", data))
    if len(starts) != 1 or len(ends) != 1 or starts[0].end() >= ends[0].start():
        raise ValueError("expected exactly one complete server section")
    newline = starts[0].group(1)
    area_key = b"`[tagtournament]`"
    if area_key in data[:starts[0].start()]:
        raise ValueError("an Imperial Arena area block already exists")
    rows = []
    for line in data[starts[0].end():ends[0].start()].splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith(b"//"):
            continue
        match = ROW.fullmatch(line)
        packed = re.search(rb"[ \t]+[0-9]+[ \t]+`[^`\r\n]+`[ \t]+[0-9]+[ \t]+`[^`\r\n]+`", line[match.end(4):]) if match else None
        if match is None or line.count(b"`") != 6 or packed:
            raise ValueError("expected one complete channel row per line")
        rows.append((int(match[1]), match[2], int(match[3]), match[4]))
    if len(rows) != EXPECTED_ROWS or len({row[0] for row in rows}) != EXPECTED_ROWS:
        raise ValueError("expected the approved 118 unique channel rows")
    if any(row[0] == CHANNEL_ID for row in rows):
        raise ValueError("destination channel ID is occupied")
    if any(row[2] == CHANNEL_TYPE or row[3] == b"`[tagtournament]`" for row in rows):
        raise ValueError("an Imperial Arena channel already exists")

    area_block = (b"[dungeon]" + newline +
                  area_key + newline +
                  b"`" + CHANNEL_NAME.encode("utf-8") + b"`" + newline +
                  b"[/dungeon]" + newline + newline)
    channel_row = (b"   187   `" + CHANNEL_NAME.encode("utf-8") +
                   b"`   39   `[tagtournament]`   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   ``" +
                   newline)

    with_row = data[:ends[0].start()] + channel_row + data[ends[0].start():]
    return with_row[:starts[0].start()] + area_block + with_row[starts[0].start():]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path, help="approved external channel_info.etc")
    parser.add_argument("output", type=Path, help="new complete channel_info.etc; never overwritten")
    args = parser.parse_args(argv)
    try:
        before = args.input.read_bytes()
        after = migrate(before)
        with args.output.open("xb") as stream:
            stream.write(after)
    except (OSError, ValueError) as err:
        message = str(err) if isinstance(err, ValueError) else type(err).__name__
        parser.exit(2, "migration refused: " + message + "\n")
    print(json.dumps({
        "input_sha256": hashlib.sha256(before).hexdigest(),
        "output_sha256": hashlib.sha256(after).hexdigest(),
        "base_rows": EXPECTED_ROWS,
        "output_rows": EXPECTED_ROWS + 1,
        "channel_id": CHANNEL_ID,
        "port": CHANNEL_PORT,
        "type": CHANNEL_TYPE,
        "group": CHANNEL_GROUP,
        "name": CHANNEL_NAME,
        "existing_bytes_preserved": True,
    }, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
