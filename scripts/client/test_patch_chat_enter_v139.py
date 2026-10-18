"""Execute both exact native publishers; UI/delegate leaves are ABI fixtures."""
from pathlib import Path
import ctypes
from ctypes import wintypes
import hashlib,json,struct,sys,tempfile

sys.path.insert(0,'H:/86JP启动器/_dist/selftest/new-exe-loader-ipc-20261003/emulator-deps')
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_chat_enter_v139 as patch

ROOT=Path(__file__).resolve().parents[2]
INPUT=Path('E:/90/client-v138-party-feature-gates-20261018/client/DNF.exe')
CANDIDATE=ROOT.parent/'client/DNF.exe'
OUT=ROOT.parent/'test-receipt.json'


def u32(b):return struct.unpack('<I',b)[0]


class Publisher:
    def __init__(self,data):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.pages=set();self.events=[]
        self.map(0,0x1000)
        opt,sections=patch.pe.headers(data)
        for va,size in ((0x29ba8f0,0x150),(0x3840030,0xf0),(0x385fa80,16),
                        (0x32b4970,16),(0x8cfe80,16)):
            self.write(va,data[patch.pe.offset(sections,va,size):patch.pe.offset(sections,va,size)+size])
        # Copy the actual complete RX adapter, including the candidate helper.
        va,size,raw=sections[-1];self.write(va,data[raw:raw+size])
        self.INP=0x20000000;self.SCREEN=0x20010000;self.UI=0x20020000
        self.WRAPPER=0x20030000;self.WIDGET=0x20040000;self.VTABLE=0x20050000
        self.STACK=0x10008000
        self.map(self.INP,0x60000);self.map(0x10000000,0x10000)
        for va in (0x55b3bdc,0x6575e78,0x6590638,0x56a3798):self.map(va,4)
        self.w(0x6575e78,self.INP);self.w(0x56a3798,self.UI)
        self.w(self.INP,self.VTABLE);self.w(self.VTABLE+0x14,0x3840030)
        self.raw=list(range(136))
        for key,raw in ((16,28),(4,29),(7,42),(10,56)):
            other=self.raw.index(raw);self.raw[key],self.raw[other]=self.raw[other],self.raw[key]
        for key,raw in enumerate(self.raw):self.w(self.INP+0x310+4*key,raw)
        self.w(self.UI+0xc0,self.WRAPPER)
        self.w(self.SCREEN+0x1b0,self.VTABLE+0x100);self.w(self.VTABLE+0x100,0x10001000)
        self.write(0x3361970,b'\xc3')
        self.write(0x347c5f0,b'\xc2\x04\x00')
        self.write(0x10001000,b'\xc2\x10\x00')
        self.u.hook_add(UC_HOOK_CODE,self.hook)

    def map(self,va,size):
        for page in range(va&~4095,(va+size+4095)&~4095,4096):
            if page not in self.pages:self.u.mem_map(page,4096);self.pages.add(page)
    def write(self,va,b):self.map(va,len(b));self.u.mem_write(va,b)
    def w(self,va,n):self.write(va,struct.pack('<I',n))
    def hook(self,u,pc,size,_):
        if pc==self.ret:u.emu_stop()
        elif pc==0x347c5f0:
            out=u32(u.mem_read(u.reg_read(UC_X86_REG_ESP)+4,4))
            self.w(out,self.WIDGET);self.w(out+4,0);u.reg_write(UC_X86_REG_EAX,out)
        elif pc==0x10001000:
            sp=u.reg_read(UC_X86_REG_ESP)
            self.events.append(list(struct.unpack('<IIII',u.mem_read(sp+4,16))))

    def run(self,key,mods,pending,main,guard,enabled=True,previous=0,skip=0):
        self.events=[];self.ret=0x2a0e0fa if main else 0x10002000
        self.map(self.ret,1)
        self.write(self.INP+0x10d,bytes(256));self.write(self.INP+0x20d,bytes(256))
        self.write(self.INP+0xd10,bytes(136));self.write(self.INP+0xc,bytes([int(enabled)]))
        if key is not None:
            self.write(self.INP+0x10d+self.raw[key],b'\x80')
            self.write(self.INP+0x20d+self.raw[key],bytes([previous]))
            self.write(self.INP+0xd10+key,bytes([skip]))
        for i,k in enumerate((7,10,4)):
            if mods&(1<<i):self.write(self.INP+0x10d+self.raw[k],b'\x80')
        self.write(self.SCREEN+0x1b4,b'\x01');self.write(0x6590638,bytes([pending]))
        self.write(self.WIDGET+0x60,bytes([guard]))
        saved={UC_X86_REG_EBX:0x11111111,UC_X86_REG_ESI:0x22222222,
            UC_X86_REG_EDI:0x33333333,UC_X86_REG_EBP:0x44444444}
        for r,v in saved.items():self.u.reg_write(r,v)
        self.w(self.STACK,self.ret);self.u.reg_write(UC_X86_REG_ESP,self.STACK)
        self.u.reg_write(UC_X86_REG_ECX,self.SCREEN)
        try:self.u.emu_start(0x29ba8f0,0,count=20000)
        except Exception as exc:
            raise AssertionError((key,mods,pending,main,guard,hex(self.u.reg_read(UC_X86_REG_EIP)),hex(self.u.reg_read(UC_X86_REG_ESP)))) from exc
        assert self.u.reg_read(UC_X86_REG_EIP)==self.ret,'publisher did not return'
        assert self.u.reg_read(UC_X86_REG_ESP)==self.STACK+4
        for r,v in saved.items():assert self.u.reg_read(r)==v,'callee-saved register changed'
        assert self.u.mem_read(0x6590638,1)==b'\x00'
        assert self.u.mem_read(self.INP+0xd10,136)==bytes([skip if k==key else 0 for k in range(136)])
        return self.events


