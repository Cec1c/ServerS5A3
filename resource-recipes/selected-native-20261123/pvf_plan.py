"""Public cumulative PVF: Fiend War 0.8% weapon reward and guild 5000 shelves."""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

BASE = 'f86090d9f39dc558a397062f5a794496343c1dd7e04a2443b982733c2c6418ee'
FIENDWAR = 'etc/raid/fiendwar/fiendwar.etc'
GUILD = 'etc/(r)guild.etc'
WEIGHTS = {490008038: (702, 3744), 490008672: (90, 450),
           490008039: (54, 36), 490008040: (27, 135),
           490008818: (27, 135), 990027103: (100, 500)}


def changes(path, original):
    tokens = [{k: t[k] for k in ('type', 'text' if 'text' in t else 'payload_u32')}
              for t in original]
    if path == FIENDWAR:
        found = set()
        for i, token in enumerate(tokens):
            if token.get('text') != 'squad_item':
                continue
            item = tokens[i + 3].get('payload_u32')
            if item not in WEIGHTS or item in found:
                raise ValueError('unexpected Fiend War personal reward pool')
            before, after = WEIGHTS[item]
            if tokens[i + 2] != dict(type=0, payload_u32=before):
                raise ValueError('unexpected original reward weight')
            tokens[i + 2]['payload_u32'] = after
            found.add(item)
        if found != set(WEIGHTS):
            raise ValueError('incomplete reward pool')
    elif path == GUILD:
        for end, values in (
            ('[/guild contents list]', [19, 6, 5000, -1, 0, 10153533, 0, 2000000, 'new']),
            ('[/additional guild contents list]', [27, 21, 5000, 1, 0, 490005461, 0, 10000000, '', 1]),
        ):
            indexes = [i for i, t in enumerate(tokens) if t.get('text') == end]
            if len(indexes) != 1:
                raise ValueError('unexpected guild section')
            row = [dict(type=6, text=v) if isinstance(v, str)
                   else dict(type=0, payload_u32=v & 0xffffffff) for v in values]
            tokens[indexes[0]:indexes[0]] = row
    else:
        raise ValueError('unexpected path')
    return tokens


def plan(cli, pvf):
    with pvf.open('rb') as stream:
        if hashlib.file_digest(stream, 'sha256').hexdigest() != BASE:
            raise ValueError('expected selected-gameplay-20261122 cumulative public PVF')
    edits = []
    for path in (FIENDWAR, GUILD):
        doc = json.loads(subprocess.check_output([
            cli, 'read', '--pvf', str(pvf), '--path', path, '--format', 'tokens', '--json']))
        edits.append(dict(path=path, data_type=1, before_sha256=doc['raw_sha256'],
                          tokens=changes(path, doc['tokens'])))
    return dict(schema=2, input_sha256=BASE, edits=edits)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pvf-cli', default='pvf')
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args()
    result = plan(args.pvf_cli, args.input)
    with args.out.open('x', encoding='utf8') as stream:
        json.dump(result, stream, ensure_ascii=False)
