"""Add display-only result rows to the exact cumulative V209 client.

Slot FFFF is a paired-server sentinel, never an inventory slot. The two native
disassembly receivers copy a temporary item's icon/name into the result window
and immediately destroy the temporary item. The ordinary reward path is intact.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '478f4f5dc070253e7679aa7d8f8fda3b71ea6376569fc794865c44d0107098f4'
APPROVED_INPUTS = {INPUT_SHA256, '7d4a57277b464a05ab85bb46f19c9dbceabad03403258d3bbd3bf689aa03d102'}
INPUT_SIZE = 112549888
SITES = ((26, 0x1FD9D29, 0x1FD9E4F), (235, 0x1FDC806, 0x1FDC92C))
ORIGINAL = bytes.fromhex('8b8514fdffff')


def helper(base, site, next_row):
    c = Code(base)
    c.emit('9c 6683bd18fdffffff')  # saved flags; cmp word [ebp-2e8],FFFF
    c.branch('0f85', 'ordinary')
    c.emit('9d 83c40c 9c 60')  # retire three cdecl reader arguments
    c.emit('83bd14fdffff00')
    c.branch('0f84', 'done')
    c.emit('83bd24fdffff00')
    c.branch('0f84', 'done')
    c.emit('ffb524fdffff 8d8d2cfdffff')
    c.branch('e8', 0x24319C0)  # count value, thiscall, ret4
    c.emit('c645fc04 6a00 6a01 50 ffb514fdffff')
    c.branch('e8', 0x2A32B30)  # temporary native item; cdecl
    c.emit('83c410 8bf0 c645fc03 8d8d2cfdffff')
    c.branch('e8', 0x985650)  # count value destructor
    c.emit('85f6')
    c.branch('0f84', 'done')
    c.emit('8b8d1cfdffff ffb524fdffff 56')
    c.branch('e8', 0x33C82E0)  # copies the row into the result UI; ret8
    c.emit('8b06 6a01 8bce ff500c')  # deleting destructor, virtual +0x0c
    c.label('done')
    c.emit('61 9d')
    c.branch('e9', next_row)
    c.label('ordinary')
    c.emit('9d')
    c.data.extend(ORIGINAL)
    c.branch('e9', site + len(ORIGINAL))
    return c.finish()


def build(data):
    identity = pe.sha(data)
    if len(data) != INPUT_SIZE or identity not in APPROVED_INPUTS:
        raise ValueError('exact cumulative V209 SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    slots = [table + i*40 for i in range(count)
             if data[table + i*40:table + i*40 + 8] == b'.ctnull\0']
    if len(slots) != 1:
        raise ValueError('audited RX padding section missing')
    header = slots[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if ((vsize, rva, rawsize) != (0x29f, 0x6b8b000, 0x1000)
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or any(data[raw + vsize:raw + rawsize])):
        raise ValueError('audited unused padding changed')
    code, hooks, ranges = bytearray(), [], []
    for opcode, site, next_row in SITES:
        at = pe.offset(sections, site, len(ORIGINAL))
        if data[at:at + len(ORIGINAL)] != ORIGINAL:
            raise ValueError('native reward reader fingerprint changed')
        va = pe.IMAGE_BASE + rva + 0x400 + len(code)
        body = helper(va, site, next_row)
        hooks.append((at, site, va))
        code.extend(body)
        ranges.append([at, at + len(ORIGINAL)])
    if 0x400 + len(code) > rawsize:
        raise ValueError('helper page overflow')
    out = bytearray(data)
    out[raw + 0x400:raw + 0x400 + len(code)] = code
    for at, site, va in hooks:
        out[at:at + 6] = b'\xe9' + struct.pack('<i', va - site - 5) + b'\x90'
    struct.pack_into('<I', out, header + 8, 0x400 + len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    ranges.extend([[opt + 64, opt + 68], [header + 8, header + 12], [raw + 0x400, raw + 0x400 + len(code)]])
    restored = bytearray(out[:len(data)])
    for lo, hi in ranges:
        restored[lo:hi] = data[lo:hi]
    if restored != data:
        raise ValueError('unexpected mutation outside approved ranges')
    return bytes(out), dict(inputSha256=identity, outputSha256=pe.sha(out), size=len(out),
                           changedRanges=sorted(ranges),
                           helpers=[dict(opcode=s[0], va=h[2]) for s, h in zip(SITES, hooks)],
                           inventoryChanged=False, playerAcceptance='pending')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    if a.input.resolve() == a.output.resolve() or a.output.exists():
        raise ValueError('output must be a separate new file')
    output, receipt = build(a.input.read_bytes())
    with a.output.open('xb') as stream:
        stream.write(output)
    if pe.sha(a.input.read_bytes()) != receipt['inputSha256'] or a.output.read_bytes() != output:
        raise RuntimeError('input/output readback mismatch')
    print(json.dumps(receipt, indent=2))
