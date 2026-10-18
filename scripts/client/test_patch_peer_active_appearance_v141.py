"""Execute exact parent/candidate mode1 final call and native wrapper ABI."""
import argparse,ctypes,hashlib,itertools,json,struct
from pathlib import Path
import patch_peer_active_appearance_v141 as patch
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *

REGS=(UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,UC_X86_REG_EFLAGS)
START=patch.PATCH_VA-1;STOP=patch.PATCH_VA+8
MEM=0x7c000000;DST=MEM+0x1000;SRC=MEM+0x2000;LOCAL=MEM+0x3000;MANAGER=MEM+0x4000
STAT=MEM+0x5000;LOCALSTAT=MEM+0x6000;STACK=MEM+0xa000
COPY=((0x50,112),(0x158,56),(0x1dc,56),(0x26d,56))

class Runner:
 def __init__(self,data,receipt):
  self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.state={};self.calls=[];self.self_finalized=False
  _,sections=patch.pe.headers(data);pages=set()
  def mapcode(address,size=1):
   for page in range(address&~4095,(address+size+4095)&~4095,4096):
    if page in pages:continue
    self.u.mem_map(page,4096);off=patch.pe.offset(sections,page,4096);self.u.mem_write(page,data[off:off+4096]);pages.add(page)
  for address,size in ((START,20),(int(receipt['helper_va'],16),receipt['helper_size']),(patch.OLD_TARGET,23),
    (0x296dd80,0xaa),(0x296d730,1),(0x1a5a5e0,1),(0x29610e0,1),(0x22c30a0,1),(0x568a519,2),(0x56a3758,4)):
   mapcode(address,size)
  self.u.mem_map(MEM,0x10000)
  self.u.hook_add(UC_HOOK_CODE,self.hook)
 def dw(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]
 def ret(self,value=0):
  sp=self.u.reg_read(UC_X86_REG_ESP);self.u.reg_write(UC_X86_REG_EIP,self.dw(sp));self.u.reg_write(UC_X86_REG_ESP,sp+4);self.u.reg_write(UC_X86_REG_EAX,value)
 def hook(self,u,pc,size,_):
  sp=u.reg_read(UC_X86_REG_ESP);s=self.state
  if pc==0x296d730:
   assert self.dw(sp+4)==s['key'];self.calls.append('active_lookup');self.ret(DST if s['active'] else 0)
  elif pc==0x1a5a5e0:
   args=[self.dw(sp+x) for x in (4,8,12)];assert args==[s['key'],0,s['route']]
   self.calls.append('aux_lookup');self.ret((DST if s['same'] else SRC) if s['found'] else 0)
  elif pc==0x29610e0:self.ret(STAT if s['local_stat'] else LOCALSTAT)
  elif pc==0x22c30a0:
   assert self.dw(sp+4)==STAT;self.self_finalized=True;self.ret(0x24681357)
  elif pc==STOP:u.emu_stop()
 def run(self,**overrides):
  s=dict(owner=0,route=0,key=6,manager=True,local_actor=False,local_stat=False,stat=True,actor=True,active=True,found=True,matching=True,same=False,flags=0x602)
  s.update(overrides);self.state=s;self.calls=[];self.self_finalized=False
  src=bytearray((i*37+19)&255 for i in range(0x700));dst=bytearray((i*13+3)&255 for i in range(0x700))
  struct.pack_into('<H',src,0x20,s['key'] if s['matching'] else (s['key']+1)&65535)
  struct.pack_into('<H',dst,0x20,s['key'])
  # Realistic no-appearance state with mode1's additional equipment slots.
  for slot in range(14):struct.pack_into('<I',dst,0x50+slot*8,0xffffffff)
  self.u.mem_write(SRC,bytes(src));self.u.mem_write(DST,bytes(dst));self.u.mem_write(LOCAL,b'\x71'*0x700)
  self.u.mem_write(STAT,b'\x31'*0x700);self.u.mem_write(LOCALSTAT,b'\x77'*0x700);self.u.mem_write(MEM+0x7000,b'\x97'*0x700)
  self.u.mem_write(MANAGER,b'\0'*0x100)
  if s['manager'] and s['manager']!='no_local':self.u.mem_write(MANAGER+0x70,struct.pack('<I',DST if s['local_actor'] else LOCAL))
  self.u.mem_write(0x56a3758,struct.pack('<I',MANAGER if s['manager'] else 0))
  self.u.mem_write(0x568a519,bytes([s['owner'],s['route']]))
  regs=(0x13572468,0x23456789,0x34567890,0x45678901,STAT if s['stat'] else 0,DST if s['actor'] else 0,0x56789abc,STACK,s['flags'])
  for reg,value in zip(REGS,regs):self.u.reg_write(reg,value)
  self.u.emu_start(START,STOP,count=4000)
  assert self.u.reg_read(UC_X86_REG_EIP)==STOP
  return dict(registers=[self.u.reg_read(x) for x in REGS],destination=bytes(self.u.mem_read(DST,len(dst))),
   source=bytes(self.u.mem_read(SRC,len(src))),local=bytes(self.u.mem_read(LOCAL,0x700)),self_finalized=self.self_finalized,
   stats=bytes(self.u.mem_read(STAT,0x700)),local_stats=bytes(self.u.mem_read(LOCALSTAT,0x700)),party=bytes(self.u.mem_read(MEM+0x7000,0x700)),
   calls=list(self.calls),original_destination=bytes(dst),original_source=bytes(src),state=s)

