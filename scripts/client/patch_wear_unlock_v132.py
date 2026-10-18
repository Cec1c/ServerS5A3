"""Complete zero-move wear ACK cleanup in the exact published v132 image."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '153903d7e76c9e2feed6fd19c053742365f04484d6dadcf24b78d5b58daf0391'
INPUT_SIZE = 111812608
GUARD_VA = 0x6e9d000
SITE = 0x1fded8a
CONTINUE = 0x1fded90
FINISH = 0x1fdfca8
CONTAINER = 0x2531a80
POP_STATE = 0x25f0290
OLD_LOAD = bytes.fromhex('8b950cffffff')


def rel(op, address, target):
    return bytes([op]) + struct.pack('<i', target - address - 5)


def old_guard():
    code = bytearray.fromhex('9c83bd0cffffff00751880bd20ffffff01770f80bd2bffffff0375069d')
    code += rel(0xe9, GUARD_VA + len(code), FINISH)
    code += b'\x9d' + OLD_LOAD
    code += rel(0xe9, GUARD_VA + len(code), CONTINUE)
    return bytes(code)


def guard():
    code = bytearray.fromhex('9c83bd0cffffff00')
    branches = []
    def branch(op, label):
        branches.append((len(code), label))
        code.extend(bytes([op, 0]))
    branch(0x75, 'fallback')
    code += bytes.fromhex('80bd20ffffff01')
    branch(0x77, 'fallback')
    code += bytes.fromhex('80bd2bffffff03')
    branch(0x75, 'fallback')
    # Use the ACK source container; preserve every register across native calls.
    code += bytes.fromhex('600fb68520ffffff50')
    code += rel(0xe8, GUARD_VA + len(code), CONTAINER)
    code += bytes.fromhex('83c40485c0')
    branch(0x74, 'done')
    code += bytes.fromhex('8b48703b486c')
    branch(0x76, 'done')
    code += bytes.fromhex('8379fc07')
    branch(0x75, 'done')
    code += bytes.fromhex('8bc8')
    code += rel(0xe8, GUARD_VA + len(code), POP_STATE)
    labels = {'done': len(code)}
    # Seven cdecl parser argument slots remain at SITE. LEA preserves flags.
    code += bytes.fromhex('619d8d64241c')
    code += rel(0xe9, GUARD_VA + len(code), FINISH)
    labels['fallback'] = len(code)
    code += b'\x9d' + OLD_LOAD
    code += rel(0xe9, GUARD_VA + len(code), CONTINUE)
    for at, label in branches:
        delta = labels[label] - at - 2
        if not -128 <= delta <= 127:
            raise ValueError('short branch overflow')
        code[at + 1] = delta & 255
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published v132 SHA-256 and length required')
    opt, sections = pe.headers(data)
    if len(sections) != 24 or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected PE sections or certificate')
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    index = next(i for i, s in enumerate(sections) if s[0] == GUARD_VA)
    header = table + index * 40
    va, size, raw = sections[index]
    old, code = old_guard(), guard()
    if data[header:header+8] != b'.wearnop' or struct.unpack_from('<I', data, header+36)[0] != 0x60000020:
        raise ValueError('expected readonly executable wear guard')
    if data[raw:raw+size] != old + bytes(size-len(old)) or len(code) > size:
        raise ValueError('unexpected guard or padding')
    site = pe.offset(sections, SITE, 6)
    if data[site:site+6] != rel(0xe9, SITE, GUARD_VA) + b'\x90':
        raise ValueError('unexpected guard trampoline')
    out = bytearray(data)
    out[raw:raw+size] = code + bytes(size-len(code))
    struct.pack_into('<I', out, header+8, len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    result = bytes(out)
    return result, dict(input_sha256=INPUT_SHA256, output_sha256=pe.sha(result),
        input_size=len(data), output_size=len(result), guard_va=hex(va),
        guard_size=len(code), guard_hex=code.hex(),
        original_ranges=sorted([(opt+64,opt+68),(header+8,header+12),(raw,raw+len(code))]),
        receive_gates_unchanged=True, native_ui_accepted=False)


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
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
