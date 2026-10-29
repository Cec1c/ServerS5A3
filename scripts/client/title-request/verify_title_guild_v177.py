"""Verify the exact V177 title plus guild-placement composed client.

The verifier is read-only. It composes nothing itself:

* calls build_title_guild_v177.build(original V177),
* requires --candidate to equal every composed byte,
* runs all 17 title-boundary cases on the final image,
* runs every PlacementTests case except test_patch_boundary_and_image on a
  subclass whose explicit setUpClass binds the original V177 and final image,
* requires GUILD_PLACEMENT_NATIVE_DIR and its exported production packets,
* validates the final PE checksum, Windows image mapping, and composed ranges.

The caller supplies the existing Unicorn/Capstone dependency path through
PYTHONPATH; no local dependency path is embedded here.
No game process, GUI, deployment, or source edit is performed.

    python -B verify_title_guild_v177.py \
        --v177 <published-V177.exe> --candidate <composed.exe> --output <report.json>
"""
import argparse
import gc
import hashlib
import io
import json
import os
import struct
import sys
import unittest
from pathlib import Path

sys.dont_write_bytecode = True


SCRIPT_DIR = Path(__file__).resolve().parent
CLIENT_DIR = SCRIPT_DIR.parent
for import_dir in (str(SCRIPT_DIR), str(CLIENT_DIR)):
    if import_dir not in sys.path:
        sys.path.insert(0, import_dir)

REQUIRED_GUILD_TESTS = (
    'test_sender_prefix_and_fields',
    'test_native_geometry',
    'test_snapshot_and_delta_scene_creation',
    'test_exact_gate_delta',
    'test_ack_dispatch',
    'test_production_packets',
)
EXCLUDED_GUILD_TESTS = ('test_patch_boundary_and_image',)
NATIVE_PACKET_FILES = ('reconnect-191.bin', 'notice-777.bin', 'cleared-191.bin')


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def error_message(exc):
    text = str(exc).strip().replace('\n', ' ')
    return '%s: %s' % (type(exc).__name__, text) if text else type(exc).__name__


def require(condition, message):
    if not condition:
        raise ValueError(message)


def pair_range(row):
    require(isinstance(row, (list, tuple)) and len(row) == 2,
            'range is not a [lo, hi] pair')
    lo, hi = row
    require(isinstance(lo, int) and not isinstance(lo, bool) and
            isinstance(hi, int) and not isinstance(hi, bool),
            'range endpoints are not integers')
    require(0 <= lo < hi, 'invalid range %r' % (row,))
    return int(lo), int(hi)


def title_ranges(receipt):
    rows = receipt.get('diff', {}).get('allowedRanges')
    require(isinstance(rows, list), 'title allowedRanges missing')
    result = []
    for row in rows:
        require(isinstance(row, (list, tuple)) and len(row) == 3,
                'title range is not [name, lo, hi]')
        lo, hi = pair_range(row[1:])
        result.append((lo, hi))
    return result


def guild_ranges(receipt):
    rows = receipt.get('changedRanges')
    require(isinstance(rows, list), 'guild changedRanges missing')
    return [pair_range(row) for row in rows]


def existing(path):
    try:
        return path.read_bytes()
    except OSError as exc:
        raise ValueError('cannot read %s: %s' % (path, exc)) from exc


