"""Exact current-image peer registration candidate, paired with bounded retries.

The reviewed helper inserts existing current-party player objects only. It never
restarts a scene, creates monsters, revives actors or changes skill ownership.
Offline builder; no installation, process access or gameplay automation.
"""
from pathlib import Path
import argparse
import importlib.util
import json
import hashlib
import struct
import sys

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
INPUT_SHA = '250d78d27e115be21240959db1415f7eddabb0c26c255c9ad452a8841f8abc0c'
INPUT_BYTES = 112619520
spec = importlib.util.spec_from_file_location('current_party_lifecycle_recipe', HERE.parent/'party-room-lifecycle-registration/build_candidate.py')
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
# Rebind the immutable prerequisite to the current cumulative image. All
# original native/helper anchors, padding, ABI and byte-diff guards still run.
base.INPUT_SHA, base.INPUT_BYTES = INPUT_SHA, INPUT_BYTES

def build(data):
    require=base.pe.require
    require(len(data)==INPUT_BYTES and hashlib.sha256(data).hexdigest()==INPUT_SHA,'exact current cumulative image required')
    image=base.pe.PE(data)
    require(image.read(base.REG_SITE,5)==base.REG_ORIGINAL and image.read(base.REG_CONTINUE,3)==bytes.fromhex('c22000'),'native registration epilogue changed')
    require(image.read(base.REG_SITE-14,2)==bytes.fromhex('b001'),'native registration success anchor changed')
    require(image.read(base.MODE_SITE,7)==base.MODE_ORIGINAL and image.read(0x22C407F,3)==bytes.fromhex('83c404'),'native mode1 cleanup changed')
    old=base.previous.helper()
    require(image.read(base.previous.CODE,len(old))==old,'existing NOTI120 helper changed')
    # The cumulative notification image renamed old section names. Bind by
    # verified existing helper/state addresses, never by a guessed section name.
    def containing(va):
        return next(s for s in image.sections if s['rva']<=va-image.base<s['rva']+s['virtual_size'])
    # The old code section now includes virtual-only growth storage. Use the
    # verified current growth RX tail, whose physical padding remains unused.
    rx=next(s for s in image.sections if s['name']=='.gttext')
    rw=containing(base.previous.DATA)
    require(rx['characteristics']==0x60000020 and rw['characteristics']==0xC0000040,'expected RX/RW helper sections required')
    code_va=image.base+rx['rva']+base.align(rx['virtual_size'],16)
    data_va=image.base+rw['rva']+base.align(rw['virtual_size'],16)
    code,entries=base.helper(code_va,data_va)
    out=bytearray(data);ranges=[]
    for section,va,payload in ((rx,code_va,code),(rw,data_va,bytes(4*len(base.COUNTERS)))):
        delta=va-image.base-section['rva']
        require(section['virtual_size']<=delta and delta+len(payload)<=section['raw_size'],'recovery helper exceeds unoccupied padding')
        offset=image.offset(va,len(payload))
        require(data[offset:offset+len(payload)]==bytes(len(payload)),'recovery padding occupied')
        out[offset:offset+len(payload)]=payload
        header=image.st+section['index']*40+8
        struct.pack_into('<I',out,header,delta+len(payload))
        ranges.extend(((offset,offset+len(payload)),(header,header+4)))
    for site,size,label in ((base.REG_SITE,5,'registered'),(base.MODE_SITE,7,'mode1')):
        offset=image.offset(site,size)
        out[offset:offset+size]=b'\xe9'+struct.pack('<i',entries[label]-site-5)+b'\x90'*(size-5)
        ranges.append((offset,offset+size))
    struct.pack_into('<I',out,image.opt+64,base.pe.pe_checksum(out,image.opt+64));ranges.append((image.opt+64,image.opt+68))
    restored=bytearray(out)
    for lo,hi in ranges:restored[lo:hi]=data[lo:hi]
    require(restored==data,'unapproved byte delta')
    candidate=bytes(out)
    receipt=dict(inputSHA256=INPUT_SHA,outputSHA256=hashlib.sha256(candidate).hexdigest(),bytes=len(candidate),
                 codeVA=code_va,codeSize=len(code),dataVA=data_va,counters=base.COUNTERS,entries=entries,
                 previousProcessSlot=base.process_slot_address(),allowedFileRanges=sorted(ranges),
                 hooks={'registration_success':base.REG_SITE,'mode1_completed':base.MODE_SITE},
                 existingNOTI120HelperUnchanged=True,registeredActorKind=0x111,maximumSlotsPerSweep=8,
                 maximumPassesPerEvent=8,packetReplay=False,timer=False,actorReset=False,liveValidated=False,productionChanged=False)
    receipt.update(recipe='party-room-recovery-current-v1',
                   requiresBoundedPeerSnapshotRetries=True,
                   retryOpportunitiesMS=[750, 2000, 5000],
                   independentlyObservesLateContextReadiness=False,
                   monsterCreation=False, mapReplay=False, skillReplay=False,
                   nativeRegistrationKind=0x111, installed=False, gameAccepted=False)
    return candidate, receipt

if __name__ == '__main__':
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input', type=Path, required=True)
    ap.add_argument('--output', type=Path, required=True)
    ap.add_argument('--receipt', type=Path, required=True)
    args = ap.parse_args()
    candidate, receipt = build(args.input.read_bytes())
    with args.output.open('xb') as f: f.write(candidate)
    with args.receipt.open('x', encoding='utf-8') as f: json.dump(receipt, f, indent=2)
    print(json.dumps(receipt))
