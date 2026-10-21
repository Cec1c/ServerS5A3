"""Enable only class0/1150 and class1/502,785 on the exact released v151."""
from pathlib import Path
import argparse, hashlib, json, struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '2def3cc35c1ee00a9c697fe81d1f31259860468d042d6d477cd3b4be08f0e6eb'
INPUT_SIZE = 111845376
CALL_VA = 0x2523a13
PREVIOUS = 0x6ea9000

def helper(va):
    code = bytearray()
    labels, jumps = {}, []
    def emit(h): code.extend(bytes.fromhex(h))
    def branch(op, label):
        emit(op); jumps.append((len(code),label)); code.extend(bytes(4))
    emit('0fb7442404 803f00')
    branch('0f84','class0')
    emit('803f01')
    branch('0f85','fallback')
    for number in (502,785):
        code.append(0x3d); code.extend(struct.pack('<I',number)); branch('0f84','allow')
    branch('e9','fallback')
    labels['class0']=len(code)
    code.append(0x3d); code.extend(struct.pack('<I',1150)); branch('0f84','allow')
    labels['fallback']=len(code)
    code.append(0xe9); code.extend(struct.pack('<i',PREVIOUS-(va+len(code)+4)))
    labels['allow']=len(code)
    emit('b801000000 c20400')
    for at,label in jumps: struct.pack_into('<i',code,at,labels[label]-at-4)
    return bytes(code)

def build(data):
    if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256: raise ValueError('exact released v151 required')
    opt,sections=pe.headers(data); pe_at=opt-24
    count=struct.unpack_from('<H',data,pe_at+6)[0]
    header=opt+struct.unpack_from('<H',data,pe_at+20)[0]+count*40
    if count!=32 or struct.unpack_from('<II',data,opt+32)!=(4096,4096) or struct.unpack_from('<I',data,opt+56)[0]!=INPUT_SIZE: raise ValueError('unexpected section layout')
    if header+40>4096 or any(data[header:header+40]) or any(struct.unpack_from('<II',data,opt+128)): raise ValueError('unexpected header/certificate')
    context=bytes.fromhex('0fb74f0183c410518b0d44fa7e05e8e85598048b0d44fa7e05')
    at=pe.offset(sections,0x2523a05,len(context))
    if data[at:at+len(context)]!=context: raise ValueError('receive call context mismatch')
    call=pe.offset(sections,CALL_VA,5)
    newva=pe.IMAGE_BASE+INPUT_SIZE; code=helper(newva)
    out=bytearray(data); out.extend(code+bytes(4096-len(code)))
    struct.pack_into('<i',out,call+1,newva-CALL_VA-5)
    struct.pack_into('<H',out,pe_at+6,count+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
    struct.pack_into('<I',out,opt+56,INPUT_SIZE+4096)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.jobgate',len(code),INPUT_SIZE,4096,INPUT_SIZE,0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    ranges=sorted([(call+1,call+5),(pe_at+6,pe_at+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)])
    cursor=0
    for lo,hi in ranges:
        if data[cursor:lo]!=out[cursor:lo]: raise ValueError('unexpected baseline change')
        cursor=hi
    if data[cursor:]!=out[cursor:INPUT_SIZE]: raise ValueError('unexpected tail change')
    return out,dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(out),input_size=INPUT_SIZE,output_size=len(out),
        call_va=hex(CALL_VA),helper_va=hex(newva),section_va=hex(newva),helper_bytes=len(code),
        fallback=hex(PREVIOUS),class0=[1150],class1=[502,785],original_ranges=ranges,
        appended_range=[INPUT_SIZE,len(out)],published=False,deployed=False,full_gui=False)

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
    if len({x.resolve() for x in (a.input,a.output,a.receipt)})!=3 or a.output.exists() or a.receipt.exists():p.error('immutable input and new outputs required')
    data,result=build(a.input.read_bytes())
    with a.output.open('xb') as f:f.write(data)
    with a.receipt.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
    print(json.dumps(result))
