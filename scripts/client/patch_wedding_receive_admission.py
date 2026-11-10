"""为精确 A5 客户端增加婚姻分类接收准入，保留原准入链及位图。"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

import patch_receivers_v128 as pe


INPUT_SHA256 = "a5f1d5ef432064cccaeac6362d5f1405fceddd62739da85661154fc008d47f7d"
INPUT_SHA = INPUT_SHA256
OUTPUT_SHA256 = "7ae08949bd575990db56003f848dee511717fb3a382e15561171bc6fa37fad6e"
INPUT_SIZE = 112549888
IMAGE_BASE = 0x400000
CLASS0 = (997, 998, 1000, 1002, 1003, 1004, 1007, 1008, 1009, 1013)
CLASS1 = (1032, 1033, 1035, 1036, 1037, 1039, 1045, 1046)
CALL_VA = CALL = 0x2523A13
OLD_ENTRY_VA = OLD = 0x6F06100
HELPER_VA = NEW = 0x6F8B400
HELPER_SIZE = 249
HELPER_SHA256 = "77cb333cec087105695ec16271c89ba3f5619389ccbbf2891c32ff73eb2e0285"
HELPER_ZERO_SHA256 = "9fb3871386d4d5abf3149a8fce19470ba4385fdc156f5da4642ba86489f1414e"
BITMAP_VA = 0x6E92300
BITMAP_SIZE = 185
SHARED_GATE_VA = 0x6E83610
LAST_SECTION_VA = 0x6F8B000
LAST_SECTION_RAW = 112545792
LAST_SECTION_OLD_VSIZE = 990
LAST_SECTION_NEW_VSIZE = 1273
SIZE_OF_IMAGE = 112771072
CHANGED_RANGES = ((216, 220), (4064, 4068), (34748947, 34748952), (112546816, 112547065))
ALREADY_ADMITTED = ((0, 997), (0, 1000))


def sha(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def helper():
    """EDI 指向接收头；[ESP+4] 是操作码；精确目标返回1，其余尾调旧链。"""
    code, labels, fixups = bytearray(), {}, []

    def emit(value):
        code.extend(bytes.fromhex(value))

    def jump(opcode, label):
        emit(opcode)
        fixups.append((len(code), label))
        code.extend(bytes(4))

    def comparisons(opcodes):
        for opcode in opcodes:
            code.append(0x3D)
            code.extend(struct.pack("<I", opcode))
            jump("0f84", "allow")

    emit("803f00")
    jump("0f84", "class0")
    emit("803f01")
    jump("0f85", "fallback")
    emit("0fb7442404")
    comparisons(CLASS1)
    jump("e9", "fallback")
    labels["class0"] = len(code)
    emit("0fb7442404")
    comparisons(CLASS0)
    jump("e9", "fallback")
    labels["allow"] = len(code)
    emit("b801000000c20400")
    labels["fallback"] = len(code)
    code.append(0xE9)
    code.extend(struct.pack("<i", OLD_ENTRY_VA - (HELPER_VA + len(code) + 4)))
    for at, label in fixups:
        struct.pack_into("<i", code, at, labels[label] - (at + 4))
    result = bytes(code)
    require(len(result) == HELPER_SIZE and sha(result) == HELPER_SHA256,
            "婚姻接收适配器与已审定的249字节计划不符")
    return result


def _layout(data):
    require(sys.byteorder == "little", "PE校验和实现要求小端宿主")
    opt, sections = pe.headers(data)
    pe_at = opt - 24
    optional_size = struct.unpack_from("<H", data, pe_at + 20)[0]
    table = opt + optional_size
    last_header = table + (len(sections) - 1) * 40
    require(len(sections) == 93 and optional_size == 224 and last_header == 4056,
            "A5节表布局不符")
    require(struct.unpack_from("<II", data, opt + 32) == (4096, 4096) and
            struct.unpack_from("<II", data, opt + 56) == (SIZE_OF_IMAGE, 4096) and
            last_header + 40 == 4096 and not any(struct.unpack_from("<II", data, opt + 128)),
            "A5映像尺寸、节表边界或证书布局不符")
    name, vsize, rva, rawsize, raw, reloc, lines, nreloc, nlines, flags = struct.unpack_from("<8sIIIIIIHHI", data, last_header)
    require(name == b".skprobe" and vsize == LAST_SECTION_OLD_VSIZE and
            IMAGE_BASE + rva == LAST_SECTION_VA and rawsize == 4096 and raw == LAST_SECTION_RAW and
            raw + rawsize == INPUT_SIZE and not (reloc or lines or nreloc or nlines) and flags == 0x60000020,
            "A5末节及既有维护代码布局不符")
    checksum_at = opt + 64
    vsize_at = last_header + 8
    call_at = pe.offset(sections, CALL_VA, 5)
    helper_at = pe.offset(sections, HELPER_VA, HELPER_SIZE)
    require((checksum_at, vsize_at, call_at, helper_at) == (216, 4064, 34748947, 112546816),
            "A5修改位置映射不符")
    require(data[call_at:call_at + 5] == b"\xe8" + struct.pack("<i", OLD_ENTRY_VA - CALL_VA - 5),
            "当前接收准入调用目标不符")
    context = bytes.fromhex("0fb74f0183c410518b0d44fa7e05e8e8269e048b0d44fa7e05")
    at = pe.offset(sections, 0x2523A05, len(context))
    require(data[at:at + len(context)] == context, "接收头操作码及原调用ABI上下文不符")
    class_switch = bytes.fromhex("3c010f85ab0200000fb60f8bc183e8000f84eb010000487449")
    at = pe.offset(sections, 0x2523C94, len(class_switch))
    require(data[at:at + len(class_switch)] == class_switch, "原生接收分类分发上下文不符")
    gate = bytes.fromhex("0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400")
    at = pe.offset(sections, SHARED_GATE_VA, len(gate))
    require(data[at:at + len(gate)] == gate, "原共享位图门槛及上界不符")
    bitmap_at = pe.offset(sections, BITMAP_VA, BITMAP_SIZE)
    for opcode in CLASS0 + CLASS1:
        existing = bool(data[bitmap_at + opcode // 8] & (1 << (opcode % 8)))
        require(existing == (opcode in (997, 1000)), "A5婚姻操作码原始位图状态不符")
    require(helper_at >= raw + vsize and HELPER_VA + HELPER_SIZE <= LAST_SECTION_VA + rawsize and
            HELPER_VA - LAST_SECTION_VA + HELPER_SIZE == LAST_SECTION_NEW_VSIZE and
            data[helper_at:helper_at + HELPER_SIZE] == bytes(HELPER_SIZE) and
            sha(data[helper_at:helper_at + HELPER_SIZE]) == HELPER_ZERO_SHA256,
            "已审定的末节零预留被占用或越界")
    require(struct.unpack_from("<I", data, checksum_at)[0] == pe.checksum(data, checksum_at),
            "A5输入PE校验和不符")
    return sections, checksum_at, vsize_at, call_at, helper_at


def build(data):
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA256,
            "只接受精确A5客户端SHA256及长度；拒绝未知输入或重复补丁")
    sections, checksum_at, vsize_at, call_at, helper_at = _layout(data)
    code = helper()
    out = bytearray(data)
    out[call_at:call_at + 5] = b"\xe8" + struct.pack("<i", HELPER_VA - CALL_VA - 5)
    out[helper_at:helper_at + len(code)] = code
    struct.pack_into("<I", out, vsize_at, LAST_SECTION_NEW_VSIZE)
    struct.pack_into("<I", out, checksum_at, pe.checksum(out, checksum_at))
    result = bytes(out)
    require(len(result) == INPUT_SIZE, "候选不得增加文件长度")
    cursor = 0
    for start, end in CHANGED_RANGES:
        require(result[cursor:start] == data[cursor:start], "候选出现未登记范围变更")
        cursor = end
    require(result[cursor:] == data[cursor:], "候选出现未登记尾部变更")
    # The complete comparison above also preserves every old private helper,
    # native wedding handler, bitmap byte and unrelated data/section/header.
    for va, size in ((OLD_ENTRY_VA, 166), (SHARED_GATE_VA, 31), (BITMAP_VA, BITMAP_SIZE),
                     (LAST_SECTION_VA, LAST_SECTION_OLD_VSIZE)):
        at = pe.offset(sections, va, size)
        require(result[at:at + size] == data[at:at + size], "旧准入链、位图或维护代码发生改变")
    output_sha = sha(result)
    require(output_sha == OUTPUT_SHA256, "候选SHA256与已审定输出不符")
    targets = [(0, opcode) for opcode in CLASS0] + [(1, opcode) for opcode in CLASS1]
    return result, {
        "kind": "wedding_receive_admission", "input_sha256": INPUT_SHA256, "output_sha256": output_sha,
        "input_size": INPUT_SIZE, "output_size": len(result),
        "changed_ranges": [list(interval) for interval in CHANGED_RANGES],
        "changed_byte_count": sum(a != b for start, end in CHANGED_RANGES for a, b in zip(data[start:end], result[start:end])),
        "class0": list(CLASS0), "class1": list(CLASS1),
        "new_admissions": [{"classification": cls, "opcode": opcode} for cls, opcode in targets if (cls, opcode) not in ALREADY_ADMITTED],
        "already_admitted": [{"classification": cls, "opcode": opcode} for cls, opcode in ALREADY_ADMITTED],
        "call_va": hex(CALL_VA), "helper_va": hex(HELPER_VA), "helper_size": len(code),
        "helper_sha256": sha(code), "fallback_va": hex(OLD_ENTRY_VA),
        "old_virtual_size": LAST_SECTION_OLD_VSIZE, "new_virtual_size": LAST_SECTION_NEW_VSIZE,
        "section_count": 93, "size_of_image": SIZE_OF_IMAGE, "checksum": struct.unpack_from("<I", result, checksum_at)[0],
        "shared_bitmap_and_bound_unchanged": True, "old_receive_chain_unchanged": True,
        "old_private_helpers_unchanged": True, "other_pairs_unchanged": True,
        "full_ui_acceptance": False,
    }


def write_candidate(source, destination, receipt=None):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    receipt = Path(receipt).resolve() if receipt is not None else None
    require(source != destination and not destination.exists(), "候选必须使用不存在的独立输出路径，禁止原位覆盖")
    if receipt is not None:
        require(receipt not in (source, destination) and not receipt.exists(), "回执必须使用不存在的独立路径")
    original = source.read_bytes()
    candidate, report = build(original)
    require(sha(source.read_bytes()) == INPUT_SHA256, "候选准备期间输入改变")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("xb") as stream:
        stream.write(candidate)
    require(sha(source.read_bytes()) == INPUT_SHA256 and sha(destination.read_bytes()) == report["output_sha256"],
            "候选输入或输出读回改变")
    if receipt is not None:
        receipt.parent.mkdir(parents=True, exist_ok=True)
        with receipt.open("x", encoding="utf-8") as stream:
            stream.write(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--receipt", type=Path)
    arguments = parser.parse_args()
    print(json.dumps(write_candidate(arguments.input, arguments.output, arguments.receipt), ensure_ascii=False, indent=2))
