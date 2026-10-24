"""Execute fixed details parser, UI decision and cleanup; external leaves are fixtures."""
from pathlib import Path
import argparse, hashlib, json, struct, sys
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import build_candidate

BASE_SHA = 'b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902'
ENTRY, SITE, RESUME, CLEANUP, HELPER = 0x22c2390,0x22c2539,0x22c253e,0x22c25b8,0x6ec3000
MODE3_RETURN, MODE8_RETURN = 0x22c461e,0x22c8acd

def read_image(path):
    raw=Path(path).read_bytes()
    pe=struct.unpack_from('<I',raw,0x3c)[0]
    n=struct.unpack_from('<H',raw,pe+6)[0]
    opt=pe+24; table=opt+struct.unpack_from('<H',raw,pe+20)[0]
    base=struct.unpack_from('<I',raw,opt+28)[0]
    def read(va,size):
        for i in range(n):
            vs,rva,rs,rp=struct.unpack_from('<IIII',raw,table+40*i+8)
            d=va-base-rva
            if d>=0 and d+size<=rs:return raw[rp+d:rp+d+size]
        raise ValueError(hex(va))
    return raw,read

def run(image_read, helper, patch, caller, parent, count, same, visible, repeats=1):
    u=Uc(UC_ARCH_X86,UC_MODE_32); pages=set()
    def put(a,b):
        for page in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if page not in pages:u.mem_map(page,4096);pages.add(page)
        u.mem_write(a,b)
    def w(a,v):put(a,struct.pack('<I',v&0xffffffff))
    def rd(a):return int.from_bytes(u.mem_read(a,4),'little')
    put(ENTRY,image_read(ENTRY,0x278)); put(0,b'\0'*4096)
    put(0x100000,b'\0'*0x10000);put(0x200000,b'\0'*0x4000)
    if patch:
        assert image_read(SITE,5)==bytes.fromhex('8b750c8bce')
        put(SITE,b'\xe9'+struct.pack('<i',HELPER-SITE-5));put(HELPER,helper)
    leaves=(0x3376fe0,0x37e83c0,0x37e82e0,0x37dda80,0x3e70882,
            0x29614e0,0x2964680,0x2964560,0x29610e0,0x32b5380,0x32bea50,0x3e5e432,0x300000)
    for a in leaves+(caller,):put(a,b'\xcc')
    w(0x55b3bdc,0x12345678);w(0x56a3798,0x230000)
    w(0x210000,0x220000);w(0x211000,0x220000);w(0x2203ac,0x300000)
    stack,outer=0x108000,0x109000
    actor,stats,local=0x212000,0x210000,0x211000
    entries=[0x10203040+i for i in range(count)]
    payload=struct.pack('<III5B',11,22,33,4,5,6,7,count)+b''.join(struct.pack('<I',x) for x in entries)
    cursor=0; created=[]; frees=[]; live=set(); terminated=False; gate=[]
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EIP,rd(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop)
        u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ECX,0xc1c1c1c1);u.reg_write(UC_X86_REG_EDX,0xd2d2d2d2)
    def hook(_u,a,n,data):
        nonlocal cursor,terminated
        sp=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
        if a==0x3376fe0:
            put(ecx,bytes(0x40));w(ecx+0x14,0x201000);live.add(0x201000);ret()
        elif a in (0x37e83c0,0x37e82e0):
            size=4 if a==0x37e83c0 else rd(sp+8)
            assert size in (1,4) and cursor+size<=len(payload)
            put(rd(sp+4),payload[cursor:cursor+size]);cursor+=size;ret()
        elif a==0x37dda80:
            capacity=rd(sp+4);begin,end=rd(ecx+4),rd(ecx+8)
            used=end-begin;assert 0<=used<=count*4 and capacity<=1024
            old=bytes(u.mem_read(begin,used)) if used else b''
            w(ecx+4,0x202000);w(ecx+8,0x202000+used);w(ecx+12,0x202000+capacity*4)
            if old:put(0x202000,old)
            live.add(0x202000);ret(pop=4)
        elif a==0x3e70882:
            pointer=rd(sp+4);assert pointer in live,(hex(pointer),live)
            live.remove(pointer);frees.append(pointer);ret()
        elif a==SITE:
            assert cursor==len(payload)
            f=u.reg_read(UC_X86_REG_EBP)
            assert rd(f+4)==caller and rd(f)==outer
            if count:assert bytes(u.mem_read(rd(f-0x40),count*4))==payload[17:]
            gate.append((u.reg_read(UC_X86_REG_EAX),u.reg_read(UC_X86_REG_EFLAGS),sp))
        elif a in (RESUME,CLEANUP):
            if a==CLEANUP and patch and caller==MODE3_RETURN and parent==MODE8_RETURN:
                assert (u.reg_read(UC_X86_REG_EAX),u.reg_read(UC_X86_REG_EFLAGS),sp)==gate[-1]
        elif a==0x29614e0: assert ecx==actor;ret(1047)
        elif a==0x2964680: assert ecx==actor;ret(32)
        elif a==0x2964560: assert ecx==actor;ret(1)
        elif a==0x29610e0:ret(local)
        elif a==0x300000:
            assert ecx in (local,stats);ret(1047 if ecx==stats or same else 1006)
        elif a==0x32b5380:assert ecx==0x230000;ret(int(visible))
        elif a==0x32bea50:
            assert ecx==0x230000
            kind,desc,last=[rd(sp+4*i) for i in (1,2,3)]
            assert kind==(0xf0 if same and not visible else 0xc8) and last==0
            if kind==0xc8:
                assert rd(desc)==1047 and [rd(desc+4*i) for i in (1,2,3)]==[11,22,33]
                assert bytes(u.mem_read(desc+0x10,4))==bytes([4,5,6,7])
            created.append(kind);ret(pop=12)
        elif a==0x3e5e432:raise AssertionError('native length failure')
        elif a==caller:
            assert sp==stack+4 and u.reg_read(UC_X86_REG_EBP)==outer
            assert rd(0)==0xabcdef01 and not live
            for reg,value in ((UC_X86_REG_EBX,0xb0b0b0b0),(UC_X86_REG_ESI,0x51515151),(UC_X86_REG_EDI,0xd1d1d1d1)):
                assert u.reg_read(reg)==value
            terminated=True;u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    for iteration in range(repeats):
        cursor=0;terminated=False;assert not live
        w(0,0xabcdef01);w(stack,caller);w(stack+4,stats);w(stack+8,actor);w(outer+4,parent)
        guard=bytes(range(64));put(stack+12,guard)
        for reg,value in ((UC_X86_REG_ESP,stack),(UC_X86_REG_EBP,outer),(UC_X86_REG_EBX,0xb0b0b0b0),(UC_X86_REG_ESI,0x51515151),(UC_X86_REG_EDI,0xd1d1d1d1)):
            u.reg_write(reg,value)
        u.emu_start(ENTRY,0,count=100000)
        assert terminated and cursor==len(payload) and bytes(u.mem_read(stack+12,64))==guard
    silent=patch and caller==MODE3_RETURN and parent==MODE8_RETURN
    assert len(created)==(0 if silent else repeats)
    assert len(frees)==repeats*(2 if count else 1)
    return dict(patched=patch,caller=hex(caller),parent=hex(parent),count=count,same=same,visible=visible,repeats=repeats,uiCalls=len(created),fullyParsed=True,allocationCleanup=True,stackAndSavedRegisters=True)

