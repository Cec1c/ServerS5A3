"""Compose notification 541 with the exact published V164 receive gate."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '2d56c7086c3fd9bc029d932571504b7f89147e47f1d563684f74b9e467f4f8a2'
INPUT_SIZE = 111898624
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD000
HELPER_VA = PREVIOUS_GATE + 0x100


def helper():
    code = bytearray.fromhex('0fb7442404803f00')
    for instruction in (b'', b'\x3d' + struct.pack('<I', 541)):
        code.extend(instruction)
        code.extend(b'\x0f\x85')
        code.extend(struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V164 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('expected one existing R195 gate section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva + pe.IMAGE_BASE != PREVIOUS_GATE or rawsize != 4096 or vsize > 0x100
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('unexpected gate section contract')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive call no longer targets the verified chain')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('existing gate padding is occupied')
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, header+8, 0x100+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out),
        size=len(out), helperVA=hex(HELPER_VA), fallbackVA=hex(PREVIOUS_GATE),
        changedRanges=sorted([(call, call+5), (at, at+len(code)), (header+8, header+12), (opt+64, opt+68)]),
        class0Added=[541], nativeReaderChanged=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(data)
    if pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
