"""Portable copy of the full aux actor-release verifier."""
import hashlib
import itertools
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import portable_core


WINDOW_PAIRS = ((0x2968150, 0x288), (0x2965750, 0x17D))


def run(windows, renderer, stat_present, descriptor, render_ui, stat_ui):
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    pages = set()
    events = []
    (actor, model, desc, stat, registry, row, tree) = (
        0x200000, 0x202000, 0x204000, 0x205000, 0x206000, 0x207000,
        0x208000)
    main_stat, main_model, mode = 0x209000, 0x20A000, 0x20B000
    render_vt, stat_vt, render_dtor, stat_dtor, stop = (
        0x20C000, 0x20D000, 0x301000, 0x301010, 0x302000)

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
    put(0x100000, bytes(0x10000))
    put(actor, bytes(0xE000))
    w(actor + 0x10, tree)
    w(tree + 4, tree)
    w(actor + 0x4F4, model if renderer else 0)
    w(actor + 0x4F8, stat if stat_present else 0)
    w(actor + 0x4FC, desc if descriptor else 0)
    w(model, render_vt)
    w(render_vt, render_dtor)
    w(stat, stat_vt)
    w(stat_vt, stat_dtor)
    w(row + 0x30, model)
    w(0x57B6FB4, 0)
    w(0x57B6FB0, main_stat)
    w(0x57B6FB8, main_model)
    w(0x566B114, registry)
    hooks = (0x2963940, 0x29667C0, 0x12FB7C0, 0x3E70882, 0x2047360,
             0x2046E80, 0x2046F50, 0x2046CC0, 0x2884460, 0x1338D30,
             0x1338CA0, 0xB27940, 0x2879C90, 0x1FA2790, 0x2524FB0,
             0x3586600, render_dtor, stat_dtor, stop)
    for address in hooks:
        put(address, b"\xcc")
    stack = 0x108000
    w(stack, stop)
    guard = bytes(range(64))
    put(stack + 4, guard)

    def ret(value=0, pop=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xCCCCCCCC)
        u.reg_write(UC_X86_REG_EDX, 0xDDDDDDDD)

    def hook(_u, address, _n, _data):
        sp = u.reg_read(UC_X86_REG_ESP)
        this = u.reg_read(UC_X86_REG_ECX)
        if address == 0x2963940:
            assert this == actor
            ret()
        elif address == 0x29667C0:
            assert this == actor + 8 and rd(sp + 4) == tree
            ret(0, 4)
        elif address == 0x12FB7C0:
            assert this == desc
            events.append("destroy-description")
            ret()
        elif address == 0x3E70882:
            assert rd(sp + 4) == desc
            events.append("free-description")
            ret()
        elif address == 0x2047360:
            assert rd(sp + 4) in (model, stat, main_stat, mode)
            ret(rd(sp + 4), 4)
        elif address == 0x2046E80:
            ret(registry if (render_ui and this == model) or
                (stat_ui and this == stat) else 0)
        elif address == 0x2046F50:
            ret(registry)
        elif address == 0x2046CC0:
            assert this == mode
            ret()
        elif address == 0x2884460:
            assert this == registry and rd(sp + 8) in (model, stat)
            assert rd(sp + 12) == 0
            events.append("unlink-render-ui" if rd(sp + 8) == model
                          else "unlink-stat-ui")
            ret(0, 12)
        elif address == 0x1338D30:
            assert rd(sp + 4) in (model, stat)
            ret(3 if rd(sp + 4) == model else 0xFFFFFFFF, 4)
        elif address == 0x1338CA0:
            assert rd(sp + 4) == 3
            ret(row, 4)
        elif address == 0xB27940:
            assert this == row and rd(sp + 4) == main_model and rd(sp + 8) == 1
            events.append("detach-render-row")
            ret(0, 8)
        elif address == 0x2879C90:
            assert this == registry and rd(sp + 4) == model
            ret(1, 4)
        elif address == 0x1FA2790:
            ret(mode)
        elif address == 0x2524FB0:
            assert this == mode
            ret()
        elif address == 0x3586600:
            assert this == actor + 0x5B8
            ret()
        elif address in (render_dtor, stat_dtor):
            assert this == (model if address == render_dtor else stat)
            assert rd(sp + 4) == 1
            events.append("destroy-renderer" if address == render_dtor
                          else "destroy-stat")
            ret(0, 4)
        elif address == stop:
            u.emu_stop()

    u.hook_add(UC_HOOK_CODE, hook)
    for repeat in range(2):
        before = len(events)
        u.reg_write(UC_X86_REG_ESP, stack)
        u.reg_write(UC_X86_REG_EBP, 0x109000)
        u.reg_write(UC_X86_REG_ECX, actor)
        u.reg_write(UC_X86_REG_ESI, 0xABCDEF01)
        u.reg_write(UC_X86_REG_EBX, 0x12345678)
        u.reg_write(UC_X86_REG_EDI, 0x23456789)
        u.emu_start(0x2968150, 0, count=1500)
        assert u.reg_read(UC_X86_REG_EIP) == stop
        assert u.reg_read(UC_X86_REG_ESP) == stack + 4
        assert u.reg_read(UC_X86_REG_EBP) == 0x109000
        assert u.reg_read(UC_X86_REG_ESI) == 0xABCDEF01
        assert u.reg_read(UC_X86_REG_EBX) == 0x12345678
        assert u.reg_read(UC_X86_REG_EDI) == 0x23456789
        assert bytes(u.mem_read(stack + 4, 64)) == guard
        assert all(rd(actor + offset) == 0
                   for offset in (0x4F4, 0x4F8, 0x4FC))
        assert rd(0x57B6FB0) == main_stat and rd(0x57B6FB8) == main_model
        assert all(rd(tree + offset) == tree for offset in (0, 4, 8))
        assert rd(actor + 0x14) == 0
        if repeat:
            assert len(events) == before
    wanted = []
    if descriptor:
        wanted += ["destroy-description", "free-description"]
    if renderer:
        if render_ui:
            wanted += ["unlink-render-ui", "detach-render-row"]
        wanted += ["destroy-renderer"]
    if stat_present:
        if stat_ui:
            wanted += ["unlink-stat-ui"]
        wanted += ["destroy-stat"]
    assert events == wanted, (events, wanted)
    return dict(renderer=renderer, stat=stat_present,
                description=descriptor, renderUI=render_ui, statUI=stat_ui,
                events=events, repeatedCleanupNoDoubleFree=True,
                mainActorUnchanged=True, passed=True)


def run_suite(root, image, manifests):
    windows = portable_core.read_window_pairs(image, WINDOW_PAIRS)
    cases = [run(windows, *case)
             for case in itertools.product((False, True), repeat=5)]
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        windowSha256={hex(address): hashlib.sha256(data).hexdigest()
                      for address, data in windows.items()},
        cases=cases,
        caseCount=len(cases),
        sourceHashes=portable_core.proof_sources(
            root, ["aux_release.py"]),
        scope=("Actual 2968150 full actor cleanup and its actual 2965750 "
               "portrait reset execute twice for non-main aux objects. "
               "Container metadata, description cleanup/free, UI managers "
               "and polymorphic destructors are fixtures. No aux-map erase, "
               "allocator internals or GUI proof."),
        processAccess=False,
        deployed=False,
        guiAccepted=False,
    )
