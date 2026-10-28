"""Build a separate cumulative client with online guild-row proxy creation."""
import argparse
import json
from pathlib import Path
import struct

import patch_guild_member_menu_v118 as pe
from patch_guild_grade_region import checksum

INPUT_SHA256 = '5465ab19a41e51b2fd51d10be47a47bae6309d394321a1c298dc76bd61f3f478'
OUTPUT_SHA256 = '01b313275faf7b598211f4d8adb932e1b31ce4347827583a2df3cfa4d350e94c'
SITE = 0x11B894E
EXPECTED = bytes.fromhex('837f3c00743e')


def rel(op, address, target):
    return op + struct.pack('<i', target - address - len(op) - 4)


def helper(base):
    code, labels, jumps = bytearray(), {}, []
    def emit(value): code.extend(bytes.fromhex(value))
    def call(target): code.extend(rel(b'\xe8', base + len(code), target))
    def jump(op, label):
        jumps.append((len(code), bytes.fromhex(op), label))
        code.extend(bytes(len(bytes.fromhex(op)) + 4))
    def label(name): labels[name] = base + len(code)
    emit('9c60')  # Preserve the intercepted UI caller's flags/registers.
    emit('837f3c00'); jump('0f85', 'done')
    emit('80bf4001000000'); jump('0f85', 'done')
    emit('8b473485c0'); jump('0f8e', 'done')
    emit('3dff000000'); jump('0f83', 'done')
    emit('8b573885d2'); jump('0f84', 'done')
    emit('81faffff0000'); jump('0f83', 'done')
    emit('837f1400'); jump('0f84', 'done')
    # Native category-4 auxiliary identity: UID, real channel, directory.
    emit('0fb64f3181f9fa000000'); jump('0f83', 'done')
    emit('516a045052'); call(0x1A62AA0); emit('83c41085c0'); jump('0f84', 'done')
    emit('8bf08bce'); call(0x3C1BEA0); emit('8bd8')
    emit('8d4704837f1808'); jump('0f82', 'name')
    emit('8b4704')
    label('name'); emit('508bcb'); call(0x37E3000)
    emit('8b47248943048b47288943088b472c89430c0fb6878201000089837c030000')
    # Set level through the native protected-value setter matching its address.
    emit('8d4b148d51048d472052508bc183e00f')
    for value in (0,4,8,12):
        emit('83f8' + f'{value:02x}'); jump('0f84', f'level{value}')
    call(0x37DF010); jump('e9', 'level_done')
    for value, target in ((0,0x37DF130),(4,0x37DF1B0),(8,0x37DF230),(12,0x37DF2B0)):
        label(f'level{value}'); call(target); jump('e9', 'level_done')
    label('level_done'); emit('89773c')
    label('done'); emit('619d837f3c00')
    code.extend(rel(b'\x0f\x84', base + len(code), 0x11B8992))
    code.extend(rel(b'\xe9', base + len(code), SITE + len(EXPECTED)))
    for at, op, name in jumps: code[at:at+len(op)+4] = rel(op,base+at,labels[name])
    return bytes(code)


def build(data):
    if pe.sha(data) != INPUT_SHA256: raise ValueError('exact cumulative vending-ack input required')
    header, opt, table, sections = pe.headers(data)
    if struct.unpack_from('<I', data, opt+28)[0] != pe.IMAGE_BASE: raise ValueError('image base differs')
    section_align, file_align = struct.unpack_from('<II',data,opt+32)
    image_size, header_size = struct.unpack_from('<II',data,opt+56)
    slot = table + len(sections)*40
    if slot+40>header_size or any(data[slot:slot+40]): raise ValueError('section header unavailable')
    if any(struct.unpack_from('<II',data,opt+128)): raise ValueError('signed PE unsupported')
    end = max(s['offset']+s['rawsize'] for s in sections)
    rva = pe.align(max(s['rva']+max(s['size'],s['rawsize']) for s in sections),section_align)
    if end!=len(data) or pe.align(end,file_align)!=end or rva!=image_size: raise ValueError('overlay/alignment differs')
    at = pe.offset_for_rva(sections,SITE-pe.IMAGE_BASE,len(EXPECTED))
    if data[at:at+len(EXPECTED)]!=EXPECTED: raise ValueError('guild row site differs')
    code=helper(pe.IMAGE_BASE+rva); size=pe.align(len(code),file_align)
    out=bytearray(data)+code+bytes(size-len(code))
    out[at:at+len(EXPECTED)]=rel(b'\xe9',SITE,pe.IMAGE_BASE+rva)+b'\x90'
    out[slot:slot+40]=struct.pack('<8sIIIIIIHHI',b'.gprof',len(code),rva,size,end,0,0,0,0,0x60000020)
    struct.pack_into('<H',out,header+6,len(sections)+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+size)
    struct.pack_into('<I',out,opt+56,pe.align(rva+len(code),section_align))
    struct.pack_into('<I',out,opt+64,checksum(out,opt+64))
    ranges=sorted([(at,at+len(EXPECTED)),(slot,slot+40),(header+6,header+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68)])
    cursor=0
    for lo,hi in ranges:
        if out[cursor:lo]!=data[cursor:lo]: raise AssertionError('unlisted change')
        cursor=hi
    if out[cursor:len(data)]!=data[cursor:]: raise AssertionError('unlisted tail change')
    if pe.sha(out)!=OUTPUT_SHA256: raise ValueError('candidate differs from reviewed output')
    return bytes(out),dict(inputSha256=pe.sha(data),outputSha256=pe.sha(out),size=len(out),helperVA=hex(pe.IMAGE_BASE+rva),helperSize=len(code),changedRanges=ranges,appendRange=[end,len(out)])


def write_candidate(source, destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists(): raise ValueError('separate new output required')
    out,receipt=build(source.read_bytes())
    with destination.open('xb') as stream: stream.write(out)
    if pe.sha(source.read_bytes())!=receipt['inputSha256'] or pe.sha(destination.read_bytes())!=receipt['outputSha256']: raise RuntimeError('readback differs')
    return receipt


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',required=True);p.add_argument('--output',required=True);a=p.parse_args()
    print(json.dumps(write_candidate(a.input,a.output),indent=2))
