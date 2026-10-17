"""Skip native wear for zero-move authoritative worn-state acknowledgements.
Requires exact v122 input; creates a separate candidate without installing it.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
IMAGE_BASE=0x400000
INPUT_SHA256="15a24675cea77f526dd8b93f6ec781122e6f148ffd42c8aff9fb0b99267efce4"
SITE=0x1fded8a
CONTINUE=0x1fded90
NOOP_FINISH=0x1fdfca8
OLD_SITE=bytes.fromhex("8b950cffffff")
def sha(data):
    return hashlib.sha256(data).hexdigest()


def align(value, alignment):
    if alignment <= 0 or alignment & (alignment - 1):
        raise ValueError("invalid PE alignment")
    return (value + alignment - 1) & -alignment


def headers(data):
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[:2] != b"MZ" or data[pe:pe + 4] != b"PE\0\0":
        raise ValueError("not a PE image")
    machine, count = struct.unpack_from("<HH", data, pe + 4)
    optsize = struct.unpack_from("<H", data, pe + 20)[0]
    opt = pe + 24
    if machine != 0x14C or struct.unpack_from("<H", data, opt)[0] != 0x10B:
        raise ValueError("not the expected x86 PE32 image")
    table = opt + optsize
    sections = []
    for i in range(count):
        at = table + 40 * i
        name, size, rva, rawsize, offset, _, _, _, _, flags = struct.unpack_from("<8sIIIIIIHHI", data, at)
        sections.append(dict(name=name, size=size, rva=rva, rawsize=rawsize, offset=offset, flags=flags))
    return pe, opt, table, sections


def offset_for_rva(sections, rva, size=1):
    for s in sections:
        delta = rva - s["rva"]
        if 0 <= delta and delta + size <= s["rawsize"]:
            return s["offset"] + delta
    raise ValueError("RVA is outside backed sections")


def checksum(data, field):
    value = 0
    for offset in range(0, len(data), 2):
        if field <= offset < field + 4:
            continue
        word = data[offset] | ((data[offset + 1] if offset + 1 < len(data) else 0) << 8)
        value += word
        value = (value & 0xFFFF) + (value >> 16)
    value = (value & 0xFFFF) + (value >> 16)
    return value + len(data)



def relative(op,at,target):
 return bytes([op])+struct.pack('<i',target-at-5)

def guard_code(va):
 # This site follows complete ACK parsing. Preserve registers and EFLAGS.
 code=bytearray.fromhex('9c83bd0cffffff00');jumps=[]
 jumps.append(len(code));code+=bytes.fromhex('7500')
 code+=bytes.fromhex('80bd20ffffff01');jumps.append(len(code));code+=bytes.fromhex('7700')
 code+=bytes.fromhex('80bd2bffffff03');jumps.append(len(code));code+=bytes.fromhex('7500')
 code+=bytes.fromhex('9d');code+=relative(0xe9,va+len(code),NOOP_FINISH)
 fallback=len(code);code+=bytes.fromhex('9d')+OLD_SITE;code+=relative(0xe9,va+len(code),CONTINUE)
 for at in jumps:code[at+1]=fallback-at-2
 return bytes(code)

def build(data):
 if sha(data)!=INPUT_SHA256:raise ValueError('exact v122 input SHA-256 mismatch')
 pe,opt,table,sections=headers(data)
 if struct.unpack_from('<I',data,opt+28)[0]!=IMAGE_BASE or len(sections)!=19:raise ValueError('unexpected v122 PE layout')
 if any(struct.unpack_from('<II',data,opt+96+4*8)):raise ValueError('certificate requires separate signing workflow')
 sa,fa=struct.unpack_from('<II',data,opt+32);hdr=table+len(sections)*40
 if hdr+40>struct.unpack_from('<I',data,opt+60)[0]or any(data[hdr:hdr+40]):raise ValueError('no unused section header')
 end=max(s['offset']+s['rawsize']for s in sections)
 if end!=len(data)or align(end,fa)!=end:raise ValueError('unexpected overlay/alignment')
 rva=align(max(s['rva']+max(s['size'],s['rawsize'])for s in sections),sa)
 if rva!=struct.unpack_from('<I',data,opt+56)[0]:raise ValueError('unexpected image size')
 site=offset_for_rva(sections,SITE-IMAGE_BASE,len(OLD_SITE))
 if data[site:site+len(OLD_SITE)]!=OLD_SITE:raise ValueError('unexpected parsed ACK site')
 code=guard_code(IMAGE_BASE+rva);rawsize=align(len(code),fa);out=bytearray(data)+code+bytes(rawsize-len(code))
 out[site:site+6]=relative(0xe9,SITE,IMAGE_BASE+rva)+bytes.fromhex('90')
 out[hdr:hdr+40]=struct.pack('<8sIIIIIIHHI',b'.wearnop',len(code),rva,rawsize,end,0,0,0,0,0x60000020)
 struct.pack_into('<H',out,pe+6,len(sections)+1);struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+rawsize)
 struct.pack_into('<I',out,opt+56,align(rva+len(code),sa));struct.pack_into('<I',out,opt+64,checksum(out,opt+64));result=bytes(out)
 return result,dict(input_sha256=INPUT_SHA256,output_sha256=sha(result),guard_va=hex(IMAGE_BASE+rva),guard_hex=code.hex(),allowed_changed_ranges=[[site,site+6],[hdr,hdr+40],[pe+6,pe+8],[opt+4,opt+8],[opt+56,opt+60],[opt+64,opt+68]],full_ui_acceptance=False)

def write_candidate(source,destination):
 source,destination=Path(source).resolve(),Path(destination).resolve()
 if source==destination or destination.exists():raise ValueError('output must be separate new file')
 result,receipt=build(source.read_bytes());destination.parent.mkdir(parents=True,exist_ok=True)
 with destination.open('xb')as f:f.write(result)
 if sha(source.read_bytes())!=INPUT_SHA256 or sha(destination.read_bytes())!=receipt['output_sha256']:raise RuntimeError('input/output changed')
 return receipt

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
