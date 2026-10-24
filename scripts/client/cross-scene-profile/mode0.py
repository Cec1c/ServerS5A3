"""Portable copy of the mode0 appearance-data verifier."""
import hashlib
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import aux_cache
import portable_core


EXTRA_RANGES = {0x22BDE30: 0x160, 0x12FBAE7: 0x9A}


def run(windows, rows):
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

    for address, data in windows.items():
        put(address, data)
    for address in (0x37E82E0, 0x37E83C0, 0x3B507A0, 0x3B50770,
                    0x3B50590, 0x1FA2790, 0x251E2F0, 0x3E72CE4):
        put(address, b"\xcc")
    put(0x100000, bytes(0x10000))
    put(0x200000, bytes(0x3000))
    put(0x300000, b"\xcc")
    source = 0x200100
    destination = 0x201100
    sp = 0x108000
    w(0x55B3BDC, 0x12345678)
    wire = bytearray([len(rows)])
    for slot, item, flags in rows:
        wire += struct.pack("<BIIBIIB", slot, item, 0, flags, 0, 0, 0)
    cursor = 0
    done = False
    phase = "parse"
    calls = []

    def ret(value=0):
        stack = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xC1C1C1C1)
        u.reg_write(UC_X86_REG_EDX, 0xD2D2D2D2)
        u.reg_write(UC_X86_REG_EIP, rd(stack))
        u.reg_write(UC_X86_REG_ESP, stack + 4)

    def read(destination_address, size):
        nonlocal cursor
        assert cursor + size <= len(wire)
        put(destination_address, bytes(wire[cursor:cursor + size]))
        cursor += size

    def hook(_u, address, _n, _data):
        nonlocal cursor, done
        stack = u.reg_read(UC_X86_REG_ESP)
        if address == 0x300000 or (phase == "copy" and address == 0x12FBB81):
            done = True
            u.emu_stop()
        elif address == 0x37E82E0:
            read(rd(stack + 4), rd(stack + 8))
            ret()
        elif address == 0x37E83C0:
            read(rd(stack + 4), 4)
            ret()
        elif address == 0x3B50590:
            assert wire[cursor:cursor + 4] == bytes(4)
            cursor += 4
            w(u.reg_read(UC_X86_REG_ECX), 0)
            ret()
        elif address in (0x3B507A0, 0x3B50770):
            w(u.reg_read(UC_X86_REG_ECX), 0)
            ret()
        elif address == 0x251E2F0:
            assert rd(stack + 12) == 0x2D
            calls.append("resource-wrapper-boundary")
            ret()
        elif address == 0x1FA2790:
            ret(0x220000)
        elif address == 0x3E72CE4:
            ret()

    u.hook_add(UC_HOOK_CODE, hook)
    w(sp, 0x300000)
    w(sp + 4, source)
    u.reg_write(UC_X86_REG_ESP, sp)
    u.reg_write(UC_X86_REG_EBP, 0x108800)
    u.reg_write(UC_X86_REG_EBX, 0xB0B0B0B0)
    u.reg_write(UC_X86_REG_ESI, 0x51515151)
    u.reg_write(UC_X86_REG_EDI, 0xD1D1D1D1)
    u.emu_start(0x22BDE30, 0, count=20000)
    assert done and cursor == len(wire) and u.reg_read(UC_X86_REG_ESP) == sp + 4
    assert u.reg_read(UC_X86_REG_EBX) == 0xB0B0B0B0
    assert u.reg_read(UC_X86_REG_ESI) == 0x51515151
    assert u.reg_read(UC_X86_REG_EDI) == 0xD1D1D1D1
    expected = {slot: (item, flags) for slot, item, flags in rows}
    for slot in range(33):
        item, flags = expected.get(slot, (0, 0))
        assert rd(source + 0x20 + slot * 8) == item
        assert rd(source + 0x24 + slot * 8) == flags >> 1
        assert rd(source + 0x1AC + slot * 4) == flags & 1
    phase = "copy"
    done = False
    u.reg_write(UC_X86_REG_ESP, sp)
    u.reg_write(UC_X86_REG_EBP, 0x108800)
    u.reg_write(UC_X86_REG_EDI, source)
    u.reg_write(UC_X86_REG_ESI, destination)
    u.reg_write(UC_X86_REG_ECX, source)
    u.emu_start(0x12FBAE7, 0, count=10000)
    assert done
    assert bytes(u.mem_read(source + 0x20, 0x2A1)) == bytes(
        u.mem_read(destination + 0x20, 0x2A1))
    assert bytes(u.mem_read(destination, 0x20)) == bytes(0x20)
    assert bytes(u.mem_read(destination + 0x2C1, 32)) == bytes(32)
    return dict(rows=rows, wireBytes=len(wire), consumed=cursor,
                resourceWrapperCalls=len(calls), passed=True)


def run_suite(root, image, manifests):
    ranges = dict(aux_cache.RANGES)
    ranges.update(EXTRA_RANGES)
    windows = portable_core.read_windows(image, ranges)
    vectors = [
        [],
        [(index, 100000 + index, index % 4) for index in range(14)],
        [(9, 200009, 3)],
        [(0, 300000, 0), (13, 300013, 2), (32, 300032, 1)],
        [(7, 400007, 0), (7, 500007, 3)],
    ]
    cases = [run(windows, vector) for vector in vectors]
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        cases=cases,
        caseCount=len(cases),
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in windows.items()},
        sourceHashes=portable_core.proof_sources(root, ["mode0.py"]),
        scope=("Native 22BDE30 slot/ID/flags decode and 12FBAE7..12FBB81 "
               "descriptor-array copy. Packet-reader and resource-wrapper "
               "boundaries; no live item resolution, model rendering, "
               "complete mode0 execution or GUI acceptance."),
        processAccess=False,
        deployed=False,
        guiAccepted=False,
    )