def file_sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def checked_composition(source, candidate, receipt, checksum_range):
    title_component = receipt.get('components', {}).get('title')
    guild_component = receipt.get('components', {}).get('guild')
    require(isinstance(title_component, dict), 'components.title missing')
    require(isinstance(guild_component, dict), 'components.guild missing')

    allowed = [pair_range(row) for row in receipt.get('diff', {}).get('allowedRanges', [])]
    component_title = title_ranges(title_component)
    component_guild = guild_ranges(guild_component)
    require(allowed == sorted(set(component_title + component_guild)),
            'combined allowed ranges differ from component union')
    require(allowed == sorted(set(allowed)), 'combined allowed ranges are not sorted unique')

    require(tuple(checksum_range) in component_title, 'title checksum range missing')
    require(tuple(checksum_range) in component_guild, 'guild checksum range missing')
    require(receipt.get('inputSha256') == sha256(source), 'input receipt digest differs')
    require(receipt.get('outputSha256') == sha256(candidate), 'output receipt digest differs')
    require(receipt.get('inputSize') == len(source), 'input receipt size differs')
    require(receipt.get('outputSize') == len(candidate), 'output receipt size differs')
    require(len(source) == len(candidate), 'composed image changed size')
    require(title_component.get('inputSha256') == sha256(source),
            'title component input digest differs')
    require(guild_component.get('inputSha256') == sha256(source),
            'guild component input digest differs')

    changed = [index for index, (before, after) in enumerate(zip(source, candidate))
               if before != after]
    def covered(index):
        return any(lo <= index < hi for lo, hi in allowed)

    outside = [index for index in changed if not covered(index)]
    require(receipt.get('diff', {}).get('changedBytes') == len(changed),
            'combined changedBytes differs')
    require(receipt.get('diff', {}).get('outsideAllowedRanges') == [],
            'combined receipt reports outside changes')
    require(not outside, 'candidate has changes outside allowed ranges')
    require(receipt.get('diff', {}).get('allOtherBytesUnchanged') is True,
            'combined receipt does not assert all other bytes unchanged')

    end = 0
    for lo, hi in allowed:
        require(lo >= end, 'combined allowed ranges overlap')
        require(source[end:lo] == candidate[end:lo],
                'candidate differs before allowed range %#x-%#x' % (lo, hi))
        end = hi
    require(source[end:] == candidate[end:], 'candidate differs after allowed ranges')

    overlaps = []
    for left in component_title:
        for right in component_guild:
            if max(left[0], right[0]) < min(left[1], right[1]):
                overlaps.append((list(left), list(right)))
    require(len(overlaps) == 1 and
            overlaps[0] == (list(checksum_range), list(checksum_range)),
            'component changes overlap outside the shared PE checksum range')
    return {
        'passed': True,
        'inputSha256': receipt['inputSha256'],
        'outputSha256': receipt['outputSha256'],
        'inputSize': receipt['inputSize'],
        'outputSize': receipt['outputSize'],
        'allowedRanges': [list(row) for row in allowed],
        'changedBytes': len(changed),
        'outsideAllowedRanges': outside,
        'allOtherBytesUnchanged': True,
        'componentRanges': {
            'title': [list(row) for row in component_title],
            'guild': [list(row) for row in component_guild],
        },
        'componentNonChecksumOverlap': False,
        'overlapOnlyPEChecksum': True,
        'components': receipt['components'],
    }


def run_title_boundaries(boundary, pe, source, candidate):
    base_image = pe.Image.from_bytes(source)
    final_image = pe.Image.from_bytes(candidate)
    require(len(boundary.CASES) == 17, 'title boundary CASES count is not 17')
    rows = []
    failures = []
    try:
        pe.available_memory()
        for name, ret, parent, slot, count, arg4, before, after, mode, null_frame in boundary.CASES:
            row = {
                'name': name, 'return': hex(ret), 'slot': slot, 'count': count,
                'arg4': arg4, 'mode': mode, 'nullFrame': null_frame,
            }
            try:
                got_before = boundary.run_case(base_image, ret, parent, slot, count, arg4,
                                               mode, null_frame)
                got_after = boundary.run_case(final_image, ret, parent, slot, count, arg4,
                                              mode, null_frame)
                require(got_before['marker'] == before,
                        '%s baseline marker %r' % (name, got_before['marker']))
                require(got_after['marker'] == after,
                        '%s final marker %r' % (name, got_after['marker']))
                require(got_before['tape'] == [before & 0xFF],
                        '%s baseline tape %r' % (name, got_before['tape']))
                require(got_after['tape'] == [after & 0xFF],
                        '%s final tape %r' % (name, got_after['tape']))
                row.update({'before': got_before, 'after': got_after, 'passed': True})
            except Exception as exc:
                row['passed'] = False
                row['error'] = error_message(exc)
                failures.append('%s: %s' % (name, row['error']))
            rows.append(row)
            gc.collect()
    except Exception as exc:
        failures.append(error_message(exc))
    claims = {
        'onlyStub': hex(boundary.APPEND_LEAF),
        'syntheticCases': [row['name'] for row in rows if row['mode'] == 'synthetic'],
        'note': 'Tape must be exactly one byte equal to the marker.',
    }
    return rows, claims, failures


