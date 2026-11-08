"""Bounded x86 query tests; memory/socket APIs are explicit offline fixtures."""
import argparse
import hashlib
import json
import struct
from pathlib import Path
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import build_candidate as b

ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--exe',required=True,type=Path)
a=ap.parse_args();original=a.exe.read_bytes();data,receipt=b.build(original)
pe=pefile.PE(data=data,fast_load=True)
image=pe.get_memory_mapped_image()

def trial(states=None, missing=None, roster_bad=False, leader_bad=False, self_bad=False, body_bad=False, busy=False, unreadable=False, fallback=False):
    u=Uc(UC_ARCH_X86,UC_MODE_32)
    u.mem_map(0x400000,(len(image)+4095)&~4095);u.mem_write(0x400000,image)
    u.mem_map(0x10000000,0x1000000);u.mem_map(0x20000000,0x100000)
    def put(at,*v):u.mem_write(at,struct.pack('<'+'I'*len(v),*v))
    def rd(at):return int.from_bytes(u.mem_read(at,4),'little')
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EIP,rd(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EAX,value)
    network,party,record,transport,selfmgr,actor=0x10000000,0x10300000,0x10310000,0x10400000,0x10800000,0x10810000
    put(0x56a3790,network);put(0x56a3768,party);put(0x57ed8d4,transport);put(0x56a3758,selfmgr)
    put(selfmgr+0x70,actor);put(actor+0x4c8,record)
    ids=[111,222,333,444,0,0,0,0]
    for i in range(8):
        put(record+0x2c+4*i,ids[i] if ids[i] else 0xffffffff)
        put(party+0x48+4*i,actor if i==0 else (0x10900000+0x1000*i if ids[i] else 0))
    if self_bad:put(party+0x48,0)
    put(record+0x4c,1 if leader_bad else 0);put(party+0x68,0)
    if roster_bad:put(record+0x30,555)
    put(transport+8,1);put(transport+0x2bcc34,1 if busy else 0)
    put(0x66d27e8,*[i+100 if ids[i] and i!=missing else 0xffffffff for i in range(8)])
    put(network+0x20a07c,*((states or [0]*8)))
    query=bytearray(b'PCQ1'+struct.pack('<Q',12345)+struct.pack('<8H',*ids)+bytes(4))
    if body_bad:query[0]=0
    frame=0x10f00000;body=frame+0x1000
    header=bytearray(16);header[0]=1;struct.pack_into('<H',header,1,0x7f10 if fallback else b.OPCODE);struct.pack_into('<I',header,3,48)
    u.mem_write(frame,bytes(header));u.mem_write(body,bytes(query));put(0x57f9ad0,body);put(0x57f9ad4,32)
    stop=0x20000000;sp=0x200f0000;put(sp,stop,frame)
    saved={UC_X86_REG_EBP:0x200e0000,UC_X86_REG_EBX:0x11223344,UC_X86_REG_ESI:0x55667788,UC_X86_REG_EDI:0x12345678}
    for reg,val in saved.items():u.reg_write(reg,val)
    u.reg_write(UC_X86_REG_ESP,sp)
    sent=[];ops=[];old=[]
    def hook(_u,at,size,_):
        esp=u.reg_read(UC_X86_REG_ESP)
        if at==0x6f189d0:
            ptr,n=rd(esp+4),rd(esp+8)
            ok=0x10000000<=ptr and ptr+n<=0x11000000 and not (unreadable and ptr==network)
            ret(0x12340000 | int(ok)) # bool only in AL; hostile high EAX bits
        elif at==0x374f280:
            assert u.reg_read(UC_X86_REG_ECX)==transport
            ops.append(rd(esp+4));ret(0,4)
        elif at==0x374eeb0:
            assert u.reg_read(UC_X86_REG_ECX)==transport
            sent.append(bytes(u.mem_read(rd(esp+4),rd(esp+8))));ret(0,8)
        elif at==0x3750ac0:ret(0)
        elif at==b.OLD_DISPATCH:old.append(rd(esp+4));ret(0)
    u.hook_add(UC_HOOK_CODE,hook)
    u.emu_start(receipt['dispatch'],stop,count=10000)
    assert u.reg_read(UC_X86_REG_EIP)==stop
    assert u.reg_read(UC_X86_REG_ESP)==sp+4
    assert all(u.reg_read(reg)==val for reg,val in saved.items())
    if sent:
        assert ops==[b.OPCODE] and len(sent)==1 and len(sent[0])==40
        assert sent[0][:4]==b'PCR1' and sent[0][4:32]==query[4:]
    return list(sent[0][32:]) if sent else [],old

cases={}
def check(name,expected,**kw):
    got,_=trial(**kw);assert got==expected,(name,got,expected);cases[name]=got
check('four_ready',[0]*8)
for state in [1,2,3,4,256]:
    values=[0]*8;values[2]=state;expected=[0]*8;expected[2]=state if state<=3 else 255
    check('peer_state_'+str(state),expected,states=values)
check('missing_socket',[0,255,0,0,0,0,0,0],missing=1)
check('roster_mismatch',[254]*8,roster_bad=True)
check('leader_mismatch',[254]*8,leader_bad=True)
check('self_slot_mismatch',[254]*8,self_bad=True)
check('bad_body',[],body_bad=True)
check('busy_sender',[],busy=True)
check('unreadable_udp_manager',[254]*8,unreadable=True)
sent,old=trial(fallback=True);assert not sent and len(old)==1;cases['old_dispatch_preserved']=True

def gate_trial(classification,opcode):
    u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(0x400000,(len(image)+4095)&~4095);u.mem_write(0x400000,image)
    u.mem_map(0x10000000,0x10000);u.mem_map(0x20000000,0x10000)
    frame=0x10000000;sp=0x20008000;stop=0x20000000
    u.mem_write(frame,bytes([classification]));u.mem_write(sp,struct.pack('<II',stop,opcode))
    u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_EDI,frame)
    old=[]
    def hook(_u,at,size,_):
        if at==b.OLD_GATE:
            esp=u.reg_read(UC_X86_REG_ESP);old.append(opcode);u.reg_write(UC_X86_REG_EIP,int.from_bytes(u.mem_read(esp,4),'little'));u.reg_write(UC_X86_REG_ESP,esp+8);u.reg_write(UC_X86_REG_EAX,77)
    u.hook_add(UC_HOOK_CODE,hook);u.emu_start(receipt['gate'],stop,count=100)
    assert u.reg_read(UC_X86_REG_ESP)==sp+8
    return u.reg_read(UC_X86_REG_EAX),old
assert gate_trial(1,b.OPCODE)==(1,[])
for classification,opcode in [(0,b.OPCODE),(2,b.OPCODE),(1,121),(1,0x7f10),(1,28)]:
    assert gate_trial(classification,opcode)==(77,[opcode])
cases['receive_gate_exact_pair_and_old_chain']=True
print(json.dumps(dict(build=receipt,cases=cases,scope='Actual patched dispatcher, original local-slot/record getters and memory reads; readability and packet-builder/socket APIs are fixtures. No game UI or online acceptance.'),indent=2))
