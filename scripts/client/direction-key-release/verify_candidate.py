"""Execute real input-loop/helper/wrapper x86 bytes in Unicorn, without a game.

Explicit dependencies: unicorn and capstone. No implicit local repository imports.
Win32 return values/device-window state are fixtures. Target buffer/cache writes
are performed only by the loaded original/patched machine instructions.
"""
import argparse
import ctypes
import hashlib
import itertools
import json
from pathlib import Path
import struct
import sys
import time

sys.dont_write_bytecode = True
from build_candidate import (BASE_SHA, PE, ORIGINAL, OLD_HELPER, OLD_HELPER_VA,
                             HOOK, TABLE, CACHE, ASYNC_IAT, build, checksum, sha, u32)
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
    UC_X86_REG_ESP, UC_X86_REG_EFLAGS, UC_X86_REG_EIP)
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

LOOP, POST = 0x6E8C200, 0x3840A30
COUNTER, WRAPPER, REAL_SLOT = 0x6E8C100, 0x6E833B0, 0x6E83390
STACK, OBJECT, API = 0x8008000, 0x8100000, 0x8200000
REGS = [UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,
        UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP]
PATTERNS = [(0,0),(128,0),(0,128),(128,128),(1,255),(255,1),(127,129),(53,90)]
GATES = ["ready","hresult_failed","disabled","inactive","no_window","background"]

class Machine:
    def __init__(self, pe, historical=False):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32)
        self.pages=set()
        self.table=bytes(pe.read(TABLE,256))
        self.put(TABLE,self.table)
        self.put(COUNTER,bytes(4))
        self.put(LOOP,bytes(pe.read(LOOP,0x7D)))
        self.put(WRAPPER,bytes(pe.read(WRAPPER,0x52)))
        self.put(REAL_SLOT,struct.pack("<I",API))
        self.put(ASYNC_IAT,struct.pack("<I",WRAPPER))
        self.put(CACHE,bytes(256))
        self.put(0x5846700,bytes(256))
        self.put(STACK-0x1000,bytes(0x2000))
        self.put(OBJECT,bytes(0x500))
        self.put(API,b"\xc2\x04\x00")
        self.put(POST,b"\xcc")
        sections=[s for s in pe.sections if s['name']=='.imeup']
        if sections:
            s=sections[0]; self.put(pe.base+s['rva'],bytes(pe.read(pe.base+s['rva'],s['virtual_size'])))
        if historical:
            self.put(OLD_HELPER_VA,OLD_HELPER)
            self.put(HOOK,b"\xe9"+struct.pack("<i",OLD_HELPER_VA-HOOK-5)+b"\x90"*3)
        self.calls=[]
        self.stopped=False
        def api(uc,address,size,unused):
            sp=uc.reg_read(UC_X86_REG_ESP)
            ret,vk=struct.unpack('<II',uc.mem_read(sp,8))
            self.calls.append(vk)
            assert 0x25<=vk<=0x28,('unexpected API',vk)
            uc.reg_write(UC_X86_REG_EAX,self.async_value)
            # Exercise legitimate volatile clobbers. Nonvolatile registers must
            # remain Win32 ABI-compliant; no game fields are stubbed here.
            uc.reg_write(UC_X86_REG_ECX,0xDEAD1111)
            uc.reg_write(UC_X86_REG_EDX,0xBEEF2222)
            uc.reg_write(UC_X86_REG_EFLAGS,0x246)
            uc.reg_write(UC_X86_REG_ESP,sp+8)
            uc.reg_write(UC_X86_REG_EIP,ret)
        def stop(uc,address,size,unused):
            self.stopped=True; uc.emu_stop()
        self.u.hook_add(UC_HOOK_CODE,api,begin=API,end=API)
        self.u.hook_add(UC_HOOK_CODE,stop,begin=POST,end=POST)

    def put(self,address,data):
        for page in range(address&~0xFFF,(address+len(data)+0xFFF)&~0xFFF,0x1000):
            if page not in self.pages:
                self.u.mem_map(page,0x1000);self.pages.add(page)
        self.u.mem_write(address,data)

    def run(self,vk,async_value,cache_value,pattern,gate,seed=0,alias_only=None):
        self.calls=[]; self.stopped=False; self.async_value=async_value
        u=self.u
        obj=bytearray(((i*37+19+seed)%256 for i in range(0x500)))
        obj[4]=int(gate!='disabled');obj[12]=int(gate!='inactive')
        current=bytearray([0x35]*256)
        aliases=[i for i,x in enumerate(self.table) if x==vk]
        assert len(aliases)==2
        for i,value in zip(aliases,pattern):current[i]=value
        if alias_only is not None:
            for i in aliases:current[i]=128 if i==alias_only else 0
        obj[0x10D:0x20D]=current
        previous=bytes(((i*13+seed+128)&255 for i in range(256)))
        obj[0x20D:0x30D]=previous
        cache=bytearray(256);cache[vk]=cache_value;cache[0x57]=1;cache[0]=255
        u.mem_write(OBJECT,bytes(obj));u.mem_write(CACHE,bytes(cache))
        u.mem_write(COUNTER,struct.pack('<I',17))
        u.mem_write(0x584678C,struct.pack('<I',0 if gate=='no_window' else 9))
        u.mem_write(0x5846770,struct.pack('<I',8 if gate=='background' else 9))
        u.mem_write(STACK-0x1000,b'\xA5'*0x2000)
        initial=[0x80000001 if gate=='hresult_failed' else 0,0x11223344,OBJECT,
                 0x33445566,0x778899AA,0x99AABBCC,0x55667788]
        flags=[0x202,0x246,0xA97,0x6D7][seed%4]
        for reg,value in zip(REGS,initial):u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_EFLAGS,flags);u.reg_write(UC_X86_REG_ESP,STACK)
        u.emu_start(LOOP,POST+1,count=100000)
        assert self.stopped,'instruction budget exceeded or did not reach native postprocessing'
        assert [u.reg_read(r) for r in REGS]==initial,'register preservation'
        assert u.reg_read(UC_X86_REG_ESP)==STACK-4,'stack imbalance'
        assert u.reg_read(UC_X86_REG_EFLAGS)==flags,'EFLAGS preservation'
        assert bytes(u.mem_read(STACK-4,4))==struct.pack('<I',0x6E8C278),'native postprocess return'
        assert bytes(u.mem_read(STACK,0x1000))==b'\xA5'*0x1000,'caller stack overwritten'
        after=bytes(u.mem_read(OBJECT,0x500))
        assert after[:0x10D]==obj[:0x10D] and after[0x20D:]==obj[0x20D:],'non-current keyboard fields changed'
        out={'current':after[0x10D:0x20D].hex(),'cache':bytes(u.mem_read(CACHE,256)).hex(),
             'count':u32(u.mem_read(COUNTER,4),0),'calls':list(self.calls)}
        return out,current,cache,aliases

