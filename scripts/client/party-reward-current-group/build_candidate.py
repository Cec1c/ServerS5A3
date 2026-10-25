#!/usr/bin/env python3
"""Build a v169-derived, isolated route2-aware P33 category0 gate.

Input and output are immutable/new files respectively. No game code is launched.
"""
from __future__ import annotations

import argparse
import array
import hashlib
import json
import os
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True

INPUT_SHA256 = "3e779126f50d02c689b27fbe3e1445ff19c8893a9c9bea9dcd8d4d5a6c432c15"
INPUT_SIZE = 111910912

PRED = 0x06EC2000
DRAW = 0x06EC2300
INPUT_GATE = 0x06EC2340
PASS = 0x06EC2380
GROUP_HELPER = 0x06EC2180
SECTION_RVA = 0x06EC2000
NEW_VIRTUAL_SIZE = 0x500


def require(condition, message):
    if not condition:
        raise ValueError(message)


def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]


def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]


def sha(data):
    return hashlib.sha256(data).hexdigest()


class PE:
    def __init__(self, data):
        self.data = data
        require(data[:2] == b"MZ", "missing MZ")
        self.nt = u32(data, 0x3C)
        require(data[self.nt:self.nt + 4] == b"PE\0\0", "missing PE signature")
        require(u16(data, self.nt + 4) == 0x14C, "not PE32/i386")
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        require(u16(data, self.opt) == 0x10B, "not PE32")
        self.base = u32(data, self.opt + 28)
        self.opt_size = u16(data, self.nt + 20)
        self.table = self.opt + self.opt_size
        self.sections = []
        for i in range(self.count):
            at = self.table + i * 40
            virt, rva, raw_size, raw = struct.unpack_from("<IIII", data, at + 8)
            self.sections.append({"index": i, "name": data[at:at+8].rstrip(b"\0").decode(),
                                  "virtual_size": virt, "rva": rva,
                                  "raw_size": raw_size, "raw": raw, "header": at})

    def section(self, name):
        hits = [s for s in self.sections if s["name"] == name]
        require(len(hits) == 1, f"section {name} count={len(hits)}")
        return hits[0]

    def offset(self, va, size=1):
        rva = va - self.base
        hits = [s for s in self.sections if s["rva"] <= rva and
                rva + size <= s["rva"] + s["raw_size"]]
        require(len(hits) == 1, f"VA 0x{va:x} mapping count={len(hits)}")
        return hits[0]["raw"] + rva - hits[0]["rva"]

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at+size]


class Code:
    def __init__(self, base):
        self.base = base
        self.buf = bytearray()
        self.labels = {}
        self.fixups = []

    def raw(self, text):
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name):
        require(name not in self.labels, "duplicate label " + name)
        self.labels[name] = len(self.buf)

    def call(self, target):
        self.buf.append(0xE8)
        self.buf.extend(struct.pack("<i", target - (self.base + len(self.buf) + 4)))

    def jump(self, target):
        self.buf.append(0xE9)
        self._branch_rel(target)

    def _branch_rel(self, target):
        off = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append((off, target))
        else:
            struct.pack_into("<i", self.buf, off, target - (self.base + off + 4))

    def je(self, target):
        self.buf.extend(b"\x0f\x84")
        self._branch_rel(target)

    def jne(self, target):
        self.buf.extend(b"\x0f\x85")
        self._branch_rel(target)

    def jae(self, target):
        self.buf.extend(b"\x0f\x83")
        self._branch_rel(target)

    def jb(self, target):
        self.buf.extend(b"\x0f\x82")
        self._branch_rel(target)

    def ja(self, target):
        self.buf.extend(b"\x0f\x87")
        self._branch_rel(target)

    def finish(self):
        for off, label in self.fixups:
            require(label in self.labels, "unresolved label " + label)
            struct.pack_into("<i", self.buf, off, self.labels[label] - off - 4)
        return bytes(self.buf)


def group_helper():
    c = Code(GROUP_HELPER)
    c.raw("55 8bec 83ec20 53 56 57")
    c.raw("8b4508 8945fc 8b450c 8945f8")       # owner/self key
    c.raw("c745f4ffffffff c745f0ffffffff")    # groups = -1
    c.raw("8b0d78376a05 85c9")
    c.je("fallback")
    c.call(0x01A553E0)
    c.raw("85c0")
    c.je("fallback")
    c.raw("83782402")
    c.jne("fallback")
    c.raw("8bb0a4000000 8bb8a8000000")        # esi=begin edi=end
    c.raw("3bf7")
    c.ja("fallback")                         # begin > end: use original gate
    c.raw("8bc7 2bc6 33d2 b964000000 f7f1 85d2")
    c.jne("fallback")                        # non 0x64 stride: use original gate
    c.raw("85c0")
    c.je("fallback")                         # empty roster: use original gate
    c.raw("83f814")
    c.ja("fallback")                         # implausible count: use original gate
    c.raw("8945ec")
    c.label("loop")
    c.raw("0fb706 3b45f8")
    c.jne("check_owner")
    c.raw("0fb6462d 8945f4")
    c.label("check_owner")
    c.raw("0fb706 3b45fc")
    c.jne("next")
    c.raw("0fb6462d 8945f0")
    c.label("next")
    c.raw("83c664 ff4dec")
    c.jne("loop")
    c.raw("8b45f4 83f8ff")
    c.je("deny")
    c.raw("8b55f0 83faff")
    c.je("deny")
    c.raw("3bc2")
    c.jne("deny")
    c.raw("b801000000")
    c.jump("done")
    c.label("fallback")
    c.raw("b802000000")
    c.jump("done")
    c.label("deny")
    c.raw("33c0")
    c.label("done")
    c.raw("5f5e5b8be55d c3")
    return c.finish()


