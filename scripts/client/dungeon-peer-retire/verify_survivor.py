"""Fixed-v173 record retirement: preserve the surviving local actor.

Executes actual kind3 call, setter, model cleanup, local-party getter and
departure predicate. Namespace decoding, actor lookup and destructor are
fixtures. Full UI, networking and gameplay are not executed.
"""
import argparse,json,hashlib
from pathlib import Path
from verify_candidate import *
import helpers
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--input',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True)
p.add_argument('--candidate',type=Path)
a=p.parse_args();data=a.input.read_bytes()
source_sha='9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
require(len(data)==111915008 and hashlib.sha256(data).hexdigest()==source_sha,'exact v173 required')
artifact=Artifact(data,data,dict(base='0x6ec5000',helpers=[dict(size=0x200),dict(size=0x100)]))
im=artifact.original
old,kind0=helpers.emit(0x6ec5000)
fixed,_=helpers.emit(0x6ec5000,preserve_local=True)
require(im.read(0x6ec5000,0x200)==old+bytes(0x200-len(old)),'old helper differs')
require(im.read(0x6ec5200,len(kind0))==kind0,'kind0 differs')
candidate_sha=None
if a.candidate:
 candidate_data=a.candidate.read_bytes();candidate=builder.PE(candidate_data)
 require(len(candidate_data)==len(data),'candidate size differs')
 require(candidate.read(0x6ec5000,0x200)==fixed+bytes(0x200-len(fixed)),'candidate kind3 differs')
 require(candidate.read(0x6ec5200,len(kind0))==kind0,'candidate kind0 differs')
 require(candidate.read(REMOVE_WINDOW,REMOVE_END-REMOVE_WINDOW)==im.read(REMOVE_WINDOW,REMOVE_END-REMOVE_WINDOW),'candidate retirement call differs')
 fixed=candidate.read(0x6ec5000,0x200)
 candidate_sha=hashlib.sha256(candidate_data).hexdigest()
class FullClear(Harness):
 def __init__(self,artifact,owner):
  self.probing=False;self.edge=None;self.run_end=None;super().__init__(artifact)
  self.put(0x296e590,im.read(0x296e590,0x42));self.put(0x201782a,im.read(0x201782a,0x49))
  for edge in (0x2017874,0x2017f95,0x2018b7b,0x2017d7c):self.put(edge,b'\xcc')
  self.w(0x56a3758,0x30009000)
  self.w(ACTOR+0x4c8,owner)
  for rec,target in ((SELF,1104),(PEER,1020)):
   self.apply_parser_fields(rec,route=1,owner=0)
   self.w(rec+4,target);self.w(rec+0x2c,1104);self.w(rec+0x30,1020)
  self.w(OTHER+0x4c8,owner)
  self.u.hook_add(UC_HOOK_CODE,self.probe_hook)
 def hook(self,u,a,n,data):
  if a==0x296dd80:self.ret(ACTOR);return
  super().hook(u,a,n,data)
 def probe_hook(self,u,a,n,data):
  if a==self.run_end:u.emu_stop();return
  if self.probing and a in (0x2017874,0x2017f95,0x2018b7b,0x2017d7c):
   self.edge=a;u.emu_stop()
 def local(self):
  self.w(STACK,STOP);self.u.reg_write(UC_X86_REG_ESP,STACK)
  self.u.emu_start(0x296e590,STOP,count=1000)
  assert self.u.reg_read(UC_X86_REG_EIP)==STOP
  return self.u.reg_read(UC_X86_REG_EAX)
 def clear(self,record):
  before=self.local()
  for slot in range(8):
   self.put(REMOVE_WINDOW,im.read(REMOVE_WINDOW,REMOVE_END-REMOVE_WINDOW))
   for reg,value in ((UC_X86_REG_ESP,STACK),(UC_X86_REG_EBP,STACK+0x200),(UC_X86_REG_EBX,record),(UC_X86_REG_ESI,slot)):self.u.reg_write(reg,value)
   self.run_end=REMOVE_END;self.u.emu_start(REMOVE_WINDOW,STOP,count=30000);self.run_end=None
   assert self.u.reg_read(UC_X86_REG_EIP)==REMOVE_END,hex(self.u.reg_read(UC_X86_REG_EIP))
   assert self.u.reg_read(UC_X86_REG_ESP)==STACK
  after=self.local();frame=STACK+0x4000
  self.put(frame-0x356,bytes([before==record]));self.put(frame-0x351,b'\x03')
  self.u.reg_write(UC_X86_REG_EBP,frame);self.u.reg_write(UC_X86_REG_EBX,record);self.u.reg_write(UC_X86_REG_ESP,STACK)
  self.probing=True;self.edge=None
  self.u.emu_start(0x201782a,STOP,count=1000);self.probing=False
  assert self.edge is not None
  return dict(record=hex(record),before=hex(before),after=hex(after),edge=hex(self.edge),networkResetPath=self.edge==0x2017874)
rows=[]
for owner in (SELF,PEER):
 for local_slot in (0,1):
  for namespace in (0,11):
   for patched in (False,True):
    h=FullClear(artifact,owner);h.namespace_owner=namespace
    for rec in (SELF,PEER):
     h.w(rec+0x2c+4*local_slot,1104);h.w(rec+0x2c+4*(1-local_slot),1020)
    if patched:h.put(0x6ec5000,fixed)
    steps=[h.clear(PEER),h.clear(SELF)]
    assert h.local()==0 and any(x['networkResetPath'] for x in steps)
    assert all(h.r(rec+0x2c+i*4)==0xffff for rec in (SELF,PEER) for i in range(8))
    assert h.r(ACTOR+0x4f8)==(MODEL if patched else 0)
    assert h.r(OTHER+0x4f8)==0
    assert (MODEL in h.destroys)==(not patched)
    rows.append(dict(initialLocalRecord=hex(owner),localSlot=local_slot,namespaceOwner=namespace,
      patched=patched,steps=steps,localModelPreserved=h.r(ACTOR+0x4f8)!=0))
out=dict(passed=True,sourceSha256=source_sha,candidateSha256=candidate_sha,cases=rows,scope=__doc__,gameAcceptance=False)
with a.receipt.open('x',encoding='utf8') as f:json.dump(out,f,indent=2)
print(json.dumps(dict(passed=True,cases=len(rows))))
