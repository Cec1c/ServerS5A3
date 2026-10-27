"""Correct CMD215 error-byte ownership on the exact cumulative V186 client."""
import argparse
import json
import struct
from pathlib import Path

import patch_receivers_v128 as pe

INPUT_SHA256 = '0cfc74d29de563c6612b2118a47f5f731c80f3e5a16b17b9e0bfd70fbd93acc9'
INPUT_SIZE = 111984640
CLASSIFIER = 0x8BAB80
TABLE = 0x8BABB0
ENTRY = TABLE + 215 - 18
CLASSIFIER_BYTES = bytes.fromhex(
    '558bec8b450883c0ee3dc500000077120fb680b0ab8b00'
    'ff2485a8ab8b00b0015dc332c05dc3')


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V186 input required')
    opt, sections = pe.headers(data)
    code = pe.offset(sections, CLASSIFIER, len(CLASSIFIER_BYTES))
    table = pe.offset(sections, TABLE, 198)
    at = pe.offset(sections, ENTRY, 1)
    expected = bytes(0 if opcode in (18, 20, 27, 215) else 1
                     for opcode in range(18, 216))
    if data[code:code + len(CLASSIFIER_BYTES)] != CLASSIFIER_BYTES:
        raise ValueError('ACK classifier changed')
    if data[table:table + 198] != expected:
        raise ValueError('ACK classification table changed')
    out = bytearray(data)
    # Switch index 1 returns false: the dispatcher must read CMD215's error
    # byte before invoking its callback. 18/20/27 retain their native contract.
    out[at] = 1
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    return bytes(out), dict(
        inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
        changedRanges=sorted([(at, at + 1), (opt + 64, opt + 68)]),
        opcode=215, errorReader='native ACK dispatcher',
        noticeBytesPreserved=True)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(out)
    if (pe.sha(source.read_bytes()) != INPUT_SHA256 or
            pe.sha(destination.read_bytes()) != receipt['outputSha256']):
        raise RuntimeError('source or output readback differs')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
