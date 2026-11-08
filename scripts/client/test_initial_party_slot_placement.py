"""Execute the current EXE's op120 slot lookup through room registration.
Explicit fixtures replace the party lookup and external virtual methods;
this establishes branch/packet semantics, not full-client rendering.
"""
from pathlib import Path
import hashlib,json,struct,sys
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import argparse
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--exe',required=True,type=Path)
args=parser.parse_args()
data=args.exe.read_bytes()
assert hashlib.sha256(data).hexdigest()=='7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0'
pe=pefile.PE(data=data,fast_load=True)
def trial(slots,available):
 u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set();registered=[];lookups=[];active=0
 def put(a,b):
  for p in range(a&~4095,(a+len(b)+4095)&~4095,4096):
   if p not in pages:u.mem_map(p,4096);pages.add(p)
  u.mem_write(a,b)
 def w(a,v):put(a,struct.pack('<I',v))
 def rd(a):return int.from_bytes(u.mem_read(a,4),'little')
 for a,n in ((0x1ff2fa7,0x1ff309c-0x1ff2fa7),(0x10f1d10,12)):
  put(a,pe.get_data(a-0x400000,n))
 put(0x100000,bytes(0x10000));put(0x200000,bytes(0x20000))
 party,room,vt,mode,mvt=0x200000,0x201000,0x202000,0x203000,0x204000
 w(party,vt);w(vt+0x3c,0x300000);w(mode,mvt);w(mvt+0x8c,0x300010)
 for slot in range(4):
  wrapper=0x205000+slot*0x1000;actor=0x210000+slot*0x1000;avt=0x218000+slot*0x1000
  w(wrapper+0x4f8,actor if slot in available else 0);w(actor,avt)
  for off,pop in ((0x664,12),(0xc30,4),(0x3c0,0),(0xaf8,4),(0x438,8),(0x430,8),(0x440,4),(0x77c,4)):
   target=0x310000+off;w(avt+off,target);put(target,b'\xcc')
 hooks=(0x300000,0x300010,0x2879c90,0x2884460,0x1fa2790,0x32b4970,0x296aa30,0x288a260,0x1ff309c,0x1ff32b1)
 for a in hooks:put(a,b'\xcc')
 def ret(value=0,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EIP,rd(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EDX,0)
 def hook(_u,a,_s,_d):
  sp=u.reg_read(UC_X86_REG_ESP)
  if a==0x300000:
   slot=rd(sp+4);lookups.append(slot);ret(0x205000+slot*0x1000,4)
  elif a==0x300010:ret(0,8)
  elif a==0x2879c90:ret(0,4)
  elif a==0x2884460:ret(0,12)
  elif a==0x1fa2790:ret(mode)
  elif a==0x32b4970:ret(mode)
  elif a==0x296aa30:ret(99+active)
  elif a==0x288a260:
   assert u.reg_read(UC_X86_REG_ECX)==room
   assert rd(sp+4)==0 and rd(sp+8)==0x111 and rd(sp+12)==99+active and rd(sp+16)==0x210000+active*0x1000
   registered.append(active);ret(0,32)
  elif a in (0x1ff309c,0x1ff32b1):u.emu_stop()
  elif 0x310000 <= a < 0x311000:
   pop={0x664:12,0xc30:4,0x3c0:0,0xaf8:4,0x438:8,0x430:8,0x440:4,0x77c:4}[a-0x310000]
   ret(100,pop)
 u.hook_add(UC_HOOK_CODE,hook)
 for active in slots:
  bp=0x108000;put(bp-0x99,bytes([active]));w(bp-0xa0,room)
  for reg,val in ((UC_X86_REG_EBP,bp),(UC_X86_REG_ESP,0x107000),(UC_X86_REG_ESI,party),(UC_X86_REG_EDI,room),(UC_X86_REG_EBX,0)):u.reg_write(reg,val)
  u.emu_start(0x1ff2fa7,0,count=1000)
 return dict(lookups=lookups,registered=registered)
result=dict(inputSHA256=hashlib.sha256(data).hexdigest(),oldOnlySlot0=trial([0],range(4)),allSlots=trial(range(4),range(4)),missingActor=trial(range(4),[]),scope='Actual op120 instructions 1ff2fa7..1ff309c; party and external virtual methods are explicit fixtures; no renderer/UI/UDP acceptance.')
assert result['oldOnlySlot0']['registered']==[0]
assert result['allSlots']['registered']==[0,1,2,3]
assert result['missingActor']['registered']==[]
print(json.dumps(result,indent=2))
