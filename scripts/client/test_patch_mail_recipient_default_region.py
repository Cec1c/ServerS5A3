"""Synthetic PE checks; no game binary or native runtime required."""
from pathlib import Path
import struct
import tempfile
import unittest

import patch_mail_recipient_default_region as patch


def image():
    pages = sorted({patch.SITE & ~4095, 0x2961000, patch.QUERY & ~4095,
                    patch.GATE_CALL & ~4095, 0x1FE7000, patch.PREVIOUS_GATE & ~4095, 0x2046000, 0x29DB000})
    data = bytearray((len(pages)+1)*4096)
    data[:2] = b'MZ'
    struct.pack_into('<I', data, 60, 0x80)
    data[0x80:0x84] = b'PE\0\0'
    struct.pack_into('<HH', data, 0x84, 0x14C, len(pages))
    struct.pack_into('<H', data, 0x94, 224)
    opt = 0x98
    struct.pack_into('<H', data, opt, 0x10B)
    struct.pack_into('<I', data, opt+4, len(pages)*4096)
    struct.pack_into('<III', data, opt+28, patch.pe.IMAGE_BASE, 4096, 4096)
    struct.pack_into('<II', data, opt+56, pages[-1]-patch.pe.IMAGE_BASE+4096, 4096)
    for i, va in enumerate(pages):
        struct.pack_into('<8sIIIIIIHHI', data, opt+224+i*40, b'.fixture', 4096,
                         va-patch.pe.IMAGE_BASE, 4096, (i+1)*4096, 0, 0, 0, 0, 0x60000020)
    _, sections = patch.pe.headers(data)
    anchors = [(patch.SITE, patch.ORIGINAL),
               (0x2961D40, bytes.fromhex('8b81ac030000c3')),
               (patch.QUERY, bytes.fromhex('558bec56578b7d088bf1893d20897b05')),
               (0x29D4B94, bytes.fromhex('6844010000')),
               (patch.GATE_CALL, b'\xe8'+struct.pack('<i', patch.PREVIOUS_GATE-patch.GATE_CALL-5)),
               (0x1FE7C71, bytes.fromhex('6a006890c7fb016844010000')),
               (0x2046A98, bytes.fromhex('6a0068402f030268ce020000')),
               (0x29DB2B0, bytes.fromhex('6815030000'))]
    for va, raw in anchors:
        at = patch.pe.offset(sections, va, len(raw))
        data[at:at+len(raw)] = raw
    struct.pack_into('<I', data, opt+64, patch.pe.checksum(data, opt+64))
    result = bytes(data)
    return result, dict(inputSha256=patch.pe.sha(result), inputSize=len(result))


class MailPatchToolingTests(unittest.TestCase):
    def test_synthetic_plan_exact_range_and_layout(self):
        before, profile = image()
        after, proof = patch.build(before, profile)
        self.assertEqual(len(after), len(before)+4096)
        self.assertEqual(len(patch.pe.headers(after)[1]), len(patch.pe.headers(before)[1])+1)
        cursor = 0
        for lo, hi in proof['changedRanges']:
            self.assertEqual(before[cursor:lo], after[cursor:lo])
            cursor = hi
        self.assertEqual(before[cursor:], after[cursor:len(before)])

    def test_native_fingerprint_rejected_even_when_input_identity_updated(self):
        before, profile = image()
        data = bytearray(before)
        opt, sections = patch.pe.headers(data)
        data[patch.pe.offset(sections, patch.SITE)] ^= 1
        struct.pack_into('<I', data, opt+64, patch.pe.checksum(data, opt+64))
        profile['inputSha256'] = patch.pe.sha(data)
        with self.assertRaisesRegex(ValueError, 'native fingerprint'):
            patch.build(bytes(data), profile)

    def test_write_requires_pinned_output_and_refuses_overwrite(self):
        before, profile = image()
        after, proof = patch.build(before, profile)
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder)/'source.exe', Path(folder)/'target.exe'
            source.write_bytes(before)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, target, profile)
            profile.update(outputSha256=proof['outputSha256'], outputSize=proof['outputSize'])
            patch.write_candidate(source, target, profile)
            self.assertEqual(target.read_bytes(), after)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, target, profile)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, source, profile)
            self.assertEqual(source.read_bytes(), before)

    def test_pinned_output_rejects_different_result(self):
        before, profile = image()
        profile.update(outputSha256='0'*64, outputSize=len(before)+4096)
        with self.assertRaisesRegex(ValueError, 'pinned output'):
            patch.build(before, profile)


if __name__ == '__main__':
    unittest.main()
