"""Fixed-v122 native ACK flow, exact patch scope, and receive-gate regression."""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
import collections
import patch_weapon_noop_v122 as patch
try:
 from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE,UC_HOOK_MEM_INVALID
 from unicorn.x86_const import *
except ImportError:
 Uc=None
base=patch.IMAGE_BASE
original=b''
def probe(count, image, source=0, destination=3):
 global b,sections,opt
 import patch_weapon_noop_v122 as patch
 _,opt,_,ss=patch.headers(image);b=image;sections=[(base+s["rva"],s["rawsize"],s["offset"])for s in ss]
 uc=Uc(UC_ARCH_X86,UC_MODE_32);size=struct.unpack_from('<I',b,opt+56)[0];uc.mem_map(base,(size+4095)//4096*4096)
 for va,n,raw in sections:
  if n:uc.mem_write(va,b[raw:raw+n])
 stack,stop,teb,tlsarr,tls,packet,heap=0x10000000,0x20000000,0x30000000,0x30001000,0x50000000,0x40000000,0x60000000
 for p,n in [(0,4096),(stack,0x40000),(stop,4096),(teb,4096),(tlsarr,4096),(tls,0x40000),(packet,0x10000),(heap,0x100000)]:uc.mem_map(p,n)
 uc.mem_write(0x18,struct.pack('<I',teb));uc.mem_write(teb+8,struct.pack('<I',stack));uc.mem_write(0x2c,struct.pack('<I',tlsarr));uc.mem_write(tlsarr,struct.pack('<I',tls))
 u32=lambda p:struct.unpack('<I',uc.mem_read(p,4))[0]
 def ret(value=0,pop=0):
  sp=uc.reg_read(UC_X86_REG_ESP);target=u32(sp);uc.reg_write(UC_X86_REG_EAX,value);uc.reg_write(UC_X86_REG_ESP,sp+4+pop);uc.reg_write(UC_X86_REG_EIP,target)
 recent=collections.deque(maxlen=30);leaves=collections.Counter();move=[];faults=[];steps=0
 inventory,actor=heap+0x1000,heap+0x2000;finish=[];parse_end=[]
 uc.mem_write(0x56a379c,struct.pack('<I',inventory))
 def hook(uc,address,n,ctx):
  nonlocal steps
  steps+=1;recent.append(hex(address));sp=uc.reg_read(UC_X86_REG_ESP)
  if address==0x1fded8a:parse_end.append(dict(esp=sp,flags=uc.reg_read(UC_X86_REG_EFLAGS),edx=uc.reg_read(UC_X86_REG_EDX)))
  if address==0x1fdfca8:finish.append(dict(esp=sp,flags=uc.reg_read(UC_X86_REG_EFLAGS),edx=uc.reg_read(UC_X86_REG_EDX)))
  elif address==0x1fded90 and (source not in (0,1)or destination!=3 or count!=0)and image!=original:finish.append(dict(fallback=True,esp=sp,edx=uc.reg_read(UC_X86_REG_EDX)));uc.emu_stop()
  elif address==0x22efc20:leaves['UI_end_accessor']+=1;ret(heap,4)
  elif address in (0x22ebab0,0x12f3140,0x12f4090):leaves['UI_end_refresh']+=1;ret(heap)
  elif address==0x3e72ce4:leaves['security_cookie']+=1;ret()
  elif address==0x32b94a0:leaves['UI_notice']+=1;ret(0,12)
  elif address in (0x2533000,0x37de240):leaves['debug_string']+=1;ret(heap)
  elif address==0x37dd6c0:leaves['debug_log']+=1;ret()
  elif address==0x25e9b30:leaves['warehouse_mode']+=1;ret(0)
  elif address==0x29610e0:leaves['actor_accessor']+=1;ret(actor)
  elif address==0x2724720:leaves['actor_stats_accessor']+=1;ret(actor)
  elif address==0x32ba120:leaves['UI_dialog_accessor']+=1;ret(0,4)
  elif address==0x3e73cb6:leaves['runtime_cast']+=1;ret(0)
  elif address==0x25f0e80:
   move.append(dict(this=hex(uc.reg_read(UC_X86_REG_ECX)),source_slot=u32(sp+4),worn_slot=u32(sp+8)));uc.emu_stop()
 def bad(uc,access,address,size,value,ctx):faults.append(dict(address=hex(address),pc=hex(uc.reg_read(UC_X86_REG_EIP))));return False
 uc.hook_add(UC_HOOK_CODE,hook);uc.hook_add(UC_HOOK_MEM_INVALID,bad)
 wire=struct.pack('<BhIBh',source,51,count, destination,12);uc.mem_write(packet,wire);uc.mem_write(0x57f9ad0,struct.pack('<II',packet,len(wire)))
 sp=stack+0x30000;uc.mem_write(sp,struct.pack('<4I',stop,0,1,0));uc.reg_write(UC_X86_REG_ESP,sp)
 error=None
 try:uc.emu_start(0x1fdece0,stop,count=1000000,timeout=20000000)
 except Exception as e:error=str(e)
 return dict(stack_after=uc.reg_read(UC_X86_REG_ESP),count=count,source=source,destination=destination,finish=finish,parse_end=parse_end,move_calls=move,error=error,faults=faults,pc=hex(uc.reg_read(UC_X86_REG_EIP)),remaining=u32(0x57f9ad4),leaves=dict(leaves),steps=steps,last=list(recent))

@unittest.skipIf(Uc is None,'install Unicorn for native execution')
class WeaponNoopPatchTest(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  global original
  if not os.environ.get('DNF_WEAPON_INPUT_EXE')or not os.environ.get('DNF_WEAPON_CANDIDATE_EXE'):raise unittest.SkipTest('set both weapon EXE paths')
  cls.before=Path(os.environ['DNF_WEAPON_INPUT_EXE']).read_bytes();cls.after=Path(os.environ['DNF_WEAPON_CANDIDATE_EXE']).read_bytes();original=cls.before
  if patch.sha(cls.before)!=patch.INPUT_SHA256:raise ValueError('wrong baseline')

 def test_zero_ack_skips_wear_but_real_count_retains_native_route(self):
  for source in (0,1):
   for count in (0,1):
    old=probe(count,self.before,source,3);self.assertIsNone(old['error']);self.assertEqual(len(old['move_calls']),1);self.assertEqual(old['remaining'],0)
   new=probe(0,self.after,source,3)
   self.assertIsNone(new['error']);self.assertEqual(new['faults'],[]);self.assertEqual(new['move_calls'],[]);self.assertEqual(new['remaining'],0)
   self.assertEqual(new['pc'],'0x20000000');self.assertEqual(new['stack_after'],0x10030004)
   self.assertEqual(len(new['finish']),1);self.assertEqual(new['finish'][0],new['parse_end'][0])

 def test_other_counts_and_endpoint_families_resume_exact_original_instruction(self):
  for source,destination,count in [(0,3,1),(1,3,1),(0,3,0xffffffff),(7,3,0),(0,17,0),(0,0,0),(3,3,0),(2,3,0),(255,3,0)]:
   with self.subTest(source=source,destination=destination,count=count):
    r=probe(count,self.after,source,destination);self.assertIsNone(r['error']);self.assertEqual(r['remaining'],0);self.assertEqual(r['faults'],[])
    self.assertTrue(r['finish'][0]['fallback']);self.assertEqual(r['finish'][0]['edx'],count);self.assertEqual(r['finish'][0]['esp'],r['parse_end'][0]['esp'])

 def test_only_exact_site_header_and_new_section_change(self):
  result,r=patch.build(self.before);self.assertEqual(result,self.after)
  pe,opt,table,sections=patch.headers(self.after);self.assertEqual(len(sections),20);self.assertEqual(sections[-1]['name'],b'.wearnop')
  ranges=sorted(r['allowed_changed_ranges']);last=0
  for start,end in ranges:self.assertEqual(self.before[last:start],self.after[last:start]);last=end
  self.assertEqual(self.before[last:],self.after[last:len(self.before)]);self.assertEqual(len(self.after)-len(self.before),sections[-1]['rawsize'])
  self.assertEqual(patch.checksum(self.after,opt+64),struct.unpack_from('<I',self.after,opt+64)[0])

 def test_wrong_input_and_existing_output_rejected(self):
  with self.assertRaises(ValueError):patch.build(self.before[:-1]+bytes([self.before[-1]^1]))
  with tempfile.TemporaryDirectory()as directory:
   target=Path(directory)/'DNF.exe';target.write_bytes(b'preserve-existing')
   with self.assertRaises(ValueError):patch.write_candidate(Path(os.environ['DNF_WEAPON_INPUT_EXE']),target)
   self.assertEqual(target.read_bytes(),b'preserve-existing')

 def test_all_65536_receive_gate_decisions_preserved(self):
  vms=[];stack=0x10000000;stop=0x20000000
  for img in (self.before,self.after):
   _,opt,_,sections=patch.headers(img);u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,patch.align(struct.unpack_from('<I',img,opt+56)[0],4096))
   for s in sections:
    if s['rawsize']:u.mem_write(base+s['rva'],img[s['offset']:s['offset']+s['rawsize']])
   u.mem_map(stack,4096);u.mem_map(stop,4096);vms.append(u)
  for opcode in range(65536):
   values=[]
   for u in vms:
    u.mem_write(stack,struct.pack('<II',stop,opcode));u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(0x6e83610,stop,count=40)
    self.assertEqual(u.reg_read(UC_X86_REG_EIP),stop);self.assertEqual(u.reg_read(UC_X86_REG_ESP),stack+8);values.append(u.reg_read(UC_X86_REG_EAX))
   self.assertEqual(values[0],values[1],opcode)

if __name__=='__main__':unittest.main(verbosity=2)
