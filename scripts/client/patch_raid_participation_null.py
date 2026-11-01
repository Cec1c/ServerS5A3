"""Keep raid participation state updates when the local own-raid is absent.

Only skips the current row's optional announcement/UI tail. The native row
parser, participation mutation, next-row loop and vector cleanup remain intact.
"""
import argparse
import struct
from pathlib import Path
import json

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUTS = {
    '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24': 112541696,
    'b63d35bb28ba70353af5388cdaea4f2f8294cacc5064fcbb31db175519a02783': 112549888,
}
SITE = 0x01FF4CAE
RESUME = SITE + 6
NEXT_ROW = 0x01FF4E3D
ORIGINAL = bytes.fromhex('8b95e0feffff')


def helper(base):
    c = Code(base)
    c.emit('9c85c0')
    c.branch('0f84', 'absent')
    c.emit('9d' + ORIGINAL.hex())
    c.branch('e9', RESUME)
    c.label('absent')
    c.emit('9d')
    c.branch('e9', NEXT_ROW)
    return c.finish()


def build(data):
    digest = pe.sha(data)
    if INPUTS.get(digest) != len(data):
        raise ValueError('exact verified cumulative input SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    header = table + count * 40
    empty_header = header + 40 <= struct.unpack_from('<I', data, opt + 60)[0] and not any(data[header:header + 40])
    reuse_v203_padding = digest == 'b63d35bb28ba70353af5388cdaea4f2f8294cacc5064fcbb31db175519a02783'
    if not empty_header and not reuse_v203_padding:
        raise ValueError('no empty section header')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096) or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected alignment or certificate')
    image_size = struct.unpack_from('<I', data, opt + 56)[0]
    end = max(struct.unpack_from('<I', data, table + i * 40 + 12)[0] +
              max(struct.unpack_from('<I', data, table + i * 40 + 8)[0],
                  struct.unpack_from('<I', data, table + i * 40 + 16)[0]) for i in range(count))
    if image_size != (end + 4095) & ~4095 or len(data) % 4096 or max(raw + size for _, size, raw in sections) != len(data):
        raise ValueError('unexpected image end or overlay')
    for va, expected in ((SITE, ORIGINAL),
                         (0x01FF4CA3, bytes.fromhex('8b0d78376a05e83207a6ff')),
                         (0x01FF4CC7, bytes.fromhex('0fb640030fb709')),
                         (NEXT_ROW, bytes.fromhex('8b85ccfeffff'))):
        at = pe.offset(sections, va, len(expected))
        if data[at:at + len(expected)] != expected:
            raise ValueError('participation callback fingerprint changed')
    if reuse_v203_padding:
        # V203 fills the PE header table. Its exact .ctnull section contains
        # 21 verified bytes and zero raw padding. Keep that existing helper
        # intact; allocate our independent helper at the next 16-byte boundary.
        last = table + (count - 1) * 40
        name, used, rva, raw_size, raw = struct.unpack_from('<8sIIII', data, last)
        flags = struct.unpack_from('<I', data, last + 36)[0]
        if name != b'.ctnull\0' or used != 21 or raw_size != 4096 or raw + raw_size != len(data) or flags != 0x60000020:
            raise ValueError('unexpected exact V203 last-section layout')
        offset = (used + 15) & ~15
        base = pe.IMAGE_BASE + rva + offset
        code = helper(base)
        if offset + len(code) > raw_size or any(data[raw + used:raw + raw_size]):
            raise ValueError('V203 helper padding is not empty')
        out = bytearray(data)
        out[raw + offset:raw + offset + len(code)] = code
        struct.pack_into('<I', out, last + 8, offset + len(code))
        allocation_ranges = [(last + 8, last + 12), (raw + offset, raw + offset + len(code))]
    else:
        base = pe.IMAGE_BASE + image_size
        code = helper(base)
        out = bytearray(data) + code + bytes(4096 - len(code))
        struct.pack_into('<8sIIIIIIHHI', out, header, b'.raidnul', len(code), image_size, 4096, len(data), 0, 0, 0, 0, 0x60000020)
        struct.pack_into('<H', out, coff + 6, count + 1)
        struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
        struct.pack_into('<I', out, opt + 56, image_size + 4096)
        allocation_ranges = [(coff + 6, coff + 8), (opt + 4, opt + 8), (opt + 56, opt + 60), (header, header + 40)]
    at = pe.offset(sections, SITE, len(ORIGINAL))
    out[at:at + 6] = b'\xe9' + struct.pack('<i', base - SITE - 5) + b'\x90'
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    ranges = sorted([(at, at + 6), (opt + 64, opt + 68)] + allocation_ranges)
    normalized = bytearray(out[:len(data)])
    for lo, hi in ranges:
        normalized[lo:hi] = data[lo:hi]
    if normalized != data:
        raise ValueError('change outside approved ranges')
    return bytes(out), dict(inputSha256=digest, outputSha256=pe.sha(out), size=len(out),
                           helperVA=hex(base), helperSize=len(code), changedRanges=ranges,
                           appendedRange=[len(data), len(out)],
                           allocation='verified_v203_last_section_padding' if reuse_v203_padding else 'new_section',
                           playerAcceptance='pending')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    if args.input.resolve() == args.output.resolve() or args.output.exists():
        raise ValueError('output must be a separate new file')
    data = args.input.read_bytes()
    result, receipt = build(data)
    with args.output.open('xb') as stream:
        stream.write(result)
    if args.input.read_bytes() != data or pe.sha(args.output.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input/output readback mismatch')
    print(json.dumps(receipt, indent=2))


if __name__ == '__main__':
    main()
