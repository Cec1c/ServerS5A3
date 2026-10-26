"""为固定V163开放class1/op903；只生成独立候选，不安装或发布。"""
from __future__ import annotations

import argparse
from array import array
import hashlib
import json
from pathlib import Path
import struct
import sys

INPUT_SHA256 = 'b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902'
INPUT_SIZE = 111894528
CALL = 0x02523A13
OLD_GATE = 0x06EAD000
NEW_GATE = 0x06EAD080
OLD_GATE_BYTES = bytes.fromhex(
    '0fb7442404803f000f853c0000003d670000000f84550000003d070300000f844a000000'
    '3dfe0400000f843f0000003d570500000f84340000003d580500000f8429000000'
    'e91f000000803f010f85160000003d430300000f84100000003d600300000f8405000000'
    'e992cfffffb801000000c20400')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def pe_fields(data):
    require(data[:2] == b'MZ', '缺少MZ头')
    pe = struct.unpack_from('<I', data, 60)[0]
    require(data[pe:pe+4] == b'PE\0\0', '缺少PE头')
    require(struct.unpack_from('<H', data, pe+4)[0] == 0x14c, '仅支持固定x86版本')
    optional = pe + 24
    require(struct.unpack_from('<H', data, optional)[0] == 0x10b, '仅支持PE32')
    base = struct.unpack_from('<I', data, optional+28)[0]
    table = optional + struct.unpack_from('<H', data, pe+20)[0]
    sections = []
    for i in range(struct.unpack_from('<H', data, pe+6)[0]):
        row = table + i*40
        vs, rva, size, raw = struct.unpack_from('<IIII', data, row+8)
        sections.append((row, base+rva, vs, size, raw))
    return optional, base, sections


def file_offset(data, address, length):
    _, _, sections = pe_fields(data)
    for row, start, _, size, raw in sections:
        if start <= address and address+length <= start+size:
            require(struct.unpack_from('<I', data, row+36)[0] & 0x20000000, '目标节不可执行')
            require(raw+address-start+length <= len(data), '目标超出文件')
            return raw+address-start
    raise ValueError('目标地址不在原始节内')


def pe_checksum(data):
    optional, _, _ = pe_fields(data)
    words = array('H')
    words.frombytes(data)
    if sys.byteorder != 'little':
        words.byteswap()
    index = (optional+64)//2
    words[index] = words[index+1] = 0
    total = sum(words)
    while total >> 16:
        total = (total & 0xffff)+(total >> 16)
    return total+len(data)


def wrapper_code():
    code = bytearray.fromhex('0fb7442404803f01')
    for instruction in (b'', b'\x3d'+struct.pack('<I', 903)):
        code.extend(instruction)
        code.extend(b'\x0f\x85')
        code.extend(struct.pack('<i', OLD_GATE-(NEW_GATE+len(code)+4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    require(len(code) == 33, '接收封装长度异常')
    return bytes(code)


def checked_input(path):
    data = Path(path).read_bytes()
    require(len(data) == INPUT_SIZE and digest(data) == INPUT_SHA256,
            '输入必须是固定V163完整摘要；其他版本或重复补丁均拒绝')
    optional, base, sections = pe_fields(data)
    require(base == 0x400000 and len(sections) == 44, 'V163节表不符')
    require(struct.unpack_from('<I', data, optional+64)[0] == pe_checksum(data), '输入PE校验和错误')
    gate = file_offset(data, OLD_GATE, len(OLD_GATE_BYTES))
    require(data[gate:gate+len(OLD_GATE_BYTES)] == OLD_GATE_BYTES, '原接收门指令不符')
    call = file_offset(data, CALL, 5)
    require(data[call:call+5] == b'\xe8'+struct.pack('<i', OLD_GATE-CALL-5), '原接收调用不符')
    return data


def build_bytes(data):
    require(len(data) == INPUT_SIZE and digest(data) == INPUT_SHA256, '构建输入摘要不符')
    optional, _, sections = pe_fields(data)
    code = wrapper_code()
    pos = file_offset(data, NEW_GATE, len(code))
    require(data[pos:pos+len(code)] == bytes(len(code)), '接收门尾部空间已被使用')
    row, start, vs, raw_size, _ = next(s for s in sections if s[1] <= NEW_GATE < s[1]+s[3])
    new_vs = NEW_GATE-start+len(code)
    require(vs == 0x76 and new_vs == 0xa1 and new_vs <= raw_size, '接收门节布局不符')
    call = file_offset(data, CALL, 5)
    result = bytearray(data)
    result[pos:pos+len(code)] = code
    result[call:call+5] = b'\xe8'+struct.pack('<i', NEW_GATE-CALL-5)
    struct.pack_into('<I', result, row+8, new_vs)
    struct.pack_into('<I', result, optional+64, pe_checksum(result))
    windows = sorted([(optional+64, optional+68), (row+8, row+12), (call, call+5), (pos, pos+len(code))])
    cursor = 0
    for lower, upper in windows:
        require(data[cursor:lower] == result[cursor:lower], '出现允许区间以外的差异')
        cursor = upper
    require(data[cursor:] == result[cursor:] and len(result) == len(data), '文件尾部或长度变化')
    return bytes(result), windows


def main():
    if sys.flags.optimize:
        raise RuntimeError('拒绝-O或优化环境')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--out-dir', required=True, type=Path)
    args = parser.parse_args()
    require(not args.out_dir.exists(), '输出目录已存在，拒绝覆盖')
    data = checked_input(args.input)
    candidate, windows = build_bytes(data)
    receipt = {'输入SHA256': digest(data), '输出SHA256': digest(candidate), '文件大小': len(candidate),
               '仅新增许可': [[1, 903]], '允许差异区间': windows,
               '修改': [{'文件偏移': lo, '字节数': hi-lo, '原字节': data[lo:hi].hex(),
                         '新字节': candidate[lo:hi].hex()} for lo, hi in windows],
               '差异字节数': sum(a != b for a, b in zip(data, candidate)),
               '原接收门VA': hex(OLD_GATE), '新接收门VA': hex(NEW_GATE),
               '用途': '魔王契约完整明细和每日已用次数；原生读者保持',
               '本机安装': False, '发布': False, '游戏实测': False}
    args.out_dir.mkdir(parents=True, exist_ok=False)
    target = args.out_dir/'DNF.exe'
    with target.open('xb') as output:
        output.write(candidate)
    require(digest(target.read_bytes()) == receipt['输出SHA256'], '候选磁盘读回失败')
    (args.out_dir/'build-receipt.json').write_text(json.dumps(receipt, ensure_ascii=False, indent=2)+'\n', encoding='utf8')
    print(json.dumps(receipt, ensure_ascii=False))


if __name__ == '__main__':
    main()
