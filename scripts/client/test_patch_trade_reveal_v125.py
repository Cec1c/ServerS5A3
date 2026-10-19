import importlib.util
import os
from pathlib import Path
import struct
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('trade_reveal_patch',Path(__file__).with_name('patch_trade_reveal_v125.py'))
patch=importlib.util.module_from_spec(spec);spec.loader.exec_module(patch)

class TradeRevealPatchTest(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  source=os.environ.get('DNF_TRADE_REVEAL_EXE')
  if not source:raise unittest.SkipTest('set DNF_TRADE_REVEAL_EXE to exact v125 fixture')
  cls.source=Path(source);cls.old=cls.source.read_bytes();cls.new,cls.receipt=patch.build(cls.old)

 def test_only_declared_original_ranges_change(self):
  ranges=self.receipt['allowed_original_changed_ranges'];masked=bytearray(self.new[:len(self.old)])
  for start,end in ranges:masked[start:end]=self.old[start:end]
  self.assertEqual(bytes(masked),self.old)
  self.assertTrue(all(self.old[start:end]!=self.new[start:end] for start,end in ranges))
  pe,opt,_,sections=patch.headers(self.new)
  self.assertEqual(len(sections),21);self.assertEqual(sections[-1]['name'].rstrip(b'\0'),b'.trdrevl')
  self.assertEqual(struct.unpack_from('<I',self.new,opt+64)[0],patch.checksum(self.new,opt+64))

 def test_all_receiver_decisions_and_prior_sections_preserved(self):
  _,_,_,before=patch.headers(self.old);_,_,_,after=patch.headers(self.new)
  self.assertEqual(before,after[:-1])
  bitmap=patch.offset_for_rva(before,0x6e92300-patch.BASE,0x5c2//8+1)
  self.assertEqual(self.old[bitmap:bitmap+0xb9],self.new[bitmap:bitmap+0xb9])
  for opcode in range(65536):
   prior=opcode<=0x5c2 and bool(self.old[bitmap+opcode//8]&(1<<(opcode%8)))
   now=opcode<=0x5c2 and bool(self.new[bitmap+opcode//8]&(1<<(opcode%8)))
   self.assertEqual(prior,now)

 def test_exact_input_required(self):
  changed=bytearray(self.old);changed[0x1000]^=1
  with self.assertRaisesRegex(ValueError,'exact v125'):patch.build(bytes(changed))

 def test_existing_or_same_output_rejected_without_writes(self):
  with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
  with tempfile.TemporaryDirectory()as directory:
   out=Path(directory)/'DNF.exe';out.write_bytes(b'keep')
   with self.assertRaises(ValueError):patch.write_candidate(self.source,out)
   self.assertEqual(out.read_bytes(),b'keep')

if __name__=='__main__':unittest.main()
