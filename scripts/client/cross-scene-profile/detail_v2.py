"""Portable copy of the final v2 mode3 detail-candidate simulation."""
import hashlib
import struct

from unicorn import (Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE,
                     UC_HOOK_MEM_WRITE)
from unicorn.x86_const import *

import aux_cache
import portable_core


DETAIL_EXTRA_RANGES = {0x22C42E6: 0x72, 0x22BEF00: 0x150}


def run(aux_windows, extra, manifest, name, key, channel, server, active,
        categories, patched=True):
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    pages = set()

    def put(address, data):
        for page in range(address & ~4095,
                          (address + len(data) + 4095) & ~4095, 4096):
            if page not in pages:
                u.mem_map(page, 4096)
                pages.add(page)
        u.mem_write(address, data)

    def w(address, value):
        put(address, struct.pack("<I", value))

    def rd(address):
        return int.from_bytes(u.mem_read(address, 4), "little")

    aux_cache.install(u, put, w, categories, aux_windows)
    for address, code in extra.items():
        put(address, code)
    site = int(manifest["site"], 16)
    assert bytes(u.mem_read(site, 5)).hex() == manifest["expected"]
    if patched:
        put(site, bytes.fromhex(manifest["replacement"]))
        put(int(manifest["candidateAddress"], 16),
            bytes.fromhex(manifest["code"]))
    put(0x100000, bytes(0x10000))
    for target in (0x296D730, 0x2971960, 0x22BE730):
        put(target, b"\xcc")
    w(0x56A3758, 0x250000)
    put(0x568A519, bytes([channel, server]))
    frame = 0x108800
    stack = 0x108000
    w(frame - 0x4C8, key)
    guard = bytes(range(64))
    put(stack, guard)
    saved = {
        UC_X86_REG_EBX: 0xB0B0B0B0,
        UC_X86_REG_ESI: 0x51515151,
        UC_X86_REG_EDI: 0xD1D1D1D1,
        UC_X86_REG_EBP: frame,
    }
    for reg, value in saved.items():
        u.reg_write(reg, value)
    u.reg_write(UC_X86_REG_ESP, stack)
    packed = aux_cache.identity(key, channel, server)
    cached = 0
    for index in range(5):
        if packed in categories.get(index, {}):
            cached = categories[index][packed]
    expected = 0x210000 if active else (cached if patched else 0)
    visits = []
    outcome = None

    def ret(value):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xC1C1C1C1)
        u.reg_write(UC_X86_REG_EDX, 0xD2D2D2D2)
        u.reg_write(UC_X86_REG_EFLAGS, 0x246)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4)

    def code(_u, address, _n, _data):
        nonlocal outcome
        sp = u.reg_read(UC_X86_REG_ESP)
        if address == 0x3E738B1:
            raise AssertionError("native iterator failure")
        if address == 0x296D730:
            assert rd(sp + 4) == key
            visits.append("active")
            ret(0x210000 if active else 0)
        elif address == 0x1A5A5E0:
            visits.append("native-aux")
        elif address == 0x22BEF00:
            visits.append("legacy-resolver")
        elif address == site + 5 and expected:
            assert u.reg_read(UC_X86_REG_EAX) == expected
            assert sp == stack - 4 and rd(sp) == key
            assert all(u.reg_read(reg) == value
                       for reg, value in saved.items())
        elif address == 0x2971960:
            assert expected == 0 and rd(sp + 4) == key
            assert u.reg_read(UC_X86_REG_ECX) == 0x250000
            outcome = "legacy-create-entry"
            u.emu_stop()
        elif address == 0x22BE730:
            assert expected and rd(sp + 4) == expected
            assert u.reg_read(UC_X86_REG_EDI) == expected and sp == stack - 8
            assert all(u.reg_read(reg) == value for reg, value in saved.items()
                       if reg != UC_X86_REG_EDI)
            outcome = "existing-actor-stat-entry"
            u.emu_stop()

    def write(_u, _access, address, size, _value, _data):
        assert 0x100000 <= address and address + size <= stack, (
            hex(address), size)

    u.hook_add(UC_HOOK_CODE, code)
    u.hook_add(UC_HOOK_MEM_WRITE, write)
    u.emu_start(0x22C42E6, 0, count=20000)
    assert outcome and bytes(u.mem_read(stack, 64)) == guard
    if patched:
        expected_visits = (["active"] if active else
                           ["active", "native-aux"] +
                           (["legacy-resolver", "active"] if not cached else []))
        assert visits == expected_visits, visits
    else:
        assert visits == ["legacy-resolver", "active"]
    return dict(name=name, identity=[key, channel, server], patched=patched,
                outcome=outcome, returnedActor=hex(expected), calls=visits,
                realAuxLookup=True, heapWrites=0, passed=True)


def run_suite(root, image, manifests):
    aux_windows = portable_core.read_windows(image, aux_cache.RANGES)
    ranges = dict(aux_cache.RANGES)
    ranges.update(DETAIL_EXTRA_RANGES)
    windows = portable_core.read_windows(image, ranges)
    extra = windows
    manifest_path = "manifests/detail-candidate/windows-v1.json"
    manifest = manifests[manifest_path]
    cases = []
    for key in (1, 4097, 65535):
        for channel in (0, 10, 255):
            for server in (0, 1, 255):
                packed = aux_cache.identity(key, channel, server)
                states = [
                    ("active", True, {1: {packed: 0x220000}}),
                    ("aux1", False, {1: {packed: 0x220000}}),
                    ("aux4", False, {4: {packed: 0x230000}}),
                    ("last-wins", False,
                     {1: {packed: 0x220000}, 4: {packed: 0x230000}}),
                    ("wrong-context", False,
                     {1: {aux_cache.identity(key, channel ^ 1, server): 0x220000}}),
                    ("null-last", False,
                     {1: {packed: 0x220000}, 4: {packed: 0}}),
                    ("empty", False, {}),
                ]
                for name, active, categories in states:
                    cases.append(run(aux_windows, extra, manifest, name, key,
                                     channel, server, active, categories))
    baseline = [run(aux_windows, extra, manifest, name, 17, 10, 1, active,
                    categories, False)
                for name, active, categories in [
                    ("active", True, {}),
                    ("existing-aux", False,
                     {1: {aux_cache.identity(17, 10, 1): 0x220000}}),
                    ("empty", False, {}),
                ]]
    return dict(
        passed=True,
        exeSha256=portable_core.sha256_hex(image),
        cases=cases,
        baseline=baseline,
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in extra.items()},
        sourceHashes=portable_core.proof_sources(
            root, ["aux_cache.py", "detail_v2.py", manifest_path]),
        scope=("Exact local mode3 selection and legacy resolver through stat "
               "entry or creation entry. Real aux hash/list lookup. Only active "
               "scene lookup and object construction/stat parsing remain "
               "boundaries. No GUI or lifetime proof."),
        processAccess=False,
        exeGenerated=False,
        deployed=False,
        guiAccepted=False,
    )
