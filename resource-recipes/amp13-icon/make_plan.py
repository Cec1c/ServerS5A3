"""Read six current item definitions and prepare an icon-only PVF plan; no publication or indexes."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent

def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', required=True)
    parser.add_argument('--base-sha256', required=True)
    parser.add_argument('--tool', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    manifest = json.loads((HERE/'manifest.json').read_text('utf8'))
    if sha(args.base) != args.base_sha256 or sha(args.tool) != manifest['toolSHA256']:
        raise ValueError('PVF/tool identity mismatch')
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    env = os.environ.copy()
    env.update(GOMAXPROCS='2', GOMEMLIMIT='1GiB', GOGC='20')
    edits = []
    for item in manifest['items']:
        path = f'stackable/private_seria_luck/{item}.stk'
        target = out/f'{item}.tokens.json'
        q = subprocess.run([args.tool, 'read', '--pvf', args.base, '--path', path,
                            '--format', 'tokens', '--out', str(target), '--json'],
                           capture_output=True, env=env, timeout=120,
                           creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        (out/f'{item}.stdout').write_bytes(q.stdout)
        (out/f'{item}.stderr').write_bytes(q.stderr)
        if q.returncode:
            raise RuntimeError(f'PVF read failed for {item}; evidence preserved')
        doc = json.loads(target.read_text('utf8'))
        ts = doc['tokens']
        positions = [i for i,t in enumerate(ts) if t.get('text') == '[icon]']
        if len(positions) != 1:
            raise ValueError(f'{item}: icon section count changed')
        i = positions[0]
        if (i+2 >= len(ts) or ts[i+1].get('text', '').lower() != 'item/stackable/consumption.img'
                or ts[i+2]['payload_u32'] != 844):
            raise ValueError(f'{item}: expected shared +12 icon no longer present; review cumulative changes')
        tokens = [dict(type=t['type'], payload_u32=t['payload_u32']) for t in ts]
        tokens[i+1] = dict(type=6, text=manifest['icon'])
        tokens[i+2] = dict(type=0, payload_u32=manifest['frame'])
        edits.append(dict(path=path, data_type=1, before_sha256=doc['raw_sha256'], tokens=tokens))
    plan = dict(schema=2, input_sha256=args.base_sha256, edits=edits)
    (out/'amp13-icons.schema2.json').write_text(json.dumps(plan, ensure_ascii=False, indent=2), 'utf8')
    print(json.dumps(dict(inputSHA256=args.base_sha256, edits=len(edits), plan=str(out/'amp13-icons.schema2.json'), pvfWritten=False)))

if __name__ == '__main__':
    main()
