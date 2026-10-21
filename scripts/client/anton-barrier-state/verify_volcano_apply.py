"""Native map-matched barrier argument checks; no controller mutation."""
from pathlib import Path
import sys,struct,json,itertools
sys.dont_write_bytecode=True
import argparse
parser=argparse.ArgumentParser(description=__doc__)
for name in ['baseline','candidate','output']:
    parser.add_argument('--'+name,type=Path,required=True)
parser.add_argument('--python-deps')
args=parser.parse_args()
if args.python_deps:sys.path.insert(0,args.python_deps)
if args.output.exists():parser.error('new output required')
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_volcano_apply as patch
pe=patch.pe
base=args.baseline.read_bytes()
candidate=args.candidate.read_bytes()
rebuilt,receipt=patch.build(base)
assert rebuilt==candidate

def case(data,rows,present,latch,match):
    u=Uc(UC_ARCH_X86,UC_MODE_32);mapped=set()
    def put(va,blob):
        for page in range(va&~4095,(va+len(blob)+4095)&~4095,4096):
            if page not in mapped:u.mem_map(page,4096);mapped.add(page)
        u.mem_write(va,blob)
    opt,sections=pe.headers(data)
    for va,size in [(0x1872709,0x4a),(0x1872470,0xb8),(0x1a57b40,0x9e),(0x224e5f0,0x1b),(0xa8f6c0,0x1b),(0x3a2d530,0x4d)]:
        off=pe.offset(sections,va,size);put(va,data[off:off+size])
    if len(data)>patch.SIZE:put(0x400000+patch.SIZE,data[patch.SIZE:patch.SIZE+receipt['helper_size']])
    obj,glob,proxy,entries,stack=0x100000,0x110000,0x120000,0x130000,0x201000
    vector,mapdata=entries+0x100,entries+0x400
    for addr,size in [(obj,4096),(glob,4096),(proxy,4096),(entries,4096),(stack-4096,8192)]:put(addr,bytes(size))
    put(0x56a3778,struct.pack('<I',glob if present else 0))
    put(glob+0xb8,struct.pack('<4I',proxy,entries,entries+8*len(rows),entries+8*len(rows)))
    put(proxy,struct.pack('<I',glob+0xb8))
    if rows:put(entries,b''.join(struct.pack('<II',*x) for x in rows))
    put(obj+0xac,bytes([latch,0x7f]))
    put(obj+0x8c,struct.pack('<II',vector,vector+8*0x30))
    put(vector+0x16c,struct.pack('<I',219))
    put(mapdata+0x5c,struct.pack('<I',219 if match else 218))
    put(0x3625320,b'\xb8'+struct.pack('<I',mapdata)+b'\xc3')
    # Actual native monster loop; world/list/classification functions provide
    # one eligible monster. The final two setters capture arguments only.
    monster,vt=entries+0x500,0x140000
    put(vt,bytes(4096));put(monster,struct.pack('<I',vt))
    def returns(va,value,pop=0):put(va,b'\xb8'+struct.pack('<I',value)+(b'\xc2'+struct.pack('<H',pop) if pop else b'\xc3'))
    put(0x56a3790,struct.pack('<I',glob))
    returns(0x91a120,0)
    returns(0x2047360,glob,4);returns(0x2046e50,glob)
    returns(0x287a8e0,1);returns(0x287a8a0,monster,4)
    returns(0x1e35ef0,monster)
    returns(0x150000,1,4);returns(0x150010,1);returns(0x150020,0)
    for off,fn in [(0x3c,0x150000),(0x990,0x150010),(0x994,0x150020)]:put(vt+off,struct.pack('<I',fn))
    put(0x27fd7e0,b'\xc2\x04\x00');put(0x27f6450,b'\xc2\x04\x00')
    regs=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP]
    for r,v in zip(regs,[0x101,obj,0x303,0x404,0x505,mapdata,stack+100,stack]):u.reg_write(r,v)
    u.reg_write(UC_X86_REG_EFLAGS,0x246)
    before=bytes(u.mem_read(obj,4096));global_before=bytes(u.mem_read(glob,4096));seen=[];arg=[];setters=[]
    def hook(emu,address,size,_):
        if address in (patch.SITE,patch.NEXT):seen.append((address,[emu.reg_read(r) for r in regs],emu.reg_read(UC_X86_REG_EFLAGS)))
        if address==0x1872470:
            sp=emu.reg_read(UC_X86_REG_ESP);arg.append(struct.unpack('<I',emu.mem_read(sp+4,4))[0])
        if address in (0x27fd7e0,0x27f6450):
            sp=emu.reg_read(UC_X86_REG_ESP)
            assert emu.reg_read(UC_X86_REG_ECX)==monster
            setters.append((address,struct.unpack('<I',emu.mem_read(sp+4,4))[0]))
    u.hook_add(UC_HOOK_CODE,hook)
    u.emu_start(0x1872709,0x1872753,count=10000)
    assert u.reg_read(UC_X86_REG_EIP)==0x1872753
    assert bytes(u.mem_read(obj,4096))==before and bytes(u.mem_read(glob,4096))==global_before
    if match:
        assert len(seen)==2
        assert seen[0][2]==seen[1][2]
        assert all(a==b for i,(a,b) in enumerate(zip(seen[0][1],seen[1][1])) if i!=2)
        assert arg==[seen[1][1][2]]
        assert setters==[(0x27fd7e0,arg[0]),(0x27f6450,arg[0])]
    else:assert seen==[] and arg==[] and setters==[]
    return arg

results=[]
states=[('empty',[]),('missing',[(110,17)]),('closed',[(111,0)]),('open',[(110,17),(111,1),(127,1)]),('invalid',[(111,2)]),('negative',[(111,0xffffffff)])]
for (name,rows),present,latch,match in itertools.product(states,[False,True],[0,1],[False,True]):
    old=case(base,rows,present,latch,match);new=case(candidate,rows,present,latch,match)
    value=dict(rows).get(111,0)
    expected=1-value if present and value<=1 else latch
    assert old==([latch] if match else [])
    assert new==([expected] if match else [])
    results.append(dict(state=name,manager=present,latch=latch,map_matches=match,baseline=old,candidate=new))
# Initializer and entire 111 callback bytes remain identical; no UI/edge change.
_,sec=pe.headers(base)
for va,size in [(0x187a3a0,0x121),(0x1877e20,0x5e0),(0x1a5bc10,0x176)]:
    off=pe.offset(sec,va,size);assert base[off:off+size]==candidate[off:off+size]
result=dict(passed=True,cases=results,baseline_sha256=pe.sha(base),candidate_sha256=pe.sha(candidate),
    scope='Native map-match, symbol getter/iterator and eligible-monster loop to both setters. World/list/classification and final setters are fixture stubs. Register/flags/stack/controller preservation; no live GUI or actual monster memory/damage claim.')
args.output.write_text(json.dumps(result,indent=2),'utf8')
print(json.dumps(dict(passed=True,cases=len(results),candidate_sha256=pe.sha(candidate))))
