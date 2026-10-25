#!/usr/bin/env python3
"""Verify a fixed v166 dungeon peer-retire candidate without installing it."""
from __future__ import annotations

import argparse
import ctypes
import hashlib
import importlib.metadata
import json
import os
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import build_candidate as builder
import unicorn
from capstone import CS_ARCH_X86, CS_MODE_32, Cs
from unicorn import Uc, UC_ARCH_X86, UC_HOOK_CODE, UC_MODE_32
from unicorn.x86_const import (
    UC_X86_REG_EAX,
    UC_X86_REG_EBP,
    UC_X86_REG_EBX,
    UC_X86_REG_ECX,
    UC_X86_REG_EDI,
    UC_X86_REG_EFLAGS,
    UC_X86_REG_EIP,
    UC_X86_REG_ESI,
    UC_X86_REG_ESP,
)

INPUT_SHA = "0e16090f0e4e3ac830f70183c814f5b24724cec235935e6c1221bf2d865d4243"
INPUT_SIZE = 111902720
CANDIDATE_SHA = "922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b"
REMOVE_SITE = 0x20175B8
KIND0_SITE = 0x2017359
REMOVE_WINDOW = 0x20175B0
KIND0_WINDOW = 0x2017350
REMOVE_END = 0x20175BD
KIND0_END = 0x201735E

SELF = 0x30000000
PEER = 0x30001000
ACTOR = 0x30002000
OTHER = 0x30003000
MODEL = 0x30005000
MODEL2 = 0x30006000
VTABLE = 0x30007000
STACK = 0x31008000
STOP = 0x32000000
DESTROY = 0x33001000
PARSER_START = 0x201693C
PARSER_END = 0x201697F
PARSER_SIZE = PARSER_END - PARSER_START
PARSER_FRAME = 0x34000000
PARSER_STACK = 0x34100000
KIND0_FRAME = STACK + 0x4000
GLOBAL_OWNER = 0x56A1E80
GLOBAL_ROUTE = 0x56A1E74
DECODERS = (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0)
EDI_SENTINEL = 0x55667788


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def h32(value):
    return f"0x{value & 0xffffffff:08x}"


def call_bytes(site, target):
    return b"\xe8" + struct.pack("<i", target - (site + 5))


class Image:
    def __init__(self, data):
        self.data = data
        self.pe = builder.PE(data)

    def read(self, va, size):
        return self.pe.read(va, size)


class Artifact:
    def __init__(self, input_data, candidate_data, manifest):
        self.input_data = input_data
        self.candidate_data = candidate_data
        self.original = Image(input_data)
        self.candidate = Image(candidate_data)
        self.base = int(manifest["base"], 16)
        self.helpers = [
            self.candidate.read(self.base + i * 0x200, item["size"])
            for i, item in enumerate(manifest["helpers"])
        ]

    def window(self, start, size, site, patched):
        image = self.candidate if patched else self.original
        data = image.read(start, size)
        if patched:
            target = self.base if start == REMOVE_WINDOW else self.base + 0x200
            offset = site - start
            require(data[offset : offset + 5] == call_bytes(site, target),
                    "candidate callsite relocation differs")
        return data


