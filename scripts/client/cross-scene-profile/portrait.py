"""Portable copy of the reviewed portrait candidate simulation."""
from itertools import product
import hashlib
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import aux_cache
import portable_core


EXTRA_RANGES = {0x2513681: 0xC3, 0x29674E0: 0xC3, 0x2965DAA: 0x26}


def environment(windows, manifest, patched=True):
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
    for patch in manifest["patches"]:
        address = int(patch["site"], 16)
        assert bytes(u.mem_read(address, 6)).hex() == patch["expected"]
        if patched:
            put(address, bytes.fromhex(patch["replacement"]))
            put(int(patch["candidateAddress"], 16),
                bytes.fromhex(patch["code"]))
    put(0x100000, bytes(0x10000))
    put(0x200000, bytes(0x10000))
    for address in (0x296D730, 0x2964340, 0x2971210, 0x2965DD0,
                    0x2513744, 0x2965BD0, 0x301000, 0x2966C60, 0x3E738B1):
        put(address, b"\xcc")

    def ret(value=0, pop=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xC1C1C1C1)
        u.reg_write(UC_X86_REG_EDX, 0xD2D2D2D2)
        u.reg_write(UC_X86_REG_EFLAGS, 0x246)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    return u, put, w, rd, ret


def draw_case(windows, aux_windows, manifest, key, channel, server, model,
              aux_present, patched=True):
    u, put, w, rd, ret = environment(windows, manifest, patched)
    actor = 0x202000
    widget = 0x200000
    renderer = 0x205000
    aux_cache.install(
        u, put, w,
        {1: {aux_cache.identity(key, channel, server): actor}}
        if aux_present else {},
        aux_windows)
    frame = 0x108800
    stack = 0x108000
    put(widget + 0x3BC, struct.pack("<HBB", key, channel, server))
    w(widget + 0x3D4, 201)
    put(widget + 0x3D0, b"\0")
    w(actor, 0x203000)
    w(0x203004, 0x29674E0)
    w(actor + 0x4F4, renderer if model else 0)
    w(actor + 0x34, 7)
    w(renderer, 0x204000)
    w(0x204370, 0x301000)
    w(0x581A298, 0x206000)
    coords = (101, 202, 303, 404)
    for offset, value in zip((-0x1C, -0x18, -0x24, -0x20), coords):
        w(frame + offset, value)
    guard = bytes(range(64))
    put(stack, guard)
    saved = {
        UC_X86_REG_EBP: frame,
        UC_X86_REG_ESI: widget,
        UC_X86_REG_EBX: 0xB0B0B0B0,
    }
    for reg, value in saved.items():
        u.reg_write(reg, value)
    u.reg_write(UC_X86_REG_EDI, 0xD1D1D1D1)
    u.reg_write(UC_X86_REG_ESP, stack)
    events = []
    done = False

    def hook(_u, address, _size, _data):
        nonlocal done
        sp = u.reg_read(UC_X86_REG_ESP)
        if address == 0x3E738B1:
            raise AssertionError("native cache iterator failure")
        if address == 0x296D730:
            assert rd(sp + 4) == key
            events.append("active-miss")
            ret()
        elif address == 0x1A5A5E0:
            events.append("native-aux")
        elif address == 0x29674E0:
            assert u.reg_read(UC_X86_REG_ECX) == actor
            assert [rd(sp + index * 4) for index in range(1, 8)] == [
                *coords, 1, 0, 201]
            events.append("actor-portrait")
        elif address == 0x2965BD0:
            assert u.reg_read(UC_X86_REG_ECX) == actor and rd(sp + 4) == 0
            events.append("model-construction-boundary")
            w(actor + 0x4F4, renderer)
            ret(renderer, 4)
        elif address == 0x301000:
            assert u.reg_read(UC_X86_REG_ECX) == renderer
            assert [rd(sp + index * 4) for index in range(1, 7)] == [
                *coords, 0xC9FFFFFF, 0]
            events.append("renderer-draw-boundary")
            ret(pop=24)
        elif address == 0x2966C60:
            assert not patched
            assert [rd(sp + index * 4) for index in range(1, 8)] == [
                *coords, 7, 0, 201]
            events.append("job-draw-boundary")
            ret()
        elif address == 0x2513744:
            assert sp == stack and bytes(u.mem_read(stack, 64)) == guard
            assert all(u.reg_read(reg) == value
                       for reg, value in saved.items())
            done = True
            u.emu_stop()

    put(0x3C1BEA0, bytes.fromhex("8d4130c3"))
    u.hook_add(UC_HOOK_CODE, hook)
    u.emu_start(0x25136EB, 0, count=25000)
    assert done
    expected = ["active-miss", "native-aux"]
    if aux_present:
        expected += (["actor-portrait"] +
                     ([] if model else ["model-construction-boundary"]) +
                     ["renderer-draw-boundary"] if patched
                     else ["job-draw-boundary"])
    assert events == expected, (events, expected)
    return dict(identity=[key, channel, server], model=model, aux=aux_present,
                patched=patched, events=events, passed=True)


