"""Replay real v141 class0/1303 readers, STL lookup, and item count predicates.

Allocator/tree insertion is an explicit fixture. Empty/nonempty tree clearing,
find/lower_bound/end, packet readers and query arithmetic run as native code.
The harness composes gate and receiver without executing the full dispatcher.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'deploy/client'))
from activity_receive_gate import build,SOURCE_SHA256,GATE_VA,BITMAP_VA,OPCODES
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--exe',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True)
p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
before=a.exe.read_bytes();after=a.candidate.read_bytes();expected,patch=build(before);assert after==expected
pe=pefile.PE(data=after,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase
u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
u.mem_write(base,after[:pe.OPTIONAL_HEADER.SizeOfHeaders])
for section in pe.sections:u.mem_write(base+section.VirtualAddress,section.get_data())
u.mem_map(0,4096);u.mem_map(0x10000000,0x20000);u.mem_map(0x20000000,0x20000)
SP=0x10010000;STOP=0x10001000;MGR=0x20001000;TREE=MGR+0x1e4
PROXY=0x20002000;HEAD=0x20003000;BUFFER=0x20004000;NODES=0x20008000
state={};entries={}
def w(addr,value):u.mem_write(addr,struct.pack('<I',value&0xffffffff))
def r(addr):return struct.unpack('<I',u.mem_read(addr,4))[0]
def ret(value=0,clean=0):
 sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
 u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+clean);u.reg_write(UC_X86_REG_EIP,target)
def init_tree():
 entries.clear();w(TREE,PROXY);w(PROXY,TREE);w(TREE+8,HEAD);w(TREE+12,0)
 for off in (0,4,8):w(HEAD+off,HEAD)
 u.mem_write(HEAD+0x21,b'\x01')
def insert_fixture(key,record):
 assert record[0]==key
 node=NODES+len(entries)*0x100;entries[key]=(node,record)
 for off in (0,4,8):w(node+off,HEAD)
 w(node+12,key)
 for i,v in enumerate(record):w(node+16+4*i,v)
 u.mem_write(node+0x21,b'\x00')
 keys=sorted(entries)
 # A valid ordered tree is enough for the actual lower_bound implementation.
 for i,k in enumerate(keys):
  nd=entries[k][0];w(nd,HEAD);w(nd+8,entries[keys[i+1]][0] if i+1<len(keys) else HEAD)
  w(nd+4,entries[keys[i-1]][0] if i else HEAD)
 w(HEAD,entries[keys[0]][0]);w(HEAD+4,entries[keys[0]][0]);w(HEAD+8,entries[keys[-1]][0]);w(TREE+12,len(keys))
 return node
def hook(vm,addr,size,user):
 if addr==STOP:u.emu_stop();return
 if state['mode']=='gate':return
 if addr==0x3e738b1:raise AssertionError('native iterator fail-fast reached')
 if state['mode']=='guild':
  if addr in (0x2303380,0x22fcaa0,0x3e73cb6):
   if addr==0x3e73cb6:
    state['casts']+=1;value=0x20005000 if state['casts']==1 else 0
   else:value=0x20005000
   ret(value,4 if addr==0x22fcaa0 else 0);return
  if addr==0x374ea10:
   state['requests'].append(r(u.reg_read(UC_X86_REG_ESP)+4));ret(0x20006000,4);return
  if addr in (0x374f280,0x3750ac0,0x1d45320,0x39a6620,0x8ef130,0x3810650,0x2a02ac0,0x32bea50):
   if addr==0x8ef130:
    dest=r(u.reg_read(UC_X86_REG_ESP)+4);u.mem_write(dest,b'\0\0')
   ret(0x20006000,4 if addr==0x3810650 else 12 if addr==0x32bea50 else 0);return
 if addr==0x3e72ce4:ret();return
 if addr==0x3e70882:
  state['frees'].append(r(u.reg_read(UC_X86_REG_ESP)+4));ret();return
 if addr==0x1a59800:
  sp=u.reg_read(UC_X86_REG_ESP);dest=r(sp+4);pair=r(sp+8)
  key=r(pair);record=[r(pair+4+4*i) for i in range(4)]
  state['inserts'].append([key,*record]);node=insert_fixture(key,record)
  w(dest,PROXY);w(dest+4,0);w(dest+8,node);ret(dest,8);return
u.hook_add(UC_HOOK_CODE,hook)
def run(start,mode,arg=None):
 state.clear();state.update(mode=mode,inserts=[],frees=[],casts=0,requests=[])
 for reg,val in ((UC_X86_REG_ESP,SP),(UC_X86_REG_EBP,0),(UC_X86_REG_ECX,MGR),
   (UC_X86_REG_EAX,0),(UC_X86_REG_EDX,0),(UC_X86_REG_EBX,0),
   (UC_X86_REG_ESI,0),(UC_X86_REG_EDI,0),(UC_X86_REG_EFLAGS,0x202)):
  u.reg_write(reg,val)
 w(SP,STOP)
 if arg is not None:w(SP+4,arg)
 u.emu_start(start,0,count=100000)
 assert u.reg_read(UC_X86_REG_EIP)==STOP
 return u.reg_read(UC_X86_REG_EAX)

# Full u16 admission comparison executes the real receive gate in both images.
oldbitmap=before[pe.get_offset_from_rva(BITMAP_VA-base):pe.get_offset_from_rva(BITMAP_VA-base)+185]
newbitmap=after[pe.get_offset_from_rva(BITMAP_VA-base):pe.get_offset_from_rva(BITMAP_VA-base)+185]
delta=[]
for op in range(65536):
 vals=[]
 for bitmap in (oldbitmap,newbitmap):
  u.mem_write(BITMAP_VA,bitmap);vals.append(run(GATE_VA,'gate',op));assert u.reg_read(UC_X86_REG_ESP)==SP+8
 if vals[0]!=vals[1]:delta.append(op)
assert delta==sorted(OPCODES)
u.mem_write(BITMAP_VA,newbitmap)
init_tree();cases=[]
payloads=[bytes([1,0,2,0,2,0,5,0,3,0,1,0]),
          bytes([3,255,255,255,1,1,2,7,2,4,4,9]),
          bytes([1,2,2,5,2,1,0,6,3,0,255,7]),
          bytes([1,1,9,3,1,8,8,8,3,0,2,0])]
for present in (False,True):
 for body in payloads:
  # Keep previous tree to exercise real clear/free before replacement.
  previous_nodes=[v[0] for v in entries.values()]
  entries.clear();w(0x56a3778,MGR if present else 0);u.mem_write(0x567669c,b'\x01')
  u.mem_write(BUFFER,body);w(0x57f9ad0,BUFFER);w(0x57f9ad4,12)
  run(0x1a5ae80,'receiver')
  assert r(0x57f9ad0)==BUFFER+12 and r(0x57f9ad4)==0
  assert u.mem_read(0x567669c,1)==b'\x00'
  inserts=list(state['inserts']);freed=list(state['frees'])
  if present:
   expected={}
   for off in range(0,12,4):expected.setdefault(body[off],list(body[off:off+4]))
   assert {k:v[1] for k,v in entries.items()}==expected
   assert sorted(freed)==sorted(previous_nodes)
   queried=[]
   for key in (1,2,3,255):
    maximum=run(0x1a58210,'query',key);assert maximum==(expected[key][2] if key in expected else 0)
    queried.append(dict(key=key,maximum=maximum))
   item_queries=[]
   for item,key in ((10162064,1),(10162065,2),(490007856,3),(0,0)):
    value=run(0x1a58080,'query',item)&255
    want=int(expected[key][1]<expected[key][2]) if key in expected else 1
    assert value==want,(item,value,want)
    item_queries.append(dict(item=item,key=key,can_use=bool(value)))
  else:
   assert not inserts and not freed;queried=[];item_queries=[]
  cases.append(dict(body_hex=body.hex(),manager_present=present,inserts=inserts,
    freed_previous_nodes=len(freed),maximum_queries=queried,item_queries=item_queries))
  if not present:init_tree()

# Shared gate also admits class1/1303. Its failure callback consumes no body.
for status in (0,):
 w(SP+8,status);w(SP+12,0);w(0x57f9ad0,BUFFER);w(0x57f9ad4,0)
 run(0xf48200,'command_failure',0);assert r(0x57f9ad4)==0 and r(0x57f9ad0)==BUFFER

guild_cases=[]
for status in (0,1):
 for result in (0,4,34,99):
  body=struct.pack('<III',result,300,500)
  w(SP+8,status);w(SP+12,0);u.mem_write(BUFFER,body);w(0x57f9ad0,BUFFER);w(0x57f9ad4,len(body))
  run(0xf48200,'guild',0)
  assert r(0x57f9ad4)==(0 if status else 12)
  assert state['requests']==([1300,1301] if status and result==0 else [])
  if status and result==0:assert r(0x20005000+0x68)==500
  guild_cases.append(dict(status=status,result=result,consumed=12-r(0x57f9ad4),requests=list(state['requests'])))

def raw(va,size):
 at=pe.get_offset_from_rva(va-base);return before[at:at+size]
assert raw(0x1a642b9,11)==bytes.fromhex('536880aea5016817050000')
receipt=dict(passed=True,source_sha256=SOURCE_SHA256,candidate_sha256=hashlib.sha256(after).hexdigest(),
 native_gate_cases=65536,changed_admissions=delta,receiver_cases=cases,guild_command_cases=guild_cases,
 notification=dict(packet_class=0,msg_id=1303,bytes=12,layout='3 x (key:u8, used:u8, limit:u8, auxiliary:u8)',
   registration_rva=hex(0x1a642b9-base),callback_rva=hex(0x1a5ae80-base),container_offset='0x1e4'),
 native_executed=['receive gate','callback','all twelve byte reads','tree clear','tree find/lower_bound/end',
   'three item ID/use-limit predicates','limit lookup','class1/1303 success/failure callbacks and result branches'],
 fixtures=['tree allocation/insertion balance','heap free sink','security cookie leaf','guild UI/string/format/network leaves'],
 shared_class1_failure_body_unchanged=True,auxiliary_semantics_unverified=True,
 full_dispatch=False,activity_list_crash_reproduced=False,online_event_ui_rendered=False,native_gameplay_accepted=False)
with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
print(json.dumps(dict(passed=True,gate_cases=65536,receiver_cases=len(cases),candidate_sha256=receipt['candidate_sha256'])))
