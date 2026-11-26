"""将已验收的武器幻化改动写入独立输出。先校验完整底本，输出路径须不存在。"""
import argparse
import gzip
import hashlib
import struct
from pathlib import Path


MAGIC = b"DNF90SK1"
HEADER = struct.Struct("<8sII32s32sI")
RECORD = struct.Struct("<II")
LIMIT = 256 * 1024 * 1024


def verify_and_apply(source: bytes, recipe: bytes) -> bytes:
    magic, old_size, new_size, old_hash, new_hash, count = HEADER.unpack_from(recipe)
    if magic != MAGIC or not 0 < old_size <= LIMIT or not 0 < new_size <= LIMIT:
        raise ValueError("补丁格式或长度错误")
    if len(source) != old_size or hashlib.sha256(source).digest() != old_hash:
        raise ValueError("EXE 底本 SHA-256 不匹配")
    output = bytearray(source[:new_size])
    output.extend(bytes(new_size - len(output)))
    pos, end = HEADER.size, 0
    for _ in range(count):
        offset, size = RECORD.unpack_from(recipe, pos)
        pos += RECORD.size
        if offset < end or not size or offset + size > new_size or pos + size > len(recipe):
            raise ValueError("补丁区间无效")
        output[offset:offset+size] = recipe[pos:pos+size]
        end = offset + size
        pos += size
    if pos != len(recipe) or hashlib.sha256(output).digest() != new_hash:
        raise ValueError("EXE 输出 SHA-256 不匹配")
    return bytes(output)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("原始EXE", type=Path)
    parser.add_argument("补丁", type=Path)
    parser.add_argument("独立输出", type=Path)
    args = vars(parser.parse_args())
    with gzip.open(args["补丁"], "rb") as stream:
        recipe = stream.read(2 * LIMIT + 1)
    if len(recipe) > 2 * LIMIT:
        raise ValueError("补丁超过长度限制")
    output = verify_and_apply(args["原始EXE"].read_bytes(), recipe)
    with args["独立输出"].open("xb") as stream:
        stream.write(output)
    print("SHA-256", hashlib.sha256(output).hexdigest())


if __name__ == "__main__":
    main()
