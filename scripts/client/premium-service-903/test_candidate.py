"""固定发行的构建保护及Windows不执行映像校验；真实EXE由环境变量提供。"""
import ctypes
import importlib.util
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('premium903_build', Path(__file__).with_name('build_candidate.py'))
patch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patch)

OUTPUT_SHA256 = 'c0f55b32f9e4680e5d5be418430c33e56b0fc955e958f6b642acac9a1da67b80'


def probe_image(path):
    from ctypes import wintypes as w
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    kernel.CreateFileW.argtypes = [w.LPCWSTR, w.DWORD, w.DWORD, w.LPVOID, w.DWORD, w.DWORD, w.HANDLE]
    kernel.CreateFileW.restype = w.HANDLE
    kernel.CreateFileMappingW.argtypes = [w.HANDLE, w.LPVOID, w.DWORD, w.DWORD, w.DWORD, w.LPCWSTR]
    kernel.CreateFileMappingW.restype = w.HANDLE
    kernel.CloseHandle.argtypes, kernel.CloseHandle.restype = [w.HANDLE], w.BOOL
    handle = kernel.CreateFileW(str(path.resolve()), 0x80000000, 7, None, 3, 0, None)
    if handle == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        mapping = kernel.CreateFileMappingW(handle, None, 0x11000002, 0, 0, None)
        error = 0 if mapping else ctypes.get_last_error()
        if mapping:
            kernel.CloseHandle(mapping)
        return {'映像映射成功': bool(mapping), 'Windows错误': error, '执行入口': False}
    finally:
        kernel.CloseHandle(handle)


class InputProtectionTests(unittest.TestCase):
    def test_invalid_or_truncated_input_is_rejected(self):
        for data in (b'', b'MZ', bytes(1024)):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                patch.build_bytes(data)


@unittest.skipUnless(os.environ.get('DNF_PREMIUM903_V163') and os.environ.get('DNF_PREMIUM903_CANDIDATE'),
                     '需显式提供固定V163及候选EXE')
class FrozenImageTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ['DNF_PREMIUM903_V163'])
        cls.target = Path(os.environ['DNF_PREMIUM903_CANDIDATE'])
        cls.original = patch.checked_input(cls.source)
        cls.candidate = cls.target.read_bytes()

    def cli(self, *args):
        return subprocess.run([sys.executable, '-B', '-X', 'utf8', str(Path(patch.__file__)), *args],
                              capture_output=True, text=True, encoding='utf8', timeout=60)

    def test_independent_rebuild_matches_pinned_complete_output(self):
        rebuilt, _ = patch.build_bytes(self.original)
        self.assertEqual(patch.digest(rebuilt), OUTPUT_SHA256)
        self.assertEqual(rebuilt, self.candidate)
        self.assertEqual(len(rebuilt), 111894528)

    def test_only_four_approved_windows_change(self):
        restored = bytearray(self.candidate)
        windows = ((392, 396), (1960, 1964), (34748947, 34748952), (111857792, 111857825))
        for start, end in windows:
            restored[start:end] = self.original[start:end]
        self.assertEqual(restored, self.original)
        self.assertEqual(sum(a != b for a, b in zip(self.original, self.candidate)), 33)

    def test_repeated_or_modified_input_is_rejected(self):
        with self.assertRaises(ValueError):
            patch.build_bytes(self.candidate)
        changed = bytearray(self.original)
        changed[-1] ^= 1
        with self.assertRaises(ValueError):
            patch.build_bytes(changed)

    def test_existing_output_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory)/'DNF.exe'
            marker.write_bytes(b'keep-existing-output')
            result = self.cli('--input', str(self.source), '--out-dir', directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_bytes(), b'keep-existing-output')

    def test_repeat_patch_creates_no_output(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)/'new-output'
            result = self.cli('--input', str(self.target), '--out-dir', str(output))
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())

    def test_optimized_cli_is_rejected(self):
        result = subprocess.run([sys.executable, '-B', '-O', str(Path(patch.__file__)), '--help'],
                                capture_output=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)

    @unittest.skipUnless(os.name == 'nt', '需要Windows映像加载器')
    def test_original_and_candidate_map_without_execution(self):
        self.assertTrue(probe_image(self.source)['映像映射成功'])
        self.assertTrue(probe_image(self.target)['映像映射成功'])

    @unittest.skipUnless(os.name == 'nt', '需要Windows独立PE校验和实现')
    def test_checksum_matches_windows_imagehlp(self):
        api = ctypes.WinDLL('imagehlp', use_last_error=True)
        api.MapFileAndCheckSumW.argtypes = [ctypes.c_wchar_p, ctypes.POINTER(ctypes.c_ulong), ctypes.POINTER(ctypes.c_ulong)]
        api.MapFileAndCheckSumW.restype = ctypes.c_ulong
        for path in (self.source, self.target):
            stored, computed = ctypes.c_ulong(), ctypes.c_ulong()
            self.assertEqual(api.MapFileAndCheckSumW(str(path.resolve()), ctypes.byref(stored), ctypes.byref(computed)), 0)
            self.assertEqual(stored.value, computed.value)


if __name__ == '__main__':
    if sys.flags.optimize:
        raise RuntimeError('拒绝-O或优化环境')
    unittest.main()
