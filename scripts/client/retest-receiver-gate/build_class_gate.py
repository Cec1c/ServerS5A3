# -*- coding: utf-8 -*-
# Build a v173 / v173-primary-v1 client candidate that admits only five class/opcode
# pairs ahead of the existing receive chain, without touching the shared bitmap.
#
# Original MR pattern reused:
#   MR233 scripts/client/premium-service-903/build_candidate.py
#   MR236 scripts/client/patched_absolute_avatar_1119.py (wrapper_code)
#
# The helper is appended in the zero tail of the existing non-writable .r195rx
# section. The original chain entry 0x6EAD500 stays byte-identical and is
# reached by the helper's fallback jmp.
from __future__ import annotations

import argparse
import array
import hashlib
import json
import os
import struct
import sys
from pathlib import Path

PROFILES = {
    "9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060": {
        "sha256": "9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060",
        "name": "v173", "size": 111915008, "sections": 49,
        "section_va": 0x06EAD000, "section_vsize": 0x526, "section_raw": 0x1000,
        "chain_entry": 0x06EAD500, "helper_va": 0x06EAD600,
    },
    "3d7d874d6b360001ac391d7e7b83d9a964f58a033a1b2b6b131d5257e1f52802": {
        "sha256": "3d7d874d6b360001ac391d7e7b83d9a964f58a033a1b2b6b131d5257e1f52802",
        "name": "v173-primary-v1", "size": 111915008, "sections": 49,
        "section_va": 0x06EAD000, "section_vsize": 0x526, "section_raw": 0x1000,
        "chain_entry": 0x06EAD500, "helper_va": 0x06EAD600,
    },
    "27c2241aae6d7b8c6b043a2bbe9e2eccfdb18d6969912055694195b972fcad67": {
        "sha256": "27c2241aae6d7b8c6b043a2bbe9e2eccfdb18d6969912055694195b972fcad67",
        "name": "v174", "size": 111919104, "sections": 50,
        "section_va": 0x06EAD000, "section_vsize": 0x625, "section_raw": 0x1000,
        "chain_entry": 0x06EAD600, "helper_va": 0x06EAD700,
    },
    "c97fec768b7178ae89446a65de5aa980a1532724e3a1bde49e5273fe5aff0db6": {
        "sha256": "c97fec768b7178ae89446a65de5aa980a1532724e3a1bde49e5273fe5aff0db6",
        "name": "v175", "size": 111919104, "sections": 50,
        "section_va": 0x06EAD000, "section_vsize": 0x79C, "section_raw": 0x1000,
        "chain_entry": 0x06EAD700, "helper_va": 0x06EAD800,
    },
}
INPUTS = {k: v["name"] for k, v in PROFILES.items()}
IMAGE_BASE = 0x400000
CALL_VA = 0x02523A13
SECTION_NAME = b".r195rx" + bytes([0])
SECTION_CHARACTERISTICS = 0x60000020

# (class, opcode) pairs this helper admits. 1203 is admitted here instead of
# opening the shared bitmap, which would also admit class1/1203.
ADMISSIONS = [(0, 296), (0, 812), (0, 1203), (1, 903), (1, 1119)]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]


def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]


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
        self.table = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for i in range(self.count):
            at = self.table + i * 40
            vs, rva, rs, raw = struct.unpack_from("<IIII", data, at + 8)
            self.sections.append({"index": i, "header": at, "name": data[at:at + 8],
                                  "vs": vs, "rva": rva, "rs": rs, "raw": raw,
                                  "chars": u32(data, at + 36)})

    def find(self, name):
        hits = [s for s in self.sections if s["name"] == name]
        require(len(hits) == 1, "section count %d for %r" % (len(hits), name))
        return hits[0]

    def offset(self, va, size=1):
        rva = va - self.base
        hits = [s for s in self.sections if s["rva"] <= rva and rva + size <= s["rva"] + s["rs"]]
        require(len(hits) == 1, "VA 0x%x mapping count=%d" % (va, len(hits)))
        return hits[0]["raw"] + rva - hits[0]["rva"]

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]


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

    def _rel(self, target):
        off = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append((off, target))
        else:
            struct.pack_into("<i", self.buf, off, target - (self.base + off + 4))

    def je(self, target):
        self.buf.extend(b"\x0f\x84")
        self._rel(target)

    def jne(self, target):
        self.buf.extend(b"\x0f\x85")
        self._rel(target)

    def jump(self, target):
        self.buf.append(0xE9)
        self._rel(target)

    def finish(self):
        for off, label in self.fixups:
            require(label in self.labels, "unresolved label " + label)
            struct.pack_into("<i", self.buf, off, self.labels[label] - off - 4)
        return bytes(self.buf)


