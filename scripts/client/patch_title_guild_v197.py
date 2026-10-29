"""Preserve V196 and repair title recursion and cross-channel adventure routing."""
import argparse
import json
from pathlib import Path
import struct

import patch_guild_member_menu_v118 as pe
from patch_guild_grade_region import checksum

INPUT_SHA256 = '97b1c087eb8a9de35cb5cbd9df327d6bfa8c5e239ce3ad078fde9b9162e6cc23'
TITLE_SITE = 0x25FA98D
PREVIOUS_TITLE = 0x6E952E8
ADVENTURE_SITE = 0x353CF49


def rel(op, address, target):
    return op + struct.pack('<i', target-address-len(op)-4)


def title_helper(base):
    code, labels, jumps = bytearray(), {}, []
    def emit(value): code.extend(bytes.fromhex(value))
    def branch(op, target):
        op=bytes.fromhex(op);jumps.append((len(code),op,target));code.extend(bytes(len(op)+4))
    def check(displacement, value):
        emit('8178'+f'{displacement:02x}');code.extend(struct.pack('<I',value));branch('0f85','fallback')
    def stack_frame():
        # EAX is an aligned frame in the current thread's committed stack.
        emit('a903000000');branch('0f85','fallback')
        emit('643b0508000000');branch('0f82','fallback')
        emit('643b0504000000');branch('0f83','fallback')
        emit('8d501c');emit('643b1504000000');branch('0f87','fallback')
    def parent():
        emit('8bd08b00')  # EDX=previous frame; EAX=parent.
        emit('3bc2');branch('0f86','fallback')
        emit('f7d24203d0')  # EDX=EAX-previous (without changing EAX).
        emit('81fa00200000');branch('0f87','fallback')
        stack_frame()
    emit('9c608bc5')  # Preserve flags/registers, EAX=inner producer frame.
    stack_frame()
    check(4,0x25FA493);check(12,13);check(16,2);check(24,1)
    # The serializer overwrites arg4 with the source inventory type. Do not
    # test the original recursive arg4=3 after this overwrite.
    parent()
    check(4,0x249C880);check(12,13);check(16,1);check(20,6);check(24,0)
    emit('8b50083b5508');branch('0f85','fallback')
    emit('85d2');branch('0f88','fallback')
    parent();check(4,0x24A6B0F)
    parent();check(4,0x32C76FD)
    emit('619d6a02')
    code.extend(rel(b'\xe8',base+len(code),0x374EA10))
    code.extend(rel(b'\xe9',base+len(code),0x25FA994))
    labels['fallback']=base+len(code);emit('619d')
    code.extend(rel(b'\xe9',base+len(code),PREVIOUS_TITLE))
    for at,op,name in jumps:code[at:at+len(op)+4]=rel(op,base+at,labels[name])
    return bytes(code)


def build(data):
    if pe.sha(data)!=INPUT_SHA256:raise ValueError('exact released V196 required')
    header,opt,table,sections=pe.headers(data)
    sa,fa=struct.unpack_from('<II',data,opt+32)
    image_size,header_size=struct.unpack_from('<II',data,opt+56)
    slot=table+len(sections)*40
    end=max(s['offset']+s['rawsize'] for s in sections)
    rva=pe.align(max(s['rva']+max(s['size'],s['rawsize']) for s in sections),sa)
    if (slot+40>header_size or any(data[slot:slot+40]) or end!=len(data)
            or pe.align(end,fa)!=end or rva!=image_size
            or any(struct.unpack_from('<II',data,opt+128))):
        raise ValueError('unexpected V196 PE contract')
    title=pe.offset_for_rva(sections,TITLE_SITE-pe.IMAGE_BASE,5)
    adventure=pe.offset_for_rva(sections,ADVENTURE_SITE-pe.IMAGE_BASE,2)
    if data[title:title+5]!=rel(b'\xe9',TITLE_SITE,PREVIOUS_TITLE):raise ValueError('title boundary differs')
    if data[adventure:adventure+2]!=b'\x74\x51':raise ValueError('adventure branch differs')
    code=title_helper(pe.IMAGE_BASE+rva);size=pe.align(len(code),fa)
    out=bytearray(data)+code+bytes(size-len(code))
    out[title:title+5]=rel(b'\xe9',TITLE_SITE,pe.IMAGE_BASE+rva)
    # This branch is reached only after the target's channel/directory differs.
    # Ordinary channels must send the same target-qualified request as raids;
    # the server still checks live identity and guild membership.
    out[adventure:adventure+2]=b'\x90\x90'
    out[slot:slot+40]=struct.pack('<8sIIIIIIHHI',b'.u197',len(code),rva,size,end,0,0,0,0,0x60000020)
    struct.pack_into('<H',out,header+6,len(sections)+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+size)
    struct.pack_into('<I',out,opt+56,pe.align(rva+len(code),sa))
    struct.pack_into('<I',out,opt+64,checksum(out,opt+64))
    ranges=sorted([(title,title+5),(adventure,adventure+2),(slot,slot+40),(header+6,header+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68)])
    cursor=0
    for lo,hi in ranges:
        if out[cursor:lo]!=data[cursor:lo]:raise AssertionError('unlisted modification')
        cursor=hi
    if out[cursor:len(data)]!=data[cursor:]:raise AssertionError('unlisted tail modification')
    return bytes(out),dict(inputSha256=pe.sha(data),outputSha256=pe.sha(out),size=len(out),titleHelperVA=hex(pe.IMAGE_BASE+rva),titleHelperSize=len(code),changedRanges=ranges,appendRange=[end,len(out)])


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
    if a.input.resolve()==a.output.resolve() or a.output.exists():raise ValueError('separate new output required')
    data,receipt=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(data)
    if pe.sha(a.output.read_bytes())!=receipt['outputSha256']:raise ValueError('readback mismatch')
    print(json.dumps(receipt,indent=2))
