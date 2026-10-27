"""Verify the complete decoded catalog and Windows resource-only loading."""
import ctypes
import os
from pathlib import Path
import re
import tempfile
import unittest

import patch_character_server_name as p


class CharacterServerNameTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source=os.environ.get('DNF_CHARACTER_SERVER_DSTR')
        if not source: raise unittest.SkipTest('requires audited dstr file')
        cls.source=Path(source)
        cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=p.build(cls.before,'PurpleOnline')

    def test_only_server_row_changes_in_all_resources(self):
        before,after=p.pefile.PE(data=self.before),p.pefile.PE(data=self.after)
        self.assertEqual(p.resources(before),p.resources(after))
        for ident,lang,offset,size in p.resources(before):
            a=p.decode(self.before[offset:offset+size])
            b=p.decode(self.after[offset:offset+size])
            if (ident,lang)==(102,1042):
                self.assertEqual(b,a.replace('71700>YKCN90','71700>PurpleOnline',1))
                self.assertEqual(re.findall(r'^71700>([^\r\n]*)',b,re.M),['PurpleOnline'])
            else: self.assertEqual(a,b)
        pos=0
        for low,high in self.receipt['changedRanges']:
            self.assertEqual(self.before[pos:low],self.after[pos:low]);pos=high
        self.assertEqual(self.before[pos:],self.after[pos:])
        self.assertEqual(len(self.before),len(self.after))
        self.assertEqual(after.OPTIONAL_HEADER.CheckSum,after.generate_checksum())

    def test_exact_input_and_independent_output(self):
        for data in (self.after,self.before[:-1]):
            with self.assertRaises(ValueError): p.build(data,'PurpleOnline')
        for name in ('','Name\nOther','Name>Other','Name\0Other','long'*100):
            with self.assertRaises(ValueError): p.build(self.before,name)
        with self.assertRaises(ValueError):p.write_candidate(self.source,self.source,'PurpleOnline')
        with tempfile.TemporaryDirectory() as folder:
            target=Path(folder)/'dstr.dat'
            p.write_candidate(self.source,target,'PurpleOnline')
            self.assertEqual(target.read_bytes(),self.after)
            with self.assertRaises(ValueError):p.write_candidate(self.source,target,'PurpleOnline')
            if os.name=='nt':self.verify_windows_resource(target)

    def verify_windows_resource(self,path):
        k=ctypes.WinDLL('kernel32',use_last_error=True)
        k.LoadLibraryExW.argtypes=[ctypes.c_wchar_p,ctypes.c_void_p,ctypes.c_uint32]
        k.LoadLibraryExW.restype=ctypes.c_void_p
        k.FindResourceExW.argtypes=[ctypes.c_void_p,ctypes.c_wchar_p,ctypes.c_void_p,ctypes.c_ushort]
        k.FindResourceExW.restype=ctypes.c_void_p
        k.LoadResource.argtypes=[ctypes.c_void_p,ctypes.c_void_p];k.LoadResource.restype=ctypes.c_void_p
        k.LockResource.argtypes=[ctypes.c_void_p];k.LockResource.restype=ctypes.c_void_p
        k.SizeofResource.argtypes=[ctypes.c_void_p,ctypes.c_void_p];k.SizeofResource.restype=ctypes.c_uint32
        k.FreeLibrary.argtypes=[ctypes.c_void_p]
        module=k.LoadLibraryExW(str(path),None,2)  # LOAD_LIBRARY_AS_DATAFILE: no execution.
        self.assertTrue(module,ctypes.get_last_error())
        try:
            resource=k.FindResourceExW(module,'FILE',ctypes.c_void_p(102),1042)
            self.assertTrue(resource,ctypes.get_last_error())
            size=k.SizeofResource(module,resource)
            address=k.LockResource(k.LoadResource(module,resource))
            self.assertTrue(address)
            text=p.decode(ctypes.string_at(address,size))
            self.assertEqual(re.findall(r'^71700>([^\r\n]*)',text,re.M),['PurpleOnline'])
        finally:k.FreeLibrary(module)


if __name__=='__main__':unittest.main()
