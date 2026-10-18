"""Add class-specific receive admissions to the exact published v129 EXE."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '37b230cd6f047ccc001501cbc02c3709d0c6ee5e0059f66f2d656023045040fa'
INPUT_SIZE = 111808512
CLASS0 = tuple(range(1483, 1492))
CLASS1 = (658, 668, 820, 889)
CALL_VA = 0x2523a13
FRAGMENT_VA = 0x2523a0c
FRAGMENT_END = 0x2523a18
OLD_ENTRY_VA = 0x376e6c0
SHARED_GATE_VA = 0x6e83610
NEW_RVA = INPUT_SIZE
NEW_VA = pe.IMAGE_BASE + NEW_RVA
NEW_RAW = INPUT_SIZE
NEW_SIZE = 4096
SECTION_NAME = b'.clsgate'


def helper():
    """EDI is the verified receive header; [EDI] is its class byte."""
    code, labels, branches = bytearray(), {}, []

    def emit(value):
        code.extend(bytes.fromhex(value))

    def branch(op, label):
        emit(op)
        branches.append((len(code), label))
        code.append(0)

    emit('0fb7442404')  # movzx eax, word [esp+4]
    emit('803f00')      # cmp byte [edi], 0
    branch('74', 'class0')
    emit('803f01')
    branch('75', 'fallback')
    for opcode in CLASS1:
        code.append(0x3d)
        code.extend(struct.pack('<I', opcode))
        branch('74', 'allow')
    branch('eb', 'fallback')
    labels['class0'] = len(code)
    code.append(0x3d)
    code.extend(struct.pack('<I', CLASS0[0]))
    branch('72', 'fallback')
    code.append(0x3d)
    code.extend(struct.pack('<I', CLASS0[-1]))
    branch('76', 'allow')
    labels['fallback'] = len(code)
    code.append(0xe9)
    code.extend(struct.pack('<i', OLD_ENTRY_VA - (NEW_VA + len(code) + 4)))
    labels['allow'] = len(code)
    emit('b801000000c20400')
    for at, label in branches:
        delta = labels[label] - (at + 1)
        if not -128 <= delta <= 127:
            raise ValueError('helper branch out of range')
        code[at] = delta & 255
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published v129 SHA-256/length required')
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    table = opt + struct.unpack_from('<H', data, pe_at + 20)[0]
    header = table + 23 * 40
    if (len(sections) != 23 or struct.unpack_from('<II', data, opt + 32) != (4096, 4096)
            or struct.unpack_from('<I', data, opt + 56)[0] != NEW_RVA
            or any(data[header:header + 40]) or header + 40 > 4096
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('unexpected v129 section/header/certificate layout')
    # Verify the full opcode argument, singleton load, native call and class
    # source before replacing only the five-byte call. No payload pointer or
    # caller stack contract is changed.
    expected = bytes.fromhex('0fb74f0183c410518b0d44fa7e05e8a8ac24018b0d44fa7e05')
    at = pe.offset(sections, 0x2523a05, len(expected))
    if data[at:at + len(expected)] != expected:
        raise ValueError('unexpected receive callsite context')
    at = pe.offset(sections, 0x2523c9c, 17)
    if data[at:at + 17] != bytes.fromhex('0fb60f8bc183e8000f84eb010000487449'):
        raise ValueError('unexpected class switch context')
    for opcode in (*CLASS0, *CLASS1):
        at = pe.offset(sections, pe.BITMAP_VA + opcode // 8)
        if data[at] & (1 << (opcode % 8)):
            raise ValueError('requested opcode already admitted by shared bitmap')
    code = helper()
    out = bytearray(data)
    out.extend(code + bytes(NEW_SIZE - len(code)))
    at = pe.offset(sections, CALL_VA, 5)
    out[at:at + 5] = b'\xe8' + struct.pack('<i', NEW_VA - (CALL_VA + 5))
    struct.pack_into('<H', out, pe_at + 6, 24)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + NEW_SIZE)
    struct.pack_into('<I', out, opt + 56, NEW_RVA + NEW_SIZE)
    struct.pack_into('<8sIIIIIIHHI', out, header, SECTION_NAME, len(code), NEW_RVA,
                     NEW_SIZE, NEW_RAW, 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    result = bytes(out)
    ranges = [(pe_at + 6, pe_at + 8), (opt + 4, opt + 8),
              (opt + 56, opt + 60), (opt + 64, opt + 68),
              (header, header + 40), (at, at + 5)]
    return result, dict(input_sha256=INPUT_SHA256, output_sha256=pe.sha(result),
        input_size=INPUT_SIZE, output_size=len(result), class0=list(CLASS0), class1=list(CLASS1),
        call_va=hex(CALL_VA), helper_va=hex(NEW_VA), helper_size=len(code),
        helper_sha256=pe.sha(code), original_ranges=sorted(ranges), appended_range=[NEW_RAW,len(result)],
        shared_bitmap_and_bound_unchanged=True, other_classes_unchanged=True,
        full_ui_acceptance=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['output_sha256']:
        raise RuntimeError('input/output changed')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
