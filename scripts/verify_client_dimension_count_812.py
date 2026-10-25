"""Offline v141 gate, actual 812 readers/setter, and UI argument execution.

Requires pefile and Unicorn. Singleton/local-actor lookup and event bus are
fixtures. Actual receiver, packet readers, scalar setters/getters, and UI
arithmetic run as native code. Rendering and full network dispatch do not run.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'deploy'/'client'))
from dimension_count_gate import build, SOURCE_SHA256, GATE_VA, BITMAP_VA

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--exe',type=Path,required=True)
p.add_argument('--candidate',type=Path,required=True)
p.add_argument('--receipt',type=Path,required=True)
a=p.parse_args()
before=a.exe.read_bytes();after=a.candidate.read_bytes()
expected,patch=build(before)
assert after==expected
pe=pefile.PE(data=after,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase
u=Uc(UC_ARCH_X86,UC_MODE_32)
u.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
u.mem_write(base,after[:pe.OPTIONAL_HEADER.SizeOfHeaders])
for s in pe.sections:u.mem_write(base+s.VirtualAddress,s.get_data())
u.mem_map(0x10000000,0x20000);u.mem_map(0x20000000,0x600000)
SP=0x10010000;STOP=0x10001000;MGR=0x20001000;CFG=0x20002000
ACTOR=0x20003000;BUS=0x20004000;VT=0x20005000;BUFFER=0x20006000
APP=0x20010000;ROOT=0x20007000
state={}
def w(addr,value):u.mem_write(addr,struct.pack('<I',value&0xffffffff))
def r(addr):return struct.unpack('<I',u.mem_read(addr,4))[0]
def ret(value=0,clean=0):
 sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
 u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+clean);u.reg_write(UC_X86_REG_EIP,target)
def hook(vm,addr,size,user):
 if addr==STOP:u.emu_stop();return
 if state['mode']=='gate':return
 if addr==0x1fa2790:ret(ROOT);return
 if addr==0x13023f0:ret(APP);return
 if addr==0x296dd80:ret(ACTOR if state['actor'] else 0);return
 if addr in (STOP+0x100,STOP+0x200,STOP+0x300):
  sp=u.reg_read(UC_X86_REG_ESP)
  count=2 if addr==STOP+0x100 else 1
  state['events'].append([hex(addr),*[r(sp+4+4*i) for i in range(count)]])
  ret(clean=4*count);return
 if addr==0x3296510:state['refresh']+=1;ret();return
 if addr==0x91a120:ret(0);return
 if addr==0x39a6620:
  sp=u.reg_read(UC_X86_REG_ESP)
  state['format']=[r(sp+4),r(sp+8),r(sp+12)];u.emu_stop();return
u.hook_add(UC_HOOK_CODE,hook)
def execute(start,mode,actor=True):
 state.clear();state.update(mode=mode,actor=actor,events=[],refresh=0,format=None)
 for reg,val in ((UC_X86_REG_EAX,0),(UC_X86_REG_ECX,MGR),(UC_X86_REG_EDX,0),
   (UC_X86_REG_EBX,0),(UC_X86_REG_ESI,0),(UC_X86_REG_EDI,MGR),
   (UC_X86_REG_EBP,SP+0x800),(UC_X86_REG_ESP,SP),(UC_X86_REG_EFLAGS,0x202)):
  u.reg_write(reg,val)
 w(SP,STOP);u.emu_start(start,0,count=10000)
 assert u.reg_read(UC_X86_REG_EIP) in (STOP,0x39a6620),hex(u.reg_read(UC_X86_REG_EIP))

w(APP+0x57eb4c,MGR);w(MGR+0x28,CFG)
w(CFG,5);w(CFG+4,10);w(CFG+8,2)
w(0x56a3758,ROOT);w(0x56a3790,BUS);w(BUS,VT)
for offset,target in ((8,STOP+0x100),(0x28,STOP+0x200),(0x24,STOP+0x300)):w(VT+offset,target)
u.mem_write(ACTOR+0x20,struct.pack('<H',123))

# Execute the real gate for every u16 opcode before/after the one-bit change.
gate=[];byte=BITMAP_VA+812//8
for op in range(65536):
 vals=[]
 for value in (2,18):
  u.mem_write(byte,bytes([value]));w(SP+4,op);execute(GATE_VA,'gate')
  assert u.reg_read(UC_X86_REG_ESP)==SP+8
  vals.append(u.reg_read(UC_X86_REG_EAX))
 if vals[0]!=vals[1]:gate.append(op)
assert gate==[812]
u.mem_write(byte,b'\x12')
cases=[]
for used,extra,weekly in ((0,0,0),(1,1,1),(5,5,2),(5,0,255)):
 for actor in (False,True):
  body=struct.pack('<IIB',used,extra,weekly)
  u.mem_write(BUFFER,body);w(0x57f9ad0,BUFFER);w(0x57f9ad4,len(body))
  for off in (0x18,0x1c,0x20):w(MGR+off,0xdeadbeef)
  w(ACTOR+0x548,0xdeadbeef)
  execute(0x1049e00,'receiver',actor)
  assert [r(MGR+off) for off in (0x18,0x1c,0x20)]==[used,extra,weekly]
  assert r(0x57f9ad0)==BUFFER+9 and r(0x57f9ad4)==0
  assert r(ACTOR+0x548)==(extra if actor else 0xdeadbeef)
  assert state['refresh']==int(actor)
  events=list(state['events'])
  if actor:assert [e[1:] for e in events]==[[0x98,1],[123],[extra]]
  else:assert events==[]
  displays=[]
  for start,want in ((0x10f619c,[48010,5-used,5]),
    (0x10f62a2,[67064,5,extra]),(0x10f636b,[67057,min(weekly,2),2])):
   execute(start,'ui');assert state['format']==want,(hex(start),state['format'],want)
   displays.append(dict(start_rva=hex(start-base),dstr=want[0],arguments=want[1:]))
  cases.append(dict(body_hex=body.hex(),actor_present=actor,scalar_values=[used,extra,weekly],
    actor_extra_value=r(ACTOR+0x548),events=events,display_arguments=displays))

# Direct registered callback identity and exact-image contract anchors.
def raw(va,size):return before[pe.get_offset_from_rva(va-base):pe.get_offset_from_rva(va-base)+size]
assert raw(0x104c7cb,11)==bytes.fromhex('5368009e0401682c030000')
assert raw(0x370e1a0,7)==bytes.fromhex('8b814ceb5700c3')
receipt=dict(passed=True,source_sha256=SOURCE_SHA256,candidate_sha256=hashlib.sha256(after).hexdigest(),
 gate_native_u16_cases=65536,changed_admissions=gate,receiver_cases=cases,
 callback_rva=hex(0x1049e00-base),registration_rva=hex(0x104c7cb-base),
 layout='little-endian u32 base-used, u32 available-extra, u8 weekly-hidden-appearances',
 manager_offsets=['0x18','0x1c','0x20'],actor_extra_offset='0x548',
 fixtures=['singleton lookup','local actor lookup','world event virtual methods',
   'event refresh leaf','offline-mode predicate','loaded config scalars 5/10/2',
   'UI execution stops at DSTR lookup arguments'],
 actual_native=['receive gate','812 callback','u32 and byte packet readers',
   'manager lookup getter','scalar setter/getters','UI subtraction and weekly clamp'],
 full_dispatch=False,rendered_ui=False,native_gameplay_acceptance=False,published=False)
with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
print(json.dumps(dict(passed=True,gate_cases=65536,receiver_cases=len(cases),changed_admissions=gate,
 candidate_sha256=receipt['candidate_sha256'])))