class Harness:
    def __init__(self, artifact, *, route=1, owner=0, peer_target=1020, self_target=1104):
        self.artifact = artifact
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        self.lookups = []
        self.cleanups = []
        self.namespace_hits = []
        self.forwarded = []
        self.destroys = []
        self.forward_spy = False
        self.lookup_map = {1104: ACTOR, 1020: OTHER}
        self.namespace_owner = 0
        self.namespace_route = 1

        self.put(0, bytes(4096))
        self.put(STACK - 0x8000, bytes(0x10000))
        self.put(STOP, b"\xcc")
        for address in (SELF, PEER, ACTOR, OTHER):
            self.put(address, bytes(4096))
        self.put(0x29733B0, artifact.original.read(0x29733B0, 0x6A0))
        self.put(0x29730E0, artifact.original.read(0x29730E0, 0x25))
        self.put(0x29614D0, artifact.original.read(0x29614D0, 16))
        self.put(0x2969430, artifact.original.read(0x2969430, 0x2C0))
        self.put(0x5656000, bytes(4096))
        self.w(0x56566B0, 1)
        self.put(0x56A1000, bytes(4096))
        self.put(0x56A3000, bytes(4096))
        self.put(0x55B3000, bytes(4096))
        for address in (0x37E3940, 0x37E3970, *DECODERS, 0x296D730, 0x1A5A5E0,
                        0x296DD80, 0x37EA650, 0x2047360, 0x2046E80, 0x1FA2790,
                        0x2046CC0, 0x2524FB0):
            self.put(address, b"\xcc")
        for record in (SELF, PEER):
            for slot in range(8):
                self.w(record + 0x2C + slot * 4, 0xFFFF)
            self.w(record + 0x70, 0)
            self.w(record + 0x74, 1)
        self.put(artifact.base, artifact.helpers[0])
        self.put(artifact.base + 0x200, artifact.helpers[1])
        self.w(SELF + 4, self_target)
        self.w(PEER + 4, peer_target)
        self.w(ACTOR + 0x20, 1104)
        self.w(OTHER + 0x20, 1020)
        for actor, model in ((ACTOR, MODEL), (OTHER, MODEL2)):
            self.w(actor + 0x4F8, model)
            self.w(model, VTABLE)
        self.w(VTABLE, DESTROY)
        self.put(DESTROY, b"\xcc")
        self.w(0x5656680, 1)
        self.put(0x57B6000, bytes(0x1000))
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.apply_parser_fields(SELF, route=route, owner=owner)
        self.apply_parser_fields(PEER, route=route, owner=owner)

    def put(self, address, data):
        for page in range(address & ~4095, (address + len(data) + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.u.mem_map(page, 4096)
                self.pages.add(page)
        self.u.mem_write(address, data)

    def w(self, address, value):
        self.put(address, struct.pack("<I", value & 0xffffffff))

    def r(self, address):
        return int.from_bytes(self.u.mem_read(address, 4), "little")

    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        self.u.reg_write(UC_X86_REG_EIP, self.r(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        self.u.reg_write(UC_X86_REG_EAX, value)

    def hook(self, uc, address, size, data):
        sp = uc.reg_read(UC_X86_REG_ESP)
        cx = uc.reg_read(UC_X86_REG_ECX)
        if address == STOP:
            uc.emu_stop()
            return
        if address == 0x29733B0 and self.forward_spy:
            self.forwarded.append({"ecx": h32(cx), "slot": self.r(sp + 4), "key": self.r(sp + 8)})
            self.ret(0, pop=8)
            return
        if address in DECODERS:
            if cx == GLOBAL_OWNER:
                value, role = self.namespace_owner, "owner/P+74"
            elif cx == GLOBAL_ROUTE:
                value, role = self.namespace_route, "route/P+70"
            else:
                raise AssertionError(f"unexpected obfuscated global {cx:#x}")
            self.namespace_hits.append({"global": h32(cx), "role": role, "value": value})
            self.w(self.r(sp + 8), value)
            self.ret(pop=8)
            return
        if address in (0x296D730, 0x1A5A5E0):
            key = self.r(sp + 4)
            self.lookups.append({"key": key, "entry": h32(address)})
            self.ret(self.lookup_map.get(key, 0))
            return
        if address == 0x296DD80:
            self.ret(0)
            return
        if address in (0x37E3940, 0x37E3970):
            self.ret(0)
            return
        if address == 0x37EA650:
            self.ret(1, pop=12)
            return
        if address == 0x2047360:
            self.ret(0, pop=4)
            return
        if address in (0x2046E80, 0x1FA2790, 0x2046CC0, 0x2524FB0):
            self.ret(0)
            return
        if address == DESTROY:
            self.destroys.append(uc.reg_read(UC_X86_REG_ECX))
            self.ret(0, pop=4)
            return
        if address == 0x2969430:
            self.cleanups.append(cx)

    def apply_parser_fields(self, record, *, route, owner, ordinal=0, extra=0):
        self.put(PARSER_START, self.artifact.original.read(PARSER_START, PARSER_SIZE))
        self.put(0xB39D20, self.artifact.original.read(0xB39D20, 4))
        self.put(PARSER_FRAME - 0x1000, bytes(0x3000))
        self.put(PARSER_STACK - 0x1000, bytes(0x3000))
        self.w(PARSER_FRAME - 0x38E, route)
        self.w(PARSER_FRAME - 0x38A, owner)
        self.w(PARSER_FRAME - 0x3E0, ordinal)
        self.w(PARSER_FRAME - 0x366, extra)
        before_target = self.r(record + 4)
        hit = {"address": None}

        def stop_parser(uc, address, size, data):
            hit["address"] = address
            uc.emu_stop()

        self.u.hook_add(UC_HOOK_CODE, stop_parser, begin=PARSER_END, end=PARSER_END + 1)
        self.u.reg_write(UC_X86_REG_EBX, record)
        self.u.reg_write(UC_X86_REG_EBP, PARSER_FRAME)
        self.u.reg_write(UC_X86_REG_ESP, PARSER_STACK)
        self.u.emu_start(PARSER_START, PARSER_END, count=1000)
        require(self.u.reg_read(UC_X86_REG_EIP) == PARSER_END, "parser fragment did not stop")
        observed = {
            "record": h32(record),
            "routeInput": route,
            "ownerInput": owner,
            "ordinalInput": ordinal,
            "extraInput": extra,
            "P70": self.r(record + 0x70),
            "P74": self.r(record + 0x74),
            "P78": self.r(record + 0x78),
            "P7C": self.r(record + 0x7C),
            "P4Before": h32(before_target),
            "P4After": h32(self.r(record + 4)),
        }
        require(observed["P70"] == route and observed["P74"] == owner, "parser route/owner proof failed")
        require(observed["P78"] == ordinal and observed["P7C"] == extra, "parser trailing fields differ")
        require(observed["P4After"] == observed["P4Before"], "parser changed target key")
        return observed

    def _run_window(self, start, code, end, *, ebx, esi, ebp):
        self.put(start, code)
        hit = {"address": None}

        def stop_window(uc, address, size, data):
            hit["address"] = address
            uc.emu_stop()

        self.u.hook_add(UC_HOOK_CODE, stop_window, begin=end, end=end + 1)
        self.w(STACK, STOP)
        self.u.reg_write(UC_X86_REG_EBX, ebx)
        self.u.reg_write(UC_X86_REG_ESI, esi)
        self.u.reg_write(UC_X86_REG_EDI, EDI_SENTINEL)
        self.u.reg_write(UC_X86_REG_EBP, ebp)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x202)
        lookup_start = len(self.lookups)
        cleanup_start = len(self.cleanups)
        destroy_start = len(self.destroys)
        namespace_start = len(self.namespace_hits)
        forward_start = len(self.forwarded)
        self.u.emu_start(start, STOP, count=30000)
        require(hit["address"] == end, f"callsite did not return to {h32(end)}")
        expected = {
            "ebx": ebx & 0xffffffff,
            "esi": esi & 0xffffffff,
            "edi": EDI_SENTINEL,
            "ebp": ebp & 0xffffffff,
            "esp": STACK & 0xffffffff,
            "eip": end & 0xffffffff,
        }
        actual = {
            "ebx": self.u.reg_read(UC_X86_REG_EBX),
            "esi": self.u.reg_read(UC_X86_REG_ESI),
            "edi": self.u.reg_read(UC_X86_REG_EDI),
            "ebp": self.u.reg_read(UC_X86_REG_EBP),
            "esp": self.u.reg_read(UC_X86_REG_ESP),
            "eip": self.u.reg_read(UC_X86_REG_EIP),
        }
        require(actual == expected, f"nonvolatile ABI mismatch expected={expected} actual={actual}")
        return {
            "callsite": {
                "start": h32(start),
                "end": h32(end),
                "returnedToEnd": True,
                "espRestored": True,
                "patchedBytes": code.hex(),
            },
            "newLookups": self.lookups[lookup_start:],
            "newCleanups": [h32(value) for value in self.cleanups[cleanup_start:]],
            "newDestroys": [h32(value) for value in self.destroys[destroy_start:]],
            "namespaceHits": self.namespace_hits[namespace_start:],
            "forwardedCalls": self.forwarded[forward_start:],
            "nonvolatileRegisters": {
                "expected": {key: h32(value) for key, value in expected.items()},
                "actual": {key: h32(value) for key, value in actual.items()},
                "equal": True,
                "flagsAfter": h32(self.u.reg_read(UC_X86_REG_EFLAGS)),
                "flagsCompared": False,
            },
        }

    def run_remove_callsite(self, *, slot, patched, spy=False):
        self.forward_spy = spy
        code = self.artifact.window(REMOVE_WINDOW, REMOVE_END - REMOVE_WINDOW, REMOVE_SITE, patched)
        return self._run_window(REMOVE_WINDOW, code, REMOVE_END, ebx=PEER, esi=slot, ebp=STACK + 0x200)

    def run_kind0_callsite(self, *, slot, key, patched, spy=False):
        self.forward_spy = spy
        self.w(KIND0_FRAME + slot * 2 - 0x28, key)
        code = self.artifact.window(KIND0_WINDOW, KIND0_END - KIND0_WINDOW, KIND0_SITE, patched)
        return self._run_window(KIND0_WINDOW, code, KIND0_END, ebx=SELF, esi=slot, ebp=KIND0_FRAME)

    def state(self):
        return {
            "actorPartyPointer": h32(self.r(ACTOR + 0x4C8)),
            "otherPartyPointer": h32(self.r(OTHER + 0x4C8)),
            "actorDrawable": h32(self.r(ACTOR + 0x4F8)),
            "otherDrawable": h32(self.r(OTHER + 0x4F8)),
            "selfSlot0": self.r(SELF + 0x2C) & 0xFFFF,
            "peerSlot0": self.r(PEER + 0x2C) & 0xFFFF,
            "lookupCalls": len(self.lookups),
            "cleanupCalls": len(self.cleanups),
            "destroyedModels": [h32(value) for value in self.destroys],
            "namespaceHits": list(self.namespace_hits),
            "forwardedCalls": list(self.forwarded),
        }


def probe_image_no_execute(path):
    if os.name != "nt":
        return {"mappingSucceeded": False, "reason": "Windows required", "executed": False}
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateFileW.argtypes = [ctypes.c_wchar_p, ctypes.c_uint32, ctypes.c_uint32,
                                   ctypes.c_void_p, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_void_p]
    kernel.CreateFileW.restype = ctypes.c_void_p
    kernel.CreateFileMappingW.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_uint32,
                                          ctypes.c_uint32, ctypes.c_uint32, ctypes.c_wchar_p]
    kernel.CreateFileMappingW.restype = ctypes.c_void_p
    kernel.CloseHandle.argtypes = [ctypes.c_void_p]
    handle = kernel.CreateFileW(str(path), 0x80000000, 1, None, 3, 0, None)
    require(handle and handle != ctypes.c_void_p(-1).value,
            f"CreateFileW failed: {ctypes.get_last_error()}")
    mapping = None
    try:
        mapping = kernel.CreateFileMappingW(handle, None, 0x11000002, 0, 0, None)
        require(mapping, f"CreateFileMappingW(SEC_IMAGE_NO_EXECUTE) failed: {ctypes.get_last_error()}")
    finally:
        if mapping:
            kernel.CloseHandle(mapping)
        kernel.CloseHandle(handle)
    return {"mappingSucceeded": True, "executed": False}


def create_artifact(input_path, candidate_path):
    original = input_path.read_bytes()
    candidate = candidate_path.read_bytes()
    require(len(original) == INPUT_SIZE and sha256(original) == INPUT_SHA, "input is not fixed v166")
    rebuilt, manifest = builder.build(original)
    require(candidate == rebuilt, "candidate differs from deterministic rebuild")
    require(sha256(candidate) == CANDIDATE_SHA, "candidate fixed SHA256 differs")
    artifact = Artifact(original, candidate, manifest)
    for actual, expected in zip(artifact.helpers, manifest["helpers"]):
        require(sha256(actual) == expected["sha256"], "candidate helper bytes differ from manifest")
    require(artifact.original.read(REMOVE_SITE, 5) == call_bytes(REMOVE_SITE, 0x29733B0),
            "original kind3 callsite differs")
    require(artifact.original.read(KIND0_SITE, 5) == call_bytes(KIND0_SITE, 0x29733B0),
            "original kind0 callsite differs")
    require(artifact.candidate.read(REMOVE_SITE, 5) == call_bytes(REMOVE_SITE, artifact.base),
            "candidate kind3 callsite differs")
    require(artifact.candidate.read(KIND0_SITE, 5) == call_bytes(KIND0_SITE, artifact.base + 0x200),
            "candidate kind0 callsite differs")
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    decoded = []
    for index, helper in enumerate(artifact.helpers):
        instructions = list(md.disasm(helper, artifact.base + index * 0x200))
        require(b"".join(instruction.bytes for instruction in instructions) == helper,
                f"helper {index} contains undecodable bytes")
        decoded.append(len(instructions))
    return artifact, {
        "inputSha256": sha256(original),
        "candidateSha256": sha256(candidate),
        "candidateSize": len(candidate),
        "base": h32(artifact.base),
        "helpers": [
            {"va": h32(artifact.base + i * 0x200), "size": len(code),
             "sha256": sha256(code), "instructions": decoded[i]}
            for i, code in enumerate(artifact.helpers)
        ],
        "patches": [
            {"site": h32(REMOVE_SITE), "before": artifact.original.read(REMOVE_SITE, 5).hex(),
             "after": artifact.candidate.read(REMOVE_SITE, 5).hex()},
            {"site": h32(KIND0_SITE), "before": artifact.original.read(KIND0_SITE, 5).hex(),
             "after": artifact.candidate.read(KIND0_SITE, 5).hex()},
        ],
        "parserProof": {
            "range": [h32(PARSER_START), h32(PARSER_END)],
            "bytesSha256": sha256(artifact.original.read(PARSER_START, PARSER_SIZE)),
        },
    }


def original_group(artifact):
    rows = []

    def add(name, harness, call, **extra):
        rows.append({"name": name, "passed": True, "call": call, "state": harness.state(), **extra})

    for patched in (False, True):
        h = Harness(artifact)
        h.w(PEER + 0x2C, 1020)
        h.w(OTHER + 0x4C8, PEER)
        call = h.run_remove_callsite(slot=0, patched=patched)
        state = h.state()
        require(state["otherDrawable"] == "0x00000000", "departing actor drawable remains")
        require(state["destroyedModels"] == [h32(MODEL2)], "departing actor model was not destroyed")
        require(state["peerSlot0"] == 0xFFFF and state["otherPartyPointer"] == "0x00000000",
                "departing actor slot/pointer was not cleared")
        add("original_target_cleanup" if not patched else "helper_target_cleanup", h, call)

    h = Harness(artifact)
    h.w(PEER + 0x2C, 1104)
    h.w(ACTOR + 0x4C8, PEER)
    remove = h.run_remove_callsite(slot=0, patched=True)
    removed = h.state()
    require(removed["actorDrawable"] == h32(MODEL), "surviving actor drawable changed")
    require(removed["cleanupCalls"] == 0 and not removed["destroyedModels"],
            "surviving actor was cleaned/destroyed")
    require(removed["actorPartyPointer"] == "0x00000000" and removed["peerSlot0"] == 0xFFFF,
            "retiring record association was not cleared")
    h.w(SELF + 0x2C, 1104)
    kind0 = h.run_kind0_callsite(slot=0, key=1104, patched=True)
    require(h.state()["actorPartyPointer"] == h32(SELF), "kind0 did not refill null binding")
    add("member_survives_then_kind0_refill", h, {"remove": remove, "kind0": kind0})

    h = Harness(artifact)
    h.w(SELF + 0x2C, 1104)
    h.w(ACTOR + 0x4C8, 0)
    call = h.run_kind0_callsite(slot=0, key=1104, patched=True)
    require(h.state()["actorPartyPointer"] == h32(SELF), "same-key null refill failed")
    add("kind0_existing_same_key_null_refill", h, call)

    h = Harness(artifact)
    h.w(SELF + 0x2C, 0xFFFF)
    h.w(ACTOR + 0x4C8, 0)
    call = h.run_kind0_callsite(slot=0, key=1104, patched=True)
    require(h.state()["actorPartyPointer"] == h32(SELF) and h.state()["selfSlot0"] == 1104,
            "new-self namespace path did not bind")
    require(any(item["global"] == h32(GLOBAL_OWNER) and item["value"] == 0
                for item in call["namespaceHits"]), "owner namespace was not used")
    add("kind0_new_self_real_namespace_globals", h, call)

    h = Harness(artifact)
    h.w(SELF + 0x2C, 1104)
    h.w(ACTOR + 0x4C8, PEER)
    call = h.run_kind0_callsite(slot=0, key=1104, patched=True)
    require(h.state()["actorPartyPointer"] == h32(PEER), "nonzero association overwritten")
    add("kind0_preserves_nonzero_binding", h, call)

    h = Harness(artifact)
    h.w(PEER + 0x2C, 1104)
    h.lookup_map = {}
    call = h.run_remove_callsite(slot=0, patched=True)
    require(h.state()["peerSlot0"] == 0xFFFF and h.state()["cleanupCalls"] == 0
            and not h.state()["destroyedModels"], "lookup miss caused cleanup or failed to clear slot")
    add("remove_actor_lookup_miss", h, call)

    h = Harness(artifact)
    h.w(PEER + 0x2C, 1104)
    h.w(OTHER + 0x4C8, PEER)
    h.lookup_map = {1104: OTHER}
    call = h.run_remove_callsite(slot=0, patched=True)
    require(h.state()["peerSlot0"] == 0xFFFF and h.state()["cleanupCalls"] == 0
            and not h.state()["destroyedModels"], "actor key mismatch caused cleanup or failed to clear slot")
    require(h.state()["otherPartyPointer"] == h32(PEER), "mismatched actor pointer was modified")
    add("remove_actor_key_mismatch", h, call)

    h = Harness(artifact)
    h.w(PEER + 0x2C, 1104)
    h.w(ACTOR + 0x4C8, SELF)
    call = h.run_remove_callsite(slot=0, patched=True)
    require(h.state()["actorPartyPointer"] == h32(SELF) and h.state()["peerSlot0"] == 0xFFFF,
            "foreign record pointer was cleared or slot not cleared")
    add("remove_pointer_to_other_record_preserved", h, call)

    for name, route, owner in (
        ("remove_wrong_route_forwards", 0, 0),
        ("remove_nonzero_other_owner_forwards", 1, 2),
    ):
        h = Harness(artifact, route=route, owner=owner)
        h.w(PEER + 0x2C, 1104)
        call = h.run_remove_callsite(slot=0, patched=True, spy=True)
        require(call["forwardedCalls"] and not call["newDestroys"], f"{name}: did not forward safely")
        add(name, h, call)

    h = Harness(artifact)
    h.w(PEER + 0x2C, 1104)
    call = h.run_remove_callsite(slot=-1, patched=True, spy=True)
    require(call["forwardedCalls"] and call["forwardedCalls"][0]["slot"] == 0xFFFFFFFF,
            "negative slot did not forward")
    add("remove_invalid_negative_slot_forwards", h, call)

    h = Harness(artifact)
    h.w(SELF + 0x2C, 1104)
    call = h.run_kind0_callsite(slot=8, key=1104, patched=True, spy=True)
    require(call["forwardedCalls"] and call["forwardedCalls"][0]["slot"] == 8,
            "high slot did not forward")
    add("kind0_invalid_high_slot_forwards", h, call)
    require(len(rows) == 13, f"original group row count {len(rows)}")
    return rows


def supplemental_group(artifact):
    rows = []

    def record(name, slot, call, **extra):
        rows.append({"name": name, "slot": slot, "passed": True, "call": call, **extra})

    def remove_state(h, slot):
        return {
            "peerSlot": h.r(PEER + slot * 4 + 0x2C) & 0xFFFF,
            "actorPartyPointer": h32(h.r(ACTOR + 0x4C8)),
            "actorDrawable": h32(h.r(ACTOR + 0x4F8)),
            "lookupCalls": len(h.lookups),
            "cleanupCalls": len(h.cleanups),
            "destroyedModels": [h32(value) for value in h.destroys],
        }

    def kind0_state(h, slot):
        return {
            "selfSlot": h.r(SELF + slot * 4 + 0x2C) & 0xFFFF,
            "actorPartyPointer": h32(h.r(ACTOR + 0x4C8)),
            "actorDrawable": h32(h.r(ACTOR + 0x4F8)),
            "lookupCalls": len(h.lookups),
            "cleanupCalls": len(h.cleanups),
            "destroyedModels": [h32(value) for value in h.destroys],
        }

    for slot in range(1, 8):
        h = Harness(artifact)
        h.w(PEER + slot * 4 + 0x2C, 1104)
        h.w(ACTOR + 0x4C8, PEER)
        call = h.run_remove_callsite(slot=slot, patched=True)
        state = remove_state(h, slot)
        require(state["peerSlot"] == 0xFFFF, f"kind3 slot {slot}: association not cleared")
        require(state["actorPartyPointer"] == "0x00000000", f"kind3 slot {slot}: old pointer remains")
        require(state["actorDrawable"] == h32(MODEL), f"kind3 slot {slot}: drawable changed")
        require(state["cleanupCalls"] == 0 and not state["destroyedModels"],
                f"kind3 slot {slot}: cleanup/destroy")
        record(f"kind3_slot_{slot}_other_member_positive", slot, call, state=state)

        h = Harness(artifact)
        h.w(PEER + slot * 4 + 0x2C, h.r(PEER + 4) & 0xFFFF)
        call = h.run_remove_callsite(slot=slot, patched=True, spy=True)
        state = remove_state(h, slot)
        require(call["forwardedCalls"] and call["forwardedCalls"][0]["slot"] == slot,
                f"kind3 slot {slot}: self key did not forward")
        require(state["peerSlot"] == (h.r(PEER + 4) & 0xFFFF), f"kind3 slot {slot}: self key changed")
        record(f"kind3_slot_{slot}_self_key_negative", slot, call, state=state)

        h = Harness(artifact)
        h.w(PEER + slot * 4 + 0x2C, 0xFFFF)
        call = h.run_remove_callsite(slot=slot, patched=True, spy=True)
        state = remove_state(h, slot)
        require(call["forwardedCalls"] and call["forwardedCalls"][0]["slot"] == slot,
                f"kind3 slot {slot}: empty slot did not forward")
        require(state["peerSlot"] == 0xFFFF, f"kind3 slot {slot}: empty slot changed")
        record(f"kind3_slot_{slot}_empty_negative", slot, call, state=state)

        h = Harness(artifact)
        h.w(SELF + slot * 4 + 0x2C, 1104)
        h.w(ACTOR + 0x4C8, 0)
        call = h.run_kind0_callsite(slot=slot, key=1104, patched=True)
        state = kind0_state(h, slot)
        require(state["actorPartyPointer"] == h32(SELF), f"kind0 slot {slot}: null binding not filled")
        require(state["actorDrawable"] == h32(MODEL), f"kind0 slot {slot}: drawable changed")
        record(f"kind0_slot_{slot}_null_binding_positive", slot, call, state=state)

        h = Harness(artifact)
        h.w(SELF + slot * 4 + 0x2C, 1104)
        h.w(ACTOR + 0x4C8, PEER)
        call = h.run_kind0_callsite(slot=slot, key=1104, patched=True)
        state = kind0_state(h, slot)
        require(state["actorPartyPointer"] == h32(PEER), f"kind0 slot {slot}: existing binding changed")
        require(state["actorDrawable"] == h32(MODEL), f"kind0 slot {slot}: drawable changed")
        record(f"kind0_slot_{slot}_existing_binding_negative", slot, call, state=state)

    for name, route, owner in (
        ("kind0_wrong_route_negative", 0, 0),
        ("kind0_wrong_owner_negative", 1, 2),
    ):
        h = Harness(artifact, route=route, owner=owner)
        h.w(SELF + 0x2C, 1104)
        h.w(ACTOR + 0x4C8, 0)
        call = h.run_kind0_callsite(slot=0, key=1104, patched=True, spy=True)
        require(call["forwardedCalls"], f"{name}: original setter not reached")
        require(h.state()["actorPartyPointer"] == "0x00000000", f"{name}: actor binding changed")
        record(name, 0, call, state=kind0_state(h, 0))

    h = Harness(artifact)
    h.w(SELF + 0x2C, 0xFFFF)
    h.w(ACTOR + 0x4C8, 0)
    call = h.run_kind0_callsite(slot=0, key=0xFFFF, patched=True, spy=True)
    require(call["forwardedCalls"], "kind0 FFFF: original setter not reached")
    require(h.state()["actorPartyPointer"] == "0x00000000", "kind0 FFFF: actor binding changed")
    record("kind0_ffff_key_negative", 0, call, state=kind0_state(h, 0))
    require(len(rows) == 38, f"supplemental row count {len(rows)}")
    return rows


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path, help="fixed v166 input EXE")
    parser.add_argument("--candidate", required=True, type=Path, help="candidate EXE to verify")
    parser.add_argument("--receipt", required=True, type=Path, help="new JSON receipt path")
    args = parser.parse_args(argv)
    require(__debug__, "optimized Python is unsupported")
    require(unicorn.__version__ == "2.1.4", f"unicorn 2.1.4 required, got {unicorn.__version__}")
    require(importlib.metadata.version("capstone") == "5.0.7",
            f"capstone 5.0.7 required, got {importlib.metadata.version('capstone')}")
    require(args.input.is_file(), f"input missing: {args.input}")
    require(args.candidate.is_file(), f"candidate missing: {args.candidate}")
    require(args.receipt.parent.is_dir(), f"receipt directory missing: {args.receipt.parent}")
    require(not args.receipt.exists(), f"receipt already exists: {args.receipt}")

    artifact, artifact_receipt = create_artifact(args.input, args.candidate)
    parser_harness = Harness(artifact)
    parser_receipt = {
        "peer": parser_harness.apply_parser_fields(PEER, route=1, owner=0),
        "self": parser_harness.apply_parser_fields(SELF, route=1, owner=0),
    }
    image_mapping = probe_image_no_execute(args.candidate)
    regression = original_group(artifact)
    supplemental = supplemental_group(artifact)
    receipt = {
        "schema": "r195-dungeon-peer-retire-public-verification-v1",
        "passed": True,
        "artifact": artifact_receipt,
        "parserProof": parser_receipt,
        "windowsImageNoExecuteMapping": image_mapping,
        "dependencies": {
            "python": sys.version.split()[0],
            "unicorn": unicorn.__version__,
            "capstone": importlib.metadata.version("capstone"),
            "bytecodeDisabled": sys.dont_write_bytecode,
        },
        "groups": {
            "original": {"expected": 13, "passed": len(regression), "cases": regression},
            "supplemental": {"expected": 38, "passed": len(supplemental), "cases": supplemental},
        },
        "totals": {"cases": len(regression) + len(supplemental), "failed": 0},
        "registerPolicy": {
            "requiredEqual": ["EBX", "ESI", "EDI", "EBP", "ESP", "EIP"],
            "eflagsCompared": False,
            "reason": "The original setter changes flags and both patched callers do not consume them.",
        },
        "fixtures": [
            "Synthetic record, actor, model, registry lookup and namespace decoder leaves",
            "Original setter/getter/accessor/cleanup windows execute",
            "Actual candidate helper and patched callsite bytes execute",
        ],
        "limitations": [
            "No complete op9 handler, UDP transport, rendering, GUI or live game replay",
            "Candidate is not signed, installed or released",
            "Synthetic actors and record slots do not prove every live server packet sequence",
        ],
    }
    with args.receipt.open("x", encoding="utf8") as stream:
        stream.write(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n")
    require(args.receipt.is_file() and args.receipt.stat().st_size > 0, "receipt readback failed")
    print(json.dumps({
        "passed": True,
        "cases": receipt["totals"]["cases"],
        "candidateSha256": sha256(args.candidate.read_bytes()),
        "receipt": str(args.receipt),
    }))


if __name__ == "__main__":
    main()
