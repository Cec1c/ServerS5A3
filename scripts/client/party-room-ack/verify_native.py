"""Actual compiled Tick/dispatch/ABI and native membership/key helper checks.

OS/time/transport, controller getters and the insertion leaf are explicit
fixtures. No running game, renderer or full native parser is claimed.
"""
from pathlib import Path
import argparse,importlib.util,json,sys,struct,hashlib
import pefile
from unicorn.x86_const import *
HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent/'party-room-lifecycle-registration'))
import verify_candidate as v

NET,FRAME,BODY=0x36000000,0x35000000,0x35001000
class Runner(v.Runner):
 def __init__(self,image,old,receipt):
  self.new=receipt
  super().__init__(image,old)
 def setup(self,local=0,**kw):
  self.reset(local=local,models=range(4),registered=(local,),**kw)
  self.event='tick';self.ticks=1000;self.thread=99;self.sent=[];self.oldCalls=[];self.netBusy=False
  self.state=self.new['bindings']['STATE_ADDRESS'];self.put(self.state,bytes(0x3000));self.w(self.state+4,1);self.w(self.state+12,99)
  self.put(NET,bytes(0x2be000));self.put(NET+8,b'\1');self.w(0x57ed8d4,NET)
  self.apis={0x6e85b18:0x3f001000,0x6e85a90:0x3f001010,0x6e85a24:0x3f001020}
  for i,a in self.apis.items():self.w(i,a);self.put(a,b'\xc3')
 def hook(self,u,a,size,d):
  sp=u.reg_read(UC_X86_REG_ESP)
  if a==0x3f001000:
   ptr,out=self.rd(sp+4),self.rd(sp+8)
   self.put(out,struct.pack('<7I',ptr&~4095,ptr&~4095,4,0x400000,0x1000,4,0x20000));self.ret(28,12);return
  if a==0x3f001010:self.ret(self.thread);return
  if a==0x3f001020:self.ret(self.ticks);return
  if a==0x374f280:
   assert self.rd(sp+4)==0x7f14 and u.reg_read(UC_X86_REG_ECX)==NET
   self.ret(1,4);return
  if a==0x374eeb0:
   assert self.rd(sp+8)==80 and u.reg_read(UC_X86_REG_ECX)==NET
   self.sent.append(bytes(u.mem_read(self.rd(sp+4),80)));self.ret(1,8);return
  if a==0x3750ac0:self.ret(1);return
  for key in ('OLD_GETTER','OLD_DISPATCH','OLD_GATE'):
   if a==self.new['bindings'][key]:
    self.oldCalls.append(key);self.ret(0x76543210,4 if key=='OLD_GATE' else 0);return
  super().hook(u,a,size,d)
 def call(self,name,args=()):
  vals={UC_X86_REG_EBX:0x12345678,UC_X86_REG_ESI:0x23456789,UC_X86_REG_EDI:FRAME,UC_X86_REG_EBP:v.BP}
  for k,x in vals.items():self.u.reg_write(k,x)
  self.w(v.STACK,v.RETURN)
  for i,x in enumerate(args):self.w(v.STACK+4+i*4,x)
  self.u.reg_write(UC_X86_REG_ESP,v.STACK)
  self.u.emu_start(self.new['exports'][name],0,count=50000)
  assert self.u.reg_read(UC_X86_REG_EIP)==v.RETURN
  assert self.u.reg_read(UC_X86_REG_ESP)==v.STACK+4+(4 if name=='Gate' else 0)
  for k,x in vals.items():assert self.u.reg_read(k)==x,(name,k)
  return self.u.reg_read(UC_X86_REG_EAX)
 def challenge(self,peer,nonce=1,budget=15000):
  q=bytearray(64);q[:4]=b'ARQ1';q[4:7]=bytes((1,self.local,peer));struct.pack_into('<Q',q,8,nonce)
  for i in range(4):struct.pack_into('<H',q,24+2*i,(i+1)*11)
  struct.pack_into('<I',q,60,budget)
  frame=bytearray(16);frame[0]=1;struct.pack_into('<H',frame,1,0x7f14);struct.pack_into('<I',frame,3,80)
  self.put(FRAME,frame);self.put(BODY,q);self.w(0x57f9ad0,BODY);self.w(0x57f9ad4,64)
  assert self.call('Dispatch',(FRAME,))==1
  return bytes(q)

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--build',type=Path,required=True);ap.add_argument('--previous-receipt',type=Path,required=True);a=ap.parse_args()
 r=json.loads((a.build/'build.json').read_text());old=json.loads(a.previous_receipt.read_text())
 raw=(a.build/'DNF.exe').read_bytes();assert hashlib.sha256(raw).hexdigest()==r['outputSHA256']
 image=pefile.PE(data=raw).get_memory_mapped_image();cases=[]
 for local in range(4):
  for fault in ('ready','late_scene','late_room','late_model','insert_failed','wrong_thread','already','stale_epoch','expired','duplicate'):
   m=Runner(image,old,r);m.setup(local)
   peer=(local+1)%4
   if fault=='late_scene':m.options['scene']=1
   if fault=='late_room':m.options['room']=False
   if fault=='late_model':m.w(v.WRAPPERS+peer*0x1000+0x4f8,0)
   if fault=='insert_failed':m.options['insertion']=False
   if fault=='wrong_thread':m.thread=100
   if fault=='already':
    end=m.rd(v.REG+0x10);m.w(end,v.ACTORS+peer*0x10000);m.w(v.REG+0x10,end+4)
    m.w(v.ACTORS+peer*0x10000+0xa8,0x111);m.w(v.ACTORS+peer*0x10000+0xc8,v.REG)
   if fault=='stale_epoch':m.w(v.WRAPPERS+peer*0x1000+0x4f8,0)
   q=m.challenge(peer);m.call('Tick')
   if fault in ('late_scene','late_room','late_model','insert_failed','wrong_thread','stale_epoch'):
    assert not m.sent,fault
    m.options['scene']=3;m.options['room']=True;m.options['insertion']=True;m.thread=99;m.add_model(peer)
    if fault=='stale_epoch':m.w(m.state+4,2)
    m.ticks+=250;m.call('Tick')
   if fault=='expired':
    # A second challenge expires before any game-thread reconciliation.
    m.sent=[];m.challenge(peer,nonce=2,budget=1);m.ticks+=1;m.call('Tick')
   assert len(m.sent)==1,(local,fault,len(m.sent),m.inserts)
   reply=m.sent[0];expected=6 if fault=='stale_epoch' else 7 if fault=='expired' else 5
   assert reply[64]==expected,(local,fault,reply.hex())
   assert reply[:4]==b'ARR1' and reply[4:60]==q[4:60] if fault!='expired' else reply[64]==7
   count=len(m.inserts);m.ticks+=250;m.call('Tick');assert len(m.inserts)==count and len(m.sent)==1
   if fault=='duplicate':m.challenge(peer);m.call('Tick');assert len(m.sent)==1 and len(m.inserts)==count
   cases.append(dict(local=local,case=fault,status=expected,inserts=m.inserts))
 for kind,slot,result in ((0x111,0,1),(0x111,1,1),(0x110,0,1),(0x111,0,0)):
  m=Runner(image,old,r);m.setup();m.run('registered',slot=slot,kind=kind,result=result)
  assert m.rd(m.state+4)==(2 if kind==0x111 and slot==0 and result else 1)
  cases.append(dict(case='registration-ABI',kind=kind,slot=slot,result=result))
 for local in range(4):
  for missing in ('scene','room'):
   m=Runner(image,old,r);m.setup(local)
   m.options[missing]=1 if missing=='scene' else False
   # No registration query and no new mode1: the main-thread callback alone
   # observes context becoming ready after the native local event.
   m.call('Tick');assert not m.inserts and not m.sent
   m.options[missing]=3 if missing=='scene' else True
   m.ticks+=250;m.call('Tick')
   assert set(m.inserts)==set(range(4))-{local} and not m.sent
   count=len(m.inserts);m.ticks+=250;m.call('Tick');assert len(m.inserts)==count
   cases.append(dict(case='independent-no-packet-late-'+missing,local=local))
  m=Runner(image,old,r);m.setup(local)
  for peer in range(4):
   if peer!=local:m.challenge(peer,nonce=100+peer)
  m.call('Tick');assert len(m.sent)==3 and all(b[64]==5 for b in m.sent)
  assert set(m.inserts)==set(range(4))-{local}
  cases.append(dict(case='three-parallel-native-challenges',local=local))
 for fault in ('retired','foreign_registry','wrong_uid','foreign_party','not_ready','legacy','wrong_namespace'):
  m=Runner(image,old,r);m.setup();peer=1
  if fault=='retired':m.w(v.ACTORS+0x10000+0xa8,0x111)
  if fault=='foreign_registry':m.w(v.ACTORS+0x10000+0xc8,v.REG+0x800)
  if fault=='wrong_uid':m.w(v.WRAPPERS+0x1000+0x20,55)
  if fault=='foreign_party':m.w(v.WRAPPERS+0x1000+0x4c8,v.PARTY+0x100)
  if fault=='not_ready':m.w(v.WRAPPERS+0x1000+0x4c,0)
  if fault=='legacy':m.w(0x57b9080,0x1234)
  if fault=='wrong_namespace':m.w(v.PARTY+0x74,99)
  m.challenge(peer);m.call('Tick')
  assert peer not in m.inserts and not any(b[64]==5 for b in m.sent)
  cases.append(dict(case='reject-'+fault))
 m=Runner(image,old,r);m.setup(insertion=False);m.challenge(1,budget=1000)
 for step in range(0,17000,250):m.ticks=1000+step;m.call('Tick')
 count=len(m.inserts);m.ticks=50000;m.call('Tick');assert len(m.inserts)==count and m.sent[-1][64]==7
 assert len(m.sent)==1 and count<=3*60+4
 cases.append(dict(case='bounded-independent-window-and-challenge',insertCalls=count))
 m=Runner(image,old,r);m.setup();m.call('Getter');assert m.oldCalls==['OLD_GETTER']
 m.put(FRAME,bytes(16));assert m.call('Dispatch',(FRAME,))==0x76543210
 m.put(FRAME,b'\1');assert m.call('Gate',(0x7f14,))==1
 assert m.call('Gate',(0x7f13,))==0x76543210 and m.oldCalls[-1]=='OLD_GATE'
 cases.append(dict(case='cumulative-getter-dispatch-gate-ABI'))
 report=dict(passed=True,cases=cases,count=len(cases),outputSHA256=r['outputSHA256'],actualCompiledPayload=True,nativeMembershipAndActorKey=True,osTransportAndInsertionFixtures=True,renderer=False,gameAccepted=False)
 (a.build/'native.json').write_text(json.dumps(report,indent=2)+'\n',encoding='utf-8');print(json.dumps(dict(passed=True,count=len(cases))))
if __name__=='__main__':main()
