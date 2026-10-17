"""Match particular-hell ticket display/precheck to the server's entry cost.

Exact v121 input only; creates a separate client candidate. No installation.
The original client computes weight**(used+1); the server and reported actual
charges use (used+1)**weight. Only these two call sites change.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

IMAGE_BASE=0x400000
INPUT_SHA256="9145c197eaa8fab4747e82b0285880558dacdf69163a8f86024211e2d24065db"
UI_START=0x158f6c1
UI_END=0x158f728
ENTRY_CALL=0x1fab5d2
# x87 log2/exp2 power, positive integer base and floating PVF exponent.
# Signature remains (float weight, int usedPlusOne), returning float in ST0.
# EBP and all nonvolatile registers are preserved; exactly one x87 result.
POWER=bytes.fromhex("558bec83ec04d94508db450cd9f1d9c0d9fcd9c9d8e1d9f0d9e8dec1d9fdddd9d95dfcd945fcc9c3")
OLD_UI=bytes.fromhex("8bd88d7301e815dd9c00d99dc8fdffff8bc685f67902f7d8f30f100d54600105f30f1095c8fdffff0f28c18d642400a8017404f30f59c2d1e87406f30f59d2ebee85f67906f30f5ec8eb030f28c8660f6e85e0fdffff0f5bc0f30f59c1f30f2cc08985e0fdffff")

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



def relative(opcode,at,target):
    return bytes([opcode])+struct.pack("<i",target-at-5)

def ui_code(power_va):
    # Preserve used in EBX and used+1 in ESI exactly as the original block.
    code=bytearray.fromhex("8bd88d730156")
    code+=relative(0xe8,UI_START+len(code),0x1f5d3e0)
    code+=bytes.fromhex("51d91c24")
    code+=relative(0xe8,UI_START+len(code),power_va)
    code+=bytes.fromhex("83c408d99dc8fdfffff30f108dc8fdffff660f6e85e0fdffff0f5bc0f30f59c1f30f2cc08985e0fdffff")
    code+=relative(0xe9,UI_START+len(code),UI_END)
    if len(code)>UI_END-UI_START:raise ValueError("UI replacement exceeds proven span")
    return bytes(code)+b"\x90"*(UI_END-UI_START-len(code))

def build(data):
    if sha(data)!=INPUT_SHA256:raise ValueError("exact v121 input SHA-256 mismatch")
    pe,opt,table,sections=headers(data)
    if struct.unpack_from("<I",data,opt+28)[0]!=IMAGE_BASE or len(sections)!=18:
        raise ValueError("unexpected v121 PE layout")
    if any(struct.unpack_from("<II",data,opt+96+4*8)):
        raise ValueError("PE certificate requires a separate signing workflow")
    sa,fa=struct.unpack_from("<II",data,opt+32)
    hdr=table+len(sections)*40;header_size=struct.unpack_from("<I",data,opt+60)[0]
    if hdr+40>header_size or any(data[hdr:hdr+40]):raise ValueError("no unused section header")
    end=max(s["offset"]+s["rawsize"] for s in sections)
    if end!=len(data) or align(end,fa)!=end:raise ValueError("unexpected overlay or alignment")
    rva=align(max(s["rva"]+max(s["size"],s["rawsize"]) for s in sections),sa)
    if rva!=struct.unpack_from("<I",data,opt+56)[0]:raise ValueError("unexpected image size")
    power_va=IMAGE_BASE+rva
    ui=offset_for_rva(sections,UI_START-IMAGE_BASE,len(OLD_UI))
    call=offset_for_rva(sections,ENTRY_CALL-IMAGE_BASE,5)
    if data[ui:ui+len(OLD_UI)]!=OLD_UI or data[call:call+5]!=relative(0xe8,ENTRY_CALL,0x1441140):
        raise ValueError("unexpected native cost arithmetic")
    rawsize=align(len(POWER),fa)
    out=bytearray(data)+POWER+bytes(rawsize-len(POWER))
    out[ui:ui+len(OLD_UI)]=ui_code(power_va)
    out[call:call+5]=relative(0xe8,ENTRY_CALL,power_va)
    out[hdr:hdr+40]=struct.pack("<8sIIIIIIHHI",b".riftfee",len(POWER),rva,rawsize,end,0,0,0,0,0x60000020)
    struct.pack_into("<H",out,pe+6,len(sections)+1)
    struct.pack_into("<I",out,opt+4,struct.unpack_from("<I",data,opt+4)[0]+rawsize)
    struct.pack_into("<I",out,opt+56,align(rva+len(POWER),sa))
    struct.pack_into("<I",out,opt+64,checksum(out,opt+64))
    result=bytes(out)
    return result,dict(input_sha256=INPUT_SHA256,output_sha256=sha(result),power_va=hex(power_va),new_section_offset=end,new_section_size=rawsize,allowed_changed_ranges=[[ui,ui+len(OLD_UI)],[call,call+5],[hdr,hdr+40],[pe+6,pe+8],[opt+4,opt+8],[opt+56,opt+60],[opt+64,opt+68]],full_ui_acceptance=False)

def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():raise ValueError("output must be a separate new file")
    result,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open("xb") as f:f.write(result)
    if sha(source.read_bytes())!=INPUT_SHA256 or sha(destination.read_bytes())!=receipt["output_sha256"]:
        raise RuntimeError("candidate input or output changed")
    return receipt

if __name__=="__main__":
    p=argparse.ArgumentParser(description=__doc__);p.add_argument("--input",type=Path,required=True);p.add_argument("--output",type=Path,required=True);args=p.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