def reference(table,current,cache,gate,async_value,patched):
    cur=bytearray(current);ca=bytearray(cache);count=17;calls=[]
    if gate=='ready':
        for scan,vk in enumerate(table):
            if not vk or not ca[vk]:continue
            if patched and 0x25<=vk<=0x28:
                calls.append(vk)
                if not async_value&0x8000:
                    ca[vk]=0
                    for i,key in enumerate(table):
                        if key==vk:cur[i]=0
                    continue
            cur[scan]|=128;count+=1
    return {'current':cur.hex(),'cache':ca.hex(),'count':count,'calls':calls}

def checksum_windows(data):
    if sys.platform!='win32':return {'available':False,'reason':'Windows ImageHlp not available'}
    dll=ctypes.WinDLL('imagehlp.dll')
    f=dll.CheckSumMappedFile
    f.argtypes=[ctypes.c_void_p,ctypes.c_uint32,ctypes.POINTER(ctypes.c_uint32),ctypes.POINTER(ctypes.c_uint32)]
    f.restype=ctypes.c_void_p
    buf=ctypes.create_string_buffer(data,len(data));header=ctypes.c_uint32();computed=ctypes.c_uint32()
    assert f(buf,len(data),ctypes.byref(header),ctypes.byref(computed))
    assert header.value==computed.value,'Windows independent checksum mismatch'
    return {'available':True,'header':hex(header.value),'computed':hex(computed.value),'match':True}

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--candidate',type=Path,required=True)
    ap.add_argument('--output-dir',type=Path,required=True)
    args=ap.parse_args();started=time.monotonic()
    data=args.input.read_bytes();candidate=args.candidate.read_bytes()
    assert sha(data)==BASE_SHA
    rebuilt,build_report=build(data)
    assert candidate==rebuilt,'independent in-memory rebuild must match entire candidate'
    p=PE(data);q=PE(candidate)
    assert q.count==41
    real=Machine(p);old=Machine(p,historical=True);new=Machine(q)
    rows=hashlib.sha256();n=0;examples=[]
    for vk,async_value,cache_value,pattern,gate in itertools.product(
            range(0x25,0x29),(0,1,0x8000,0x8001),(0,1,128,255),PATTERNS,GATES):
        actual,cur,cache,aliases=new.run(vk,async_value,cache_value,pattern,gate,n)
        historic,_,_,_=old.run(vk,async_value,cache_value,pattern,gate,n)
        original,_,_,_=real.run(vk,async_value,cache_value,pattern,gate,n)
        assert actual==historic,('historical helper mismatch',n)
        assert actual==reference(new.table,cur,cache,gate,async_value,True),('patched contract',n)
        assert original==reference(new.table,cur,cache,gate,async_value,False),('original contract',n)
        rows.update(json.dumps([vk,async_value,cache_value,pattern,gate,actual,original],sort_keys=True).encode())
        if gate=='ready' and cache_value==1 and pattern==(128,128) and vk==0x28:
            examples.append({'vk':vk,'async':hex(async_value),'aliases':aliases,
                             'old_current':[bytes.fromhex(original['current'])[i] for i in aliases],
                             'candidate_current':[bytes.fromhex(actual['current'])[i] for i in aliases],
                             'candidate_cache':bytes.fromhex(actual['cache'])[vk]})
        n+=1
        if n%512==0:print(f'native matrix {n}/3072',flush=True)
    assert n==3072
    alias_rows=[]
    for vk in range(0x25,0x29):
        for alias in [i for i,k in enumerate(new.table) if k==vk]:
            actual,cur,cache,aliases=new.run(vk,1,1,(0,0),'ready',alias,alias)
            historic,_,_,_=old.run(vk,1,1,(0,0),'ready',alias,alias)
            assert actual==historic==reference(new.table,cur,cache,'ready',1,True)
            assert all(bytes.fromhex(actual['current'])[i]==0 for i in aliases)
            alias_rows.append({'vk':hex(vk),'sole_stale_scan':hex(alias),'all_aliases_cleared':True,
                               'previous_preserved':True,'api_low_bit_only_is_released':True})
    assert len(alias_rows)==8
    # A candidate and arbitrary input must not be accepted as a new v157 base.
    rejected=[]
    for name,invalid in [('already_patched',candidate),('wrong_hash',data[:-1]+bytes([data[-1]^1]))]:
        try:build(invalid)
        except AssertionError:rejected.append(name)
        else:raise AssertionError('invalid input accepted')
    win=checksum_windows(candidate)
    assert u32(candidate,q.opt+64)==checksum(candidate,q.opt+64)
    assert sha(args.input.read_bytes())==BASE_SHA,'production changed concurrently'
    out=args.output_dir;out.mkdir(parents=True,exist_ok=True)
    md=Cs(CS_ARCH_X86,CS_MODE_32)
    sections=[s for s in q.sections if s['name']=='.imeup'];s=sections[0];va=q.base+s['rva']
    for name,address,blob in [('helper',va,q.read(va,s['virtual_size'])),('loop',LOOP,q.read(LOOP,0x7D)),
                              ('existing_async_wrapper',WRAPPER,q.read(WRAPPER,0x52))]:
        (out/(name+'.asm')).write_text('\n'.join(f'{i.address:08X} {i.bytes.hex():24} {i.mnemonic} {i.op_str}'
                                     for i in md.disasm(blob,address))+'\n',encoding='utf8')
    report={'passed':True,'input_sha256':BASE_SHA,'candidate_sha256':sha(candidate),
            'independent_rebuild_full_file_equal':True,'existing_sections_preserved':40,
            'historical_receipt_helper_exact_at_original_va':True,'historical_vectors_reused':False,
            'matrix_cases':n,'matrix_definitions':{'direction_vks':4,'async_returns':[0,1,32768,32769],
            'cache_values':[0,1,128,255],'current_alias_patterns':PATTERNS,'device_window_gates':GATES},
            'matrix_mode':'each case executes original loop, original receipt helper at old VA, relocated candidate helper',
            'matrix_trace_sha256':rows.hexdigest(),'matrix_native_executions':n*3,
            'alias_cases':alias_rows,'examples':examples,'invalid_inputs_rejected':rejected,
            'register_flags_stack_previous_preserved_every_execution':True,'native_postprocess_entry_reached':True,
            'windows_checksum':win,'duration_seconds':round(time.monotonic()-started,3),
            'limitations':['original historical test script/vectors not supplied; matrix reconstructed explicitly',
              'Win32 return and device/window state are fixtures; original API wrapper executed',
              'native loop and helpers run in Unicorn; native input postprocessor entry reached but body not emulated',
              'no game/IME GUI acceptance, no production replacement, no publication, no Go/PVF']}
    (out/'validation-receipt.json').write_text(json.dumps(report,indent=2)+'\n',encoding='utf8')
    print(json.dumps({k:report[k] for k in ('passed','candidate_sha256','matrix_cases','matrix_native_executions','duration_seconds')}),flush=True)

if __name__=='__main__':main()
