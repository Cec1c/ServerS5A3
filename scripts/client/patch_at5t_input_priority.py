"""Give an eligible nearby owned AT-5T the first native input evaluation.

Accepts the exact official v147 image. Produces a separate offline candidate;
does not publish assets, modify PVF, or change any server configuration.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = 'f17d9289658664f1a61069f322ace28608d24f93f1af30f81698d534ab4a8484'
INPUT_SIZE = 111828992
SITE = 0x03763B50
FINGERPRINT = bytes.fromhex('55 8bec 6aff')
LOOP_SITE = 0x03763BF0
LOOP_FINGERPRINT = bytes.fromhex('8d4dc8 51 8d4f6c')
LOCAL_ACTOR = 0x029610E0
VEHICLE_VTABLE = 0x04B2168C
EVALUATOR = 0x03761C20


class Code:
    """Small deterministic rel32 emitter; instruction bytes stay reviewable."""
    def __init__(self, base):
        self.base, self.data, self.labels, self.fixups = base, bytearray(), {}, []

    def emit(self, hex_bytes):
        self.data.extend(bytes.fromhex(hex_bytes))

    def label(self, name):
        if name in self.labels:
            raise ValueError('duplicate label')
        self.labels[name] = self.base + len(self.data)

    def branch(self, opcode, target):
        self.emit(opcode)
        self.fixups.append((len(self.data), target))
        self.data.extend(bytes(4))

    def finish(self):
        for offset, target in self.fixups:
            address = self.labels[target] if isinstance(target, str) else target
            struct.pack_into('<i', self.data, offset, address - self.base - offset - 4)
        return bytes(self.data)


def helper(base):
    c = Code(base)
    # thiscall wrapper, no arguments. Locals: reserved, chosen slot, phase.
    # Keep per-invocation state outside the native SEH frame and its local offsets.
    c.label('wrapper')
    c.emit('55 8bec 83ec0c c745f800000000 c745f400000000')
    c.branch('e8', 'original')
    c.label('after_poll')
    c.emit('8be5 5d c3')
    c.label('original')
    c.data.extend(FINGERPRINT)
    c.branch('e9', SITE + len(FINGERPRINT))

    # Enter after the native hardware producer and begin-iterator construction.
    # Only the stack iterator's current slot changes. Cache slots and every
    # hardware-history reference into that cache remain unchanged throughout.
    c.label('loop')
    c.emit('9c 60 8b5d00 837bf400')
    c.branch('0f85', 'continue_order')
    c.emit('8bcf')
    c.branch('e8', 'choose')
    c.emit('8943f8 c743f401000000 85c0')
    c.branch('0f84', 'native_loop')
    c.emit('8945dc')             # native iterator +8 = selected cache row
    c.branch('e9', 'native_loop')
    c.label('continue_order')
    c.emit('8b53f8 85d2')
    c.branch('0f84', 'native_loop')
    c.emit('837bf401')
    c.branch('0f85', 'skip_chosen')
    c.emit('c743f402000000 8b4770 8945dc')
    c.label('skip_chosen')
    c.emit('3955dc')
    c.branch('0f85', 'native_loop')
    c.emit('8345dc08')
    c.label('native_loop')
    c.emit('61 9d')
    c.data.extend(LOOP_FINGERPRINT)
    c.branch('e9', LOOP_SITE + len(LOOP_FINGERPRINT))

    # Return a cache slot only for the local actor's concrete native input.
    # Locals: descriptor, scene, scene end, current vehicle.
    c.label('choose')
    c.emit('55 8bec 83ec10 53 56 57 8bd9')
    c.branch('e8', LOCAL_ACTOR)
    c.emit('85c0')
    c.branch('0f84', 'none')
    c.emit('8bf0 8b06 8bce ff90180d0000 3bc3')
    c.branch('0f85', 'none')
    c.emit('8b03 817874201c7603')  # native descriptor evaluator, no custom callback
    c.branch('0f85', 'none')
    c.emit('8b8b9c000000 53')
    c.branch('e8', 0x02A122A0)     # replay/modal-managed inputs keep native order
    c.emit('84c0')
    c.branch('0f85', 'none')
    # Only a new physical/logical press can need interaction arbitration.
    # Release/hold continue through the original loop without calling class code.
    c.emit('8b4338 8b533c')
    c.label('fresh_row')
    c.emit('3bc2')
    c.branch('0f83', 'none')
    c.emit('80780c00')
    c.branch('0f84', 'next_row')
    c.emit('80780d00')
    c.branch('0f85', 'has_press')
    c.label('next_row')
    c.emit('83c018')
    c.branch('e9', 'fresh_row')
    c.label('has_press')
    c.emit('8b86e03d0000 85c0')
    c.branch('0f84', 'none')
    c.emit('8945fc 80783000')
    c.branch('0f84', 'none')
    c.emit('83be1067000000')       # actor ride
    c.branch('0f85', 'none')
    c.emit('80be7069000000')       # actor ride lock
    c.branch('0f85', 'none')
    c.emit('80bee467000001')       # native entry gate 0276C040
    c.branch('0f84', 'none')
    c.emit('8b86c8000000 85c0')
    c.branch('0f84', 'none')
    c.emit('8945f8 8b780c 8b4010 8945f4')
    c.label('vehicle')
    c.emit('3b7df4')
    c.branch('0f83', 'none')
    c.emit('8b0f 85c9')
    c.branch('0f84', 'next')
    c.emit('81398c16b204')
    c.branch('0f85', 'next')
    c.emit('81b90004000033ef0000')  # marker 61235
    c.branch('0f85', 'next')
    c.emit('39b108090000')
    c.branch('0f85', 'next')
    c.emit('8b45f8 3981c8000000')
    c.branch('0f85', 'next')
    c.emit('83b9605f000001')
    c.branch('0f87', 'next')
    for raw in ('83b9845c000000', '83b9d801000000', '80b9645f000000'):
        c.emit(raw)
        c.branch('0f85', 'next')
    c.emit('894df0 8b01 ff90ec000000 83f814')
    c.branch('0f84', 'next')
    c.emit('83f816')
    c.branch('0f84', 'next')
    c.emit('8b4df0')
    c.branch('e8', 0x02676330)     # original local object gate
    c.emit('84c0')
    c.branch('0f84', 'next')
    c.emit('8b4df0 8b01 6a00 56 ff500c 83f878')
    c.branch('0f8d', 'next')

    # Invoke class-specific eligibility only after finding an owned nearby AT.
    c.emit('8b06 8bce ff90ac0c0000 85c0')
    c.branch('0f85', 'none')
    # Same two native vertical-component comparisons as 02D74083..02D7410D.
    # Keep x87 balanced on every branch; positive velocity/height or NaN reject.
    c.emit('8b06 8bce ff505c 85c0')
    c.branch('0f84', 'none')
    c.emit('8bc8')
    c.branch('e8', 0x0288E9F0)
    c.emit('85c0')
    c.branch('0f84', 'none')
    c.emit('8bf8 8b07 6a02 8bcf ff500c d9ee dff1 ddd8')
    c.branch('0f82', 'none')
    c.emit('8b07 6a02 8bcf ff10 d9ee d9c9 dfe9 ddd8 9f f6c444')
    c.branch('0f8a', 'none')

    # Return the existing row address; never change cache bytes or ownership.
    c.emit('8b4370 8b5374 8b4dfc')
    c.label('find')
    c.emit('3bc2')
    c.branch('0f83', 'none')
    c.emit('3908')
    c.branch('0f84', 'chosen')
    c.emit('83c008')
    c.branch('e9', 'find')
    c.label('next')
    c.emit('83c704')
    c.branch('e9', 'vehicle')
    c.label('none')
    c.emit('33c0')
    c.label('chosen')
    c.emit('5f 5e 5b 8be5 5d c3')
    return c.finish(), c.labels


def build(data):
    if len(data) != INPUT_SIZE or hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact official v147 input SHA256/length required')
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    count = struct.unpack_from('<H', data, pe_at + 6)[0]
    header = opt + struct.unpack_from('<H', data, pe_at + 20)[0] + count * 40
    if count != 28 or header + 40 > 4096 or any(data[header:header + 40]):
        raise ValueError('unexpected PE section layout')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096):
        raise ValueError('unexpected image alignment')
    sites = [(SITE,FINGERPRINT), (LOOP_SITE,LOOP_FINGERPRINT)]
    for address, raw in sites:
        offset = pe.offset(sections, address, len(raw))
        if data[offset:offset + len(raw)] != raw:
            raise ValueError('native input poll fingerprint mismatch')
    base = pe.IMAGE_BASE + INPUT_SIZE
    code, labels = helper(base)
    if len(code) > 4096:
        raise ValueError('helper exceeds new section')
    out = bytearray(data)
    out.extend(code + bytes(4096 - len(code)))
    for (address, raw), target in zip(sites, (base, labels['loop'])):
        offset = pe.offset(sections, address, len(raw))
        out[offset:offset+len(raw)] = b'\xe9' + struct.pack('<i', target-address-5) + b'\x90'*(len(raw)-5)
    struct.pack_into('<H', out, pe_at + 6, count + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, INPUT_SIZE + 4096)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.at5tpri', len(code), INPUT_SIZE,
                     4096, INPUT_SIZE, 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    allowed = [(pe.offset(sections,a,len(b)),pe.offset(sections,a,len(b))+len(b)) for a,b in sites]
    allowed += [(pe_at + 6, pe_at + 8), (opt + 4, opt + 8),
               (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)]
    cursor = 0
    for lo, hi in sorted(allowed):
        assert data[cursor:lo] == out[cursor:lo]
        cursor = hi
    assert data[cursor:] == out[cursor:len(data)]
    receipt = dict(input_sha256=INPUT_SHA256, output_sha256=hashlib.sha256(out).hexdigest(),
                   output_size=len(out), helper_va=hex(base), helper_length=len(code),
                   labels={k: hex(v) for k, v in labels.items()},
                   site_va=hex(SITE), loop_site_va=hex(LOOP_SITE), cache_bytes_unchanged=True, pvf_changed=False,
                   native_mount_guards_changed=False, ui_acceptance=False)
    return bytes(out), receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('input', 'output', 'receipt'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    paths = [args.input.resolve(), args.output.resolve(), args.receipt.resolve()]
    if len(set(paths)) != 3 or args.output.exists() or args.receipt.exists():
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
