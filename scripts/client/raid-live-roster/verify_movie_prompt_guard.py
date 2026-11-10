"""Bounded x86 NOTI599 duplicate guard, native window leaves are fixtures."""
import argparse,json,struct
from pathlib import Path
import movie_prompt_guard as p
import unicorn
from unicorn.x86_const import *
def verify(before,after):
 expected,receipt=p.build(before);assert expected==after
 rows=[]
 for manager,opened in ((False,False),(True,False),(True,True)):
  for candidate in (False,True):
   im=p.Image(after if candidate else before);u=unicorn.Uc(unicorn.UC_ARCH_X86,unicorn.UC_MODE_32);mapped=set()
   def put(a,b):
    for page in range(a&~4095,(a+len(b)+4095)&~4095,4096):
     if page not in mapped:u.mem_map(page,4096);mapped.add(page)
    u.mem_write(a,b)
   def word(a,v):put(a,struct.pack('<I',v))
   def rd(a):return struct.unpack('<I',u.mem_read(a,4))[0]
   S=0x11010000;M=0x12000000;MOV=0x13000000
   put(S-0x10000,bytes(0x20000));put(M,bytes(4096));put(MOV,bytes(4096));word(0x56A3798,M if manager else 0)
   put(p.SITE,im.read(p.SITE,5));put(p.ENTRY,im.read(p.ENTRY,len(p.helper())));put(p.ORIGINAL,b'\xc3');put(0x32B55E0,b'\xc3')
   guard=bytes(range(64));put(S,guard);calls=[]
   def leaf(value,pop=0):
    sp=u.reg_read(UC_X86_REG_ESP);ret=rd(sp);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EIP,ret)
   def hook(machine,a,size,user):
    if a==0x32B55E0:
     assert manager and u.reg_read(UC_X86_REG_ECX)==M and rd(u.reg_read(UC_X86_REG_ESP)+4)==0x332
     calls.append('existing_332');u.reg_write(UC_X86_REG_ECX,0xabcdef01);u.reg_write(UC_X86_REG_EDX,0xabcdef02);u.reg_write(UC_X86_REG_EFLAGS,0x202);leaf(int(opened),4)
    elif a==p.ORIGINAL:
     assert u.reg_read(UC_X86_REG_ECX)==MOV
     calls.append('original_prompt');leaf(0x76543210)
   u.hook_add(unicorn.UC_HOOK_CODE,hook)
   initial={UC_X86_REG_EBX:0x21112222,UC_X86_REG_ESI:0x32223333,UC_X86_REG_EDI:0x43334444,UC_X86_REG_EBP:S+0x1000,UC_X86_REG_ECX:MOV,UC_X86_REG_EDX:0x12345678}
   for reg,val in initial.items():u.reg_write(reg,val)
   u.reg_write(UC_X86_REG_EAX,0x11223344);u.reg_write(UC_X86_REG_EFLAGS,0x246);u.reg_write(UC_X86_REG_ESP,S)
   u.emu_start(p.SITE,p.SITE+5,count=100)
   assert u.reg_read(UC_X86_REG_ESP)==S and bytes(u.mem_read(S,64))==guard
   assert all(u.reg_read(reg)==val for reg,val in initial.items()) and u.reg_read(UC_X86_REG_EFLAGS)==0x246
   original=not(candidate and manager and opened)
   assert ('original_prompt' in calls)==original
   assert u.reg_read(UC_X86_REG_EAX)==(0x76543210 if original else 0x11223344)
   rows.append(dict(candidate=candidate,manager=manager,confirmationOpen=opened,originalInvoked=original,calls=calls,passed=True))
 # Fail-closed identity is part of the recipe; other revisions and mutations
 # must never become eligible merely because their selected call is similar.
 corrupt=bytearray(before);corrupt[1000]^=1
 try:p.build(corrupt)
 except ValueError:pass
 else:raise AssertionError('mutated client accepted')
 return dict(passed=True,cases=rows,receipt=receipt,tamperedInputRejected=True,scope='actual network call/helper instructions; native window/original leaves are fixtures',gameAcceptance=False)
if __name__=='__main__':
 a=argparse.ArgumentParser(description=__doc__);a.add_argument('--input',type=Path,required=True);a.add_argument('--candidate',type=Path,required=True);a.add_argument('--report',type=Path,required=True);v=a.parse_args();assert not v.report.exists();r=verify(v.input.read_bytes(),v.candidate.read_bytes());v.report.write_text(json.dumps(r,indent=2),'utf8');print('guard passed',len(r['cases']),'cases')
