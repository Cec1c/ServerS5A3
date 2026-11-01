"""Report an accepted native cash-shop open on the exact cumulative input.

The native visibility guards and scene-state setter remain intact. Ordinary
empty Cera queries (including login initialization) do not carry this marker.
"""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '30c936131e000bd7bc0940bfc1db1b902c84d92cd17ff1d8fbe28f62fbf91425'
INPUT_SIZE = 112549888
SITE = 0x197876E
ORIGINAL = 0x243BD90
HELPER = 0x6F20500
MARKER = b'MSV1'

def helper():
    code = bytearray(b'\x9c\x60')  # preserve flags and all registers
    jumps = []
    def call(va):
        at = HELPER + len(code)
        code.extend(b'\xe8' + struct.pack('<i', va-at-5))
    def skip(op):
        code.extend(op); jumps.append(len(code)); code.extend(bytes(4))
    code.extend(bytes.fromhex('837c242805'))  # original argument, after pushfd/pushad
    skip(b'\x0f\x85')
    call(0x374EA10)
    code.extend(bytes.fromhex('85c0')); skip(b'\x0f\x84')
    code.extend(bytes.fromhex('80780800')); skip(b'\x0f\x84')
    code.extend(bytes.fromhex('6a3f89c1')); call(0x374F280)
    code.extend(b'\x68'+MARKER); call(0x374EA10)
    code.extend(b'\x89\xc1'); call(0x374F3F0)
    call(0x3750AC0)
    end = len(code)
    for at in jumps: struct.pack_into('<i', code, at, end-at-4)
    code.extend(b'\x61\x9d')
    at = HELPER + len(code)
    code.extend(b'\xe9'+struct.pack('<i', ORIGINAL-at-5))
    return bytes(code)

def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative input SHA256 and length required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt-4)[0]
    hits = [table+i*40 for i in range(len(sections)) if data[table+i*40:table+i*40+8] == b'.bp0\0\0\0\0']
    if len(hits) != 1: raise ValueError('patch section missing')
    header = hits[0]
    vsize,rva,rawsize,raw = struct.unpack_from('<IIII',data,header+8)
    if rva+pe.IMAGE_BASE != 0x6F06000 or rawsize != 0x1B000 or vsize != 0x1A431:
        raise ValueError('cumulative patch-section layout changed')
    if struct.unpack_from('<I',data,header+36)[0] != 0x60000020 or any(struct.unpack_from('<II',data,opt+128)):
        raise ValueError('patch section flags or certificate changed')
    site = pe.offset(sections,SITE,5)
    if data[site:site+5] != b'\xe8'+struct.pack('<i',ORIGINAL-SITE-5):
        raise ValueError('native state transition call changed')
    code = helper(); at = pe.offset(sections,HELPER,len(code))
    if at < raw+vsize or at+len(code)>raw+rawsize or any(data[raw+vsize:raw+rawsize]):
        raise ValueError('helper space occupied')
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[site:site+5] = b'\xe8'+struct.pack('<i',HELPER-SITE-5)
    struct.pack_into('<I',out,header+8,HELPER-(rva+pe.IMAGE_BASE)+len(code))
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    ranges = sorted([(opt+64,opt+68),(header+8,header+12),(site,site+5),(at,at+len(code))])
    cursor=0
    for lo,hi in ranges:
        if data[cursor:lo] != out[cursor:lo]: raise ValueError('undeclared change')
        cursor=hi
    if data[cursor:] != out[cursor:]: raise ValueError('undeclared trailing change')
    out=bytes(out)
    return out,dict(inputSHA256=INPUT_SHA256,outputSHA256=pe.sha(out),size=len(out),
        changedRanges=ranges,siteVA=hex(SITE),helperVA=hex(HELPER),marker=MARKER.decode(),
        opcode=63,otherBytesPreserved=True,gameExecuted=False)

def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists(): raise ValueError('separate new output required')
    out,receipt=build(source.read_bytes())
    with destination.open('xb') as f:f.write(out)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['outputSHA256']:
        raise RuntimeError('input/output changed')
    return receipt

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
