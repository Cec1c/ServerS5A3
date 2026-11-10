"""Bounded x86 ABI/dispatch verification; native UI leaves are fixtures.

This does not start a game, read a process, or prove renderer acceptance.
"""
from pathlib import Path
import argparse,json,struct,sys
import build_candidate as patch
import unicorn
from unicorn.x86_const import *

def verify(before,after):
 expected,receipt=patch.build(before)
 if after!=expected:raise ValueError('candidate must match the exact recipe')
 image=patch.Image(after)
 results=[]
 for manager,window,cast,matching in [(False,False,False,False),(True,False,False,False),(True,True,False,False),(True,True,True,False),(True,True,True,True)]:
  for candidate in (False,True):
   raw=after if candidate else before;im=patch.Image(raw)
   u=unicorn.Uc(unicorn.UC_ARCH_X86,unicorn.UC_MODE_32);mapped=set()
   def put(a,b):
    for page in range(a&~4095,(a+len(b)+4095)&~4095,4096):
     if page not in mapped:u.mem_map(page,4096);mapped.add(page)
    u.mem_write(a,b)
   def word(a,v):put(a,struct.pack('<I',v))
   def rd(a):return struct.unpack('<I',u.mem_read(a,4))[0]
   def leaf(value=0,pop=0):
    sp=u.reg_read(UC_X86_REG_ESP);ret=rd(sp);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EIP,ret)
   S=0x11010000;F=S+0x1000;M=0x12000000;W=0x13000000;R=0x14000000;key=0x01170063
   put(S-0x10000,bytes(0x20000));put(M,bytes(4096));put(W,bytes(4096));put(R,bytes(4096))
   put(patch.SITE,im.read(patch.SITE,5));put(patch.ENTRY,im.read(patch.ENTRY,len(patch.helper())))
   for a in (patch.ORIGINAL,0x32B55E0,0x32B5C90,0x3E73CB6,0x1C6B5A0,0x1DF4D80):put(a,b'\xc3')
   word(0x56A3798,M if manager else 0);word(F-0x14,key);word(S,1)
   guard=bytes(range(64));put(S+4,guard)
   calls=[]
   def hook(machine,a,size,user):
    sp=u.reg_read(UC_X86_REG_ESP)
    if a==patch.ORIGINAL:
     assert u.reg_read(UC_X86_REG_ECX)==R and rd(sp+4)==1
     calls.append('original_manager_refresh');u.reg_write(UC_X86_REG_ECX,0x11112222);u.reg_write(UC_X86_REG_EDX,0x33334444);u.reg_write(UC_X86_REG_EFLAGS,0x246);leaf(0x55667788,4)
    elif a==0x32B55E0:
     assert u.reg_read(UC_X86_REG_ECX)==M and rd(sp+4)==0x7a
     calls.append('existing_window_check');leaf(int(window),4)
    elif a==0x32B5C90:
     assert rd(sp+4)==0x7a and rd(sp+8)==0
     calls.append('existing_window_get');leaf(W,8)
    elif a==0x3E73CB6:
     assert [rd(sp+4+i*4) for i in range(5)]==[W,0,0x5563694,0x556E46C,0]
     calls.append('checked_window_cast');leaf(W if cast else 0)
    elif a==0x1C6B5A0:
     assert u.reg_read(UC_X86_REG_ECX)==W
     calls.append('window_raid_key');leaf(key if matching else key+1)
    elif a==0x1DF4D80:
     assert manager and window and cast and matching and u.reg_read(UC_X86_REG_ECX)==W
     calls.append('window_refresh');u.reg_write(UC_X86_REG_ECX,0x76543210);u.reg_write(UC_X86_REG_EDX,0x98765432);u.reg_write(UC_X86_REG_EFLAGS,0x202);leaf(0x11223344)
   u.hook_add(unicorn.UC_HOOK_CODE,hook)
   initial={UC_X86_REG_EBX:0x21112222,UC_X86_REG_ESI:0x32223333,UC_X86_REG_EDI:0x43334444,UC_X86_REG_EBP:F}
   for reg,val in initial.items():u.reg_write(reg,val)
   u.reg_write(UC_X86_REG_ECX,R);u.reg_write(UC_X86_REG_ESP,S)
   u.emu_start(patch.SITE,patch.SITE+5,count=1000)
   assert u.reg_read(UC_X86_REG_EIP)==patch.SITE+5 and u.reg_read(UC_X86_REG_ESP)==S+4
   assert all(u.reg_read(reg)==val for reg,val in initial.items())
   assert (u.reg_read(UC_X86_REG_EAX),u.reg_read(UC_X86_REG_ECX),u.reg_read(UC_X86_REG_EDX),u.reg_read(UC_X86_REG_EFLAGS))==(0x55667788,0x11112222,0x33334444,0x246)
   assert bytes(u.mem_read(S+4,64))==guard and rd(F-0x14)==key
   assert calls.count('original_manager_refresh')==1
   assert ('window_refresh' in calls)==(candidate and manager and window and cast and matching)
   results.append(dict(candidate=candidate,manager=manager,window=window,cast=cast,matchingRaid=matching,calls=calls,passed=True))
 return dict(passed=True,receipt=receipt,cases=results,method='real mode3 call site and helper; original manager and UI leaves stubbed; ABI, key guards and ordering verified',rendererExecuted=False,gameAcceptance=False,installed=False)

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--report',type=Path,required=True);a=p.parse_args()
 if a.report.exists():p.error('new report required')
 result=verify(a.input.read_bytes(),a.candidate.read_bytes());a.report.write_text(json.dumps(result,indent=2)+'\n','utf8')
 print(json.dumps(dict(passed=result['passed'],cases=len(result['cases']),outputSHA256=result['receipt']['outputSHA256'],installed=False,gameAcceptance=False)))
if __name__=='__main__':main()
