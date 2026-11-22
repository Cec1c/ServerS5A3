"""Native guard/ABI checks with explicit lifecycle and insertion fixtures.

The native NOTI120 helper, membership search and actor-key function execute.
Completed local insertion/model binding are controlled events. This is not a
full mode1 parser, native insertion callback, renderer or gameplay proof.
"""
from pathlib import Path
import argparse
import hashlib
import itertools
import json
import struct
import sys

import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE,UC_HOOK_MEM_UNMAPPED,UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
import build_candidate as b

CTRL,ROOM,REG,VEC=0x30000000,0x30001000,0x30002000,0x30003000
MANAGER,LOCALMGR,PARTY=0x30004000,0x30005000,0x30006000
WRAPPERS,ACTORS=0x30100000,0x30200000
STACK,BP,RETURN,REENTER=0x31008000,0x31009000,0x3F000000,0x3F000100
REGS=(UC_X86_REG_EAX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_EBX,UC_X86_REG_ESP,UC_X86_REG_EBP,UC_X86_REG_ESI,UC_X86_REG_EDI)

class Runner:
    def __init__(self,image,receipt):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.image=image;self.receipt=receipt;self.pages=set()
        self.u.hook_add(UC_HOOK_MEM_UNMAPPED,self.missing)
        self.u.hook_add(UC_HOOK_CODE,self.hook)
        self.u.hook_add(UC_HOOK_MEM_WRITE,self.wrote)
        self.put(0,bytes(4096))
        self.put(STACK-0x2000,bytes(0x6000))
        for a in (CTRL,ROOM,REG,VEC,MANAGER,LOCALMGR,PARTY):self.put(a,bytes(4096))
        self.put(0x32000000,bytes(4096));self.put(0x33000000,b'\xcc')
        self.put(RETURN,b'\xcc');self.put(REENTER,b'\xcc')

    def put(self,a,data):
        for page in range(a&~4095,(a+len(data)+4095)&~4095,4096):
            if page not in self.pages:
                self.u.mem_map(page,4096);self.pages.add(page)
        self.u.mem_write(a,bytes(data))
    def w(self,a,value):self.put(a,struct.pack('<I',value&0xffffffff))
    def rd(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]
    def missing(self,u,access,a,size,value,_):
        if not 0x400000<=a<0x400000+len(self.image):return False
        page=a&~4095
        if page in self.pages:return False
        self.put(page,self.image[page-0x400000:page-0x400000+4096]);return True
    def ret(self,value=0,pop=0):
        sp=self.u.reg_read(UC_X86_REG_ESP);dest=self.rd(sp)
        self.u.reg_write(UC_X86_REG_ESP,sp+4+pop)
        self.u.reg_write(UC_X86_REG_EAX,value)
        self.u.reg_write(UC_X86_REG_ECX,0x11112222)
        self.u.reg_write(UC_X86_REG_EDX,0x33334444)
        self.u.reg_write(UC_X86_REG_EFLAGS,0x202)
        self.u.reg_write(UC_X86_REG_EIP,dest)
    def wrote(self,u,access,a,size,value,_):
        if ACTORS<=a<ACTORS+0x40000 or WRAPPERS<=a<WRAPPERS+0x4000:
            self.actor_writes.append((a,size,value))
    def hook(self,u,a,size,_):
        sp=u.reg_read(UC_X86_REG_ESP)
        if a==RETURN:u.emu_stop();return
        if a in (b.previous.CONTINUE,b.previous.DONE) and self.event=='noti120':u.emu_stop();return
        if a==b.MODE_CONTINUE and self.event=='mode1':u.emu_stop();return
        if a==REENTER:
            self.ret(int(self.options.get('insertion',True)),32);return
        if a==0x1FA2790:self.ret(CTRL if self.options.get('controller',True) else 0)
        elif a==0x2526B50:self.ret(self.options.get('scene',3))
        elif a==0x2524FB0:self.ret(ROOM if self.options.get('room',True) else 0)
        elif a==0x2964680:self.ret(23)
        elif a==0x33000000:
            actor=u.reg_read(UC_X86_REG_ECX);slot=(actor-ACTORS)//0x10000
            assert actor==ACTORS+slot*0x10000 and 0<=slot<4
            self.ret(WRAPPERS+slot*0x1000)
        elif a==0x1455F70:self.ret(1)
        elif a==0x276AAB0:self.ret(0x12345678)
        elif a==0x296A410:self.ret(0)
        elif a==0x288A260:
            args=[self.rd(sp+4+i*4) for i in range(8)]
            slot=(args[3]-ACTORS)//0x10000
            assert args==[0,0x111,slot*1000,ACTORS+slot*0x10000,0,0,0,1]
            assert u.reg_read(UC_X86_REG_ECX)==REG
            self.inserts.append(slot)
            if self.options.get('insertion',True):
                end=self.rd(REG+0x10);self.w(end,args[3]);self.w(REG+0x10,end+4)
                self.w(args[3]+0xA8,0x111);self.w(args[3]+0xC8,REG)
            if len(self.inserts)==1 and self.options.get('change')=='room':self.w(ROOM+0xC4,REG+0x800)
            elif len(self.inserts)==1 and self.options.get('change')=='party':
                self.put(PARTY+0x100,bytes(self.u.mem_read(PARTY,0x100)))
                for i in range(4):self.w(WRAPPERS+i*0x1000+0x4C8,PARTY+0x100)
            reenter=self.options.get('reenter_always') or (len(self.inserts)==1 and (self.options.get('reenter') or self.options.get('reenter_earlier')))
            if reenter:
                if self.options.get('reenter_earlier'):self.add_model(1)
                self.w(sp-4,REENTER);u.reg_write(UC_X86_REG_ESP,sp-4)
                u.reg_write(UC_X86_REG_EIP,self.receipt['entries']['sweep'])
            else:self.ret(int(self.options.get('insertion',True)),32)

    def reset(self,local=0,models=(),registered=(),**options):
        self.local=local;self.options=options;self.inserts=[];self.actor_writes=[];self.active=local
        self.put(PARTY,bytes(4096));self.put(VEC,bytes(4096));self.put(REG,bytes(4096))
        self.w(0x57B9080,0x12345000 if options.get('legacy') else 0)
        self.w(0x56A3768,MANAGER if options.get('manager',True) else 0)
        self.w(0x56A3758,LOCALMGR if options.get('local_manager',True) else 0)
        self.w(0x57B6FB0,ACTORS+local*0x10000 if options.get('self_global',True) else 0)
        self.w(LOCALMGR+0x70,WRAPPERS+local*0x1000 if options.get('local_cached',True) and options.get('local_wrapper',True) else 0)
        for i in range(8):
            self.w(MANAGER+0x48+i*4,WRAPPERS+i*0x1000 if i<4 else 0)
        if options.get('local_index')==8:self.w(MANAGER+0x48+local*4,0)
        self.w(ROOM+0xC4,REG if options.get('registry',True) else 0)
        self.w(PARTY+0x70,options.get('route',1));self.w(PARTY+0x74,options.get('owner',23))
        for i in range(8):self.w(PARTY+0x2C+i*4,65535)
        for i in range(4):
            wrapper=WRAPPERS+i*0x1000;actor=ACTORS+i*0x10000
            self.put(wrapper,bytes(4096));self.put(actor,bytes(0x7000));self.w(actor,0x32000000)
            self.w(wrapper+0x20,(i+1)*11);self.w(PARTY+0x2C+i*4,(i+1)*11)
            self.w(wrapper+0x4C,1);self.w(wrapper+0x4C8,PARTY)
            self.w(wrapper+0x4F8,actor if i in models else 0)
            self.w(actor+0xA8,0x111 if i in registered else 0)
            self.w(actor+0xC8,REG if i in registered else 0)
        self.w(0x32000000+0xD20,0x33000000)
        for i,slot in enumerate(registered):self.w(VEC+i*4,ACTORS+slot*0x10000)
        self.w(REG+0xC,VEC);self.w(REG+0x10,VEC+4*len(registered))
        self.put(b.previous.DATA,bytes(36));self.put(self.receipt['dataVA'],bytes(4*len(b.COUNTERS)))
        self.w(0,0x1234);self.actor_writes=[]

    def add_model(self,slot):self.w(WRAPPERS+slot*0x1000+0x4F8,ACTORS+slot*0x10000)
    def add_local_membership(self):
        self.add_model(self.local)
        actor=ACTORS+self.local*0x10000
        end=self.rd(REG+0x10);self.w(end,actor);self.w(REG+0x10,end+4)
        self.w(actor+0xA8,0x111);self.w(actor+0xC8,REG)
    def members(self):return [self.rd(a) for a in range(VEC,self.rd(REG+0x10),4)]
    def counters(self):return list(struct.unpack('<'+str(len(b.COUNTERS))+'I',self.u.mem_read(self.receipt['dataVA'],4*len(b.COUNTERS))))

    def run(self,event,slot=None,kind=0x111,flags=0x246,result=0x13572401):
        self.event=event;self.actor_writes=[]
        values=(result,0x20304050,0x30405060,0x40506070,STACK,BP,0x50607080,0x60708090)
        for reg,value in zip(REGS,values):self.u.reg_write(reg,value)
        self.u.reg_write(UC_X86_REG_EFLAGS,flags)
        self.w(BP-0xC,0x12345678);self.w(BP-4,0x55)
        if event=='noti120':
            self.put(BP-0x99,bytes([self.local if slot is None else slot]));start=0x1FF2F27
        elif event=='mode1':start=b.MODE_SITE
        else:
            self.w(STACK,0x12341234);self.w(STACK+4,0x56785678)
            self.w(BP,0x76543210);self.w(BP+4,RETURN)
            self.w(BP+0xC,kind);self.w(BP+0x14,ACTORS+(self.local if slot is None else slot)*0x10000)
            start=b.REG_SITE
        self.u.emu_start(start,0,count=16000)
        endpoint=self.u.reg_read(UC_X86_REG_EIP)
        expected={'noti120':(b.previous.CONTINUE,b.previous.DONE),'mode1':(b.MODE_CONTINUE,),'registered':(RETURN,)}[event]
        assert endpoint in expected,'instruction bound or unexpected control flow'
        assert not self.actor_writes,'helper directly wrote actor/profile memory'
        after=tuple(self.u.reg_read(reg) for reg in REGS)
        if event=='mode1':
            assert after==values and self.u.reg_read(UC_X86_REG_EFLAGS)==flags
            assert self.rd(BP-4)==0xffffffff,'original mode1 cleanup state lost'
        elif event=='registered':
            want=list(values);want[4]=BP+40;want[5]=0x76543210;want[6]=0x12341234;want[3]=0x56785678
            assert after==tuple(want) and self.u.reg_read(UC_X86_REG_EFLAGS)==flags,'native success return ABI lost'
        else:assert after[4]==STACK
        return dict(members=self.members(),inserts=list(self.inserts),counters=self.counters())

