"""Compose guild hongbao reception and base refresh fixes from official V173."""
import argparse
import json
from pathlib import Path

import patch_guild_hongbao_v173 as hongbao
import patch_guild_war_refresh_v173 as war


def build(data):
    intermediate, first = hongbao.build(data)
    out, second = war.build(intermediate)
    ranges = sorted(set(tuple(r) for r in first['changedRanges'] + second['changedRanges']))
    cursor = 0
    for lo, hi in ranges:
        if lo > cursor:
            assert data[cursor:lo] == out[cursor:lo]
        cursor = max(cursor, hi)
    assert data[cursor:] == out[cursor:len(data)]
    return out, dict(inputSha256=first['inputSha256'], outputSha256=second['outputSha256'],
        size=len(out), changedRanges=ranges, stages=[first, second])


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('distinct new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(out)
    if (hongbao.pe.sha(destination.read_bytes()) != receipt['outputSha256']
            or hongbao.pe.sha(source.read_bytes()) != receipt['inputSha256']):
        raise RuntimeError('file readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
