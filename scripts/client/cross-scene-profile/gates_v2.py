"""Portable copy of the final v2 lookup-gate simulation."""
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import struct


WINDOW_PAIRS = (
    (0x1DF5D79, 0x2B),
    (0x35454C4, 0x3B),
    (0x353ADE5, 0x3B),
    (0x2513681, 0xC3),
    (0x3C1BEA0, 4),
)


def run(windows, kind, active, aux, patch=None, key=17, channel=10, server=1,
        cache_key=None, noise=False):
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
    if patch is not None:
        assert patch["name"] == kind
        site = int(patch["site"], 16)
        expected = bytes.fromhex(patch["expected"])
        assert bytes(u.mem_read(site, len(expected))) == expected
        put(site, bytes.fromhex(patch["replacement"]))
        put(int(patch["candidateAddress"], 16),
            bytes.fromhex(patch["code"]))
    for address in (0x296D730, 0x1A5A5E0, 0x261E440, 0x3549600,
                    0x2966C60, 0x301000, 0x3549122, 0x353D411, 0x2513744):
        put(address, b"\xcc")
    put(0x100000, bytes(0x10000))
    put(0x200000, bytes(0x6000))
    w(0x56A3798, 0x205000)
    frame = 0x108800
    widget = 0x200000
    local_actor = 0x202000
    aux_actor = 0x203000
    context_object = 0x201000
    key_object = 0x205000
    stack = frame - 0x800
    guard = bytes(range(64))
    put(stack, guard)
    w(widget, key)
    put(widget + 0x3BC, struct.pack("<HBB", key, channel, server))
    w(key_object, key)
    put(context_object + 0x3E, bytes([channel, server]))
    put(widget + 0x3E, bytes([channel ^ 1, server ^ 1]))
    put(key_object + 0x3E, bytes([channel ^ 2, server ^ 2]))
    w(local_actor, 0x204000)
    w(0x204004, 0x301000)
    w(aux_actor + 0x34, 7)
    w(frame - 0xB4, key)
    w(frame - 0x238, key)
    server_word = server | (0xA500CD00 if noise else 0)
    channel_word = channel | (0x5A00EF00 if noise else 0)
    w(frame - 0xCC, server_word)
    w(frame - 0xD0, channel_word)
    w(frame - 0x24C, server_word)
    w(frame - 0x248, channel_word)
    u.reg_write(UC_X86_REG_EBP, frame)
    u.reg_write(UC_X86_REG_ESP, stack)
    u.reg_write(UC_X86_REG_EDI, key_object)
    u.reg_write(UC_X86_REG_ESI, widget)
    u.reg_write(UC_X86_REG_EBX, context_object)
    seen = []
    outcome = None
    helper_state = None
    helper_return_checked = False
    cache_key = (key, channel, server) if cache_key is None else cache_key
    aux_hit = aux and cache_key == (key, channel, server)
    expected_pointer = local_actor if active else (
        aux_actor if aux_hit and (patch is not None or kind == "face") else 0)

    def ret(value=0, pop=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        u.reg_write(UC_X86_REG_EAX, value)
        u.reg_write(UC_X86_REG_ECX, 0xC1C1C1C1)
        u.reg_write(UC_X86_REG_EDX, 0xD2D2D2D2)
        u.reg_write(UC_X86_REG_EFLAGS, 0x246)
        u.reg_write(UC_X86_REG_EIP, rd(sp))
        u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    gates = {
        "open": (0x1DF5D79, 0x3549600, 0x1DF5DA2),
        "build": (0x35454C4, 0x35454FF, 0x3549122),
        "click": (0x353ADE5, 0x353AE20, 0x353D411),
    }
    for _, yes, no in gates.values():
        if yes not in (0x3549600,):
            put(yes, b"\xcc")
        put(no, b"\xcc")

    def hook(_u, address, _n, _data):
        nonlocal outcome, helper_state, helper_return_checked
        sp = u.reg_read(UC_X86_REG_ESP)
        if patch is not None and address == int(patch["candidateAddress"], 16):
            helper_state = [u.reg_read(reg) for reg in
                            (UC_X86_REG_EBX, UC_X86_REG_ESI,
                             UC_X86_REG_EDI, UC_X86_REG_EBP)] + [sp + 4]
        elif patch is not None and address == int(patch["site"], 16) + 5:
            assert helper_state == [u.reg_read(reg) for reg in
                                    (UC_X86_REG_EBX, UC_X86_REG_ESI,
                                     UC_X86_REG_EDI, UC_X86_REG_EBP)] + [sp]
            assert u.reg_read(UC_X86_REG_EAX) == expected_pointer
            assert rd(sp) == key and bytes(u.mem_read(stack, 64)) == guard
            helper_return_checked = True
        elif address == 0x296D730:
            assert rd(sp + 4) == key
            seen.append("active-lookup")
            ret(local_actor if active else 0)
        elif address == 0x1A5A5E0:
            assert [rd(sp + 4) & 0xFFFF, rd(sp + 8) & 0xFF,
                    rd(sp + 12) & 0xFF] == [key, channel, server]
            seen.append("aux-lookup")
            ret(aux_actor if aux_hit else 0)
        elif kind in gates and address in gates[kind][1:]:
            outcome = "continue" if address == gates[kind][1] else "return-no-action"
            assert bytes(u.mem_read(stack, 64)) == guard
            if kind == "open" and outcome == "continue":
                assert sp == stack - 20
                assert [rd(sp + x) for x in (4, 8, 12, 16)] == [
                    expected_pointer, 0, 0, 1]
            else:
                assert sp == stack
                if kind in ("build", "click"):
                    target = widget if kind == "build" else key_object
                    assert rd(target + 0x4C) == expected_pointer
            u.emu_stop()
        elif address == 0x261E440:
            ret(0x205000)
        elif kind == "face" and address in (0x301000, 0x2966C60, 0x2513744):
            outcome = {0x301000: "actor-draw", 0x2966C60: "job-icon-draw",
                       0x2513744: "no-draw"}[address]
            if address == 0x2966C60:
                assert rd(sp + 20) == 7
            u.emu_stop()

    u.hook_add(UC_HOOK_CODE, hook)
    u.emu_start(0x25136EB if kind == "face" else gates[kind][0], 0,
                count=1000)
    assert outcome is not None
    if kind == "face":
        expected = "actor-draw" if active else (
            "job-icon-draw" if aux_hit else "no-draw")
    else:
        expected = "continue" if active or patch is not None and aux_hit else (
            "return-no-action")
        assert seen == (["active-lookup", "aux-lookup"]
                        if patch is not None and not active
                        else ["active-lookup"])
        if patch is not None:
            assert helper_return_checked
    assert outcome == expected, (kind, active, aux, outcome, expected)
    return dict(gate=kind, activeActor=active, auxActor=aux,
                identity=[key, channel, server], cacheIdentity=cache_key,
                lookups=seen, outcome=outcome,
                helperABI=helper_return_checked if patch is not None else None,
                expectedPointer=hex(expected_pointer), stackGuard=True,
                distinctSavedRegisters=True, volatileClobber=True,
                contextHighBits=noise, passed=True)
