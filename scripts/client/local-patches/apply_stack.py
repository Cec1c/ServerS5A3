"""按 stack.json 的顺序把我方本地补丁打到一个 DNF.exe 上。

    # 只检查：新上游 EXE 到手后先跑这个，看每个补丁能否自动套上
    python -B apply_stack.py --input 上游.exe --check

    # 生成：不覆盖输入，输出必须是新文件
    python -B apply_stack.py --input 上游.exe --output DNF.exe

每个补丁报告三种状态之一：
    absent        未打，特征完整，可以自动应用
    applied       输入里已经有了（例如输入本身就是我方旧版本），跳过
    incompatible  特征对不上（上游改了相关代码），必须人工移植，整体失败

生成时只允许补丁声明过的字节和 PE 校验和发生变化，最后与 stack.json 的
releases 比对：命中则报告版本号，否则提示把新版本登记进去。
"""
import argparse
import importlib
import json
import sys
from pathlib import Path

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
sys.path[:0] = [str(HERE), str(HERE / 'patches')]

from pe_image import Image, pe_checksum, sha256  # noqa: E402


def load_stack():
    stack = json.loads((HERE / 'stack.json').read_text(encoding='utf-8'))
    for entry in stack['patches']:
        module = importlib.import_module(entry['module'])
        if module.ID != entry['id']:
            raise ValueError(f"stack.json 的 {entry['id']} 与模块 ID {module.ID} 不一致")
        entry['impl'] = module
    return stack


def release_of(stack, digest):
    return next((r for r in stack['releases'] if r['outputSHA256'] == digest), None)


def identify(stack, digest):
    """输入文件是哪个已登记版本或上游原版。"""
    release = release_of(stack, digest)
    if release:
        return release['version']
    upstream = next((r for r in stack['releases'] if r.get('upstreamSHA256') == digest), None)
    return f"上游 {upstream['upstream']}" if upstream else None


def run(data, stack, only=None, check=False):
    """返回 (输出字节或 None, 报告)。check=True 时只报告状态，不应用。"""
    out = bytearray(data)
    report, changes, failed = [], [], False
    for entry in stack['patches']:
        patch = entry['impl']
        if only and patch.ID not in only:
            report.append(dict(id=patch.ID, status='skipped'))
            continue
        status, info = patch.inspect(Image(out))
        row = dict(id=patch.ID, summary=patch.SUMMARY, status=status, **info)
        if status == 'incompatible':
            failed = True
        elif status == 'absent' and not check:
            ranges = patch.apply(out)
            after, info = patch.inspect(Image(out))
            if after != 'applied':
                raise RuntimeError(f'{patch.ID} 应用后自检为 {after}')
            row.update(status='applied-now', changes=[[hex(a), hex(b), why] for a, b, why in ranges])
            changes += ranges
        report.append(row)
    if failed:
        return None, report
    if check:
        return bytes(data), report
    img = Image(out)
    out[img.checksum_at:img.checksum_at + 4] = pe_checksum(out, img.checksum_at).to_bytes(4, 'little')
    allowed = sorted([(a, b) for a, b, _ in changes] + [(img.checksum_at, img.checksum_at + 4)])
    cursor = 0
    for lo, hi in allowed:
        if out[cursor:lo] != data[cursor:lo]:
            raise RuntimeError(f'{cursor:#x}..{lo:#x} 出现未声明的改动')
        cursor = max(cursor, hi)
    if out[cursor:] != data[cursor:] or len(out) != len(data):
        raise RuntimeError('文件尾部出现未声明的改动')
    return bytes(out), report


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--check', action='store_true', help='只检查每个补丁的状态，不生成文件')
    parser.add_argument('--only', help='只应用这些补丁（逗号分隔的 id），其余跳过')
    args = parser.parse_args()
    if not args.check and not args.output:
        parser.error('需要 --output，或者用 --check 只检查')

    stack = load_stack()
    data = args.input.read_bytes()
    result, report = run(data, stack, set(args.only.split(',')) if args.only else None, args.check)
    summary = dict(input=str(args.input), inputSHA256=sha256(data), size=len(data),
                   inputRelease=identify(stack, sha256(data)), patches=report)
    if result is None:
        summary['result'] = '失败：有补丁需要人工移植（status=incompatible）'
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 2
    if args.check:
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 0
    digest = sha256(result)
    if digest == summary['inputSHA256']:
        summary['result'] = '输入已包含全部补丁，没有生成文件'
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 0
    with args.output.open('xb') as stream:
        stream.write(result)
    if sha256(args.output.read_bytes()) != digest or sha256(args.input.read_bytes()) != summary['inputSHA256']:
        raise RuntimeError('写出后摘要不一致')
    release = release_of(stack, digest)
    summary.update(output=str(args.output), outputSHA256=digest,
                   outputRelease=release['version'] if release else None,
                   result='已生成' if release else '已生成；这是未登记的新组合，确认发布前请登记到 stack.json 的 releases')
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == '__main__':
    sys.exit(main())
