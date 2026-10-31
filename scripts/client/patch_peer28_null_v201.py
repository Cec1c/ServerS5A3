"""Guard a missing equipment-slot object in the exact released V201 client.

Produces a separate candidate. Does not launch the game or publish an update.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = 'b965e2d1d04b4679eba1ba00de6bbceacdc1ccab8789ea22bef98a28f61283d5'
INPUT_SIZE = 112123904
SITE = 0x032A5F56
ORIGINAL = bytes.fromhex('8bf8 8b07 8b9060010000')
CONTINUE = SITE + len(ORIGINAL)
MISSING = 0x032AEE79


def helper(base):
    c = Code(base)
    c.emit('8bf8 9c 85ff')  # Original EDI assignment; save getter flags.
    c.branch('0f84', 'missing')
    c.emit('9d 8b07 8b9060010000')
    c.branch('e9', CONTINUE)
    c.label('missing')
    c.emit('9d')
    c.branch('e9', MISSING)  # Existing case cleanup, also used for missing actor.
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact released V201 input SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    header = table + count * 40
    if header + 40 > struct.unpack_from('<I', data, opt + 60)[0] or any(data[header:header + 40]):
        raise ValueError('no empty section header')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096) or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected alignment or certificate')
    image_size = struct.unpack_from('<I', data, opt + 56)[0]
    end = max(struct.unpack_from('<I', data, table + i * 40 + 12)[0] +
              max(struct.unpack_from('<I', data, table + i * 40 + 8)[0],
                  struct.unpack_from('<I', data, table + i * 40 + 16)[0]) for i in range(count))
    if image_size != (end + 4095) & ~4095 or len(data) % 4096 or max(raw + size for _, size, raw in sections) != len(data):
        raise ValueError('unexpected image end or overlay')
    at = pe.offset(sections, SITE, len(ORIGINAL))
    if data[at:at + len(ORIGINAL)] != ORIGINAL:
        raise ValueError('native fingerprint changed')
    switch = pe.offset(sections, 0x032AEEE4 + 28 * 4, 4)
    if struct.unpack_from('<I', data, switch)[0] != 0x032A5E9E:
        raise ValueError('opcode 28 dispatch changed')
    base = pe.IMAGE_BASE + image_size
    code = helper(base)
    out = bytearray(data) + code + bytes(4096 - len(code))
    out[at:at + len(ORIGINAL)] = b'\xe9' + struct.pack('<i', base - SITE - 5) + b'\x90' * (len(ORIGINAL) - 5)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.p28null', len(code), image_size, 4096, len(data), 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<H', out, coff + 6, count + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, image_size + 4096)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    changes = sorted([(at, at + len(ORIGINAL)), (coff + 6, coff + 8), (opt + 4, opt + 8),
                      (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)])
    normalized = bytearray(out[:len(data)])
    for lo, hi in changes:
        normalized[lo:hi] = data[lo:hi]
    if normalized != data:
        raise ValueError('change outside approved ranges')
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
                           helperVA=hex(base), helperSize=len(code), changedRanges=changes,
                           appendedRange=[len(data), len(out)], playerAcceptance='pending')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input/output readback mismatch')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
