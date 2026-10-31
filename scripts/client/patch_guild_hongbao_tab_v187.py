"""Enable the hongbao tab on exact V187 while preserving the removed call's ret 4.

Only the five-byte gate site and PE checksum change. Always write a new file.
This enables the client entry; the server remains authoritative for transactions.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '01b313275faf7b598211f4d8adb932e1b31ce4347827583a2df3cfa4d350e94c'
INPUT_SIZE = 111988736
SITE_VA = 0x11B3765
ORIGINAL = bytes.fromhex('e8a6be1401')
FIXED = bytes.fromhex('b00183c404')  # mov al,1; add esp,4
BEFORE = bytes.fromhex('684a090000e81dfc14018bc8')
AFTER = bytes.fromhex('8b8f80020000')


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V187 input SHA256/size required')
    opt, sections = pe.headers(data)
    at = pe.offset(sections, SITE_VA, len(ORIGINAL))
    if (data[at - len(BEFORE):at] != BEFORE
            or data[at:at + 5] != ORIGINAL
            or data[at + 5:at + 5 + len(AFTER)] != AFTER):
        raise ValueError('hongbao gate context changed')
    out = bytearray(data)
    out[at:at + 5] = FIXED
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(result),
                        size=len(result), siteVA=hex(SITE_VA), before=ORIGINAL.hex(),
                        after=FIXED.hex(), changedRanges=sorted([(at, at + 5), (opt + 64, opt + 68)]),
                        playerAcceptance='pending')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if (pe.sha(source.read_bytes()) != INPUT_SHA256
            or pe.sha(destination.read_bytes()) != receipt['outputSha256']):
        raise RuntimeError('source or candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