def validate(parent,candidate,receipt):
 expected,r=patch.build(parent);assert candidate==expected and r==receipt
 allowed=set(range(r['patch_offset'],r['patch_offset']+5))
 for offset,size in ((r['helper_file_offset'],r['helper_size']),(r['section_virtual_size_offset'],4),(r['checksum_offset'],4)):
  allowed.update(range(offset,offset+size))
 assert all(a==b or i in allowed for i,(a,b) in enumerate(zip(parent,candidate)))
 try:patch.build(candidate);raise AssertionError('second patch accepted')
 except ValueError:pass
 before=Runner(parent,r);after=Runner(candidate,r);cases=changed=0
 def check(**options):
  nonlocal cases,changed
  old=before.run(**options);new=after.run(**options);s=new['state']
  should=s['owner']==0 and s['manager'] is True and not s['local_actor'] and s['stat'] and s['actor'] and s['key'] not in (0,65535) and s['active'] and s['found'] and s['matching'] and not s['same']
  want=bytearray(old['destination'])
  if should:
   for offset,size in COPY:want[offset:offset+size]=new['source'][offset:offset+size]
   changed+=1
  assert new['destination']==bytes(want),(options,'destination bounds')
  assert new['source']==new['original_source'] and new['local']==old['local']==b'\x71'*0x700
  assert new['stats']==old['stats']==b'\x31'*0x700 and new['local_stats']==old['local_stats']==b'\x77'*0x700 and new['party']==old['party']==b'\x97'*0x700
  assert new['registers']==old['registers'] and new['self_finalized']==old['self_finalized'],(options,'original ABI/finalizer')
  cases+=1
 for owner,route in itertools.product(range(256),range(256)):check(owner=owner,route=route)
 for manager,local_actor,stat,actor,active,found,matching,same,key in itertools.product((False,True,'no_local'),(False,True),(False,True),(False,True),(False,True),(False,True),(False,True),(False,True),(0,6,65535)):
  check(manager=manager,local_actor=local_actor,local_stat=local_actor,stat=stat,actor=actor,active=active,found=found,matching=matching,same=same,key=key)
 # Native copied resource fields are ordinary DWORDs in the original descriptor
 # assignment (12fbb6b..12fbb7f), not owning pointer/refcount operations.
 for flags in (0x202,0x246,0x602,0x646):
  for key in (1,5,6,47,100,65534):check(flags=flags,key=key)
 imagehlp=ctypes.WinDLL('imagehlp');fn=imagehlp.CheckSumMappedFile
 fn.argtypes=[ctypes.c_void_p,ctypes.c_uint32,ctypes.POINTER(ctypes.c_uint32),ctypes.POINTER(ctypes.c_uint32)];fn.restype=ctypes.c_void_p
 buf=ctypes.create_string_buffer(candidate);stored=ctypes.c_uint32();computed=ctypes.c_uint32()
 assert fn(buf,len(candidate),ctypes.byref(stored),ctypes.byref(computed)) and stored.value==computed.value
 return dict(passed=True,native_executions=cases*2,parent_candidate_comparisons=cases,changed_cases=changed,
  all_u8_packet_context_pairs=True,original_self_finalizer_preserved=True,local_actor_unchanged=True,source_actor_unchanged=True,
  other_19_slots_and_identity_unchanged=True,stats_and_party_untouched=True,registers_flags_stack_preserved=True,
  exact_delta_only=True,windows_checksum_valid=True,input_sha256=patch.INPUT_SHA256,output_sha256=r['output_sha256'],
  server_changes=False,resources_changed=False,full_ui_acceptance=False,
  limitations=['Native caller, candidate helper, retained-local fast getter and original self-only wrapper execute.',
  'Active/aux map lookup, local stat getter and self finalizer are explicit fixtures. No world, NPK, portrait or renderer execution. Requires gameplay acceptance.'])

def main():
 p=argparse.ArgumentParser();p.add_argument('parent',type=Path);p.add_argument('candidate',type=Path);p.add_argument('--receipt',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
 result=validate(a.parent.read_bytes(),a.candidate.read_bytes(),json.loads(a.receipt.read_text()))
 a.output.write_text(json.dumps(result,indent=2),encoding='utf-8');print(json.dumps(result),flush=True)
if __name__=='__main__':main()
