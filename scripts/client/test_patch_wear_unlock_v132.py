"""Exact-image scope and safe generation tests; no live process access."""
import ctypes
from ctypes import wintypes
import os
from pathlib import Path
import struct
import tempfile
import unittest
import patch_wear_unlock_v132 as patch


class WearUnlockPatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get('DNF_WEAR_UNLOCK_INPUT')
        candidate = os.environ.get('DNF_WEAR_UNLOCK_CANDIDATE')
        if not source or not candidate:
            raise unittest.SkipTest('set DNF_WEAR_UNLOCK_INPUT and DNF_WEAR_UNLOCK_CANDIDATE')
        cls.source = Path(source)
        cls.before = cls.source.read_bytes()
        cls.after = Path(candidate).read_bytes()
        cls.generated, cls.receipt = patch.build(cls.before)

    def test_reproducible_exact_scope(self):
        self.assertEqual(self.generated, self.after)
        self.assertEqual(len(self.before), len(self.after))
        last = 0
        for start, end in self.receipt['original_ranges']:
            self.assertEqual(self.before[last:start], self.after[last:start])
            last = end
        self.assertEqual(self.before[last:], self.after[last:])

    def test_all_receive_and_recovery_code_identical(self):
        _, sections = patch.pe.headers(self.before)
        # Shared gate, bitmap, class adapter and 868 consumer. Whole-file
        # scope test additionally proves every other native function unchanged.
        for address, size in [(0x6e83610,0x40),(0x6e92300,0x100),
                              (0x6ea1000,0x1000),(0x1fadf80,0x100)]:
            at = patch.pe.offset(sections,address,size)
            self.assertEqual(self.before[at:at+size],self.after[at:at+size])

    @unittest.skipUnless(os.name == 'nt', 'Windows ImageHlp independent checksum')
    def test_independent_pe_checksum(self):
        image = ctypes.create_string_buffer(self.after)
        header, computed = wintypes.DWORD(), wintypes.DWORD()
        api = ctypes.WinDLL('imagehlp').CheckSumMappedFile
        api.argtypes = [ctypes.c_void_p,wintypes.DWORD,
                        ctypes.POINTER(wintypes.DWORD),ctypes.POINTER(wintypes.DWORD)]
        api.restype = ctypes.c_void_p
        self.assertTrue(api(image,len(self.after),ctypes.byref(header),ctypes.byref(computed)))
        self.assertEqual(header.value,computed.value)

    def test_wrong_input_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1])
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1]+bytes([self.before[-1]^1]))

    def test_existing_output_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)/'DNF.exe'
            target.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source,target)
            self.assertEqual(target.read_bytes(),b'preserve')


if __name__ == '__main__':
    unittest.main(verbosity=2)
