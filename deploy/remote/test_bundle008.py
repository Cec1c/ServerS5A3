import copy
import hashlib
from pathlib import Path
import re
import unittest

import bundle008 as bundle


class Bundle008PublicContract(unittest.TestCase):
    def test_published_channel_is_the_exact_120_row_asset(self):
        path = Path(__file__).resolve().parents[2] / 'resource-recipes/bundle008/channel.override.etc'
        raw = path.read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), bundle.BUNDLE008_CHANNEL_SHA)
        rows = re.findall(rb'^\s*(\d+)\s+`<chn_channel_info_[^>]+>`', raw, re.M)
        self.assertEqual(len(rows), 120)
        self.assertEqual(len(set(rows)), 120)

    def records(self):
        return [dict(path=name, **pin) for name, pin in bundle.BUNDLE008_CLIENT_FILES.items()]

    def test_all_nine_exact_records_are_required(self):
        original = self.records()
        self.assertEqual(bundle.checked_file_records(original), self.records())
        for rows in (original[:-1], original + [original[0]], original[1:] + [original[1]]):
            with self.assertRaises(ValueError):
                bundle.checked_file_records(rows)

    def test_bad_path_hash_size_and_boolean_size_are_rejected(self):
        for key, value in (('path', '../DNF.exe'), ('path', 'dnf.exe'), ('sha256', '0'*64), ('bytes', True), ('bytes', 1)):
            rows = copy.deepcopy(self.records());rows[0][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                bundle.checked_file_records(rows)

    def test_resource_and_channel_bundle_cannot_be_split(self):
        for pair, channel in (({'bundle': bundle.BUNDLE008_ID}, {}), ({}, {'bundle': bundle.BUNDLE008_ID})):
            with self.assertRaises(ValueError):
                bundle.checked_resource_channel_bundle(pair, channel)

    def test_119_to_120_port_change_is_exact(self):
        before = list(range(10001, 10113)) + list(range(10501, 10508))
        # The synthetic original set must not already contain the new PVP 75.
        before[74] = 10499
        after = [p for p in before if p not in range(10501, 10508)] + [10075] + list(range(10191, 10198))
        bundle.checked_bundle008_ports(before, after)
        with self.assertRaises(ValueError):
            bundle.checked_bundle008_ports(before, after[:-1])
        with self.assertRaises(ValueError):
            bundle.checked_bundle008_ports(before, after[:-1] + [10999])


if __name__ == '__main__':
    unittest.main()
