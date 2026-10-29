"""Bounded native decisions with the actual active-window vector reader.

Actor directory/getters are explicit fixtures; this is not full game UI QA.
"""
import argparse,hashlib,itertools,json,struct,sys
from pathlib import Path
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import profile_refresh as p
import skill_default

def image(path):
    data=Path(path).read_bytes();assert hashlib.sha256(data).hexdigest()==skill_default.BASE_SHA256
    nt=struct.unpack_from('<I',data,0x3c)[0];opt=nt+24;st=opt+struct.unpack_from('<H',data,nt+20)[0]
    count=struct.unpack_from('<H',data,nt+6)[0];base=struct.unpack_from('<I',data,opt+28)[0]
    def read(va,n):
        for i in range(count):
            _,rva,size,raw=struct.unpack_from('<IIII',data,st+40*i+8)
            d=va-base-rva
            if 0<=d and d+n<=size:return data[raw+d:raw+d+n]
        raise ValueError(hex(va))
    return read

def case(read,kind,active,closing,keymatch,ownermatch,routematch,scene,bound,scoped=True,helper_address=0x7000000,helper_bytes=None):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set();calls=[]
    def put(a,b):
        for q in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if q not in pages:u.mem_map(q,4096);pages.add(q)
        u.mem_write(a,b)
    def w(a,v):put(a,struct.pack('<I',v&0xffffffff))
    def rd(a):return int.from_bytes(u.mem_read(a,4),'little')
    H=helper_address;SP=0x108000;BP=0x109000;manager=0x200000;window=0x220000;actor=0x230000
    put(0x100000,bytes(0x10000));put(manager,bytes(0x20000));put(window,bytes(0x4000));put(actor,bytes(0x1000))
    w(0x56a3798,manager);w(manager+0x10a8,0x210000);w(manager+0x10ac,0x210004 if active else 0x210000)
    # 0x104 + 20*0xc8 = 0x10a4; begin/end are +4/+8.
    w(0x210000,window);w(window,p.UI_VTABLE);w(window+4,0x221000);put(0x22103c,bytes([int(closing)]))
    w(window+0x38c,17 if keymatch else 29);put(window+0x3b8,bytes([42 if ownermatch else 43,1 if routematch else 2]))
    w(window+0x1b8,actor if bound else actor+0x1000);w(actor+0x20,17)
    put(0x568a519,bytes([42,1]));w(BP+4,0x22c8acd if scoped else 0x22c88dd);w(BP-0x4c8,17)
    w(BP,0x109800);w(0x109804,0x22c8acd if scoped else 0x22c88dd);w(BP+0xc,actor)
    if kind=='quiet':w(BP+4,0x22c461e)
    put(p.UI_GET,read(p.UI_GET,0x2c))
    code=helper_bytes if helper_bytes is not None else {'quiet':p.quiet_helper,'detail':p.detail_helper,'lookup':p.lookup_helper}[kind](H);put(H,code)
    endpoints=[0x22c42ec,0x22c42f7,0x22c253e,0x22c25b8,0x300000]
    leaves=[0x296d730,0x1a5a5e0,0x29614e0,0x2964680,0x2964560]
    for a in endpoints+leaves:put(a,b'\xcc')
    if kind=='lookup':w(SP,0x300000);w(SP+4,17)
    saved={UC_X86_REG_EAX:0xa0a0a0a0,UC_X86_REG_ECX:0xc1c1c1c1,UC_X86_REG_EDX:0xd2d2d2d2,UC_X86_REG_EBX:0xb0b0b0b0,UC_X86_REG_ESI:window if kind=='lookup' else 0x51515151,UC_X86_REG_EDI:0xd1d1d1d1,UC_X86_REG_EBP:BP}
    for reg,v in saved.items():u.reg_write(reg,v)
    u.reg_write(UC_X86_REG_ESP,SP);u.reg_write(UC_X86_REG_EFLAGS,0x246)
    stop=[]
    def ret(value):
        sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EIP,rd(sp));u.reg_write(UC_X86_REG_ESP,sp+4)
        u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ECX,0xcccccccc);u.reg_write(UC_X86_REG_EDX,0xdddddddd)
    def hook(_,a,n,d):
        if a in endpoints:stop.append(a);u.emu_stop();return
        if a in leaves:
            calls.append(a)
            if a==0x296d730:assert rd(u.reg_read(UC_X86_REG_ESP)+4)==17;ret(actor if scene else 0)
            elif a==0x1a5a5e0:
                sp=u.reg_read(UC_X86_REG_ESP);assert [rd(sp+i) for i in (4,8,12)]==[17,42 if ownermatch else 43,1 if routematch else 2];ret(actor+0x2000)
            else:
                assert u.reg_read(UC_X86_REG_ECX)==actor
                ret({0x29614e0:17,0x2964680:42,0x2964560:1}[a])
    u.hook_add(UC_HOOK_CODE,hook);u.emu_start(H,0,count=1000);assert len(stop)==1
    exact=active and not closing and keymatch and ownermatch and routematch
    if kind=='detail':
        ordinary=not scoped or exact and scene and bound
        assert stop[0]==(0x22c42ec if ordinary else 0x22c42f7)
        for reg,v in saved.items():assert u.reg_read(reg)==(17 if reg==UC_X86_REG_EAX and ordinary else v),(kind,reg)
    elif kind=='quiet':
        ordinary=not scoped or exact
        assert stop[0]==(0x22c253e if ordinary else 0x22c25b8)
        for reg,v in saved.items():assert u.reg_read(reg)==(actor if ordinary and reg in (UC_X86_REG_ESI,UC_X86_REG_ECX) else v)
    else:
        assert u.reg_read(UC_X86_REG_EAX)==(actor if scene else actor+0x2000 if keymatch else 0)
        for reg in (UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP):assert u.reg_read(reg)==saved[reg]
    assert u.reg_read(UC_X86_REG_ESP)==SP+(4 if kind=='lookup' else 0)
    if kind!='lookup':assert u.reg_read(UC_X86_REG_EFLAGS)==0x246
    return dict(kind=kind,active=active,closing=closing,key=keymatch,owner=ownermatch,route=routematch,scene=scene,bound=bound,scoped=scoped,stop=hex(stop[0]),calls=[hex(x) for x in calls])

def main():
    a=argparse.ArgumentParser();a.add_argument('--baseline',required=True);a.add_argument('--output',required=True);args=a.parse_args();read=image(args.baseline)
    assert read(p.DETAIL_SITE,6)==p.DETAIL_OLD and read(p.QUIET_SITE,5)==p.QUIET_OLD
    cases=[]
    for kind in ('detail','quiet'):
        for bits in itertools.product((False,True),repeat=8):cases.append(case(read,kind,*bits))
    for scene,key,owner,route in itertools.product((False,True),repeat=4):cases.append(case(read,'lookup',True,False,key,owner,route,scene,True))
    for site in p.PANEL_LOOKUP_SITES:assert read(site,5)==b'\xe8'+struct.pack('<i',0x296d730-site-5)
    out=Path(args.output);assert not out.exists();out.write_text(json.dumps(dict(passed=True,caseCount=len(cases),cases=cases,scope='Native helper decisions and actual active-window vector reader; directory/getter calls are fixtures; full game acceptance pending'),indent=2))
    print(json.dumps(dict(passed=True,caseCount=len(cases))))
if __name__=='__main__':main()
