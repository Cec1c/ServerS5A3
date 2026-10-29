"""为已核对的24.9客户端独立生成class0/op855接收许可候选。"""
import argparse
import array
import hashlib
import json
import struct
import sys
from pathlib import Path
import capstone
import pefile

SOURCE_SHA = 'e324c74f42538fa892c6e2307e192b5772b7d0fe9af940f0b1bbf6a7c545ad3f'
OUTPUT_SHA = '0e7b83af033e567c9905e8a4cf44106e760493fa8252bd34730d6a127842f4d1'
CALL = 0x2523a13
OLD = 0x6eadd40
BASE = 0x400000


def sha(data):
    return hashlib.sha256(data).hexdigest()


def checksum(data, offset):
    d = bytearray(data)
    struct.pack_into('<I', d, offset, 0)
    if len(d) % 2:
        d.append(0)
    words = array.array('H', d)
    if not sys.byteorder == 'little':
        raise ValueError("核对失败：sys.byteorder == 'little'")
    total = sum(words)
    while total >> 16:
        total = (total & 65535) + (total >> 16)
    return total + len(data)


def evaluator(data):
    pe = pefile.PE(data=data, fast_load=True)
    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_32)
    md.detail = True
    x = capstone.x86
    call_at = pe.get_offset_from_rva(CALL - BASE)
    if not data[call_at] == 232:
        raise ValueError('核对失败：data[call_at] == 232')
    entry = CALL + 5 + struct.unpack_from('<i', data, call_at + 1)[0]
    nodes, classes, pending = ({}, set(), [entry])

    def operand(a):
        if a.type == x.X86_OP_IMM:
            return ('imm', a.imm)
        if a.type == x.X86_OP_REG:
            if not a.reg in (x.X86_REG_EAX, x.X86_REG_AL):
                raise ValueError('核对失败：a.reg in (x.X86_REG_EAX, x.X86_REG_AL)')
            return ('eax' if a.reg == x.X86_REG_EAX else 'al', 0)
        if not (a.type == x.X86_OP_MEM and a.mem.index == 0):
            raise ValueError('核对失败：a.type == x.X86_OP_MEM and a.mem.index == 0')
        if a.mem.base == x.X86_REG_ESP and a.mem.disp == 4 and (a.size == 2):
            return ('opcode', 0)
        if a.mem.base == x.X86_REG_EDI and a.mem.disp == 0 and (a.size == 1):
            return ('class', 0)
        raise AssertionError('遇到未声明的接收上下文')
    while pending:
        pc = pending.pop()
        if pc in nodes:
            continue
        i = next(md.disasm(pe.get_data(pc - BASE, 16), pc, count=1))
        n, args, next_pc = (i.mnemonic, i.operands, pc + i.size)
        if n in ('mov', 'movzx'):
            if not (args[0].type == x.X86_OP_REG and args[0].reg == x.X86_REG_EAX):
                raise ValueError('核对失败：args[0].type == x.X86_OP_REG and args[0].reg == x.X86_REG_EAX')
            row = (n, next_pc, operand(args[1]))
        elif n == 'cmp':
            a, b = (operand(args[0]), operand(args[1]))
            if a[0] == 'class':
                if not b[0] == 'imm':
                    raise ValueError("核对失败：b[0] == 'imm'")
                classes.add(b[1])
            row = (n, next_pc, a, b, (1 << args[0].size * 8) - 1)
        elif n in ('je', 'jne', 'jb', 'jbe', 'ja', 'jae', 'jmp'):
            if not args[0].type == x.X86_OP_IMM:
                raise ValueError('核对失败：args[0].type == x.X86_OP_IMM')
            row = (n, next_pc, args[0].imm)
            pending.append(args[0].imm)
        elif n == 'bt':
            if not (args[0].type == x.X86_OP_MEM and args[0].mem.base == args[0].mem.index == 0):
                raise ValueError('核对失败：args[0].type == x.X86_OP_MEM and args[0].mem.base == args[0].mem.index == 0')
            if not operand(args[1])[0] == 'eax':
                raise ValueError("核对失败：operand(args[1])[0] == 'eax'")
            address = args[0].mem.disp
            if not address == 115942144:
                raise ValueError('核对失败：address == 115942144')
            row = (n, next_pc, pe.get_data(address - BASE, 188))
        elif n == 'setb':
            if not args[0].reg == x.X86_REG_AL:
                raise ValueError('核对失败：args[0].reg == x.X86_REG_AL')
            row = (n, next_pc)
        elif n == 'xor':
            if not all((a.type == x.X86_OP_REG and a.reg == x.X86_REG_EAX for a in args)):
                raise ValueError('核对失败：all((a.type == x.X86_OP_REG and a.reg == x.X86_REG_EAX for a in args))')
            row = (n, next_pc)
        elif n == 'ret':
            if not (len(args) == 1 and args[0].imm == 4):
                raise ValueError('核对失败：len(args) == 1 and args[0].imm == 4')
            row = (n, next_pc)
        else:
            raise AssertionError('不支持指令：' + n + ' ' + i.op_str)
        nodes[pc] = row
        if n not in ('ret', 'jmp'):
            pending.append(next_pc)
        if not len(nodes) < 2000:
            raise ValueError('核对失败：len(nodes) < 2000')

    def evaluate(opcode, packet_class):
        eax = cf = zf = None
        pc = entry

        def val(a):
            kind = a[0]
            if kind == 'imm':
                return a[1]
            if kind == 'class':
                return packet_class
            if kind == 'opcode':
                return opcode
            if not eax is not None:
                raise ValueError('核对失败：eax is not None')
            return eax if kind == 'eax' else eax & 255
        for _ in range(1000):
            r = nodes[pc]
            n, pc = r[:2]
            if n in ('mov', 'movzx'):
                eax = val(r[2]) & 4294967295
            elif n == 'cmp':
                a, b = (val(r[2]) & r[4], val(r[3]) & r[4])
                cf, zf = (a < b, a == b)
            elif n == 'jmp':
                pc = r[2]
            elif n in ('je', 'jne', 'jb', 'jbe', 'ja', 'jae'):
                if not (cf is not None and zf is not None):
                    raise ValueError('核对失败：cf is not None and zf is not None')
                take = zf if n == 'je' else not zf if n == 'jne' else cf if n == 'jb' else cf or zf if n == 'jbe' else not cf and (not zf) if n == 'ja' else not cf
                if take:
                    pc = r[2]
            elif n == 'bt':
                if not (eax is not None and 0 <= eax <= 1474):
                    raise ValueError('核对失败：eax is not None and 0 <= eax <= 1474')
                cf, zf = (bool(r[2][eax // 8] & 1 << eax % 8), None)
            elif n == 'setb':
                if not (cf is not None and eax is not None):
                    raise ValueError('核对失败：cf is not None and eax is not None')
                eax = eax & ~255 | int(cf)
            elif n == 'xor':
                eax, cf, zf = (0, False, True)
            elif n == 'ret':
                if not eax in (0, 1):
                    raise ValueError('核对失败：eax in (0, 1)')
                return eax
        raise AssertionError('执行步数超限')
    return (evaluate, classes, len(nodes))


def build(original):
    if not sha(original) == SOURCE_SHA:
        raise ValueError('核对失败：sha(original) == SOURCE_SHA')
    pe = pefile.PE(data=original, fast_load=True)
    if not (pe.OPTIONAL_HEADER.ImageBase == BASE and pe.FILE_HEADER.Machine == 332):
        raise ValueError('核对失败：pe.OPTIONAL_HEADER.ImageBase == BASE and pe.FILE_HEADER.Machine == 332')
    if not pe.FILE_HEADER.NumberOfSections == 66:
        raise ValueError('核对失败：pe.FILE_HEADER.NumberOfSections == 66')
    if not pe.OPTIONAL_HEADER.SectionAlignment == pe.OPTIONAL_HEADER.FileAlignment == 4096:
        raise ValueError('核对失败：pe.OPTIONAL_HEADER.SectionAlignment == pe.OPTIONAL_HEADER.FileAlignment == 4096')
    if not (len(original) == 112037888 and pe.OPTIONAL_HEADER.SizeOfImage == 112123904):
        raise ValueError('核对失败：len(original) == 112037888 and pe.OPTIONAL_HEADER.SizeOfImage == 112123904')
    if not pe.sections[-1].PointerToRawData + pe.sections[-1].SizeOfRawData == len(original):
        raise ValueError('核对失败：pe.sections[-1].PointerToRawData + pe.sections[-1].SizeOfRawData == len(original)')
    if not pe.sections[-1].VirtualAddress + 4096 == pe.OPTIONAL_HEADER.SizeOfImage:
        raise ValueError('核对失败：pe.sections[-1].VirtualAddress + 4096 == pe.OPTIONAL_HEADER.SizeOfImage')
    if not not pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size:
        raise ValueError('核对失败：not pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size')
    header = pe.sections[-1].get_file_offset() + 40
    if not header + 40 <= pe.OPTIONAL_HEADER.SizeOfHeaders:
        raise ValueError('核对失败：header + 40 <= pe.OPTIONAL_HEADER.SizeOfHeaders')
    if not original[header:header + 40] == bytes(40):
        raise ValueError('核对失败：original[header:header + 40] == bytes(40)')
    call_at = pe.get_offset_from_rva(CALL - BASE)
    if not original[call_at:call_at + 5] == bytes.fromhex('e828a39804'):
        raise ValueError("核对失败：original[call_at:call_at + 5] == bytes.fromhex('e828a39804')")
    new_rva = pe.OPTIONAL_HEADER.SizeOfImage
    new_va = BASE + new_rva
    code = bytearray.fromhex('803f000f85')
    code.extend(struct.pack('<i', OLD - (new_va + len(code) + 4)))
    code.extend(bytes.fromhex('0fb74424043d570300000f85'))
    code.extend(struct.pack('<i', OLD - (new_va + len(code) + 4)))
    code.extend(bytes.fromhex('b801000000c20400'))
    result = bytearray(original)
    result.extend(code + bytes(4096 - len(code)))
    result[call_at:call_at + 5] = b'\xe8' + struct.pack('<i', new_va - (CALL + 5))
    opt = pe.OPTIONAL_HEADER.get_file_offset()
    sections_at = pe.FILE_HEADER.get_field_absolute_offset('NumberOfSections')
    struct.pack_into('<H', result, sections_at, 67)
    struct.pack_into('<I', result, opt + 4, pe.OPTIONAL_HEADER.SizeOfCode + 4096)
    struct.pack_into('<I', result, opt + 56, new_rva + 4096)
    struct.pack_into('<8sIIIIIIHHI', result, header, b'.wk855', len(code), new_rva, 4096, len(original), 0, 0, 0, 0, 1610612768)
    struct.pack_into('<I', result, opt + 64, checksum(result, opt + 64))
    ranges = sorted([(sections_at, sections_at + 2), (opt + 4, opt + 8), (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40), (call_at, call_at + 5)])
    cursor = 0
    for a, b in ranges:
        if not original[cursor:a] == result[cursor:a]:
            raise ValueError('批准区间外字节发生变化')
        cursor = b
    if not original[cursor:] == result[cursor:len(original)]:
        raise ValueError('核对失败：original[cursor:] == result[cursor:len(original)]')
    if sha(result) != OUTPUT_SHA:
        raise ValueError('输出未匹配已验收的精确候选摘要')
    return (bytes(result), ranges)


def verify(original, candidate):
    expected, ranges = build(original)
    if expected != candidate:
        raise ValueError('候选与批准字节变化不一致')
    before, classes1, count1 = evaluator(original)
    after, classes2, count2 = evaluator(candidate)
    if classes1 != {0, 1} or classes2 != classes1:
        raise ValueError('存在未核对的其他类别条件')
    rows = []
    for cls in (0, 1, 2):
        changes = []
        for opcode in range(65536):
            a, b = before(opcode, cls), after(opcode, cls)
            if a != b:
                changes.append([opcode, a, b])
        wanted = [[855, 0, 1]] if cls == 0 else []
        if changes != wanted:
            raise ValueError('接收许可变化超出class0/op855：' + str(changes))
        rows.append({'分类': cls, '编号覆盖': 65536, '变化': changes})
    if any(before(855, cls) != after(855, cls) for cls in range(1, 256)):
        raise ValueError('其他分类的855发生变化')
    return {'成功': True, '输入SHA256': sha(original), '输出SHA256': sha(candidate),
            '原文件允许变化区间': ranges, '新增节': '.wk855，RX，4096字节',
            '新增许可': 'class0/op855', '全量编号验证': rows,
            '类别比较常量': [0, 1], '接收门指令数': [count1, count2],
            '其他类别依据': '完整可达控制流仅比较0和1，类别2代表2至255；另逐值核对其855。',
            '共享位图及原处理器保持': True, '验证方式': '静态逐指令求值，未加载或执行EXE/DLL',
            '原生处理器及游戏UI验证': False}

def write_candidate(source, destination, receipt):
    source, destination, receipt = (Path(p).resolve() for p in (source, destination, receipt))
    if len({source, destination, receipt}) != 3 or destination.exists() or receipt.exists():
        raise ValueError('输入、候选、回执必须不同；两个输出均须为新文件')
    original = source.read_bytes()
    candidate, _ = build(original)
    proof = verify(original, candidate)
    if sha(source.read_bytes()) != SOURCE_SHA:
        raise ValueError('核对期间输入发生变化')
    # 独立输出，不替换、安装、启动原程序，不加载额外DLL。
    destination.parent.mkdir(parents=True, exist_ok=True)
    receipt.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(candidate)
    if sha(destination.read_bytes()) != OUTPUT_SHA:
        raise ValueError('候选写入后实际摘要不符')
    with receipt.open('x', encoding='utf-8') as stream:
        json.dump(proof, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    return proof

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output, args.receipt), ensure_ascii=False, indent=2))

if __name__ == '__main__':
    main()
