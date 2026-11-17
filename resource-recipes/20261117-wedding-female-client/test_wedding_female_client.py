import importlib.util
import os
from pathlib import Path
import struct
import unittest

spec=importlib.util.spec_from_file_location('wedding_client_build',Path(__file__).with_name('build.py'))
recipe=importlib.util.module_from_spec(spec);spec.loader.exec_module(recipe)


class WeddingClientTests(unittest.TestCase):
    base_environment = 'WEDDING_CLIENT_BASE'
    expected_base = recipe.BASE_SHA
    @classmethod
    def setUpClass(cls):
        path=os.environ.get(cls.base_environment)
        if not path: raise unittest.SkipTest('set '+cls.base_environment+' to the exact formal DNF.exe')
        cls.original=Path(path).read_bytes();cls.candidate=recipe.patch_client(cls.original)
        if recipe.digest(cls.original) != cls.expected_base: raise ValueError('wrong baseline for test class')

    def test_exact_reviewed_derivative_and_checksum(self):
        expected=recipe.VARIANTS[self.expected_base]
        self.assertEqual(recipe.digest(self.candidate),expected['sha256'])
        self.assertEqual(struct.unpack_from('<I',self.candidate,recipe.checksum_offset(self.candidate))[0],expected['peChecksum'])

    def test_only_branch_and_checksum_change_across_the_complete_file(self):
        recipe.verify_difference(self.original,self.candidate)
        offset=recipe.checksum_offset(self.original)
        restored=bytearray(self.candidate);restored[recipe.PATCH]=0x75;restored[offset:offset+4]=self.original[offset:offset+4]
        self.assertEqual(restored,self.original)
        start,size=recipe.STABLE
        self.assertEqual(self.original[start:start+size],self.candidate[start:start+size])

    def test_pe_checksum_independently_matches_pefile(self):
        import pefile
        parsed=pefile.PE(data=self.candidate,fast_load=True)
        try:self.assertEqual(parsed.OPTIONAL_HEADER.CheckSum,parsed.generate_checksum())
        finally:parsed.close()

    def test_all_gender_pairs_skip_popup(self):
        result=recipe.branch_evidence(self.candidate)
        self.assertEqual(len(result['cases']),4)
        self.assertTrue(all(case['destination']==recipe.DESTINATION and not case['popupReached'] for case in result['cases']))

    def test_unknown_base_and_unrelated_edits_rejected(self):
        with self.assertRaises(ValueError):recipe.patch_client(self.candidate)
        tampered=bytearray(self.candidate);tampered[recipe.STABLE[0]]^=1
        struct.pack_into('<I',tampered,recipe.checksum_offset(tampered),recipe.pe_checksum(tampered))
        with self.assertRaisesRegex(ValueError,'unapproved byte'):recipe.verify_difference(self.original,tampered)


class TagBuffWeddingClientTests(WeddingClientTests):
    base_environment = 'WEDDING_TAG_BUFF_CLIENT_BASE'
    expected_base = recipe.TAG_BUFF_BASE_SHA


if __name__=='__main__':unittest.main()
