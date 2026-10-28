"""Send initialized hongbao business fields on the exact cumulative V188 image."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '6c85e8e46fb670adba98bb7c8e7ea2c345f8d059d391c21be828a7f101977735'
INPUT_SIZE = 111988736
# These raw writer calls append their entire buffer to an existing packet.
# Advance past the unused 13-byte structure prefix and reduce the copy length.
SITES = (
    (0x1091E57, 'issue', '8d55dc6a12', '8d55e96a05'),
    (0x11A61B3, 'claim', '8d55dc6a11', '8d55e96a04'),
    (0x11B3F17, 'history', '8d45bc6a11', '8d45c96a04'),
)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V188 input SHA256/size required')
    opt, sections = pe.headers(data)
    out = bytearray(data)
    changed = [(opt + 64, opt + 68)]
    sites = []
    for va, name, old_hex, new_hex in SITES:
        old, new = bytes.fromhex(old_hex), bytes.fromhex(new_hex)
        at = pe.offset(sections, va, len(old))
        if data[at:at + len(old)] != old:
            raise ValueError(f'{name} sender fingerprint changed')
        out[at:at + len(new)] = new
        changed.extend((at + i, at + i + 1) for i, (a, b) in enumerate(zip(old, new)) if a != b)
        sites.append(dict(name=name, va=hex(va), before=old_hex, after=new_hex))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(result),
                        size=len(result), sites=sites, changedRanges=sorted(changed),
                        playerAcceptance='pending')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('source or candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
