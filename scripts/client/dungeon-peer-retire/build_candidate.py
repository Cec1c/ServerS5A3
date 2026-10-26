"""Build the fixed v166 dungeon member-retirement correction; never install it."""
from pathlib import Path
import argparse
import hashlib
import json
import struct
import sys

import helpers

INPUT_SHA = '0e16090f0e4e3ac830f70183c814f5b24724cec235935e6c1221bf2d865d4243'
INPUT_SIZE = 111902720
IMAGE_BASE = 0x400000
SITES = (0x20175B8, 0x2017359)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, offset):
    return struct.unpack_from('<H', data, offset)[0]


def u32(data, offset):
    return struct.unpack_from('<I', data, offset)[0]


def align(value, alignment):
    require(alignment > 0 and alignment & (alignment - 1) == 0, 'invalid alignment')
    return (value + alignment - 1) & ~(alignment - 1)


def checksum(data, offset):
    require(len(data) % 2 == 0, 'odd file size')
    words = memoryview(data).cast('H')
    value = sum(words) - words[offset // 2] - words[offset // 2 + 1]
    while value >> 16:
        value = (value & 65535) + (value >> 16)
    return (value + len(data)) & 0xffffffff


class PE:
    def __init__(self, data):
        self.data = data
        require(data[:2] == b'MZ', 'MZ missing')
        self.nt = u32(data, 0x3c)
        require(data[self.nt:self.nt+4] == b'PE\0\0', 'PE missing')
        self.opt = self.nt + 24
        require(u16(data, self.nt+4) == 0x14c and u16(data, self.opt) == 0x10b, 'requires x86 PE32')
        require(u32(data, self.opt+28) == IMAGE_BASE, 'image base differs')
        self.count = u16(data, self.nt+6)
        self.table = self.opt + u16(data, self.nt+20)
        self.sa, self.fa = u32(data, self.opt+32), u32(data, self.opt+36)
        self.sections = []
        for index in range(self.count):
            at = self.table + index * 40
            vs, rva, rs, raw = struct.unpack_from('<IIII', data, at+8)
            self.sections.append(dict(name=data[at:at+8].rstrip(b'\0').decode('ascii'),
                                      vsize=vs, va=IMAGE_BASE+rva, raw_size=rs, raw=raw))

    def offset(self, va, size=1):
        for s in self.sections:
            delta = va-s['va']
            if 0 <= delta and delta+size <= s['raw_size']:
                return s['raw']+delta
        raise ValueError('address not backed by file: '+hex(va))

    def read(self, va, size):
        offset = self.offset(va, size)
        return self.data[offset:offset+size]


def call(site, target):
    return b'\xe8'+struct.pack('<i', target-site-5)


def build(data):
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA, 'fixed input differs')
    pe = PE(data)
    require(u16(data, pe.opt+70) & 0x40 == 0, 'relocated input unsupported')
    require(data[pe.opt+128:pe.opt+136] == bytes(8), 'signed input unsupported')
    require(len(data) == max(s['raw']+s['raw_size'] for s in pe.sections), 'overlay unsupported')
    header = pe.table + 40*pe.count
    require(header+40 <= min(s['raw'] for s in pe.sections if s['raw_size']), 'no section header space')
    require(header+40 <= u32(data, pe.opt+60), 'header outside SizeOfHeaders')
    require(data[header:header+40] == bytes(40), 'nonempty header padding')
    rva = u32(data, pe.opt+56)
    require(rva == align(max(s['va']-IMAGE_BASE+max(s['vsize'],s['raw_size']) for s in pe.sections),pe.sa), 'image end mismatch')
    base = IMAGE_BASE+rva
    remove, kind0 = helpers.emit(base)
    require(len(remove) <= 0x200, 'helper overlap')
    payload = remove+bytes(0x200-len(remove))+kind0
    raw = align(len(data),pe.fa)
    raw_size = align(len(payload),pe.fa)
    out = bytearray(data)+bytes(raw-len(data))+payload+bytes(raw_size-len(payload))
    edits=[]
    patches=[]
    for site,target in zip(SITES,(base,base+0x200)):
        original = call(site,0x29733B0)
        require(pe.read(site,5)==original,'original call differs')
        at=pe.offset(site,5)
        out[at:at+5]=call(site,target)
        edits.append((at,at+5))
        patches.append(dict(site=hex(site),target=hex(target),before=original.hex(),after=out[at:at+5].hex()))
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.prsafe',len(payload),rva,raw_size,raw,0,0,0,0,0x60000020)
    struct.pack_into('<H',out,pe.nt+6,pe.count+1)
    struct.pack_into('<I',out,pe.opt+4,u32(data,pe.opt+4)+raw_size)
    struct.pack_into('<I',out,pe.opt+56,align(rva+len(payload),pe.sa))
    struct.pack_into('<I',out,pe.opt+64,checksum(out,pe.opt+64))
    edits += [(header,header+40),(pe.nt+6,pe.nt+8),(pe.opt+4,pe.opt+8),(pe.opt+56,pe.opt+60),(pe.opt+64,pe.opt+68)]
    cursor=0
    for left,right in sorted(edits):
        require(out[cursor:left]==data[cursor:left], 'unapproved original bytes changed')
        cursor=right
    require(out[cursor:len(data)]==data[cursor:], 'unapproved final original bytes changed')
    final=PE(out)
    require(final.read(base,len(remove))==remove and final.read(base+0x200,len(kind0))==kind0,'helper readback')
    require(u32(out,pe.opt+64)==checksum(out,pe.opt+64),'checksum mismatch')
    return bytes(out),dict(inputSha256=INPUT_SHA,outputSha256=sha(out),size=len(out),base=hex(base),
        section='.prsafe',helpers=[dict(va=hex(base),size=len(remove),sha256=sha(remove)),dict(va=hex(base+0x200),size=len(kind0),sha256=sha(kind0))],
        patches=patches,allowedOriginalByteRanges=sorted(edits),installed=False,released=False,
        scope='op9 kind3 preserves other members active actors; kind0 restores only a null binding in dungeon route1/owner0')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',required=True,type=Path)
    parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args()
    require(not sys.flags.optimize,'optimization mode forbidden')
    require(not args.output.exists(),'output already exists')
    data,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f:
        f.write(data)
    require(sha(args.output.read_bytes())==receipt['outputSha256'],'disk readback differs')
    print(json.dumps(receipt,indent=2))


if __name__=='__main__':
    main()