def checksum_independent(data,field):
    imagehlp=ctypes.WinDLL('imagehlp')
    imagehlp.CheckSumMappedFile.argtypes=[ctypes.c_void_p,wintypes.DWORD,
        ctypes.POINTER(wintypes.DWORD),ctypes.POINTER(wintypes.DWORD)]
    imagehlp.CheckSumMappedFile.restype=ctypes.c_void_p
    buf=ctypes.create_string_buffer(data);old=wintypes.DWORD();computed=wintypes.DWORD()
    assert imagehlp.CheckSumMappedFile(buf,len(data),ctypes.byref(old),ctypes.byref(computed))
    assert old.value==computed.value==u32(data[field:field+4])


def main():
    old=INPUT.read_bytes();new=CANDIDATE.read_bytes();built,receipt=patch.build(old)
    assert built==new
    opt,sections=patch.pe.headers(old)
    at=patch.pe.offset(sections,0x29ba981,6)
    # Independent jump and scope expectations, not the patch generator's list.
    assert old[at:at+6]==bytes.fromhex('8b0d98376a05')
    assert new[at]==0xe9 and 0x29ba981+5+struct.unpack_from('<i',new,at+1)[0]==0x6ea1200
    allowed=[(at,at+6),(111808512+512,111808512+536),
             (receipt['section_virtual_size_offset'],receipt['section_virtual_size_offset']+4),
             (opt+64,opt+68)]
    pos=0
    for a,b in sorted(allowed):assert old[pos:a]==new[pos:a];pos=b
    assert old[pos:]==new[pos:]
    checksum_independent(new,opt+64)
    a,b=Publisher(old),Publisher(new);runs=0;changes=[]
    for key in range(136):
      for mods in range(8):
       for pending in (0,1):
        for main in (False,True):
         for guard in (0,1):
            before=a.run(key,mods,pending,main,guard)
            after=b.run(key,mods,pending,main,guard);runs+=2
            expected=[]
            if pending:expected=[[0xffffffff,26,16,0]]
            elif key<=108 and key not in (4,7,10) and mods==0:
                if key!=16 or (not main and not guard):expected=[[0xffffffff,26,key,0]]
            assert after==expected,(key,mods,pending,main,guard,before,after,expected)
            if before!=after:
                assert (key,mods,pending,main,guard)==(16,0,0,True,0)
                changes.append(dict(key=key,modifiers=mods,pending=pending,main=main,guard=guard))
    # Held key, disabled input and per-key suppression remain unchanged.
    for option in ({'enabled':False},{'previous':128},{'skip':1}):
        for main in (False,True):
            for pending in (0,1):
                assert a.run(16,0,pending,main,0,**option)==b.run(16,0,pending,main,0,**option)
                runs+=2
    assert len(changes)==1
    try:patch.build(new)
    except ValueError:pass
    else:raise AssertionError('repeated application accepted')
    result=dict(schema='chat-enter-v139-native',passed=True,all_passed=True,
        input_sha256=hashlib.sha256(old).hexdigest(),output_sha256=hashlib.sha256(new).hexdigest(),
        input_size=len(old),output_size=len(new),native_executions=runs,
        completed=['publisher_matrix','abi','scope','imagehlp','input_protection'],missing=[],
        changed_decisions=changes,receive_gate_bytes_identical=True,
        other_callers_identical=True,all_other_keys_identical=True,wm_text_path_identical=True,
        ui_leaves_are_fixtures=True,full_ui_acceptance=False,payload_consumers_executed=False,
        limitations='Native publisher and edge predicate execute; generic update, weak-reference lock and event delegate are ABI fixtures. Live text/IME/UI acceptance remains required.')
    OUT.write_text(json.dumps(result,indent=2),encoding='utf-8');print(json.dumps(result))


if __name__=='__main__':main()
