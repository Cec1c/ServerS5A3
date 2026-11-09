"""Execute candidate instructions and native membership search in Unicorn.

Scene/party getter results and the final registry insertion are explicit
fixtures. This proves guards, slot identity, idempotence and preservation, not
live rendering, packet timing, or full native registration side effects.
"""
from pathlib import Path
import argparse
import gc
import json
import struct
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED, UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
import build_candidate as b

CTRL, ROOM, REG, VEC = 0x30000000, 0x30001000, 0x30002000, 0x30003000
MANAGER, LOCALMGR, PARTY = 0x30004000, 0x30005000, 0x30006000
WRAPPERS, ACTORS = 0x30100000, 0x30200000
STACK, BP = 0x31008000, 0x31009000
REGS = (UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_EBX,
        UC_X86_REG_ESP, UC_X86_REG_EBP, UC_X86_REG_ESI, UC_X86_REG_EDI)

def verify(raw):
    candidate, receipt = b.build(raw)
    p = pefile.PE(data=candidate, fast_load=True)
    assert p.verify_checksum()
    image = p.get_memory_mapped_image()

    def trial(slot=1, local=0, present=(), *, legacy=False, scene=3, controller=True,
              room=True, registry=True, manager=True, wrapper=True, actor=True,
              state=1, route=1, owner=23, channel=23, party=True,
              local_manager=True, local_wrapper=True, local_model=True,
              same_party=True, uid_match=True, uid=22, insertion=True, repeats=1,
              object_kind=0, foreign_registry=False, registry_key=0x12345678,
              native_key_mode=False, sequence=None, mutate_after_insert=None):
        u = Uc(UC_ARCH_X86, UC_MODE_32)
        pages = set()
        def put(a, data):
            for page in range(a & ~4095, (a+len(data)+4095) & ~4095, 4096):
                if page not in pages:
                    u.mem_map(page, 4096); pages.add(page)
            u.mem_write(a, data)
        def w(a, v): put(a, struct.pack('<I', v & 0xffffffff))
        def rd(a): return struct.unpack('<I', u.mem_read(a, 4))[0]
        def missing(uc, access, a, size, value, _):
            if not 0x400000 <= a < 0x400000+len(image): return False
            page = a & ~4095
            if page in pages: return False
            put(page, image[page-0x400000:page-0x400000+4096])
            return True
        put(STACK-4096, bytes(0x4000))
        for a in (CTRL, ROOM, REG, VEC, MANAGER, LOCALMGR, PARTY): put(a, bytes(4096))
        w(0x57B9080, 0x12345000 if legacy else 0)
        w(0x56A3768, MANAGER if manager else 0)
        w(0x56A3758, LOCALMGR if local_manager else 0)
        w(0x57B6FB0, ACTORS+local*0x10000)
        w(ROOM+0xC4, REG if registry else 0)
        w(PARTY+0x70, route); w(PARTY+0x74, owner)
        for i in range(8): w(PARTY+0x2c+i*4, 65535)
        for i in range(4):
            wrapper_va, actor_va = WRAPPERS+i*0x1000, ACTORS+i*0x10000
            put(wrapper_va, bytes(4096))
            put(actor_va, bytes([0x5a])*0x7000)
            w(actor_va, 0x32000000)
            key = uid if i == slot else (i+1)*11
            w(wrapper_va+0x20, key); w(PARTY+0x2c+i*4, key)
            w(wrapper_va+0x4c, state)
            w(wrapper_va+0x4c8, PARTY if party else 0)
            w(wrapper_va+0x4f8, actor_va if actor or i != slot else 0)
            w(actor_va+0xa8, 0x111 if i in present else object_kind)
            w(actor_va+0xc8, REG if i in present else (REG+0x500 if foreign_registry else 0))
        if not same_party: w(WRAPPERS+local*0x1000+0x4c8, PARTY+0x200)
        w(0x32000000+0xd20, 0x33000000)
        if not local_model: w(WRAPPERS+local*0x1000+0x4f8, 0)
        if not uid_match and slot < 8: w(PARTY+0x2c+slot*4, 123)
        for i, s in enumerate(present): w(VEC+i*4, ACTORS+s*0x10000)
        w(REG+0xc, VEC); w(REG+0x10, VEC+len(present)*4)
        put(b.DATA, bytes(len(b.COUNTERS)*4))
        calls, writes, endpoints = [], [], []
        active = slot
        leaves = (0x1FA2790,0x2526B50,0x2524FB0,0x2974890,0x296DD80,0x2964680,0x288A260,
                  0x33000000,0x1455F70,0x276AAB0,0x2974460,0x296A410)
        put(0x33000000,b'\xcc')
        def ret(value=0, pop=0):
            sp=u.reg_read(UC_X86_REG_ESP); dest=rd(sp)
            u.reg_write(UC_X86_REG_ESP,sp+4+pop)
            u.reg_write(UC_X86_REG_EAX,value)
            u.reg_write(UC_X86_REG_ECX,0x11112222)
            u.reg_write(UC_X86_REG_EDX,0x33334444)
            u.reg_write(UC_X86_REG_EFLAGS,0x202)
            u.reg_write(UC_X86_REG_EIP,dest)
        def hook(uc,a,size,_):
            nonlocal active
            sp=u.reg_read(UC_X86_REG_ESP)
            if a in (b.CONTINUE,b.DONE): endpoints.append(a);u.emu_stop();return
            if a not in leaves:return
            calls.append(a)
            if a==0x1FA2790: ret(CTRL if controller else 0)
            elif a==0x2526B50: assert u.reg_read(UC_X86_REG_ECX)==CTRL;ret(scene)
            elif a==0x2524FB0: assert u.reg_read(UC_X86_REG_ECX)==CTRL;ret(ROOM if room else 0)
            elif a==0x2974890:
                assert u.reg_read(UC_X86_REG_ECX)==MANAGER
                active=rd(sp+4);assert active<8
                ret(WRAPPERS+active*0x1000 if active<4 and (wrapper or active!=slot) else 0,4)
            elif a==0x296DD80: ret(WRAPPERS+local*0x1000 if local_wrapper else 0)
            elif a==0x2964680: ret(channel)
            elif a==0x33000000:
                assert u.reg_read(UC_X86_REG_ECX)==ACTORS+active*0x10000
                ret(WRAPPERS+active*0x1000)
            elif a==0x1455F70: ret(int(native_key_mode))
            elif a==0x276AAB0:
                assert u.reg_read(UC_X86_REG_ECX)==ACTORS+active*0x10000
                ret(registry_key)
            elif a==0x2974460:
                assert u.reg_read(UC_X86_REG_ECX)==MANAGER and rd(sp+4)==WRAPPERS+active*0x1000
                ret(active,4)
            elif a==0x296A410: ret(0)
            elif a==0x288A260:
                args=[rd(sp+4+i*4) for i in range(8)]
                assert u.reg_read(UC_X86_REG_ECX)==REG
                key = active*1000 if native_key_mode else registry_key
                assert args==[0,0x111,key,ACTORS+active*0x10000,0,0,0,1],args
                if insertion:
                    end=rd(REG+0x10);w(end,args[3]);w(REG+0x10,end+4)
                    if mutate_after_insert=='room': w(ROOM+0xc4, REG+0x100)
                    elif mutate_after_insert=='party':
                        put(PARTY+0x12c,bytes(u.mem_read(PARTY+0x2c,32)))
                        for i in range(4):w(WRAPPERS+i*0x1000+0x4c8,PARTY+0x100)
                        w(PARTY+0x170,1);w(PARTY+0x174,23)
                ret(int(insertion),32)
        def wrote(uc,access,a,size,value,_):
            if ACTORS<=a<ACTORS+0x40000 or WRAPPERS<=a<WRAPPERS+0x4000:
                writes.append((a,size,value))
        u.hook_add(UC_HOOK_MEM_UNMAPPED,missing)
        u.hook_add(UC_HOOK_CODE,hook)
        u.hook_add(UC_HOOK_MEM_WRITE,wrote)
        counter_history=[]
        events=sequence if sequence is not None else [(slot,None)]*repeats
        for event_slot, ready in events:
            if ready is not None:
                w(WRAPPERS+local*0x1000+0x4f8,ACTORS+local*0x10000 if ready else 0)
            put(BP-0x99,bytes([event_slot]))
            initial=(0x10203040,0x20304050,0x30405060,0,STACK,BP,0x50607080,0x60708090)
            for reg,value in zip(REGS,initial):u.reg_write(reg,value)
            u.reg_write(UC_X86_REG_EFLAGS,0x246)
            before_endpoints=len(endpoints)
            u.emu_start(0x1FF2F27,0,count=12000)
            assert len(endpoints)==before_endpoints+1, 'Instruction bound reached without return'
            assert u.reg_read(UC_X86_REG_ESP)==STACK
            if endpoints[-1]==b.DONE:
                assert tuple(u.reg_read(reg) for reg in REGS)==initial
                # The original slot comparison changes flags before the hook.
                assert (u.reg_read(UC_X86_REG_EFLAGS)&0x40)==(0x40 if event_slot==8 else 0)
            else:
                assert all(u.reg_read(REGS[i])==initial[i] for i in (3,4,5,6,7))
            counter_history.append(list(struct.unpack('<'+str(len(b.COUNTERS))+'I',u.mem_read(b.DATA,len(b.COUNTERS)*4))))
        assert not writes,'actor/profile writes outside registration fixture'
        result=dict(slot=slot,local=local,endpoints=[hex(a) for a in endpoints],
                    counters=counter_history[-1],counterHistory=counter_history,
                    insertCalls=calls.count(0x288A260),actorWrites=writes,
                    registered=[rd(x) for x in range(VEC,rd(REG+0x10),4)])
        del u
        return result

    cases=[]
    for local in range(4):
        for slot in range(4):
            if slot==local: continue
            got=trial(slot,local,present=(local,),repeats=2)
            assert got['insertCalls']==1 and got['counters'][3:6]==[1,1,0]
            cases.append(dict(name=f'peer_{local}_{slot}_idempotent',result=got))
    negative = dict(legacy={'legacy':True}, town={'scene':1}, no_controller={'controller':False},
                    no_room={'room':False}, no_registry={'registry':False}, no_manager={'manager':False},
                    no_wrapper={'wrapper':False}, no_actor={'actor':False}, wrong_state={'state':0},
                    wrong_route={'route':8}, wrong_owner={'owner':44}, no_party={'party':False},
                    no_local_manager={'local_manager':False}, no_local_wrapper={'local_wrapper':False},
                    no_local_model={'local_model':False}, other_party={'same_party':False},
                    changed_uid={'uid_match':False}, zero_uid={'uid':0}, empty_uid={'uid':65535},
                    local_actor={'slot':0}, invalid_slot={'slot':8}, extreme_slot={'slot':255})
    negative.update(retired_actor={'object_kind':0x111}, foreign_kind={'object_kind':2},
                    foreign_registry={'foreign_registry':True})
    for name, kw in negative.items():
        got=trial(**kw);assert got['insertCalls']==0,(name,got)
        cases.append(dict(name=name,result=got))
    for name,kw in (('already_registered',dict(present=(0,1))),('namespace_zero',dict(owner=0)),
                    ('native_insert_failed',dict(insertion=False))):
        got=trial(**kw)
        if name=='already_registered':assert got['insertCalls']==0 and got['counters'][3]==1
        elif name=='namespace_zero':assert got['counters'][4]==1
        else:assert got['counters'][5]==1
        cases.append(dict(name=name,result=got))
    gc.collect()
    for name,kw in (('native_slot_key',dict(native_key_mode=True)),
                    ('native_slot_zero_key',dict(native_key_mode=True,slot=0,local=1))):
        got=trial(**kw);assert got['insertCalls']==1 and got['counters'][4]==1
        cases.append(dict(name=name,result=got))
    for local in range(4):
        peer=(local+1)%4
        got=trial(slot=peer,local=local,present=(local,),native_key_mode=True,
                  sequence=[(peer,False),(local,True),(local,True)])
        assert got['counterHistory'][0][4]==0
        assert got['counterHistory'][1][4]==3 and got['counterHistory'][2][4]==3
        assert got['insertCalls']==3 and got['counters'][8]==2
        assert got['endpoints']==[hex(b.CONTINUE)]*3
        assert set(got['registered'])=={ACTORS+i*0x10000 for i in range(4)}
        cases.append(dict(name=f'early_peer_then_local_ready_{local}',result=got))
    for change in ('room','party'):
        got=trial(slot=0,local=0,present=(0,),mutate_after_insert=change)
        assert got['insertCalls']==1 and got['counters'][8]==1
        cases.append(dict(name=f'sweep_stops_on_{change}_change',result=got))
    return dict(passed=True,cases=cases,build=receipt,nativeMembershipExecuted=True,
                nativeActorKeyExecuted=True,
                fixtures=['scene and party getters','actor-key leaf getters and mode selection','final registry insertion and return value'],
                untested=['live packet timing','full native insertion side effects','renderer','game acceptance'])

if __name__=='__main__':
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output',type=Path,required=True)
    args=ap.parse_args()
    result=verify(args.input.read_bytes())
    with args.output.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
    print(json.dumps(dict(passed=result['passed'],cases=len(result['cases']),outputSHA256=result['build']['outputSHA256'])))
