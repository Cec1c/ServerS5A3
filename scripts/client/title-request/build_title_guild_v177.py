"""Compose exact V177 title and upstream guild-placement fixes without overlap."""
import argparse
import json
from pathlib import Path
import struct
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import patch_guild_placement_v177 as guild
import build_claim_v177 as title
from pe_support import sha256


def require(condition, message):
    if not condition:
        raise ValueError(message)


def build(data):
    # Both independent builders authenticate the complete original input.
    title_bytes, title_receipt = title.build(data)
    guild_bytes, guild_receipt = guild.build(data)
    optional, _ = guild.pe.headers(data)
    checksum = (optional + 64, optional + 68)
    title_ranges = [(lo, hi) for _, lo, hi in title_receipt['diff']['allowedRanges']]
    guild_ranges = [tuple(row) for row in guild_receipt['changedRanges']]
    require(checksum in title_ranges and checksum in guild_ranges, 'checksum boundary missing')
    require(len(data) == len(title_bytes) == len(guild_bytes), 'component changed image length')
    for lo, hi in title_ranges + guild_ranges:
        require(0 <= lo < hi <= len(data), 'invalid component range')
    for left in title_ranges:
        for right in guild_ranges:
            if max(left[0], right[0]) < min(left[1], right[1]):
                require(left == right == checksum, 'component modifications overlap outside checksum')
    candidate = bytearray(title_bytes)
    for lo, hi in guild_ranges:
        if (lo, hi) != checksum:
            candidate[lo:hi] = guild_bytes[lo:hi]
    struct.pack_into('<I', candidate, checksum[0], guild.pe.checksum(candidate, checksum[0]))
    ranges = sorted(set(title_ranges + guild_ranges))
    end = 0
    for lo, hi in ranges:
        require(data[end:lo] == candidate[end:lo], 'bytes outside composed ranges changed')
        end = hi
    require(data[end:] == candidate[end:], 'image tail changed')
    for image, parts in [(title_bytes, title_ranges), (guild_bytes, guild_ranges)]:
        for lo, hi in parts:
            if (lo, hi) != checksum:
                require(candidate[lo:hi] == image[lo:hi], 'component bytes were not retained')
    changed = sum(sum(a != b for a, b in zip(data[lo:hi],candidate[lo:hi])) for lo,hi in ranges)
    final = bytes(candidate)
    receipt = dict(schema='title-guild-compose.v1', inputSha256=sha256(data),
                   outputSha256=sha256(final), inputSize=len(data), outputSize=len(final),
                   components=dict(title=title_receipt,guild=guild_receipt),
                   diff=dict(allowedRanges=ranges,changedBytes=changed,outsideAllowedRanges=[],
                             allOtherBytesUnchanged=True,overlapOnlyPEChecksum=True),
                   gameAcceptance=False,deployed=False)
    return final, receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--receipt',type=Path,required=True)
    args = parser.parse_args()
    source, output, receipt_path = (p.resolve() for p in [args.input,args.output,args.receipt])
    require(len({source,output,receipt_path}) == 3, 'input and outputs must be distinct')
    require(not output.exists() and not receipt_path.exists(), 'refusing to overwrite outputs')
    original = source.read_bytes()
    candidate, receipt = build(original)
    output.parent.mkdir(parents=True,exist_ok=True)
    receipt_path.parent.mkdir(parents=True,exist_ok=True)
    with output.open('xb') as stream:
        stream.write(candidate)
    with receipt_path.open('x',encoding='utf8') as stream:
        json.dump(receipt,stream,indent=2)
    require(sha256(source.read_bytes()) == receipt['inputSha256'], 'input changed')
    require(sha256(output.read_bytes()) == receipt['outputSha256'], 'output readback differs')
    print(json.dumps(dict(outputSha256=receipt['outputSha256'],changedBytes=receipt['diff']['changedBytes'])))


if __name__ == '__main__':
    main()
