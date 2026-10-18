"""Admit the requested task/arena notifications on the exact v134 candidate."""
import argparse
import json
from pathlib import Path
import struct
import patch_class_gates_v129 as prior

pe = prior.pe
INPUT_SHA256 = 'a8264e54cf78f5cc5629fbf3a7bbb319c4fbd5a53b098bc87f0c9559ae3dbeb8'
INPUT_SIZE = 111812608
SIZE = INPUT_SIZE
CLASS0 = (47, 323, 504, 505, 578, 1229, 1230)
CLASS1 = (51,)
INHERITED_CLASS0_RANGE = (1483, 1491)
INHERITED_CLASS1 = (658, 668, 820, 868, 889)
EXCLUDED_UPLINK = (377, 570, 1266)
CALL_VA, FRAGMENT_VA, FRAGMENT_END = prior.CALL_VA, prior.FRAGMENT_VA, prior.FRAGMENT_END
NEW_VA, NEW_RAW, NEW_SIZE, NEW_RVA = prior.NEW_VA, prior.NEW_RAW, prior.NEW_SIZE, prior.NEW_RVA
INPUT_ADAPTER = bytes.fromhex(
    '0fb7442404803f00742a803f0175333d9202000074313d9c020000742a'
    '3d3403000074233d79030000741c3d640300007415eb0e3dcb05000072'
    '073dd30500007605e979d68cfcb801000000c20400')


def helper():
    """Preserve the receive ABI and tail-call the original gate on all misses."""
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
    code.extend(struct.pack('<i', prior.OLD_ENTRY_VA - (NEW_VA + len(code) + 4)))
    labels['allow'] = len(code)
    code.extend(bytes.fromhex('b801000000c20400'))
    for at, label in fixups:
        struct.pack_into('<i', code, at, labels[label] - (at + 4))
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact v134 SHA-256 and length required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    header = table + 23 * 40
    if (len(sections) != 24 or sections[-1] != (NEW_VA, NEW_SIZE, NEW_RAW)
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('unexpected v134 PE layout')
    if (data[header:header + 8] != b'.clsgate'
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or struct.unpack_from('<I', data, header + 8)[0] != len(INPUT_ADAPTER)):
        raise ValueError('unexpected receive adapter section')
    if data[NEW_RAW:NEW_RAW + NEW_SIZE] != INPUT_ADAPTER + bytes(NEW_SIZE - len(INPUT_ADAPTER)):
        raise ValueError('unexpected inherited adapter bytes')
    at = pe.offset(sections, CALL_VA, 5)
    if data[at:at + 5] != b'\xe8' + struct.pack('<i', NEW_VA - CALL_VA - 5):
        raise ValueError('unexpected receive call')
    code = helper()
    if not len(INPUT_ADAPTER) <= len(code) <= NEW_SIZE:
        raise ValueError('adapter does not fit existing section')
    out = bytearray(data)
    out[NEW_RAW:NEW_RAW + len(code)] = code
    struct.pack_into('<I', out, header + 8, len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    result = bytes(out)
    return result, dict(
        input_sha256=INPUT_SHA256, output_sha256=pe.sha(result),
        input_size=len(data), output_size=len(result),
        class0=list(CLASS0), class1=list(CLASS1),
        excluded_uplink=list(EXCLUDED_UPLINK),
        helper_va=hex(NEW_VA), helper_size=len(code), call_va=hex(CALL_VA),
        original_ranges=sorted([(opt + 64, opt + 68), (header + 8, header + 12),
                                (NEW_RAW, NEW_RAW + len(code))]),
        shared_bitmap_and_bound_unchanged=True,
        inherited_trade_and_inventory_fixes_unchanged=True,
        payload_consumers_executed=False, full_ui_acceptance=False)


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
