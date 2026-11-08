"""Rebuild the reviewed arena/drop PVF without publishing or overwriting it."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parent


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('base', 'tool', 'output'):
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    manifest = json.loads((ROOT / 'manifest.json').read_text('utf8'))
    plan_path = ROOT / 'resource.schema2.json'
    plan = json.loads(plan_path.read_text('utf8'))
    if (digest(args.base) != manifest['baseline']['sha256'] or
            args.base.stat().st_size != manifest['baseline']['bytes'] or
            digest(args.tool) != manifest['toolSHA256'] or
            digest(plan_path) != manifest['planSHA256'] or
            plan['input_sha256'] != manifest['baseline']['sha256'] or plan['schema'] != 2 or
            sorted(e['path'] for e in plan['edits']) != sorted(manifest['changedPaths'])):
        raise ValueError('input/tool/plan differs from reviewed identity')
    args.output.mkdir(parents=True, exist_ok=False)
    candidate = args.output / 'Script.pvf'
    for operation in ('patch', 'verify'):
        result = subprocess.run([str(args.tool), operation, '--pvf', str(args.base),
                                 '--plan', str(plan_path), '--out', str(candidate)],
                                capture_output=True, timeout=600,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        (args.output / (operation + '.stdout')).write_bytes(result.stdout)
        (args.output / (operation + '.stderr')).write_bytes(result.stderr)
        if result.returncode:
            raise RuntimeError('preserve failed ' + operation + ' evidence')
        receipt = json.loads(result.stdout)
        if (receipt['input_sha256'] != manifest['baseline']['sha256'] or
                receipt['changed_entries'] != len(manifest['changedPaths']) or
                receipt['unchanged_entries_verified'] != manifest['archiveFiles'] - len(manifest['changedPaths'])):
            raise ValueError('archive preservation mismatch')
    if dict(sha256=digest(candidate), bytes=candidate.stat().st_size) != manifest['expected_candidate']:
        raise ValueError('candidate identity mismatch')
    (args.output / 'result.json').write_text(json.dumps(dict(passed=True, receipt=receipt,
        gameAcceptance=False, deployed=False), indent=2), 'utf8')


if __name__ == '__main__':
    main()
