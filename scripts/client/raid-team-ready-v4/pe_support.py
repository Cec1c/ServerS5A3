"""Minimal PE32 helpers used by the exact-baseline offline builder."""
import hashlib,struct
IMAGE_BASE=0x400000

def sha(data):
    return hashlib.sha256(data).hexdigest()


def headers(data):
    pe = struct.unpack_from('<I', data, 0x3c)[0]
    opt = pe + 24
    if data[:2] != b'MZ' or data[pe:pe + 4] != b'PE\0\0':
        raise ValueError('not a PE image')
    if (struct.unpack_from('<H', data, pe + 4)[0] != 0x14c
            or struct.unpack_from('<H', data, opt)[0] != 0x10b
            or struct.unpack_from('<I', data, opt + 28)[0] != IMAGE_BASE):
        raise ValueError('unexpected x86 PE32 layout')
    table = opt + struct.unpack_from('<H', data, pe + 20)[0]
    sections = []
    for i in range(struct.unpack_from('<H', data, pe + 6)[0]):
        _, rva, rawsize, raw = struct.unpack_from('<IIII', data, table + 40 * i + 8)
        sections.append((IMAGE_BASE + rva, rawsize, raw))
    return opt, sections


def offset(sections, va, size=1):
    for start, count, raw in sections:
        if start <= va and va + size <= start + count:
            return raw + va - start
    raise ValueError('address outside file-backed sections')


def checksum(data, field):
    # All images accepted by build have an even length. Sum PE words excluding
    # the checksum field; folding at the end is equivalent to folding per word.
    words = memoryview(data).cast('H')
    value = sum(words) - words[field // 2] - words[field // 2 + 1]
    while value >> 16:
        value = (value & 0xffff) + (value >> 16)
    return value + len(data)
