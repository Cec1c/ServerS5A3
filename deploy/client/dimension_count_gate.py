"""Build an offline class0/812 receive-gate candidate from the published v141.

This enables an existing receiver; it does not implement server weekly state or
publish a client. The input is immutable and output paths must be new.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from image_communication_gate import pe_checksum
from story_pause_gate import file_offset, pe_layout

SOURCE_SHA256 = '26abf551753c2745c2e5b349fb24336422a189187ff847c995e0df9a68871079'
OPCODE, GATE_VA, BITMAP_VA = 812, 0x6e83610, 0x6e92300


def build(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise ValueError('input is not the pinned published v141')
    base, _, sections = pe_layout(data)
    gate = file_offset(sections, GATE_VA, 27)
    fingerprint = bytes.fromhex('0fb74424043dc205000077100fa305') + struct.pack('<I', BITMAP_VA)
    if base != 0x400000 or data[gate:gate+len(fingerprint)] != fingerprint:
        raise ValueError('unexpected image or receive gate')
    bound = struct.unpack_from('<I', data, gate+6)[0]
    bitmap = file_offset(sections, BITMAP_VA, bound//8+1)
    offset = bitmap+OPCODE//8
    if data[offset] != 0x02:
        raise ValueError('unexpected dimension count gate byte')
    after = bytearray(data)
    after[offset] |= 1 << (OPCODE % 8)
    allowed = lambda raw, op: op <= bound and bool(raw[bitmap+op//8] & (1 << (op%8)))
    changed = [op for op in range(65536) if allowed(data, op) != allowed(after, op)]
    if changed != [OPCODE] or after[:offset] != data[:offset] or after[offset+1:] != data[offset+1:]:
        raise ValueError('unexpected admission or byte delta')
    checksum_offset = struct.unpack_from('<I', data, 0x3c)[0]+24+64
    before_checksum = struct.unpack_from('<I', data, checksum_offset)[0]
    if before_checksum != pe_checksum(data, checksum_offset):
        raise ValueError('source PE checksum mismatch')
    checksum = pe_checksum(after, checksum_offset)
    struct.pack_into('<I', after, checksum_offset, checksum)
    return bytes(after), dict(source_sha256=SOURCE_SHA256,
        candidate_sha256=hashlib.sha256(after).hexdigest(), bytes=len(after),
        opcode=OPCODE, gate_va=hex(GATE_VA), bitmap_va=hex(BITMAP_VA),
        changes=[dict(offset=offset, before=2, after=18)],
        checksum=dict(offset=checksum_offset, before=before_checksum, after=checksum),
        changed_admissions=changed, shared_gate_all_packet_classes=True,
        published=False, server_dependency_ready=False, native_ui_acceptance=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('source', type=Path)
    p.add_argument('output', type=Path)
    p.add_argument('--receipt', required=True, type=Path)
    a = p.parse_args()
    if len({x.resolve() for x in (a.source,a.output,a.receipt)}) != 3 or a.output.exists() or a.receipt.exists():
        p.error('source and new output/receipt must be distinct; outputs cannot exist')
    after, receipt = build(a.source.read_bytes())
    with a.output.open('xb') as f:
        f.write(after)
    with a.receipt.open('x', encoding='utf-8') as f:
        json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
