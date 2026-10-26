#!/usr/bin/env python3
"""Build a file-only v160 candidate for the empty self-pointer cache gate.

Only the existing .locface helper, its VirtualSize and PE CheckSum change. The
canonical getter may write scene+0x70; there is no model/scene registration.
This script does not launch or install the executable.
"""
import argparse
import array
import hashlib
import json
import struct
import sys
import tempfile
from pathlib import Path

BASE_SHA256 = "a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1"
BASE_SIZE = 111890432
OLD_HELPER_SHA256 = "0e80a4f78cb79810acc4c670be6bfd754f4a854a8fee8e4652a5aaf2cfc4573d"
HELPER = 0x6EAE000
OLD_SIZE = 249
PATCH_NEXT = 0x110B16F
CACHE_HIT_RESUME = 0x110B199
OUTPUT_SHA256 = "5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc"


class PE:
    def __init__(self, data):
        self.data = data
        assert data[:2] == b'MZ'
        nt = struct.unpack_from('<I', data, 0x3c)[0]
        assert data[nt:nt+4] == b'PE\0\0'
        assert struct.unpack_from('<H', data, nt+4)[0] == 0x14c
        opt = nt+24
        self.opt = opt
        assert struct.unpack_from('<H', data, opt)[0] == 0x10b
        self.base = struct.unpack_from('<I', data, opt+28)[0]
        table = opt + struct.unpack_from('<H', data, nt+20)[0]
        self.sections = []
        for i in range(struct.unpack_from('<H', data, nt+6)[0]):
            at = table+i*40
            vs,rva,rs,raw = struct.unpack_from('<IIII',data,at+8)
            self.sections.append(dict(at=at,name=data[at:at+8].rstrip(b'\0').decode('ascii'),vs=vs,rva=rva,rs=rs,raw=raw))

    def offset(self, va, size):
        for s in self.sections:
            delta = va-self.base-s['rva']
            if delta >= 0 and delta+size <= s['rs']:
                return s['raw']+delta
        raise ValueError('unmapped VA '+hex(va))

    def read(self, va, size):
        at=self.offset(va,size)
        return self.data[at:at+size]


def pe_checksum(data, offset):
    work = bytearray(data)
    struct.pack_into('<I', work, offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array('H', work)
    if sys.byteorder != 'little':
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xffff) + (total >> 16)
    return (total + len(data)) & 0xffffffff


