"""Migrate an extracted channel_info.etc to free u8 IDs without repacking PVF.

Usage: python scripts/migrate_fiendwar_channel_u8.py INPUT OUTPUT
OUTPUT must be new. Only the three channel IDs change; ports/configuration are
outside this tool. The original type 42 and group [fiendwar] are preserved.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re


CHANNEL_IDS = {261: 244, 262: 245, 263: 246}
ROW = re.compile(rb"[ \t]*([0-9]+)[ \t]+(`[^`\r\n]*`)[ \t]+([0-9]+)[ \t]+(`[^`\r\n]*`)[^\r\n]*")


def migrate(data: bytes) -> bytes:
    """Validate the complete server section, then replace exactly three IDs."""
    starts = list(re.finditer(rb"(?m)^[ \t]*\[server\][ \t]+[0-9]+[ \t]*\r?$", data))
    ends = list(re.finditer(rb"(?m)^[ \t]*\[/server\][ \t]*\r?$", data))
    if len(starts) != 1 or len(ends) != 1 or starts[0].end() >= ends[0].start():
        raise ValueError("expected exactly one complete server section")
    start, end = starts[0].end(), ends[0].start()
    rows = {}
    offset = start
    for line in data[start:end].splitlines(keepends=True):
        body = line.rstrip(b"\r\n")
        stripped = body.strip()
        if stripped and not stripped.startswith(b"//"):
            match = ROW.fullmatch(body)
            if match is None:
                raise ValueError("unrecognized server row; cannot establish free IDs")
            channel_id = int(match[1])
            if channel_id in rows:
                raise ValueError("duplicate server channel ID")
            rows[channel_id] = (match, offset)
        offset += len(line)
    if any(target in rows for target in CHANNEL_IDS.values()):
        raise ValueError("destination channel ID is occupied")
    replacements = []
    for source, target in CHANNEL_IDS.items():
        if source not in rows:
            raise ValueError("required source channel is missing")
        match, offset = rows[source]
        if (match[2], match[3], match[4]) != (b"`<chn_channel_info_048>`", b"42", b"`[fiendwar]`"):
            raise ValueError("source channel name/type/group does not match FiendWar")
        replacements.append((offset + match.start(1), offset + match.end(1), str(target).encode("ascii")))
    result = data
    for lo, hi, value in sorted(replacements, reverse=True):
        result = result[:lo] + value + result[hi:]
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path, help="extracted channel_info.etc")
    parser.add_argument("output", type=Path, help="new output file; never overwritten")
    args = parser.parse_args(argv)
    try:
        before = args.input.read_bytes()
        after = migrate(before)
        with args.output.open("xb") as stream:
            stream.write(after)
    except (OSError, ValueError) as err:
        # Do not disclose host paths from filesystem exceptions.
        message = str(err) if isinstance(err, ValueError) else type(err).__name__
        parser.exit(2, "migration refused: " + message + "\n")
    print(json.dumps({"input_sha256": hashlib.sha256(before).hexdigest(),
                      "output_sha256": hashlib.sha256(after).hexdigest(),
                      "channel_ids": CHANNEL_IDS, "type": 42, "group": "fiendwar",
                      "changed_rows": 3, "unrelated_bytes_preserved": True}, sort_keys=True))


if __name__ == "__main__":
    main()
