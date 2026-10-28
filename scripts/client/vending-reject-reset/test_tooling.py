#!/usr/bin/env python3
"""Static unit tests for the public vending reject-reset tooling."""

from __future__ import annotations

import hashlib
from pathlib import Path
import struct
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import build_candidate  # noqa: E402
import verify_reset  # noqa: E402


def put_u16(data: bytearray, offset: int, value: int) -> None:
    struct.pack_into("<H", data, offset, value)


def put_u32(data: bytearray, offset: int, value: int) -> None:
    struct.pack_into("<I", data, offset, value)


def synthetic_pe() -> bytes:
    image_base = 0x400000
    section_alignment = 0x1000
    file_alignment = 0x200
    data = bytearray(0x1200)
    data[:2] = b"MZ"
    put_u32(data, 0x3C, 0x80)
    data[0x80:0x84] = b"PE\0\0"
    put_u16(data, 0x84, 0x14C)
    put_u16(data, 0x86, 1)
    put_u16(data, 0x94, 224)
    put_u16(data, 0x96, 0x0102)
    optional = 0x98
    put_u16(data, optional, 0x10B)
    put_u32(data, optional + 16, 0x1000)
    put_u32(data, optional + 28, image_base)
    put_u32(data, optional + 32, section_alignment)
    put_u32(data, optional + 36, file_alignment)
    put_u32(data, optional + 56, 0x2000)
    put_u32(data, optional + 60, 0x200)
    table = optional + 224
    data[table:table + 8] = b".text\0\0\0"
    put_u32(data, table + 8, 0x1000)
    put_u32(data, table + 12, 0x1000)
    put_u32(data, table + 16, 0x1000)
    put_u32(data, table + 20, 0x200)
    put_u32(data, table + 36, 0x60000020)

    def place(va: int, payload: bytes) -> None:
        offset = 0x200 + (va - (image_base + 0x1000))
        data[offset:offset + len(payload)] = payload

    place(0x401000, bytes.fromhex("6a0068ba0b0000"))
    place(0x401100, bytes.fromhex("8b868c000000"))
    place(0x401110, bytes.fromhex("89868c000000"))
    place(0x401200, bytes.fromhex("558bec83ec085356"))
    place(0x401260, bytes.fromhex("c20400"))
    place(0x401300, bytes.fromhex("6a008bcee833feffff"))
    return bytes(data)


def profile(data: bytes, expected_output: str | None = None, expected_size: int | None = None) -> dict:
    return {
        "schema": build_candidate.PROFILE_SCHEMA,
        "input_sha256": hashlib.sha256(data).hexdigest(),
        "input_size": len(data),
        "image_base": 0x400000,
        "failure_branch": {
            "site_va": "0x401000",
            "original_bytes": "6a0068ba0b0000",
            "resume_va": "0x401007",
        },
        "controller": {
            "manager_global_va": "0x401080",
            "slot_offset": "0x8c",
            "getter_load": {"va": "0x401100", "bytes": "8b868c000000"},
            "getter_store": {"va": "0x401110", "bytes": "89868c000000"},
        },
        "reset_call": {
            "function_va": "0x401200",
            "argument": 0,
            "prologue": {"va": "0x401200", "bytes": "558bec83ec085356"},
            "return": {"va": "0x401260", "bytes": "c20400"},
            "callers": [{"va": "0x401300", "bytes": "6a008bcee833feffff"}],
        },
        "new_section": {
            "name": ".vreset",
            "virtual_size": "0x100",
            "characteristics": "0x60000020",
        },
        "expected_output_sha256": expected_output,
        "expected_output_size": expected_size,
    }


