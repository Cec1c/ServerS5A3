"""Native dispatcher, notice reader/check/mark and first-entry report.

std::set storage, strings and video UI are adapters; notice functions and the
503 scalar writer execute original code. No game process is launched.
"""
import os,struct,tempfile,unittest
from pathlib import Path
import patch_account_notice_progress as patch
from test_guild_social_channel_v179 import SocialSender,native
from test_patch_fatigue_count_v164 import windows_image
from unicorn.x86_const import *

class NoticeNative(SocialSender):
 FLAGS=0x20018000
 def __init__(self,data):
  super().__init__(data);self.flags=set();self.videos=0
  self.put(0x568A080,self.FLAGS);self.put(0x2000C000,0x2034320,0)
  self.hooks.update({0x25244A0:lambda:self.ret(4,self.NETWORK),
   0xA8F6C0:self.end,0xFA1600:self.find,0x204B100:self.insert,0x2142F90:self.clear,
   0x88B760:self.copy_video,0x23FB630:lambda:self.ret(32),0x23FA5B0:lambda:self.ret(),
   0x32BEA50:self.open_video,0x91A390:lambda:self.ret()})
 def iterator(self,out,value,pop):
  self.put(out,0x2000E000,0,value);self.put(0x2000E000,0x2000E100);self.ret(pop,out)
 def end(self):
  assert self.reg(UC_X86_REG_ECX)==self.FLAGS+20
  self.iterator(self.arg(),0,4)
 def find(self):
  assert self.reg(UC_X86_REG_ECX)==self.FLAGS+20
  id=self.read(self.arg(1));self.iterator(self.arg(),id+1 if id in self.flags else 0,8)
 def insert(self):
  assert self.reg(UC_X86_REG_ECX)==self.FLAGS+20
  id=self.read(self.arg(1));self.flags.add(id);self.iterator(self.arg(),id+1,8)
 def clear(self):
  assert self.reg(UC_X86_REG_ECX)==self.FLAGS+20
  self.flags.clear();self.ret()
 def copy_video(self):
  assert self.arg()==self.MANAGER+0xEC
  self.u.mem_write(self.reg(UC_X86_REG_ECX),bytes(32));self.ret(4)
 def open_video(self):
  assert self.arg()==742
  assert bytes(self.u.mem_read(self.arg(1)+32,2))==bytes([1,1])
  self.videos+=1;self.ret(12,0)
 def enter(self):self.run(0x106C4E0);return self.videos
 def receive(self,body):
  wire=bytearray(16)+body;wire[0]=0;struct.pack_into('<H',wire,1,440);struct.pack_into('<I',wire,3,len(wire))
  self.u.mem_write(self.PACKET,bytes(wire));self.run(0x25239A0,(0,self.PACKET,0),ecx=self.NETWORK)
  return set(self.flags)
 def has(self,id):return bool(self.run(0x22894E0,(id,),ecx=self.FLAGS)&255)
 def enter_area(self,town,area):
  self.put(self.STACK,self.STOP,town,area)
  self.u.reg_write(UC_X86_REG_ESP,self.STACK)
  self.u.reg_write(UC_X86_REG_ECX,self.SCENE)
  self.u.emu_start(0x3705C80,0x3705CD0,count=100000)
  assert self.reg(UC_X86_REG_EIP)==0x3705CD0
  return self.videos

class NoticeTests(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  path=os.environ.get('DNF_ACCOUNT_NOTICE_CLIENT')
  if not path:raise unittest.SkipTest('exact cumulative guild item-link input required')
  cls.source=Path(path);cls.before=cls.source.read_bytes();cls.after,cls.receipt=patch.build(cls.before)
 def test_native_first_entry_report_and_reconnect(self):
  lab=NoticeNative(self.after)
  self.assertEqual(lab.receive(bytes([0])),set())
  self.assertEqual(lab.enter(),1);self.assertEqual(lab.sent,[(503,struct.pack("<I",42))]);self.assertTrue(lab.has(42))
  self.assertEqual(lab.enter(),1);self.assertEqual(len(lab.sent),1)
  folder=os.environ.get('DNF_ACCOUNT_NOTICE_NATIVE_DIR')
  if folder:
   Path(folder).mkdir(parents=True,exist_ok=True);Path(folder,'request.bin').write_bytes(lab.sent[0][1])
  for data,expected in ((self.before,1),(self.after,0)):
   relog=NoticeNative(data);relog.receive(bytes([1,42]));self.assertEqual(relog.enter(),expected)
  lab.receive(bytes([0]));self.assertFalse(lab.has(42));self.assertEqual(lab.enter(),2)
 def test_native_full_set_bounds_no_report_on_restore(self):
  lab=NoticeNative(self.after);body=bytes([128])+bytes(range(128))
  self.assertEqual(lab.receive(body),set(range(128)));self.assertEqual(lab.sent,[])
  self.assertEqual(lab.read(0x57F9AD4),0)
  self.assertTrue(all(lab.has(i) for i in range(128)));self.assertFalse(lab.has(128))
  for id in (128,255,0xffffffff):lab.run(0x2289770,(id,),ecx=lab.FLAGS)
  self.assertEqual(lab.sent,[]);self.assertEqual(lab.enter(),0)
 def test_actual_guardian_area_entry(self):
  lab=NoticeNative(self.after)
  for town,area in [(1,3),(8,2),(8,0)]:self.assertEqual(lab.enter_area(town,area),0)
  self.assertEqual(lab.enter_area(8,3),1)
  self.assertEqual(lab.enter_area(8,3),1)
  self.assertEqual(lab.sent,[(503,struct.pack('<I',42))])
 def test_go_production_notice(self):
  folder=os.environ.get('DNF_ACCOUNT_NOTICE_NATIVE_DIR')
  if not folder:self.skipTest('Go production notice directory required')
  for name,expected in [('notice-viewed.bin',0),('notice-fresh.bin',1)]:
   lab=NoticeNative(self.after);lab.receive(Path(folder,name).read_bytes());self.assertEqual(lab.enter(),expected)
 def test_all_gate_ids_and_ranges(self):
  for kind in (0,1,2,3,127,255):
   before=native(self.before,(kind,),range(65536));after=native(self.after,(kind,),range(65536))
   self.assertEqual([i for i in range(65536) if before[i]!=after[i]],[440] if kind==0 else [])
  cursor=0
  for lo,hi in self.receipt['changedRanges']:
   self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
  self.assertEqual(self.before[cursor:],self.after[cursor:]);self.assertEqual(len(self.before),len(self.after))
  for bad in (self.after,self.before[:-1]):
   with self.assertRaises(ValueError):patch.build(bad)
  with tempfile.TemporaryDirectory() as d:
   out=Path(d,'candidate.exe');patch.write_candidate(self.source,out);windows_image(out)
   with self.assertRaises(ValueError):patch.write_candidate(self.source,out)
  with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
if __name__=='__main__':unittest.main()
