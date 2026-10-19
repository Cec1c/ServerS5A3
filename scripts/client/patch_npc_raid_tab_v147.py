"""Opt in to the native raid tab with [select channel window] 3.

The existing zero-valued role keeps its special-area default. Only the exact
v146 input is accepted; the role getter and native channel window are reused.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = 'e74ea0b98810921f80e4636388d4e8b17c18565266d97e0e3d514e55ad36eb8c'
INPUT_SIZE = 111824896
SITE = 0x354d7fc
GETTER = 0x29227f0
OPEN = 0x32bea50
RETURN = 0x354d810
FINGERPRINT = bytes.fromhex('8b0d98376a05 6a02 6a00 682f040000 e84012d7ff')


def rel(op, at, target):
    return op + struct.pack('<i', target - at - len(op) - 4)


def helper(at):
    # ESI is the NPC role object, as in the adjacent native 0x78 handler.
    code = bytearray.fromhex('68a3000000 8bce')
    code += rel(b'\xe8', at + len(code), GETTER)
    # Only the new opt-in value changes the existing behavior.
    code += bytes.fromhex('83f803 7405 b802000000')
    code += bytes.fromhex('50 6a00 682f040000 8b0d98376a05')
    code += rel(b'\xe8', at + len(code), OPEN)
    code += rel(b'\xe9', at + len(code), RETURN)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact v146 input SHA256/length required')
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    count = struct.unpack_from('<H', data, pe_at + 6)[0]
    table = opt + struct.unpack_from('<H', data, pe_at + 20)[0]
    header = table + count * 40
    if count != 27 or any(data[header:header + 40]) or header + 40 > 4096:
        raise ValueError('unexpected section layout')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096):
        raise ValueError('unexpected image alignment')
    site = pe.offset(sections, SITE, len(FINGERPRINT))
    if data[site:site + len(FINGERPRINT)] != FINGERPRINT:
        raise ValueError('native C2 instruction fingerprint mismatch')
    # Pin the native role-object source and getter convention as well.
    for va, raw in ((0x3549888, bytes.fromhex('ffd2 8bf0')),
                    (0x354cbff, bytes.fromhex('6a78 8bce e8e85b3dff'))):
        offset = pe.offset(sections, va, len(raw))
        if data[offset:offset + len(raw)] != raw:
            raise ValueError('native role-object/getter fingerprint mismatch')
    va = pe.IMAGE_BASE + INPUT_SIZE
    code = helper(va)
    out = bytearray(data)
    out.extend(code + bytes(4096 - len(code)))
    out[site:site + len(FINGERPRINT)] = rel(b'\xe9', SITE, va) + b'\x90' * (len(FINGERPRINT) - 5)
    struct.pack_into('<H', out, pe_at + 6, count + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, INPUT_SIZE + 4096)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.npctab', len(code), INPUT_SIZE, 4096, INPUT_SIZE, 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    allowed = [(site, site + len(FINGERPRINT)), (pe_at + 6, pe_at + 8),
               (opt + 4, opt + 8), (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)]
    cursor = 0
    for lo, hi in sorted(allowed):
        assert data[cursor:lo] == out[cursor:lo]
        cursor = hi
    assert data[cursor:] == out[cursor:len(data)]
    receipt = dict(input_sha256=INPUT_SHA256, output_sha256=hashlib.sha256(out).hexdigest(),
                   output_size=len(out), helper_va=hex(va), helper_length=len(code),
                   role=0xa3, opt_in_parameter=3, native_window=0x42f,
                   legacy_parameter=2, raid_tab_index=2, packet_admission_changed=False,
                   eligibility_changed=False, ui_acceptance=False)
    return bytes(out), receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('input', 'output', 'receipt'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    if len({p.resolve() for p in (args.input, args.output, args.receipt)}) != 3 or args.output.exists() or args.receipt.exists():
        parser.error('immutable input and distinct new outputs required')
    data, receipt = build(args.input.read_bytes())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('xb') as stream:
        stream.write(data)
    with args.receipt.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
