"""Compose guardian ACK/state reception with exact V166; no server implementation.

This is a client dependency candidate. See Docs/seeker/GUILD_GUARDIAN_WAR_PLAN.md
for the outstanding state/transaction contract before integrating or releasing it.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '0e16090f0e4e3ac830f70183c814f5b24724cec235935e6c1221bf2d865d4243'
INPUT_SIZE = 111902720
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD100
HELPER_VA = 0x6EAD200


def helper():
    code = bytearray()
    labels, jumps = {}, []

    def jump(op, label):
        code.extend(op)
        jumps.append((len(code), label))
        code.extend(b'\0' * 4)

    code.extend(bytes.fromhex('0fb7442404803f00'))
    jump(b'\x0f\x85', 'class1')
    code.extend(b'\x3d' + struct.pack('<I', 1297))
    jump(b'\x0f\x85', 'fallback')
    jump(b'\xe9', 'accept')
    labels['class1'] = len(code)
    code.extend(bytes.fromhex('803f01'))
    jump(b'\x0f\x85', 'fallback')
    code.extend(b'\x3d' + struct.pack('<I', 1350))
    jump(b'\x0f\x85', 'fallback')
    labels['accept'] = len(code)
    code.extend(bytes.fromhex('b801000000c20400'))
    labels['fallback'] = len(code)
    code.extend(b'\xe9' + struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 5)))
    for offset, label in jumps:
        struct.pack_into('<i', code, offset, labels[label] - offset - 4)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V166 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('expected existing R195 receive section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva + pe.IMAGE_BASE != 0x6EAD000 or vsize != 0x121 or rawsize != 4096
            or struct.unpack_from('<I', data, header+36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt+128))):
        raise ValueError('unexpected V166 receive-section contract')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive call no longer targets the verified chain')
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('receive-section padding is occupied')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, header+8, 0x200+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out),
        size=len(out), helperVA=hex(HELPER_VA), fallbackVA=hex(PREVIOUS_GATE),
        changedRanges=sorted([(call,call+5),(at,at+len(code)),(header+8,header+12),(opt+64,opt+68)]),
        added=[dict(packetClass=0,opcode=1297),dict(packetClass=1,opcode=1350)],
        nativeReceiversChanged=False, serverTransactionsImplemented=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(out)
    if pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
