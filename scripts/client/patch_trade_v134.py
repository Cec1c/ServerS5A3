"""Trade result for zero receipts and explicit close notification on exact v133."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '29a3f7f141841483f492e63834aac6c3f0eeb50baf373c0100ba6a3e33ec32b8'
INPUT_SIZE = 111812608
CAVE = 0x6e9d100
CLOSE_SITE = 0x331e1b7
ORIGINAL_CLOSE = 0x2606480
RESULT_GUARD = 0x2012249

def rel(op, address, target):
    return bytes([op]) + struct.pack('<i', target-address-5)

def close_hook():
    code = bytearray()
    def call(target):
        code.extend(rel(0xe8,CAVE+len(code),target))
    call(ORIGINAL_CLOSE)
    # Preserve the original helper's return registers/flags. The caller already
    # checked that the trade peer is valid, so completion-driven window closure
    # after the native trade reset does not enter this call site.
    code += b'\x9c\x60\x6a\x18'
    call(0x374ea10)
    code += b'\x8b\xc8'
    call(0x374f280)
    # Explicit v134 client/server contract. Do not infer the protected legacy
    # cancel helper's packet shape or use an invented item transfer.
    code += b'\x6a\x06'
    call(0x374ea10)
    code += b'\x8b\xc8'
    call(0x374f390)
    call(0x3750ac0)
    code += b'\x61\x9d\xc3'
    return bytes(code)

def build(data):
    if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256:
        raise ValueError('exact published v133 required')
    opt,sections=pe.headers(data)
    if len(sections)!=24 or any(struct.unpack_from('<II',data,opt+128)):
        raise ValueError('unexpected PE layout')
    table=opt+struct.unpack_from('<H',data,opt-4)[0]
    index=next(i for i,s in enumerate(sections) if s[0]==0x6e9d000)
    header=table+index*40
    if data[header:header+8]!=b'.wearnop' or struct.unpack_from('<I',data,header+36)[0]!=0x60000020:
        raise ValueError('unexpected section')
    code=close_hook()
    cave=pe.offset(sections,CAVE,len(code))
    site=pe.offset(sections,CLOSE_SITE,5)
    guard=pe.offset(sections,RESULT_GUARD,2)
    if data[cave:cave+len(code)]!=bytes(len(code)) or data[site:site+5]!=rel(0xe8,CLOSE_SITE,ORIGINAL_CLOSE) or data[guard:guard+2]!=b'\x76\x38':
        raise ValueError('unexpected patch sites')
    out=bytearray(data)
    out[cave:cave+len(code)]=code
    out[site:site+5]=rel(0xe8,CLOSE_SITE,CAVE)
    out[guard:guard+2]=b'\x90\x90'
    struct.pack_into('<I',out,header+8,CAVE-0x6e9d000+len(code))
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    result=bytes(out)
    return result,dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(result),input_size=len(data),output_size=len(result),
        original_ranges=sorted([(opt+64,opt+68),(header+8,header+12),(cave,cave+len(code)),(site,site+5),(guard,guard+2)]),
        cave_va=hex(CAVE),cave_hex=code.hex(),requires_server_abort_state=6,receive_gates_unchanged=True,native_ui_accepted=False)

def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():raise ValueError('separate new output required')
    result,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream:stream.write(result)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['output_sha256']:raise RuntimeError('input/output changed')
    return receipt

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',required=True);p.add_argument('--output',required=True)
    a=p.parse_args()
    print(json.dumps(write_candidate(a.input,a.output),indent=2))
