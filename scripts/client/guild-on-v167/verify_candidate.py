"""Verify the composed image without starting a game process."""
import argparse
import json
from pathlib import Path
import struct
import sys
import unittest

import build_candidate as build

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-v166', type=Path, required=True)
    parser.add_argument('--input-v167', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--manifest', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--deps', type=Path)
    args = parser.parse_args()
    build.require(not args.report.exists(), 'report must be new')
    if args.deps:
        sys.path.insert(0, str(args.deps.resolve()))
    from test_patch_fatigue_count_v164 import native, windows_image
    from test_patch_guild_grade_region import GuildGradeRegionTest, Sender
    from test_guild_lifecycle_v166 import GuildLifecycleNativeTest
    from test_guild_teleport_v166 import GuildTeleportNativeTest
    base, before, after = args.base_v166.read_bytes(), args.input_v167.read_bytes(), args.candidate.read_bytes()
    expected, manifest = build.build(base, before)
    build.require(expected == after, 'candidate is not the exact reproducible composition')
    build.require(json.loads(args.manifest.read_text('utf-8')) == manifest, 'manifest mismatch')
    del expected

    class Gate(unittest.TestCase):
        def test_full_receive_matrix_and_abi(self):
            old = native(before, (0, 1), range(65536))
            new = native(after, (0, 1), range(65536))
            delta = [(i // 65536, i % 65536) for i in range(len(old)) if old[i] != new[i]]
            self.assertEqual(delta, [(0, 377), (1, 303), (1, 434)])
            for cls, op in delta:
                self.assertEqual((old[cls * 65536 + op], new[cls * 65536 + op]), (0, 1))
            ops = (0, 126, 153, 303, 377, 434, 435, 541, 741, 830, 65535)
            self.assertEqual(native(before, (2, 255), ops), native(after, (2, 255), ops))
        def test_original_peer_bytes_headers_and_checksum(self):
            cursor = 0
            for lo, hi in manifest['changedRanges']:
                self.assertEqual(before[cursor:lo], after[cursor:lo])
                cursor = hi
            self.assertEqual(before[cursor:], after[cursor:])
            opt, sections = build.disband.pe.headers(after)
            self.assertEqual(sections, build.disband.pe.headers(before)[1])
            self.assertEqual(struct.unpack_from('<I', after, opt + 64)[0], build.disband.pe.checksum(after, opt + 64))
            self.assertEqual(len(after), build.V167_SIZE)
        def test_windows_no_execute_mapping(self):
            windows_image(args.candidate)
        def test_wrong_or_already_patched_input_rejected(self):
            for bad in (after, before[:-1]):
                with self.assertRaises(ValueError):
                    build.build(base, bad)

    class Grade(GuildGradeRegionTest):
        @classmethod
        def setUpClass(cls):
            cls.old, cls.new = Sender(before), Sender(after)

    class Lifecycle(GuildLifecycleNativeTest):
        @classmethod
        def setUpClass(cls):
            cls.lab = Sender(after)
        def test_composed_disband_callback(self):
            self.assertEqual(self.execute(build.disband.CALLBACK_VA, (0, 1, 0)), [])
            for code, message in ((3, 6596), (34, 6576), (36, 36312), (87, 46335)):
                calls = self.execute(build.disband.CALLBACK_VA, (0, 0, code))
                self.assertIn(('text', message), calls)
                self.assertIn(('dialog', 726), calls)

    class Teleport(GuildTeleportNativeTest):
        @classmethod
        def setUpClass(cls):
            cls.lab = Sender(after)

    selected = {
        Gate: ['test_full_receive_matrix_and_abi', 'test_original_peer_bytes_headers_and_checksum',
               'test_windows_no_execute_mapping', 'test_wrong_or_already_patched_input_rejected'],
        Grade: ['test_zero_region_red_green_and_valid_boundaries', 'test_invalid_regions_do_not_reach_cache_or_writer',
                'test_member_cache_and_special_state_checks_remain'],
        Lifecycle: ['test_exit_and_kick_ack_read_order_and_return', 'test_clear_notification_resets_model_guild_identity_and_hall',
                    'test_disband_success_consumes_no_body', 'test_composed_disband_callback'],
        Teleport: ['test_native_request_and_consent_writers', 'test_native_377_slot_mapping_and_terminal_states',
                   'test_native_434_failure_consumes_full_trailer'],
    }
    suite = unittest.TestSuite(cls(name) for cls, names in selected.items() for name in names)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    passed = result.wasSuccessful() and not result.skipped and result.testsRun == 14
    report = dict(passed=passed, cases=result.testsRun, failures=len(result.failures), errors=len(result.errors),
                  skipped=len(result.skipped), manifest=manifest, receivePairsCompared=2 * 65536,
                  windowsNoExecuteMapping=True if passed else None, gameProcessStarted=False,
                  limits=['bounded native memory/cache/network/UI substitutes', 'no multiplayer or real GUI acceptance'],
                  sources={str(p.relative_to(Path(__file__).resolve().parent.parent)): build.sha(p.read_bytes())
                           for p in [Path(__file__).resolve(), Path(build.__file__).resolve(),
                                     *[Path(m.__file__).resolve() for m in (build.grade, build.disband, build.teleport)]]})
    with args.report.open('x', encoding='utf-8') as stream:
        json.dump(report, stream, indent=2)
    print(json.dumps(dict(passed=passed, cases=result.testsRun, outputSha256=manifest['outputSha256'])))
    return 0 if passed else 1

if __name__ == '__main__':
    raise SystemExit(main())
