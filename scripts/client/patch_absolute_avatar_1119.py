"""为已完成!235映像修复的固定V162开放class1/1119，不改原生结果读者。"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import struct


INPUT_SHA256 = 'b9fec733823dc95e1d6fd24c139f47b00a2fbcff57f913612cdfa34101d83902'
OUTPUT_SHA256 = 'ca9c300f8f941476fab61fd7094449920e9bc91d31397d3e3fe3451fe36cc125'
INPUT_SIZE = 111894528
IMAGE_BASE = 0x400000
CALL_VA = 0x2523a13
FRAGMENT_VA = 0x2523a0c
FRAGMENT_END = CALL_VA + 5
FALLBACK_VA = 0x6ead000
HELPER_VA = 0x6ead080
OPCODE = 1119
NATIVE_ANCHORS = (
    ('请求写者', 0x17d99d0, 0x500, 'd74fcb60881a6dcd509951b80404ba011655f2ada01c3c1627d614af5cf57b77'),
    ('应答读者', 0x17dbf80, 0x1000, '49c26c151f6297e7cb38f8d38c8c9c73cdc1cca3fb175766c47279e3bd7a1d9d'),
    ('结果应用', 0x17db9f0, 0x590, '3341ca5eda601d0089a6305776ab7daf27bd85df8251d6f76d0d62b76c9f4e61'),
    ('应答登记', 0x440e8d0, 0x80, 'afea4cd266e50635e9dedfa6aa41aa5b5e1685a5b5afb0f88aeebf3faa9d0e62'),
)

_spec = importlib.util.spec_from_file_location(
    'absolute_avatar_pe_gap', Path(__file__).with_name('pe-image-gap') / 'build_candidate.py')
pe = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(pe)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def offset(layout, va, size=1):
    for section in layout['sections']:
        start = IMAGE_BASE + section['rva']
        if start <= va and va + size <= start + section['rawSize']:
            return section['raw'] + va - start
    raise ValueError('地址不在文件支持的节内')


def helper():
    # EDI仍指向已核实的接收头，[EDI]是分类；原参数、ECX及非易失寄存器保持。
    code = bytearray.fromhex('0fb7442404803f01')
    code.extend(b'\x0f\x85' + struct.pack('<i', FALLBACK_VA - (HELPER_VA + len(code) + 6)))
    code.extend(b'\x3d' + struct.pack('<I', OPCODE))
    code.extend(b'\x0f\x85' + struct.pack('<i', FALLBACK_VA - (HELPER_VA + len(code) + 6)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA256,
            '须为!235修好Windows映像的固定V162输入；其它版本、原错误193版本及重复补丁均拒绝')
    layout = pe.parse(data)
    require(len(layout['sections']) == 44 and
            layout['sectionAlignment'] == layout['fileAlignment'] == 0x1000 and
            struct.unpack_from('<I', data, layout['opt'] + 28)[0] == IMAGE_BASE and
            not pe.discontinuities(layout), 'V162映像布局不匹配')
    require(not any(struct.unpack_from('<II', data, layout['opt'] + 128)), '不支持带签名证书的不同输入')
    call = offset(layout, CALL_VA, 5)
    require(data[call:call + 5] == b'\xe8' + struct.pack('<i', FALLBACK_VA - CALL_VA - 5),
            '原接收调用目标不匹配')
    context = bytes.fromhex('0fb74f0183c410518b0d44fa7e05')
    at = offset(layout, 0x2523a05, len(context))
    require(data[at:at + len(context)] == context, '原分类头与调用参数契约不匹配')
    at = offset(layout, FALLBACK_VA, 8)
    require(data[at:at + 8] == bytes.fromhex('0fb7442404803f00'), '原分类链入口不匹配')
    section = next((s for s in layout['sections'] if s['name'] == '.r195rx'), None)
    require(section is not None and section['rva'] == FALLBACK_VA - IMAGE_BASE and
            section['rawSize'] == 0x1000 and section['virtualSize'] == 0x76 and
            section['characteristics'] == 0x60000020, '原只读可执行节布局不匹配')
    code = helper()
    start = offset(layout, HELPER_VA, len(code))
    require(data[start:start + len(code)] == bytes(len(code)), '接收辅助代码空间已占用')
    anchors = []
    for name, va, size, digest in NATIVE_ANCHORS:
        at = offset(layout, va, size)
        require(sha(data[at:at + size]) == digest, name + '契约字节不匹配')
        anchors.append(dict(用途=name, VA=hex(va), 字节数=size, SHA256=digest))
    result = bytearray(data)
    result[start:start + len(code)] = code
    result[call:call + 5] = b'\xe8' + struct.pack('<i', HELPER_VA - CALL_VA - 5)
    virtual = section['header'] + 8
    new_size = HELPER_VA - IMAGE_BASE - section['rva'] + len(code)
    require(new_size <= section['rawSize'], '辅助代码超出原节')
    struct.pack_into('<I', result, virtual, new_size)
    checksum = layout['opt'] + 64
    struct.pack_into('<I', result, checksum, pe.checksum(result, checksum))
    ranges = sorted([(start, start + len(code)), (call, call + 5),
                     (virtual, virtual + 4), (checksum, checksum + 4)])
    normalized = bytearray(result)
    for first, last in ranges:
        normalized[first:last] = data[first:last]
    require(normalized == data, '出现允许范围以外的改动')
    require(not pe.discontinuities(pe.parse(result)), '输出映像不连续')
    require(sha(result) == OUTPUT_SHA256, '输出摘要与已验证补丁不一致')
    receipt = dict(input_sha256=INPUT_SHA256, output_sha256=OUTPUT_SHA256, size=INPUT_SIZE,
                   enabled_class=1, enabled_opcode=OPCODE, call_va=hex(CALL_VA),
                   helper_va=hex(HELPER_VA), helper_size=len(code), helper_sha256=sha(code),
                   original_ranges=ranges, old_virtual_size=section['virtualSize'],
                   new_virtual_size=new_size, original_gate_preserved=True,
                   shared_bitmap_unchanged=True, native_anchors=anchors,
                   image_layout_contiguous=True, full_ui_acceptance=False)
    return bytes(result), receipt


def write_candidate(source, destination, verify_windows=False):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    require(source != destination and not destination.exists(), '输出必须是不同路径的新文件')
    result, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(result)
    require(sha(source.read_bytes()) == INPUT_SHA256, '生成期间输入改变')
    require(sha(destination.read_bytes()) == OUTPUT_SHA256, '输出读回摘要不符')
    if verify_windows:
        receipt['windows_before'] = pe.probe_image(source)
        receipt['windows_after'] = pe.probe_image(destination)
        require(receipt['windows_before']['mappingSucceeded'] and
                receipt['windows_after']['mappingSucceeded'], 'Windows拒绝输入或输出映像')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--verify-windows', action='store_true')
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output, args.verify_windows),
                     ensure_ascii=False, indent=2))
