"""Enable only the proven R195 notification/response pairs on exact v153."""
from pathlib import Path
import argparse, json, struct, sys

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / 'raid-partyface'))
import pe_support as pe

INPUT_SHA = 'c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8'
INPUT_SIZE = 111857664
CALL = 0x2523a13
PREVIOUS_GATE = 0x6eaa000
ADDITIONS = ((0, 103), (0, 775), (0, 1278), (0, 1367), (0, 1368), (1, 835), (1, 864))


def helper(va):
    code = bytearray.fromhex('0fb7442404')
    labels, jumps = {}, []
    def jump(opcode, label):
        code.extend(opcode)
        jumps.append((len(code), label))
        code.extend(bytes(4))
    code.extend(bytes.fromhex('803f00'))
    jump(b'\x0f\x85', 'class1')
    for opcode in (103, 775, 1278, 1367, 1368):
        code.extend(b'\x3d' + struct.pack('<I', opcode))
        jump(b'\x0f\x84', 'accept')
    jump(b'\xe9', 'fallback')
    labels['class1'] = len(code)
    code.extend(bytes.fromhex('803f01'))
    jump(b'\x0f\x85', 'fallback')
    for opcode in (835, 864):
        code.extend(b'\x3d' + struct.pack('<I', opcode))
        jump(b'\x0f\x84', 'accept')
    labels['fallback'] = len(code)
    code.extend(b'\xe9' + struct.pack('<i', PREVIOUS_GATE - (va + len(code) + 5)))
    labels['accept'] = len(code)
    code.extend(bytes.fromhex('b801000000c20400'))
    for offset, label in jumps:
        struct.pack_into('<i', code, offset, labels[label] - offset - 4)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA:
        raise ValueError('exact deployed v153 input required')
    opt, sections = pe.headers(data)
    header = opt + struct.unpack_from('<H', data, opt - 4)[0] + len(sections) * 40
    if len(sections) != 35 or header + 40 > 4096 or any(data[header:header + 40]):
        raise ValueError('unexpected section layout')
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096):
        raise ValueError('unexpected PE alignment')
    if struct.unpack_from('<I', data, opt + 56)[0] != INPUT_SIZE or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected image size or certificate table')
    site = pe.offset(sections, CALL, 5)
    original = b'\xe8' + struct.pack('<i', PREVIOUS_GATE - CALL - 5)
    if data[site:site + 5] != original:
        raise ValueError('receive call site does not target v152/v153 gate')
    va = pe.IMAGE_BASE + INPUT_SIZE
    code = helper(va)
    out = bytearray(data)
    out.extend(code + bytes(4096 - len(code)))
    out[site:site + 5] = b'\xe8' + struct.pack('<i', va - CALL - 5)
    struct.pack_into('<H', out, opt - 18, len(sections) + 1)
    struct.pack_into('<I', out, opt + 4, struct.unpack_from('<I', data, opt + 4)[0] + 4096)
    struct.pack_into('<I', out, opt + 56, INPUT_SIZE + 4096)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.r195rx', len(code), INPUT_SIZE, 4096, INPUT_SIZE, 0, 0, 0, 0, 0x60000020)
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    ranges = sorted([(site + 1, site + 5), (opt - 18, opt - 16), (opt + 4, opt + 8),
                     (opt + 56, opt + 60), (opt + 64, opt + 68), (header, header + 40)])
    cursor = 0
    for lo, hi in ranges:
        if data[cursor:lo] != out[cursor:lo]:
            raise ValueError('unexpected original-image change')
        cursor = hi
    if data[cursor:] != out[cursor:INPUT_SIZE]:
        raise ValueError('unexpected original-image tail change')
    receipt = dict(input_sha256=INPUT_SHA, output_sha256=pe.sha(out), size=len(out),
                   helper_va=hex(va), helper_size=len(code), additions=ADDITIONS,
                   original_edit_ranges=ranges, published=False, deployed=False, gui_accepted=False)
    return bytes(out), receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists() or args.receipt.exists():
        raise ValueError('new output paths required')
    result, receipt = build(args.input.read_bytes())
    with args.output.open('xb') as out:
        out.write(result)
    with args.receipt.open('x', encoding='utf8') as out:
        json.dump(receipt, out, indent=2)
    print(json.dumps(receipt))
