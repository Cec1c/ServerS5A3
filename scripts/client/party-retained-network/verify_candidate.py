"""Emulate the exact candidate helper; negative cases must retain the native reset."""
import argparse
import json
import struct
from pathlib import Path
import build_candidate as builder
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

MANAGER=0x30000000
RECORD=0x30001000
ACTORS=0x30002000
LOCAL=0x31000000
STACK=0x32008000
REGS=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP]

def run(code, old, new, self_slot, *, owner=11, channel=11, route=1, missing_model=None, sockets=True, bound=True, patched=True):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set()
    def put(a,b):
        for p in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if p not in pages:u.mem_map(p,4096);pages.add(p)
        u.mem_write(a,b)
    def w(a,v):put(a,struct.pack('<I',v))
    put(STACK-4096,bytes(8192));put(MANAGER,bytes(0x12000))
    w(0x56a3768,MANAGER);w(0x57b6fb0,LOCAL);w(0x56a3758,0x33000000);w(0x56a3790,0x34000000)
    w(RECORD+0x70,route);w(RECORD+0x74,owner)
    for i in range(8):
        key=old[i] if i<len(old) else 65535
        value=new[i] if i<len(new) else 65535
        w(RECORD+0x2c+4*i,value)
        a=ACTORS+i*0x1000
        w(MANAGER+0x48+4*i,a if key!=65535 else 0)
        w(a+0x20,key);w(a+0x4f8,0 if missing_model==i else (LOCAL if i==self_slot else LOCAL+0x1000*(i+1)))
        w(a+0x4c8,RECORD if bound else RECORD+0x400)
        w(0x66d27e8+4*i,i if sockets and i!=self_slot and key!=65535 else 0xffffffff)
    put(builder.BASE,code)
    for a in (builder.RETAIN,builder.FALLBACK,0x296DD80,0x2964680):put(a,b'\xcc')
    initial=[0x11223344,RECORD,0x22334455,0x33445566,0x44556677,0x55667788,0x66778899,STACK]
    for r,v in zip(REGS,initial):u.reg_write(r,v)
    u.reg_write(UC_X86_REG_EFLAGS,0x246)
    result={}
    def hook(uc,address,size,unused):
        if address in (builder.RETAIN,builder.FALLBACK):
            result['retained']=address==builder.RETAIN;uc.emu_stop()
        elif address in (0x296DD80,0x2964680):
            sp=uc.reg_read(UC_X86_REG_ESP);ret=struct.unpack('<I',uc.mem_read(sp,4))[0]
            uc.reg_write(UC_X86_REG_ESP,sp+4);uc.reg_write(UC_X86_REG_EAX,ACTORS+self_slot*0x1000 if address==0x296DD80 else channel)
            uc.reg_write(UC_X86_REG_ECX,0xabcdef01);uc.reg_write(UC_X86_REG_EDX,0xabcdef02)
            uc.reg_write(UC_X86_REG_EFLAGS,0x202);uc.reg_write(UC_X86_REG_EIP,ret)
    u.hook_add(UC_HOOK_CODE,hook)
    before=bytes(u.mem_read(MANAGER,0x12000))
    if patched:u.emu_start(builder.BASE,0,count=3000)
    else:
        # Original six bytes lead directly to the native network-reset block.
        put(builder.SITE,builder.ORIGINAL);u.emu_start(builder.SITE,builder.FALLBACK,count=10)
        result['retained']=False
    assert result
    expected=initial.copy()
    if not result['retained']:expected[2]=0x34000000
    assert [u.reg_read(r) for r in REGS]==expected, 'register/stack corruption'
    assert u.reg_read(UC_X86_REG_EFLAGS)==0x246, 'flags corruption'
    assert bytes(u.mem_read(MANAGER,0x12000))==before, 'helper modified party data'
    return result['retained']

def verify(data):
    candidate,receipt=builder.build(data);code=builder.peutils.PE(candidate).read(builder.BASE,len(builder.helper()))
    cases=[]
    def case(name,expected,old,new,self_slot,**kw):
        actual=run(code,old,new,self_slot,**kw)
        assert actual==expected,(name,actual,expected)
        cases.append(dict(name=name,retained=actual))
    for owner in (0,11):
        for n in (2,3,4):
            old=list(range(10,10+n))
            for me in range(n):
                case(f'alias-{owner}-{n}-{me}',True,old,old,me,owner=owner)
                if n>2:
                    for gone in range(n):
                        if gone==me:continue
                        new=old.copy();new[gone]=65535
                        case(f'departure-{owner}-{n}-{me}-{gone}',True,old,new,me,owner=owner)
    case('original-reproduces-alias-reset',False,[10,11,12],[10,65535,12],0,patched=False)
    case('new-party-empty-manager',False,[],[10,11],0)
    case('new-peer',False,[10,11],[10,11,12],0)
    case('replacement-peer',False,[10,11],[10,12],0)
    case('reordered-peers',False,[10,11],[11,10],0)
    case('local-left',False,[10,11,12],[65535,11,12],0)
    case('singleton',False,[10,11],[10],0)
    case('zero-uid',False,[10,11],[10,0],0)
    case('missing-peer-model',False,[10,11],[10,11],0,missing_model=1)
    case('missing-self-model',False,[10,11],[10,11],0,missing_model=0)
    case('no-existing-peer-transport',False,[10,11],[10,11],0,sockets=False)
    case('different-route',False,[10,11],[10,11],0,route=0)
    case('different-owner',False,[10,11],[10,11],0,owner=12)
    case('unknown-selected-channel',False,[10,11],[10,11],0,channel=0)
    case('unbound-record',False,[10,11],[10,11],0,bound=False)
    return dict(build=receipt,cases=cases,passed=len(cases),limitations='Fixture memory and lookup callbacks; no claim of live multiplayer acceptance.')

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--report',type=Path,required=True);a=p.parse_args()
    assert not a.report.exists()
    result=verify(a.input.read_bytes());a.report.write_text(json.dumps(result,indent=2),'utf8')
    print(json.dumps(dict(passed=result['passed'],outputSha256=result['build']['outputSha256'],report=str(a.report))))
