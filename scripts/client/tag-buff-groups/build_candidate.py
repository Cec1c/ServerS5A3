"""Fixed-image two-group tag Buff receiver; never installs or starts the client."""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('tag_group_asm', HERE.parent/'buff-group-registration/build_candidate.py')
support = importlib.util.module_from_spec(spec)
spec.loader.exec_module(support)
BASE = '6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4'
CURRENT_BASE = '28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'
SUPPORTED_BASES = frozenset((BASE, CURRENT_BASE))
ROLE = -0x178  # original raw role byte remains live through this entry


def payload(base):
    A = support.Asm
    role = A(base)
    role.raw('0fb68588feffff 83e07f')
    role.jmp(0x145dd86)
    clear = A(base+0x40)
    clear.raw('f68588feffff80'); clear.branch('0f85', 'preserve')
    clear.jmp(0x997c30)
    clear.label('preserve'); clear.raw('c20400')
    select = A(base+0x80)
    select.raw('8b8e58040000 53 50 0fb68588feffff c1e807 50 51')
    select.jmp(0x145de5e)
    row = A(base+0xc0)
    row.raw('8b8258040000 50 0fb68588feffff c1e807 50')
    row.jmp(0x145e0c7)
    return [(base,role.finish()),(base+0x40,clear.finish()),
            (base+0x80,select.finish()),(base+0xc0,row.finish())]


def build(raw):
    input_sha = hashlib.sha256(raw).hexdigest()
    if input_sha not in SUPPORTED_BASES:
        raise ValueError('fixed formal cumulative input differs')
    pe = support.p.PE(raw)
    pe.require_contiguous()
    candidates=[]
    for section in pe.sections:
        start=support.p.align(section['virtual_size'],16)
        at=section['raw']+start
        if (section['characteristics']==0x60000020 and section['raw_size']-start>=0x100
                and raw[at:at+0x100]==bytes(0x100)):
            candidates.append((section,start))
    if not candidates:
        raise ValueError('no verified RX padding for tag group helpers')
    section,delta=candidates[-1]
    base=pe.base+section['rva']+delta
    chunks=payload(base)
    out=bytearray(raw);ranges=[];hooks=[]
    def patch(va,before,after):
        assert len(before)==len(after) and pe.read(va,len(before))==before,hex(va)
        at=pe.offset(va,len(before));out[at:at+len(after)]=after
        ranges.append((at,at+len(after)))
        hooks.append(dict(va=va,before=before.hex(),after=after.hex()))
    def jump(va,before,target):
        patch(va,bytes.fromhex(before),b'\xe9'+struct.pack('<i',target-va-5)+b'\x90'*(len(bytes.fromhex(before))-5))
    def call(va,target,old):
        patch(va,b'\xe8'+struct.pack('<i',old-va-5),b'\xe8'+struct.pack('<i',target-va-5))
    jump(0x145dd7f,'0fb68588feffff',base)
    call(0x145ddba,base+0x40,0x997c30)
    call(0x145de48,base+0x40,0x997c30)
    jump(0x145de54,'8b8e58040000',base+0x80)
    jump(0x145e0bf,'8b8258040000',base+0xc0)
    for va,code in chunks:
        at=pe.offset(va,len(code));assert raw[at:at+len(code)]==bytes(len(code))
        out[at:at+len(code)]=code;ranges.append((at,at+len(code)))
    end=delta+max(va-base+len(code) for va,code in chunks)
    header=pe.st+section['index']*40+8
    struct.pack_into('<I',out,header,end);ranges.append((header,header+4))
    struct.pack_into('<I',out,pe.opt+64,support.p.pe_checksum(out,pe.opt+64));ranges.append((pe.opt+64,pe.opt+68))
    restored=bytearray(out)
    for a,b in ranges:restored[a:b]=raw[a:b]
    assert restored==raw and len(out)==len(raw)
    support.p.PE(out).require_contiguous()
    receipt=dict(inputSHA256=input_sha,outputSHA256=hashlib.sha256(out).hexdigest(),
        base=base,section=section['name'],rawRoleLocal=ROLE,frameUnchanged=True,hooks=hooks,ranges=ranges,
        chunks=[dict(va=va,hex=code.hex()) for va,code in chunks],installed=False)
    return bytes(out),receipt


if __name__=='__main__':
    cli=argparse.ArgumentParser(description=__doc__)
    cli.add_argument('--input',type=Path,required=True)
    cli.add_argument('--output-dir',type=Path,required=True)
    args=cli.parse_args()
    raw,receipt=build(args.input.read_bytes())
    args.output_dir.mkdir(parents=True,exist_ok=False)
    (args.output_dir/'DNF.exe').write_bytes(raw)
    (args.output_dir/'build.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf8')
    print(json.dumps(receipt))
