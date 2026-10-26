"""Enable the audited party consent notification and teleport response."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256='62954fbf00aabc400f2bdb9f70f7de721b8260c052fcaac632b91d4d5d1ea88f'
INPUT_SIZE=111902720
CALL_VA=0x2523A13
PREVIOUS_GATE=0x6EAD200
HELPER_VA=0x6EAD300

def helper():
    code=bytearray(); labels={}; refs=[]
    def branch(op,label):
        code.extend(op); refs.append((len(code),label)); code.extend(b'\0'*4)
    code.extend(bytes.fromhex('0fb7442404803f00'))
    branch(b'\x0f\x85','class1')
    code.extend(b'\x3d'+struct.pack('<I',377)); branch(b'\x0f\x84','allow'); branch(b'\xe9','fallback')
    labels['class1']=len(code)
    code.extend(bytes.fromhex('803f01'));branch(b'\x0f\x85','fallback')
    code.extend(b'\x3d'+struct.pack('<I',434));branch(b'\x0f\x85','fallback')
    labels['allow']=len(code);code.extend(bytes.fromhex('b801000000c20400'))
    labels['fallback']=len(code);code.extend(b'\xe9'+struct.pack('<i',PREVIOUS_GATE-(HELPER_VA+len(code)+5)))
    for at,label in refs:struct.pack_into('<i',code,at,labels[label]-at-4)
    return bytes(code)

def build(data):
    if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256:raise ValueError('exact cumulative guild lifecycle candidate required')
    opt,sections=pe.headers(data);table=opt+struct.unpack_from('<H',data,opt-4)[0]
    headers=[table+i*40 for i in range(len(sections)) if data[table+i*40:table+i*40+8]==b'.r195rx\0']
    if len(headers)!=1:raise ValueError('gate section mismatch')
    h=headers[0];vsize,rva,rawsize,raw=struct.unpack_from('<IIII',data,h+8)
    if not (0x240<vsize<=0x300) or rva+pe.IMAGE_BASE!=0x6EAD000 or rawsize!=4096 or any(data[raw+vsize:raw+rawsize]):raise ValueError('occupied helper range')
    call=pe.offset(sections,CALL_VA,5)
    if data[call:call+5]!=b'\xe8'+struct.pack('<i',PREVIOUS_GATE-CALL_VA-5):raise ValueError('receive chain mismatch')
    out=bytearray(data);code=helper();at=pe.offset(sections,HELPER_VA,len(code))
    out[at:at+len(code)]=code;out[call:call+5]=b'\xe8'+struct.pack('<i',HELPER_VA-CALL_VA-5)
    struct.pack_into('<I',out,h+8,0x300+len(code));struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    return bytes(out),dict(inputSha256=INPUT_SHA256,outputSha256=pe.sha(out),size=len(out),changedRanges=sorted([(call,call+5),(at,at+len(code)),(h+8,h+12),(opt+64,opt+68)]),class0Added=[377],class1Added=[434],nativeReadersChanged=False)

def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():raise ValueError('separate new output required')
    data,receipt=build(source.read_bytes());destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb')as f:f.write(data)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['outputSha256']:raise RuntimeError('readback mismatch')
    return receipt

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',required=True,type=Path);p.add_argument('--output',required=True,type=Path)
    a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
