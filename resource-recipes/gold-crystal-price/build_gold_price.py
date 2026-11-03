"""Create and verify a local PVF candidate; no publishing or server changes."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import struct
import subprocess

HERE = Path(__file__).resolve().parent


def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', required=True)
    parser.add_argument('--tool', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--verify-existing', action='store_true')
    args = parser.parse_args()
    manifest = json.loads((HERE / 'manifest.json').read_text('utf8'))
    plan_path = HERE / 'gold-price.schema1.json'
    plan = json.loads(plan_path.read_text('utf8'))
    if (sha(args.base) != manifest['baselineSHA256'] or
            sha(args.tool) != manifest['toolSHA256'] or
            sha(plan_path) != manifest['planSHA256']):
        raise ValueError('input identity mismatch')
    if len(plan['edits']) != 1 or plan['edits'][0]['path'] != manifest['path']:
        raise ValueError('unexpected edit scope')
    out = Path(args.output).resolve()
    if not args.verify_existing:
        out.mkdir(parents=True, exist_ok=False)
    candidate = out / 'Script.pvf'

    def run(label, arguments):
        if any((out / (label + suffix)).exists() for suffix in ('.stdout', '.stderr')):
            raise ValueError('existing operation evidence must be preserved: ' + label)
        result = subprocess.run([args.tool, *arguments], capture_output=True,
                                timeout=600, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        with (out / (label + '.stdout')).open('xb') as stream:
            stream.write(result.stdout)
        with (out / (label + '.stderr')).open('xb') as stream:
            stream.write(result.stderr)
        if result.returncode:
            raise RuntimeError(label + ' failed; inspect preserved log')
        return json.loads(result.stdout) if result.stdout.strip() else None

    def tokens(label, pvf):
        target = out / (label + '.tokens.json')
        run(label, ['read', '--pvf', str(pvf), '--path', manifest['path'],
                    '--format', 'tokens', '--out', str(target), '--json'])
        return json.loads(target.read_text('utf8'))

    if args.verify_existing:
        before = json.loads((out / 'before.tokens.json').read_text('utf8'))
        patch = json.loads((out / 'patch.stdout').read_text('utf8'))
        if sha(candidate) != patch['output_sha256']:
            raise ValueError('existing candidate differs from patch receipt')
    else:
        before = tokens('before', args.base)
        patch = run('patch', ['patch', '--pvf', args.base, '--plan', str(plan_path), '--out', str(candidate)])
    verify = run('verify-existing' if args.verify_existing else 'verify', ['verify', '--pvf', args.base, '--plan', str(plan_path), '--out', str(candidate)])
    after = tokens('after', candidate)
    index = manifest['insertAfterToken'] + 1
    old = before['tokens']
    new = after['tokens']
    old_raw = b''.join(struct.pack('<BI', t['type'], t['payload_u32']) for t in old)
    if hashlib.sha256(old_raw).hexdigest() != manifest['beforeRawSHA256']:
        raise ValueError('baseline token evidence changed')
    if (before['raw_sha256'] != manifest['beforeRawSHA256'] or
            after['raw_sha256'] != manifest['afterRawSHA256'] or
            len(old) != manifest['originalTokenCount'] or len(new) != len(old) + 2 or
            new[:index] + new[index + 2:] != old):
        raise ValueError('non-price token changed')
    if (new[index].get('text') != '[price]' or new[index]['type'] != 3 or
            new[index + 1]['type'] != 0 or new[index + 1]['payload_u32'] != 400):
        raise ValueError('price addition mismatch')
    values = [i for i, t in enumerate(new) if t.get('text') == '[value]']
    prices = [i for i, t in enumerate(new) if t.get('text') == '[price]']
    if len(values) != 1 or len(prices) != 1 or new[values[0] + 1]['payload_u32'] != 400:
        raise ValueError('existing value changed or duplicate price')
    raw = b''.join(struct.pack('<BI', t['type'], t['payload_u32']) for t in new)
    if raw != base64.b64decode(plan['edits'][0]['raw_base64']):
        raise ValueError('candidate does not match frozen plan')
    digest = sha(candidate)
    for receipt in (patch, verify):
        if (receipt['input_sha256'] != manifest['baselineSHA256'] or
                receipt['output_sha256'] != digest or receipt['changed_entries'] != 1 or
                receipt['unchanged_entries_verified'] < 1):
            raise ValueError('archive preservation receipt mismatch')
    result = dict(passed=True, patchReused=args.verify_existing, candidateSHA256=digest, bytes=candidate.stat().st_size,
                  changedEntries=1, addedTokens=2, preservedTokens=len(old),
                  unchangedEntriesVerified=verify['unchanged_entries_verified'],
                  price=400, value=400, indexesBuilt=False, published=False)
    (out / 'result.json').write_text(json.dumps(result, indent=2) + '\n', 'utf8')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