def run_guild_tests(guild_tests, source_path, source, candidate, receipt):
    native_dir_value = os.environ.get('GUILD_PLACEMENT_NATIVE_DIR', '').strip()
    if not native_dir_value:
        return {
            'executed': False,
            'counts': {'run': 0, 'failures': 0, 'errors': 1, 'skips': 0},
            'failures': ['GUILD_PLACEMENT_NATIVE_DIR is required'],
            'errors': [],
            'skips': [],
            'methods': [],
            'excluded': list(EXCLUDED_GUILD_TESTS),
        }
    native_dir = Path(native_dir_value)
    missing = [name for name in NATIVE_PACKET_FILES if not (native_dir / name).is_file()]
    if missing:
        return {
            'executed': False,
            'nativeDirectory': str(native_dir),
            'counts': {'run': 0, 'failures': 0, 'errors': 1, 'skips': 0},
            'failures': ['missing production packets: %s' % ', '.join(missing)],
            'errors': [],
            'skips': [],
            'methods': [],
            'excluded': list(EXCLUDED_GUILD_TESTS),
        }

    class FinalPlacementTests(guild_tests.PlacementTests):
        @classmethod
        def setUpClass(cls):
            cls.source = source_path
            cls.before = source
            cls.after = candidate
            cls.receipt = receipt

    methods = [
        name for name in unittest.TestLoader().getTestCaseNames(FinalPlacementTests)
        if name not in EXCLUDED_GUILD_TESTS
    ]
    missing_contracts = [name for name in REQUIRED_GUILD_TESTS if name not in methods]
    if missing_contracts:
        return {
            'executed': False,
            'nativeDirectory': str(native_dir),
            'counts': {'run': 0, 'failures': 0, 'errors': 1, 'skips': 0},
            'failures': ['missing required test methods: %s' % ', '.join(missing_contracts)],
            'errors': [],
            'skips': [],
            'methods': methods,
            'excluded': list(EXCLUDED_GUILD_TESTS),
        }

    suite = unittest.TestSuite([FinalPlacementTests(name) for name in methods])
    stream = io.StringIO()
    result = unittest.TextTestRunner(stream=stream, verbosity=2).run(suite)

    def details(rows):
        output = []
        for test, trace in rows:
            last = trace.strip().splitlines()[-1] if trace.strip() else ''
            output.append({'test': test.id(), 'detail': last})
        return output

    failures = details(result.failures)
    errors = details(result.errors)
    skips = details(result.skipped)
    gc.collect()
    counts = {
        'run': result.testsRun,
        'failures': len(result.failures),
        'errors': len(result.errors),
        'skips': len(result.skipped),
    }
    passed = (result.testsRun == len(methods) and not failures and
              not errors and not skips)
    return {
        'executed': True,
        'passed': passed,
        'nativeDirectory': str(native_dir),
        'nativePackets': {
            name: {
                'size': (native_dir / name).stat().st_size,
                'sha256': file_sha256(native_dir / name),
            }
            for name in NATIVE_PACKET_FILES
        },
        'counts': counts,
        'failures': failures,
        'errors': errors,
        'skips': skips,
        'methods': methods,
        'requiredMethods': list(REQUIRED_GUILD_TESTS),
        'excluded': list(EXCLUDED_GUILD_TESTS),
        'componentBoundaryTestExecuted': False,
    }


