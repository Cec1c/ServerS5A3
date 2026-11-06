"""精确7ae客户端的完整婚姻接收准入；不修改界面或既有准入链。"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

INPUT_SHA256 = INPUT_SHA = "7ae08949bd575990db56003f848dee511717fb3a382e15561171bc6fa37fad6e"
OUTPUT_SHA256 = "d11237feaaf8535429eed59545d69a36e67c5760370ae07185eac85db8e55853"
INPUT_SIZE = 112549888
IMAGE_BASE = 0x400000
CALL = CALL_VA = 0x2523A13
OLD = OLD_ENTRY_VA = 0x6F8B400
NEW = HELPER_VA = 0x6F8B500
# Actual bridge send inventory: 999 sets/clears ceremony visuals; 1011 clears both room
# projections. 1010 remains unused; 1001 is added by the final ring-notice stage.
CLASS0 = (999, 1005, 1006, 1011, 1012, 1014, 1015, 1016)
CLASS1 = (1034, 1038, 1040, 1041, 1042, 1043, 1044, 1047, 1048, 1049, 1050, 1051, 1052, 1053)
AUTHORIZED_CLASS0 = frozenset(CLASS0)
AUTHORIZED_CLASS1 = frozenset(CLASS1)
HELPER_SIZE = 51 + 11 * (len(CLASS0) + len(CLASS1))
LAST_SECTION_VA, LAST_SECTION_RAW = 0x6F8B000, 112545792
OLD_VSIZE = 1273
NEW_VSIZE = HELPER_VA - LAST_SECTION_VA + HELPER_SIZE
SIZE_OF_IMAGE = 112771072
CHECKSUM_AT, VSIZE_AT, CALL_AT, HELPER_AT = 216, 4064, 34748947, 112547072
CHANGED_RANGES = ((CHECKSUM_AT, CHECKSUM_AT + 4), (VSIZE_AT, VSIZE_AT + 4),
                  (CALL_AT, CALL_AT + 5), (HELPER_AT, HELPER_AT + HELPER_SIZE))
PRESERVED = (
    (0x6F8B400, 249, "77cb333cec087105695ec16271c89ba3f5619389ccbbf2891c32ff73eb2e0285"),
    (0x6F06100, 166, "4e29b00eccd5dc594a31ea26011bfc99f0433531f3cae96e037fecb4237f9db4"),
    (0x6E92300, 185, "373e0d71be31c0a984991dbf2f0409c0056bacfcf864b2036b41426b9211ae00"),
    (LAST_SECTION_VA, OLD_VSIZE, "ffac565a2e7a166fed19b0d9ba3bd342eec5c812fab9110f1e1c104f063af3fa"),
)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def require(ok, message):
    if not ok:
        raise ValueError(message)


def headers(data):
    require(len(data) >= 4096 and data[:2] == b"MZ", "不是已审PE输入")
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    require(pe + 248 <= 4096 and data[pe:pe + 4] == b"PE\0\0", "PE头越界或签名不符")
    machine, count = struct.unpack_from("<HH", data, pe + 4)
    optional_size = struct.unpack_from("<H", data, pe + 20)[0]
    opt, table = pe + 24, pe + 24 + optional_size
    require(machine == 0x14C and count == 93 and optional_size == 224 and
            struct.unpack_from("<H", data, opt)[0] == 0x10B and
            struct.unpack_from("<I", data, opt + 28)[0] == IMAGE_BASE and
            table + count * 40 == 4096, "原生x86节表布局不符")
    require(struct.unpack_from("<II", data, opt + 32) == (4096, 4096) and
            struct.unpack_from("<II", data, opt + 56) == (SIZE_OF_IMAGE, 4096) and
            not any(struct.unpack_from("<II", data, opt + 128)), "PE尺寸/对齐/证书不符")
    sections = []
    for i in range(count):
        row = struct.unpack_from("<8sIIIIIIHHI", data, table + i * 40)
        name, vsize, rva, rawsize, raw = row[:5]
        require(raw + rawsize <= len(data), "节raw范围越界")
        sections.append((IMAGE_BASE + rva, rawsize, raw))
    last_at = table + (count - 1) * 40
    last = struct.unpack_from("<8sIIIIIIHHI", data, last_at)
    require(last == (b".skprobe", OLD_VSIZE, LAST_SECTION_VA - IMAGE_BASE, 4096,
                     LAST_SECTION_RAW, 0, 0, 0, 0, 0x60000020), "末节原RX预留布局不符")
    require(LAST_SECTION_RAW + 4096 == len(data) and opt + 64 == CHECKSUM_AT and
            last_at + 8 == VSIZE_AT, "末节/校验字段位置不符")
    return sections


def offset(sections, va, size):
    for start, count, raw in sections:
        if start <= va and va + size <= start + count:
            return raw + va - start
    raise ValueError("VA越过文件节范围")


def checksum(data):
    require(sys.byteorder == "little" and len(data) % 2 == 0, "需要小端偶数字节PE")
    words = memoryview(data).cast("H")
    value = sum(words) - words[CHECKSUM_AT // 2] - words[CHECKSUM_AT // 2 + 1]
    while value >> 16:
        value = (value & 0xFFFF) + (value >> 16)
    return value + len(data)


def helper():
    """EDI=接收头，[ESP+4]=u16 opcode；精确pair返回1，其余原链尾调。"""
    require(set(CLASS0) <= AUTHORIZED_CLASS0 and set(CLASS1) <= AUTHORIZED_CLASS1 and
            len(CLASS0) == len(set(CLASS0)) and len(CLASS1) == len(set(CLASS1)), "超出授权集合或有重复pair")
    code, labels, fixups = bytearray(), {}, []

    def jump(opcode, label):
        code.extend(bytes.fromhex(opcode))
        fixups.append((len(code), label))
        code.extend(bytes(4))

    def compare(opcodes):
        for opcode in opcodes:
            code.append(0x3D)
            code.extend(struct.pack("<I", opcode))
            jump("0f84", "allow")

    code.extend(bytes.fromhex("803f00"))
    jump("0f84", "class0")
    code.extend(bytes.fromhex("803f01"))
    jump("0f85", "fallback")
    code.extend(bytes.fromhex("0fb7442404"))
    compare(CLASS1)
    jump("e9", "fallback")
    labels["class0"] = len(code)
    code.extend(bytes.fromhex("0fb7442404"))
    compare(CLASS0)
    jump("e9", "fallback")
    labels["allow"] = len(code)
    code.extend(bytes.fromhex("b801000000c20400"))
    labels["fallback"] = len(code)
    code.append(0xE9)
    code.extend(struct.pack("<i", OLD - (NEW + len(code) + 4)))
    for at, label in fixups:
        struct.pack_into("<i", code, at, labels[label] - at - 4)
    require(len(code) == HELPER_SIZE and NEW_VSIZE <= 4096, "helper尺寸越界")
    return bytes(code)


def build(data):
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA,
            "只接受精确7ae输入；拒绝未知版本、重复补丁或未知改写")
    sections = headers(data)
    require(struct.unpack_from("<I", data, CHECKSUM_AT)[0] == checksum(data), "输入PE校验和不符")
    require(offset(sections, CALL, 5) == CALL_AT and offset(sections, NEW, HELPER_SIZE) == HELPER_AT,
            "固定VA/raw映射不符")
    require(data[CALL_AT:CALL_AT + 5] == bytes.fromhex("e8e879a604"), "当前CALL不是已装249B准入入口")
    require(data[HELPER_AT:HELPER_AT + HELPER_SIZE] == bytes(HELPER_SIZE) and
            HELPER_AT >= LAST_SECTION_RAW + OLD_VSIZE, "RX零预留已占用")
    context = bytes.fromhex("0fb74f0183c410518b0d44fa7e05e8e879a6048b0d44fa7e05")
    at = offset(sections, 0x2523A05, len(context))
    require(data[at:at + len(context)] == context, "原收包头/调用ABI上下文改变")
    context = bytes.fromhex("3c010f85ab0200000fb60f8bc183e8000f84eb010000487449")
    at = offset(sections, 0x2523C94, len(context))
    require(data[at:at + len(context)] == context, "原分类分发上下文改变")
    gate = bytes.fromhex("0fb74424043dc205000077100fa3050023e9060f92c00fb6c0c2040031c0c20400")
    at = offset(sections, 0x6E83610, len(gate))
    require(data[at:at + len(gate)] == gate, "旧共享位图门槛改变")
    for va, size, expected in PRESERVED:
        at = offset(sections, va, size)
        require(sha(data[at:at + size]) == expected, "旧准入/helper/bitmap摘要不符")
    code, out = helper(), bytearray(data)
    out[CALL_AT:CALL_AT + 5] = b"\xE8" + struct.pack("<i", NEW - CALL - 5)
    out[HELPER_AT:HELPER_AT + len(code)] = code
    struct.pack_into("<I", out, VSIZE_AT, NEW_VSIZE)
    struct.pack_into("<I", out, CHECKSUM_AT, checksum(out))
    candidate = bytes(out)
    require(len(candidate) == INPUT_SIZE, "禁止增加EXE长度")
    cursor = 0
    for lo, hi in CHANGED_RANGES:
        require(candidate[cursor:lo] == data[cursor:lo], "授权范围外出现修改")
        cursor = hi
    require(candidate[cursor:] == data[cursor:], "尾部授权范围外出现修改")
    require(sha(candidate) == OUTPUT_SHA256, "完整准入输出与已审定版本不符")
    return candidate, dict(kind="wedding_full_receive_admission", input_sha256=INPUT_SHA,
        output_sha256=sha(candidate), input_size=len(data), output_size=len(candidate),
        class0=list(CLASS0), class1=list(CLASS1), new_admission_count=len(CLASS0) + len(CLASS1),
        changed_ranges=[list(row) for row in CHANGED_RANGES],
        changed_byte_count=sum(a != b for lo, hi in CHANGED_RANGES for a, b in zip(data[lo:hi], candidate[lo:hi])),
        call_va=hex(CALL), helper_va=hex(NEW), fallback_va=hex(OLD), helper_size=len(code), helper_sha256=sha(code),
        old_virtual_size=OLD_VSIZE, new_virtual_size=NEW_VSIZE, section_flags="0x60000020",
        old_helpers_and_bitmap_preserved=True, interface_modified=False, added_dll=False,
        gameplay_acceptance="pending")


def write_candidate(source, destination, receipt=None):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    receipt = Path(receipt).resolve() if receipt is not None else None
    require(source != destination and not destination.exists(), "拒绝原位改写或覆盖已有候选")
    require(receipt is None or (receipt not in (source, destination) and not receipt.exists()), "回执重叠或已存在")
    before = source.read_bytes()
    after, report = build(before)
    require(sha(source.read_bytes()) == INPUT_SHA, "生成过程中输入改变")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("xb") as stream:
        stream.write(after)
    require(sha(destination.read_bytes()) == report["output_sha256"] and sha(source.read_bytes()) == INPUT_SHA,
            "候选/原始输入读回不符")
    if receipt is not None:
        receipt.parent.mkdir(parents=True, exist_ok=True)
        with receipt.open("x", encoding="utf-8") as stream:
            json.dump(report, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output, args.receipt), ensure_ascii=False, indent=2))
