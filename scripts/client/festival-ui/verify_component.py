"""Execute the compiled popup lifecycle and wire parser in Unicorn, never a game.

Native window ownership, controls and network sinks are explicit adapters.
No player resources, live process or connection is used.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_ESP, UC_X86_REG_EIP


class Machine:
    STACK=0x10010000
    SCRATCH=0x20000000
    STOP=0x30000000
    def __init__(self,path,map_path):
        data=path.read_bytes();pe=pefile.PE(data=data)
        assert not getattr(pe,'DIRECTORY_ENTRY_IMPORT',None)
        self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.pages=set();self.mocks={};self.trace=[]
        self.put(pe.OPTIONAL_HEADER.ImageBase,pe.get_memory_mapped_image())
        self.put(self.STACK-0x10000,b'\0'*0x20000)
        self.put(self.SCRATCH,b'\0'*0x400000)
        self.put(self.STOP,b'\x90')
        self.exports={s.name.decode():pe.OPTIONAL_HEADER.ImageBase+s.address for s in pe.DIRECTORY_ENTRY_EXPORT.symbols}
        self.f={};self.g={}
        for line in map_path.read_text().splitlines():
            m=re.search(r'\?([^@]+)@festivalui@@\S+\s+([0-9a-fA-F]{8})\s',line)
            if m:self.f[m[1]]=int(m[2],16)
            m=re.search(r'\?([^@]+)@@\S+\s+([0-9a-fA-F]{8})\s',line)
            if m:self.g[m[1]]=int(m[2],16)
        self.u.hook_add(UC_HOOK_CODE,self.hook)
    def put(self,address,data):
        for page in range(address&~4095,(address+len(data)+4095)&~4095,4096):
            if page not in self.pages:self.u.mem_map(page,4096);self.pages.add(page)
        self.u.mem_write(address,data)
    def write(self,address,value):self.put(address,struct.pack('<I',value))
    def read(self,address):return struct.unpack('<I',self.u.mem_read(address,4))[0]
    def arg(self,index=0):return self.read(self.u.reg_read(UC_X86_REG_ESP)+4+index*4)
    def mock(self,address,value=0,pop=0):
        self.put(address,b'\x90');self.mocks[address]=(value,pop)
    def hook(self,u,address,size,_):
        self.trace=(self.trace+[hex(address)])[-12:]
        if address==self.STOP:u.emu_stop();return
        if address not in self.mocks:return
        value,pop=self.mocks[address];result=value() if callable(value) else value
        sp=u.reg_read(UC_X86_REG_ESP);target=self.read(sp)
        u.reg_write(UC_X86_REG_EAX,(result or 0)&0xffffffff)
        u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
    def call(self,address,args=(),pop=0):
        self.put(self.STACK,struct.pack('<'+'I'*(1+len(args)),self.STOP,*args))
        self.u.reg_write(UC_X86_REG_ESP,self.STACK)
        self.u.reg_write(UC_X86_REG_ECX,0);self.u.reg_write(UC_X86_REG_EDX,0)
        try:self.u.emu_start(address,self.STOP+1,count=500000)
        except Exception as error:
            at=self.u.reg_read(UC_X86_REG_EIP)
            raise RuntimeError(f"native emulation stopped at {at:#x}: {bytes(self.u.mem_read(at,16)).hex()}; trace={self.trace}") from error
        assert self.u.reg_read(UC_X86_REG_EIP)==self.STOP,'execution did not return'
        assert self.u.reg_read(UC_X86_REG_ESP)==self.STACK+4+pop,'stack imbalance'
        return self.u.reg_read(UC_X86_REG_EAX)


def verify(payload,map_path):
    m=Machine(payload,map_path);f=m.f;S=m.SCRATCH
    for name in ('ui','contracts','readable'):m.mock(m.g[name],1)
    for name in ('fit','render','query'):m.mock(f[name],1 if name=='fit' else 0)
    manager,row=S+0x10000,S+0x40000
    m.write(0x56a3798,manager)
    windows={};active=set();cached=set();created=[]
    def owned():
        ident=m.read(f['id'])
        return int(m.read(f['win'])==windows.get(ident) and ident in (active if m.arg() else cached))
    def get_window():
        ident=m.arg()
        if ident not in windows:
            windows[ident]=S+0x60000+len(windows)*0x2000;created.append(ident);m.write(f['win'],windows[ident])
        active.add(ident);cached.discard(ident);return windows[ident]
    def close_window():
        ident=m.arg();assert ident in active;active.remove(ident);cached.add(ident)
    m.mock(f['owns'],owned);m.mock(f['freeSlot'],lambda:int(m.arg() not in windows))
    m.mock(0x32bea50,get_window,12);m.mock(0x32b94a0,close_window,12)
    m.write(row+0x140,312)
    for n in range(60):
        m.write(row,60001+n%6)
        assert m.call(m.exports['CalendarClick'],(row,))==1068
        assert created==[1068] and m.read(f['tab'])==n%6
        if n%5==4:
            sender=S+0x7f000;m.write(windows[1068]+0x50,sender)
            assert m.call(f['event'],(sender,13,0,0),16)==1
            assert 1068 in cached and 1068 not in active
    # The exact calendar rebuild call returns the native no-popup sentinel.
    assert m.call(m.exports['CalendarFromCalendar'],(row,0,0x17c4423))==0x4d8
    m.write(row,59999);assert m.call(m.exports['CalendarClick'],(row,))==0
    # Use a fresh machine for real query execution: no previously translated
    # mock instruction may remain in the emulator's code cache.
    m=Machine(payload,map_path);f=m.f
    for name in ('ui','contracts','readable'):m.mock(m.g[name],1)
    m.mock(f['render'])
    net=S+0x100000;m.put(net,b'\0'*0x2bd06c);m.put(net+8,b'\1')
    m.mock(0x374ea10,net)
    clock=S+0x3f0000;m.write(0x6e85a24,clock);m.mock(clock,10000)
    op=[];words=[]
    m.mock(0x374f280,lambda:op.append(m.arg()) or 1,4)
    m.mock(0x374f3f0,lambda:words.append(m.arg()),4);m.mock(0x3750ac0,1)
    m.call(f['query']);assert op==[0x7f02]
    assert struct.pack('<4I',*words)==b'FEQ1\1\0\0\0'+struct.pack('<II',1,0)
    packet=S+0x300000
    def consume():
        assert m.arg(1)==1568;m.put(m.arg(),bytes(m.u.mem_read(packet,1568)));m.write(0x57f9ad4,0)
    m.mock(0x37e8430,consume)
    def response(sequence=1,bad_period=False):
        b=bytearray(1568);b[:4]=b'FES1';struct.pack_into('<HHHHH',b,4,1,1568,0,19,6)
        struct.pack_into('<QI',b,16,4,sequence)
        for i,pid in enumerate((201601,201605,201607,201701,201801,201807)):
            at=32+i*256;struct.pack_into('<I',b,at,pid if not bad_period else 0);struct.pack_into('<I',b,at+24,1)
            struct.pack_into('<HBBI',b,at+64,2,2,1,490700585)
        return b
    def receive(b):
        m.put(packet,bytes(b));m.write(0x57f9ad0,packet);m.write(0x57f9ad4,len(b))
        m.call(m.exports['ReceiveFestival']);m.call(f['apply'])
    good=response();receive(good)
    assert m.u.mem_read(f['ready'],1)==b'\1' and bytes(m.u.mem_read(f['data'],1568))==good
    m.write(f['pending'],2);receive(good);assert m.read(f['pending'])==2
    receive(response(2,True));assert not m.read(f['pending']) and m.u.mem_read(f['ready'],1)==b'\0'
    return dict(passed=True,payloadSHA256=hashlib.sha256(payload.read_bytes()).hexdigest(),repeatedClicks=60,closeReopens=12,allocatedWindows=created,automaticOpenSuppressed=True,queryBytesVerified=True,responseAndStaleNonceVerified=True,invalidPeriodRejected=True,stackBalanced=True,adapters=['UI thread/readability/native contract','native window manager and controls','network sink'],gameExecuted=False,renderingAccepted=False)


if __name__=='__main__':
    p=argparse.ArgumentParser()
    for name in ('payload','map','output'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();r=verify(a.payload,a.map)
    with a.output.open('x',encoding='utf8') as out:json.dump(r,out,indent=2)
    print(json.dumps(r))
