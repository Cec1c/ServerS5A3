"""Allow advertised region zero in the native guild founder-auth chooser.
Exact current v123 input only; emits a separate client candidate, no install.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
IMAGE_BASE=0x400000
INPUT_SHA256="7ec090a4519c1d5d9dfabb7276bbdf424fae5d768bfd6ca3f94e8f5694de0e2c"
EDITS=((0x3437bb3,bytes.fromhex('bf01000000'),bytes.fromhex('bf00000000')),
       (0x343a995,bytes.fromhex('8d47ff3df8000000'),bytes.fromhex('8d47003df9000000')))
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



def build(data):
 if sha(data)!=INPUT_SHA256:raise ValueError('exact v123 input SHA-256 mismatch')
 pe,opt,_,sections=headers(data)
 if struct.unpack_from('<I',data,opt+28)[0]!=IMAGE_BASE or len(sections)!=20:raise ValueError('unexpected v123 PE layout')
 if any(struct.unpack_from('<II',data,opt+96+4*8)):raise ValueError('certificate requires separate signing workflow')
 out=bytearray(data);ranges=[]
 for va,old,new in EDITS:
  at=offset_for_rva(sections,va-IMAGE_BASE,len(old))
  if data[at:at+len(old)]!=old or len(old)!=len(new):raise ValueError('unexpected guild region instructions')
  out[at:at+len(old)]=new;ranges.append([at,at+len(old)])
 struct.pack_into('<I',out,opt+64,checksum(out,opt+64));ranges.append([opt+64,opt+68]);result=bytes(out)
 return result,dict(input_sha256=INPUT_SHA256,output_sha256=sha(result),allowed_changed_ranges=ranges,enabled_opcodes=[],full_ui_acceptance=False)

def write_candidate(source,destination):
 source,destination=Path(source).resolve(),Path(destination).resolve()
 if source==destination or destination.exists():raise ValueError('output must be separate new file')
 result,receipt=build(source.read_bytes());destination.parent.mkdir(parents=True,exist_ok=True)
 with destination.open('xb')as f:f.write(result)
 if sha(source.read_bytes())!=INPUT_SHA256 or sha(destination.read_bytes())!=receipt['output_sha256']:raise RuntimeError('input/output changed')
 return receipt

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
