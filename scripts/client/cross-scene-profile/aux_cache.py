"""Portable copy of the final v2 native aux-cache simulation."""
import hashlib
import struct

from unicorn import (Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE,
                     UC_HOOK_MEM_WRITE)
from unicorn.x86_const import *

import portable_core


RANGES = {
    0x1A5A5E0: 0x97,
    0x224D950: 0x18,
    0x224D8D0: 0x17,
    0x2429E20: 0x16C,
    0x224E5F0: 0x1B,
    0x3AC9DD0: 0x5D,
    0xC3FA60: 0xF6,
    0x3E76D18: 0xE,
}


def identity(key, channel, server):
    return ((server & 255) << 24) | ((channel & 255) << 16) | (key & 65535)


def bucket(key):
    quotient, remainder = divmod((key ^ 0xDEADBEEF) & 0x7FFFFFFF, 0x1F31D)
    value = remainder * 0x41A7 - quotient * 0xB14
    if value < 0:
        value += 0x7FFFFFFF
    return value & 7


def signed(value):
    return value if value < 0x80000000 else value - 0x100000000


def install(u, put, w, categories, windows):
    """Five native hash containers; vectors contain checked list iterators."""
    for address, code in windows.items():
        put(address, code)
    put(0x3E738B1, b"\xcc")
    for category in range(5):
        table = 0x5676898 + 0x38 * category
        arena = 0x400000 + 0x2000 * category
        proxy, sentinel, vector = arena, arena + 0x20, arena + 0x100
        put(table, bytes(0x38))
        put(arena, bytes(0x2000))
        w(table + 8, proxy)
        w(proxy, table + 8)
        w(table + 12, sentinel)
        w(table + 0x1C, vector)
        w(table + 0x20, vector + 8 * 24)
        w(table + 0x24, vector + 8 * 24)
        w(table + 0x2C, 7)
        w(table + 0x30, 8)
        groups = [[] for _ in range(8)]
        records = categories.get(category, {})
        for key, value in records.items():
            groups[bucket(key)].append((key, value))
        ordered = [item for group in groups for item in
                   sorted(group, key=lambda item: signed(item[0]))]
        nodes = {key: arena + 0x400 + index * 16
                 for index, (key, _) in enumerate(ordered)}
        chain = [sentinel] + [nodes[key] for key, _ in ordered] + [sentinel]
        w(sentinel, chain[1])
        w(sentinel + 4, chain[-2])
        w(table + 16, len(ordered))
        for index, (key, value) in enumerate(ordered, 1):
            node = nodes[key]
            w(node, chain[index + 1])
            w(node + 4, chain[index - 1])
            w(node + 8, key)
            w(node + 12, value)
        for index, group in enumerate(groups):
            group.sort(key=lambda item: signed(item[0]))
            first = nodes[group[0][0]] if group else sentinel
            last = nodes[group[-1][0]] if group else sentinel
            for offset, node in ((0, first), (12, last)):
                w(vector + index * 24 + offset, proxy)
                w(vector + index * 24 + offset + 8, node)


def run_case(windows, name, key, channel, server, categories, expected):
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

    install(u, put, w, categories, windows)
    put(0x100000, bytes(0x10000))
    put(0x300000, b"\xcc")
    sp = 0x108000
    for index, value in enumerate((0x300000, key, channel, server)):
        w(sp + 4 * index, value)
    guard = bytes(range(64))
    put(sp + 16, guard)
    registers = {
        UC_X86_REG_EBX: 0xB0B0B0B0,
        UC_X86_REG_ESI: 0x51515151,
        UC_X86_REG_EDI: 0xD1D1D1D1,
        UC_X86_REG_EBP: 0xBFBFBFBF,
    }
    for reg, value in registers.items():
        u.reg_write(reg, value)
    u.reg_write(UC_X86_REG_ESP, sp)
    visits = []
    writes = []
    returned = False

    def code(_u, address, _n, _data):
        nonlocal returned
        if address in windows:
            visits.append(hex(address))
        if address == 0x3E738B1:
            raise AssertionError("native checked iterator rejected fixture")
        if address == 0x300000:
            assert u.reg_read(UC_X86_REG_EAX) == expected, (
                name, hex(u.reg_read(UC_X86_REG_EAX)), hex(expected))
            assert u.reg_read(UC_X86_REG_ESP) == sp + 4
            assert all(u.reg_read(reg) == value
                       for reg, value in registers.items())
            assert bytes(u.mem_read(sp + 16, 64)) == guard
            assert rd(sp + 4) == key and rd(sp + 8) == channel
            assert rd(sp + 12) == identity(key, channel, server)
            returned = True
            u.emu_stop()

    def write(_u, _access, address, size, _value, _data):
        assert 0x100000 <= address and address + size <= sp + 16, (
            hex(address), size)
        writes.append(address)

    u.hook_add(UC_HOOK_CODE, code)
    u.hook_add(UC_HOOK_MEM_WRITE, write)
    u.emu_start(0x1A5A5E0, 0, count=20000)
    assert returned and visits.count("0x224d950") == 5
    return dict(name=name, identity=[key, channel, server],
                expectedPointer=hex(expected), nativeCalls=len(visits),
                nativeLookup=True, lookupStubbed=False, heapWrites=0,
                passed=True)


def run_suite(root, image, manifests):
    windows = portable_core.read_windows(image, RANGES)
    cases = []
    for key in (1, 4097, 65535):
        for channel in (0, 10, 255):
            for server in (0, 1, 255):
                packed = identity(key, channel, server)
                decoys = {
                    identity(key ^ 2, channel, server): 0x220000,
                    identity(key, channel ^ 1, server): 0x230000,
                    identity(key, channel, server ^ 1): 0x240000,
                }
                for category in range(5):
                    expected = 0x210000 + category * 0x1000
                    cases.append(run_case(
                        windows, "category-" + str(category), key, channel,
                        server,
                        {category: {**decoys, packed: expected}}, expected))
                cases.append(run_case(
                    windows, "wrong-identities", key, channel, server,
                    {1: decoys}, 0))
                cases.append(run_case(
                    windows, "last-category-wins", key, channel, server,
                    {i: {packed: 0x210000 + i * 0x1000} for i in range(5)},
                    0x214000))
                cases.append(run_case(
                    windows, "null-later-category-shadows", key, channel,
                    server, {1: {packed: 0x211000}, 4: {packed: 0}}, 0))
    for packed in (0x010A1001, 0xFF0A1001):
        colliders = [candidate for candidate in range(packed - 80, packed + 81)
                     if bucket(candidate) == bucket(packed)]
        records = {candidate: 0x250000 + index * 0x100
                   for index, candidate in enumerate(colliders)}
        cases.append(run_case(
            windows, "collision-present", packed & 65535,
            (packed >> 16) & 255, packed >> 24, {1: records},
            records[packed]))
        records.pop(packed)
        cases.append(run_case(
            windows, "collision-absent", packed & 65535,
            (packed >> 16) & 255, packed >> 24, {1: records}, 0))
    cases.append(run_case(windows, "all-empty", 17, 10, 1, {}, 0))
    return dict(
        passed=True,
        exeSha256=portable_core.sha256_hex(image),
        cases=cases,
        caseCount=len(cases),
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in windows.items()},
        sourceHashes=portable_core.proof_sources(root, ["aux_cache.py"]),
        fixtures=("Constructed valid five hash/list containers. All eight "
                  "native functions including CRT signed div execute without "
                  "stubs. No allocation/removal/lifetime or live GUI proof."),
        processAccess=False,
        exeGenerated=False,
        deployed=False,
        guiAccepted=False,
    )