def guard_case(windows, manifest, portrait, active, self_actor, remote,
               patched=True):
    u, put, w, rd, ret = environment(windows, manifest, patched)
    frame = 0x108800
    stack = 0x108000
    actor = 0x202000
    renderer = 0x205000
    put(actor + 0x20, struct.pack("<H", 17))
    w(actor + 0x4F4, renderer)
    w(0x56A3758, 0x206000)
    w(frame + 4, 0x2967509 if portrait else 0x22C3C84)
    w(frame + 8, 17 if self_actor else 29)
    guard = bytes(range(64))
    put(stack, guard)
    saved = {
        UC_X86_REG_EBP: frame,
        UC_X86_REG_ESI: actor,
        UC_X86_REG_EBX: 0xB0B0B0B0,
        UC_X86_REG_EDI: 0xD1D1D1D1,
    }
    for reg, value in saved.items():
        u.reg_write(reg, value)
    u.reg_write(UC_X86_REG_ESP, stack)
    registered = False
    lookups = 0
    done = False

    def hook(_u, address, _n, _data):
        nonlocal registered, lookups, done
        sp = u.reg_read(UC_X86_REG_ESP)
        if address == 0x2964340:
            assert u.reg_read(UC_X86_REG_ECX) == actor
            ret(int(remote))
        elif address == 0x296D730:
            assert rd(sp + 4) == 17
            lookups += 1
            ret({"same": actor, "other": 0x203000, "none": 0}[active])
        elif address == 0x2971210:
            assert u.reg_read(UC_X86_REG_ECX) == 0x206000 and rd(sp + 4) == renderer
            registered = True
            ret(pop=4)
        elif address == 0x2965DD0:
            assert sp == stack and bytes(u.mem_read(stack, 64)) == guard
            assert all(u.reg_read(reg) == value
                       for reg, value in saved.items())
            done = True
            u.emu_stop()

    u.hook_add(UC_HOOK_CODE, hook)
    u.emu_start(0x2965DAA, 0, count=200)
    assert done
    eligible = not self_actor and not remote
    assert registered == (eligible and (not patched or not portrait or active == "same"))
    assert lookups == int(patched and portrait and eligible)
    return dict(portraitCaller=portrait, activePointer=active, self=self_actor,
                remote=remote, patched=patched, sceneRegistered=registered,
                activeLookups=lookups, passed=True)


def run_suite(root, image, manifests):
    aux_windows = portable_core.read_windows(image, aux_cache.RANGES)
    ranges = dict(aux_cache.RANGES)
    ranges.update(EXTRA_RANGES)
    windows = portable_core.read_windows(image, ranges)
    manifest_path = "manifests/portrait-candidate/windows-v1.json"
    manifest = manifests[manifest_path]
    draws = [draw_case(windows, aux_windows, manifest, *case)
             for case in product((1, 65535), (10, 255), (0, 1, 255),
                                 (False, True), (False, True))]
    guards = [guard_case(windows, manifest, *case)
              for case in product((False, True),
                                  ("same", "other", "none"),
                                  (False, True), (False, True))]
    baseline = [draw_case(windows, aux_windows, manifest, 17, 10, 1, model,
                          present, False)
                for model, present in product((False, True), repeat=2)]
    baseline += [guard_case(windows, manifest, *case, False)
                 for case in product((False, True),
                                     ("same", "other", "none"),
                                     (False, True), (False, True))]
    return dict(
        passed=True,
        baseSha256=portable_core.sha256_hex(image),
        drawCases=draws,
        guardCases=guards,
        baseline=baseline,
        windows={hex(address): hashlib.sha256(data).hexdigest()
                 for address, data in windows.items()},
        sourceHashes=portable_core.proof_sources(
            root, ["aux_cache.py", "portrait.py", manifest_path]),
        scope=("Exact aux MemberFace dispatch, actor virtual portrait "
               "function and lazy-builder registration tail. Native aux "
               "lookup executes. Scene lookup, context predicate, graphics "
               "allocation and draw are explicit fixtures. No full lifetime "
               "or GUI proof."),
        processAccess=False,
        exeGenerated=False,
        deployed=False,
        guiAccepted=False,
    )
