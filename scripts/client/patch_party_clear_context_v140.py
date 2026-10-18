"""Keep NOTI9 kind3 party removal in the context supplied by its record.

The parser resolves the record by (route << 16 | owner, character key), but
its final removal wrapper substitutes the retained local connection context.
Only that call site is redirected; parser, slot cleanup and scene code stay.
"""
import argparse,json,struct
from pathlib import Path
import patch_receivers_v128 as pe

INPUT_SHA256='5499a71294e388d3efc6690a9e6ddaf31673d74aff5befd3de3c8fa6c9bc8b92'
INPUT_SIZE=111812608
PATCH_VA=0x2018B92
OLD_TARGET=0x2976070
REMOVE_TARGET=0x2975F40
HELPER_OFFSET=576
ORIGINAL=b'\xe8'+struct.pack('<i',OLD_TARGET-PATCH_VA-5)

def helper(va):
    # Entered with ECX=party manager, stack=[return, character key] and EBP
    # still belonging to NOTI9. Use the same two parsed bytes as its lookup.
    code=bytearray.fromhex('0fb68572fcffffc1e0100fb69576fcffff09d0')
    code+=bytes.fromhex('5a5052') # [return, context, key] for remove(ctx,key)
    code+=b'\xe9'+struct.pack('<i',REMOVE_TARGET-(va+len(code)+5))
    assert len(code)==27
    return bytes(code)

def build(data):
    if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256:
        raise ValueError('exact accepted v139 input required')
    opt,sections=pe.headers(data)
    header=opt+struct.unpack_from('<H',data,opt-4)[0]+23*40
    if len(sections)!=24 or data[header:header+8]!=b'.clsgate':raise ValueError('adapter section changed')
    vsize,rva,capacity,raw=struct.unpack_from('<IIII',data,header+8)
    if vsize!=536 or capacity!=4096 or struct.unpack_from('<I',data,header+36)[0]!=0x60000020:
        raise ValueError('adapter layout changed')
    if any(data[raw+536:raw+capacity]) or any(struct.unpack_from('<II',data,opt+128)):
        raise ValueError('adapter padding/certificate changed')
    at=pe.offset(sections,PATCH_VA,5)
    if data[at:at+5]!=ORIGINAL:raise ValueError('kind3 removal call changed')
    prefix=bytes.fromhex('80bdaffcffff0375130fb78560fcffff8b0d68376a0550')
    if data[at-len(prefix):at]!=prefix:raise ValueError('kind3-only caller changed')
    helper_va=pe.IMAGE_BASE+rva+HELPER_OFFSET;code=helper(helper_va)
    out=bytearray(data);out[at:at+5]=b'\xe8'+struct.pack('<i',helper_va-PATCH_VA-5)
    out[raw+HELPER_OFFSET:raw+HELPER_OFFSET+len(code)]=code
    struct.pack_into('<I',out,header+8,HELPER_OFFSET+len(code))
    struct.pack_into('<I',out,opt+64,0);struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    receipt=dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(out),input_size=len(data),output_size=len(out),
        patch_va=hex(PATCH_VA),old_target=hex(OLD_TARGET),remove_target=hex(REMOVE_TARGET),
        helper_va=hex(helper_va),helper_offset=HELPER_OFFSET,helper_size=len(code),
        input_helper_size=536,adapter_virtual_size=HELPER_OFFSET+len(code),helper_capacity=capacity,
        patch_offset=at,helper_file_offset=raw+HELPER_OFFSET,section_virtual_size_offset=header+8,
        checksum_offset=opt+64,expected_original=ORIGINAL.hex(),receive_adapter_preserved=True,
        chat_enter_v139_preserved=True,server_changes=False,resources_changed=False,
        full_ui_acceptance=False,user_acceptance=False)
    return bytes(out),receipt

def main():
    p=argparse.ArgumentParser();p.add_argument('input',type=Path);p.add_argument('output',type=Path)
    p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
    if a.output.exists() or a.receipt.exists():raise SystemExit('output already exists')
    out,r=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(out)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(r,f,indent=2)
    print(json.dumps(r))

if __name__=='__main__':main()
