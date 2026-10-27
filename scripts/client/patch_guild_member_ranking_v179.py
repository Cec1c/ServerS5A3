"""Enable the native CMD1505 member-ranking response on the reviewed V179 cumulative client."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUTS = {'45eb790dc217f11fb5a7beb4f2b778745c7c4edfb4b68e8931e00a131835130f': 0x6EADC00}

INPUT_SIZE = 112005120
CALL_VA = 0x2523A13
HELPER_VA = 0x6EADD00


def helper(previous):
    # Entry ABI: EDI points to classification, opcode is u16 at ESP+4.
    code = bytearray.fromhex('803f0175000fb7442404663de1057500b801000000c20400')
    # Resolve both short branches to the first byte of the fallback jump.
    code[4] = len(code) - 5
    code[15] = len(code) - 16
    code.extend(b'\xe9' + struct.pack('<i', previous - (HELPER_VA + len(code) + 5)))
    return bytes(code)


def build(data):
    digest = pe.sha(data)
    previous = INPUTS.get(digest)
    if len(data) != INPUT_SIZE or previous is None:
        raise ValueError('exact reviewed V179 controls and social candidate required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    headers = [table + i * 40 for i in range(len(sections)) if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(headers) != 1:
        raise ValueError('gate section mismatch')
    h = headers[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, h+8)
    if vsize != 0xC21 or rva + pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096 or any(data[raw+vsize:raw+rawsize]):
        raise ValueError('occupied helper range')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', previous-CALL_VA-5):
        raise ValueError('receive chain mismatch')
    out = bytearray(data)
    code = helper(previous)
    at = pe.offset(sections, HELPER_VA, len(code))
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, h+8, 0xD00+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    return bytes(out), dict(inputSha256=digest, outputSha256=pe.sha(out), size=len(out), changedRanges=sorted([(call,call+5),(at,at+len(code)),(h+8,h+12),(opt+64,opt+68)]), class0Added=[], class1Added=[1505], nativeReadersChanged=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(data)
    if pe.sha(source.read_bytes()) != receipt['inputSha256'] or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
