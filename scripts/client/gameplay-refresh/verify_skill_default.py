"""Execute the fixed op19 vector branch, PVF serializer and native validator.

Heap/vector allocation and actor accessors are bounded fixtures. The original
command serializer and validation bytes run in Unicorn; this is not a game
or GPU acceptance test and never reads another process's memory.
"""
from pathlib import Path
import argparse, hashlib, json, struct
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import skill_default as patch


def image_reader(path):
    raw = Path(path).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == patch.BASE_SHA256
    nt = struct.unpack_from('<I',raw,0x3c)[0]
    opt = nt+24
    count = struct.unpack_from('<H',raw,nt+6)[0]
    table = opt+struct.unpack_from('<H',raw,nt+20)[0]
    base = struct.unpack_from('<I',raw,opt+28)[0]
    def read(va,size):
        for n in range(count):
            vs,rva,rs,rp=struct.unpack_from('<IIII',raw,table+40*n+8)
            at=va-base-rva
            if 0<=at and at+size<=rs:return raw[rp+at:rp+at+size]
        raise ValueError(hex(va))
    return read


def run(read, patched, command, held, actions, helper_address=0x7000000, helper_bytes=None):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set()
    def put(addr,raw):
        for p in range(addr&~4095,(addr+len(raw)+4095)&~4095,4096):
            if p not in pages:u.mem_map(p,4096);pages.add(p)
        u.mem_write(addr,raw)
    def w(addr,n):put(addr,struct.pack('<I',n&0xffffffff))
    def r(addr):return int.from_bytes(u.mem_read(addr,4),'little')
    helper=helper_address
    put(0x2020B0A,read(0x2020B0A,0x174))
    assert read(patch.SITE,6)==(patch.redirect(helper) if helper_bytes is not None else patch.ORIGINAL)
    if patched:put(patch.SITE,patch.redirect(helper));put(helper,helper_bytes if helper_bytes is not None else patch.helper(helper))
    put(0x295B2C0,read(0x295B2C0,0x1B0))
    put(0x295C260,read(0x295C260,0x4C))
    for a in (0x89FCF0,0x37DDA80,0x29610E0,0x32B4970,0x300000):put(a,b'\xcc')
    put(0,b'\0'*4096);w(0x55B3BDC,0x55555555)
    put(0x100000,b'\0'*0x10000);put(0x200000,b'\0'*0x10000)
    ebp,sp,skill=0x108000,0x107C00,0x200000
    vector=ebp-0xac;w(vector,0x20F000);w(0x20F000,vector)
    for off in (4,8,12):w(vector+off,0)
    w(ebp-0x28,len(command));w(ebp-0x20,0x201000);w(ebp-0x18,8)
    for i,value in enumerate(command):w(0x201004+i*4,value)
    # Nested native default command container. No server-side key mapping.
    if held or actions:
        w(skill+0x102c,0x202000);w(skill+0x1030,0x202008)
        w(0x202000,0x203000);w(0x202004,0)
        w(0x203018,0x204000);w(0x20301c,0x204000+4*len(held))
        for i,value in enumerate(held):w(0x204000+i*4,value)
        w(0x203004,0x205000);w(0x203008,0x205000+0x1c*len(actions))
        for i,value in enumerate(actions):
            p=0x206000+i*0x10
            w(0x205004+i*0x1c,p);w(0x205008+i*0x1c,p+4);w(p,value)
    w(0x208000,0x209000);w(0x209D1C,0x300000)
    accepted=[];values=[];done=False;calls=0
    initial={UC_X86_REG_ESP:sp,UC_X86_REG_EBP:ebp,UC_X86_REG_EBX:0,UC_X86_REG_ESI:skill,UC_X86_REG_EDI:0,UC_X86_REG_EAX:0}
    def ret(value=0,pop=0):
        cur=u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EIP,r(cur));u.reg_write(UC_X86_REG_ESP,cur+4+pop)
        u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_ECX,0xc1c1c1c1);u.reg_write(UC_X86_REG_EDX,0xd2d2d2d2)
    def hook(_u,a,n,data):
        nonlocal done,calls
        cur=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
        if a==patch.SERIALIZE:calls+=1
        if a==0x89FCF0:
            assert ecx==vector
            begin,end=r(ecx+4),r(ecx+8)
            if not begin:begin=end=0x207000;w(ecx+4,begin);w(ecx+12,begin+256)
            assert end-begin<252
            w(end,r(r(cur+4)));w(ecx+8,end+4);ret(pop=4)
        elif a==0x37DDA80:
            assert ecx==vector
            used=r(ecx+8)-r(ecx+4)
            w(ecx+4,0x207000);w(ecx+8,0x207000+used);w(ecx+12,0x207000+r(cur+4)*4);ret(pop=4)
        elif a==0x29610E0:ret(0x208000)
        elif a==0x300000:assert ecx==0x208000;ret(0x20A000)
        elif a==0x32B4970:assert ecx==0x20A000;ret(0x20B000)
        elif a==0x295C260:
            assert ecx==0x20B000 and r(cur+4)==8 and r(cur+8)==vector
            values.extend(r(p) for p in range(r(vector+4),r(vector+8),4))
        elif a==0x295C294:
            accepted.append(bool(u.reg_read(UC_X86_REG_EAX)&255))
            # Validation is real; stop before pending-map construction/heap.
            u.reg_write(UC_X86_REG_EIP,0x295C298)
        elif a==0x2020C7E:
            assert u.reg_read(UC_X86_REG_ESP)==sp and u.reg_read(UC_X86_REG_EBP)==ebp
            assert r(0)==0
            done=True;u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    for reg,value in initial.items():u.reg_write(reg,value)
    u.emu_start(0x2020B0A,0,count=10000)
    assert done and len(accepted)==1
    expected=command if command else ([x+9 for x in held]+actions if patched else [])
    assert values==expected,(values,expected)
    assert calls==int(patched and not command)
    assert accepted==[bool(expected)],(values,accepted)
    return dict(patched=patched,custom=command,held=held,actions=actions,actualVector=values,nativeAccepted=accepted[0],defaultSerializerCalls=calls,stackPreserved=True)


def main():
    p=argparse.ArgumentParser();p.add_argument('--exe',required=True);p.add_argument('--output',required=True);a=p.parse_args()
    read=image_reader(a.exe);results=[]
    for changed in (False,True):
        for command in ([],[1,4],[3,4],[12,4]):
            for held,actions in (([],[]),([],[3,4]),([3],[4]),([],[0,1,2,4]),([0,3],[4])):
                results.append(run(read,changed,command,held,actions))
    report=dict(baseSha256=patch.BASE_SHA256,passed=True,cases=results,caseCount=len(results),scope=__doc__,guiAccepted=False)
    out=Path(a.output);assert not out.exists();out.write_text(json.dumps(report,indent=2),encoding='utf8')
    print(json.dumps({k:v for k,v in report.items() if k!='cases'}))


if __name__=='__main__':main()