def helper_code(profile):
    c = Code(profile["helper_va"])
    c.raw("0fb7442404")        # movzx eax, word [esp+4]
    c.raw("803f00")            # cmp byte [edi], 0
    c.jne("class1")
    c.raw("3d28010000")        # cmp eax, 296
    c.je("allow")
    c.raw("3d2c030000")        # cmp eax, 812
    c.je("allow")
    c.raw("3db3040000")        # cmp eax, 1203  (0x4B3)
    c.je("allow")
    c.jump("orig")
    c.label("class1")
    c.raw("803f01")            # cmp byte [edi], 1
    c.jne("orig")
    c.raw("3d87030000")        # cmp eax, 903
    c.je("allow")
    c.raw("3d5f040000")        # cmp eax, 1119
    c.jne("orig")
    c.label("allow")
    c.raw("b801000000")        # mov eax, 1
    c.raw("c20400")            # ret 4
    c.label("orig")
    c.jump(profile["chain_entry"])   # jmp to the untouched original chain head
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


def compose(data, profile, *, verify_hash=True, verified_by=None):
    """Compose the class gate onto data in memory and return (bytes, receipt).

    verify_hash=True  -> data must equal the pinned profile image byte-for-byte.
    verify_hash=False -> the caller certifies the base image (for example a
                         v174 tree that already has title/peer/hp composed and
                         verified).  Every structural invariant is still
                         enforced, so section, CALL, chain-head or tail drift
                         is refused instead of silently patched.
    """
    require(isinstance(profile, dict), "profile must be a PROFILES entry")
    for key in ("sha256", "name", "size", "sections", "section_va", "section_vsize",
                "section_raw", "chain_entry", "helper_va"):
        require(key in profile, "profile missing " + key)
    digest = sha(data)
    if verify_hash:
        require(digest == profile["sha256"],
                "input sha256 %s is not the pinned %s image" % (digest, profile["name"]))
    else:
        require(bool(verified_by),
                "composable path requires verified_by to record who certified the base")
    require(len(data) == profile["size"],
            "input size %d != %d" % (len(data), profile["size"]))
    pe = PE(data)
    require(pe.base == IMAGE_BASE, "unexpected image base")
    require(pe.count == profile["sections"],
            "section count %d != %d" % (pe.count, profile["sections"]))
    require(u32(data, pe.opt + 128) == 0 and u32(data, pe.opt + 132) == 0,
            "signed image requires a separate signing step")

    sec = pe.find(SECTION_NAME)
    require(sec["rva"] == profile["section_va"] - IMAGE_BASE, ".r195rx VA changed")
    require(sec["vs"] == profile["section_vsize"],
            ".r195rx VirtualSize %#x != %#x" % (sec["vs"], profile["section_vsize"]))
    require(sec["rs"] == profile["section_raw"], ".r195rx raw size changed")
    require(sec["chars"] == SECTION_CHARACTERISTICS, ".r195rx characteristics changed")

    call = pe.offset(CALL_VA, 5)
    require(data[call] == 0xE8, "receive call is not a near call")
    target = CALL_VA + 5 + struct.unpack_from("<i", data, call + 1)[0]
    require(target == profile["chain_entry"],
            "receive call target 0x%x != 0x%x" % (target, profile["chain_entry"]))
    head = pe.read(profile["chain_entry"], 2)
    require(head == b"\x80\x3f",
            "chain head at 0x%x does not look like a class gate" % profile["chain_entry"])

    code = helper_code(profile)
    helper_va = profile["helper_va"]
    new_vs = helper_va - profile["section_va"] + len(code)
    require(new_vs <= sec["rs"], "helper exceeds raw section")
    helper_off = pe.offset(helper_va, len(code))
    tail_off = pe.offset(profile["section_va"] + profile["section_vsize"], 1)
    require(set(data[tail_off:tail_off + (new_vs - profile["section_vsize"])]) == {0},
            "declared .r195rx tail (0x%x..0x%x) is not zero-filled"
            % (profile["section_va"] + profile["section_vsize"], profile["section_va"] + new_vs))
    require(profile["section_va"] + profile["section_vsize"] <= helper_va,
            "helper starts inside the live section")
    require(helper_va + len(code) <= profile["section_va"] + profile["section_raw"],
            "helper exceeds .r195rx raw data")

    out = bytearray(data)
    out[helper_off:helper_off + len(code)] = code
    out[call:call + 5] = b"\xe8" + struct.pack("<i", helper_va - (CALL_VA + 5))
    vs_at = sec["header"] + 8
    struct.pack_into("<I", out, vs_at, new_vs)
    csum_at = pe.opt + 64
    struct.pack_into("<I", out, csum_at, pe_checksum(out, csum_at))
    final = bytes(out)

    windows = sorted([
        (call, call + 5),
        (helper_off, helper_off + len(code)),
        (vs_at, vs_at + 4),
        (csum_at, csum_at + 4),
    ])
    cursor = 0
    for left, right in windows:
        require(cursor <= left < right <= len(data), "invalid window")
        require(final[cursor:left] == data[cursor:left],
                "unlisted change before 0x%x" % left)
        cursor = right
    require(final[cursor:] == data[cursor:], "unlisted change after last window")
    require(len(final) == len(data), "file size changed")

    restored = bytearray(final)
    for left, right in windows:
        restored[left:right] = data[left:right]
    require(bytes(restored) == data, "reversible restore check failed")

    receipt = {
        "profile": profile["name"],
        "profileSpec": {k: (hex(v) if isinstance(v, int) else v) for k, v in profile.items()},
        "input": {"size": len(data), "sha256": digest},
        "output": {"size": len(final), "sha256": sha(final)},
        "call": {"va": hex(CALL_VA), "beforeTarget": hex(profile["chain_entry"]),
                 "afterTarget": hex(helper_va), "bytes": final[call:call + 5].hex()},
        "helper": {"va": hex(helper_va), "size": len(code), "sha256": sha(code),
                   "bytes": code.hex()},
        "admissions": [{"class": c, "opcode": o} for c, o in ADMISSIONS],
        "fallback": hex(profile["chain_entry"]) + " unchanged; reached by helper jmp",
        "section": {"name": ".r195rx", "raw": sec["raw"], "rawSize": sec["rs"],
                    "virtualSizeBefore": profile["section_vsize"], "virtualSizeAfter": new_vs,
                    "virtualSizeField": hex(vs_at)},
        "windows": [{"offset": l, "size": r - l,
                     "before": data[l:r].hex(), "after": final[l:r].hex()}
                    for l, r in windows],
        "bitmapTouched": False,
        "sharedBitmapNote": "0x6E92300 is not referenced; class1/296 stays denied",
        "hashVerified": bool(verify_hash),
        "verifiedBy": verified_by,
        "reversible": True,
        "exeWrittenByThisRun": False,
        "nativeVerified": False,
        "gameAccepted": False,
    }
    return final, receipt


