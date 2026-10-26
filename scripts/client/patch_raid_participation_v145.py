"""Admit class0 584 at the real dispatcher in the exact published v144.

Preserve the previous class adapter for every other class/opcode. The existing
584 consumer and v144 FiendWar initialization remain byte-for-byte unchanged.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '6810d2130fd12849cbcccac1c627ec9eb243961a5b6e179b8de4710115ffbabd'
INPUT_SIZE = 111816704
CALL_VA = 0x2523a13
PREVIOUS_ADAPTER = 0x6ea1000


def helper(va):
    # The dispatcher supplies the packet pointer in EDI and a u16 argument.
    code = bytearray.fromhex('0fb7442404803f000f85')
    code += struct.pack('<i', PREVIOUS_ADAPTER - (va + len(code) + 4))
    code += bytes.fromhex('3d480200000f85')
    code += struct.pack('<i', PREVIOUS_ADAPTER - (va + len(code) + 4))
    code += bytes.fromhex('b801000000c20400')
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact published v144 required')
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    count = struct.unpack_from('<H', data, pe_at + 6)[0]
    table = opt + struct.unpack_from('<H', data, pe_at + 20)[0]
    header = table + count * 40
    if count != 25 or any(data[header:header+40]) or header+40 > 4096:
        raise ValueError('unexpected section layout')
    if struct.unpack_from('<II', data, opt+32) != (4096,4096) or struct.unpack_from('<I',data,opt+56)[0] != INPUT_SIZE:
        raise ValueError('unexpected alignment/image size')
    at = pe.offset(sections, CALL_VA, 5)
    expected = b'\xe8'+struct.pack('<i', PREVIOUS_ADAPTER-CALL_VA-5)
    if data[at:at+5] != expected:
        raise ValueError('actual dispatcher call fingerprint mismatch')
    registration = pe.offset(sections, 0x200285a, 19)
    if data[registration:registration+19] != bytes.fromhex('6a00684049ff0168480200008bcee8331a5200'):
        raise ValueError('584 registration fingerprint mismatch')
    new_va = pe.IMAGE_BASE + INPUT_SIZE
    code = helper(new_va)
    out = bytearray(data)
    out.extend(code+bytes(4096-len(code)))
    struct.pack_into('<i',out,at+1,new_va-CALL_VA-5)
    struct.pack_into('<H',out,pe_at+6,count+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
    struct.pack_into('<I',out,opt+56,INPUT_SIZE+4096)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.raidpos',len(code),INPUT_SIZE,4096,INPUT_SIZE,0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    allowed=[(at+1,at+5),(pe_at+6,pe_at+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)]
    assert all(a==b or any(lo<=i<hi for lo,hi in allowed) for i,(a,b) in enumerate(zip(data,out)))
    return bytes(out),dict(input_sha256=INPUT_SHA256,output_sha256=hashlib.sha256(out).hexdigest(),input_size=len(data),output_size=len(out),adapter_va=hex(new_va),adapter_size=len(code),call_va=hex(CALL_VA),previous_adapter_va=hex(PREVIOUS_ADAPTER),changed_admissions=[dict(packet_class=0,opcode=584)],consumer_va='0x1ff4940',consumer_changed=False,full_ui_acceptance=False)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('input','output','receipt'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args()
    if len({x.resolve() for x in (a.input,a.output,a.receipt)}) != 3 or a.output.exists() or a.receipt.exists():p.error('immutable source and new outputs required')
    out,receipt=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(out)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))


if __name__ == '__main__':main()
