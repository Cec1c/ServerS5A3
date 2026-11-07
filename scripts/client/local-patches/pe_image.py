"""本地客户端补丁栈共用的 PE 工具（只用标准库）。

只处理 32 位、无 ASLR 重定位的 DNF.exe：所有地址都按固定基址 0x400000 计算。
"""
import hashlib
import re
import struct

IMAGE_BASE = 0x400000


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, at):
    return struct.unpack_from('<H', data, at)[0]


def u32(data, at):
    return struct.unpack_from('<I', data, at)[0]


class Image:
    """对文件字节的只读视图：节表、VA 与文件偏移互换、字节特征搜索。"""

    def __init__(self, data):
        if data[:2] != b'MZ':
            raise ValueError('not a PE file')
        self.data = data
        nt = u32(data, 0x3C)
        if data[nt:nt + 4] != b'PE\0\0' or u16(data, nt + 4) != 0x14C:
            raise ValueError('not a 32-bit PE image')
        self.opt = nt + 24
        if u32(data, self.opt + 28) != IMAGE_BASE:
            raise ValueError('unexpected image base')
        self.checksum_at = self.opt + 64
        table = self.opt + u16(data, nt + 20)
        self.sections = []
        for index in range(u16(data, nt + 6)):
            at = table + 40 * index
            self.sections.append(dict(
                header=at, name=data[at:at + 8].rstrip(b'\0').decode('ascii', 'replace'),
                vsize=u32(data, at + 8), rva=u32(data, at + 12), rsize=u32(data, at + 16),
                raw=u32(data, at + 20), flags=u32(data, at + 36)))

    def offset(self, va, size=1):
        """VA → 文件偏移；要求整段都有文件数据。"""
        rva = va - IMAGE_BASE
        for s in self.sections:
            if s['rva'] <= rva and rva + size <= s['rva'] + s['rsize']:
                return s['raw'] + rva - s['rva']
        raise ValueError(f'VA {va:#x}+{size:#x} has no file data')

    def va(self, offset):
        for s in self.sections:
            if s['raw'] <= offset < s['raw'] + s['rsize']:
                return IMAGE_BASE + s['rva'] + offset - s['raw']
        raise ValueError(f'file offset {offset:#x} is not in a section')

    def read(self, va, size):
        at = self.offset(va, size)
        return bytes(self.data[at:at + size])

    def section(self, name):
        found = [s for s in self.sections if s['name'] == name]
        return found[0] if len(found) == 1 else None

    def section_of(self, va):
        rva = va - IMAGE_BASE
        for s in self.sections:
            if s['rva'] <= rva < s['rva'] + max(s['vsize'], s['rsize']):
                return s
        return None

    def rel32_target(self, va):
        """va 处 E8/E9 rel32 指令的目标；不是这两种指令时返回 None。"""
        code = self.read(va, 5)
        if code[0] not in (0xE8, 0xE9):
            return None
        return (va + 5 + struct.unpack_from('<i', code, 1)[0]) & 0xFFFFFFFF

    def find(self, pattern):
        """按十六进制特征搜索，'??' 匹配任意字节；返回全部文件偏移。"""
        regex = b''.join(b'.' if tok == '??' else re.escape(bytes.fromhex(tok))
                         for tok in pattern.split())
        return [m.start() for m in re.finditer(regex, bytes(self.data), re.DOTALL)]


def rel32(source_va, length, target_va):
    """从 source_va 开始、长度为 length 的相对跳转/调用指向 target_va 时的位移。"""
    return struct.pack('<i', target_va - (source_va + length))


def pe_checksum(data, field):
    image = bytearray(data)
    struct.pack_into('<I', image, field, 0)
    if len(image) % 2:
        image.append(0)
    total = sum(memoryview(bytes(image)).cast('H'))
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF
