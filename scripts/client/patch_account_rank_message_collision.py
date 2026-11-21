"""修复账号排行榜与战令通知共用窗口消息编号的问题。

使用 --input 指定已核对的 DNF.exe。省略 --output 时只做核对，指定
--output 时生成独立候选。输入必须为包含原统计和战令模块的确切版本。
"""

import argparse
import json
from pathlib import Path

import patch_receivers_v128 as pe


INPUT_SHA256 = '9c32750d08d84ab0f079b3e7bf10c32e25b691c042bef9658c495d7b59d5a567'
OUTPUT_SHA256 = '08899023ac030589aaa711adbf5d9695868eede26700146dbeccb2d5773b96b8'
INPUT_SIZE = 113696768
SITES = (
    # 地址、原指令、替换指令。战令通知独占 300/301，排行榜保留 200/201。
    (0x06EF5BE2, '3dc9000000', '3d2d010000'),  # 重置比较
    (0x06EF5C41, '3dc8000000', '3d2c010000'),  # 结果比较
    (0x06EF6531, '68c9000000', '682d010000'),  # 重置发送
    (0x06EF8E89, '68c8000000', '682c010000'),  # 结果发送
)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('输入长度或 SHA256 不匹配，请按目标客户端版本复核源码和指令')

    _, sections = pe.headers(data)
    candidate = bytearray(data)
    changes = []
    for address, original_hex, replacement_hex in SITES:
        original = bytes.fromhex(original_hex)
        replacement = bytes.fromhex(replacement_hex)
        offset = pe.offset(sections, address, len(original))
        if data[offset:offset + len(original)] != original:
            raise ValueError(f'原指令不匹配，地址 {address:#010x}')
        candidate[offset:offset + len(original)] = replacement
        changes.append({'va': f'{address:#010x}', 'offset': offset,
                        'before': original_hex, 'after': replacement_hex})

    if len(candidate) != len(data) or pe.sha(candidate) != OUTPUT_SHA256:
        raise ValueError('候选长度或 SHA256 不匹配')
    return candidate, {'inputSha256': INPUT_SHA256, 'outputSha256': OUTPUT_SHA256,
                       'size': len(candidate), 'changes': changes}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path, help='待核对的 DNF.exe')
    parser.add_argument('--output', type=Path, help='尚不存在的候选文件')
    args = parser.parse_args()
    try:
        source = args.input.resolve(strict=True)
        target = args.output.resolve() if args.output else None
        if target is not None and (target == source or target.exists()):
            raise ValueError('输出必须是独立的新文件')
        candidate, receipt = build(source.read_bytes())
        if target is not None:
            if pe.sha(source.read_bytes()) != INPUT_SHA256:
                raise ValueError('候选生成期间输入文件发生变化')
            with target.open('xb') as stream:
                stream.write(candidate)
            if pe.sha(target.read_bytes()) != OUTPUT_SHA256:
                raise ValueError('候选读回 SHA256 不匹配')
        receipt['wroteCandidate'] = target is not None
        print(json.dumps(receipt, indent=2))
    except (OSError, ValueError) as exc:
        parser.exit(1, f'{exc}\n')


if __name__ == '__main__':
    main()
