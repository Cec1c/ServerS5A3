"""Add bounded modern-party room checks at completed native lifecycle events.

Offline exact-image recipe. Existing NOTI120 validation and peer registration
are reused unchanged; no packet replay, timer, actor reset or live access.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

HERE=Path(__file__).resolve().parent

def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result

previous=module('lifecycle_previous',HERE.parent/'party-initial-room-registration/build_candidate.py')
pe=previous.pe
asm=previous.asm
INPUT_SHA='69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544'
INPUT_BYTES=112607232
REG_SITE,REG_CONTINUE=0x288ABF4,0x288ABF9
REG_ORIGINAL=bytes.fromhex('5e5b8be55d')
MODE_SITE,MODE_CONTINUE=0x22C4082,0x22C4089
MODE_ORIGINAL=bytes.fromhex('c745fcffffffff')
COUNTERS=('inflight','completed_sweeps','mode1_completed','local_registration_completed','reentrant_skips','last_local_slot','pending')

def align(value,step):return (value+step-1)&~(step-1)

def process_slot_address():
    old=previous.helper()
    marker=bytes.fromhex('0fb69d67ffffff6a006a00e8')
    at=old.find(marker)
    pe.require(at>=0 and old.find(marker,at+1)<0,'old entry must have one exact slot call')
    call=at+len(marker)-1
    return previous.CODE+call+5+struct.unpack_from('<i',old,call+1)[0]

def helper(code_va,data_va):
    c=asm.Code(code_va)
    short_fixups=[]
    def short(op,label):
        c.raw(op+'00');short_fixups.append((len(c.buf)-1,label))
    def counter(index):c.raw('ff05'+struct.pack('<I',data_va+index*4).hex())
    c.label('registered')
    c.raw('9c60 fc 84c0');short('74','registered_done')
    c.raw('817d0c11010000');short('75','registered_done')
    c.raw('8b4514 3b05b06f7b05');short('75','registered_done')
    counter(3);c.call('sweep')
    c.label('registered_done');c.raw('619d');c.raw(REG_ORIGINAL.hex());c.jump(REG_CONTINUE)

    c.label('mode1')
    c.raw('9c60 fc');counter(2);c.call('sweep')
    c.raw('619d');c.raw(MODE_ORIGINAL.hex());c.jump(MODE_CONTINUE)

    c.label('sweep')
    # XCHG with memory is atomic. A nested registration callback may enter the
    # completed-model path; it must not start another sweep or clear its owner.
    c.raw('b801000000 8705'+struct.pack('<I',data_va).hex())
    c.raw('85c0');c.jne('busy')
    c.raw('833d80907b0500');c.jne('release')
    c.raw('8b0d58376a05 85c9');c.je('release')
    c.raw('83797000');c.je('release') # avoid native lazy local lookup
    c.call(0x296DD80);c.raw('85c0');c.je('release')
    c.raw('8bf0 8b0d68376a05 85c9');c.je('release')
    c.raw('56');c.call(0x2974460)
    c.raw('8bd8 83fb07');short('77','release')
    c.raw('6a00 6a00');c.call(process_slot_address())
    c.raw('83f802');short('75','release')
    # Return 2 proves local membership in the selected registry and supplies
    # its registry/party pair. Every peer rechecks this exact pinned context.
    c.raw('891d'+struct.pack('<I',data_va+20).hex())
    c.raw('8bf1 8bfa 6a08')
    c.label('pass')
    c.raw('c705'+struct.pack('<I',data_va+24).hex()+'00000000')
    counter(1);c.raw('33db')
    c.label('slot')
    c.raw('57 56');c.call(process_slot_address())
    c.raw('43 83fb08');short('72','slot')
    c.raw('833d'+struct.pack('<I',data_va+24).hex()+'00');short('74','finish')
    c.raw('ff0c24');short('75','pass')
    c.label('finish');c.raw('83c404')
    c.label('release')
    c.raw('c705'+struct.pack('<I',data_va).hex()+'00000000 c3')
    c.label('busy');counter(4)
    c.raw('c705'+struct.pack('<I',data_va+24).hex()+'01000000 c3')
    labels={name:code_va+off for name,off in c.labels.items()}
    code=bytearray(c.finish())
    for at,label in short_fixups:
        delta=c.labels[label]-at-1
        pe.require(-128<=delta<=127,'short lifecycle branch exceeds range')
        code[at]=delta&255
    return bytes(code),labels

def build(data):
    require=pe.require
    require(len(data)==INPUT_BYTES and hashlib.sha256(data).hexdigest()==INPUT_SHA,'exact accepted cumulative EXE required')
    image=pe.PE(data)
    require(image.read(REG_SITE,5)==REG_ORIGINAL and image.read(REG_CONTINUE,3)==bytes.fromhex('c22000'),'native registration success epilogue changed')
    require(image.read(REG_SITE-14,2)==bytes.fromhex('b001'),'native success result anchor changed')
    require(image.read(MODE_SITE,7)==MODE_ORIGINAL,'mode1 completion cleanup changed')
    require(image.read(0x22C407F,3)==bytes.fromhex('83c404'),'mode1 final call cleanup anchor changed')
    old=previous.helper()
    require(image.read(previous.CODE,len(old))==old,'existing NOTI120 guards/registration changed')
    rx=next(s for s in image.sections if s['name']=='.gld127')
    rw=next(s for s in image.sections if s['name']=='.a4rw')
    require(rx['characteristics']==0x60000020 and rw['characteristics']==0xC0000040,'expected RX/RW padding sections required')
    code_va=image.base+rx['rva']+align(rx['virtual_size'],16)
    data_va=image.base+rw['rva']+align(rw['virtual_size'],16)
    code,entries=helper(code_va,data_va)
    out=bytearray(data);ranges=[]
    for section,va,payload in ((rx,code_va,code),(rw,data_va,bytes(4*len(COUNTERS)))):
        delta=va-image.base-section['rva']
        require(section['virtual_size']<=delta and delta+len(payload)<=section['raw_size'],'new lifecycle helper exceeds unoccupied padding')
        offset=image.offset(va,len(payload))
        require(data[offset:offset+len(payload)]==bytes(len(payload)),'lifecycle padding occupied')
        out[offset:offset+len(payload)]=payload
        header=image.st+section['index']*40+8
        struct.pack_into('<I',out,header,delta+len(payload))
        ranges.extend(((offset,offset+len(payload)),(header,header+4)))
    for site,size,label in ((REG_SITE,5,'registered'),(MODE_SITE,7,'mode1')):
        offset=image.offset(site,size)
        out[offset:offset+size]=b'\xe9'+struct.pack('<i',entries[label]-site-5)+b'\x90'*(size-5)
        ranges.append((offset,offset+size))
    struct.pack_into('<I',out,image.opt+64,pe.pe_checksum(out,image.opt+64))
    ranges.append((image.opt+64,image.opt+68))
    restored=bytearray(out)
    for lo,hi in ranges:restored[lo:hi]=data[lo:hi]
    require(restored==data,'unapproved byte delta')
    return bytes(out),dict(inputSHA256=INPUT_SHA,outputSHA256=hashlib.sha256(out).hexdigest(),
        bytes=len(out),codeVA=code_va,codeSize=len(code),dataVA=data_va,counters=COUNTERS,entries=entries,
        previousProcessSlot=process_slot_address(),allowedFileRanges=sorted(ranges),
        hooks={'registration_success':REG_SITE,'mode1_completed':MODE_SITE},
        existingNOTI120HelperUnchanged=True,registeredActorKind=0x111,maximumSlotsPerSweep=8,maximumPassesPerEvent=8,
        packetReplay=False,timer=False,actorReset=False,liveValidated=False,installed=False,productionChanged=False)

if __name__=='__main__':
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input',type=Path,required=True);ap.add_argument('--output',type=Path,required=True)
    ap.add_argument('--receipt',type=Path,required=True);args=ap.parse_args()
    candidate,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f:f.write(candidate)
    with args.receipt.open('x',encoding='utf8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt,indent=2))
