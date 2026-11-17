"""Check the shared 120-channel source, historical copy and human reference."""
import argparse
import csv
import hashlib
import io
import json
from pathlib import Path
import re

SHA256 = "5630b099ef98e322806c35cec25be7c7aae699ebfbc8f16e60f1a70d12bfe7d7"
TABLE = "go-server/internal/services/dnfbridge/testdata/channel_info.etc"
HISTORICAL = "resource-recipes/bundle008/channel.override.etc"
CSV = "Docs/assets/channel-reference-120.csv"


def check(root):
    raw = (root / TABLE).read_bytes()
    if hashlib.sha256(raw).hexdigest() != SHA256:
        raise ValueError("common channel table SHA256 differs")
    if (root / HISTORICAL).read_bytes() != raw:
        raise ValueError("historical channel table differs from common baseline")
    matches = re.findall(rb"(?m)^[ \t]*(\d+)[ \t]+`<chn_channel_info_[^`]+>`[ \t]+(\d+)[ \t]+`\[([^]]+)\]`", raw)
    channels = {int(i): (int(kind), area.decode().casefold()) for i, kind, area in matches}
    if len(matches) != 120 or len(channels) != 120:
        raise ValueError("common table must contain 120 unique channels")
    rows = list(csv.DictReader(io.StringIO((root / CSV).read_text(encoding="utf-8-sig"))))
    if len(rows) != 120 or len({int(r["频道号"]) for r in rows}) != 120:
        raise ValueError("CSV must contain 120 unique channels")
    for row in rows:
        number = int(row["频道号"])
        if (channels.get(number) != (int(row["类型"]), row["区域标识"].casefold()) or
                int(row["端口"]) != 10000 + number):
            raise ValueError("CSV channel/type/area/port differs from common table")
    return dict(sha256=SHA256, channels=120, ports=sorted(10000 + i for i in channels),
                fiendwar=[244, 245, 246], historicalCopyIdentical=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    print(json.dumps(check(args.root), sort_keys=True))


if __name__ == "__main__":
    main()