def verify(raw,previous_candidate=None,previous_receipt=None):
    candidate,receipt=b.build(raw)
    parent_pe=pefile.PE(data=raw,fast_load=True);new_pe=pefile.PE(data=candidate,fast_load=True)
    assert new_pe.verify_checksum() and len(candidate)==len(raw)
    old=Runner(parent_pe.get_memory_mapped_image(),receipt)
    new=Runner(new_pe.get_memory_mapped_image(),receipt)
    cases=[];all_members={ACTORS+i*0x10000 for i in range(4)}
    for local in range(4):
        peers=[x for x in range(4) if x!=local]
        for order in itertools.permutations(peers):
            for actor in (old,new):
                actor.reset(local=local,models=(),registered=())
                for slot in range(4):actor.run('noti120',slot=slot)
                actor.add_local_membership();actor.run('registered')
                for slot in order:actor.add_model(slot);actor.run('mode1')
            assert set(old.members())=={ACTORS+local*0x10000},'baseline must retain the controlled late-binding gap'
            assert set(new.members())==all_members and len(new.inserts)==3
            count=len(new.inserts);new.run('mode1');new.run('registered')
            assert len(new.inserts)==count and new.counters()[0]==0
            cases.append(dict(name=f'cold_local{local}_late_models_{order}',oldMissing=3,newMissing=0))
        for actor in (old,new):
            # Peers are bound before the local model exists. Existing NOTI120
            # rejects them at the local-model identity gate; local registration
            # is the later concrete event that makes the context usable.
            actor.reset(local=local,models=peers,registered=())
            for slot in range(4):actor.run('noti120',slot=slot)
            actor.add_local_membership();actor.run('registered')
        assert set(old.members())=={ACTORS+local*0x10000}
        assert set(new.members())==all_members
        cases.append(dict(name=f'models_before_local_registration_{local}',oldMissing=3,newMissing=0))
        for actor in (old,new):
            actor.reset(local=local,models=range(4),registered=(local,))
            for slot in peers:actor.run('noti120',slot=slot)
        assert set(old.members())==set(new.members())==all_members
        cases.append(dict(name=f'warm_existing_NOTI120_{local}',unchanged=True))

    for name,options in dict(town={'scene':1},legacy={'legacy':True},no_controller={'controller':False},
            no_room={'room':False},no_registry={'registry':False},no_manager={'manager':False},
            no_local_manager={'local_manager':False},no_local_wrapper={'local_wrapper':False},
            no_local_cached={'local_cached':False},wrong_index={'local_index':8},
            no_self={'self_global':False},route={'route':8},owner={'owner':44}).items():
        new.reset(models=range(4),registered=(0,),**options);new.run('mode1')
        assert not new.inserts and new.counters()[0]==0,(name,new.inserts)
        cases.append(dict(name=name,denied=True))
    new.reset(models=range(4),registered=());new.run('mode1');assert not new.inserts
    cases.append(dict(name='local_not_registered',denied=True))
    for event,slot,kind,result in [('registered',1,0x111,1),('registered',0,0x110,1),('registered',0,0x111,0)]:
        new.reset(models=range(4),registered=(0,));new.run(event,slot=slot,kind=kind,result=result)
        assert not new.inserts and new.counters()[1]==0
        cases.append(dict(name=f'non_local_or_kind_or_failure_{slot}_{kind}_{result}',denied=True))
    for name,addr,value in [('retired',ACTORS+0x10000+0xA8,0x111),('foreign_registry',ACTORS+0x10000+0xC8,REG+0x800),
            ('wrong_uid',PARTY+0x30,123),('not_ready',WRAPPERS+0x1000+0x4C,0)]:
        new.reset(models=range(4),registered=(0,));new.w(addr,value);new.run('mode1')
        assert 1 not in new.inserts and set(new.inserts)=={2,3},name
        cases.append(dict(name=name,other_valid_peers=2))
    for change in ('room','party'):
        new.reset(models=range(4),registered=(0,),change=change);new.run('mode1')
        assert len(new.inserts)==1 and new.counters()[0]==0
        cases.append(dict(name=f'pin_stops_{change}_switch',inserts=1))
    new.reset(models=range(4),registered=(0,),reenter=True);new.run('mode1')
    assert len(new.inserts)==3 and new.counters()[4]==1 and new.counters()[1]==2 and new.counters()[0]==0
    cases.append(dict(name='reentrant_callback',coalescedPass=True))
    new.reset(models=(0,2,3),registered=(0,),reenter_earlier=True);new.run('mode1')
    assert new.inserts==[2,3,1] and new.counters()[1]==2 and new.counters()[0]==0
    cases.append(dict(name='reentrant_earlier_slot_becomes_ready',insertOrder=[2,3,1]))
    previous_negative=None
    if previous_candidate is not None:
        assert hashlib.sha256(previous_candidate).hexdigest()=='aa0f9399f841ba6f0352d84084ecfc9493be63514ba7e820cc8738f4cb14fcf9'
        prior=Runner(pefile.PE(data=previous_candidate,fast_load=True).get_memory_mapped_image(),previous_receipt)
        prior.reset(models=(0,2,3),registered=(0,),reenter_earlier=True);prior.run('mode1')
        assert prior.inserts==[2,3] and len(prior.members())==3,'first candidate must miss the earlier-ready peer'
        previous_negative=dict(firstCandidateMissing=1,revisedMissing=0)
    new.reset(models=range(4),registered=(0,),insertion=False,reenter_always=True);new.run('mode1')
    assert len(new.inserts)==24 and new.counters()[1]==8 and new.counters()[0]==0
    cases.append(dict(name='reentrant_failed_insertions_bounded',passes=8,attempts=24))
    for flags in (0x202,0x246,0x602,0x646):
        for event in ('mode1','registered'):
            new.reset(models=range(4),registered=(0,));new.run(event,flags=flags)
            assert set(new.members())==all_members
            cases.append(dict(name=f'ABI_{event}_{flags:x}',preserved=True))

    # Re-execute all original NOTI120 guard cases on the combined candidate.
    saved=sys.modules.get('build_candidate');sys.modules['build_candidate']=b.previous
    try:old_verify=b.module('lifecycle_old_validation',b.HERE.parent/'party-initial-room-registration/verify_candidate.py')
    finally:sys.modules['build_candidate']=saved
    original=b.previous.build;b.previous.build=lambda _: (candidate,receipt)
    try:previous=old_verify.verify(raw)
    finally:b.previous.build=original
    assert previous['passed'] and len(previous['cases'])==48
    return dict(passed=True,inputSHA256=b.INPUT_SHA,outputSHA256=receipt['outputSHA256'],
        lifecycleCases=cases,lifecycleCaseCount=len(cases),previousNOTI120Cases=48,
        earlierCandidateReentrancyNegative=previous_negative,
        nativeMembershipExecuted=True,nativeActorKeyExecuted=True,nativeLocalSlotSearchExecuted=True,nativeSlotGetterExecuted=True,nativeCachedLocalGetterExecuted=True,existingNOTI120HelperUnchanged=True,
        byteWhitelistVerified=True,registersFlagsStackPreserved=True,
        fixtures=['completed native local registration / model1 binding events','native getter leaves','final registry insertion and callbacks'],
        untested=['complete mode1 parser','full insertion callback side effects','first-failure live packet timing','renderer','game acceptance'])

if __name__=='__main__':
    ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--input',type=Path,required=True);ap.add_argument('--output',type=Path,required=True)
    ap.add_argument('--previous-candidate',type=Path);ap.add_argument('--previous-receipt',type=Path)
    args=ap.parse_args();assert bool(args.previous_candidate)==bool(args.previous_receipt)
    value=verify(args.input.read_bytes(),args.previous_candidate.read_bytes() if args.previous_candidate else None,json.loads(args.previous_receipt.read_text('utf8')) if args.previous_receipt else None)
    with args.output.open('x',encoding='utf8') as f:json.dump(value,f,indent=2)
    print(json.dumps(dict(passed=True,lifecycleCases=value['lifecycleCaseCount'],previousNOTI120Cases=48,outputSHA256=value['outputSHA256'])))