def build(data):
    """CLI contract: only the pinned profile images are accepted."""
    digest = sha(data)
    require(digest in PROFILES,
            "input is not a pinned v173 / v173-primary-v1 / v174 / v175 image; a new baseline needs a "
            "fresh layout/overlap check, not another allowlist entry")
    return compose(data, PROFILES[digest], verify_hash=True)


def compose_preverified(data, profile_name="v174", verified_by="caller"):
    """Composable entry for an already-verified base (e.g. v174 + title/peer/hp).

    The caller must have verified the base PE itself.  All structural checks
    still run; only the exact input SHA equality is skipped.
    """
    hits = [p for p in PROFILES.values() if p["name"] == profile_name]
    require(len(hits) == 1, "profile name %r count=%d" % (profile_name, len(hits)))
    return compose(data, hits[0], verify_hash=False, verified_by=verified_by)


def inspect(data):
    """Read-only facts about an arbitrary image. Never builds, never writes."""
    out = {"sha256": sha(data), "size": len(data), "knownProfile": INPUTS.get(sha(data))}
    try:
        pe = PE(data)
    except Exception as exc:
        out["error"] = "not PE32: %s" % exc
        return out
    out["sections"] = pe.count
    out["imageBase"] = hex(pe.base)
    profile = PROFILES.get(out["sha256"])
    try:
        sec = pe.find(SECTION_NAME)
        out["r195rx"] = {"rva": hex(sec["rva"]), "rawSize": sec["rs"],
                         "virtualSize": hex(sec["vs"]), "chars": hex(sec["chars"]),
                         "va": hex(pe.base + sec["rva"])}
        if profile:
            out["expectedVirtualSize"] = hex(profile["section_vsize"])
            out["virtualSizeMatchesDraft"] = sec["vs"] == profile["section_vsize"]
    except Exception as exc:
        out["r195rx"] = {"error": str(exc)}
    try:
        call = pe.offset(CALL_VA, 5)
        target = CALL_VA + 5 + struct.unpack_from("<i", data, call + 1)[0]
        out["call"] = {"bytes": data[call:call + 5].hex(), "target": hex(target)}
        if profile:
            out["call"]["matchesDraft"] = target == profile["chain_entry"]
    except Exception as exc:
        out["call"] = {"error": str(exc)}
    try:
        entry = profile["chain_entry"] if profile else (
            int(out["call"]["target"], 16) if "target" in out.get("call", {}) else 0x6EAD500)
        head = pe.read(entry, 8)
        out["chainHead"] = {"va": hex(entry), "bytes": head.hex(),
                            "looksLikeClassGate": head[:2] == b"\x80\x3f"}
    except Exception as exc:
        out["chainHead"] = {"error": str(exc)}
    try:
        sec = pe.find(SECTION_NAME)
        sec_va = pe.base + sec["rva"]
        live_end = sec_va + sec["vs"]
        candidate = profile["helper_va"] if profile else ((live_end + 0xFF) & ~0xFF)
        code = helper_code({"helper_va": candidate, "chain_entry": 0x6EAD500})
        if candidate + len(code) <= sec_va + sec["rs"]:
            tail = pe.read(live_end, candidate + len(code) - live_end)
            out["tail"] = {"liveEnd": hex(live_end), "candidateHelper": hex(candidate),
                           "range": [hex(live_end), hex(candidate + len(code))],
                           "allZero": set(tail) == {0}}
        else:
            out["tail"] = {"liveEnd": hex(live_end), "candidateHelper": hex(candidate),
                           "error": "no room in .r195rx"}
    except Exception as exc:
        out["tail"] = {"error": str(exc)}
    try:
        gate = pe.offset(0x6E83610, 19)
        fp = data[gate:gate + 15]
        out["bitmap"] = {"fingerprintMatches": fp == bytes.fromhex("0fb74424043dc205000077100fa305"),
                         "address": hex(struct.unpack_from("<I", data, gate + 15)[0])}
        bits = {}
        bitmap = pe.offset(0x6E92300, 256)
        for op in (296, 812, 903, 1119, 1203, 1302, 1304, 1467):
            at = bitmap + op // 8
            bits[str(op)] = bool(data[at] & (1 << (op % 8)))
        out["bitmap"]["bits"] = bits
    except Exception as exc:
        out["bitmap"] = {"error": str(exc)}
    out["buildAllowed"] = (
        profile is not None
        and out.get("virtualSizeMatchesDraft") is True
        and out.get("call", {}).get("matchesDraft") is True
        and out.get("tail", {}).get("allZero") is True
    )
    return out