class ToolTests(unittest.TestCase):
    def test_strict_profile_and_deterministic_build(self):
        baseline = synthetic_pe()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            profile_path = root / "profile.json"
            profile_path.write_text(__import__("json").dumps(profile(baseline)), "utf8")
            loaded = build_candidate.load_profile(profile_path)
            candidate, receipt = build_candidate.build(baseline, loaded)
            self.assertEqual(receipt["input"]["sha256"], hashlib.sha256(baseline).hexdigest())
            self.assertEqual(receipt["output"]["sha256"], hashlib.sha256(candidate).hexdigest())
            self.assertNotEqual(candidate, baseline)
            layout = build_candidate.pe32_layout(baseline, 0x400000)
            site_offset = build_candidate.file_offset(layout, 0x401000, 7)
            manager_offset = build_candidate.file_offset(layout, 0x401080, 3)
            self.assertEqual(candidate[site_offset], 0xE9)
            self.assertEqual(candidate[manager_offset:manager_offset + 3], b"\0\0\0")
            self.assertEqual(receipt["newSection"]["name"], ".vreset")
            self.assertFalse(receipt["inputPathRecorded"])
            built_again, receipt_again = build_candidate.build(baseline, loaded)
            self.assertEqual(built_again, candidate)
            self.assertEqual(receipt_again["output"], receipt["output"])

    def test_declared_ranges_and_expected_output_gate(self):
        baseline = synthetic_pe()
        with tempfile.TemporaryDirectory() as folder:
            p = Path(folder) / "profile.json"
            p.write_text(__import__("json").dumps(profile(baseline)), "utf8")
            loaded = build_candidate.load_profile(p)
        plan = build_candidate.build_plan(baseline, loaded)
        candidate, _ = build_candidate.build(baseline, loaded)
        cursor = 0
        for row in sorted(plan["changedOriginalRanges"], key=lambda item: item["offset"]):
            low, high = row["offset"], row["offset"] + row["length"]
            self.assertEqual(baseline[cursor:low], candidate[cursor:low])
            cursor = high
        self.assertEqual(baseline[cursor:], candidate[cursor:len(baseline)])
        wrong = bytearray(baseline)
        wrong[0x200] ^= 1
        with self.assertRaises(ValueError):
            build_candidate.build(bytes(wrong), loaded)

    def test_write_requires_expected_digest_and_refuses_existing_paths(self):
        baseline = synthetic_pe()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            input_path = root / "input.exe"
            input_path.write_bytes(baseline)
            profile_path = root / "profile.json"
            profile_path.write_text(__import__("json").dumps(profile(baseline)), "utf8")
            output = root / "candidate.exe"
            receipt = root / "receipt.json"
            output.write_bytes(b"sentinel")
            with self.assertRaises(ValueError):
                build_candidate.write_outputs(input_path, profile_path, output, receipt)
            output.unlink()
            candidate, built = build_candidate.build(baseline, build_candidate.load_profile(profile_path))
            pinned = profile(baseline, built["output"]["sha256"], built["output"]["size"])
            profile_path.write_text(__import__("json").dumps(pinned), "utf8")
            written = build_candidate.write_outputs(input_path, profile_path, output, receipt)
            self.assertEqual(output.read_bytes(), candidate)
            self.assertEqual(written["output"]["sha256"], built["output"]["sha256"])

    def test_unknown_profile_field_is_rejected(self):
        baseline = synthetic_pe()
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "profile.json"
            value = profile(baseline)
            value["unexpected"] = True
            path.write_text(__import__("json").dumps(value), "utf8")
            with self.assertRaises(ValueError):
                build_candidate.load_profile(path)

    def test_public_fixture_contract(self):
        baseline = synthetic_pe()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            profile_path = root / "profile.json"
            profile_path.write_text(__import__("json").dumps(profile(baseline)), "utf8")
            loaded = build_candidate.load_profile(profile_path)
            candidate, built = build_candidate.build(baseline, loaded)
            profile_path.write_text(__import__("json").dumps(profile(baseline, built["output"]["sha256"], built["output"]["size"])), "utf8")
            loaded = build_candidate.load_profile(profile_path)
            fixture = root / "fixture.py"
            fixture.write_text(
                "def verify_reset_fixture(profile, baseline, candidate):\n"
                "    return {'passed': True, 'tests': 8, 'failures': 0, 'errors': 0, 'skipped': 0,\n"
                "            'redFailureAdvanced': True, 'greenFailureReset': True, 'repeatedFailureReset': True,\n"
                "            'successParity': True, 'retrySuccessParity': True, 'nullManagerSafe': True,\n"
                "            'nullControllerSafe': True, 'helperMapped': True}\n",
                "utf8",
            )
            fixture_sha256 = hashlib.sha256(fixture.read_bytes()).hexdigest()
            report = verify_reset.verify(loaded, baseline, candidate, fixture, fixture_sha256)
            self.assertTrue(report["passed"])
            self.assertEqual(report["native"]["tests"], 8)
            self.assertFalse(report["native"]["fixturePathRecorded"])
            static = verify_reset.verify(loaded, baseline, candidate, None, None)
            self.assertFalse(static["passed"])
            self.assertEqual(static["status"], "static-only-native-not-run")


if __name__ == "__main__":
    unittest.main(verbosity=2)
