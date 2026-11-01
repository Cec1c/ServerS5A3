"""Keep ordinary deletion checks when an event-288 role is selected.

The native handler still checks the modal, selected row, availability, other
event restrictions and role state. Only the event-288 reason override is
removed; the existing name-confirmation window and server request are kept.
"""
import argparse
import json
from pathlib import Path
import patch_receivers_v128 as pe
import struct

INPUT_SHA256 = 'f8d68484a8f4b7b25afa7692c6ba011aab67c6d1928952934057d570615ce646'
INPUT_SIZE = 112549888
SITE = 0x36C45CD


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative input SHA256 and length required')
    opt, sections = pe.headers(data)
    at = pe.offset(sections, SITE, 2)
    if data[at:at+2] != b'\x74\x09':
        raise ValueError('event-288 deletion branch changed')
    out = bytearray(data)
    out[at] = 0xEB
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    allowed = [(opt+64, opt+68), (at, at+1)]
    cursor = 0
    for lo, hi in sorted(allowed):
        if data[cursor:lo] != out[cursor:lo]:
            raise ValueError('undeclared change')
        cursor = hi
    if data[cursor:] != out[cursor:]:
        raise ValueError('undeclared trailing change')
    return bytes(out), dict(inputSHA256=INPUT_SHA256, outputSHA256=pe.sha(out),
        size=len(out), siteVA=hex(SITE), changedRanges=allowed,
        otherBytesPreserved=True, gameExecuted=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(data)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSHA256']:
        raise RuntimeError('input/output changed')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
