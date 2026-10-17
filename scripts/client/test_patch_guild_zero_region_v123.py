"""Execute the existing founder-auth enumeration and selected-region gates."""
import os
from pathlib import Path
import struct
import tempfile
import unittest
import patch_guild_zero_region_v123 as patch
try:
 from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
 from unicorn.x86_const import *
except ImportError:Uc=None

def vm(image):
 _,opt,_,sections=patch.headers(image);u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(patch.IMAGE_BASE,patch.align(struct.unpack_from('<I',image,opt+56)[0],4096))
 for s in sections:
  if s['rawsize']:u.mem_write(patch.IMAGE_BASE+s['rva'],image[s['offset']:s['offset']+s['rawsize']])
 u.mem_map(0x10000000,0x10000);u.mem_map(0x20000000,4096);u.mem_map(0x11000000,0x4000);return u

def flow(image,kind,available,selected=0):
 u=vm(image);u32=lambda p:struct.unpack('<I',u.mem_read(p,4))[0]
 def ret(value,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);target=u32(sp);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
 rows=[];lookups=[];done=[];writer=[]
 u.mem_write(0x11000294,struct.pack('<I',0x11001000));u.mem_write(0x11002000,'Founder\0'.encode('utf-16le'));u.mem_write(0x11002100,b'Founder\0')
 def hook(u,p,n,c):
  if p==0x1b4a020:ret(0x5678e38)
  elif p==0x1b3bbc0:
   v=u32(u.reg_read(UC_X86_REG_ESP)+4);lookups.append(v);ret(int(v in available),4)
  elif p==0x3437be4:
   # The native eligibility gates have all completed. Isolate the UI row-
   # construction/selection block; no eligibility branch is hooked.
   rows.append(u.reg_read(UC_X86_REG_EDI));u.reg_write(UC_X86_REG_EIP,0x3437c61)
  elif p==0x374ea10:writer.append(u32(u.reg_read(UC_X86_REG_ESP)+4));ret(0x11003000,4)
  elif p in (0x374f280,0x374f390,0x374f550,0x3750ac0):ret(0x11003000)
  elif p==0x3858e80:ret(0x11002000)
  elif p==0xb62330:ret(0x11002100)
  elif p==0x343a9c9 and kind!='produce':done.append(hex(p));u.emu_stop()
  elif p in (0x3437c6e,0x343aa86,0x343aa0b):done.append(hex(p));u.emu_stop()
 handle=u.hook_add(UC_HOOK_CODE,hook);sp=0x10008000;u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_EBP,sp+0x100);u.reg_write(UC_X86_REG_EAX,selected&0xffffffff);u.reg_write(UC_X86_REG_EBX,0);u.reg_write(UC_X86_REG_ESI,0x11000000)
 u.emu_start(0x3437bb3 if kind=='enum'else 0x343a993,0x20000000,count=20000)
 r=dict(rows=rows,lookups=lookups,done=done,writer=writer,stack_delta=u.reg_read(UC_X86_REG_ESP)-sp);u.hook_del(handle);return r

@unittest.skipIf(Uc is None,'install Unicorn for native execution')
class GuildZeroRegionTest(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  if not os.environ.get('DNF_GUILD_ZERO_INPUT_EXE')or not os.environ.get('DNF_GUILD_ZERO_CANDIDATE_EXE'):raise unittest.SkipTest('set both guild region EXE paths')
  cls.before=Path(os.environ['DNF_GUILD_ZERO_INPUT_EXE']).read_bytes();cls.after=Path(os.environ['DNF_GUILD_ZERO_CANDIDATE_EXE']).read_bytes()
  if patch.sha(cls.before)!=patch.INPUT_SHA256:raise ValueError('wrong baseline')
 def test_only_available_regions_enumerated_with_zero_supported(self):
  for available in ({0},{1,249},set(),{0,1,249},set(range(250))):
   with self.subTest(available=sorted(available)):
    old=flow(self.before,'enum',available);new=flow(self.after,'enum',available)
    self.assertEqual(old['rows'],sorted(x for x in available if 1<=x<=249));self.assertEqual(new['rows'],sorted(x for x in available if 0<=x<=249))
    self.assertEqual(new['rows'],([0]if 0 in available else [])+old['rows']);self.assertEqual(new['stack_delta'],0)
 def test_selected_region_gate_keeps_availability_and_bounds(self):
  for available in ({0},{1,249},set(),{0,1,249}):
   for selected in (-1,0,1,249,250,255,0x7fffffff):
    with self.subTest(available=sorted(available),selected=selected):
     old=flow(self.before,'invite',available,selected);new=flow(self.after,'invite',available,selected)
     self.assertEqual(old['done']==['0x343a9c9'],selected in available and 1<=selected<=249)
     self.assertEqual(new['done']==['0x343a9c9'],selected in available and 0<=selected<=249);self.assertEqual(new['stack_delta'],0)
 def test_existing_producer_keeps_selected_zero_as_zero(self):
  for selected in (0,1,249):
   r=flow(self.after,'produce',{selected},selected);self.assertEqual(r['done'],['0x343aa0b']);self.assertEqual(r['writer'],[741,selected,0x11002100]);self.assertEqual(r['stack_delta'],0)
 def test_only_three_instruction_bytes_and_pe_checksum_change(self):
  result,receipt=patch.build(self.before);self.assertEqual(result,self.after);self.assertEqual(len(self.before),len(self.after));last=0
  for start,end in sorted(receipt['allowed_changed_ranges']):self.assertEqual(self.before[last:start],self.after[last:start]);last=end
  self.assertEqual(self.before[last:],self.after[last:]);_,opt,_,sections=patch.headers(self.after);self.assertEqual(len(sections),20);self.assertEqual(patch.checksum(self.after,opt+64),struct.unpack_from('<I',self.after,opt+64)[0])
 def test_wrong_input_and_existing_output_are_rejected(self):
  with self.assertRaises(ValueError):patch.build(self.before[:-1]+bytes([self.before[-1]^1]))
  with tempfile.TemporaryDirectory()as directory:
   target=Path(directory)/'DNF.exe';target.write_bytes(b'preserve-existing')
   with self.assertRaises(ValueError):patch.write_candidate(Path(os.environ['DNF_GUILD_ZERO_INPUT_EXE']),target)
   self.assertEqual(target.read_bytes(),b'preserve-existing')
 def test_all_receive_gate_decisions_preserved(self):
  vms=[vm(self.before),vm(self.after)];stack=0x10000000;stop=0x20000000
  for opcode in range(65536):
   values=[]
   for u in vms:
    u.mem_write(stack,struct.pack('<II',stop,opcode));u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(0x6e83610,stop,count=40);self.assertEqual(u.reg_read(UC_X86_REG_EIP),stop);self.assertEqual(u.reg_read(UC_X86_REG_ESP),stack+8);values.append(u.reg_read(UC_X86_REG_EAX))
   self.assertEqual(values[0],values[1],opcode)

if __name__=='__main__':unittest.main(verbosity=2)
