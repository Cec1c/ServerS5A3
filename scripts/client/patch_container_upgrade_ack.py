"""Enable only class1 warehouse/inventory ACKs on the exact cumulative client.

Input stays intact. Output is a separate candidate; no game process is started.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24'
INPUT_SIZE = 112541696
INPUT_PROFILES = {
    INPUT_SHA256: INPUT_SIZE,
    # Published V204 retains the exact receive gate and .bp0 layout checked
    # below. Its later skill/raid sections remain outside every changed range.
    'a9a6587699b7203cc41ce18b0d0fb977538df077dd0b46c745e3caff3ff22cf8': 112549888,
}
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6F06100
HELPER_VA = 0x6F20400


def helper():
    # cmp byte [edi],1; jne previous; movzx eax,word [esp+4]
    code = bytearray.fromhex('803f010f85')
    code.extend(struct.pack('<i', PREVIOUS_GATE-(HELPER_VA+len(code)+4)))
    code.extend(bytes.fromhex('0fb74424043d980100000f84100000003dae0100000f8405000000e9'))
    code.extend(struct.pack('<i', PREVIOUS_GATE-(HELPER_VA+len(code)+4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    input_sha256 = pe.sha(data)
    if INPUT_PROFILES.get(input_sha256) != len(data):
        raise ValueError('exact cumulative client SHA256 and length required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt-4)[0]
    hits = [table+i*40 for i in range(len(sections))
            if data[table+i*40:table+i*40+8] == b'.bp0\0\0\0\0']
    if len(hits) != 1:
        raise ValueError('expected one existing patch section')
    header = hits[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header+8)
    if (rva+pe.IMAGE_BASE != 0x6F06000 or vsize != 0x1A3E6 or rawsize != 0x1B000
            or struct.unpack_from('<I', data, header+36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt+128))):
        raise ValueError('patch section or certificate contract changed')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8'+struct.pack('<i', PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive call does not target the verified previous gate')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    if at < raw+vsize or any(data[raw+vsize:raw+rawsize]):
        raise ValueError('candidate helper padding is already occupied')
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8'+struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, header+8, HELPER_VA-(rva+pe.IMAGE_BASE)+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    ranges = sorted([(opt+64,opt+68),(header+8,header+12),(call,call+5),(at,at+len(code))])
    cursor = 0
    for lo, hi in ranges:
        if data[cursor:lo] != out[cursor:lo]:
            raise ValueError('unexpected change outside declared ranges')
        cursor = hi
    if data[cursor:] != out[cursor:]:
        raise ValueError('unexpected trailing change')
    result = bytes(out)
    return result, dict(inputSHA256=input_sha256, outputSHA256=pe.sha(result),
        size=len(result), helperVA=hex(HELPER_VA), fallbackVA=hex(PREVIOUS_GATE),
        addedClass1=[408,430], changedRanges=ranges, gameExecuted=False,
        entryImportsTLSUnchanged=True, otherBytesPreserved=True)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(result)
    if pe.sha(source.read_bytes()) != receipt['inputSHA256'] or pe.sha(destination.read_bytes()) != receipt['outputSHA256']:
        raise RuntimeError('input or candidate changed during readback')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input,a.output),indent=2))
