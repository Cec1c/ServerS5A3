"""Execute native confirmation completion/state3 close and scoped helper offline."""
from pathlib import Path
import argparse,json,struct
import failure_movie_close as p
import unicorn
from unicorn.x86_const import *
def verify(before,after):
 expected,receipt=p.build(before);assert after==expected;rows=[]
 cases=[('1-F',1,True,True,True,False),('2-F',1,True,True,True,False),('1-S',1,True,True,False,False),('2-S',1,True,True,False,False),('1-F',0,True,True,False,False),('1-F',1,False,True,False,False),('1-F',1,True,False,False,False),('1-F',1,True,True,False,True)]
 for movie,confirmed,current,active,shouldClose,wrong in cases:
  for patched in (False,True):
   im=p.Image(after if patched else before);u=unicorn.Uc(unicorn.UC_ARCH_X86,unicorn.UC_MODE_32);mapped=set()
   def put(a,b):
    for page in range(a&~4095,(a+len(b)+4095)&~4095,4096):
     if page not in mapped:u.mem_map(page,4096);mapped.add(page)
    u.mem_write(a,b)
   def word(a,v):put(a,struct.pack('<I',v))
   def rd(a):return struct.unpack('<I',u.mem_read(a,4))[0]
   S=0x11010000;M=0x12000000;MOV=0x13000000;TXT=0x14000000;WRITER=0x15000000
   for a in (M,MOV,TXT,WRITER):put(a,bytes(4096))
   put(S-0x10000,bytes(0x20000));word(0x56A3798,M);word(M+0xc3dc,MOV if current else MOV+0x800)
   text=('Video/OtherCutScene/' if wrong else 'Video/AntonCutScene/')+movie+'.avi';put(TXT,text.encode('utf-16le')+b'\x00\x00');word(MOV+0x3e0,TXT);word(MOV+0x3f0,len(text));word(MOV+0x3f4,40);put(MOV+0x3fc,bytes([int(active),int(active)]));word(MOV+0x3d8,2)
   put(p.SITE,im.read(p.SITE,5));put(p.ENTRY,im.read(p.ENTRY,len(p.helper())))
   # Include the complete C2 04 00 return instruction (three bytes).
   # State dispatcher also uses its four-entry native jump table at 18FC798.
   for a,z in ((p.ORIGINAL,0x18FBF1C),(0x18FC6F0,0x18FC7A8)):put(a,im.read(a,z-a))
   for a in (0x374EA10,0x374F280,0x374F390,0x3750AC0,0x38168F0,0x32B55E0,0x32B94A0):put(a,b'\xc3')
   calls=[];writes=[];trace=[]
   def leaf(value=0,pop=0):
    sp=u.reg_read(UC_X86_REG_ESP);ret=rd(sp);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EIP,ret)
   def hook(machine,a,size,user):
    trace.append(hex(a))
    sp=u.reg_read(UC_X86_REG_ESP)
    if a==0x374EA10:writes.append(rd(sp+4));leaf(WRITER,4)
    elif a in (0x374F280,0x374F390):leaf()
    elif a==0x3750AC0:calls.append('send659');leaf()
    elif a==0x38168F0:leaf(0,8)
    elif a==0x32B55E0:assert rd(sp+4)==0x2e6;leaf(1,4)
    elif a==0x32B94A0:
     assert [rd(sp+4+i*4) for i in range(3)]==[0x2e6,0xffffffff,0];calls.append('close_movie_2e6');leaf(0,12)
   u.hook_add(unicorn.UC_HOOK_CODE,hook)
   word(S,confirmed);guard=bytes(range(64));put(S+4,guard)
   initial={UC_X86_REG_EBX:0x21112222,UC_X86_REG_ESI:0x32223333,UC_X86_REG_EDI:0x43334444,UC_X86_REG_EBP:S+0x1000}
   for reg,val in initial.items():u.reg_write(reg,val)
   u.reg_write(UC_X86_REG_ECX,MOV);u.reg_write(UC_X86_REG_ESP,S)
   try:u.emu_start(p.SITE,p.SITE+5,count=1000)
   except unicorn.UcError as exc:raise AssertionError((movie,confirmed,current,active,wrong,patched,hex(u.reg_read(UC_X86_REG_EIP)),trace[-24:],calls)) from exc
   assert u.reg_read(UC_X86_REG_ESP)==S+4 and bytes(u.mem_read(S+4,64))==guard and all(u.reg_read(r)==v for r,v in initial.items()), (movie,confirmed,current,active,wrong,patched,hex(u.reg_read(UC_X86_REG_ESP)),hex(S+4),[(r,hex(u.reg_read(r)),hex(v)) for r,v in initial.items()],calls)
   assert ('send659' in calls)==bool(confirmed and active)
   if confirmed and active:assert writes==[659,1]
   assert calls.count('close_movie_2e6')==int(patched and shouldClose)
   assert bytes(u.mem_read(MOV+0x3fc,2))==b'\x00\x00'
   rows.append(dict(movie=movie,confirmed=confirmed,current=current,active=active,wrongPrefix=wrong,candidate=patched,closed=patched and shouldClose,calls=calls,passed=True))
 return dict(passed=True,receipt=receipt,cases=rows,method='real native18FBED0 and state3 dispatcher; writer/window leaves are fixtures; no renderer or game process',gameAcceptance=False)
if __name__=='__main__':
 a=argparse.ArgumentParser(description=__doc__);a.add_argument('--input',type=Path,required=True);a.add_argument('--candidate',type=Path,required=True);a.add_argument('--report',type=Path,required=True);v=a.parse_args();assert not v.report.exists();r=verify(v.input.read_bytes(),v.candidate.read_bytes());v.report.write_text(json.dumps(r,indent=2),'utf8');print('native failure close cases passed',len(r['cases']))
