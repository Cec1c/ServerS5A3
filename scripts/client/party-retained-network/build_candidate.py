"""Build a fixed-image NOTI9 alias-record network retention candidate, offline only."""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('retained_pe', HERE.parent/'quiet-profile-refresh/build_candidate.py')
peutils = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peutils)
spec = importlib.util.spec_from_file_location('retained_asm', HERE.parent/'dungeon-peer-retire/helpers.py')
asm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(asm)
INPUT_SHA = '735ed3e96f178bd2b546be18ff38ba4a578c951092b1d97a352ab73db7333d8b'
INPUT_SIZE = 112607232
SITE = 0x2017D97
BASE = 0x6F88100
ORIGINAL = bytes.fromhex('8b0d90376a05')
FALLBACK = 0x2017D9D
RETAIN = 0x2017DA7

def helper():
    x = asm.Code(BASE)
    x.raw('9c 60')  # preserve every register and flags at the original instruction
    x.raw('85 db')  # ebx is the newly bound record in the native join branch
    x.je('fallback')
    x.raw('83 7b 70 01')
    x.jne('fallback')
    x.raw('8b 3d 68376a05 85 ff')  # current active party manager
    x.je('fallback')
    x.raw('a1 b06f7b05 85 c0')  # current local drawable
    x.je('fallback')
    x.raw('8b 0d 58376a05 85 c9')
    x.je('fallback')
    x.call(0x296DD80)
    x.raw('85 c0')
    x.je('fallback')
    x.raw('39 98 c8040000')
    x.jne('fallback')
    x.raw('83 7b 74 00')
    x.je('owner_ok')
    x.raw('89 c1')
    x.call(0x2964680)
    x.raw('0f b6 c0 85 c0')
    x.je('fallback')
    x.raw('39 43 74')
    x.jne('fallback')
    x.label('owner_ok')
    x.raw('31 f6 31 ed 31 c9 31 d2')  # index, retained count, self count, connected peer count
    x.label('loop')
    x.raw('8b 44 b3 2c 3d ffff0000')
    x.je('next')  # removal is permitted; native reconciliation retires that slot
    x.raw('85 c0')
    x.je('fallback')
    x.raw('8b 44 b7 48 85 c0')
    x.je('fallback')  # a newly added slot requires the original network reset
    x.raw('50 0f b7 40 20 3b 44 b3 2c 58')
    x.jne('fallback')  # reordered/replaced identities are never retained
    x.raw('83 b8 f8040000 00')
    x.je('fallback')
    x.raw('45 50 8b 80 f8040000 3b 05 b06f7b05 58')
    x.jne('peer')
    x.raw('41')
    x.jump('next')
    x.label('peer')
    x.raw('83 3c b5 e8276d06 ff')
    x.je('next')
    x.raw('42')
    x.label('next')
    x.raw('46 83 fe 08')
    x.branch('0f82', 'loop')
    x.raw('83 fd 02')
    x.branch('0f82', 'fallback')
    x.raw('83 f9 01')
    x.jne('fallback')
    x.raw('85 d2')
    x.je('fallback')
    x.raw('61 9d')
    x.jump(RETAIN)
    x.label('fallback')
    x.raw('61 9d ' + ORIGINAL.hex())
    x.jump(FALLBACK)
    return x.finish()

def build(data):
    require = peutils.require
    require(len(data)==INPUT_SIZE and hashlib.sha256(data).hexdigest()==INPUT_SHA, 'fixed input differs')
    pe = peutils.PE(data)
    pe.require_contiguous()
    require(pe.read(SITE, len(ORIGINAL))==ORIGINAL, 'native join site changed')
    require(pe.read(0x2017DA5, 2)==b'\xff\xd0', 'native reset call changed')
    section = next(s for s in pe.sections if s['name']=='.gld127')
    require(section['characteristics']==0x60000020, 'cave is not RX')
    code = helper()
    delta = BASE-pe.base-section['rva']
    require(delta>=section['virtual_size'] and delta+len(code)<=section['raw_size'], 'helper exceeds unused padding')
    cave = pe.offset(BASE, len(code)); site = pe.offset(SITE, len(ORIGINAL))
    require(data[cave:cave+len(code)]==bytes(len(code)), 'cave occupied')
    header = pe.st+section['index']*40+8
    out = bytearray(data)
    out[cave:cave+len(code)] = code
    out[site:site+6] = b'\xe9'+struct.pack('<i',BASE-SITE-5)+b'\x90'
    struct.pack_into('<I',out,header,delta+len(code))
    struct.pack_into('<I',out,pe.opt+64,peutils.pe_checksum(out,pe.opt+64))
    ranges = sorted([(cave,cave+len(code)),(site,site+6),(header,header+4),(pe.opt+64,pe.opt+68)])
    restored = bytearray(out)
    for a,b in ranges: restored[a:b]=data[a:b]
    require(restored==data, 'unapproved byte changes')
    require(peutils.PE(out).read(BASE,len(code))==code, 'helper readback failed')
    return bytes(out), dict(inputSha256=INPUT_SHA,outputSha256=hashlib.sha256(out).hexdigest(),size=len(out),helperVA=hex(BASE),helperSize=len(code),allowedRanges=ranges,installed=False,released=False)

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',required=True,type=Path)
    parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args()
    assert not args.output.exists()
    output,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f:f.write(output)
    assert hashlib.sha256(args.output.read_bytes()).hexdigest()==receipt['outputSha256']
    print(json.dumps(receipt,indent=2))