def predicate():
    c = Code(PRED)
    c.raw("55 8bec 83ec18 53 56 57 33c0 8945f8 8945fc")
    c.raw("8b81e4030000 8945f4 85c0")
    c.je("allow")
    c.raw("3dffff0000")
    c.jae("allow")
    c.raw("8d45f8 50")
    c.call(0x1F1AAE0)
    c.raw("8b75f8 85f6")
    c.je("allow")
    c.raw("83bebc03000000")
    c.jne("allow")
    c.call(0x18C2800)
    c.raw("84c0")
    c.jne("allow")
    c.raw("a158376a05 85c0")
    c.je("allow")
    c.raw("8b7870 85ff")
    c.je("allow")
    c.raw("897df0 0fb74720 85c0")
    c.je("allow")
    c.raw("3dffff0000")
    c.jae("allow")
    c.raw("8945e8")                           # preserve self key across helper
    c.raw("3b45f4")
    c.je("allow")
    # Only route2/FiendWar uses current raid GroupIndex. Every other route and
    # every missing-current-raid context continues to the original party gate.
    c.raw("50 ff75f4")
    c.call(GROUP_HELPER)
    c.raw("83c408 83f802")
    c.je("party")
    c.raw("8945ec")
    c.jump("release")
    c.label("party")
    c.raw("8b45e8")
    c.raw("8b9fc8040000 85db")
    c.je("deny")
    c.raw("50 8bcb")
    c.call(0x2973110)
    c.raw("83f8ff")
    c.je("allow")
    c.raw("8b4df0")
    c.call(0x2964680)
    c.raw("0fb6c0 85c0")
    c.je("allow")
    c.raw("8b5374 85d2")
    c.je("owner_ok")
    c.raw("3bd0")
    c.jne("allow")
    c.label("owner_ok")
    c.raw("8b4df0")
    c.call(0x2964560)
    c.raw("0fb6c0 394370")
    c.jne("allow")
    c.raw("ff75f4 8bcb")
    c.call(0x2973110)
    c.raw("83f8ff")
    c.je("deny")
    c.label("allow")
    c.raw("b801000000")
    c.jump("release")
    c.label("deny")
    c.raw("33c0")
    c.label("release")
    c.raw("8945ec 8b75fc 85f6")
    c.je("done")
    c.raw("8d4604 83c9ff f00fc108")
    c.jne("done")
    c.raw("8b06 8bce ff10 8d4608 83c9ff f00fc108")
    c.jne("done")
    c.raw("8b06 8bce ff5004")
    c.label("done")
    c.raw("8b45ec 5f5e5b8be55d c3")
    return c.finish()


