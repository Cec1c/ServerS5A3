from __future__ import annotations

import copy
import importlib.util
import json
import os
from pathlib import Path
import unittest


HERE = Path(__file__).resolve().parent
EVIDENCE_DIR = Path(os.environ["ITEM_TEXT_RECEIPT_DIR"]) if "ITEM_TEXT_RECEIPT_DIR" in os.environ else None


def load_json(path: Path):
    with path.open("r", encoding="utf-8") as stream:
        return json.load(stream)


def load_builder():
    path = HERE / "build_item_text.py"
    spec = importlib.util.spec_from_file_location("build_item_text_under_test", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load build_item_text.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ReceiptValidationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        required = [
            HERE / "manifest.json",
            HERE / "expected-receipt.json",
            HERE / "item-text.schema2.json",
            HERE / "receipt-fixture.json",
        ]
        missing = [str(path) for path in required if not path.is_file()]
        if missing:
            raise AssertionError("receipt fixtures missing: " + ", ".join(missing))

        cls.builder = load_builder()
        cls.manifest = load_json(HERE / "manifest.json")
        cls.plan = load_json(HERE / "item-text.schema2.json")
        cls.expected_receipt = load_json(HERE / "expected-receipt.json")
        cls.patch_receipt = load_json(EVIDENCE_DIR / "patch.receipt.json" if EVIDENCE_DIR else HERE / "receipt-fixture.json")
        cls.verify_receipt = load_json(EVIDENCE_DIR / "verify.receipt.json" if EVIDENCE_DIR else HERE / "receipt-fixture.json")
        cls.contract = cls.builder.validate_expected_receipt(
            cls.manifest, cls.plan, cls.expected_receipt
        )
        candidate = cls.manifest["expected_candidate"]
        cls.candidate_sha256 = candidate["sha256"]
        cls.candidate_bytes = candidate["bytes"]

    def validate(self, receipt, manifest=None):
        self.builder.validate_tool_receipt(
            receipt,
            "test",
            manifest or self.manifest,
            self.plan,
            self.contract,
            self.candidate_sha256,
            self.candidate_bytes,
        )

    def test_actual_patch_and_verify_receipts_pass(self):
        self.builder.validate_tool_receipt_pair(
            self.patch_receipt,
            self.verify_receipt,
            self.manifest,
            self.plan,
            self.contract,
            self.candidate_sha256,
            self.candidate_bytes,
        )
        self.assertEqual(self.patch_receipt, self.verify_receipt)

    def test_expected_receipt_contract_is_exact(self):
        self.assertEqual(self.contract["changed_entries"], 3)
        self.assertEqual(self.contract["appended_strings"], 4)
        self.assertEqual(self.contract["unchanged_entries_verified"], 1126435)
        self.assertEqual(self.contract["paths"], {
            "stackable/cash/chn_490701733.stk",
            "stackable/10151001/10151892.stk",
            "stackable/private_aiken_exchange/990031101.stk",
        })

    def test_receipt_schema_must_be_two(self):
        receipt = copy.deepcopy(self.patch_receipt)
        receipt["schema"] = 1
        with self.assertRaisesRegex(ValueError, "schema must be 2"):
            self.validate(receipt)

    def test_tampered_input_digest_is_rejected(self):
        receipt = copy.deepcopy(self.patch_receipt)
        receipt["input_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "input digest mismatch"):
            self.validate(receipt)

    def test_tampered_output_identity_is_rejected(self):
        receipt = copy.deepcopy(self.patch_receipt)
        receipt["output_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "output digest mismatch"):
            self.validate(receipt)

        receipt = copy.deepcopy(self.patch_receipt)
        receipt["output_bytes"] -= 1
        with self.assertRaisesRegex(ValueError, "output byte count mismatch"):
            self.validate(receipt)

    def test_tampered_entry_digest_is_rejected(self):
        receipt = copy.deepcopy(self.patch_receipt)
        first_path = next(iter(receipt["entry_sha256"]))
        receipt["entry_sha256"][first_path] = "0" * 64
        with self.assertRaisesRegex(ValueError, "entry digest.*match manifest"):
            manifest = copy.deepcopy(self.manifest)
            manifest["expected_entry_sha256"] = self.patch_receipt["entry_sha256"]
            self.validate(receipt, manifest)

    def test_tampered_counts_are_rejected(self):
        replacements = {
            "changed_entries": 2,
            "appended_strings": 3,
            "unchanged_entries_verified": 1126434,
        }
        for field, replacement in replacements.items():
            with self.subTest(field=field):
                receipt = copy.deepcopy(self.patch_receipt)
                receipt[field] = replacement
                with self.assertRaisesRegex(ValueError, f"{field} mismatch"):
                    self.validate(receipt)

    def test_missing_entry_path_is_rejected(self):
        receipt = copy.deepcopy(self.patch_receipt)
        first_path = next(iter(receipt["entry_sha256"]))
        del receipt["entry_sha256"][first_path]
        with self.assertRaisesRegex(ValueError, "entry path set mismatch"):
            self.validate(receipt)

    def test_empty_verification_is_rejected(self):
        receipt = copy.deepcopy(self.patch_receipt)
        receipt["verification"] = "  "
        with self.assertRaisesRegex(ValueError, "verification must be a non-empty string"):
            self.validate(receipt)

    def test_patch_and_verify_receipt_mismatch_is_rejected(self):
        verify_receipt = copy.deepcopy(self.verify_receipt)
        verify_receipt["verification"] += " changed"
        with self.assertRaisesRegex(ValueError, "patch and verify receipts differ"):
            self.builder.validate_tool_receipt_pair(
                self.patch_receipt,
                verify_receipt,
                self.manifest,
                self.plan,
                self.contract,
                self.candidate_sha256,
                self.candidate_bytes,
            )

    def test_manifest_entry_expectation_matches_when_available(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["expected_entry_sha256"] = copy.deepcopy(self.patch_receipt["entry_sha256"])
        self.validate(self.patch_receipt, manifest)

        receipt = copy.deepcopy(self.patch_receipt)
        first_path = next(iter(receipt["entry_sha256"]))
        receipt["entry_sha256"][first_path] = "0" * 64
        with self.assertRaisesRegex(ValueError, "entry digest does not match manifest"):
            self.validate(receipt, manifest)


if __name__ == "__main__":
    unittest.main()