def probe_image(path):
    import ctypes, os
    require = build_candidate.require
    require(os.name == "nt", "Windows is required for native image validation")
    from ctypes import wintypes as w
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateFileW.argtypes = [w.LPCWSTR, w.DWORD, w.DWORD, w.LPVOID, w.DWORD, w.DWORD, w.HANDLE]
    kernel.CreateFileW.restype = w.HANDLE
    kernel.CreateFileMappingW.argtypes = [w.HANDLE, w.LPVOID, w.DWORD, w.DWORD, w.DWORD, w.LPCWSTR]
    kernel.CreateFileMappingW.restype = w.HANDLE
    kernel.CloseHandle.argtypes, kernel.CloseHandle.restype = [w.HANDLE], w.BOOL
    handle = kernel.CreateFileW(str(Path(path).resolve()), 0x80000000, 7, None, 3, 0, None)
    if handle == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        # PAGE_READONLY | SEC_IMAGE_NO_EXECUTE: validates image, no entrypoint execution.
        mapping = kernel.CreateFileMappingW(handle, None, 0x11000002, 0, 0, None)
        error = 0 if mapping else ctypes.get_last_error()
        if mapping:
            kernel.CloseHandle(mapping)
        return dict(mappingSucceeded=bool(mapping), win32Error=error, executed=False)
    finally:
        kernel.CloseHandle(handle)


def main():
    assert __debug__,'optimization disables validation'
    import unicorn
    assert unicorn.__version__=='2.1.4'
    p=argparse.ArgumentParser();p.add_argument('--baseline',required=True);p.add_argument('--candidate',required=True);p.add_argument('--output',required=True);a=p.parse_args()
    raw,read=read_image(a.baseline);assert hashlib.sha256(raw).hexdigest()==BASE_SHA
    expected,receipt=build_candidate.build(raw)
    candidate,candidate_read=read_image(a.candidate)
    assert candidate==expected,'candidate does not match the complete pinned build'
    helper=candidate_read(HELPER,receipt['helper']['size'])
    assert helper==build_candidate.build_helper()
    windowsBefore=probe_image(a.baseline);windowsAfter=probe_image(a.candidate)
    assert windowsBefore["mappingSucceeded"] and windowsAfter["mappingSucceeded"]
    results=[]
    for patched in (False,True):
        for caller,parent in ((MODE3_RETURN,MODE8_RETURN),(MODE3_RETURN,0x22c88dd),(0x22c4971,MODE8_RETURN),(0x22c4971,0)):
            for count in (0,1,3,255):
                for same,visible in ((False,False),(False,True),(True,False),(True,True)):
                    results.append(run(read,helper,patched,caller,parent,count,same,visible))
    results.append(run(read,helper,True,MODE3_RETURN,MODE8_RETURN,3,False,False,1000))
    out=Path(a.output);assert not out.exists();out.mkdir(parents=True)
    report=dict(passed=True,windowsBefore=windowsBefore,windowsAfter=windowsAfter,baselineSha256=BASE_SHA,candidateSha256=hashlib.sha256(candidate).hexdigest(),fullCandidateReconstruction=True,helperSha256=hashlib.sha256(helper).hexdigest(),cases=results,caseCount=len(results),scope='Actual full details consumer 22c2390 through cleanup/return. Packet reads, allocator/reserve/free, actor getters, local stat and UI manager are fixtures. Repeated cleanup is scoped to temporary descriptor allocations. No full mode3, GPU, real game heap or crash-free live claim.',guiAccepted=False,processMemoryRead=False)
    (out/'proof.json').write_text(json.dumps(report,indent=2),encoding='utf8');print(json.dumps({k:v for k,v in report.items() if k!='cases'}))
if __name__=='__main__':main()
