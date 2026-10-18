"""Build a pinned V126+475 candidate with character-owned automatic story gates.

Requires pefile. Never executes the client. Existing input sections, including
author additions and opcode 475, are retained. Outputs must be new files.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile

SOURCE_SHA256 = '9276e7a83312517ec1cb36b02a5a74bd4880235a46ce3d27f47f54f5df5d5ab6'
PVF_SHA256 = 'fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525'
ORIGINAL = 0x1454100
CLONE_SIZE = 0x244
CLONE_SHA256 = '9485a9d130b97c38ae89d190cef74b1e3ad185c2539749d3805f92f3e65d483c'
CALL_OFFSETS = (0x10,0x28,0x3a,0x62,0x6e,0x7a,0x86,0x92,0x9f,0xab,0xc8,
                0xd1,0xe1,0xeb,0xf8,0x10b,0x115,0x134,0x13e,0x149,0x171,
                0x17d,0x189,0x195,0x1a1,0x1b1,0x1b8,0x1bd,0x1e2,0x1e9)
ABS_OFFSETS = (0x52,0x59,0x161,0x168,*range(0x1fc,0x210,4),*range(0x220,0x234,4))
AUTO_CALL = 0x2044db3
SNAPSHOT_CALL = 0x20096f0
CLEAR_ENTRY = 0x2921670
COLLECTION = 0x56bde00
PLAYER_MANAGER = 0x56a3758
SELECT_SUCCESS = 0x1fb2239
RETURN_SUCCESS = 0x1fb912d


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def align(n, size):
    return (n + size - 1) // size * size


def rel(op, start, target):
    return bytes.fromhex(op) + struct.pack('<i', target - start - len(bytes.fromhex(op)) - 4)


class Code:
    """Small fixed x86 emitter: labels are resolved as signed rel32 fields."""
    def __init__(self, va):
        self.va, self.data, self.labels, self.fixups = va, bytearray(), {}, []

    def emit(self, value):
        self.data.extend(bytes.fromhex(value))

    def imm(self, value):
        self.data.extend(struct.pack('<I', value))

    def label(self, name):
        self.labels[name] = self.va + len(self.data)

    def branch(self, op, target):
        self.emit(op)
        self.fixups.append((len(self.data), target))
        self.data.extend(bytes(4))

    def finish(self):
        for offset, target in self.fixups:
            target = self.labels[target] if isinstance(target, str) else target
            struct.pack_into('<i', self.data, offset, target - self.va - offset - 4)
        return bytes(self.data)


def make_code(original, code_va, state_va):
    clone = bytearray(original)
    delta = code_va - ORIGINAL
    for offset in CALL_OFFSETS:
        if clone[offset] != 0xe8:
            raise ValueError('native call relocation mismatch')
        distance = struct.unpack_from('<i', clone, offset + 1)[0]
        struct.pack_into('<i', clone, offset + 1, distance - delta)
    for offset in ABS_OFFSETS:
        address = struct.unpack_from('<I', clone, offset)[0]
        if not ORIGINAL <= address < ORIGINAL + CLONE_SIZE:
            raise ValueError('native table relocation mismatch')
        struct.pack_into('<I', clone, offset, address + delta)

    gate_va, snapshot_va, clear_va = code_va + 0x280, code_va + 0x400, code_va + 0x500
    # The original function has already selected target descriptor ESI and the
    # first scene index [EBP-4]. Nothing has loaded video, written last_level or
    # sent 1443 at this point. Only this private automatic copy is changed.
    clone[0x129:0x12f] = rel('e9', code_va + 0x129, gate_va) + b'\x90'
    g = Code(gate_va)
    g.emit('9c 60 31d2')                       # pushfd/pushad; required mask EDX
    g.emit('8b462c 3d86030000')                # target ending == 902
    g.branch('0f85', 'ending903')
    g.emit('83ca01')
    g.label('ending903'); g.emit('3d87030000')
    g.branch('0f85', 'scenes')
    g.emit('83ca02')
    g.label('scenes'); g.emit('8b4634 8b4dfc 8d0488 8b7e38')
    g.label('loop'); g.emit('3bc7')            # suffix [first_new, end)
    g.branch('0f83', 'check')
    g.emit('833802'); g.branch('0f85', 'scene3')
    g.emit('83ca01')
    g.label('scene3'); g.emit('833803'); g.branch('0f85', 'next')
    g.emit('83ca02')
    g.label('next'); g.emit('83c004'); g.branch('e9', 'loop')
    g.label('check'); g.emit('85d2'); g.branch('0f84', 'allow')
    g.emit('a1'); g.imm(state_va)              # nonzero owner means ready
    g.emit('85c0'); g.branch('0f84', 'deny')
    g.emit('3b45f8'); g.branch('0f85', 'deny') # actual selected player, from native
    g.emit('a1'); g.imm(state_va + 4)
    g.emit('21d0 3bc2'); g.branch('0f85', 'deny')
    g.label('allow'); g.emit('61 9d 837b1400') # replay displaced cmp/jne
    g.branch('0f85', code_va + 0x146)
    g.branch('e9', code_va + 0x12f)
    g.label('deny'); g.emit('61 9d')
    g.branch('e9', code_va + 0x11e)           # native false epilogue, ret4

    s = Code(snapshot_va)
    s.emit('55 8bec ff750c ff7508')
    s.branch('e8', 0x2921a50)                # unmodified actual table loader
    s.emit('9c 60 c705'); s.imm(state_va); s.imm(0)
    s.emit('8b7508 31d2 31c0 80be760c000000 0f95c2') # table[3190] != 0
    s.emit('80be8f0c000000 0f95c0 d1e0 09d0 a3'); s.imm(state_va + 4)
    s.emit('8b0d'); s.imm(PLAYER_MANAGER)
    s.emit('85c9'); s.branch('0f84', 'done')
    s.branch('e8', 0x296dd80)                # bind to actual selected actor
    s.emit('a3'); s.imm(state_va)
    s.label('done'); s.emit('61 9d 8be5 5d c20800')

    c = Code(clear_va)
    c.emit('9c 60 81f9'); c.imm(COLLECTION)
    c.branch('0f85', 'replay')
    c.emit('c705'); c.imm(state_va); c.imm(0)
    c.emit('c705'); c.imm(state_va + 4); c.imm(0)
    c.label('replay'); c.emit('61 9d 56 8bf1 8d4e14')
    c.branch('e9', CLEAR_ENTRY + 6)           # native clearing continues

    # Success-only lifecycle boundaries are independent of the allocator and
    # channel initialization. Failure replies retain the current valid snapshot.
    lifecycle=[]
    for at,resume,displaced in ((code_va+0x600,SELECT_SUCCESS+6,'8b0d98376a05'),
                                 (code_va+0x680,RETURN_SUCCESS+6,'56b9c0a56c05')):
        x=Code(at);x.emit('9c 60 c705');x.imm(state_va);x.imm(0)
        x.emit('c705');x.imm(state_va+4);x.imm(0)
        x.emit('61 9d '+displaced);x.branch('e9',resume)
        lifecycle.append(x.finish())
    result = bytearray(0x700)
    for offset, chunk, limit in ((0,bytes(clone),0x280),(0x280,g.finish(),0x400),
                                  (0x400,s.finish(),0x500),(0x500,c.finish(),0x600),
                                  (0x600,lifecycle[0],0x680),(0x680,lifecycle[1],0x700)):
        if offset + len(chunk) > limit:
            raise ValueError('code slot overflow')
        result[offset:offset + len(chunk)] = chunk
    return bytes(result), dict(clone=code_va, gate=gate_va, snapshot=snapshot_va,
                               clear=clear_va, state=state_va,
                               select_success=code_va+0x600,return_success=code_va+0x680)


def build(data):
    if sha256(data) != SOURCE_SHA256:
        raise ValueError('expected exact approved V126+475 input')
    p = pefile.PE(data=data, fast_load=True)
    if p.FILE_HEADER.Machine != 0x14c or p.OPTIONAL_HEADER.ImageBase != 0x400000:
        raise ValueError('expected fixed-base x86 image')
    if p.OPTIONAL_HEADER.DllCharacteristics & 0x40 or p.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size:
        raise ValueError('relocatable input requires separate review')
    if p.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size:
        raise ValueError('signed input requires separate review')
    base = p.OPTIONAL_HEADER.ImageBase
    offset = lambda va: p.get_offset_from_rva(va-base)
    source = data[offset(ORIGINAL):offset(ORIGINAL)+CLONE_SIZE]
    if sha256(source) != CLONE_SHA256:
        raise ValueError('story function fingerprint mismatch')
    section_align, file_align = p.OPTIONAL_HEADER.SectionAlignment, p.OPTIONAL_HEADER.FileAlignment
    count = len(p.sections)
    table = p.sections[0].get_file_offset()
    new_headers = table + count * 40
    if new_headers + 80 > p.OPTIONAL_HEADER.SizeOfHeaders or any(data[new_headers:new_headers+80]):
        raise ValueError('no unused section header space')
    end_raw = max(s.PointerToRawData + s.SizeOfRawData for s in p.sections)
    if len(data) != end_raw:
        raise ValueError('unexpected overlay; do not truncate it')
    code_rva = align(max(s.VirtualAddress + max(s.Misc_VirtualSize,s.SizeOfRawData) for s in p.sections),section_align)
    code_size = align(0x700, file_align)
    state_rva = align(code_rva + code_size, section_align)
    state_size = align(8, file_align)
    code, entries = make_code(source, base+code_rva, base+state_rva)
    after = bytearray(data)
    changes = []
    def edit(at, before, value, label):
        if bytes(after[at:at+len(before)]) != before or len(before) != len(value):
            raise ValueError('patch fingerprint mismatch: '+label)
        after[at:at+len(value)] = value
        changes.append(dict(offset=at,length=len(value),before=before.hex(),after=value.hex(),purpose=label))
    edit(offset(AUTO_CALL),rel('e8',AUTO_CALL,ORIGINAL),rel('e8',AUTO_CALL,entries['clone']),'automatic call only')
    edit(offset(SNAPSHOT_CALL),rel('e8',SNAPSHOT_CALL,0x2921a50),rel('e8',SNAPSHOT_CALL,entries['snapshot']),'356 table capture after native load')
    edit(offset(CLEAR_ENTRY),bytes.fromhex('568bf18d4e14'),rel('e9',CLEAR_ENTRY,entries['clear'])+b'\x90','invalidate on native collection clear')
    edit(offset(SELECT_SUCCESS),bytes.fromhex('8b0d98376a05'),rel('e9',SELECT_SUCCESS,entries['select_success'])+b'\x90','invalidate on successful character selection')
    edit(offset(RETURN_SUCCESS),bytes.fromhex('56b9c0a56c05'),rel('e9',RETURN_SUCCESS,entries['return_success'])+b'\x90','invalidate on successful return to selection')
    for i,(name,vsize,rva,rawsize,raw,flags) in enumerate([
        (b'.sdgate',len(code),code_rva,code_size,len(data),0x60000020),
        (b'.sdstate',8,state_rva,state_size,len(data)+code_size,0xc0000040)]):
        header=struct.pack('<8sIIIIIIHHI',name,vsize,rva,rawsize,raw,0,0,0,0,flags)
        edit(new_headers+i*40,bytes(40),header,'new section header')
    fields=[(p.FILE_HEADER.get_field_absolute_offset('NumberOfSections'),2,count+2),
            (p.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfImage'),4,align(state_rva+8,section_align)),
            (p.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfCode'),4,p.OPTIONAL_HEADER.SizeOfCode+code_size),
            (p.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfInitializedData'),4,p.OPTIONAL_HEADER.SizeOfInitializedData+state_size)]
    for at,n,value in fields:
        edit(at,data[at:at+n],value.to_bytes(n,'little'),'PE size/count')
    after.extend(code+bytes(code_size-len(code))+bytes(state_size))
    check = p.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    checksum = pefile.PE(data=bytes(after),fast_load=True).generate_checksum()
    edit(check,data[check:check+4],struct.pack('<I',checksum),'PE checksum')
    out = bytes(after)
    # Full original-file preservation check, not merely section/hash samples.
    restore = bytearray(out[:len(data)])
    for change in changes:
        at=change['offset'];restore[at:at+change['length']]=bytes.fromhex(change['before'])
    if bytes(restore) != data or not pefile.PE(data=out,fast_load=True).verify_checksum():
        raise ValueError('preservation/checksum verification failed')
    return out, dict(source_sha256=SOURCE_SHA256,candidate_sha256=sha256(out),size=len(out),
                     source_size=len(data),entries=entries,changes=changes,
                     appended_sha256=sha256(out[len(data):]),pvf_sha256=PVF_SHA256,
                     pe_checksum=checksum,all_other_original_bytes_preserved=True,
                     published=False,deployed=False,native_acceptance=False)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source',type=Path);parser.add_argument('output',type=Path)
    parser.add_argument('--receipt',type=Path,required=True)
    args=parser.parse_args()
    paths=[p.resolve() for p in (args.source,args.output,args.receipt)]
    if len(set(paths)) != 3 or args.output.exists() or args.receipt.exists():
        parser.error('source, new output and new receipt must be distinct; outputs must not exist')
    output,receipt=build(args.source.read_bytes())
    with args.output.open('xb') as stream:stream.write(output)
    with args.receipt.open('x',encoding='utf-8') as stream:json.dump(receipt,stream,indent=2)
    print(json.dumps(receipt))


if __name__=='__main__':
    main()
