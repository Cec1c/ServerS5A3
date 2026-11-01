"""Guard Cleansing Thunder target lookup after its scene is detached.

Produces a separate candidate. Does not launch the game or publish an update.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUTS = {
    '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24': 112541696,
    '6091f9ac92e588643226a8f61d313cb798b308d530bf3013cab0bda1d0cd0ece': 112545792,
}
SITES = (0x00D395F0, 0x00D396C5)
FINDER = 0x0287A000


def original(site):
    return bytes.fromhex('8bc8e8') + struct.pack('<i', FINDER - site - 7)


def helper(base):
    c = Code(base)
    c.emit('9c85c0')  # Keep incoming flags for the valid native lookup.
    c.branch('0f85', 'valid')
    c.emit('9dc20c00')  # NULL is already EAX; match the lookup's ret 12.
    c.label('valid')
    c.emit('9d8bc8')
    c.branch('e9', FINDER)  # Tail call preserves native arguments/return address.
    return c.finish()


def build(data):
    input_sha = pe.sha(data)
    if INPUTS.get(input_sha) != len(data):
        raise ValueError('exact verified cumulative input SHA256/size required')
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
    for site in SITES:
        at = pe.offset(sections, site, 7)
        if data[at:at + 7] != original(site):
            raise ValueError('Cleansing Thunder lookup fingerprint changed')
    for va, expected in (
        (0x020A88D0, bytes.fromhex('568bf18b06')),
        (0x0287A022, bytes.fromhex('8b5f102b5f0c')),
        (0x0467D1C8, struct.pack('<I', 0x00D39420)),
    ):
        off = pe.offset(sections, va, len(expected))
        if data[off:off + len(expected)] != expected:
            raise ValueError('callback or scene lookup fingerprint changed')
    base = pe.IMAGE_BASE + image_size
    code = helper(base)
    out = bytearray(data) + code + bytes(4096 - len(code))
    changes = []
    for site in SITES:
        at = pe.offset(sections, site, 7)
        out[at:at + 7] = b'\xe8' + struct.pack('<i', base - site - 5) + b'\x90\x90'
        changes.append((at, at + 7))
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.ctnull', len(code), image_size, 4096, len(data), 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<H', out, coff + 6, count + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, image_size + 4096)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    changes = sorted(changes + [(coff + 6, coff + 8), (opt + 4, opt + 8),
                      (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)])
    normalized = bytearray(out[:len(data)])
    for lo, hi in changes:
        normalized[lo:hi] = data[lo:hi]
    if normalized != data:
        raise ValueError('change outside approved ranges')
    return bytes(out), dict(inputSha256=input_sha, outputSha256=pe.sha(out), size=len(out),
                           helperVA=hex(base), helperSize=len(code), changedRanges=changes,
                           appendedRange=[len(data), len(out)], playerAcceptance='pending')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != receipt['inputSha256'] or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input/output readback mismatch')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
