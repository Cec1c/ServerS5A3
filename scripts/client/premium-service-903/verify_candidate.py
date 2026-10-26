"""执行固定V163/候选接收门及实际分类分流；不启动游戏或执行网络正文。"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys
import time

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
                              UC_X86_REG_EDX, UC_X86_REG_EDI, UC_X86_REG_EBP,
                              UC_X86_REG_ESP, UC_X86_REG_EIP, UC_X86_REG_ESI,
                              UC_X86_REG_EFLAGS)

INPUT_SHA256 = 'b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902'
INPUT_SIZE = 111894528


def require(condition, message):
    if not condition:
        raise ValueError(message)


class Gate:
    """解码调用以固定跳过点代替；许可与分类代码由EXE实际执行。"""
    def __init__(self, data):
        self.data = data
        self.vm = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        self.seen = None
        pe = struct.unpack_from('<I', data, 60)[0]
        optional = pe+24
        base = struct.unpack_from('<I', data, optional+28)[0]
        table = optional+struct.unpack_from('<H', data, pe+20)[0]
        self.sections = []
        for i in range(struct.unpack_from('<H', data, pe+6)[0]):
            vs, rva, raw_size, raw = struct.unpack_from('<IIII', data, table+i*40+8)
            self.sections.append((base+rva, max(vs, raw_size), raw_size, raw))
        self.vm.hook_add(UC_HOOK_MEM_UNMAPPED, self.missing)
        self.vm.hook_add(UC_HOOK_CODE, self.hook)
        for address in (0x10000000, 0x10001000, 0x20000000, 0x30000000):
            self.put(address, bytes(4096))

    def read(self, address, length):
        for start, _, raw_size, raw in self.sections:
            if start <= address and address+length <= start+raw_size:
                return self.data[raw+address-start:raw+address-start+length]
        raise ValueError('地址不在原始映像内：'+hex(address))

    def put(self, address, data):
        for page in range(address & ~4095, (address+len(data)+4095) & ~4095, 4096):
            if page not in self.pages:
                self.vm.mem_map(page, 4096)
                self.pages.add(page)
        self.vm.mem_write(address, data)

    def missing(self, vm, access, address, size, value, user):
        page = address & ~4095
        for start, virtual_size, raw_size, raw in self.sections:
            if start <= page < start+virtual_size:
                relative = page-start
                content = self.data[raw+relative:raw+min(relative+4096, raw_size)] if relative < raw_size else b''
                self.put(page, content.ljust(4096, b'\0'))
                return True
        return False

    def hook(self, vm, address, size, user):
        if address == 0x02523A30:
            # 固定夹具跳过正文解码；保留前面实际写入的许可结果。
            vm.reg_write(UC_X86_REG_EIP, 0x02523C8A)
        elif address in (0x02523CF6, 0x02523E95, 0x02523F50, 0x02523CAD):
            self.seen = {0x02523CF6: 'class1', 0x02523E95: 'class0',
                         0x02523F50: '未分派', 0x02523CAD: '分类拒绝'}[address]
            vm.emu_stop()

    def route(self, classification, opcode):
        self.seen = None
        self.vm.mem_write(0x30000000, struct.pack('<BH', classification, opcode))
        for reg, value in ((UC_X86_REG_EDI, 0x30000000), (UC_X86_REG_EBP, 0x10001000),
                           (UC_X86_REG_ESP, 0x10000800), (UC_X86_REG_EAX, 0),
                           (UC_X86_REG_EBX, 0), (UC_X86_REG_ECX, 0), (UC_X86_REG_EDX, 0),
                           (UC_X86_REG_ESI, 0), (UC_X86_REG_EFLAGS, 2)):
            self.vm.reg_write(reg, value)
        self.vm.emu_start(0x02523A05, 0x20000000, count=1000)
        require(self.seen is not None, f'接收执行未结束：{classification}/{opcode}')
        return self.seen


def verify(original, candidate):
    require(len(original) == INPUT_SIZE and hashlib.sha256(original).hexdigest() == INPUT_SHA256,
            '原始EXE必须为固定V163')
    require(len(candidate) == len(original), '候选长度变化')
    before, after = Gate(original), Gate(candidate)
    require(before.read(0x02523A05, 14) == after.read(0x02523A05, 14), '接收调用前代码变化')
    require(before.read(0x02523A30, 5) == bytes.fromhex('e8dbb12401'), '正文跳过点不匹配')
    require(before.read(0x02523C8A, 0x23) == after.read(0x02523C8A, 0x23), '原生分类分流变化')
    require(before.route(1, 903) == '未分派', '固定V163的903已开放，拒绝重复补丁')
    differences = []
    started = time.monotonic()
    for classification in (0, 1):
        for opcode in range(65536):
            old = before.route(classification, opcode)
            new = after.route(classification, opcode)
            if old != new:
                differences.append([classification, opcode, old, new])
        print(f'完成分类{classification}的全部16位编号对照', flush=True)
    require(differences == [[1, 903, '未分派', 'class1']], '存在非903的接收行为变化')
    wrong = 0
    for classification in (2, 3, 127, 255):
        for opcode in (*range(2048), 65535):
            require(before.route(classification, opcode) == after.route(classification, opcode), '错误分类行为变化')
            wrong += 1
    anchors = []
    for name, address, length in (('903明细原生读者', 0x01FAE6E0, 0x1f0),
                                  ('74字节状态写入', 0x023265F0, 0x19f),
                                  ('契约资格判定', 0x02326360, 0x56),
                                  ('契约牌资源选择', 0x0359DAF0, 0x60),
                                  ('翻牌价格读者', 0x01FFE430, 0x1024)):
        region = before.read(address, length)
        require(region == after.read(address, length), name+'变化')
        anchors.append({'用途': name, 'VA': hex(address), '字节数': length,
                        'SHA256': hashlib.sha256(region).hexdigest(), '字节保持': True})
    return {'通过': True, '输入SHA256': INPUT_SHA256,
            '候选SHA256': hashlib.sha256(candidate).hexdigest(), '完整分类编号案例': 131072,
            '错误分类案例': wrong, '唯一接收差异': differences, '原生代码保持': anchors,
            '耗时秒': round(time.monotonic()-started, 3),
            '范围': '实际接收门和分类分流；正文解码为显式跳过夹具，不计完整903业务或游戏GUI验收',
            '游戏实测': False, '安装': False, '发布': False}


def main():
    if sys.flags.optimize:
        raise RuntimeError('拒绝-O或优化环境')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--candidate', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args = parser.parse_args()
    require(not args.receipt.exists(), '回执已存在，拒绝覆盖')
    # 全文件差异、固定原指令和PE checksum由独立构建器重新推导。
    import build_candidate as builder
    original = builder.checked_input(args.input)
    candidate = args.candidate.read_bytes()
    expected, _ = builder.build_bytes(original)
    require(candidate == expected, '候选不是独立重建的唯一输出')
    report = verify(original, candidate)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    with args.receipt.open('x', encoding='utf8') as output:
        output.write(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print(json.dumps(report, ensure_ascii=False))


if __name__ == '__main__':
    main()
