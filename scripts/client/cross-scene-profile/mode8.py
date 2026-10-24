"""Portable copy of the reviewed mode8 route candidate simulation."""
import hashlib
import struct

from unicorn import (Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE,
                     UC_HOOK_MEM_WRITE)
from unicorn.x86_const import *

import aux_cache
import portable_core


EXTRA_RANGES = {0x22C5054: 0x6F, 0x22C42E6: 0x72, 0x22BEF00: 0x150}


def run(aux_windows, windows, manifest, detail, mode, key, channel, server, caller,
        active, categories, patched=True):
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
    for address, data in windows.items():
        put(address, data)
    for row in manifest["patches"]:
        address = int(row["site"], 16)
        assert bytes(u.mem_read(address, 6)).hex() == row["expected"]
        if patched:
            put(address, bytes.fromhex(row["replacement"]))
            put(int(row["candidateAddress"], 16),
                bytes.fromhex(row["code"]))
    if patched:
        put(int(detail["site"], 16), bytes.fromhex(detail["replacement"]))
        put(int(detail["candidateAddress"], 16),
            bytes.fromhex(detail["code"]))
    put(0x100000, bytes(0x10000))
    frame = 0x108800
    stack = 0x108000
    guard = bytes(range(64))
    put(stack, guard)
    w(frame + 4, caller)
    w(frame - 0x4B8, key)
    w(frame - 0x4C8, key)
    w(0x56A3758, 0x250000)
    put(0x568A519, bytes([channel, server]))
    for target in (0x296D730, 0x2971960, 0x1A62AA0, 0x2965750,
                   0x22BE730):
        put(target, b"\xcc")
    saved = {UC_X86_REG_EBX: 0xB0B0B0B0, UC_X86_REG_EBP: frame}
    for reg, value in saved.items():
        u.reg_write(reg, value)
    u.reg_write(UC_X86_REG_EDI, key)
    u.reg_write(UC_X86_REG_ESI, 0x51515151)
    u.reg_write(UC_X86_REG_ESP, stack)
    packed = aux_cache.identity(key, channel, server)
    cached = 0
    for index in range(5):
        if packed in categories.get(index, {}):
            cached = categories[index][packed]
    scoped = patched and caller == (0x22C8AAD if mode == 0 else 0x22C8ACD)
    expected = ((cached or 0x240000) if scoped else
                (0x210000 if active else
                 (cached if patched and mode == 3 and cached else 0x240000)))
    visits = []
    outcome = False

    def ret(value):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xC1C1C1C1)
        u.reg_write(UC_X86_REG_EDX, 0xD2D2D2D2)
        u.reg_write(UC_X86_REG_EFLAGS, 0x246)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4)

    def hook(_u, address, _n, _data):
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
        elif address == 0x2965750:
            assert scoped and mode == 0 and cached
            assert u.reg_read(UC_X86_REG_ECX) == cached
            visits.append("renderer-reset-boundary")
            ret(0)
        elif address == 0x1A62AA0:
            assert scoped and not cached
            assert [rd(sp + 4 * index) for index in range(1, 5)] == [
                key, channel, 4, server]
            visits.append("aux-create-boundary")
            ret(0x240000)
        elif address == 0x22BEF00:
            visits.append("legacy-resolver")
        elif address == 0x2971960:
            assert not scoped and not active and rd(sp + 4) == key
            visits.append("scene-create-boundary")
            outcome = True
            u.emu_stop()
        elif address == (0x22C50C3 if mode == 0 else 0x22BE730):
            if mode == 0:
                assert u.reg_read(UC_X86_REG_ESI) == expected and sp == stack
            else:
                assert u.reg_read(UC_X86_REG_EDI) == expected
                assert rd(sp + 4) == expected and sp == stack - 8
            assert all(u.reg_read(reg) == value
                       for reg, value in saved.items())
            outcome = True
            u.emu_stop()

    def write(_u, _access, address, size, _value, _data):
        assert 0x100000 <= address and address + size <= stack, (
            hex(address), size)

    u.hook_add(UC_HOOK_CODE, hook)
    u.hook_add(UC_HOOK_MEM_WRITE, write)
    u.emu_start(0x22C5054 if mode == 0 else 0x22C42E6, 0, count=30000)
    assert outcome and bytes(u.mem_read(stack, 64)) == guard
    if scoped:
        assert "active" not in visits
        assert "legacy-resolver" not in visits
        assert "scene-create-boundary" not in visits
        assert visits.count("native-aux") == (2 if cached else 1)
        assert ("renderer-reset-boundary" in visits) == (
            mode == 0 and bool(cached))
        assert ("aux-create-boundary" in visits) == (not cached)
    else:
        assert ("scene-create-boundary" in visits) == (
            not active and not (patched and mode == 3 and cached))
        assert "aux-create-boundary" not in visits
        assert "renderer-reset-boundary" not in visits
    return dict(mode=mode, key=key, channel=channel, server=server,
                caller=hex(caller), active=active, patched=patched,
                mode8Scoped=scoped, calls=visits, heapWrites=0, passed=True)


def run_suite(root, image, manifests):
    aux_windows = portable_core.read_windows(image, aux_cache.RANGES)
    ranges = dict(aux_cache.RANGES)
    ranges.update(EXTRA_RANGES)
    windows = portable_core.read_windows(image, ranges)
    manifest_path = "manifests/mode8-route-candidate/windows-v1.json"
    detail_path = "manifests/detail-candidate/windows-v1.json"
    manifest = manifests[manifest_path]
    detail = manifests[detail_path]
    cases = []
    for mode in (0, 3):
        for key, channel, server in ((17, 11, 0), (4097, 10, 1),
                                    (65535, 255, 255)):
            packed = aux_cache.identity(key, channel, server)
            states = [
                {},
                {1: {packed: 0x220000}},
                {4: {packed: 0x230000}},
                {1: {packed: 0x220000}, 4: {packed: 0x230000}},
                {1: {packed: 0x220000}, 4: {packed: 0}},
                {1: {aux_cache.identity(key, channel ^ 1, server): 0x220000}},
            ]
            for caller in (0x22C8AAD, 0x22C8ACD, 0x22C8000):
                for active in (False, True):
                    for categories in states:
                        cases.append(run(
                            aux_windows, windows, manifest, detail, mode, key, channel,
                            server, caller, active, categories))
    baseline = [run(
        aux_windows, windows, manifest, detail, mode, 17, 11, 0, caller, active,
        {1: {aux_cache.identity(17, 11, 0): 0x220000}}, False)
        for mode in (0, 3)
        for caller in (0x22C8AAD, 0x22C8ACD)
        for active in (False, True)]
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        cases=cases,
        caseCount=len(cases),
        baseline=baseline,
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in windows.items()},
        sourceHashes=portable_core.proof_sources(
            root, ["aux_cache.py", "mode8.py", manifest_path, detail_path]),
        scope=("Actual local mode0/mode3 branch windows plus original remote "
               "branch and all eight native aux lookup functions. Existing "
               "detail helper combined. Active lookup, renderer reset, "
               "creation and mode3 stat initialization are boundaries. No "
               "descriptor/render/UI/lifetime claims."),
        processAccess=False,
        exeGenerated=False,
        deployed=False,
        guiAccepted=False,
    )
