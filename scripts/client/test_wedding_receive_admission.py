"""婚姻接收准入候选的输入、PE、范围、回落与输出拒绝回归。"""
import os
from pathlib import Path
import tempfile
import unittest

import patch_wedding_receive_admission as patch
import verify_wedding_receive_admission as verify


class WeddingReceiveAdmission(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get("DNF_WEDDING_INPUT_EXE")
        if not source:
            raise RuntimeError("DNF_WEDDING_INPUT_EXE must name the exact reviewed A5 input")
        cls.source = Path(source)
        cls.before = cls.source.read_bytes()
        cls.after, cls.report = patch.build(cls.before)

    def test_pe_checksum_and_exact_four_ranges_preserve_previous_features(self):
        result = verify.verify_layout(self.before, self.after, self.report)
        self.assertEqual(result["actual_changed_bytes"], 147)
        self.assertTrue(result["all_previous_feature_bytes_unchanged_except_gate_call"])

    def test_strict_input_guards_reject_truncation_growth_mutation_and_candidate(self):
        for name, data in (("truncated", self.before[:-1]), ("grown", self.before + b"\0"),
                           ("wrong-hash", bytes([self.before[0] ^ 1]) + self.before[1:]),
                           ("already-patched", self.after)):
            with self.subTest(name=name), self.assertRaises(ValueError):
                patch.build(data)
        self.assertEqual(verify.sha(self.source.read_bytes()), verify.INPUT_SHA)

    def test_wrong_candidate_or_declared_range_cannot_verify(self):
        altered = bytearray(self.after)
        altered[0x1000] ^= 1
        with self.assertRaises(AssertionError):
            verify.verify_layout(self.before, bytes(altered), self.report)
        report = dict(self.report, changed_ranges=[[0, len(self.before)]])
        with self.assertRaises(AssertionError):
            verify.verify_layout(self.before, self.after, report)

    def test_other_classes_tail_fallback_is_opcode_independent(self):
        result = verify.verify_fallback_structure(self.after)
        self.assertEqual(result["classifications"], list(range(2, 256)))
        self.assertFalse(result["full_254_by_65536_native_crossproduct_executed"])

    def test_native_target_and_old_fallback_smoke_abi(self):
        original, candidate = verify.Native(self.before), verify.Native(self.after)
        # Reproduce the formal verifier's transition from unhooked batches to
        # ABI observation. Cached OLD blocks must still hit the exact observer.
        original.batch(0, (0, 1001, 65535), verify.OLD)
        candidate.batch(0, (0, 1001, 65535), verify.NEW)
        for classification, opcode in ((0, 0), (1, 1032), (0, 1002), (0, 1001), (2, 1032), (255, 65535), (0, 0)):
            with self.subTest(classification=classification, opcode=opcode):
                old, old_entries, _ = original.call_site(classification, opcode)
                new, entries, initial = candidate.call_site(classification, opcode)
                self.assertEqual(len(old_entries), 1)
                if (classification, opcode) in verify.TARGETS:
                    self.assertEqual(new[verify.UC_X86_REG_EAX], 1)
                    self.assertEqual(new[verify.UC_X86_REG_ECX], initial[verify.UC_X86_REG_ECX])
                    self.assertFalse(entries)
                else:
                    self.assertEqual(new, old)
                    self.assertEqual(len(entries), 1)

    def test_input_in_place_and_existing_evidence_outputs_are_rejected(self):
        with tempfile.TemporaryDirectory(prefix="wedding-admission-") as folder:
            root = Path(folder)
            source = root / "input.exe"
            source.write_bytes(b"source guard")
            with self.assertRaises(AssertionError):
                verify.output_paths(source, source)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, source)
            existing_candidate = root / "candidate.exe"
            existing_candidate.write_bytes(b"preserve candidate")
            with self.assertRaises(ValueError):
                patch.write_candidate(source, existing_candidate)
            with self.assertRaises(ValueError):
                patch.write_candidate(source, root / "new.exe", source)
            report = root / "native.json"
            report.write_text("preserve", encoding="utf-8")
            with self.assertRaises(AssertionError):
                verify.output_paths(source, report)
            self.assertEqual(report.read_text(encoding="utf-8"), "preserve")
            fresh = root / "fresh.json"
            (root / "fresh-assets").mkdir()
            with self.assertRaises(AssertionError):
                verify.output_paths(source, fresh)
            self.assertEqual(source.read_bytes(), b"source guard")
            self.assertEqual(existing_candidate.read_bytes(), b"preserve candidate")


if __name__ == "__main__":
    unittest.main()
