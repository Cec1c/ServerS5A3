"""Compose the upstream guild patches onto the exact published v167 client."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import patch_guild_grade_region as grade
import patch_guild_disband_v166 as disband
import patch_guild_teleport_v166 as teleport

V167_SHA = '922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b'
V167_SIZE = 111906816

def sha(data):
    return hashlib.sha256(data).hexdigest()

def require(value, message):
    if not value:
        raise ValueError(message)

def merged_ranges(ranges):
    result = []
    for lo, hi in sorted(ranges):
        if result and lo <= result[-1][1]:
            result[-1][1] = max(result[-1][1], hi)
        else:
            result.append([lo, hi])
    return result

def build(base, current):
    require(sha(base) == grade.INPUT_SHA256, 'exact published v166 base required')
    require(len(current) == V167_SIZE and sha(current) == V167_SHA, 'exact published v167 required')
    first, a = grade.build(base)
    second, b = disband.build(first)
    final_guild, c = teleport.build(second)
    require(len(final_guild) == len(base), 'upstream guild patches changed base size')
    opt, old_sections = disband.pe.headers(base)
    current_opt, current_sections = disband.pe.headers(current)
    require(current_opt == opt and current_sections[:len(old_sections)] == old_sections,
            'v167 remapped existing sections')
    ranges = merged_ranges(a['allowed_changed_ranges'] + b['changedRanges'] + c['changedRanges'])
    cursor = 0
    for lo, hi in ranges:
        require(base[cursor:lo] == final_guild[cursor:lo], 'upstream writes outside declared ranges')
        cursor = hi
    require(base[cursor:] == final_guild[cursor:], 'upstream tail changed')
    result = bytearray(current)
    for lo, hi in ranges:
        if [lo, hi] == [opt + 64, opt + 68]:
            continue
        require(current[lo:hi] == base[lo:hi], 'guild change overlaps the published peer repair')
        result[lo:hi] = final_guild[lo:hi]
    struct.pack_into('<I', result, opt + 64, disband.pe.checksum(result, opt + 64))
    result = bytes(result)
    cursor = 0
    for lo, hi in ranges:
        require(current[cursor:lo] == result[cursor:lo], 'composition modified unrelated v167 bytes')
        cursor = hi
    require(current[cursor:] == result[cursor:], 'composition modified v167 tail')
    require(disband.pe.headers(result)[1] == current_sections, 'section mapping changed')
    receipt = dict(schema=1, baseSha256=sha(base), inputSha256=sha(current), outputSha256=sha(result),
                   size=len(result), changedRanges=ranges, preservesAllOtherV167Bytes=True,
                   upstreamIntermediateSha256=[sha(first), sha(second), sha(final_guild)],
                   class0Added=[377], class1Added=[303, 434], acceptedGuildRegions=[0, 249],
                   nativeReadersChanged=False, installed=False)
    return result, receipt

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-v166', type=Path, required=True)
    parser.add_argument('--input-v167', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--manifest', type=Path, required=True)
    args = parser.parse_args()
    require(not args.output.exists() and not args.manifest.exists(), 'outputs must be new files')
    require(args.output.resolve() not in (args.base_v166.resolve(), args.input_v167.resolve()), 'input cannot be overwritten')
    base, current = args.base_v166.read_bytes(), args.input_v167.read_bytes()
    result, receipt = build(base, current)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('xb') as stream:
        stream.write(result)
    require(sha(args.output.read_bytes()) == receipt['outputSha256'], 'output readback mismatch')
    require(sha(args.base_v166.read_bytes()) == receipt['baseSha256'] and sha(args.input_v167.read_bytes()) == V167_SHA, 'input changed')
    with args.manifest.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))

if __name__ == '__main__':
    main()