class Code:
    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[str, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        self.labels[name] = len(self.buf)

    def call(self, va: int) -> None:
        self.buf.append(0xE8)
        self.buf.extend(struct.pack("<i", va - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        self.buf.append(0xE9)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append(("jmp", offset, target))
        else:
            struct.pack_into("<i", self.buf, offset, target - (self.base + offset + 4))

    def je(self, target: str) -> None:
        self.buf.extend(b"\x0f\x84")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("je", offset, target))

    def jne(self, target: str) -> None:
        self.buf.extend(b"\x0f\x85")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("jne", offset, target))

    def finish(self) -> bytes:
        for _, offset, label in self.fixups:
            struct.pack_into("<i", self.buf, offset, self.labels[label] - offset - 4)
        return bytes(self.buf)


def helper(va: int) -> bytes:
    c = Code(va)

    # Relocated original instructions, then a focused register/flags save.
    c.raw("8b4d8c 8b33")
    c.raw("9c 53 56 57")              # pushfd; push ebx; push esi; push edi

    # Empty control returns to the original path.
    c.raw("85c9")
    c.je("original")

    # Local-own gate. Use the canonical self getter to fill its cache if empty.
    c.raw("8b83f4040000 85c0")
    c.jne("original")
    c.raw("83bb3400000010")
    c.jne("original")
    c.raw("8b0d58376a05 85c9")
    c.je("original")
    c.call(0x296DD80)
    c.raw("85c0")
    c.je("original")
    c.raw("3bc3")
    c.jne("original")

    # Current raid namespace. The getter must receive the raid manager.
    c.raw("8b0d78376a05 85c9")
    c.je("original")
    c.call(0x1A553E0)
    c.raw("85c0")
    c.je("original")

    # 1A5A5E0(key, owner, route), read-only across the five registry maps.
    c.raw("0fb65003 0fb64802 52 51")
    c.raw("0fb74b20 51")
    c.call(0x1A5A5E0)
    c.raw("83c40c 85c0")
    c.je("original")

    # Reject a mismatched cached entry or an entry carrying the same sentinel.
    c.raw("0fb75020 0fb74b20 3bd1")
    c.jne("original")
    c.raw("83783410")
    c.je("original")

    # Require the existing nonzero descriptor field; this does not prove that
    # GPU resources are loaded. The embedded descriptor starts at actor+0x30.
    c.raw("8b4830 85c9")
    c.je("original")

    # Existing cached visual info; no allocation and no scene drawable write.
    c.raw("8bc8")
    c.call(0x3C1BEA0)
    c.raw("8b7804")

    # Exact original 110AE90 14x14 vcall geometry and colour.
    c.raw("8b4d8c")
    c.call(0x37E9F40)
    c.raw("83e802 8bf0")
    c.raw("8b4d8c")
    c.call(0x209BBE0)
    c.raw("83e811")

    # 2966C60(x,y,14,14,cacheActor+34,0,0xFF)
    c.raw("68ff000000 6a00 57 6a0e 6a0e 56 50")
    c.call(0x2966C60)
    c.raw("83c41c")
    c.raw("5f 5e 5b 9d")
    c.jump(CACHE_HIT_RESUME)

    c.label("original")
    c.raw("5f 5e 5b 9d")
    c.raw("8b4d8c")
    c.jump(PATCH_NEXT)

    return c.finish()


def build(data):
    assert len(data) == BASE_SIZE and hashlib.sha256(data).hexdigest() == BASE_SHA256, 'fixed v160 required'
    pe = PE(data)
    assert struct.unpack_from('<I', data, pe.opt+64)[0] == pe_checksum(data,pe.opt+64), 'baseline checksum mismatch'
    section, = [s for s in pe.sections if pe.base+s['rva']==HELPER]
    assert section['name']=='.locface' and section['vs']==OLD_SIZE and section['rs']==4096
    original = pe.read(HELPER, OLD_SIZE)
    assert hashlib.sha256(original).hexdigest()==OLD_HELPER_SHA256
    code = helper(HELPER)
    assert len(code)==251
    assert pe.read(HELPER+OLD_SIZE, len(code)-OLD_SIZE)==bytes(len(code)-OLD_SIZE)
    site=pe.read(0x110B16A,5)
    assert site[0]==0xe9 and 0x110B16A+5+struct.unpack_from('<i',site,1)[0]==HELPER
    out=bytearray(data)
    at=pe.offset(HELPER,len(code))
    out[at:at+len(code)]=code
    struct.pack_into('<I',out,section['at']+8,len(code))
    checksum = pe_checksum(out, pe.opt+64)
    struct.pack_into('<I',out,pe.opt+64,checksum)
    allowed=set(range(at,at+len(code)))|set(range(section['at']+8,section['at']+12))|set(range(pe.opt+64,pe.opt+68))
    changed=[i for i,(a,b) in enumerate(zip(data,out)) if a!=b]
    assert changed and all(i in allowed for i in changed) and len(out)==len(data)
    digest=hashlib.sha256(out).hexdigest()
    if OUTPUT_SHA256 is not None:
        assert digest==OUTPUT_SHA256
    parsed=PE(out)
    assert parsed.read(HELPER,len(code))==code
    final_section, = [s for s in parsed.sections if parsed.base+s['rva']==HELPER]
    assert final_section == dict(section,vs=len(code))
    assert struct.unpack_from('<I',out,parsed.opt+64)[0] == pe_checksum(out,parsed.opt+64)
    receipt=dict(baseSha256=BASE_SHA256,candidateSha256=digest,size=len(out),helperAddress=hex(HELPER),
        helperSize=len(code),helperSha256=hashlib.sha256(code).hexdigest(),changedByteCount=len(changed),
        oldHelperSize=OLD_SIZE,oldHelperSha256=OLD_HELPER_SHA256,
        checksum=checksum,
        patches=[dict(address=hex(HELPER),fileOffset=at,size=len(code)),dict(field='locface.VirtualSize',fileOffset=section['at']+8,size=4),dict(field='optionalHeader.CheckSum',fileOffset=pe.opt+64,size=4)],
        sceneCacheMutation='canonical 296DD80 may fill scene+0x70; actor, model and registry data are not patched',
        installed=False,processAccess=False)
    return bytes(out),receipt


def main():
    if not __debug__:
        raise SystemExit('optimized Python unsupported')
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',type=Path,required=True)
    p.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    assert not args.output.exists(), 'output must be new'
    assert args.output.parent.is_dir(), 'output parent missing'
    data,receipt=build(args.input.read_bytes())
    with tempfile.TemporaryDirectory(prefix='.a03-staging-',dir=args.output.parent) as td:
        staging=Path(td)/'result'
        staging.mkdir()
        exe=staging/'DNF-a03-self-cache-candidate.exe'
        exe.write_bytes(data)
        assert hashlib.sha256(exe.read_bytes()).hexdigest()==receipt['candidateSha256']
        (staging/'build-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf8',newline='\n')
        staging.rename(args.output)
    print(json.dumps(receipt))


if __name__=='__main__':
    main()
