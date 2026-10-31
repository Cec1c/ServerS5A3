"""Use the current character's region when the mail recipient has no selection.

An exact input profile is mandatory for writes. No EXE or private profile is
distributed. Native window lifetime, query producer and server lookup remain.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

SITE = 0x29DA7BA
ORIGINAL = bytes.fromhex('833d20897b05007553')
EXPLICIT, WARNING, DONE = 0x29DA816, 0x29DA7C3, 0x29DA90F
SELECTION, MANAGER = 0x57B8920, 0x56A3758
GET_CHARACTER, QUERY = 0x296DD80, 0x29D4B00
GATE_CALL, PREVIOUS_GATE = 0x2523A13, 0x6EADFC0
OFFICIAL_SHA256 = 'dddfbcb30309e9af4e9690da2e51853893aef7d35f919b7d809784818765bd2b'
OFFICIAL_SIZE = 112050176


def require(ok, message):
    if not ok:
        raise ValueError(message)


def helper(base):
    code, labels, fixups = bytearray(), {}, []

    def emit(value):
        code.extend(bytes.fromhex(value))

    def branch(opcode, target):
        emit(opcode)
        fixups.append((len(code), target))
        code.extend(bytes(4))

    emit('833d20897b0500')  # Keep explicit recipient-region selection.
    branch('0f85', EXPLICIT)
    emit('9c60 8b0d58376a05 85c9')
    branch('0f84', 'warning')
    branch('e8', GET_CHARACTER)
    emit('85c0')
    branch('0f84', 'warning')
    emit('8b80ac030000 3df9000000')  # Authoritative social region 0..249.
    branch('0f87', 'warning')
    emit('50 8bce')
    branch('e8', QUERY)  # Native thiscall takes the real region, including zero.
    emit('619d')
    branch('e9', DONE)
    labels['warning'] = base + len(code)
    emit('619d')
    branch('e9', WARNING)
    for at, target in fixups:
        target = labels[target] if isinstance(target, str) else target
        struct.pack_into('<i', code, at, target - base - at - 4)
    return bytes(code)


def build(data, profile=None):
    profile = dict(profile or dict(inputSha256=OFFICIAL_SHA256, inputSize=OFFICIAL_SIZE))
    require(set(profile) <= {'inputSha256', 'inputSize', 'outputSha256', 'outputSize'}
            and {'inputSha256', 'inputSize'} <= set(profile), 'invalid profile fields')
    require(pe.sha(data) == profile['inputSha256'] and len(data) == profile['inputSize'],
            'exact input SHA256 and size required')
    opt, sections = pe.headers(data)
    require(not struct.unpack_from('<H', data, opt + 70)[0] & 0x40, 'ASLR unsupported')
    require(not any(data[opt+128:opt+136]), 'certificate table unsupported')
    require(struct.unpack_from('<II', data, opt+32) == (4096, 4096), 'alignment changed')
    require(struct.unpack_from('<I', data, opt+64)[0] == pe.checksum(data, opt+64),
            'input checksum mismatch')
    require(len(data) == max(raw+size for _, size, raw in sections), 'overlay unsupported')
    nt = opt - 24
    table = opt + struct.unpack_from('<H', data, nt+20)[0]
    header = table + len(sections)*40
    require(header+40 <= min(raw for _, size, raw in sections if size)
            and data[header:header+40] == bytes(40), 'no empty section header')
    va = pe.IMAGE_BASE + struct.unpack_from('<I', data, opt+56)[0]
    require(va % 4096 == 0, 'unaligned image end')
    for start, size, raw in sections:
        require(start+size <= va and raw+size <= len(data), 'section outside image/file')
    for address, expected in (
            (SITE, ORIGINAL), (0x2961D40, bytes.fromhex('8b81ac030000c3')),
            (QUERY, bytes.fromhex('558bec56578b7d088bf1893d20897b05')),
            (0x29D4B94, bytes.fromhex('6844010000')),
            (GATE_CALL, b'\xe8'+struct.pack('<i', PREVIOUS_GATE-GATE_CALL-5)),
            (0x1FE7C71, bytes.fromhex('6a006890c7fb016844010000')),
            (0x2046A98, bytes.fromhex('6a0068402f030268ce020000')),
            (0x29DB2B0, bytes.fromhex('6815030000'))):
        at = pe.offset(sections, address, len(expected))
        require(data[at:at+len(expected)] == expected, 'native fingerprint changed')
    code = helper(va)
    require(len(code) <= 256, 'helper too large')
    # Preserve the original gate and its ABI; add only ACK324 and NOTI718.
    gate_va = va+256
    gate, labels, fixups = bytearray(), {}, []

    def emit(raw):
        gate.extend(bytes.fromhex(raw))

    def jump(opcode, label):
        emit(opcode)
        fixups.append((len(gate), label))
        gate.append(0)

    emit('ff742404e80000000085c0')
    struct.pack_into('<i', gate, 5, PREVIOUS_GATE-(gate_va+9))
    jump('75', 'done')
    emit('803f01')
    jump('75', 'notification')
    emit('66817c24044401')
    jump('75', 'done')
    emit('b801000000')
    jump('eb', 'done')
    labels['notification'] = len(gate)
    emit('803f00')
    jump('75', 'done')
    emit('66817c2404ce02')
    jump('75', 'done')
    emit('b801000000')
    labels['done'] = len(gate)
    emit('c20400')
    for at, label in fixups:
        displacement = labels[label]-at-1
        require(-128 <= displacement <= 127, 'gate branch overflow')
        gate[at] = displacement & 255
    out = bytearray(data)
    at = pe.offset(sections, SITE, len(ORIGINAL))
    out[at:at+9] = b'\xe9' + struct.pack('<i', va-SITE-5) + b'\x90'*4
    payload = bytearray(4096)
    payload[:len(code)] = code
    payload[256:256+len(gate)] = gate
    out.extend(payload)
    gate_at = pe.offset(sections, GATE_CALL, 5)
    out[gate_at:gate_at+5] = b'\xe8'+struct.pack('<i', gate_va-GATE_CALL-5)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.mailrg', 256+len(gate),
                     va-pe.IMAGE_BASE, 4096, len(data), 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<H', out, nt+6, len(sections)+1)
    struct.pack_into('<I', out, opt+4, struct.unpack_from('<I', data, opt+4)[0]+4096)
    struct.pack_into('<I', out, opt+56, va-pe.IMAGE_BASE+4096)
    ranges = sorted([(at, at+9), (gate_at, gate_at+5), (header, header+40), (nt+6, nt+8),
                     (opt+4, opt+8), (opt+56, opt+60), (opt+64, opt+68)])
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    cursor = 0
    for lo, hi in ranges:
        require(data[cursor:lo] == out[cursor:lo], 'unrelated original bytes changed')
        cursor = hi
    require(data[cursor:] == out[cursor:len(data)], 'original tail changed')
    result = bytes(out)
    for name, actual in [('outputSha256', pe.sha(result)), ('outputSize', len(result))]:
        if name in profile:
            require(profile[name] == actual, 'pinned output mismatch')
    return result, dict(inputSha256=pe.sha(data), inputSize=len(data),
                        outputSha256=pe.sha(result), outputSize=len(result),
                        changedRanges=ranges, helperVA=hex(va),
                        defaultRegion='current_character_including_zero',
                        explicitSelectionPreserved=True, serverChanged=False,
                        addedReceive=[dict(packetClass=1, opcode=324),
                                      dict(packetClass=0, opcode=718)],
                        pvfChanged=False, deployed=False, gameplayVerified=False)


def write_candidate(source, destination, profile):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    require(source != destination and not destination.exists(), 'separate new output required')
    require({'outputSha256', 'outputSize'} <= profile.keys(), 'write requires pinned output')
    original = source.read_bytes()
    out, proof = build(original, profile)
    with destination.open('xb') as stream:
        stream.write(out)
    require(source.read_bytes() == original and destination.read_bytes() == out,
            'input/output readback mismatch')
    return proof


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--profile', type=Path)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    profile = json.loads(args.profile.read_text()) if args.profile else None
    if args.output:
        require(profile is not None, 'write requires an explicit pinned profile')
        proof = write_candidate(args.input, args.output, profile)
    else:
        _, proof = build(args.input.read_bytes(), profile)
    print(json.dumps(proof, indent=2))
