"""Enable 169, 182-189, 195 and 334 in the exact released v128 client."""
import argparse
import hashlib
import json
from pathlib import Path
import struct

INPUT_SHA256 = 'ea2f763927c8c993cc9f5cf41eb85ea37d5a50760ca310706497e5419bbc5912'
INPUT_SIZE = 111808512
IMAGE_BASE = 0x400000
BITMAP_VA = 0x6e92300
GATE_VA = 0x6e83610
ORIGINAL_GATE_VA = 0x376e6c0
OPCODES = (169, *range(182, 190), 195, 334)
EXPECTED_BITMAP_BYTES = {21: 0x64, 22: 0x00, 23: 0x80, 24: 0x00, 41: 0x18}
# Existing registration sites are preserved, including distinct contexts for
# duplicated numbers. A registration alone does not prove full UI behavior.
REGISTRATIONS = {
    169: ((0x2046449, 0x2033270),),
    182: ((0x1fe7de5, 0x1fce210), (0x20022ef, 0x1fedf10)),
    183: ((0x1fe7df8, 0x1fc8ae0), (0x2046482, 0x2033380)),
    184: ((0x1fe7e0b, 0x1fc8cf0), (0x2046495, 0x769ad0)),
    185: ((0x1fe7e1e, 0x1fc8e90), (0x20464a8, 0x769ad0)),
    186: ((0x1fe7e44, 0x1fc11d0), (0x2031d6e, 0x2005e90)),
    187: ((0x2031d81, 0x2005f20),),
    188: ((0x1fe7e57, 0x1fc14f0), (0x2002302, 0x1fe9bf0)),
    189: ((0x1fe7e6a, 0x1fc16d0), (0x20464bb, 0x2033490)),
    195: ((0x1fe7933, 0x1fbb360), (0x2046507, 0x20334c0)),
    334: ((0x1fe7e31, 0x1fc9030),),
}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def headers(data):
    pe = struct.unpack_from('<I', data, 0x3c)[0]
    opt = pe + 24
    if data[:2] != b'MZ' or data[pe:pe + 4] != b'PE\0\0':
        raise ValueError('not a PE image')
    if (struct.unpack_from('<H', data, pe + 4)[0] != 0x14c
            or struct.unpack_from('<H', data, opt)[0] != 0x10b
            or struct.unpack_from('<I', data, opt + 28)[0] != IMAGE_BASE):
        raise ValueError('unexpected x86 PE32 layout')
    table = opt + struct.unpack_from('<H', data, pe + 20)[0]
    sections = []
    for i in range(struct.unpack_from('<H', data, pe + 6)[0]):
        _, rva, rawsize, raw = struct.unpack_from('<IIII', data, table + 40 * i + 8)
        sections.append((IMAGE_BASE + rva, rawsize, raw))
    return opt, sections


def offset(sections, va, size=1):
    for start, count, raw in sections:
        if start <= va and va + size <= start + count:
            return raw + va - start
    raise ValueError('address outside file-backed sections')


def checksum(data, field):
    # All images accepted by build have an even length. Sum PE words excluding
    # the checksum field; folding at the end is equivalent to folding per word.
    words = memoryview(data).cast('H')
    value = sum(words) - words[field // 2] - words[field // 2 + 1]
    while value >> 16:
        value = (value & 0xffff) + (value >> 16)
    return value + len(data)


def build(data):
    if len(data) != INPUT_SIZE or sha(data) != INPUT_SHA256:
        raise ValueError('exact released v128 input SHA-256/length mismatch')
    opt, sections = headers(data)
    if len(sections) != 23 or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected sections or certificate')
    gate = bytes.fromhex('0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400')
    at = offset(sections, GATE_VA, len(gate))
    if data[at:at + len(gate)] != gate:
        raise ValueError('unexpected native receive gate')
    at = offset(sections, ORIGINAL_GATE_VA, 5)
    jump = data[at:at + 5]
    if jump[0] != 0xe9 or ORIGINAL_GATE_VA + 5 + struct.unpack_from('<i', jump, 1)[0] != GATE_VA:
        raise ValueError('unexpected original gate trampoline')
    for opcode, entries in REGISTRATIONS.items():
        for va, handler in entries:
            expected = b'\x6a\x00\x68' + struct.pack('<I', handler) + b'\x68' + struct.pack('<I', opcode)
            at = offset(sections, va, len(expected))
            if data[at:at + len(expected)] != expected:
                raise ValueError('unexpected existing registration')
    masks = {}
    for opcode in OPCODES:
        index, mask = opcode // 8, 1 << (opcode % 8)
        at = offset(sections, BITMAP_VA + index)
        if data[at] != EXPECTED_BITMAP_BYTES[index] or data[at] & mask:
            raise ValueError('unexpected baseline receive bit')
        masks[index] = masks.get(index, 0) | mask
    out, edits = bytearray(data), []
    for index, mask in sorted(masks.items()):
        at = offset(sections, BITMAP_VA + index)
        out[at] |= mask
        edits.append(dict(file_offset=at, bitmap_va=hex(BITMAP_VA + index),
                          before=data[at], after=out[at], mask=mask,
                          opcodes=[op for op in OPCODES if op // 8 == index]))
    struct.pack_into('<I', out, opt + 64, checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(input_sha256=INPUT_SHA256, output_sha256=sha(result),
                        size=len(result), enabled_opcodes=list(OPCODES), edits=edits,
                        checksum_range=[opt + 64, opt + 68], full_ui_acceptance=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(result)
    if sha(source.read_bytes()) != INPUT_SHA256 or sha(destination.read_bytes()) != receipt['output_sha256']:
        raise RuntimeError('input/output changed')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
