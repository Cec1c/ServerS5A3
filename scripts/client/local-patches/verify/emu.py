"""验证脚本共用：把 PE 按节映射进 unicorn（32 位），不运行游戏。"""
import sys
from pathlib import Path

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
sys.path[:0] = [str(ROOT), str(ROOT / 'patches')]

from unicorn import UC_ARCH_X86, UC_MODE_32, Uc  # noqa: E402

from pe_image import IMAGE_BASE, Image, u32  # noqa: E402


def map_image(data):
    img = Image(data)
    size = u32(data, img.opt + 56)
    mu = Uc(UC_ARCH_X86, UC_MODE_32)
    mu.mem_map(IMAGE_BASE, (size + 0xFFF) & ~0xFFF)
    for s in img.sections:
        n = min(s['rsize'], max(0, len(data) - s['raw']))
        if n:
            mu.mem_write(IMAGE_BASE + s['rva'], bytes(data[s['raw']:s['raw'] + n]))
    return mu, img


def changed_offsets(a, b, chunk=1 << 20):
    if len(a) != len(b):
        raise ValueError('size differs')
    out = []
    for o in range(0, len(a), chunk):
        x, y = a[o:o + chunk], b[o:o + chunk]
        if x != y:
            out += [o + i for i, (p, q) in enumerate(zip(x, y)) if p != q]
    return out
