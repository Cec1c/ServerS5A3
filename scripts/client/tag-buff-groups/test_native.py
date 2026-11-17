"""Execute tag group hooks and original continuations in x86.

Manager selection/row/clear leaves are explicit fixtures; packet decoding,
item construction, skill effects and renderer are not executed here.
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
s=importlib.util.spec_from_file_location('tag_builder',Path(__file__).with_name('build_candidate.py'))
b=importlib.util.module_from_spec(s);s.loader.exec_module(b)
raw=args.base.read_bytes();candidate,receipt=b.build(raw)
assert candidate==args.candidate.read_bytes()

def run(mode,group=0,role=0,skill=14,patched=True):
    image=b.support.p.PE(candidate if patched else raw)
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set()
    def put(a,v):
        for page in range(a&~4095,(a+len(v)+4095)&~4095,4096):
            if page not in pages:u.mem_map(page,4096);pages.add(page)
        u.mem_write(a,bytes(v))
    def w(a,v):put(a,struct.pack('<I',v))
    def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
    sp,bp,actor,record,manager=0x30006000,0x30007000,0x31001000+role*0x100,0x31000000,0x32000000
    put(sp-4096,bytes(12288));put(record,bytes(0x3000));put(manager,bytes(4096))
    for va,n in [(0x145dd7f,12),(0x145ddba,5),(0x145de48,5),(0x145de54,0x16),(0x145e0bf,0x14)]:put(va,image.read(va,n))
    for ch in receipt['chunks']:put(ch['va'],image.read(ch['va'],len(bytes.fromhex(ch['hex']))))
    for va in (0x997c30,0x9982b0,0x998530,0x9983f0):put(va,b'\xc3')
    w(record+0x458,actor)
    put(bp-0x178,bytes([role|(group<<7)]))
    u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_EBP,bp)
    u.reg_write(UC_X86_REG_EBX,0);u.reg_write(UC_X86_REG_ESI,record)
    u.reg_write(UC_X86_REG_EDI,0x11223344);u.reg_write(UC_X86_REG_ECX,manager)
    u.reg_write(UC_X86_REG_EDX,record);u.reg_write(UC_X86_REG_EAX,skill)
    events=[]
    if mode=='role':start,stop=0x145dd7f,0x145dd86;expected_sp=sp
    elif mode in ('clear1','clear2'):
        start=0x145ddba if mode=='clear1' else 0x145de48;stop=start+5;w(sp,actor);expected_sp=sp+4
    elif mode=='select':start,stop=0x145de54,0x145de6a;expected_sp=sp
    else:
        assert mode=='row';start,stop=0x145e0bf,0x145e0d3;expected_sp=sp+20
        for i,v in enumerate((12,0,21,0x33000000,0)):w(sp+i*4,v)
    put(stop,b'\xcc')
    def finish(clean=0,value=0):
        p=u.reg_read(UC_X86_REG_ESP);dest=r(p)
        u.reg_write(UC_X86_REG_ESP,p+4+clean);u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_EIP,dest)
    def hook(uc,a,n,_):
        p=uc.reg_read(UC_X86_REG_ESP)
        if a==stop:uc.emu_stop()
        elif a==0x9982b0:finish(value=manager)
        elif a==0x997c30:events.append(('clear',r(p+4)));finish(4)
        elif a==0x998530:
            events.append(('select',r(p+4),r(p+8),r(p+12),r(p+16)));finish(16)
        elif a==0x9983f0:
            events.append(('row',*[r(p+i*4) for i in range(1,8)]));finish(28)
    u.hook_add(UC_HOOK_CODE,hook);u.emu_start(start,stop+1,count=200)
    assert u.reg_read(UC_X86_REG_EIP)==stop
    assert u.reg_read(UC_X86_REG_ESP)==expected_sp
    assert u.reg_read(UC_X86_REG_EBP)==bp and u.reg_read(UC_X86_REG_ESI)==record
    assert u.reg_read(UC_X86_REG_EDI)==0x11223344 and u.reg_read(UC_X86_REG_EBX)==0
    if mode=='role':
        expected_role=role if patched else role|(group<<7)
        assert u.reg_read(UC_X86_REG_EAX)==expected_role
        assert u.reg_read(UC_X86_REG_ECX)==manager
        assert bytes(u.mem_read(bp+b.ROLE,1))==bytes([role|(group<<7)])
    elif mode.startswith('clear'):
        assert events==([] if patched and group==1 else [('clear',actor)])
    elif mode=='select':assert events==[('select',actor,group if patched else 0,skill,0)]
    else:assert events==[('row',group if patched else 0,actor,12,0,21,0x33000000,0)]
    return dict(mode=mode,group=group,role=role,patched=patched,events=events)

results=[]
for patched in (False,True):
    for role in (0,1):
        for group in (0,1):
            for mode in ('role','clear1','clear2','select','row'):
                results.append(run(mode,group,role,patched=patched))
for role in (0,1):
    results.append(run('select',1,role,skill=0))
with args.result.open('x',encoding='utf8') as f:
    json.dump(dict(passed=True,cases=results,count=len(results),candidateSHA256=receipt['outputSHA256'],
        completeParser=False,realManager=False,realItems=False,skillEffects=False,GUI=False),f,indent=2)
print(str(len(results))+' x86 hook/ABI cases passed')