def pe_checksum(data, at):
    work = bytearray(data)
    struct.pack_into("<I", work, at, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H", work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF


def build(source):
    require(__debug__, "assertions required")
    require(len(source) == INPUT_SIZE and sha(source) == INPUT_SHA256, "v169 input mismatch")
    pe = PE(source)
    require(not u16(source, pe.opt + 70) & 0x40, "fixed-address helper requires non-dynamic base")
    require(u32(source, pe.opt + 128) == 0 and u32(source, pe.opt + 132) == 0,
            "signed input is outside this fixed builder")
    row = pe.section(".rrow")
    require(row["rva"] == SECTION_RVA - pe.base, ".rrow VA changed")
    require(row["raw_size"] == 4096 and row["virtual_size"] == 0x39A, ".rrow layout changed")
    require(u32(pe.read(0x0491A074, 4), 0) == PASS, "pass wrapper entry changed")
    require(u32(pe.read(0x0491A078, 4), 0) == DRAW, "draw wrapper entry changed")
    require(u32(pe.read(0x0491A0C4, 4), 0) == INPUT_GATE, "input wrapper entry changed")
    require(pe.read(DRAW, 26).hex() == "51e8fafcffff5985c00f8405000000e97c6e96fcb801000000c3", "draw wrapper bytes changed")
    require(pe.read(INPUT_GATE, 26).hex() == "51e8bafcffff5985c00f8405000000e9dc4596fcb800000000c3", "input wrapper bytes changed")
    require(pe.read(PASS, 26).hex() == "51e87afcffff5985c00f8405000000e9bc7b96fcb801000000c3", "pass wrapper bytes changed")

    pred = predicate()
    helper = group_helper()
    require(len(pred) <= DRAW - PRED, "predicate overlaps draw wrapper")
    require(PRED + len(pred) <= GROUP_HELPER, "predicate overlaps group helper")
    require(GROUP_HELPER + len(helper) <= DRAW, "group helper overlaps draw wrapper")
    require(len(helper) <= NEW_VIRTUAL_SIZE - 0x400, "group helper exceeds section budget")
    require(next((s["rva"] for s in pe.sections if s["rva"] > row["rva"]), 1 << 32) >=
            row["rva"] + NEW_VIRTUAL_SIZE, "expanded section overlaps following section")
    require(set(pe.read(GROUP_HELPER, 0x180)) == {0}, "group helper destination is not zero-filled")

    out = bytearray(source)
    pred_off = pe.offset(PRED, len(pred))
    helper_off = pe.offset(GROUP_HELPER, len(helper))
    out[pred_off:pred_off+len(pred)] = pred
    out[helper_off:helper_off+len(helper)] = helper
    # Clear the unreachable tail of the old predicate so stale bytes cannot be
    # mistaken for live code. Wrappers begin at +0x300 and are untouched.
    old_pred_end = pred_off + 339
    if len(pred) < 339:
        out[pred_off+len(pred):old_pred_end] = bytes(339 - len(pred))
    struct.pack_into("<I", out, row["header"] + 8, NEW_VIRTUAL_SIZE)
    checksum_off = pe.opt + 64
    struct.pack_into("<I", out, checksum_off, pe_checksum(out, checksum_off))
    require(u32(out, checksum_off) == pe_checksum(out, checksum_off), "checksum failed")
    final = bytes(out)
    require(len(final) == INPUT_SIZE, "file size changed")
    allowed = sorted([
        (pred_off, pred_off + max(339, len(pred))),
        (helper_off, helper_off + len(helper)),
        (row["header"] + 8, row["header"] + 12),
        (checksum_off, checksum_off + 4),
    ])
    cursor = 0
    for left, right in allowed:
        require(cursor <= left < right <= len(source), "invalid allowed range")
        if final[cursor:left] != source[cursor:left]:
            bad = next(i for i in range(cursor, left) if final[i] != source[i])
            raise ValueError(f"unlisted byte changed at 0x{bad:x} before 0x{left:x}")
        cursor = right
    require(final[cursor:] == source[cursor:], "unlisted bytes changed at file tail")
    return final, pred, helper


def main(argv=None):
    require(__debug__, "do not use optimized Python")
    ap = argparse.ArgumentParser()
    ap.add_argument("--input-exe", type=Path, required=True,
                    help="fixed v169 EXE; SHA256 and size are enforced")
    ap.add_argument("--output-dir", type=Path, required=True)
    args = ap.parse_args(argv)
    target = args.output_dir.resolve()
    source = args.input_exe.resolve(strict=True)
    require(not os.path.lexists(target), "output directory exists")
    require(target.parent.is_dir(), "output parent missing")
    require(source.is_file(), "fixed input missing")
    final, pred, helper = build(source.read_bytes())
    target.mkdir()
    out = target / "DNF-v169-p33-route2-group.exe"
    out.write_bytes(final)
    require(out.stat().st_size == len(final) and sha(out.read_bytes()) == sha(final),
            "candidate disk read-back failed")
    final_pe = PE(final)
    wrapper_specs = (("draw", DRAW), ("input", INPUT_GATE), ("pass", PASS))
    wrappers = {}
    for name, va in wrapper_specs:
        raw = final_pe.read(va, 26)
        wrappers[name] = {"va": hex(va), "size": len(raw), "sha256": sha(raw), "bytes": raw.hex()}
    receipt = {
        "input": {"path": str(source), "size": INPUT_SIZE, "sha256": INPUT_SHA256},
        "candidate": {"path": str(out), "size": len(final), "sha256": sha(final)},
        "predicate": {"va": hex(PRED), "size": len(pred), "sha256": sha(pred), "bytes": pred.hex()},
        "groupHelper": {"va": hex(GROUP_HELPER), "size": len(helper), "sha256": sha(helper), "bytes": helper.hex()},
        "wrappers": wrappers,
        "routeOffset": "+0x24", "fiendwarRoute": 2,
        "ordinaryRoutesPreserveExistingPartyGate": True,
        "unknownRoutePreservesExistingPartyGate": True,
        "fileSizeChanged": False, "productionModified": False, "gameStarted": False,
    }
    (target / "build-receipt.json").write_text(json.dumps(receipt, indent=2)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps({"candidateSha256": receipt["candidate"]["sha256"],
                      "predicateSize": len(pred), "helperSize": len(helper), "output": str(out)}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"build failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1)
