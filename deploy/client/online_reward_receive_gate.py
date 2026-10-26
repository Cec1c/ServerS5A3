"""Build the hash-pinned v142 -> v143 online-reward receive-gate client.

Only the existing 561 receive bit and PE checksum change. The source file is
immutable; neither the game nor a player installation is launched or edited.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from image_communication_gate import pe_checksum
from story_pause_gate import file_offset, pe_layout

SOURCE_SHA256 = 'b86000548b09bf438b666b807429c56120130e35f5c89fed4b001da9f4c392ca'
GATE_VA = 0x6e83610
BITMAP_VA = 0x6e92300
OPCODE = 561


def build(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError('input is not the pinned published v142 client')
    base, _, sections = pe_layout(data)
    gate = file_offset(sections, GATE_VA, 27)
    fingerprint = bytes.fromhex('0fb74424043dc205000077100fa305') + struct.pack('<I', BITMAP_VA)
    if base != 0x400000 or data[gate:gate + len(fingerprint)] != fingerprint:
        raise ValueError('receive gate fingerprint mismatch')
    bound = struct.unpack_from('<I', data, gate + 6)[0]
    bitmap = file_offset(sections, BITMAP_VA, bound // 8 + 1)
    offset = bitmap + OPCODE // 8
    if data[offset] & (1 << (OPCODE % 8)):
        raise ValueError('561 already admitted')
    output = bytearray(data)
    output[offset] |= 1 << (OPCODE % 8)
    allowed = lambda raw, op: op <= bound and bool(raw[bitmap + op // 8] & (1 << (op % 8)))
    delta = [op for op in range(65536) if allowed(data, op) != allowed(output, op)]
    if delta != [OPCODE]:
        raise ValueError('unexpected admission delta')
    checksum_offset = struct.unpack_from('<I', data, 0x3c)[0] + 24 + 64
    checksum_before = struct.unpack_from('<I', data, checksum_offset)[0]
    if checksum_before != pe_checksum(data, checksum_offset):
        raise ValueError('source checksum mismatch')
    checksum_after = pe_checksum(output, checksum_offset)
    struct.pack_into('<I', output, checksum_offset, checksum_after)
    permitted = {offset, *range(checksum_offset, checksum_offset + 4)}
    if any(a != b and i not in permitted for i, (a, b) in enumerate(zip(data, output))):
        raise ValueError('unexpected byte delta')
    return bytes(output), dict(
        source_sha256=SOURCE_SHA256, candidate_sha256=hashlib.sha256(output).hexdigest(),
        bytes=len(output), changed_admissions=delta,
        changes=[dict(opcode=OPCODE, offset=offset, before=data[offset], after=output[offset])],
        checksum=dict(offset=checksum_offset, before=checksum_before, after=checksum_after),
        gate_shared_all_packet_classes=True, published=False, native_ui_accepted=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('source', type=Path)
    p.add_argument('output', type=Path)
    p.add_argument('--receipt', type=Path, required=True)
    a = p.parse_args()
    if len({path.resolve() for path in (a.source, a.output, a.receipt)}) != 3 or a.output.exists() or a.receipt.exists():
        p.error('distinct source and new output/receipt required')
    output, receipt = build(a.source.read_bytes())
    with a.output.open('xb') as stream:
        stream.write(output)
    with a.receipt.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