def main(argv=None):
    if sys.flags.optimize:
        raise SystemExit("refusing optimized Python (assertions/checks must stay live)")
    ap = argparse.ArgumentParser(description="class-gate helper for v173 / v173-primary-v1")
    ap.add_argument("input", type=Path)
    ap.add_argument("--output", type=Path)
    ap.add_argument("--receipt", type=Path)
    ap.add_argument("--plan", action="store_true", help="compute and print windows; write nothing")
    ap.add_argument("--inspect", action="store_true",
                    help="read-only facts for any image; never builds")
    args = ap.parse_args(argv)

    data = args.input.read_bytes()
    if args.inspect:
        print(json.dumps(inspect(data), ensure_ascii=False, indent=2))
        return 0
    final, receipt = build(data)
    receipt["input"]["path"] = str(args.input.resolve())
    if args.plan:
        receipt["planOnly"] = True
        print(json.dumps(receipt, ensure_ascii=False, indent=2))
        return 0

    require(args.output is not None and args.receipt is not None,
            "output and receipt paths are required unless --plan is used")
    require(args.output.exists() is False, "refusing to overwrite output")
    require(args.receipt.exists() is False, "refusing to overwrite receipt")
    with args.output.open("xb") as f:
        f.write(final)
    with args.receipt.open("x", encoding="utf-8") as f:
        json.dump(receipt, f, ensure_ascii=False, indent=2)
    require(sha(args.output.read_bytes()) == receipt["output"]["sha256"], "output read-back mismatch")
    print(json.dumps({"output": receipt["output"], "helper": receipt["helper"]["va"]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
