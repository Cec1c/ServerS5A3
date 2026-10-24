"""Pinned V158 artifact and independent preservation checks.

Set STORY_CHAPTER_SOURCE and STORY_CHAPTER_CANDIDATE to binary fixtures.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import pefile
import story_digest_chapter_gates as patch


class ChapterArtifactTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source, candidate = os.getenv('STORY_CHAPTER_SOURCE'), os.getenv('STORY_CHAPTER_CANDIDATE')
        if not source or not candidate:
            raise unittest.SkipTest('matched V158 fixtures not configured')
        cls.source_path, cls.candidate_path = Path(source), Path(candidate)
        cls.source, cls.candidate = cls.source_path.read_bytes(), cls.candidate_path.read_bytes()
        cls.receipt = json.loads(cls.candidate_path.with_name('build-receipt.json').read_text())

    def test_deterministic_rebuild(self):
        output, receipt = patch.build(self.source)
        self.assertEqual(output, self.candidate)
        self.assertEqual(receipt, self.receipt)

    def test_reject_wrong_and_modified_inputs(self):
        changed = bytearray(self.source)
        changed[-1] ^= 1
        for data in (b'MZ', bytes(changed), self.candidate):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                patch.build(data)

    def test_only_two_code_windows_and_declared_pe_fields_change(self):
        before = pefile.PE(data=self.source, fast_load=True)
        after = pefile.PE(data=self.candidate, fast_load=True)
        ranges = [
            (before.get_offset_from_rva(patch.GATE_CALL - 0x400000), 6),
            (before.get_offset_from_rva(patch.original.SNAPSHOT_CALL - 0x400000), 5),
            (before.sections[0].get_file_offset() + len(before.sections) * 40, 40),
            (before.FILE_HEADER.get_field_absolute_offset('NumberOfSections'), 2),
        ]
        ranges += [(before.OPTIONAL_HEADER.get_field_absolute_offset(field), 4)
                   for field in ('SizeOfImage', 'SizeOfCode', 'CheckSum')]
        allowed = {i for start, size in ranges for i in range(start, start + size)}
        actual = {i for i, (a, b) in enumerate(zip(self.source, self.candidate)) if a != b}
        self.assertTrue(actual <= allowed, sorted(actual - allowed))
        self.assertEqual({(c['offset'], c['length']) for c in self.receipt['changes']}, set(ranges))
        self.assertEqual(len(after.sections), len(before.sections) + 1)
        for old, new in zip(before.sections, after.sections):
            self.assertEqual(old.__pack__(), new.__pack__())
        new = after.sections[-1]
        self.assertEqual(new.Name.rstrip(b'\0'), b'.sdg2')
        self.assertEqual(new.Characteristics, 0x60000020)
        self.assertTrue(after.verify_checksum())
        self.assertEqual(len(self.candidate) - len(self.source), new.SizeOfRawData)

    def test_existing_playback_lifecycle_and_snapshot_state_are_preserved(self):
        before, after = [pefile.PE(data=b, fast_load=True) for b in (self.source, self.candidate)]
        for address, size in (
            (patch.original.ORIGINAL, patch.original.CLONE_SIZE),
            (patch.original.AUTO_CALL, 5), (patch.original.CLEAR_ENTRY, 6),
            (patch.original.SELECT_SUCCESS, 6), (patch.original.RETURN_SUCCESS, 6),
            (patch.CLONE + 0x500, 0x200), (patch.STATE, 8),
        ):
            self.assertEqual(before.get_data(address - 0x400000, size),
                             after.get_data(address - 0x400000, size), hex(address))
        self.assertEqual(after.get_data(patch.STATE - 0x400000, 8), bytes(8))

    def test_existing_files_are_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output, receipt = root / 'DNF.exe', root / 'receipt.json'
            output.write_bytes(b'keep-exe')
            receipt.write_bytes(b'keep-receipt')
            for source, target, note in ((self.source_path, output, receipt),
                                         (self.source_path, self.source_path, receipt),
                                         (self.source_path, output, self.source_path)):
                result = subprocess.run([sys.executable, patch.__file__, str(source), str(target),
                                         '--receipt', str(note)], capture_output=True)
                self.assertNotEqual(result.returncode, 0)
            self.assertEqual(output.read_bytes(), b'keep-exe')
            self.assertEqual(receipt.read_bytes(), b'keep-receipt')
        self.assertEqual(patch.sha256(self.source_path.read_bytes()), patch.SOURCE_SHA256)


if __name__ == '__main__':
    unittest.main()
