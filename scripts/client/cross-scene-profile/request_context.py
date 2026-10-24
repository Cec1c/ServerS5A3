"""Portable copy of the native actor-context and menu-branch verifier."""
import hashlib
import itertools
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import portable_core


WINDOW_PAIRS = ((0x2964340, 0x220), (0x353B80B, 0xF))


def run(windows, channel, server, actor_channel, actor_server):
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
    locks = (0x37E3940, 0x37E3970)
    getters = (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0)
    ends = (0x353BA38, 0x353B81A)
    for address in locks + getters + ends:
        put(address, b"\xcc")
    put(0, bytes(4096))
    w(0x55B3BDC, 0x12345678)
    put(0x100000, bytes(0x10000))
    put(0x200000, bytes(0x1000))
    put(0x200604, bytes((actor_channel, actor_server)))
    stack = 0x108000
    guard = bytes(range(64))
    put(stack, guard)
    u.reg_write(UC_X86_REG_ESP, stack)
    u.reg_write(UC_X86_REG_EBP, 0x109000)
    u.reg_write(UC_X86_REG_ESI, 0x200000)
    u.reg_write(UC_X86_REG_EDI, 0xABCDEF01)
    u.reg_write(UC_X86_REG_EBX, 0x23456789)
    seen = []
    result = None

    def ret(pop=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        u.reg_write(UC_X86_REG_EAX, 0xCACACACA)
        u.reg_write(UC_X86_REG_ECX, 0xCCCCCCCC)
        u.reg_write(UC_X86_REG_EDX, 0xDDDDDDDD)

    def hook(_u, address, _n, _data):
        nonlocal result
        if address in locks:
            ret()
        elif address in getters:
            sp = u.reg_read(UC_X86_REG_ESP)
            obj = u.reg_read(UC_X86_REG_ECX)
            assert obj in (0x56A1E80, 0x56A1E74)
            assert rd(sp + 4) == obj + 4
            w(rd(sp + 8), channel if obj == 0x56A1E80 else server)
            seen.append("channel" if obj == 0x56A1E80 else "server")
            ret(8)
        elif address in ends:
            result = ("local-C2S8-path" if address == ends[0]
                      else "remote-context-path")
            assert u.reg_read(UC_X86_REG_ESP) == stack
            assert u.reg_read(UC_X86_REG_EBP) == 0x109000
            assert u.reg_read(UC_X86_REG_ESI) == 0x200000
            assert u.reg_read(UC_X86_REG_EDI) == 0xABCDEF01
            assert u.reg_read(UC_X86_REG_EBX) == 0x23456789
            assert bytes(u.mem_read(stack, 64)) == guard and rd(0) == 0
            u.emu_stop()

    u.hook_add(UC_HOOK_CODE, hook)
    u.emu_start(0x353B80B, 0, count=400)
    local = actor_channel == 0 or (
        actor_channel == channel and actor_server == server)
    assert result == ("local-C2S8-path" if local else "remote-context-path")
    return dict(connection=[channel, server],
                actor=[actor_channel, actor_server], result=result,
                protectedReads=seen, stackAndSavedRegisters=True,
                passed=True)


def run_suite(root, image, manifests):
    windows = portable_core.read_window_pairs(image, WINDOW_PAIRS)
    cases = []
    for channel, server in itertools.product((1, 19, 23, 255),
                                              (0, 1, 255)):
        for actor_channel, actor_server in (
                (channel, server), (channel, server ^ 1),
                (channel ^ 1, server), (0, server), (0, server ^ 1)):
            cases.append(run(windows, channel, server,
                             actor_channel, actor_server))
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        cases=cases,
        caseCount=len(cases),
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in windows.items()},
        sourceHashes=portable_core.proof_sources(
            root, ["request_context.py"]),
        scope=("Exact 2964340 actor context predicate and 353B80B menu "
               "branch. Matching real connection namespace selects the "
               "existing local request path even when the actor is "
               "auxiliary. Protected global getters/locks are fixtures; "
               "stops before request serialization."),
        processAccess=False,
        guiAccepted=False,
        deployed=False,
    )
