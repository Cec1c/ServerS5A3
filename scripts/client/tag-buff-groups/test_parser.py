"""Run the complete 1393 zero-item parser and real selection setter in x86.

Packet I/O, roster lookup, allocation, actor-map lookup/clear and UI are explicit
fixtures. No real items, stat effects, exception dispatch or client GUI execute.
The real SEH cookie checks are exercised using the parser's captured frame.
"""
from pathlib import Path
import argparse, importlib.util, json, struct, sys

cli=argparse.ArgumentParser(description=__doc__)
cli.add_argument('--base',type=Path,required=True)
cli.add_argument('--candidate',type=Path,required=True)
cli.add_argument('--deps',type=Path,required=True)
cli.add_argument('--result',type=Path,required=True)
args=cli.parse_args();sys.path.insert(0,str(args.deps))
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
spec=importlib.util.spec_from_file_location('builder',Path(__file__).with_name('build_candidate.py'))
b=importlib.util.module_from_spec(spec);spec.loader.exec_module(b)
raw=args.base.read_bytes();candidate,receipt=b.build(raw)
assert candidate==args.candidate.read_bytes()


def run(packets,patched):
    pe=b.support.p.PE(candidate if patched else raw)
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set()
    def put(a,v):
        for page in range(a&~4095,(a+len(v)+4095)&~4095,4096):
            if page not in pages:u.mem_map(page,4096);pages.add(page)
        u.mem_write(a,bytes(v))
    def w(a,v):put(a,struct.pack('<I',v))
    def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
    for va,n in [(0x145dc60,0x5b0),(0x998530,0xc0),(0x3fea69e,0x28)]:put(va,pe.read(va,n))
    for chunk in receipt['chunks']:put(chunk['va'],pe.read(chunk['va'],len(bytes.fromhex(chunk['hex']))))
    leaves=[0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0,
        0x296d730,0x296a380,0x37e82e0,0x37e8350,0x1456e80,0x145bee0,
        0x9982b0,0x997c30,0x8a9480,0x997a10,0x3e70882,0x32b5c90,
        0x3e73cb6,0x3e72ce4,0x3e7298d]
    for va in leaves:put(va,b'\xc3')
    stack,stop,roster,manager=0x30008000,0x33000000,0x31000000,0x32000000
    put(stack-0x4000,bytes(0x8000));put(stop,b'\xcc');put(roster,bytes(0x4000))
    put(manager,bytes(0x4000));put(0,bytes(4096))
    cookie=0x159a75c3;w(0x55b3bdc,cookie);w(0x56a3798,0)
    actors=[roster+0x2000,roster+0x2100]
    for i,actor in enumerate(actors):w(roster+i*0x500+0x458,actor)
    def selection(actor):
        assert actor in actors
        return manager+0x100+actors.index(actor)*0x100
    def finish(clean=0,value=0):
        sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
        u.reg_write(UC_X86_REG_ESP,sp+4+clean);u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_EIP,target)
    cursor=0;payload=b'';alloc=manager+0x1000;events=[];frame=0;cookie_checks=0;seh=False
    def hook(uc,va,n,_):
        nonlocal cursor,alloc,frame,cookie_checks
        sp=uc.reg_read(UC_X86_REG_ESP)
        if va==stop:uc.emu_stop()
        elif va==0x145dd1f:frame=uc.reg_read(UC_X86_REG_EBP)
        elif va in leaves[:5]:w(r(sp+8),1);finish(8)
        elif va==0x296d730:finish(value=roster)
        elif va==0x296a380:finish(8,roster)
        elif va in (0x37e82e0,0x37e8350):
            count=r(sp+8) if va==0x37e82e0 else 2
            assert cursor+count<=len(payload), 'fixture packet truncated'
            put(r(sp+4),payload[cursor:cursor+count]);cursor+=count;finish()
        elif va==0x1456e80:
            role=r(sp+4);assert role in (0,1), ('unmasked role',role)
            events.append(['role',role]);finish(4,roster+role*0x500)
        elif va==0x145bee0:raise AssertionError('unexpected roster fallback')
        elif va==0x9982b0:finish(value=manager)
        elif va==0x997c30:
            actor=r(sp+4);put(selection(actor),bytes(8));events.append(['clear',actors.index(actor)]);finish(4)
        elif va==0x8a9480:
            assert r(sp+4)==8
            alloc+=0x10;finish(value=alloc)
        elif va==0x997a10:finish(4,selection(r(r(sp+4))))
        elif va==0x3e70882:finish()
        elif va==0x32b5c90:finish(8)
        elif va==0x3e73cb6:finish()
        elif va==0x3e72ce4:
            assert uc.reg_read(UC_X86_REG_ECX)==cookie, ('bad security cookie',hex(uc.reg_read(UC_X86_REG_ECX)))
            cookie_checks+=1;finish(value=uc.reg_read(UC_X86_REG_EAX))
        elif va==0x3e7298d:
            assert seh
            uc.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    snapshots=[]
    for entries in packets:
        payload=bytes([len(entries)])+b''.join(bytes([role])+struct.pack('<H',skill)+b'\0' for role,skill in entries)
        cursor=0;w(stack,stop);w(0,0x13572468)
        saved={UC_X86_REG_EBX:0xabcdef01,UC_X86_REG_ESI:0xabcdef02,UC_X86_REG_EDI:0xabcdef03,UC_X86_REG_EBP:0xabcdef04}
        for reg,value in saved.items():u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(0x145dc60,stop+1,count=20000)
        assert u.reg_read(UC_X86_REG_EIP)==stop and cursor==len(payload)
        assert u.reg_read(UC_X86_REG_ESP)==stack+4 and r(0)==0x13572468
        for reg,value in saved.items():assert u.reg_read(reg)==value
        snapshots.append([[r(selection(actor)),r(selection(actor)+4)] for actor in actors])
    # Invoke the unchanged real handler against the frame produced by the last
    # parser call. Stop at the CRT dispatcher, not a simulated exception unwind.
    seh=True;sp=stack+0x2000;w(sp,stop);w(sp+4,0);w(sp+8,frame-0xc)
    u.reg_write(UC_X86_REG_ESP,sp);u.emu_start(0x3fea69e,0x3e7298e,count=1000)
    assert u.reg_read(UC_X86_REG_EIP)==0x3e7298d and cookie_checks==len(packets)+2
    return dict(patched=patched,packets=packets,snapshots=snapshots,events=events,cookieChecks=cookie_checks)


