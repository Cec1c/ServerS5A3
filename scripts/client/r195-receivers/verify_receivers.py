"""Independent PE contract and complete class0/class1 native receive audit."""
from pathlib import Path
import argparse, gc, hashlib, json, struct, sys

BASE_SHA = 'c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8'
ADDITIONS = {(0, 103), (0, 775), (0, 1278), (0, 1367), (0, 1368), (1, 835), (1, 864)}
CALL, RETURN = 0x2523a13, 0x2523a18


def layout(data):
    assert data[:2] == b'MZ'
    start = struct.unpack_from('<I', data, 0x3c)[0]
    assert data[start:start + 4] == b'PE\0\0'
    opt = start + 24
    assert struct.unpack_from('<H', data, opt)[0] == 0x10b
    table = opt + struct.unpack_from('<H', data, start + 20)[0]
    count = struct.unpack_from('<H', data, start + 6)[0]
    sections = []
    for i in range(count):
        vsize, rva, rsize, raw = struct.unpack_from('<IIII', data, table + i * 40 + 8)
        assert raw + rsize <= len(data)
        sections.append((rva + 0x400000, vsize, raw, rsize))
    return start, opt, table, sections


def offset(sections, va):
    for start, _, raw, size in sections:
        if start <= va < start + size:
            return raw + va - start
    raise ValueError(hex(va))


def native(data, classes, opcodes):
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
    from unicorn.x86_const import UC_X86_REG_EIP, UC_X86_REG_ESP, UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP
    _, _, _, sections = layout(data)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    try:
        pages = [0x2523000, 0x376e000, 0x6e83000, 0x6e92000, 0x57ef000]
        pages += list(range(0x6ea0000, max(start + size for start, _, _, size in sections), 4096))
        for page in pages:
            u.mem_map(page, 4096)
            for start, _, raw, size in sections:
                lo, hi = max(page, start), min(page + 4096, start + size)
                if lo < hi:
                    u.mem_write(lo, data[raw + lo - start:raw + hi - start])
        sp, scratch = 0x10008000, 0x20000000
        u.mem_map(0x10000000, 0x10000)
        u.mem_map(scratch, 4096)
        u.mem_write(0x57efa44, bytes(4))
        values = bytearray()
        keep = [(UC_X86_REG_EBX, 0x11111111), (UC_X86_REG_ESI, 0x33333333),
                (UC_X86_REG_EDI, scratch), (UC_X86_REG_EBP, 0x44444444)]
        for cls in classes:
            for opcode in opcodes:
                u.mem_write(scratch, bytes([cls]) + struct.pack('<H', opcode))
                u.mem_write(sp, struct.pack('<I', opcode))
                for reg, value in keep + [(UC_X86_REG_ESP, sp), (UC_X86_REG_EAX, 0xdeadbeef),
                                           (UC_X86_REG_ECX, 0), (UC_X86_REG_EDX, 0x22222222)]:
                    u.reg_write(reg, value)
                u.emu_start(CALL, RETURN, count=256)
                assert u.reg_read(UC_X86_REG_EIP) == RETURN
                assert u.reg_read(UC_X86_REG_ESP) == sp + 4
                assert all(u.reg_read(reg) == value for reg, value in keep)
                result = u.reg_read(UC_X86_REG_EAX)
                assert result in (0, 1)
                values.append(result)
        return values
    finally:
        del u
        gc.collect()


def verify(baseline, candidate):
    assert hashlib.sha256(baseline).hexdigest() == BASE_SHA
    assert len(baseline) == 111857664 and len(candidate) == len(baseline) + 4096
    start, opt, table, sections = layout(baseline)
    _, newopt, newtable, newsections = layout(candidate)
    assert newopt == opt and newtable == table and len(newsections) == len(sections) + 1
    assert newsections[:-1] == sections
    header = table + len(sections) * 40
    assert candidate[header:header + 8] == b'.r195rx\0'
    assert newsections[-1][0] == 0x400000 + len(baseline)
    assert newsections[-1][2:] == (len(baseline), 4096)
    assert struct.unpack_from('<I', candidate, header + 36)[0] == 0x60000020
    assert struct.unpack_from('<I', candidate, opt + 56)[0] == len(candidate)
    assert struct.unpack_from('<I', candidate, opt + 4)[0] == struct.unpack_from('<I', baseline, opt + 4)[0] + 4096
    assert not any(candidate[len(baseline) + newsections[-1][1]:])
    call = offset(sections, CALL)
    assert candidate[call] == 0xe8
    assert CALL + 5 + struct.unpack_from('<i', candidate, call + 1)[0] == newsections[-1][0]
    ranges = sorted([(start + 6, start + 8), (opt + 4, opt + 8), (opt + 56, opt + 60),
                     (opt + 64, opt + 68), (header, header + 40), (call + 1, call + 5)])
    cursor = 0
    for lo, hi in ranges:
        assert baseline[cursor:lo] == candidate[cursor:lo]
        cursor = hi
    assert baseline[cursor:] == candidate[cursor:len(baseline)]
    checksum = 0
    for i, (word,) in enumerate(struct.iter_unpack('<H', candidate)):
        if i * 2 not in (opt + 64, opt + 66):
            checksum += word
    while checksum >> 16:
        checksum = (checksum & 65535) + (checksum >> 16)
    assert checksum + len(candidate) == struct.unpack_from('<I', candidate, opt + 64)[0]
    before = native(baseline, (0, 1), range(65536))
    after = native(candidate, (0, 1), range(65536))
    changes = {(i // 65536, i % 65536) for i, value in enumerate(after) if value != before[i]}
    assert changes == ADDITIONS, changes
    assert all(before[cls * 65536 + opcode] == 0 and after[cls * 65536 + opcode] == 1 for cls, opcode in ADDITIONS)
    for cls, opcode in ((0, 1150), (1, 502), (1, 785)):
        assert after[cls * 65536 + opcode] == 1
    controls = sorted({0, 65535, 1278, 1367, 1368, 103, 775, 835, 864})
    assert native(baseline, (2, 255), controls) == native(candidate, (2, 255), controls)
    return dict(passed=True, baseline=BASE_SHA, candidate=hashlib.sha256(candidate).hexdigest(),
                changed_class_opcode_pairs=sorted(changes), exhaustive_cases_per_image=len(after),
                extra_class_controls=2 * len(controls), full_gui_acceptance=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--python-deps', type=Path, required=True)
    args = parser.parse_args()
    sys.path.insert(0, str(args.python_deps))
    result = verify(args.baseline.read_bytes(), args.candidate.read_bytes())
    with args.output.open('x', encoding='utf8') as out:
        json.dump(result, out, indent=2)
    print(json.dumps(result))
