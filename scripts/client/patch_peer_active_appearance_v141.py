"""Restore peer appearance after the native mode1 equipment rebuild.

Only the final mode1 call site changes. The helper copies the same four base
appearance arrays as the native descriptor assignment, limited to slots0..13,
then tail-calls the original self-only finalizer. No network or UI entrypoints
are introduced. Exact accepted v140 is the only input.
"""
import argparse,json,struct
from pathlib import Path
import patch_receivers_v128 as pe

INPUT_SHA256='cb21f6504ea3b994c5d03c986df96643b55f0bb538dd654ded08a29553b20c71'
INPUT_SIZE=111812608
PATCH_VA=0x22c407a
OLD_TARGET=0x6e96000
HELPER_OFFSET=640
ORIGINAL=b'\xe8'+struct.pack('<i',OLD_TARGET-PATCH_VA-5)

def helper(va):
 b=bytearray();fix=[];labels={}
 def emit(s):b.extend(bytes.fromhex(s))
 def jcc(op,label='done'):
  emit(op);fix.append((len(b),label));b.extend(b'\0'*4)
 def call(target):b.extend(b'\xe8'+struct.pack('<i',target-(va+len(b)+5)))
 emit('9c60 89fd') # EDI=active actor; ESI=bound stat at this exact call site.
 emit('85ed');jcc('0f84')
 emit('85f6');jcc('0f84')
 emit('803d19a5680500');jcc('0f85') # packet owner zero: dungeon peer binding.
 emit('8b0d58376a05 85c9');jcc('0f84')
 emit('83797000');jcc('0f84') # do not enter native lazy local lookup.
 call(0x296dd80)
 emit('39e8');jcc('0f84') # exclude local actor.
 emit('0fb75d20 85db');jcc('0f84')
 emit('81fbffff0000');jcc('0f84')
 emit('53');call(0x296d730);emit('83c404 39e8');jcc('0f85')
 emit('0fb6051aa56805 50 6a00 53');call(0x1a5a5e0);emit('83c40c')
 emit('85c0');jcc('0f84')
 emit('39e8');jcc('0f84')
 emit('0fb74820 39d9');jcc('0f85')
 for off,count in ((0x50,28),(0x158,14),(0x1dc,14),(0x26d,14)):
  emit('8db0');b.extend(struct.pack('<I',off))
  emit('8dbd');b.extend(struct.pack('<I',off))
  emit('b9');b.extend(struct.pack('<I',count));emit('fcf3a5')
 labels['done']=len(b);emit('619d')
 b.extend(b'\xe9'+struct.pack('<i',OLD_TARGET-(va+len(b)+5)))
 for at,label in fix:struct.pack_into('<i',b,at,labels[label]-(at+4))
 return bytes(b)

def build(data):
 if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256:raise ValueError('exact accepted v140 input required')
 opt,sections=pe.headers(data);header=opt+struct.unpack_from('<H',data,opt-4)[0]+23*40
 if len(sections)!=24 or data[header:header+8]!=b'.clsgate':raise ValueError('adapter section changed')
 vsize,rva,capacity,raw=struct.unpack_from('<IIII',data,header+8)
 if vsize!=603 or capacity!=4096 or struct.unpack_from('<I',data,header+36)[0]!=0x60000020:raise ValueError('adapter layout changed')
 if any(data[raw+603:raw+capacity]) or any(struct.unpack_from('<II',data,opt+128)):raise ValueError('padding or certificate changed')
 at=pe.offset(sections,PATCH_VA,5)
 if data[at:at+5]!=ORIGINAL or data[at-1]!=0x56 or data[at+5:at+8]!=bytes.fromhex('83c404'):raise ValueError('mode1 final caller changed')
 wrapper=pe.offset(sections,OLD_TARGET,23)
 if data[wrapper:wrapper+23]!=bytes.fromhex('9c60e8d9b0acfb3b4424287507619de98cd042fb619dc3'):raise ValueError('original self-only finalizer changed')
 helper_va=pe.IMAGE_BASE+rva+HELPER_OFFSET;code=helper(helper_va)
 if HELPER_OFFSET+len(code)>capacity:raise ValueError('helper capacity exceeded')
 out=bytearray(data);out[at:at+5]=b'\xe8'+struct.pack('<i',helper_va-PATCH_VA-5)
 out[raw+HELPER_OFFSET:raw+HELPER_OFFSET+len(code)]=code
 struct.pack_into('<I',out,header+8,HELPER_OFFSET+len(code))
 struct.pack_into('<I',out,opt+64,0);struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
 receipt=dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(out),input_size=len(data),output_size=len(out),patch_va=hex(PATCH_VA),
  old_target=hex(OLD_TARGET),helper_va=hex(helper_va),helper_offset=HELPER_OFFSET,helper_size=len(code),input_helper_size=603,
  adapter_virtual_size=HELPER_OFFSET+len(code),helper_capacity=capacity,patch_offset=at,helper_file_offset=raw+HELPER_OFFSET,
  section_virtual_size_offset=header+8,checksum_offset=opt+64,expected_original=ORIGINAL.hex(),copied_base_slots=14,copied_bytes=280,
  original_self_finalizer_preserved=True,other_19_slots_preserved=True,receive_adapter_preserved=True,party_clear_v140_preserved=True,
  chat_enter_v139_preserved=True,server_changes=False,resources_changed=False,full_ui_acceptance=False,user_acceptance=False)
 return bytes(out),receipt

def main():
 p=argparse.ArgumentParser();p.add_argument('input',type=Path);p.add_argument('output',type=Path);p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
 if a.output.exists() or a.receipt.exists():raise SystemExit('output already exists')
 out,r=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
 a.output.write_bytes(out);a.receipt.write_text(json.dumps(r,indent=2),encoding='utf-8');print(json.dumps(r))
if __name__=='__main__':main()
