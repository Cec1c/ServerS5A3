"""Reject worn reinforcement/amplification targets before the native op80 send.

Reads an exact audited cumulative image and writes a separate candidate. Reuses the
native modal and dstr strings; does not change server, PVF, dstr or player files.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from patch_receivers_v128 import headers, offset, checksum

APPROVED_INPUTS = {
    'eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03': 112549888,
    '67f524e2c25b8ff0d57b10bf735510ddaac95965c143e11d404b144d7620327d': 112627712,
    'c3faebd7bcab6f45a6aa6b361c7388e061c98b9791fd33003f4390ed86d0430a': 112627712,
    # V208 adds only op817/818; the second input also enables op602.
    '5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c': 112549888,
    '27f66d19ea80f18abbe7d342d78cbc908b79d9c9c3691b109c1a958cd2492b85': 112549888,
}
PREVIOUS_INPUT = 'c3faebd7bcab6f45a6aa6b361c7388e061c98b9791fd33003f4390ed86d0430a'
PREVIOUS_HELPER = bytes.fromhex('837d94030f8536000000837d90000f8c2c000000837d10010f8722000000837d10000f850c0000006a006873060000e9d52967fb6a006836530000e9c92967fb538bcee8c81c66fbe92a2a67fb')
HOOK = 0x25FDB6F
RESUME = 0x25FDB77
MATERIAL_LOOKUP = 0x25ECE10
POPUP = 0x25FDB09
RETURN_FALSE = 0x25FDB24
REINFORCE_STRING = 1651
AMPLIFY_STRING = 21302
CALLERS = (
    # Preserve this caller's saved EBX before its existing return cleanup.
    (0x17F3C2A, bytes.fromhex('5b84c07520'), 0x17F3C2F, 0x17F3C4F, 0x17F3C4F, True),
    # A rejected coupon must clear selection, not enter the sent/waiting state.
    (0x331234E, bytes.fromhex('84c00f85eb000000'), 0x3312356, 0x3312441, 0x3312375, False),
)


def relative(at, target, opcode=b'\xe9'):
    return opcode + struct.pack('<i', target - at - len(opcode) - 4)


def helper(va):
    code = bytearray()
    fixups, labels = [], {}
    def branch(op, label):
        fixups.append((len(code), op, label))
        code.extend(op + bytes(4))
    code.extend(bytes.fromhex('837d9403'))  # resolved target list, not a slot guess
    branch(b'\x0f\x85', 'original')
    code.extend(bytes.fromhex('837d9000'))  # target lookup must actually succeed
    branch(b'\x0f\x8c', 'original')
    code.extend(bytes.fromhex('837d1001'))  # modes 0/1 only; preserve other actions
    branch(b'\x0f\x87', 'original')
    code.extend(bytes.fromhex('837d1000'))
    branch(b'\x0f\x85', 'amplify')
    for name, string_id in [('reinforce', REINFORCE_STRING), ('amplify', AMPLIFY_STRING)]:
        labels[name] = len(code)
        code.extend(b'\x6a\x00\x68' + struct.pack('<I', string_id))
        branch(b'\xe9', 'show')
    labels['show'] = len(code)
    code.extend(relative(va + len(code), 0x39A6620, b'\xe8'))
    code.extend(bytes.fromhex('8b0d98376a0583c4045068d6020000'))
    code.extend(relative(va + len(code), 0x32BEA50, b'\xe8'))
    # AL remains false. The two coupon callers recognize only this handled
    # rejection; ordinary false returns still show their original busy popup.
    # The audited security-cookie epilogue preserves EAX.
    code.extend(b'\xb8' + struct.pack('<I', va))
    code.extend(relative(va + len(code), RETURN_FALSE))
    labels['original'] = len(code)
    code.extend(bytes.fromhex('538bce'))
    code.extend(relative(va + len(code), MATERIAL_LOOKUP, b'\xe8'))
    code.extend(relative(va + len(code), RESUME))
    for at, op, label in fixups:
        code[at:at + len(op) + 4] = relative(va + at, va + labels[label], op)
    return bytes(code)


def caller_helper(va, handled, caller):
    _, _, busy, success, reject, restore_ebx = caller
    code = bytearray(b'\x5b' if restore_ebx else b'')
    code.extend(b'\x3d' + struct.pack('<I', handled))
    normal = len(code)
    code.extend(bytes(6))
    code.extend(bytes.fromhex('31c0'))
    code.extend(relative(va + len(code), reject))
    target = va + len(code)
    code[normal:normal + 6] = relative(va + normal, target, b'\x0f\x85')
    code.extend(bytes.fromhex('84c0'))
    code.extend(relative(va + len(code), success, b'\x0f\x85'))
    code.extend(relative(va + len(code), busy))
    return bytes(code)


def build(data):
    identity = hashlib.sha256(data).hexdigest()
    if APPROVED_INPUTS.get(identity) != len(data):
        raise ValueError('exact audited cumulative SHA-256 and length required')
    opt, sections = headers(data)
    pe = struct.unpack_from('<I', data, 0x3c)[0]
    table = opt + struct.unpack_from('<H', data, pe + 20)[0]
    slots = [table + 40*i for i in range(len(sections))
             if data[table + 40*i:table + 40*i + 8] == b'.ctnull\0']
    if len(slots) != 1:
        raise ValueError('audited RX padding section missing')
    h = slots[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, h + 8)
    previous = identity == PREVIOUS_INPUT
    padding = bytearray(data[raw + 0x3b:raw + rawsize])
    if previous:
        if data[raw + 0x100:raw + 0x100 + len(PREVIOUS_HELPER)] != PREVIOUS_HELPER:
            raise ValueError('previous guard changed')
        padding[0x100 - 0x3b:0x100 - 0x3b + len(PREVIOUS_HELPER)] = bytes(len(PREVIOUS_HELPER))
    if (vsize != (0x100 + len(PREVIOUS_HELPER) if previous else 0x3b)
            or rawsize != 0x1000 or rva != 0x6b8b000
            or struct.unpack_from('<I', data, h + 36)[0] != 0x60000020 or any(padding)):
        raise ValueError('audited RX padding changed')
    va = 0x400000 + rva + 0x100
    code = helper(va)
    if 0x100 + len(code) > rawsize:
        raise ValueError('helper exceeds RX padding')
    at = offset(sections, HOOK, 8)
    original = (relative(HOOK, va) + b'\x90'*3 if previous else
                bytes.fromhex('538bce') + relative(HOOK + 3, MATERIAL_LOOKUP, b'\xe8'))
    if data[at:at + 8] != original:
        raise ValueError('native material lookup site changed')
    out = bytearray(data)
    out[at:at + 8] = relative(HOOK, va) + b'\x90'*3
    out[raw + 0x100:raw + 0x100 + len(code)] = code
    ranges = [[at, at+8], [h+8, h+12], [raw+0x100, raw+0x100+len(code)], [opt+64, opt+68]]
    end = 0x100 + len(code)
    for i, caller in enumerate(CALLERS):
        hook, expected, *_ = caller
        site = offset(sections, hook, len(expected))
        if data[site:site + len(expected)] != expected:
            raise ValueError('audited coupon caller changed')
        slot = 0x200 + i*0x80
        thunk = caller_helper(0x400000+rva+slot, va, caller)
        out[site:site+len(expected)] = relative(hook, 0x400000+rva+slot) + b'\x90'*(len(expected)-5)
        out[raw+slot:raw+slot+len(thunk)] = thunk
        ranges.extend([[site, site+len(expected)], [raw+slot, raw+slot+len(thunk)]])
        end = max(end, slot+len(thunk))
    if end > rawsize:
        raise ValueError('caller helpers exceed RX padding')
    struct.pack_into('<I', out, h + 8, end)
    struct.pack_into('<I', out, opt + 64, checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(input_sha256=identity, output_sha256=hashlib.sha256(result).hexdigest(),
                        size=len(result), helper_va=va,
                        changed_ranges=ranges,
                        server_changed=False, pvf_changed=False, dstr_changed=False,
                        gameplay_verified=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    if args.input.resolve() == args.output.resolve() or args.output.exists():
        raise ValueError('output must be a separate new file')
    before = args.input.read_bytes()
    after, receipt = build(before)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('xb') as stream:
        stream.write(after)
    if args.input.read_bytes() != before or args.output.read_bytes() != after:
        raise RuntimeError('input/output readback mismatch')
    print(json.dumps(receipt, indent=2))


if __name__ == '__main__':
    main()
