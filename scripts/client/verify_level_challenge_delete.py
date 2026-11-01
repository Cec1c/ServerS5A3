"""Execute the complete native deletion handler with explicit external adapters.

The real handler, row-vector lookup, guard branches and confirmation selection
run from entry through return. Event membership, GUI/string sinks and other
eligibility leaves are fixture interfaces, not full game execution.
"""
from pathlib import Path
import argparse,struct,json,itertools,tempfile
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_level_challenge_delete as patch
from test_patch_fatigue_count_v164 import windows_image

class Lab:
 def __init__(self,data):
  self.u=Uc(UC_ARCH_X86,UC_MODE_32);p=pefile.PE(data=data,fast_load=True)
  self.u.mem_map(0x400000,(p.OPTIONAL_HEADER.SizeOfImage+4095)&~4095)
  self.u.mem_write(0x400000,bytes(p.get_memory_mapped_image()))
  self.u.mem_map(0,0x1000);self.u.mem_map(0x20000000,0x40000)
  self.u.hook_add(UC_HOOK_CODE,self.hook)
 def w(self,a,v):self.u.mem_write(a,struct.pack('<I',v&0xffffffff))
 def r(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]
 def ret(self,value=0,pop=0):
  sp=self.u.reg_read(UC_X86_REG_ESP);ip=self.r(sp)
  self.u.reg_write(UC_X86_REG_EAX,value);self.u.reg_write(UC_X86_REG_ESP,sp+4+pop);self.u.reg_write(UC_X86_REG_EIP,ip)
 def hook(self,u,a,size,userdata):
  if a==0x2003f000:u.emu_stop();return
  sp=u.reg_read(UC_X86_REG_ESP)
  if a==0x32b5c90:self.ret(self.case['modal'],8)
  elif a==0x3826970:self.ret(self.case['available'])
  elif a==0x3bf1540:self.ret(self.case['generic'])
  elif a==0x2303380:self.event=self.r(sp+4);self.ret(0x20005000,4)
  elif a in (0x22fcaa0,0x22fcac0):self.ret(0x20006000)
  elif a==0x3e73cb6:self.ret(0x20006000)
  elif a==0x22ff610:self.ret(self.case['active'] if self.event==288 else self.case['other'])
  elif a==0xb39d20:self.ret(0x20007000)
  elif a==0x37e2b70:self.ret(pop=4)
  elif a==0x37e2b00:self.ret(1)
  elif a==0x37e1a80:self.ret()
  elif a==0xea1e40:
   self.lookups.append(self.r(sp+4));self.ret(self.case['member'],4)
  elif a==0x39a6620:self.ret(self.r(sp+4))
  elif a==0x32bea50:
   self.dialogs.append([self.r(sp+4),self.r(sp+8),self.r(sp+12)]);self.ret(0,12)
  elif a==0x3e738b1:raise AssertionError('unexpected bounds failure')
 def run(self,**case):
  self.case=dict(modal=0,available=1,selected=0,can_delete=1,busy=0,generic=0,role_state=0,other=0,active=1,member=1);self.case.update(case)
  self.dialogs=[];self.lookups=[];self.event=0
  self.u.mem_write(0x20000000,bytes(0x3f000));self.w(0,0)
  W=0x20001000;V=0x20002000;C=0x20003000
  self.w(0x56a3798,0x20008000);self.w(W+0x14c,0x20004000)
  self.w(W+0x108,self.case['selected']);self.u.mem_write(W+0x9c,bytes([self.case['can_delete']]));self.u.mem_write(W+0x10c,bytes([self.case['busy']]))
  self.w(W+0xa4,V);self.w(W+0xa8,V+4);self.w(V,0x20009000)
  self.w(W+0xb8,C);self.w(W+0xbc,C+0x450);self.w(C,0x2000a000);self.u.mem_write(C+0x3ae,bytes([self.case['role_state']]))
  sp=0x2003e000;self.w(sp,0x2003f000)
  preserved={UC_X86_REG_EBX:0x11223344,UC_X86_REG_ESI:0x22334455,UC_X86_REG_EDI:0x33445566,UC_X86_REG_EBP:0x44556677}
  for reg,value in preserved.items():self.u.reg_write(reg,value)
  self.u.reg_write(UC_X86_REG_ECX,W);self.u.reg_write(UC_X86_REG_ESP,sp)
  self.u.emu_start(0x36c43e0,0x2003f001,count=3000)
  assert self.u.reg_read(UC_X86_REG_EIP)==0x2003f000
  assert self.u.reg_read(UC_X86_REG_ESP)==sp+4
  for reg,value in preserved.items():assert self.u.reg_read(reg)==value
  return dict(dialogs=self.dialogs,lookups=self.lookups)

def verify(data,after,path):
 candidate,receipt=patch.build(data)
 assert after==candidate
 old,new=Lab(data),Lab(candidate);cases=[]
 for member,active,generic,state,other in itertools.product((0,1),(0,1),(0,1),(0,1,3,5),(0,1)):
  c=dict(member=member,active=active,generic=generic,role_state=state,other=other)
  a,b=old.run(**c),new.run(**c)
  blocked=generic or state in (1,3,5) or other
  if member and active:
   assert a['dialogs'][0][:2]==[0x2d6,0xa68e],a
   if blocked:
    reason=0x4c71 if other else 0x9878 if state in (1,3,5) else 0xa544
    assert b['dialogs'][0][:2]==[0x2d6,reason],b
   else:assert b['dialogs']==[[0x2f0,0x2000a000,0]],b
  else:assert a==b,(c,a,b)
  assert a['lookups']==b['lookups']
  cases.append(dict(case=c,before=a,after=b))
 for c in (dict(modal=1),dict(available=0),dict(selected=-1),dict(can_delete=0),dict(busy=1)):
  a,b=old.run(**c),new.run(**c);assert a==b and not b['dialogs'];cases.append(dict(case=c,before=a,after=b))
 p=pefile.PE(data=candidate,fast_load=True);assert p.verify_checksum()
 for bad in (data[:-1],data[:-1]+bytes([data[-1]^1]),candidate):
  try:patch.build(bad)
  except ValueError:pass
  else:raise AssertionError('invalid input accepted')
 result=dict(passed=True,cases=len(cases),inputSHA256=patch.INPUT_SHA256,outputSHA256=receipt['outputSHA256'],receipt=receipt,actualNativeEntry='0x36c43e0',actualNativeEnd='0x36c46a0',adapters=['event availability and roster membership','window manager and popup/string sinks','other event/character eligibility leaves'],stackAndCalleeSavedVerified=True,checksumVerified=True,gameExecuted=False,guiAccepted=False)
 windows_image(path)
 with tempfile.TemporaryDirectory() as directory:
  source=Path(directory)/'source';target=Path(directory)/'existing'
  source.write_bytes(b'input sentinel');target.write_bytes(b'output sentinel')
  for destination in (source,target):
   try:patch.write_candidate(source,destination)
   except ValueError:pass
   else:raise AssertionError('existing output accepted')
  assert source.read_bytes()==b'input sentinel' and target.read_bytes()==b'output sentinel'
 result.update(cases=cases,windowsImage=True)
 return result

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--before',type=Path,required=True);p.add_argument('--after',type=Path,required=True);p.add_argument('--result',type=Path,required=True)
 a=p.parse_args();result=verify(a.before.read_bytes(),a.after.read_bytes(),a.after.resolve())
 with a.result.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
 print(json.dumps(dict(passed=True,cases=len(result['cases']),windowsImage=True,outputSHA256=result['outputSHA256'])))
