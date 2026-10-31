"""Build the local-own raid face fallback candidate from fixed table3.

The patch is local to 110AE90. It does not change 29674E0, create a scene
drawable, or insert into the cached portrait registry. Output is external and
is never installed by this script.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "raid-partyface"))
import pe_support as pe


INPUT_SHA = "53c93cf72f83454e1e5aa07e0ce45bffc492e3219f5f3f5a9434b0dd6645ff40"
INPUT_SIZE = 111861760
PATCH_SITE = 0x110B16A
PATCH_ORIGINAL = bytes.fromhex("8b4d8c8b33")
PATCH_NEXT = PATCH_SITE + len(PATCH_ORIGINAL)
CACHE_HIT_RESUME = 0x110B199

SCENE_MANAGER = 0x56A3758
RAID_MANAGER = 0x56A3778

WINDOWS = {
    "patch_site": (PATCH_SITE, 5, "cc947e6202c132d5784e086459f81777455ef4743e9d40d372c6ead82eee695f"),
    "vcall_arguments": (0x110B16F, 0x26, "251ed53aaff6ce4db6728aaccc21ff29ada6e04cf8dcb0584763a841aca5dea6"),
    "downstream": (0x110B199, 0x210, "31cd56a769f7b16fcd86c3054dd00b925d77f858dbc2f352e0f0991ade7f7c63"),
    "raid_getter": (0x1A553E0, 0x20, "55a9f63aeb58ba1e6541695d35a7654daaad58464e0f8df0e2f73873acadb16b"),
    "cache_lookup": (0x1A5A5E0, 0x100, "e68052af0eeeb100131c6e79a6ef635218b85e99b41572133197c45c50ce8072"),
    "actor_info": (0x3C1BEA0, 4, "0f44d97c9f42d2e1a1e7956b0bb8e4b07c70feb205b095c08c6fc79b67e90094"),
    "y_getter": (0x37E9F40, 8, "9fa967842105fb6aaaccc5333f979dd3cc706c1e9165b6639293a3819dbe4c6b"),
    "x_getter": (0x209BBE0, 4, "087a4a4a1d3591c4f4697b2aeda95e2b286c2de3930f398f72f4d12404d5c677"),
    "image_draw": (0x2966C60, 0x190, "f0b16729c30c34af11b9a30638d848175821f82e09cdb07e3547d2b025b7c247"),
    "draw_gate": (0x29674E0, 0xC5, "b41a94c1eef9d0ba74eeef2067107fe11b0e1b6a3576952fdcc3d8ca0ce4ed97"),
    "actor_vtable": (0x49ED1DC, 8, "9294ea045815406e9d07dc06810ac1c5df16ef592bb34ca640bbfede661d4fba"),
    "anton_row_call": (0x1877C44, 0x16, "8057a6e3309052ca9348f84b80c78ea7c599cf74b59b362658bc3c0f0244cc57"),
}


class Code:
    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[str, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        self.labels[name] = len(self.buf)

    def call(self, va: int) -> None:
        self.buf.append(0xE8)
        self.buf.extend(struct.pack("<i", va - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        self.buf.append(0xE9)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append(("jmp", offset, target))
        else:
            struct.pack_into("<i", self.buf, offset, target - (self.base + offset + 4))

    def je(self, target: str) -> None:
        self.buf.extend(b"\x0f\x84")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("je", offset, target))

    def jne(self, target: str) -> None:
        self.buf.extend(b"\x0f\x85")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("jne", offset, target))

    def finish(self) -> bytes:
        for _, offset, label in self.fixups:
            struct.pack_into("<i", self.buf, offset, self.labels[label] - offset - 4)
        return bytes(self.buf)


def helper(va: int) -> bytes:
    c = Code(va)

    # Relocated original instructions, then a focused register/flags save.
    c.raw("8b4d8c 8b33")
    c.raw("9c 53 56 57")              # pushfd; push ebx; push esi; push edi

    # Empty control returns to the original path.
    c.raw("85c9")
    c.je("original")

    # Local-own gate. No lazy local-actor getter is called.
    c.raw("8b83f4040000 85c0")
    c.jne("original")
    c.raw("83bb3400000010")
    c.jne("original")
    c.raw("8b0d58376a05 85c9")
    c.je("original")
    c.raw("8b4170 85c0")
    c.je("original")
    c.raw("3bc3")
    c.jne("original")

    # Current raid namespace. The getter must receive the raid manager.
    c.raw("8b0d78376a05 85c9")
    c.je("original")
    c.call(0x1A553E0)
    c.raw("85c0")
    c.je("original")

    # 1A5A5E0(key, owner, route), read-only across the five registry maps.
    c.raw("0fb65003 0fb64802 52 51")
    c.raw("0fb74b20 51")
    c.call(0x1A5A5E0)
    c.raw("83c40c 85c0")
    c.je("original")

    # Reject a mismatched cached entry or an entry carrying the same sentinel.
    c.raw("0fb75020 0fb74b20 3bd1")
    c.jne("original")
    c.raw("83783410")
    c.je("original")

    # The cached visual object must have a loaded image key before consuming
    # its +0x34 field. This is the same +0x30 object read by 2016E/25136BB.
    c.raw("8b4830 85c9")
    c.je("original")

    # Existing cached visual info; no allocation and no scene drawable write.
    c.raw("8bc8")
    c.call(0x3C1BEA0)
    c.raw("8b7804")

    # Exact original 110AE90 14x14 vcall geometry and colour.
    c.raw("8b4d8c")
    c.call(0x37E9F40)
    c.raw("83e802 8bf0")
    c.raw("8b4d8c")
    c.call(0x209BBE0)
    c.raw("83e811")

    # 2966C60(x,y,14,14,cacheActor+34,0,0xFF)
    c.raw("68ff000000 6a00 57 6a0e 6a0e 56 50")
    c.call(0x2966C60)
    c.raw("83c41c")
    c.raw("5f 5e 5b 9d")
    c.jump(CACHE_HIT_RESUME)

    c.label("original")
    c.raw("5f 5e 5b 9d")
    c.raw("8b4d8c")
    c.jump(PATCH_NEXT)

    return c.finish()


def window_checked(data: bytes, sections, item: tuple[int, int, str]) -> bytes:
    va, size, expected = item
    off = pe.offset(sections, va, size)
    raw = data[off:off + size]
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError(f"native window changed at {va:08x}")
    return raw


def build(data: bytes) -> tuple[bytes, dict]:
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA:
        raise ValueError("exact DNF-r195-table3.exe input required")
    opt, sections = pe.headers(data)
    for item in WINDOWS.values():
        window_checked(data, sections, item)
    vtable_slot = pe.offset(sections, 0x49ED1E0, 4)
    if struct.unpack_from("<I", data, vtable_slot)[0] != 0x29674E0:
        raise ValueError("actor vtable+4 is not 29674E0")

    pe_off = struct.unpack_from("<I", data, 0x3C)[0]
    section_count = struct.unpack_from("<H", data, pe_off + 6)[0]
    header = opt + struct.unpack_from("<H", data, opt - 4)[0] + section_count * 40
    if len(sections) != 36 or section_count != 36 or header + 40 > 4096:
        raise ValueError("unexpected table3 section table")
    if any(data[header:header + 40]):
        raise ValueError("new section header slot is not empty")
    if struct.unpack_from("<II", data, opt + 32) != (4096, 4096):
        raise ValueError("unexpected file/image alignment")
    if struct.unpack_from("<I", data, opt + 56)[0] != INPUT_SIZE:
        raise ValueError("unexpected SizeOfImage")

    site = pe.offset(sections, PATCH_SITE, len(PATCH_ORIGINAL))
    if data[site:site + len(PATCH_ORIGINAL)] != PATCH_ORIGINAL:
        raise ValueError("patch boundary mismatch")
    helper_va = pe.IMAGE_BASE + INPUT_SIZE
    code = helper(helper_va)
    if len(code) > 4096:
        raise ValueError("helper exceeds one section")

    out = bytearray(data)
    out.extend(code + bytes(4096 - len(code)))
    out[site:site + 5] = b"\xE9" + struct.pack("<i", helper_va - PATCH_SITE - 5)
    struct.pack_into("<H", out, pe_off + 6, section_count + 1)
    struct.pack_into("<I", out, opt + 4, struct.unpack_from("<I", data, opt + 4)[0] + 4096)
    struct.pack_into("<I", out, opt + 56, INPUT_SIZE + 4096)
    struct.pack_into(
        "<8sIIIIIIHHI", out, header,
        b".locface", len(code), INPUT_SIZE, 4096, INPUT_SIZE,
        0, 0, 0, 0, 0x60000020,
    )
    struct.pack_into("<I", out, opt + 64, pe.checksum(out, opt + 64))

    ranges = sorted([
        (site, site + 5),
        (pe_off + 6, pe_off + 8),
        (opt + 4, opt + 8),
        (opt + 56, opt + 60),
        (opt + 64, opt + 68),
        (header, header + 40),
    ])
    cursor = 0
    for lo, hi in ranges:
        if data[cursor:lo] != out[cursor:lo]:
            raise ValueError("unexpected change before allowed range")
        cursor = hi
    if data[cursor:] != out[cursor:INPUT_SIZE]:
        raise ValueError("unexpected change in original image")
    if any(out[header + 40:4096]):
        raise ValueError("section header padding changed")

    receipt = {
        "input_sha256": INPUT_SHA,
        "output_sha256": pe.sha(out),
        "input_size": INPUT_SIZE,
        "size": len(out),
        "patch_site": hex(PATCH_SITE),
        "patch_bytes": out[site:site + 5].hex(),
        "helper_section": ".locface",
        "helper_va": hex(helper_va),
        "helper_size": len(code),
        "allowed_original_ranges": ranges,
        "native_window_sha256": {name: item[2] for name, item in WINDOWS.items()},
        "scene_manager": hex(SCENE_MANAGER),
        "raid_manager": hex(RAID_MANAGER),
        "published": False,
        "deployed": False,
        "installed": False,
        "native_gui_accepted": False,
    }
    return bytes(out), receipt


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--receipt", required=True, type=Path)
    args = parser.parse_args()
    if args.output.exists() or args.receipt.exists():
        raise SystemExit("new output paths required")
    image, receipt = build(args.input.read_bytes())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("xb") as stream:
        stream.write(image)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
