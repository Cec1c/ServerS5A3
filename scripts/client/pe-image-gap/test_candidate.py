import importlib.util
import os
from pathlib import Path
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("pe_gap", Path(__file__).with_name("build_candidate.py"))
patch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patch)


class LayoutTests(unittest.TestCase):
    def layout(self, second=0x2000, first_size=0x30, raw=0x1000, image=0x3000):
        return dict(headers=0x1000, sectionAlignment=0x1000, imageSize=image,
                    sections=[dict(name="first", rva=0x1000, virtualSize=first_size, rawSize=raw),
                              dict(name="second", rva=second, virtualSize=0x30, rawSize=0x1000)])

    def test_contiguous_raw_alignment(self):
        self.assertEqual(patch.discontinuities(self.layout()), [])

    def test_gap_is_reported(self):
        self.assertEqual(patch.discontinuities(self.layout(second=0xF000, image=0x10000)),
                         [dict(section="second", expected=0x2000, actual=0xF000)])

    def test_virtual_zero_fill_preserves_following_rva(self):
        self.assertEqual(patch.discontinuities(self.layout(second=0xF000, first_size=0xE000, image=0x10000)), [])

    def test_overlap_is_reported(self):
        self.assertTrue(patch.discontinuities(self.layout(first_size=0x2000)))

    def test_image_size_is_checked(self):
        self.assertEqual(patch.discontinuities(self.layout(image=0x4000))[-1]["section"], "SizeOfImage")

    def test_invalid_and_truncated_input_rejected(self):
        for data in (b"", b"MZ", bytes(256)):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                patch.build(data)


@unittest.skipUnless(os.environ.get("DNF_PE_GAP_V162"), "provide the exact private v162 fixture")
class FrozenImageTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ["DNF_PE_GAP_V162"])
        cls.original = cls.source.read_bytes()
        cls.fixed, cls.receipt = patch.build(cls.original)

    def test_only_virtual_size_and_checksum_change(self):
        normalized = bytearray(self.fixed)
        for change in self.receipt["changes"]:
            at, size = change["offset"], change["size"]
            normalized[at:at + size] = self.original[at:at + size]
        self.assertEqual(normalized, self.original)
        self.assertTrue(self.receipt["all44RawSectionsPreserved"])
        self.assertEqual(patch.discontinuities(patch.parse(self.fixed)), [])

    def test_checksum_and_size(self):
        offset = patch.parse(self.fixed)["opt"] + 64
        self.assertEqual(struct.unpack_from("<I", self.fixed, offset)[0], patch.checksum(self.fixed, offset))
        self.assertEqual(len(self.fixed), len(self.original))

    def test_modified_or_already_repaired_input_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.fixed)
        changed = bytearray(self.original)
        changed[-1] ^= 1
        with self.assertRaises(ValueError):
            patch.build(changed)

    @unittest.skipUnless(os.name == "nt", "Windows image loader required")
    def test_native_error_193_to_valid_image_without_execution(self):
        before = patch.probe_image(self.source)
        self.assertFalse(before["mappingSucceeded"])
        self.assertEqual(before["win32Error"], 193)
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "DNF.exe"
            target.write_bytes(self.fixed)
            after = patch.probe_image(target)
        self.assertTrue(after["mappingSucceeded"])
        self.assertFalse(after["executed"])


if __name__ == "__main__":
    unittest.main()
