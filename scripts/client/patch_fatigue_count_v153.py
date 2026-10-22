"""Admit notification 541 on the exact V153 client, retaining its gate chain."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = 'c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8'
INPUT_SIZE = 111857664
CALL_VA = 0x2523A13
OLD_GATE_VA = 0x6EAA000
HELPER_VA = OLD_GATE_VA + 0x50
SECTION_RVA = OLD_GATE_VA - pe.IMAGE_BASE
OLD_SECTION_SIZE = 0x4A


def helper():
    code = bytearray.fromhex('0fb7442404803f00')

    def fallback_unless_equal():
        code.extend(bytes.fromhex('0f85'))
        code.extend(struct.pack('<i', OLD_GATE_VA - (HELPER_VA + len(code) + 4)))

    fallback_unless_equal()
    code.extend(b'\x3d' + struct.pack('<I', 541))
    fallback_unless_equal()
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V153 SHA-256/length required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if struct.unpack_from('<I', data, table + i * 40 + 12)[0] == SECTION_RVA]
    if len(matches) != 1:
        raise ValueError('missing original job-change gate section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (data[header:header+8] != b'.jobgate' or vsize != OLD_SECTION_SIZE
            or rawsize != 4096 or struct.unpack_from('<I', data, header+36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt+128))):
        raise ValueError('unexpected original gate section or certificate')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', OLD_GATE_VA-CALL_VA-5):
        raise ValueError('unexpected receive call target')
    # Existing notification registration and daily counter reader remain native.
    checks = {
        0x203247D: bytes.fromhex('6840e20202681d020000'),
        0x202E2A4: bytes.fromhex('e817a17b01'),
        0x202E2AF: bytes.fromhex('6a0152e829a07b01'),
        0x25DC68E: bytes.fromhex('e8fdfeffff8b8e6c0400002bc8'),
    }
    for va, expected in checks.items():
        at = pe.offset(sections, va, len(expected))
        if data[at:at+len(expected)] != expected:
            raise ValueError(f'unexpected native count contract at {va:#x}')
    code = helper()
    at = pe.offset(sections, HELPER_VA, len(code))
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('existing gate padding is occupied')
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', out, header+8, HELPER_VA-OLD_GATE_VA+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    result = bytes(out)
    return result, {
        'input_sha256': INPUT_SHA256, 'output_sha256': pe.sha(result),
        'input_size': len(data), 'output_size': len(result),
        'class0_added': [541], 'call_va': hex(CALL_VA), 'helper_va': hex(HELPER_VA),
        'fallback_va': hex(OLD_GATE_VA), 'helper_size': len(code),
        'changed_ranges': [(opt+64, opt+68), (header+8, header+12),
                           (call, call+5), (at, at+len(code))],
        'shared_bitmap_unchanged': True, 'native_reader_unchanged': True,
        'full_ui_acceptance': False,
    }


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
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
