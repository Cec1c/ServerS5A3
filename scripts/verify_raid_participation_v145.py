"""Execute the real dispatcher and 584 consumer on sender-produced bodies.

The native parser, squad append/removal owner, vector append/copy constructor,
and byte/word/u32 readers execute. Allocation, indexed dungeon lookup, generic
STL reserve/erase/copy leaves and announcement lookup use explicit fixtures.
This checks native model changes, not screen drawing or live multiplayer.
"""
import argparse
import collections
import hashlib
import json
from pathlib import Path
import struct
import sys

sys.path.insert(0,str(Path(__file__).parent/'client'))
import patch_receivers_v128 as pe
from patch_raid_participation_v145 import build, INPUT_SHA256
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE
from unicorn.x86_const import *


def machine(data):
    u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(0x400000,0x7000000)
    for va,size,raw in pe.headers(data)[1]:u.mem_write(va,data[raw:raw+size])
    for at,size in ((0,4096),(0x10000000,0x40000),(0x20000000,0x20000),(0x60000000,0x1000000)):u.mem_map(at,size)
    return u


def admissions(data):
    u=machine(data);results=[]
    for cls in range(3):
        for opcode in range(2001):
            u.mem_write(0x20000000,struct.pack('<BH',cls,opcode))
            u.reg_write(UC_X86_REG_EDI,0x20000000);u.reg_write(UC_X86_REG_ECX,opcode);u.reg_write(UC_X86_REG_ESP,0x10010000)
            u.emu_start(0x2523a0c,0x2523a18,count=1000)
            assert u.reg_read(UC_X86_REG_EIP)==0x2523a18 and u.reg_read(UC_X86_REG_ESP)==0x10010000
            results.append(bool(u.reg_read(UC_X86_REG_EAX)))
    return results


