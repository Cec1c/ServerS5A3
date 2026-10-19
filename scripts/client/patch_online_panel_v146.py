"""Bind event 228 to its native reward window and bound activity-list tags.

Accept only the exact published v145. Existing packet admission, notification
consumers, reward requests and all other event click routes remain unchanged.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = 'b65b60984c6740d416a643b4f66cc510988efe96c5dd8fc96f13410d39f1e6e6'
INPUT_SIZE = 111820800
CLICK = 0x23ee036
TAG = 0x23ed943


def rel(op, va, target):
    return op + struct.pack('<i', target - va - len(op) - 4)


def click_helper(va):
    code = bytearray.fromhex('813fe4000000750a b827000000')
    code += rel(b'\xe9', va + len(code), CLICK + 6)
    code += bytes.fromhex('8b8740010000')
    code += rel(b'\xe9', va + len(code), CLICK + 6)
    return bytes(code)


def tag_helper(va):
    code = bytearray.fromhex('8b4da4 85c9')
    code += rel(b'\x0f\x84', va + len(code), 0x23edac7)
    # The paired Event_tag.img has four frames, although catalog categories
    # extend through seven. Missing tag art must not suppress the row title.
    code += bytes.fromhex('8b4108 48 83f803')
    code += rel(b'\x0f\x87', va + len(code), 0x23ed9d6)
    code += bytes.fromhex('a1e4a38105')
    code += rel(b'\xe9', va + len(code), TAG + 5)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact published v145 required')
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    count = struct.unpack_from('<H', data, pe_at + 6)[0]
    table = opt + struct.unpack_from('<H', data, pe_at + 20)[0]
    header = table + count * 40
    if count != 26 or any(data[header:header + 40]) or header + 40 > 4096:
        raise ValueError('unexpected section layout')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096):
        raise ValueError('unexpected image alignment')
    expected = {CLICK: bytes.fromhex('8b8740010000'), TAG: bytes.fromhex('a1e4a38105')}
    for va, raw in expected.items():
        at = pe.offset(sections, va, len(raw))
        if data[at:at + len(raw)] != raw:
            raise ValueError('activity UI instruction fingerprint mismatch')
    va = pe.IMAGE_BASE + INPUT_SIZE
    click = click_helper(va)
    tag_va = va + 64
    tag = tag_helper(tag_va)
    code = click + bytes(64 - len(click)) + tag
    out = bytearray(data)
    out.extend(code + bytes(4096 - len(code)))
    allowed = []
    for site, target, length in ((CLICK, va, 6), (TAG, tag_va, 5)):
        at = pe.offset(sections, site, length)
        out[at:at + length] = rel(b'\xe9', site, target) + bytes([0x90]) * (length - 5)
        allowed.append((at, at + length))
    struct.pack_into('<H', out, pe_at + 6, count + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, INPUT_SIZE + 4096)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.online', len(code), INPUT_SIZE, 4096, INPUT_SIZE, 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    allowed += [(pe_at + 6, pe_at + 8), (opt + 4, opt + 8), (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)]
    assert all(a == b or any(lo <= i < hi for lo, hi in allowed) for i, (a, b) in enumerate(zip(data, out)))
    receipt = dict(input_sha256=INPUT_SHA256, output_sha256=hashlib.sha256(out).hexdigest(), output_size=len(out),
                   click_helper_va=hex(va), tag_helper_va=hex(tag_va), helper_length=len(code),
                   event_id=228, native_window_id=39, accepted_tag_types=list(range(1, 5)),
                   packet_admission_changed=False, packet_consumers_changed=False, ui_acceptance=False)
    return bytes(out), receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('input', 'output', 'receipt'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    if len({p.resolve() for p in (args.input, args.output, args.receipt)}) != 3 or args.output.exists() or args.receipt.exists():
        parser.error('immutable input and new distinct outputs required')
    result, receipt = build(args.input.read_bytes())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('xb') as stream:
        stream.write(result)
    with args.receipt.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
