"""Enable five existing DK/Guardian receivers in the exact preview v124 image."""
import argparse
import hashlib
import json
from pathlib import Path
import struct

INPUT_SHA256 = 'eaa2485609fa9cc2c9ca99fadd743b5206a37b5517dd07d195eb35b3c0a29d50'
IMAGE_BASE = 0x400000
BITMAP_VA = 0x6e92300
GATE_VA = 0x6e83610
OPCODES = (447, 510, 580, 657, 809)
EXPECTED_BYTES = {447: 0x02, 510: 0x20, 580: 0x80, 657: 0x19, 809: 0x00}
# Current registrations, distinct from the unrelated 657 notification receiver.
REGISTRATIONS = {447: (0x200260d, 0x1ffc1a0), 510: (0x1faca93, 0x1fa56e0),
                 580: (0x20469ed, 0x2034ca0), 657: (0x1fba0dd, 0x1fad700),
                 809: (0x2002620, 0x1ffc580)}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def headers(data):
    pe = struct.unpack_from('<I', data, 0x3c)[0]
    opt = pe + 24
    if data[:2] != b'MZ' or data[pe:pe + 4] != b'PE\0\0':
        raise ValueError('not a PE image')
    if struct.unpack_from('<H', data, pe + 4)[0] != 0x14c or struct.unpack_from('<H', data, opt)[0] != 0x10b:
        raise ValueError('not the expected x86 PE32 image')
    if struct.unpack_from('<I', data, opt + 28)[0] != IMAGE_BASE:
        raise ValueError('unexpected image base')
    table = opt + struct.unpack_from('<H', data, pe + 20)[0]
    sections = []
    for i in range(struct.unpack_from('<H', data, pe + 6)[0]):
        vs, rva, rawsize, raw = struct.unpack_from('<IIII', data, table + 40 * i + 8)
        sections.append((IMAGE_BASE + rva, rawsize, raw))
    return opt, sections


def offset(sections, va, size=1):
    for start, count, raw in sections:
        if start <= va and va + size <= start + count:
            return raw + va - start
    raise ValueError('address is outside file-backed sections')


def checksum(data, field):
    value = 0
    for at in range(0, len(data), 2):
        if field <= at < field + 4:
            continue
        value += data[at] | ((data[at + 1] if at + 1 < len(data) else 0) << 8)
        value = (value & 0xffff) + (value >> 16)
    value = (value & 0xffff) + (value >> 16)
    return value + len(data)


def build(data):
    if sha(data) != INPUT_SHA256:
        raise ValueError('exact v124 input SHA-256 mismatch')
    opt, sections = headers(data)
    if len(sections) != 20 or len(data) != 111796224:
        raise ValueError('unexpected v124 layout')
    if any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('certificate requires a separate signing workflow')
    gate = bytes.fromhex('0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400')
    at = offset(sections, GATE_VA, len(gate))
    if data[at:at + len(gate)] != gate:
        raise ValueError('unexpected current receive gate')
    for opcode, (va, handler) in REGISTRATIONS.items():
        expected = b'\x6a\x00\x68' + struct.pack('<I', handler) + b'\x68' + struct.pack('<I', opcode)
        at = offset(sections, va, len(expected))
        if data[at:at + len(expected)] != expected:
            raise ValueError('unexpected existing receiver registration')
    out, edits = bytearray(data), []
    for opcode in OPCODES:
        at = offset(sections, BITMAP_VA + opcode // 8)
        mask = 1 << (opcode % 8)
        if data[at] != EXPECTED_BYTES[opcode] or data[at] & mask:
            raise ValueError('unexpected baseline receive bit')
        out[at] |= mask
        edits.append(dict(opcode=opcode, file_offset=at, before=data[at], after=out[at], mask=mask))
    struct.pack_into('<I', out, opt + 64, checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(input_sha256=INPUT_SHA256, output_sha256=sha(result), enabled_opcodes=list(OPCODES),
                        edits=edits, checksum_range=[opt + 64, opt + 68], full_ui_acceptance=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(result)
    if sha(source.read_bytes()) != INPUT_SHA256 or sha(destination.read_bytes()) != receipt['output_sha256']:
        raise RuntimeError('input/output changed')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
