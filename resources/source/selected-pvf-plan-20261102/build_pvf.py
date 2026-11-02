"""Rebuild the cumulative private PVF from reviewed, hash-pinned plans.

This command only creates local candidate files. It has no publication or
server mutation functions. Run from a verified clean authoritative checkout.
"""
from pathlib import Path
import argparse
import hashlib
import json
import os
import subprocess
import zipfile


def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', required=True)
    parser.add_argument('--addition-tool', required=True)
    parser.add_argument('--patch-tool', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    manifest = json.loads((root / 'manifest.json').read_text('utf8'))
    for path, key in ((args.base, 'basePVFSHA256'),
                      (args.addition_tool, 'additionToolSHA256'),
                      (args.patch_tool, 'patchToolSHA256'),
                      (root / 'pvf-plans.zip', 'planArchiveSHA256')):
        if sha(path) != manifest[key]:
            raise ValueError('input hash mismatch: ' + key)
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with zipfile.ZipFile(root / 'pvf-plans.zip') as archive:
        if len(archive.namelist()) != len(manifest['plans']) or set(archive.namelist()) != set(manifest['plans']):
            raise ValueError('unexpected plan archive members')
        for name, expected in manifest['plans'].items():
            if Path(name).name != name:
                raise ValueError('plan path must be a simple filename')
            entry = archive.getinfo(name)
            if entry.file_size != expected['bytes'] or entry.file_size >= 16 * 1024 * 1024:
                raise ValueError('plan exceeds bounded size: ' + name)
            data = archive.read(name)
            if len(data) != expected['bytes'] or hashlib.sha256(data).hexdigest() != expected['sha256']:
                raise ValueError('plan digest mismatch: ' + name)
            (output / name).write_bytes(data)
    env = os.environ.copy()
    env.update(GOMAXPROCS='2', GOMEMLIMIT='1GiB', GOGC='20')

    def run(label, command):
        result = subprocess.run(command, env=env, capture_output=True, timeout=300,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        (output / (label + '.stdout')).write_bytes(result.stdout)
        (output / (label + '.stderr')).write_bytes(result.stderr)
        if result.returncode:
            raise RuntimeError(label + ' failed; inspect local receipts')

    source = output / 'seed.pvf'
    run('additions', [args.addition_tool, '-pvf', args.base,
                     '-plan', str(output / 'additions-plan.json'), '-out', str(source),
                     '-report', str(output / 'additions-verify.json')])
    additions = json.loads((output / 'additions-verify.json').read_text('utf8'))
    if additions.get('new_tokens_verified') is not True or additions.get('changed_paths'):
        raise ValueError('addition preservation verification failed')
    for index in (1, 2, 3):
        plan = output / ('bounded-plan-%d.json' % index)
        expected = json.loads(plan.read_text('utf8'))
        if sha(source) != expected['input_sha256']:
            raise ValueError('intermediate input digest mismatch')
        target = output / ('Script.pvf' if index == 3 else 'step-%d.pvf' % index)
        # PVF-CLI verify compares original --pvf against patched --out using
        # the approved plan. It deliberately receives the same original as patch.
        for operation in ('patch', 'verify'):
            run('step-%d-%s' % (index, operation),
                [args.patch_tool, operation, '--pvf', str(source), '--plan', str(plan), '--out', str(target)])
        source = target
    digest = sha(source)
    if digest != manifest['expectedPVFSHA256']:
        raise ValueError('final cumulative PVF digest mismatch')
    result = dict(passed=True, outputSHA256=digest, deployed=False)
    (output / 'result.json').write_text(json.dumps(result, indent=2), 'utf8')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
