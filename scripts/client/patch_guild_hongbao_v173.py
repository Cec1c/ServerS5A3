"""Restore only guild hongbao history/claim responses in the cumulative V173 image."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
INPUT_SIZE = 111915008
CALL_VA, PREVIOUS_GATE, HELPER_VA = 0x2523A13, 0x6EAD500, 0x6EAD600


def helper():
    code = bytearray.fromhex('803f01751b0fb74424043d1605000074073d180500007508b801000000c20400e9')
    code += struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 4))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V173 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    headers = [table + i * 40 for i in range(len(sections))
               if data[table + i * 40:table + i * 40 + 8] == b'.r195rx\0']
    if len(headers) != 1:
        raise ValueError('receive section missing')
    h = headers[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, h + 8)
    if (vsize != 0x526 or rva + pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096
            or struct.unpack_from('<I', data, h + 36)[0] != 0x60000020
            or any(data[raw + vsize:raw + rawsize])
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('receive section/free range contract changed')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call + 5] != b'\xe8' + struct.pack('<i', PREVIOUS_GATE - CALL_VA - 5):
        raise ValueError('cumulative receive chain changed')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    out = bytearray(data)
    out[at:at + len(code)] = code
    out[call:call + 5] = b'\xe8' + struct.pack('<i', HELPER_VA - CALL_VA - 5)
    struct.pack_into('<I', out, h + 8, 0x600 + len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
        added=[dict(packetClass=1, opcode=op) for op in (1302, 1304)],
        changedRanges=sorted([(call, call + 5), (at, at + len(code)), (h + 8, h + 12), (opt + 64, opt + 68)]),
        helperVA=hex(HELPER_VA), fallbackVA=hex(PREVIOUS_GATE), nativeReceiversChanged=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(out)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('file readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
