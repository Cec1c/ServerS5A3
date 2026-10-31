"""Explain unavailable black-diamond entitlement on the cumulative hongbao image."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '8ded3250c450309c91984e58216cb806cc1e3d5c35682626f8a11c34a26e6cb5'
INPUT_SIZE = 111992832
SITE = 0x1FE6D39
ORIGINAL = bytes.fromhex('0fb64510 48')
MESSAGE = '需要有效的黑钻资格才能使用此售货机。'


def helper(base):
    c = Code(base)
    c.emit('0fb64510 3c17')
    c.branch('0f85', 'original')
    c.emit('9c 60')
    c.emit('a198376a05 85c0')
    c.branch('0f84', 'done')
    c.emit('8b888c000000 85c9')
    c.branch('0f84', 'message')
    c.emit('6a00')
    c.branch('e8', 0x3305920)
    c.label('message')
    c.emit('6a00 68')
    pointer = len(c.data); c.data.extend(bytes(4))
    c.emit('8b0d98376a05 68d6020000')
    c.branch('e8', 0x32BEA50)
    c.label('done')
    c.emit('61 9d')
    c.branch('e9', 0x1FE6D8C)
    c.label('original')
    c.emit('48')
    c.branch('e9', SITE+len(ORIGINAL))
    struct.pack_into('<I', c.data, pointer, base+len(c.data))
    c.data.extend((MESSAGE+'\0').encode('utf-16le'))
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative hongbao-state input SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    header = table + count * 40
    if header + 40 > struct.unpack_from('<I', data, opt + 60)[0] or any(data[header:header+40]):
        raise ValueError('no empty section header')
    if struct.unpack_from('<II', data, opt+32) != (4096, 4096) or any(struct.unpack_from('<II', data, opt+128)):
        raise ValueError('unexpected PE alignment or certificate')
    image_size = struct.unpack_from('<I', data, opt+56)[0]
    end = max(struct.unpack_from('<I', data, table+i*40+12)[0] + max(struct.unpack_from('<I', data, table+i*40+8)[0], struct.unpack_from('<I', data, table+i*40+16)[0]) for i in range(count))
    if image_size != (end+4095)&~4095 or len(data)%4096 or max(raw+size for _,size,raw in sections) != len(data):
        raise ValueError('unexpected image end or overlay')
    base = pe.IMAGE_BASE + image_size
    code = helper(base)
    out = bytearray(data) + code + bytes(4096-len(code))
    changes = [(opt+64, opt+68), (coff+6, coff+8), (opt+4, opt+8), (opt+56, opt+60), (header, header+40), (len(data), len(out))]
    edits = [(SITE, ORIGINAL, b'\xe9' + struct.pack('<i', base-SITE-5))]
    for va, old, new in edits:
        at = pe.offset(sections, va, len(old))
        if data[at:at+len(old)] != old:
            raise ValueError('native fingerprint changed')
        out[at:at+len(new)] = new
        changes.append((at, at+len(new)))
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.vippend', len(code), image_size, 4096, len(data), 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<H', out, coff+6, count+1)
    struct.pack_into('<I', out, opt+4, struct.unpack_from('<I', data, opt+4)[0]+4096)
    struct.pack_into('<I', out, opt+56, image_size+4096)
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    result = bytes(out)
    return result, dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(result), size=len(result), helperVA=hex(base), changedRanges=sorted(changes), playerAcceptance='pending')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('source or candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
