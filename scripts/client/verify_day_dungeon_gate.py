"""只读比对周常855补丁前后的文件及完整接收许可。"""
import argparse
import json
from pathlib import Path

from patch_day_dungeon_gate import verify


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--candidate', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args = parser.parse_args()
    paths = [p.resolve() for p in (args.input, args.candidate, args.receipt)]
    if len(set(paths)) != 3 or paths[2].exists():
        parser.error('输入、候选和新回执必须是不同路径；不得覆盖回执')
    proof = verify(paths[0].read_bytes(), paths[1].read_bytes())
    paths[2].parent.mkdir(parents=True, exist_ok=True)
    with paths[2].open('x', encoding='utf-8') as stream:
        json.dump(proof, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps(proof, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
