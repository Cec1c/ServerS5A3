"""Replay actual x86 routing/wrappers; UI leaves are explicit fixtures, not GUI QA."""
import hashlib,itertools,json,struct,sys
from pathlib import Path
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
from build_client import INPUT_SHA,OFFSET,BEFORE,AFTER

def u32(n): return struct.pack('<I', n)
def replay(data, channel, single, state, cache):
    pe=pefile.PE(data=data,fast_load=True)
    uc=Uc(UC_ARCH_X86,UC_MODE_32)
    mapped=set()
    def put(addr,body):
        for p in range(addr&~4095,(addr+len(body)+4095)&~4095,4096):
            if p not in mapped:uc.mem_map(p,4096);mapped.add(p)
        uc.mem_write(addr,body)
    for addr,size in [(0x32b6530,0x60),(0x1a55570,0x90),(0x1a54ff0,0x60),(0x192eae0,0x10),(0x192fae0,0x40)]:
        off=pe.get_offset_from_rva(addr-pe.OPTIONAL_HEADER.ImageBase)
        put(addr,data[off:off+size])
    manager,record,owner,window,legacy,vtable=0x10000000,0x10010000,0x10020000,0x10040000,0x10050000,0x10060000
    put(manager,bytes(4096));put(record,bytes(4096));put(owner,bytes(0xd000));put(window,bytes(4096));put(legacy,bytes(0xd000));put(vtable,bytes(4096))
    put(0x56a3778,u32(manager));put(0x56a3798,u32(legacy))
    put(manager+4,u32(7));put(manager+8,u32(record if cache else 0));put(record+0xb4,bytes([single]))
    put(owner+0xc3cc,u32(window));put(legacy+0xc36c,u32(legacy));put(window,u32(vtable));put(window+0x420,u32(state))
    leaves=[0x1fa2790,0x2525920,0x1a55350,0x35a8560,0x243bea0,0x192f0a0,0x10070000,0x10071000]
    for addr in leaves:put(addr,b'\xc3')
    put(vtable+0xc,u32(0x10070000));put(vtable+8,u32(0x10071000))
    stop,stack=0x11000000,0x12000000
    put(stop,b'\x90');put(stack,bytes(0x10000))
    events=[]
    def finish(clean=0,value=None):
        sp=uc.reg_read(UC_X86_REG_ESP);ret=struct.unpack('<I',bytes(uc.mem_read(sp,4)))[0]
        uc.reg_write(UC_X86_REG_ESP,sp+4+clean)
        if value is not None:uc.reg_write(UC_X86_REG_EAX,value)
        uc.reg_write(UC_X86_REG_EIP,ret)
    def hook(u,addr,size,unused):
        if addr==0x1fa2790:finish(value=manager)
        elif addr==0x2525920:finish(value=channel)
        elif addr==0x1a55350:finish(4,record)
        elif addr==0x35a8560:
            assert struct.unpack('<I',bytes(u.mem_read(u.reg_read(UC_X86_REG_ESP)+4,4)))[0]==2
            events.append('ordinary');finish(4)
        elif addr==0x243bea0:finish()
        elif addr in (0x10070000,0x10071000):finish(4)
        elif addr==0x192f0a0:
            events.append('raid');put(window+0x420,u32(3));finish(4)
    uc.hook_add(UC_HOOK_CODE,hook)
    for _ in range(2):
        sp=stack+0xff00;put(sp,u32(stop))
        uc.reg_write(UC_X86_REG_ESP,sp);uc.reg_write(UC_X86_REG_ECX,owner);uc.reg_write(UC_X86_REG_ESI,0x76543210)
        uc.emu_start(0x32b6530,stop,count=2000)
        assert uc.reg_read(UC_X86_REG_EIP)==stop
        assert uc.reg_read(UC_X86_REG_ESP)==sp+4 and uc.reg_read(UC_X86_REG_ESI)==0x76543210
    return events

def verify(source,candidate,receipt):
    old,new=Path(source).read_bytes(),Path(candidate).read_bytes()
    assert hashlib.sha256(old).hexdigest()==INPUT_SHA
    assert old[OFFSET:OFFSET+5]==BEFORE and new==old[:OFFSET]+AFTER+old[OFFSET+5:]
    rows=[]
    for ch,single,state,cache in itertools.product(range(22,47),[0,1],[0,3,4],[0,1]):
        a,b=replay(old,ch,single,state,cache),replay(new,ch,single,state,cache)
        if a==['ordinary','ordinary']:
            assert b==(['raid'] if state==0 else [])
        else:assert a==b
        rows.append(dict(channel=ch,single=single,state=state,cached=cache,before=a,after=b))
    assert any(r['channel']==23 and r['before']==['ordinary','ordinary'] for r in rows)
    assert any(r['channel']==25 and r['before']==['ordinary','ordinary'] for r in rows)
    result=dict(passed=True,scenarios=len(rows),executions=len(rows)*4,sha256=hashlib.sha256(new).hexdigest(),bytes=len(new),changedBytes=[i for i,(a,b) in enumerate(zip(old,new)) if a!=b],guiValidated=False,fixtures=['channel getter','cache lookup','ordinary UI constructor','raid UI virtual leaves','raid state setter'],rows=rows)
    with Path(receipt).open('x',encoding='utf8') as f:json.dump(result,f,ensure_ascii=False,indent=2)
    print(json.dumps({k:v for k,v in result.items() if k!='rows'}))
if __name__=='__main__':verify(*sys.argv[1:])
