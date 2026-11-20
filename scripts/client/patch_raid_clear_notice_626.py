"""核对或制作下行 class0/626 团本通关黄字的客户端候选。省略 --output 时只在内存核对。"""

import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe


INPUT_SHA256 = '4a8c99f55dea49dfcd6c58717b9bfec3fd248d5f152b683b9cfdeb98fbefe056'
INPUT_SIZE = 113893376
NOTICE = 626
CALL_VA = 0x02523A13
PREVIOUS_GATE = 0x06EEE000
HELPER_VA = PREVIOUS_GATE + 0x40
PREVIOUS_CODE = bytes.fromhex('803f000f8537fdfbff0fb74424043d570300000f8527fdfbffb801000000c20400')
REGISTRATION_VA = 0x0203261D
REGISTRATION = bytes.fromhex('6a0068d0e9000268720200008bcee8701c4f00')


def relative_call(target):
    return b'\xe8' + struct.pack('<i', target - (CALL_VA + 5))


def helper():
    # 仅通知类的 626 直接放行。其余输入尾调用原接收链，保持现有分类、位图及栈约定。
    code = bytearray()
    for check in (bytes.fromhex('803f00'), bytes.fromhex('0fb74424043d') + struct.pack('<I', NOTICE)):
        code.extend(check)
        code.extend(b'\x0f\x85')
        code.extend(struct.pack('<i', PREVIOUS_GATE - (HELPER_VA + len(code) + 4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('客户端摘要或长度与批准输入不一致')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.wk855\0\0']
    if len(matches) != 1 or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('客户端接收节或证书目录与核对结果不一致')
    header = matches[0]
    size, rva, raw_size, raw = struct.unpack_from('<IIII', data, header + 8)
    flags = struct.unpack_from('<I', data, header + 36)[0]
    code = helper()
    helper_offset = HELPER_VA - PREVIOUS_GATE
    if (rva + pe.IMAGE_BASE != PREVIOUS_GATE or flags != 0x60000020
            or size != 0x21 or raw_size != 0x1000 or not size <= helper_offset <= raw_size - len(code)
            or data[raw:raw+len(PREVIOUS_CODE)] != PREVIOUS_CODE or any(data[raw+len(PREVIOUS_CODE):raw+raw_size])):
        raise ValueError('既有接收链或节内空余范围与核对结果不一致')

    call = pe.offset(sections, CALL_VA, 5)
    registration = pe.offset(sections, REGISTRATION_VA, len(REGISTRATION))
    if data[call:call+5] != relative_call(PREVIOUS_GATE) or data[registration:registration+len(REGISTRATION)] != REGISTRATION:
        raise ValueError('客户端接收入口或原版 626 注册已变化')

    output = bytearray(data)
    new_gate = raw + helper_offset
    output[new_gate:new_gate+len(code)] = code
    output[call:call+5] = relative_call(HELPER_VA)
    struct.pack_into('<I', output, header + 8, helper_offset + len(code))
    struct.pack_into('<I', output, opt + 64, pe.checksum(output, opt + 64))
    result = bytes(output)
    ranges = sorted([(call, call+5), (new_gate, new_gate+len(code)), (header+8, header+12), (opt+64, opt+68)])
    return result, dict(输入SHA256=INPUT_SHA256, 候选SHA256=pe.sha(result), 允许分类=0, 允许编号=NOTICE,
                        合法差异范围=ranges, 原版处理函数='0x0200E9D0')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True, help='固定版本的 DNF.exe')
    parser.add_argument('--output', type=Path, help='独立且尚不存在的候选绝对或相对路径')
    args = parser.parse_args()
    try:
        source = args.input.resolve(strict=True)
        destination = args.output.resolve() if args.output else None
        if destination is not None and (destination == source or destination.exists()):
            raise ValueError('请选择尚不存在的独立输出文件')
        result, receipt = build(source.read_bytes())
        if destination is not None:
            with destination.open('xb') as stream:
                stream.write(result)
            if pe.sha(destination.read_bytes()) != receipt['候选SHA256']:
                raise RuntimeError('候选文件读回校验失败')
            receipt['候选文件'] = str(destination)
        receipt['仅内存核对'] = destination is None
    except (OSError, ValueError, RuntimeError) as exc:
        parser.exit(1, f'核对失败：{exc}\n')
    print(json.dumps(receipt, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
