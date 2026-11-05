"""只读比较精确A5原件与最终婚姻候选，执行真实x86准入链，不启动游戏。"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import time

import capstone
import pefile
import patch_wedding_receive_complete as patch
import verify_wedding_receive_admission as native

# Independent reviewed inventory; do not derive expected deltas from the builder.
CLASS0 = (997, 998, 999, 1000, 1001, 1002, 1003, 1004, 1005, 1006,
          1007, 1008, 1009, 1011, 1012, 1013, 1014, 1015, 1016)
CLASS1 = tuple(range(1032, 1054))
TARGETS = {(0, op) for op in CLASS0} | {(1, op) for op in CLASS1}
ADDITIONS = TARGETS - {(0, 997), (0, 1000)}
RANGES = ((216, 220), (4064, 4068), (34748947, 34748952),
          (112546816, 112547065), (112547072, 112547376))
OLD, NEW, CALL = 0x6F06100, 0x6F8B500, 0x2523A13


def require(ok, message):
    if not ok:
        raise AssertionError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def verify(before, after):
    require(sha(before) == native.INPUT_SHA and sha(after) == patch.OUTPUT_SHA256,
            "只核对精确A5原件与最终a59候选")
    require(len(before) == len(after) == 112549888, "长度不符")
    require(tuple(patch.CLASS0) == CLASS0 and tuple(patch.CLASS1) == CLASS1, "工具准入集合与审查不符")
    restored = bytearray(after)
    for lo, hi in RANGES:
        restored[lo:hi] = before[lo:hi]
    require(restored == before, "登记范围外字节改变")
    for data, size in ((before, 990), (after, 0x630)):
        pe = pefile.PE(data=data, fast_load=True)
        require(pe.verify_checksum() and pe.FILE_HEADER.NumberOfSections == 93 and
                pe.OPTIONAL_HEADER.ImageBase == 0x400000 and
                pe.OPTIONAL_HEADER.SizeOfImage == 112771072, "PE校验、节表或映像改变")
        section = pe.sections[-1]
        require(section.Misc_VirtualSize == size and section.Characteristics == 0x60000020,
                "RX末节范围或权限不符")
    pe = pefile.PE(data=after, fast_load=True)
    at = pe.get_offset_from_rva(NEW - 0x400000)
    code = after[at:at+304]
    ins = list(capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_32).disasm(code, NEW))
    require(sum(i.size for i in ins) == 304 and code[-5] == 0xE9 and
            NEW + 304 + struct.unpack_from('<i', code, 300)[0] == 0x6F8B400,
            "完整适配器解码或旧链回落不符")
    # Both adapters classify before reading an opcode; all other classes tail
    # through the original chain. Prove that structure, then sample every class.
    for address, size in ((NEW, 304), (0x6F8B400, 249)):
        at = pe.get_offset_from_rva(address-0x400000)
        helper = after[at:at+size]
        require(helper[:5] == bytes.fromhex('803f000f84') and
                helper[9:14] == bytes.fromhex('803f010f85') and
                18 + struct.unpack_from('<i', helper, 14)[0] == size-5,
                "分类2..255不是opcode无关回落")
    original, candidate = native.Native(before), native.Native(after)
    deltas, matrices = [], []
    for classification in (0, 1):
        old = original.batch(classification, range(65536), OLD)
        new = candidate.batch(classification, range(65536), NEW)
        for opcode, (a, b) in enumerate(zip(struct.iter_unpack('<I', old), struct.iter_unpack('<I', new))):
            require(b[0] == int(a[0] or (classification, opcode) in TARGETS),
                    '非预期准入或原准入丢失: %d/%d' % (classification, opcode))
            if a != b:
                deltas.append((classification, opcode))
        matrices.append(dict(classification=classification, before_sha256=sha(old), after_sha256=sha(new)))
    require(set(deltas) == ADDITIONS and len(deltas) == 39, "增量不是精确39组")
    samples = sorted(set(CLASS0+CLASS1+(0, 1010, 1054, 1474, 0x7F01, 65535)))
    cases = 0
    for classification in range(256):
        for opcode in samples:
            a, old_entries, _ = original.call_site(classification, opcode)
            b, entries, initial = candidate.call_site(classification, opcode)
            if (classification, opcode) in TARGETS:
                require(b[native.UC_X86_REG_EAX] == 1 and not entries and
                        b[native.UC_X86_REG_ECX] == initial[native.UC_X86_REG_ECX], "新增路径ABI错误")
            else:
                require(a == b and len(old_entries) == len(entries) == 1, "非目标回落ABI改变")
                require(all(old_entries[0][r] == entries[0][r] for r in native.REGISTERS[1:]),
                        "旧链入口寄存器改变")
            cases += 1
    for classification, opcode in ((0, 1001), (1, 1036), (0, 1010), (1, 1054), (255, 1001)):
        a, _, _ = original.call_site(classification, opcode, 0xCAFE)
        b, entries, _ = candidate.call_site(classification, opcode, 0xCAFE)
        require((b[native.UC_X86_REG_EAX] == 1 and not entries) if (classification, opcode) in TARGETS
                else a == b and len(entries) == 1, "u16高位参数ABI改变")
    return dict(status='passed', input_sha256=sha(before), output_sha256=sha(after),
        changed_ranges=[list(r) for r in RANGES], matrices=matrices,
        actual_new_pairs=deltas, actual_new_pair_count=39, actual_gate_calls=262144,
        abi_samples_per_image=cases, high_word_samples_per_image=5,
        other_classes='opcode-independent fallback proof plus samples for all 256 classes',
        full_16m_matrix_executed=False, game_launched=False, gameplay_acceptance=False,
        limitations=['验证收包门槛、范围和ABI，不替代包体或实际游戏验收'])


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True, help='精确A5原件')
    p.add_argument('--candidate', type=Path, required=True, help='最终a59候选')
    p.add_argument('--report', type=Path, required=True)
    a = p.parse_args()
    source, candidate, report = a.input.resolve(), a.candidate.resolve(), a.report.resolve()
    require(report not in (source, candidate) and not report.exists(), "禁止覆盖输入或已有证据")
    before, after = source.read_bytes(), candidate.read_bytes()
    started = time.perf_counter()
    result = verify(before, after)
    require(sha(source.read_bytes()) == sha(before) and sha(candidate.read_bytes()) == sha(after), "验证中输入改变")
    result['seconds'] = time.perf_counter()-started
    result['tool_sha256'] = {Path(v).name:sha(Path(v).read_bytes()) for v in
        (__file__, native.__file__, patch.__file__)}
    report.parent.mkdir(parents=True, exist_ok=True)
    with report.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps({k:result[k] for k in ('status', 'actual_new_pair_count', 'actual_gate_calls', 'seconds')}, ensure_ascii=False))
