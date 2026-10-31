"""Execute the actual NOTI127 callback with explicit runtime interface stubs.

This is a synthetic native regression, not a capture replay or online game.
"""
from pathlib import Path
import argparse,hashlib,json,struct
import pefile
from functools import lru_cache
from unicorn import Uc,UcError,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE,UC_HOOK_MEM_INVALID
from unicorn.x86_const import *

@lru_cache(maxsize=2)
def parse(data): return pefile.PE(data=data,fast_load=True)

def run(data, *, role=True, alignment=0, actor=True, grade=4, guild=True, cached=True):
    p=parse(data);u=Uc(UC_ARCH_X86,UC_MODE_32);mapped=set()
    def region(va,n):
        for page in range(va&~4095,(va+n+4095)&~4095,4096):
            if page not in mapped:u.mem_map(page,4096);mapped.add(page)
    def native(va,n):region(va,n);u.mem_write(va,p.get_data(va-0x400000,n))
    for va,n in [(0x2038000,0x1000),(0x296d000,0x2000),(0x37df000,0x1000),(0x3c1b000,0x1000)]:native(va,n)
    for s in p.sections:
        if s.Name.rstrip(b'\0')==b'.gld127':native(0x400000+s.VirtualAddress,s.SizeOfRawData)
    for va,n in [(0x10000000,0x20000),(0x20000000,0x10000),(0x56a3000,0x1000),(0x57b7000,0x1000),
                 (0x2961000,0x1000),(0x276b000,0x1000),(0x37e8000,0x1000),(0x2986000,0x1000),(0x2985000,0x1000)]:region(va,n)
    put=lambda a,v:u.mem_write(a,struct.pack('<I',v&0xffffffff))
    get=lambda a:struct.unpack('<I',u.mem_read(a,4))[0]
    manager=0x20001000;obj=0x20002000+alignment if role else 0;player=0x20003000;vtable=0x20004000
    guild_manager=0x20005000;guild_obj=0x20006000;stop=0x1001f000;esp=0x10018000
    put(0x56a3758,manager);put(0x56a378c,guild_manager);put(manager+0x70,obj if cached else 0)
    put(player,vtable);put(vtable+0xbb0,0x20007000)
    put(esp,stop);put(esp+4,127);put(esp+8,0)
    registers={UC_X86_REG_EBP:0x10017000,UC_X86_REG_EBX:0x11223344,UC_X86_REG_ESI:0x55667788,UC_X86_REG_EDI:0x99aabbcc}
    for reg,value in registers.items():u.reg_write(reg,value)
    u.reg_write(UC_X86_REG_ESP,esp);u.reg_write(UC_X86_REG_EFLAGS,0x246)
    calls=[];faults=[];events=[]
    def ret(value,argc=0):
        sp=u.reg_read(UC_X86_REG_ESP);ra=get(sp)
        u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+argc*4);u.reg_write(UC_X86_REG_EIP,ra)
    def code(uc,addr,size,ctx):
        sp=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
        if addr==stop:u.emu_stop()
        elif addr==0x37e82e0:
            assert get(sp+8)==1;u.mem_write(get(sp+4),bytes([grade]));events.append(['read-grade',grade]);ret(1)
        elif addr==0x29610e0:ret(player if actor else 0)
        elif addr==0x276bc30:
            assert ecx==player;events.append(['actor-grade',get(sp+4)]);ret(0,1)
        elif addr==0x20007000:
            assert ecx==player and get(sp+4)==1;events.append(['actor-refresh',1]);ret(0,1)
        elif addr==0x296d730:
            assert get(sp+4)==7;events.append(['resolve-role',bool(role)]);ret(obj)
        elif addr in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):
            if ecx==0x57b7218:put(get(sp+8),7);ret(7,2)
            elif obj and ecx==obj+0x30+0x2dc:
                assert get(sp+4)==ecx+4;put(get(sp+8),123);calls.append(hex(addr));ret(123,2)
            # A missing role executes the real codec and faults at [ECX].
        elif addr==0x2986ab0:
            assert ecx==guild_manager and get(sp+4)==123;events.append(['guild-lookup',123]);ret(guild_obj if guild else 0,1)
        elif addr==0x2985cc0:
            assert ecx==guild_obj and get(sp+4)==0;events.append(['guild-refresh',0]);ret(0,1)
    def invalid(uc,access,address,size,value,ctx):
        faults.append(dict(eip=hex(u.reg_read(UC_X86_REG_EIP)),address=hex(address),access=access));return False
    u.hook_add(UC_HOOK_CODE,code);u.hook_add(UC_HOOK_MEM_INVALID,invalid)
    error=None
    try:u.emu_start(0x20384e0,stop,count=10000)
    except UcError as e:error=str(e)
    done=u.reg_read(UC_X86_REG_EIP)==stop
    if done:
        assert not faults and u.reg_read(UC_X86_REG_ESP)==esp+4
        assert all(u.reg_read(reg)==value for reg,value in registers.items())
        assert get(esp+4)==127 and get(esp+8)==0
    return dict(returned=done,faults=faults,error=error,events=events,codecs=calls,
        eax=u.reg_read(UC_X86_REG_EAX),eflags=u.reg_read(UC_X86_REG_EFLAGS),role=role,alignment=alignment,actor=actor,grade=grade,guild=guild,cached=cached)

def main():
    p=argparse.ArgumentParser();p.add_argument('--baseline',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
    before=a.baseline.read_bytes();after=a.candidate.read_bytes();results=[];red=[]
    for actor in (False,True):
        for grade in (0,1,4,100):
            args=dict(role=False,actor=actor,grade=grade,cached=False)
            old=run(before,**args);new=run(after,**args)
            assert old['faults']==[dict(eip='0x37df6a3',address='0x30c',access=19)],old
            assert new['returned'] and not new['faults'],new
            assert old['events']==new['events'] and not new['codecs']
            red.append(old);results.append(new)
    for alignment in range(16):
        for actor in (False,True):
            for guild in (False,True):
                for cached in (False,True):
                    args=dict(alignment=alignment,actor=actor,guild=guild,cached=cached)
                    old=run(before,**args);new=run(after,**args)
                    assert old['returned'] and old==new,(old,new)
                    results.append(new)
    receipt=dict(passed=True,baselineSHA256=hashlib.sha256(before).hexdigest(),candidateSHA256=hashlib.sha256(after).hexdigest(),
        cases=len(results),baselineNullFailures=red,results=results,gameExecuted=False,captureReplay=False,
        stubs=['packet byte reader','current actor','actor grade setter and refresh','role ID resolver','valid protected integer reads','guild lookup and refresh'],
        realCode=['NOTI127 callback','role cache accessor','role +0x30 accessor','missing role protected integer dereference','candidate gate'])
    a.output.write_text(json.dumps(receipt,indent=2),'utf8');print(json.dumps({k:v for k,v in receipt.items() if k not in ('results','baselineNullFailures')}))

if __name__=='__main__':main()
