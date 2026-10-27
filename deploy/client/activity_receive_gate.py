"""Hash-pinned v141 activity-related receive-gate candidate; no input overwrite."""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from image_communication_gate import pe_checksum
from story_pause_gate import file_offset, pe_layout

SOURCE_SHA256='26abf551753c2745c2e5b349fb24336422a189187ff847c995e0df9a68871079'
GATE_VA=0x6e83610
BITMAP_VA=0x6e92300
OPCODES=(1303,1386,1455)


def build(data):
    if hashlib.sha256(data).hexdigest()!=SOURCE_SHA256:
        raise ValueError('input is not the pinned published v141')
    base,_,sections=pe_layout(data)
    offset=file_offset(sections,GATE_VA,27)
    fingerprint=bytes.fromhex('0fb74424043dc205000077100fa305')+struct.pack('<I',BITMAP_VA)
    if base!=0x400000 or data[offset:offset+len(fingerprint)]!=fingerprint:
        raise ValueError('unexpected image or receive gate')
    bound=struct.unpack_from('<I',data,offset+6)[0]
    bitmap=file_offset(sections,BITMAP_VA,bound//8+1)
    after=bytearray(data)
    changes={}
    for op in OPCODES:
        at=bitmap+op//8
        if after[at]&(1<<(op%8)):
            raise ValueError('requested opcode already admitted')
        changes.setdefault(at,dict(offset=at,before=data[at]))
        after[at]|=1<<(op%8)
        changes[at]['after']=after[at]
    allowed=lambda b,op:op<=bound and bool(b[bitmap+op//8]&(1<<(op%8)))
    delta=[op for op in range(65536) if allowed(data,op)!=allowed(after,op)]
    if delta!=sorted(OPCODES):
        raise ValueError('unexpected admission delta')
    checksum_offset=struct.unpack_from('<I',data,0x3c)[0]+24+64
    checksum_before=struct.unpack_from('<I',data,checksum_offset)[0]
    if checksum_before!=pe_checksum(data,checksum_offset):
        raise ValueError('source checksum mismatch')
    checksum_after=pe_checksum(after,checksum_offset)
    struct.pack_into('<I',after,checksum_offset,checksum_after)
    allowed_bytes=set(changes)|set(range(checksum_offset,checksum_offset+4))
    if any(a!=b and i not in allowed_bytes for i,(a,b) in enumerate(zip(data,after))):
        raise ValueError('unexpected byte delta')
    return bytes(after),dict(source_sha256=SOURCE_SHA256,candidate_sha256=hashlib.sha256(after).hexdigest(),
        bytes=len(after),changes=list(changes.values()),changed_admissions=delta,
        checksum=dict(offset=checksum_offset,before=checksum_before,after=checksum_after),
        gate_shared_all_packet_classes=True,published=False,native_ui_accepted=False)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('source',type=Path);p.add_argument('output',type=Path)
    p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
    if len({x.resolve() for x in (a.source,a.output,a.receipt)})!=3 or a.output.exists() or a.receipt.exists():
        p.error('distinct immutable source and new output/receipt required')
    data,receipt=build(a.source.read_bytes())
    with a.output.open('xb') as f:f.write(data)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))


if __name__=='__main__':main()