cases=[]
legacy=[(0,38),(1,14)]
for patched in (False,True):
    out=run([legacy,legacy],patched)
    assert out['snapshots']==[[[38,0],[14,0]]]*2
    cases.append(out)
paired=[(0,38),(128,39),(1,14),(129,10)]
for packets,expected in [
    ([paired,paired],[[[38,39],[14,10]]]*2),
    ([paired,[(1,14),(129,10),(0,38),(128,39)]],[[[38,39],[14,10]]]*2),
    ([paired,[(0,0),(128,39),(1,14),(129,0)]],[[[38,39],[14,10]],[[0,39],[14,0]]]),
    ([paired,[(0,0),(128,0),(1,0),(129,0)]],[[[38,39],[14,10]],[[0,0],[0,0]]])]:
    out=run(packets,True);assert out['snapshots']==expected;cases.append(out)
with args.result.open('x',encoding='utf8') as f:
    json.dump(dict(passed=True,count=len(cases),cases=cases,candidateSHA256=receipt['outputSHA256'],
        completeZeroItemParser=True,realSelectionSetter=True,realCookieChecks=True,
        realMapClear=False,realItems=False,skillEffects=False,exceptionUnwind=False,GUI=False),f,indent=2)
print(str(len(cases))+' complete zero-item parser/selection/frame cases passed')
