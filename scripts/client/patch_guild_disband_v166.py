"""Add only the native class1/op303 disband ACK to the audited guild candidate."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '1881498970cffc4bd949fcdc493b285a84d728b84e6d3eb31a2e5e037722caae'
INPUT_SIZE = 111902720
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD100
HELPER_VA = 0x6EAD200
CALLBACK_VA = 0x6EAD240
CALLBACK_REGISTRATION_VA = 0x1FE852D


def callback():
    # op303 originally renders only the safe-mode error. Reuse the existing
    # op153 error callback for the established membership/permission errors;
    # successful op303 still consumes no body through its original callback.
    code = bytearray.fromhex('807c2408000f85')
    code.extend(struct.pack('<i',0x1FBEC90-(CALLBACK_VA+len(code)+4)))
    for error in (3,34,36,87):
        code.extend(bytes.fromhex('807c240c')+bytes([error])+b'\x0f\x84')
        code.extend(struct.pack('<i',0x1FC73D0-(CALLBACK_VA+len(code)+4)))
    code.extend(b'\xe9'+struct.pack('<i',0x1FBEC90-(CALLBACK_VA+len(code)+5)))
    return bytes(code)


def helper():
    code = bytearray.fromhex('0fb7442404803f01')
    for instruction in (b'', b'\x3d' + struct.pack('<I', 303)):
        code.extend(instruction)
        code.extend(b'\x0f\x85')
        code.extend(struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact audited V166 guild-grade candidate required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('expected one existing gate section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva + pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096 or not 0x100 < vsize <= 0x200
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('unexpected gate section contract')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('unexpected existing receive chain')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    callback_code = callback()
    callback_at = pe.offset(sections, CALLBACK_VA, len(callback_code))
    registration = pe.offset(sections, CALLBACK_REGISTRATION_VA, 10)
    if data[registration:registration+10] != bytes.fromhex('6890ecfb01682f010000'):
        raise ValueError('unexpected native op303 callback registration')
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('gate padding is occupied')
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[callback_at:callback_at+len(callback_code)] = callback_code
    struct.pack_into('<I',out,registration+1,CALLBACK_VA)
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, header+8, 0x240+len(callback_code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
        changedRanges=sorted([(call,call+5),(at,at+len(code)),(callback_at,callback_at+len(callback_code)),(registration+1,registration+5),(header+8,header+12),(opt+64,opt+68)]),
        class1Added=[303], class0Added=[], nativeReadersChanged=False,
        disbandVisibleErrors=[3,34,36,87])


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(data)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input or candidate changed')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
