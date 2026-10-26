"""Execute the native binding/setter and cache-key arithmetic, not GUI mocks."""
from pathlib import Path
import sys, struct, json, itertools, argparse
sys.dont_write_bytecode=True
parser=argparse.ArgumentParser()
for name in ('baseline','candidate','output'): parser.add_argument('--'+name,required=True,type=Path)
parser.add_argument('--python-deps',type=Path)
args=parser.parse_args()
if args.python_deps: sys.path.insert(0,str(args.python_deps))
if args.output.exists(): raise ValueError('new output path required')
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_partyface as patch
pe=patch.pe
base=args.baseline.read_bytes()
candidate=args.candidate.read_bytes()
rebuilt,receipt=patch.build(base)
assert rebuilt==candidate

def memory(data):
    u=Uc(UC_ARCH_X86,UC_MODE_32); mapped=set()
    def put(va,blob):
        for page in range(va&~4095,(va+len(blob)+4095)&~4095,4096):
            if page not in mapped:u.mem_map(page,4096);mapped.add(page)
        u.mem_write(va,blob)
    _,sections=pe.headers(data)
    def native(va,size):
        off=pe.offset(sections,va,size);put(va,data[off:off+size])
    return u,put,native

def cache_key(key,channel,server):
    u,put,native=memory(candidate)
    native(0x1a5a5e0,0x22)
    sp=0x201000;put(sp-4096,bytes(8192))
    put(sp,struct.pack('<4I',0,key,channel,server))
    u.reg_write(UC_X86_REG_ESP,sp)
    u.emu_start(0x1a5a5e0,0x1a5a602,count=100)
    return u.reg_read(UC_X86_REG_EAX)

def case(data,key,channel,server,flags):
    u,put,native=memory(data)
    native(patch.SITE,0x0e)
    native(0x2512d00,0x2e)
    if len(data)>patch.SIZE:
        put(pe.IMAGE_BASE+patch.SIZE,data[patch.SIZE:patch.SIZE+receipt['helper_size']])
    obj,face,sp,bp=0x100000,0x110000,0x201000,0x201400
    put(obj,bytes(4096));put(face,bytes([0x71])*4096);put(sp-4096,bytes(8192))
    put(obj+2,bytes([channel,server]));put(bp-0x94,struct.pack('<I',face))
    regs=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP]
    values=[0x12121212,0,key,0x34343434,0x56565656,obj,bp,sp]
    for r,v in zip(regs,values):u.reg_write(r,v)
    u.reg_write(UC_X86_REG_EFLAGS,flags)
    before=bytes(u.mem_read(face,4096));beforeobj=bytes(u.mem_read(obj,4096));seen=[]
    def hook(emu,address,size,_):
        if address==patch.NEXT:
            got=[emu.reg_read(r) for r in regs]
            expect=values.copy();expect[2]=face;expect[7]=sp-12
            assert got==expect,(got,expect)
            assert emu.reg_read(UC_X86_REG_EFLAGS)==flags
            seen.append(struct.unpack('<3I',emu.mem_read(sp-12,12)))
    u.hook_add(UC_HOOK_CODE,hook)
    u.emu_start(patch.SITE,0x1df055d,count=100)
    assert len(seen)==1
    targetChannel,targetServer=(channel,server) if len(data)>patch.SIZE else (0,0)
    assert seen[0]==(key,targetChannel,targetServer)
    assert u.reg_read(UC_X86_REG_ESP)==sp and u.reg_read(UC_X86_REG_EBP)==bp
    expected=bytearray(before)
    struct.pack_into('<HBB',expected,0x3bc,key,targetChannel,targetServer)
    struct.pack_into('<I',expected,0x3c4,0)
    assert bytes(u.mem_read(face,4096))==expected
    assert bytes(u.mem_read(obj,4096))==beforeobj
    return cache_key(key,targetChannel,targetServer)

results=[]
for key,(channel,server),flags in itertools.product([1,19,65534],[(0,0),(23,0),(42,1),(255,255)],[0x202,0x246]):
    old=case(base,key,channel,server,flags)
    new=case(candidate,key,channel,server,flags)
    expected=(server<<24)|(channel<<16)|key
    assert new==expected and old==key
    results.append(dict(key=key,channel=channel,server=server,flags=flags,old_cache_key=old,new_cache_key=new))
result=dict(passed=True,cases=results,candidate_sha256=pe.sha(candidate),scope='Actual native setter and exact cache-key arithmetic; full register/flags preservation at callsite, stack and object boundaries. UI drawing, cache containers and live acceptance are not simulated or claimed.')
with args.output.open('x',encoding='utf8') as f: json.dump(result,f,indent=2)
print(json.dumps(dict(passed=True,cases=len(results),candidate_sha256=pe.sha(candidate))))
