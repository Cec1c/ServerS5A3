"""Build one exact cumulative PVF candidate; never publish or overwrite outputs."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('base', 'tool', 'output'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    manifest = json.loads((HERE / 'manifest.json').read_text('utf8'))
    plan_path = HERE / 'resource.schema1.json'
    plan = json.loads(plan_path.read_text('utf8'))
    if (digest(args.base) != manifest['baseline']['sha256'] or
            args.base.stat().st_size != manifest['baseline']['bytes'] or
            digest(args.tool) != manifest['toolSHA256'] or
            digest(plan_path) != manifest['planSHA256'] or
            plan['schema'] != 1 or plan['input_sha256'] != manifest['baseline']['sha256']):
        raise ValueError('reviewed input/tool/plan identity required')
    entries = {e['path']: e for e in plan['edits']}
    if len(entries) != len(plan['edits']) or set(entries) != set(manifest['changedPaths']):
        raise ValueError('unexpected changed entries')
    for entry in entries.values():
        raw = base64.b64decode(entry['raw_base64'], validate=True)
        if entry['data_type'] != 1 or len(raw) % 5 or hashlib.sha256(raw).hexdigest() != entry['after_sha256']:
            raise ValueError('invalid frozen token document')
    if args.output.resolve() in (args.base.resolve(), args.tool.resolve(), HERE):
        raise ValueError('separate candidate directory required')
    args.output.mkdir(parents=True, exist_ok=False)
    candidate = args.output / 'Script.pvf'
    receipts = []
    for operation in ('patch', 'verify'):
        command = [str(args.tool), operation, '--pvf', str(args.base), '--plan', str(plan_path), '--out', str(candidate)]
        result = subprocess.run(command, capture_output=True, timeout=600,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        for suffix, data in (('stdout', result.stdout), ('stderr', result.stderr)):
            with (args.output / (operation + '.' + suffix)).open('xb') as stream:
                stream.write(data)
        if result.returncode:
            raise RuntimeError(operation + ' failed; preserve output and reconcile before retry')
        receipt = json.loads(result.stdout)
        if (receipt['input_sha256'] != manifest['baseline']['sha256'] or
                receipt['output_sha256'] != digest(candidate) or
                receipt['changed_entries'] != len(entries) or
                receipt['unchanged_entries_verified'] != manifest['archiveFiles'] - len(entries)):
            raise ValueError('whole-archive preservation mismatch')
        receipts.append(receipt)
    report = dict(passed=True, candidateSHA256=digest(candidate), bytes=candidate.stat().st_size,
                  changedPaths=sorted(entries), unchangedEntriesVerified=receipts[-1]['unchanged_entries_verified'],
                  wholeArchiveVerified=True, indexesGenerated=False, deployed=False, gameAcceptance=False)
    with (args.output / 'result.json').open('x', encoding='utf8') as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
    print(json.dumps(report))


if __name__ == '__main__':
    main()
