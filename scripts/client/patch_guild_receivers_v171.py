"""Restore four audited guild response pairs on the exact V171 client."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = 'c962e82d2258e15edb0ac372f1a4e3b58a1360ee53192201720f723302be5409'
INPUT_SIZE = 111915008
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD300
HELPER_VA = 0x6EAD400
ADDED = ((0, 1284), (0, 1297), (1, 1350), (1, 1503))


def helper():
    code, labels, jumps = bytearray(), {}, []

    def jump(op, label):
        code.extend(op)
        jumps.append((len(code), label))
        code.extend(b'\0' * 4)

    code.extend(bytes.fromhex('0fb7442404803f00'))
    jump(b'\x0f\x85', 'class1')
    for opcode in (1284, 1297):
        code.extend(b'\x3d' + struct.pack('<I', opcode))
        jump(b'\x0f\x84', 'accept')
    jump(b'\xe9', 'fallback')
    labels['class1'] = len(code)
    code.extend(bytes.fromhex('803f01'))
    jump(b'\x0f\x85', 'fallback')
    for opcode in (1350, 1503):
        code.extend(b'\x3d' + struct.pack('<I', opcode))
        jump(b'\x0f\x84', 'accept')
    jump(b'\xe9', 'fallback')
    labels['accept'] = len(code)
    code.extend(bytes.fromhex('b801000000c20400'))
    labels['fallback'] = len(code)
    code.extend(b'\xe9' + struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 5)))
    for offset, label in jumps:
        struct.pack_into('<i', code, offset, labels[label] - offset - 4)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact V171 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('receive section mismatch')
    h = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, h + 8)
    if (vsize != 0x33F or rva + pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096
            or struct.unpack_from('<I', data, h + 36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt + 128))
            or any(data[raw+vsize:raw+rawsize])):
        raise ValueError('receive section contract or free range changed')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive chain mismatch')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, h + 8, 0x400 + len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
        added=[dict(packetClass=c, opcode=o) for c, o in ADDED],
        changedRanges=sorted([(call, call+5), (at, at+len(code)), (h+8, h+12), (opt+64, opt+68)]),
        helperVA=hex(HELPER_VA), fallbackVA=hex(PREVIOUS_GATE), nativeReceiversChanged=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(data)
    if pe.sha(destination.read_bytes()) != receipt['outputSha256'] or pe.sha(source.read_bytes()) != INPUT_SHA256:
        raise RuntimeError('file readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
