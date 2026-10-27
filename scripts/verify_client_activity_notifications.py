"""Replay v141 ordinary-attendance/epic-state readers with explicit UI fixtures."""
import argparse,hashlib,json,struct,sys
from pathlib import Path
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'deploy/client'))
from activity_receive_gate import build
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--exe',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True)
a=p.parse_args();before=a.exe.read_bytes();after=a.candidate.read_bytes();assert build(before)[0]==after
pe=pefile.PE(data=after,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase
u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
for s in pe.sections:u.mem_write(base+s.VirtualAddress,s.get_data())
u.mem_map(0,4096);u.mem_map(0x10000000,0x20000);u.mem_map(0x20000000,0x20000)
SP=0x10010000;STOP=0x10001000;OBJ=0x20001000;UI=0x20002000;BUFFER=0x20004000
state={}
def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
def w(a,v):u.mem_write(a,struct.pack('<I',v&0xffffffff))
def ret(value=0,clean=0):
 sp=u.reg_read(UC_X86_REG_ESP);target=r(sp);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+clean);u.reg_write(UC_X86_REG_EIP,target)
def hook(vm,at,size,unused):
 if at==STOP:u.emu_stop();return
 if at==0x3e738b1:raise AssertionError('NATIVE_FAIL_FAST')
 if at in (0x2303380,0x32b5c90,0x22fcac0,0x3e73cb6):
  clean={0x2303380:0,0x32b5c90:8,0x22fcac0:4,0x3e73cb6:0}[at]
  ret(UI if state['ui'] else 0,clean);return
 if at==0xbf3e60:state['refreshes']+=1;ret();return
 if at==0x1381610:
  state['claimable_notifications'].append(r(u.reg_read(UC_X86_REG_ESP)+4));ret(0,4);return
u.hook_add(UC_HOOK_CODE,hook)
def run(start,body,present=True,ui=True):
 state.clear();state.update(ui=ui,refreshes=0,claimable_notifications=[])
 u.mem_write(OBJ,b'\xcc'*64);u.mem_write(UI,b'\xcc'*64);u.mem_write(BUFFER,body)
 w(0x57f9ad0,BUFFER);w(0x57f9ad4,len(body))
 for reg,val in ((UC_X86_REG_ESP,SP),(UC_X86_REG_EBP,0),(UC_X86_REG_EAX,0),(UC_X86_REG_ECX,0),(UC_X86_REG_EDX,0),(UC_X86_REG_EBX,0),(UC_X86_REG_ESI,0),(UC_X86_REG_EDI,0),(UC_X86_REG_EFLAGS,0x202)):u.reg_write(reg,val)
 w(SP,STOP);w(SP+4,0);w(SP+8,OBJ if present else 0)
 u.emu_start(start,0,count=50000);assert u.reg_read(UC_X86_REG_EIP)==STOP and u.reg_read(UC_X86_REG_ESP)==SP+4
 return dict(state)
cases=[]
for present in (False,True):
 for ui in (False,True):
  for can,days,claimed in ((0,0,0),(1,12,5),(255,0xffffffff,0xfffffffe)):
   body=struct.pack('<BII',can,days,claimed);s=run(0xb3f7f0,body,present,ui)
   assert r(0x57f9ad4)==(0 if present else 9)
   if present:
    assert u.mem_read(OBJ+4,1)==bytes([bool(can)]) and r(OBJ+8)==days and r(OBJ+12)==claimed
    assert s['refreshes']==int(ui) and s['claimable_notifications']==([int(bool(can))] if ui else [])
   cases.append(dict(op=1386,owner_present=present,ui_present=ui,body_hex=body.hex(),consumed=9-r(0x57f9ad4)))
for ui in (False,True):
 for progress,claimed in ((0,0),(18000,7),(0xffffffff,0xffffffff)):
  body=struct.pack('<II',progress,claimed);run(0x8ff9d0,body,True,ui)
  assert r(0x57f9ad4)==0 and (not ui or (r(UI+4)==progress and r(UI+8)==claimed))
  cases.append(dict(op=1455,ui_present=ui,body_hex=body.hex(),consumed=8))
receipt=dict(passed=True,source_sha256=hashlib.sha256(before).hexdigest(),candidate_sha256=hashlib.sha256(after).hexdigest(),cases=cases,
 native_executed=['callback1386','callback1455','byte/u32 native packet readers','owner null check','boolean normalization','state field stores','UI null checks'],
 fixtures=['UI/control lookup','RTTI cast','UI refresh sink'],full_dispatch=False,ui_rendered=False,activity_list_crash_reproduced=False)
with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
print(json.dumps(dict(passed=True,cases=len(cases))))