def pe_and_mapping_checks(guild_tests, pe, candidate_path, candidate):
    image = pe.Image.from_bytes(candidate)
    checksum_offset = image.optional + 64
    stored = struct.unpack_from('<I', candidate, checksum_offset)[0]
    computed = pe.pe_checksum(candidate, checksum_offset)
    checksum = {
        'offset': checksum_offset,
        'stored': stored,
        'computed': computed,
        'passed': stored == computed,
    }
    mapping = {'executed': False, 'passed': False}
    try:
        require(os.name == 'nt', 'Windows image mapping requires os.name == nt')
        guild_tests.windows_image(candidate_path)
        mapping.update({'executed': True, 'passed': True, 'readOnly': True,
                        'processStarted': False})
    except Exception as exc:
        mapping['error'] = error_message(exc)
    return checksum, mapping


def write_report(path, report):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x', encoding='utf8') as stream:
        stream.write(json.dumps(report, ensure_ascii=False, indent=2) + '\n')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--v177', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    source_path = args.v177.resolve()
    candidate_path = args.candidate.resolve()
    output_path = args.output.resolve()
    if len({source_path, candidate_path, output_path}) != 3:
        parser.error('--v177, --candidate, and --output must resolve to distinct paths')
    if output_path.exists():
        parser.error('--output already exists; refusing to overwrite')
    args.v177, args.candidate, args.output = source_path, candidate_path, output_path

    report = {
        'schema': 'title-guild-v177-verification.v1',
        'executed': False,
        'passed': False,
        'inputSha256': None,
        'candidateSha256': None,
        'inputSize': None,
        'candidateSize': None,
        'candidateEqualsBuild': False,
        'fullProducerExecuted': False,
        'fullProducerNote': 'Title remains a boundary/stubbed-leaf verification; the guild native suite uses its adapter lab.',
        'candidateExtensionBytes': None,
        'titleCaseCount': 0,
        'cases': [],
        'abi': {},
        'claims': {},
        'title': {'executed': False, 'caseCount': 0, 'failures': []},
        'guild': {'executed': False,
                  'counts': {'run': 0, 'failures': 0, 'errors': 1, 'skips': 0},
                  'failures': [], 'errors': [], 'skips': [], 'methods': []},
        'combine': {'passed': False},
        'diff': {},
        'components': {},
        'peChecksum': {'passed': False},
        'windowsImage': {'executed': False, 'passed': False, 'processStarted': False},
        'errors': [],
    }

    try:
        import build_title_guild_v177
        import pe_support
        import verify_boundary
        import test_guild_placement_v177 as guild_tests
    except (Exception, SystemExit) as exc:
        report['errors'].append('import: ' + error_message(exc))
        write_report(args.output, report)
        print(json.dumps({'passed': False, 'output': str(args.output)}))
        return 1

    report['candidateExtensionBytes'] = len(verify_boundary.title_extension(
        verify_boundary.OP19UI + verify_boundary.OLD_VIRTUAL_SIZE))
    report['abi'] = {
        'getter': hex(verify_boundary.GETTER),
        'getterCleanup': 0,
        'writer': hex(verify_boundary.WRITER_U8),
        'writerBytes': 1,
        'writerCleanup': 4,
        'appendLeaf': hex(verify_boundary.APPEND_LEAF),
        'appendLeafCleanup': 8,
    }

    source = candidate = None
    try:
        source = existing(args.v177)
        report['inputSha256'] = sha256(source)
        report['inputSize'] = len(source)
    except Exception as exc:
        report['errors'].append('input: ' + error_message(exc))

    try:
        candidate = existing(args.candidate)
        report['candidateSha256'] = sha256(candidate)
        report['candidateSize'] = len(candidate)
    except Exception as exc:
        report['errors'].append('candidate: ' + error_message(exc))

    receipt = None
    if source is not None:
        try:
            built, receipt = build_title_guild_v177.build(source)
            report['builtSha256'] = sha256(built)
            report['candidateEqualsBuild'] = candidate is not None and candidate == built
            if candidate is not None and not report['candidateEqualsBuild']:
                report['errors'].append('candidate differs from build_title_guild_v177.build(V177)')
            if receipt.get('inputSha256') != sha256(source):
                report['errors'].append('builder receipt inputSha256 differs')
            if receipt.get('outputSha256') != sha256(built):
                report['errors'].append('builder receipt outputSha256 differs')
        except (Exception, SystemExit) as exc:
            report['errors'].append('build: ' + error_message(exc))

    if receipt is not None and source is not None and candidate is not None:
        try:
            source_image = pe_support.Image.from_bytes(source)
            checksum_range = (source_image.optional + 64, source_image.optional + 68)
            combine = checked_composition(source, candidate, receipt, checksum_range)
            report['combine'] = combine
            report['diff'] = {key: combine[key] for key in
                              ('allowedRanges', 'changedBytes',
                               'outsideAllowedRanges', 'allOtherBytesUnchanged')}
            report['components'] = combine['components']
        except Exception as exc:
            report['combine'] = {'passed': False, 'error': error_message(exc)}
            report['errors'].append('compose-ranges: ' + error_message(exc))

    if source is not None and candidate is not None:
        try:
            rows, claims, failures = run_title_boundaries(
                verify_boundary, pe_support, source, candidate)
            report['cases'] = rows
            report['titleCaseCount'] = len(rows)
            report['claims'] = claims
            report['title'] = {
                'executed': True,
                'caseCount': len(rows),
                'passed': len(rows) == 17 and not failures,
                'failures': failures,
            }
            if len(rows) != 17:
                report['errors'].append('title boundary did not execute 17 cases')
            report['errors'].extend('title: ' + item for item in failures)
        except Exception as exc:
            report['title'] = {'executed': False, 'caseCount': 0,
                               'passed': False, 'failures': [error_message(exc)]}
            report['errors'].append('title: ' + error_message(exc))

        try:
            guild = run_guild_tests(guild_tests, args.v177, source, candidate, receipt)
            report['guild'] = guild
            if not guild.get('passed'):
                report['errors'].append('guild suite did not pass')
        except Exception as exc:
            report['guild'] = {
                'executed': False,
                'counts': {'run': 0, 'failures': 0, 'errors': 1, 'skips': 0},
                'failures': [error_message(exc)], 'errors': [], 'skips': [], 'methods': [],
            }
            report['errors'].append('guild: ' + error_message(exc))

        try:
            checksum, mapping = pe_and_mapping_checks(
                guild_tests, pe_support, args.candidate, candidate)
            report['peChecksum'] = checksum
            report['windowsImage'] = mapping
            if not checksum['passed']:
                report['errors'].append('final PE checksum mismatch')
            if not mapping['passed']:
                report['errors'].append('Windows image mapping failed')
        except Exception as exc:
            report['errors'].append('final-image: ' + error_message(exc))

    guild_counts = report.get('guild', {}).get('counts', {})
    title_passed = bool(report.get('title', {}).get('passed'))
    guild_passed = bool(report.get('guild', {}).get('passed'))
    report['executed'] = bool(
        source is not None and candidate is not None and receipt is not None and
        report.get('title', {}).get('executed') and
        report.get('guild', {}).get('executed') and
        report.get('windowsImage', {}).get('executed')
    )
    report['passed'] = bool(
        report['candidateEqualsBuild'] and
        report.get('combine', {}).get('passed') and
        title_passed and
        guild_passed and
        guild_counts.get('skips') == 0 and
        report.get('peChecksum', {}).get('passed') and
        report.get('windowsImage', {}).get('passed') and
        not report['errors']
    )
    write_report(args.output, report)
    print(json.dumps({
        'passed': report['passed'],
        'titleCases': report['title'].get('caseCount'),
        'guild': report['guild'].get('counts'),
        'changedBytes': report.get('combine', {}).get('changedBytes'),
    }))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