def consume(data,corpus,admit):
    u=machine(data);SP,STOP,MODEL=0x10030000,0x20001000,0x60000000
    allocation=MODEL+0x10000;records={};trace=collections.deque(maxlen=20);seen=set();writes=[]
    def r(at):return struct.unpack('<I',u.mem_read(at,4))[0]
    def w(at,value):u.mem_write(at,struct.pack('<I',value&0xffffffff))
    def alloc(size):
        nonlocal allocation
        at=allocation;allocation+=(size+255)//256*256;return at
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EIP,r(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop)
    def vector(at,size=4096):
        proxy,begin=alloc(8),alloc(size);w(at,proxy);w(proxy,at);w(at+4,begin);w(at+8,begin);w(at+12,begin+size)
    def erase(at,width,sp):
        dest,pos=r(sp+4),r(sp+16);end=r(at+8)
        assert r(at+4)<=pos<end and (pos-r(at+4))%width==0
        if pos+width<end:u.mem_write(pos,bytes(u.mem_read(pos+width,end-pos-width)))
        w(at+8,end-width)
        if width==20:
            for row in range(r(at+4),r(at+8),20):w(r(row),row)
        u.mem_write(dest,struct.pack('<III',r(at),0,pos));ret(dest,16)
    def hook(vm,at,size,unused):
        trace.append(hex(at));seen.add(at);sp=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
        if at==STOP:u.emu_stop()
        elif at==0x8a9480:ret(alloc(r(sp+4)))
        elif at in (0x3e70882,0x3e72ce4):ret()
        elif at==0x1c6d840:w(ecx+8,r(ecx+4));ret()
        elif at==0x37dda80:
            count=r(sp+4);begin=alloc(max(256,count*4));old=r(ecx+8)-r(ecx+4)
            if old:u.mem_write(begin,bytes(u.mem_read(r(ecx+4),old)))
            w(ecx+4,begin);w(ecx+8,begin+old);w(ecx+12,begin+count*4);ret(0,4)
        elif at==0x3d40f20:ret(alloc(r(sp+4)*4),4)
        elif at==0x3c5f720:
            begin,end,dest=r(sp+12),r(sp+24),r(sp+28)
            assert end>=begin and end-begin<0x10000
            if end>begin:u.mem_write(dest,bytes(u.mem_read(begin,end-begin)))
            ret(dest+end-begin,28)
        elif at==0x1a64c20:
            dungeon=r(sp+4)
            if dungeon not in records:
                record=alloc(64);vector(record+8);records[dungeon]=record
            ret(records[dungeon],4)
        elif at in (0x21e3c30,0xe76e60):
            dest=r(sp+4);u.mem_write(dest,bytes(u.mem_read(ecx,12)));w(ecx+8,r(ecx+8)+(4 if at==0x21e3c30 else 20));ret(dest,8)
        elif at==0x32b7020:erase(ecx,4,sp)
        elif at==0x1a5e6e0:erase(ecx,20,sp)
        elif at==0x1a553e0:ret(MODEL+0x6000)
        elif at==0x1a572f0:ret(0,8)
        elif at==0x1f65090:ret(0)
        elif at==0x3e738b1:raise AssertionError('native iterator invariant')
    def write(vm,access,at,size,value,unused):
        if MODEL<=at<MODEL+0x1000000:writes.append(dict(address=hex(at-MODEL),size=size,value=value,instruction=hex(vm.reg_read(UC_X86_REG_EIP))))
    u.hook_add(UC_HOOK_CODE,hook);u.hook_add(UC_HOOK_MEM_WRITE,write);w(0x56a3778,MODEL)
    results=[]
    for scenario in corpus['scenarios']:
        for packet in scenario['packets']:
            if not admit:continue
            body=bytes.fromhex(packet['body_hex']);u.mem_write(0x20002000,body);w(0x57f9ad0,0x20002000);w(0x57f9ad4,len(body))
            for reg in (UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP):u.reg_write(reg,0)
            u.reg_write(UC_X86_REG_ESP,SP);u.reg_write(UC_X86_REG_EFLAGS,0x202);w(SP,STOP)
            try:u.emu_start(0x1ff4940,0,count=200000)
            except Exception as exc:raise AssertionError(dict(scenario=scenario['id'],body=body.hex(),eip=hex(u.reg_read(UC_X86_REG_EIP)),trace=list(trace),error=str(exc))) from exc
            assert u.reg_read(UC_X86_REG_EIP)==STOP and u.reg_read(UC_X86_REG_ESP)==SP+4,(scenario['id'],list(trace))
            assert r(0x57f9ad4)==0,'native reader left bytes'
        record=records.get(corpus['dungeon']);squads=[]
        if record:
            vec=record+8
            for row in range(r(vec+4),r(vec+8),20):squads.append([r(pos) for pos in range(r(row+4),r(row+8),4)])
        assert squads==(scenario['expected_squads'] if admit else []),(scenario['id'],squads)
        results.append(dict(scenario=scenario['id'],squads=squads,packets=len(scenario['packets']) if admit else 0))
    if admit:assert {0x1ff4940,0x1a64e80,0x1a64f10,0x11059c0,0x45e350,0x37e82e0,0x37e8350,0x37e83c0}<=seen
    return dict(passed=True,module_sha256=hashlib.sha256(data).hexdigest(),results=results,native_model_writes=writes,native_executed=['registered 584 parser','native byte/word/u32 readers','squad append and removal owners','native vector append and copy constructor'],fixtures=['allocation and vector reserve/erase/copy leaves','indexed dungeon record lookup','temporary vector clear','announcement lookup absent'],rendered=False)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('input','candidate','corpus','fiendwar-corpus','receipt'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();assert not a.receipt.exists()
    before=a.input.read_bytes();after=a.candidate.read_bytes();assert hashlib.sha256(before).hexdigest()==INPUT_SHA256 and build(before)[0]==after
    old,new=admissions(before),admissions(after)
    changed=[dict(packet_class=i//2001,opcode=i%2001,before=old[i],after=new[i]) for i in range(len(old)) if old[i]!=new[i]]
    assert changed==[dict(packet_class=0,opcode=584,before=False,after=True)],changed
    from verify_raid_board_v144 import probe
    fiendwar=probe(after,json.loads(a.fiendwar_corpus.read_bytes()),True)
    corpus=json.loads(a.corpus.read_bytes());receipt=dict(passed=True,admission_cases_per_image=len(old),changed_admissions=changed,source=consume(before,corpus,False),candidate=consume(after,corpus,True),corpus_sha256=hashlib.sha256(a.corpus.read_bytes()).hexdigest(),fiendwar=fiendwar,fiendwar_corpus_sha256=hashlib.sha256(a.fiendwar_corpus.read_bytes()).hexdigest(),full_ui_acceptance=False)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(dict(passed=True,admission_cases_per_image=len(old),changed_admissions=changed,candidate_scenarios=receipt['candidate']['results'])))


if __name__=='__main__':main()
