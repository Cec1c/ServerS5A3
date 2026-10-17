"""Generator boundaries. Set STORY_DIGEST_SOURCE and STORY_DIGEST_CANDIDATE."""
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

import pefile
import story_digest_gate as patch


class GeneratorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source=os.environ.get('STORY_DIGEST_SOURCE')
        candidate=os.environ.get('STORY_DIGEST_CANDIDATE')
        if not source or not candidate:raise unittest.SkipTest('matched binary fixtures not configured')
        cls.source_path,cls.candidate_path=Path(source),Path(candidate)
        cls.source,cls.candidate=cls.source_path.read_bytes(),cls.candidate_path.read_bytes()
        cls.receipt=json.loads(cls.candidate_path.with_name('build-receipt.json').read_text())

    def test_exact_rebuild_and_checksum(self):
        output,receipt=patch.build(self.source)
        self.assertEqual(output,self.candidate)
        self.assertEqual(receipt,self.receipt)
        self.assertTrue(pefile.PE(data=output,fast_load=True).verify_checksum())

    def test_reject_wrong_version_tampered_and_already_patched(self):
        altered=bytearray(self.source);altered[-1]^=1
        for raw in (b'MZ',bytes(altered),self.candidate):
            with self.subTest(size=len(raw)),self.assertRaises(ValueError):patch.build(raw)

    def test_full_diff_whitelist_and_original_section_headers(self):
        restored=bytearray(self.candidate[:len(self.source)])
        for change in self.receipt['changes']:
            at,n=change['offset'],change['length']
            self.assertEqual(restored[at:at+n],bytes.fromhex(change['after']))
            restored[at:at+n]=bytes.fromhex(change['before'])
        self.assertEqual(restored,self.source)
        old,new=[pefile.PE(data=b,fast_load=True)for b in (self.source,self.candidate)]
        for a,b in zip(old.sections,new.sections):self.assertEqual(a.__pack__(),b.__pack__())
        self.assertEqual([s.Name.rstrip(b'\0')for s in new.sections[-2:]],[b'.sdgate',b'.sdstate'])
        self.assertFalse(new.sections[-2].Characteristics&0x80000000)
        self.assertFalse(new.sections[-1].Characteristics&0x20000000)
        self.assertEqual(new.get_data(self.receipt['entries']['state']-0x400000,8),bytes(8))

    def test_original_function_gate_and_author_sections_preserved(self):
        old,new=[pefile.PE(data=b,fast_load=True)for b in (self.source,self.candidate)]
        for va,n in ((patch.ORIGINAL,patch.CLONE_SIZE),(0x6e83610,33),(0x6e92300,185)):
            self.assertEqual(old.get_data(va-0x400000,n),new.get_data(va-0x400000,n))
        for s in old.sections:
            if s.Name.rstrip(b'\0') not in (b'.tp3',b'.tvm0'):
                self.assertEqual(s.get_data(),new.get_data(s.VirtualAddress,s.SizeOfRawData))
        self.assertEqual(new.get_data(0x6a9233b,1),b'\x08')

    def test_existing_output_receipt_and_same_path_refused(self):
        script=Path(patch.__file__)
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);out=root/'candidate.exe';receipt=root/'receipt.json'
            cases=((self.source_path,self.source_path,receipt),(self.source_path,out,self.source_path),
                   (self.source_path,out,receipt))
            out.write_bytes(b'keep-output');receipt.write_bytes(b'keep-receipt')
            for source,dest,note in cases:
                result=subprocess.run([sys.executable,str(script),str(source),str(dest),'--receipt',str(note)],capture_output=True)
                self.assertNotEqual(result.returncode,0)
            self.assertEqual(out.read_bytes(),b'keep-output')
            self.assertEqual(receipt.read_bytes(),b'keep-receipt')
        self.assertEqual(patch.sha256(self.source_path.read_bytes()),patch.SOURCE_SHA256)


if __name__=='__main__':unittest.main()
