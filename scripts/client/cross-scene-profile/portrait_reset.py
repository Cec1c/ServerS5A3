"""Portable copy of the frozen native portrait-reset verifier."""
import hashlib
import itertools
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import portable_core


WINDOW_PAIR = (0x2965750, 0x29658CD - 0x2965750)


def run(code, present, descriptor, ui, self_ui):
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    pages = set()
    events = []
    actor, model, desc, stat = 0x200000, 0x202000, 0x204000, 0x205000
    registry, row, vtable, dtor, stop = (
        0x206000, 0x207000, 0x208000, 0x301000, 0x302000)

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

    put(0x2965750, code)
    put(0x100000, bytes(0x10000))
    put(actor, bytes(0x9000))
    w(actor + 0x4F4, model if present else 0)
    w(actor + 0x4F8, stat)
    w(actor + 0x4FC, desc if descriptor else 0)
    w(model, vtable)
    w(vtable, dtor)
    w(row + 0x30, model)
    w(0x57B6FB4, stat if self_ui else 0)
    w(0x57B6FB0, 0)
    w(0x57B6FB8, 0x209000)
    w(0x566B114, registry)
    hooks = (0x2047360, 0x2046E80, 0x2046F50, 0x2884460, 0x1338D30,
             0x1338CA0, 0xB27940, 0x2879C90, 0x12FB5E0, dtor, stop)
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
        if address == 0x2047360:
            assert rd(sp + 4) in (model, stat)
            ret(rd(sp + 4), 4)
        elif address == 0x2046E80:
            ret(registry if (ui and this == model) or
                (self_ui and this == stat) else 0)
        elif address == 0x2046F50:
            ret(registry)
        elif address == 0x2884460:
            assert this == registry and rd(sp + 8) == model and rd(sp + 12) == 0
            events.append("unlink-ui")
            ret(0, 12)
        elif address == 0x1338D30:
            assert rd(sp + 4) == model
            ret(3, 4)
        elif address == 0x1338CA0:
            assert rd(sp + 4) == 3
            ret(row, 4)
        elif address == 0xB27940:
            assert this == row and rd(sp + 4) == 0x209000 and rd(sp + 8) == 1
            events.append("detach-visible-row")
            ret(0, 8)
        elif address == 0x2879C90:
            assert this == registry and rd(sp + 4) == model
            ret(1, 4)
        elif address == 0x12FB5E0:
            assert this == desc and rd(sp + 4) == model
            events.append("unlink-description")
            ret(0, 4)
        elif address == dtor:
            assert this == model and rd(sp + 4) == 1
            events.append("destroy-renderer")
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
        u.emu_start(0x2965750, 0, count=500)
        assert u.reg_read(UC_X86_REG_EIP) == stop
        assert u.reg_read(UC_X86_REG_ESP) == stack + 4
        assert u.reg_read(UC_X86_REG_EBP) == 0x109000
        assert u.reg_read(UC_X86_REG_ESI) == 0xABCDEF01
        assert u.reg_read(UC_X86_REG_EBX) == 0x12345678
        assert u.reg_read(UC_X86_REG_EDI) == 0x23456789
        assert bytes(u.mem_read(stack + 4, 64)) == guard
        assert rd(actor + 0x4F4) == 0 and rd(actor + 0x4F8) == stat
        assert rd(actor + 0x4FC) == (desc if descriptor else 0)
        if repeat:
            assert len(events) == before
    expected = []
    if present:
        if ui:
            expected += ["unlink-ui", "detach-visible-row"]
        if self_ui:
            expected += ["unlink-ui"]
        if descriptor:
            expected += ["unlink-description"]
        expected += ["destroy-renderer"]
    assert events == expected, (events, expected)
    return dict(rendererPresent=present, description=descriptor,
                rendererUI=ui, selfUI=self_ui, events=events,
                secondResetNoOp=True, statPreserved=True, passed=True)


def run_suite(root, image, manifests):
    code = portable_core.read_window_pairs(image, [WINDOW_PAIR])[0x2965750]
    cases = [run(code, *case)
             for case in itertools.product((False, True), repeat=4)]
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        windowSha256=hashlib.sha256(code).hexdigest(),
        cases=cases,
        caseCount=len(cases),
        sourceHashes=portable_core.proof_sources(
            root, ["portrait_reset.py"]),
        scope=("Actual 2965750 reset instructions execute twice per case. "
               "Existing UI/description detach callbacks and virtual "
               "destructor are fixtures. Proves ordered detach, one "
               "destructor call, cleared renderer pointer, preserved stat "
               "pointer and reset idempotence."),
        processAccess=False,
        deployed=False,
        guiAccepted=False,
    )
