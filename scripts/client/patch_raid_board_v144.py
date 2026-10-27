"""Add a hash-pinned FiendWar snapshot initializer to the published v143.

The registered empty 1483 callback becomes an adapter to the existing native
model constructor/reset and active-board mode. Receive admissions are unchanged.
The source and player installation are never edited.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '1ea94c7db46996cffa940ef50551fd912808f854656487c5664b388098cf7d88'
INPUT_SIZE = 111812608
REGISTRATION_VA = 0xb90cc4
OLD_CALLBACK = 0x769ad0
SECTION_SIZE = 4096


def helper(va):
    code = bytearray(); labels = {}; jumps = []
    def emit(value): code.extend(bytes.fromhex(value))
    def call(target):
        code.append(0xe8); code.extend(struct.pack('<i', target - (va + len(code) + 4)))
    def jump(op, label):
        emit(op); jumps.append((len(code), label)); code.extend(bytes(4))
    emit('9c608b35e47e650585f6')  # preserve flags/registers; load existing model
    jump('0f85', 'reset')
    emit('680c020000'); call(0x8a9480); emit('83c40485c0')
    jump('0f84', 'done')
    emit('8bc8'); call(0xb8f990); emit('8bf0a3e47e6505')
    labels['reset'] = len(code)
    emit('85f6'); jump('0f84', 'done')
    emit('8bce'); call(0xb8e6a0)
    emit('c7460402000000c7464801000000')
    labels['done'] = len(code); emit('619dc3')
    for at, label in jumps: struct.pack_into('<i', code, at, labels[label] - at - 4)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact published v143 required')
    opt, sections = pe.headers(data); pe_at = opt - 24
    count = struct.unpack_from('<H', data, pe_at + 6)[0]
    table = opt + struct.unpack_from('<H', data, pe_at + 20)[0]
    header = table + count * 40
    if count != 24 or any(data[header:header + 40]) or header + 40 > 4096:
        raise ValueError('unexpected section-header layout')
    if struct.unpack_from('<II', data, opt + 32) != (4096,4096) or struct.unpack_from('<I',data,opt+56)[0] != INPUT_SIZE:
        raise ValueError('unexpected section alignment/image size')
    at = pe.offset(sections, REGISTRATION_VA, 5)
    if data[at:at+5] != b'\x68' + struct.pack('<I',OLD_CALLBACK):
        raise ValueError('1483 registration fingerprint mismatch')
    for va, expected in [(0xb8e6a0,'558bec83ec0c5356'),(0xb8f990,'558bec6aff'),(0xb88390,'558bec5657')]:
        raw=bytes.fromhex(expected); offset=pe.offset(sections,va,len(raw))
        if data[offset:offset+len(raw)] != raw: raise ValueError('native dependency fingerprint mismatch')
    new_va = pe.IMAGE_BASE + INPUT_SIZE; code=helper(new_va); out=bytearray(data)
    out.extend(code+bytes(SECTION_SIZE-len(code)))
    struct.pack_into('<I',out,at+1,new_va)
    struct.pack_into('<H',out,pe_at+6,count+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+SECTION_SIZE)
    struct.pack_into('<I',out,opt+56,INPUT_SIZE+SECTION_SIZE)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.raidui',len(code),INPUT_SIZE,SECTION_SIZE,INPUT_SIZE,0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    allowed=[(at+1,at+5),(pe_at+6,pe_at+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)]
    assert all(a==b or any(lo<=i<hi for lo,hi in allowed) for i,(a,b) in enumerate(zip(data,out)))
    return bytes(out),dict(input_sha256=INPUT_SHA256,output_sha256=hashlib.sha256(out).hexdigest(),input_size=len(data),output_size=len(out),callback_va=hex(new_va),callback_size=len(code),registration_va=hex(REGISTRATION_VA),old_callback_va=hex(OLD_CALLBACK),changed_admissions=[],native_dependencies=['existing model constructor','existing model reset','native per-frame battle UI selection'],full_ui_acceptance=False)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
    if len({x.resolve() for x in(a.input,a.output,a.receipt)})!=3 or a.output.exists() or a.receipt.exists():p.error('distinct immutable input and new outputs required')
    output,receipt=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(output)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))

if __name__=='__main__':main()
