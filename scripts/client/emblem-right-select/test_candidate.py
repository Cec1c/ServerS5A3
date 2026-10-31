"""Synthetic PE tooling checks; contains no client binary or private profile."""
from pathlib import Path
import struct
import tempfile
import unittest
import build_candidate as patch


def image(cumulative=False):
    pages = sorted({x & ~4095 for x in (patch.CLICK, *patch.TEXTS, 0x29b172a, 0x29b33d9, 0x249bb22)})
    data = bytearray((len(pages)+1)*4096)
    data[:2] = b'MZ'
    struct.pack_into('<I', data, 60, 0x80)
    data[0x80:0x84] = b'PE\0\0'
    struct.pack_into('<HH', data, 0x84, 0x14c, len(pages))
    struct.pack_into('<H', data, 0x94, 224)
    opt = 0x98
    struct.pack_into('<H', data, opt, 0x10b)
    struct.pack_into('<I', data, opt+4, len(pages)*4096)
    struct.pack_into('<III', data, opt+28, patch.pe.IMAGE_BASE, 4096, 4096)
    struct.pack_into('<II', data, opt+56, pages[-1]-patch.pe.IMAGE_BASE+4096, 4096)
    for i, va in enumerate(pages):
        struct.pack_into('<8sIIIIIIHHI', data, opt+224+i*40, b'.fixture', 4096,
                         va-patch.pe.IMAGE_BASE, 4096, (i+1)*4096, 0, 0, 0, 0, 0x60000020)
    _, sections = patch.pe.headers(data)
    for va, target in [(patch.CLICK, 0x249c1e0), *[(x,0x39a6620) for x in patch.TEXTS],(0x29b33d9,0x3826970),(0x249bb22,0x32c7e40)]:
        at = patch.pe.offset(sections, va, 5)
        data[at:at+5] = b'\xe8'+struct.pack('<i', target-va-5)
    at=patch.pe.offset(sections,0x29b172a,5)
    data[at:at+5]=bytes.fromhex('8b118b4208')
    return identity(data)


def identity(data):
    data = bytearray(data)
    opt, _ = patch.pe.headers(data)
    struct.pack_into('<I', data, opt+64, patch.pe.checksum(data, opt+64))
    return bytes(data), dict(inputSha256=patch.pe.sha(data), inputSize=len(data))


class ToolingTests(unittest.TestCase):
    def test_exact_range_checksum_and_new_rx_section(self):
        for cumulative in (False,):
            before, profile = image(cumulative)
            after, proof = patch.build(before, profile)
            cursor = 0
            for lo, hi in proof['ranges']:
                self.assertEqual(before[cursor:lo], after[cursor:lo])
                cursor = hi
            self.assertEqual(before[cursor:], after[cursor:len(before)])
            self.assertEqual(len(after), len(before)+4096)
            self.assertEqual(proof['outputSize'], len(after))
            self.assertFalse(proof['gameplayAccepted'])
            opt, sections = patch.pe.headers(after)
            self.assertEqual(struct.unpack_from('<I', after, opt+64)[0], patch.pe.checksum(after, opt+64))
            self.assertEqual(len(sections), len(patch.pe.headers(before)[1])+1)

    def test_identity_and_all_native_hooks_fail_closed(self):
        before, profile = image(True)
        with self.assertRaisesRegex(ValueError, 'exact input'):
            patch.build(before, {**profile, 'inputSha256': '0'*64})
        for va in (patch.CLICK, *patch.TEXTS, 0x29b172a, 0x29b33d9, 0x249bb22):
            data = bytearray(before)
            data[patch.pe.offset(patch.pe.headers(data)[1], va)] ^= 1
            changed, identity_profile = identity(data)
            with self.assertRaisesRegex(ValueError, 'fingerprint|layout hook|callback changed|Shift hook'):
                patch.build(changed, identity_profile)

    def test_invalid_layout_and_checksum_are_rejected(self):
        before, _ = image()
        for field, value in ((70, 0x40), (56, 0x1001), (128, 1)):
            data = bytearray(before)
            struct.pack_into('<H' if field == 70 else '<I', data, 0x98+field, value)
            changed, profile = identity(data)
            with self.assertRaises(ValueError):
                patch.build(changed, profile)
        data = bytearray(before)
        data[0x98+64] ^= 1
        with self.assertRaisesRegex(ValueError, 'checksum'):
            patch.build(data, dict(inputSha256=patch.pe.sha(data), inputSize=len(data)))

    def test_write_requires_exact_output_and_never_overwrites(self):
        before, profile = image()
        after, proof = patch.build(before, profile)
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder)/'before.exe', Path(folder)/'after.exe'
            source.write_bytes(before)
            with self.assertRaisesRegex(ValueError, 'pinned output'):
                patch.write_candidate(source, target, profile)
            pinned = {**profile, 'outputSha256': proof['outputSha256'], 'outputSize': len(after)}
            with self.assertRaisesRegex(ValueError, 'pinned output'):
                patch.write_candidate(source, target, {**pinned, 'outputSha256': '0'*64})
            self.assertFalse(target.exists())
            patch.write_candidate(source, target, pinned)
            self.assertEqual(target.read_bytes(), after)
            with self.assertRaisesRegex(ValueError, 'new output'):
                patch.write_candidate(source, target, pinned)
            self.assertEqual(source.read_bytes(), before)
            with self.assertRaisesRegex(ValueError, 'repeated patch'):
                patch.build(after, dict(inputSha256=patch.pe.sha(after), inputSize=len(after)))


if __name__ == '__main__':
    unittest.main()
