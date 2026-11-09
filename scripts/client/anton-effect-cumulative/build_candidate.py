"""Standalone, exact-v4 cumulative effect guard; creates new offline files only."""
import argparse
import json
from pathlib import Path
import struct
import pe_utils as pe

INPUT_SHA256 = '213d3e290cfefbd13763799f0d13c1cd565ff1ffab78fc1be2b10e9f93a75440'
INPUT_SIZE = 112607232
SITE, CONTINUE, CALLEE, HELPER = 0x010164B1, 0x010164B8, 0x02947D20, 0x06F88890
ORIGINAL = bytes.fromhex('8bc8e868189301')
RANGES = ((216, 220), (3944, 3948), (12674225, 12674232), (112535696, 112535728))
MODEL = ((0x01FF2F34, 5), (0x06F88600, 649), (0x06EB0100, 36))
FINGERPRINTS = (
    (SITE, ORIGINAL),
    (0x0101649F, bytes.fromhex('6aff6a006aff51f30f1104246a018bceffd0')),
    (CONTINUE, bytes.fromhex('8d4d08518bcf8bf0e86bd6e1ff8970185f5e5b5dc20c00')),
    (CALLEE, bytes.fromhex('558bec83ec0c5356578b7d108bd985ff79096a01e8e78cfeff')),
    (0x02947D49, bytes.fromhex('5f5e33c05b8be55dc21400')),
)


def helper():
    # PR180 instructions and contracts retained, all external rel32 relocated.
    code = bytearray.fromhex('9c85c00f840d0000009d8bc8e800000000e9000000009d8d642414e900000000')
    for displacement, target in ((13, CALLEE), (18, CONTINUE), (28, CONTINUE)):
        struct.pack_into('<i', code, displacement, target - (HELPER + displacement + 4))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact v4 SHA256 and size required; no other baseline accepted')
    opt, sections = pe.headers(data)
    if opt + 64 != 216 or struct.unpack_from('<II', data, opt + 32) != (4096, 4096):
        raise ValueError('unexpected PE layout/alignment')
    if any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected certificate')
    header = 3936
    if (data[header:header + 8] != b'.gld127\0'
            or struct.unpack_from('<IIII', data, header + 8) != (0x889, 0x6B88000, 4096, 112533504)
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020):
        raise ValueError('exact RX helper section required')
    for va, expected in FINGERPRINTS:
        at = pe.offset(sections, va, len(expected))
        if data[at:at + len(expected)] != expected:
            raise ValueError('native fingerprint mismatch at ' + hex(va))
    code = helper()
    cave = pe.offset(sections, HELPER, len(code))
    at = pe.offset(sections, SITE, 7)
    if cave != 112535696 or at != 12674225 or len(code) != 32:
        raise ValueError('unexpected patch mapping')
    if data[cave:cave + 32] != bytes(32):
        raise ValueError('occupied cave')
    out = bytearray(data)
    out[cave:cave + 32] = code
    out[at:at + 7] = b'\xe9' + struct.pack('<i', HELPER - SITE - 5) + b'\x90\x90'
    struct.pack_into('<I', out, 3944, 0x8B0)
    struct.pack_into('<I', out, 216, pe.checksum(out, 216))
    normalized = bytearray(out)
    for lo, hi in RANGES:
        normalized[lo:hi] = data[lo:hi]
    if normalized != data:
        raise ValueError('unexpected change outside four exact ranges')
    model = []
    for va, size in MODEL:
        offset = pe.offset(sections, va, size)
        if out[offset:offset + size] != data[offset:offset + size]:
            raise ValueError('v4 model bytes changed')
        model.append(dict(va=hex(va), size=size, fileOffset=offset,
                          sha256=pe.sha(data[offset:offset + size]), preserved=True))
    receipt = dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out), size=len(out),
                   helperVA=hex(HELPER), helperHex=code.hex(), helperSize=32,
                   hookVA=hex(SITE), hookHex=out[at:at + 7].hex(),
                   changedRanges=RANGES, virtualSizeBefore=0x889, virtualSizeAfter=0x8B0,
                   checksum=struct.unpack_from('<I', out, 216)[0], modelPreservation=model,
                   dependencyChain=['exact v4 (PR183 model registration)',
                                    'PR180 guard relocated from 0x06F88600 to 0x06F88890'],
                   resourceChanges=[], modelBehaviorValidation='pending independent cumulative run',
                   liveAcceptance='pending', installed=False)
    return bytes(out), receipt


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--manifest', type=Path, required=True)
    a = p.parse_args()
    paths = [x.resolve() for x in (a.input, a.output, a.manifest)]
    if len(set(paths)) != 3 or a.output.exists() or a.manifest.exists():
        raise ValueError('separate new output and manifest required')
    result, receipt = build(a.input.read_bytes())
    with a.output.open('xb') as f:
        f.write(result)
    if pe.sha(a.input.read_bytes()) != INPUT_SHA256 or pe.sha(a.output.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input/output readback mismatch')
    receipt.update(inputPath=str(paths[0]), outputPath=str(paths[1]))
    receipt['recipeFiles'] = {p.name: pe.sha(p.read_bytes()) for p in
                             sorted(Path(__file__).parent.iterdir()) if p.is_file()}
    with a.manifest.open('x', encoding='utf8') as f:
        json.dump(receipt, f, indent=2)
        f.write('\n')
    print(json.dumps(receipt, indent=2))


if __name__ == '__main__':
    main()
