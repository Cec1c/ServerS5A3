"""Add class-specific party/dual-character admissions to exact released v137.

The existing RX .clsgate adapter checks the requested high IDs before the
bounded native shared bitmap. It never indexes that bitmap with 1530 or 1554.
Every miss tail-calls the unchanged native gate, preserving all other decisions.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_item_link_gate_v137 as previous

pe = previous.pe
INPUT_SHA256 = '10478ee7b4d8273da2ca8dfae5f520e3b5ceedda7aa42ec4e6de014c0ea0941f'
INPUT_SIZE = 111812608
ADDED_CLASS0 = (*range(1388, 1401), 1402, 1530)
ADDED_CLASS1 = (1457, 1458, 1465, 1466, 1554)
CLASS0 = tuple(sorted(set((*previous.CLASS0, *ADDED_CLASS0))))
CLASS1 = tuple(sorted(set((*previous.CLASS1, *ADDED_CLASS1))))
INHERITED_CLASS0_RANGE = previous.INHERITED_CLASS0_RANGE
INHERITED_CLASS1 = previous.INHERITED_CLASS1
EXCLUDED_UPLINK = previous.EXCLUDED_UPLINK
CALL_VA, FRAGMENT_VA, FRAGMENT_END = previous.CALL_VA, previous.FRAGMENT_VA, previous.FRAGMENT_END
NEW_VA, NEW_RAW, NEW_SIZE, NEW_RVA = previous.NEW_VA, previous.NEW_RAW, previous.NEW_SIZE, previous.NEW_RVA
OLD_ENTRY_VA = previous.OLD_ENTRY_VA
INPUT_HELPER_SIZE = len(previous.helper())


def helper():
    code, labels, fixups = bytearray(), {}, []

    def branch(op, target):
        code.extend(bytes.fromhex(op))
        fixups.append((len(code), target))
        code.extend(bytes(4))

    def compare(value):
        code.append(0x3d)
        code.extend(struct.pack('<I', value))

    code.extend(bytes.fromhex('0fb7442404803f00'))
    branch('0f85', 'class1')
    for opcode in CLASS0:
        compare(opcode)
        branch('0f84', 'allow')
    compare(INHERITED_CLASS0_RANGE[0])
    branch('0f82', 'fallback')
    compare(INHERITED_CLASS0_RANGE[1])
    branch('0f86', 'allow')
    branch('e9', 'fallback')
    labels['class1'] = len(code)
    code.extend(bytes.fromhex('803f01'))
    branch('0f85', 'fallback')
    for opcode in sorted((*CLASS1, *INHERITED_CLASS1)):
        compare(opcode)
        branch('0f84', 'allow')
    labels['fallback'] = len(code)
    code.append(0xe9)
    code.extend(struct.pack('<i', OLD_ENTRY_VA - (NEW_VA + len(code) + 4)))
    labels['allow'] = len(code)
    code.extend(bytes.fromhex('b801000000c20400'))
    for at, label in fixups:
        struct.pack_into('<i', code, at, labels[label] - (at + 4))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact released v137 SHA-256 and length required')
    opt, sections = pe.headers(data)
    if len(sections) != 24:
        raise ValueError('unexpected v137 section count')
    header = previous._section_header(data, opt, 23)
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    flags = struct.unpack_from('<I', data, header + 36)[0]
    if (data[header:header + 8] != b'.clsgate' or rva != NEW_RVA
            or rawsize != NEW_SIZE or raw != NEW_RAW or flags != 0x60000020
            or vsize != INPUT_HELPER_SIZE):
        raise ValueError('unexpected v137 adapter section')
    if struct.unpack_from('<I', data, opt + 56)[0] != NEW_RVA + NEW_SIZE:
        raise ValueError('unexpected SizeOfImage')
    if any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected certificate directory')
    if data[NEW_RAW:NEW_RAW + NEW_SIZE] != previous.helper() + bytes(NEW_SIZE - INPUT_HELPER_SIZE):
        raise ValueError('unexpected v137 adapter body or padding')
    at = pe.offset(sections, CALL_VA, 5)
    if data[at:at + 5] != b'\xe8' + struct.pack('<i', NEW_VA - CALL_VA - 5):
        raise ValueError('unexpected v137 receive call')
    code = helper()
    if not INPUT_HELPER_SIZE < len(code) <= NEW_SIZE:
        raise ValueError('v138 adapter exceeds allocated RX section capacity')
    out = bytearray(data)
    out[NEW_RAW:NEW_RAW + NEW_SIZE] = code + bytes(NEW_SIZE - len(code))
    struct.pack_into('<I', out, header + 8, len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(
        input_sha256=INPUT_SHA256, output_sha256=pe.sha(result),
        input_size=len(data), output_size=len(result),
        added_class0=list(ADDED_CLASS0), added_class1=list(ADDED_CLASS1),
        class0=list(CLASS0), class1=list(CLASS1),
        inherited_class0_range=list(INHERITED_CLASS0_RANGE),
        inherited_class1=list(INHERITED_CLASS1),
        excluded_uplink=list(EXCLUDED_UPLINK),
        helper_va=hex(NEW_VA), helper_size=len(code), input_helper_size=INPUT_HELPER_SIZE,
        helper_capacity=NEW_SIZE, helper_sha256=pe.sha(code), call_va=hex(CALL_VA),
        original_ranges=sorted([(opt + 64, opt + 68), (header + 8, header + 12),
                                (NEW_RAW, NEW_RAW + len(code))]),
        shared_bitmap_and_bound_unchanged=True,
        high_ids_use_class_adapter_without_bitmap_access=True,
        max_requested_class0=1530, max_requested_class1=1554,
        preserved_existing_notifications=[1401, 1403],
        adapter_section_reused=True, file_size_unchanged=True,
        payload_consumers_executed=False, full_ui_acceptance=False,
    )


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['output_sha256']:
        raise RuntimeError('input/output changed')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
