"""Distinguish an account's claimed envelope from global exhaustion on V189."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '7c64c854a02ecc1c3bb6ae7ee174733c5b2e5957fa8a76b88db2110fcc3df4ba'
INPUT_SIZE = 111988736
SITE = 0x11B23C8
ORIGINAL = bytes.fromhex('8b4b18 8b01 8b5008 6a00 ffd2')


def helper(base):
    c = Code(base)
    c.emit('9c 60 fc')  # Flags and all registers belong to the native renderer.
    c.emit('8d4598 50 8b8d70ffffff 83c140')
    c.branch('e8', 0x11B0BF0)  # Same selected history-vector row as native UI.
    c.emit('80782702')
    c.branch('0f85', 'original')
    c.emit('0fb64026 83ec20')  # Remaining portions; bounded UTF-16 stack string.
    for offset, value in ((0, '已领'), (4, ' 余')):
        c.emit('c74424'); c.data.append(offset); c.data.extend(value.encode('utf-16le'))
    c.emit('31d2 b90a000000 f7f1 8d7c2408 85c0')
    c.branch('0f84', 'units')
    c.emit('6683c030 66ab')
    c.label('units')
    c.emit('8bc2 6683c030 66ab b8fd4e0000 ab')  # Units, then 份 and NUL.
    c.emit('8bc4 50 8b4b18 8b11 ff92e4000000 83c420')
    c.emit('8b4b18 8b01 6a01 ff5008')  # Show the truthful status label.
    c.emit('8b4b20 8b01 6a00 ff5008')  # Hide the exhausted stamp.
    c.emit('61 9d')
    c.branch('e9', 0x11B23E0)
    c.label('original')
    c.emit('61 9d'); c.data.extend(ORIGINAL)
    c.branch('e9', SITE + len(ORIGINAL))
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V189 input SHA256/size required')
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
    edits = [(0xF492F0, bytes.fromhex('807e2701'), bytes.fromhex('807e2700')),
             (0xF49334, bytes.fromhex('741a'), bytes.fromhex('751a')),
             (SITE, ORIGINAL, b'\xe9' + struct.pack('<i', base-SITE-5) + b'\x90'*(len(ORIGINAL)-5))]
    for va, old, new in edits:
        at = pe.offset(sections, va, len(old))
        if data[at:at+len(old)] != old:
            raise ValueError('native fingerprint changed')
        out[at:at+len(new)] = new
        changes.append((at, at+len(new)))
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.hbstate', len(code), image_size, 4096, len(data), 0, 0, 0, 0, 0x60000020)
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
