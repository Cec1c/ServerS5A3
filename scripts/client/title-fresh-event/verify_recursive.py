"""Bounded native test for the title recursive-marker candidate.

The positive route executes the actual UI-side producer CALL at 0x12ACB78,
the real producer prologue, the source=-1 branch, and the real recursive CALL
at 0x25FA48E before reaching the marker decision. UI/window payload setup and
the inner producer's pre-marker body are explicit fixtures. The marker getter,
writer, redirect, and continuation are real code; only the terminal append leaf
is an ABI stub.

All 17 legacy boundary cases are also retained. They continue to use their
established CALL/prologue fixture and do not claim to execute the full producer.
"""

import argparse
import json
import struct
import sys
from pathlib import Path

from unicorn import (
    Uc,
    UC_ARCH_X86,
    UC_HOOK_CODE,
    UC_HOOK_MEM_INVALID,
    UC_MEM_FETCH_UNMAPPED,
    UC_MEM_FETCH_PROT,
    UC_MEM_READ_UNMAPPED,
    UC_MEM_READ_PROT,
    UC_MEM_WRITE_UNMAPPED,
    UC_MEM_WRITE_PROT,
    UC_MODE_32,
    UcError,
)
from unicorn.x86_const import (
    UC_X86_REG_EAX,
    UC_X86_REG_EBX,
    UC_X86_REG_ECX,
    UC_X86_REG_EDX,
    UC_X86_REG_EFLAGS,
    UC_X86_REG_EIP,
    UC_X86_REG_EBP,
    UC_X86_REG_EDI,
    UC_X86_REG_ESI,
    UC_X86_REG_ESP,
)

import build_candidate
import recursive_extension as rec
from pe_support import Image, available_memory, sha256


PROLOGUE_STOP = 0x25F9A73
LEGACY_ENTRY = rec.OP19UI
BOUNDARY = rec.PRODUCER_BOUNDARY
CONTINUE = rec.CONTINUE
APPEND_LEAF = rec.APPEND_LEAF
AFTER_WRITER = 0x25FA99B
WRITER_GLOBAL = rec.WRITER_GLOBAL

STACK = 0x10000000
FRAME = 0x11000000
FAKE_WRITER = 0x13000000
FAKE_WINDOW = 0x14000000
FAKE_ITEM = 0x14001000
FAKE_SIZE = (0x2BCC40 + 0x1FFF) & ~0xFFF
STOP = 0x12000000

SENTINELS = {
    "ebx": (UC_X86_REG_EBX, 0x11112222),
    "ecx": (UC_X86_REG_ECX, 0x22223333),
    "edx": (UC_X86_REG_EDX, 0x33334444),
    "esi": (UC_X86_REG_ESI, 0x44445555),
    "edi": (UC_X86_REG_EDI, 0x55556666),
}

MEMORY_ACCESS_NAMES = {
    UC_MEM_READ_UNMAPPED: "read-unmapped",
    UC_MEM_WRITE_UNMAPPED: "write-unmapped",
    UC_MEM_FETCH_UNMAPPED: "fetch-unmapped",
    UC_MEM_READ_PROT: "read-protection",
    UC_MEM_WRITE_PROT: "write-protection",
    UC_MEM_FETCH_PROT: "fetch-protection",
}


class FixtureMemoryError(AssertionError):
    """Bounded native fixture failed with a structured memory diagnostic."""

# Legacy 17-case boundary set. The input raw image already contains the
# accepted V179 rules, so both the baseline image and the candidate image must
# produce the ``expected_v179`` marker. The pre-V179 red values are kept
# separately in V177_HISTORICAL_BEFORE and are historical evidence only.
ORIGINAL_CASES = (
    ("simple-2499E10", 0x2499E10, 0xA5A50000, 13, 1, 0, 2, "real", False),
    ("legacy-12A3479-parent-AEF1B", 0x12A3479, 0x12AEF1B, 13, 1, 2, 2, "real", False),
    ("legacy-12AB199-parent-AE884", 0x12AB199, 0x12AE884, 13, 1, 2, 2, "real", False),
    ("legacy-12ACB30-parent-AE59F", 0x12ACB30, 0x12AE59F, 13, 1, 2, 2, "real", False),
    ("legacy-12ACB7D-parent-AE59F", 0x12ACB7D, 0x12AE59F, 13, 1, 0, 2, "real", False),
    ("legacy-12AF91D-noParent", 0x12AF91D, 0xA5A50000, 13, 1, 2, 2, "real", False),
    ("claim-24A288E-positive", 0x24A288E, 0xA5A50000, 13, 1, 0, 2, "real", False),
    ("claim-24A288E-nullFrame", 0x24A288E, 0x0, 13, 1, 0, 2, "synthetic", True),
    ("claim-24A288E-wrongSlot12", 0x24A288E, 0xA5A50000, 12, 1, 0, 0, "real", False),
    ("claim-24A288E-wrongCount2", 0x24A288E, 0xA5A50000, 13, 2, 0, 0, "real", False),
    ("claim-24A288E-wrongArg4", 0x24A288E, 0xA5A50000, 13, 1, 2, 0, "real", False),
    ("uncovered-25FA396", 0x25FA396, 0xA5A50000, 13, 1, 0, 0, "real", False),
    ("uncovered-25FA493", 0x25FA493, 0xA5A50000, 13, 1, 0, 0, "real", False),
    ("uncovered-25FCC38", 0x25FCC38, 0xA5A50000, 13, 1, 0, 0, "real", False),
    ("uncovered-25FCCFC", 0x25FCCFC, 0xA5A50000, 13, 1, 0, 0, "real", False),
    ("unknown-1FDF58D", 0x1FDF58D, 0xA5A50000, 13, 1, 0, 0, "synthetic", False),
    ("claim-24A288E-slot26", 0x24A288E, 0xA5A50000, 26, 1, 0, 0, "real", False),
)

# Retained from the pre-V179 baseline for audit. These values are never used
# as the df496 V179 baseline assertion; exactly two are deliberately red.
V177_HISTORICAL_BEFORE = {
    "simple-2499E10": 2,
    "legacy-12A3479-parent-AEF1B": 2,
    "legacy-12AB199-parent-AE884": 2,
    "legacy-12ACB30-parent-AE59F": 2,
    "legacy-12ACB7D-parent-AE59F": 2,
    "legacy-12AF91D-noParent": 2,
    "claim-24A288E-positive": 0,
    "claim-24A288E-nullFrame": 0,
    "claim-24A288E-wrongSlot12": 0,
    "claim-24A288E-wrongCount2": 0,
    "claim-24A288E-wrongArg4": 0,
    "uncovered-25FA396": 0,
    "uncovered-25FA493": 0,
    "uncovered-25FCC38": 0,
    "uncovered-25FCCFC": 0,
    "unknown-1FDF58D": 0,
    "claim-24A288E-slot26": 0,
}

STRICT_INNER = dict(source=3, slot=13, count=2, arg4=3, arg5=1)
STRICT_OUTER = dict(source=-1, slot=13, count=1, arg4=0, arg5=0)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def read_u32(machine, address):
    return struct.unpack("<I", machine.mem_read(address, 4))[0]


def read_i32(machine, address):
    return struct.unpack("<i", machine.mem_read(address, 4))[0]


def put_u32(machine, address, value):
    machine.mem_write(address, struct.pack("<I", value & 0xFFFFFFFF))


def guarded_emu_start(machine, start, stop, *, case_label, phase, meta=None,
                      state=None, count=50000, timeout=5_000_000):
    """Run a bounded case and print exact context before propagating UcError."""
    capture = {"fault": None, "lastCode": []}

    def on_code(cpu, address, size, _):
        capture["lastCode"].append({"pc": hex(address), "size": size})
        if len(capture["lastCode"]) > 16:
            del capture["lastCode"][0]

    def on_unmapped(cpu, access, address, size, value, _):
        try:
            pc = cpu.reg_read(UC_X86_REG_EIP)
        except Exception:
            pc = None
        capture["fault"] = {
            "access": MEMORY_ACCESS_NAMES.get(access, f"access-{access}"),
            "pc": None if pc is None else hex(pc),
            "address": hex(address),
            "size": size,
            "value": value,
        }
        return False

    machine.hook_add(UC_HOOK_CODE, on_code)
    machine.hook_add(UC_HOOK_MEM_INVALID, on_unmapped)
    try:
        machine.emu_start(start, stop, count=count, timeout=timeout)
    except UcError as exc:
        registers = {}
        for name, register in (
            ("eax", UC_X86_REG_EAX),
            ("ebx", UC_X86_REG_EBX),
            ("ecx", UC_X86_REG_ECX),
            ("edx", UC_X86_REG_EDX),
            ("esi", UC_X86_REG_ESI),
            ("edi", UC_X86_REG_EDI),
            ("ebp", UC_X86_REG_EBP),
            ("esp", UC_X86_REG_ESP),
            ("eip", UC_X86_REG_EIP),
        ):
            registers[name] = hex(machine.reg_read(register))
        fault = capture["fault"] or {}
        diagnostic = {
            "schema": "title-recursive-native-failure.v1",
            "case": case_label,
            "phase": phase,
            "start": hex(start),
            "stop": hex(stop),
            "meta": {} if meta is None else meta,
            "errorType": type(exc).__name__,
            "error": str(exc),
            "fault": fault,
            "registers": registers,
            "lastCode": capture["lastCode"],
            "path": [] if state is None else list(state.get("path", [])),
        }
        encoded = json.dumps(diagnostic, ensure_ascii=False, sort_keys=True)
        print("TITLE_RECURSIVE_NATIVE_FAILURE " + encoded, file=sys.stderr)
        raise FixtureMemoryError(
            f"{case_label} {phase}: {type(exc).__name__}: {exc}; {encoded}"
        ) from exc
    return capture


def prepare_machine(image):
    machine = Uc(UC_ARCH_X86, UC_MODE_32)
    mapped = (image.size_of_image + 0xFFF) & ~0xFFF
    machine.mem_map(0x400000, mapped)
    for section in image.sections:
        if section["raw_size"]:
            machine.mem_write(
                section["va"],
                image.data[section["raw"]:section["raw"] + section["raw_size"]],
            )
    machine.mem_map(STACK, 0x10000)
    machine.mem_map(FRAME, 0x10000)
    machine.mem_map(FAKE_WRITER, FAKE_SIZE)
    machine.mem_map(FAKE_WINDOW, 0x1000)
    machine.mem_map(FAKE_ITEM, 0x1000)
    machine.mem_map(STOP, 0x1000)
    put_u32(machine, WRITER_GLOBAL, FAKE_WRITER)
    machine.mem_write(FAKE_WRITER + 8, b"\x01")
    put_u32(machine, FAKE_WRITER + 0x2BCC2C, 0)
    return machine


def set_sentinels(machine):
    machine.reg_write(UC_X86_REG_EFLAGS, 0x246)
    machine.reg_write(UC_X86_REG_EAX, 0x66778899)
    for _, (register, value) in SENTINELS.items():
        machine.reg_write(register, value)


def resume_return(machine, extra_stack=0):
    esp = machine.reg_read(UC_X86_REG_ESP)
    return_address = read_u32(machine, esp)
    machine.reg_write(UC_X86_REG_ESP, esp + 4 + extra_stack)
    machine.reg_write(UC_X86_REG_EIP, return_address)


def execute_marker(image, *, start, ebp, esp, extra_hook=None,
                   case_label="marker", phase="execute", meta=None):
    machine = prepare_machine(image)
    set_sentinels(machine)
    machine.reg_write(UC_X86_REG_EBP, ebp)
    machine.reg_write(UC_X86_REG_ESP, esp)
    state = {
        "path": [],
        "tape": bytearray(),
        "continue_esp": None,
        "marker_pending": None,
        "flags": None,
        "sentinels": None,
    }

    def on_code(cpu, address, size, _):
        if extra_hook is not None and extra_hook(cpu, address, size, state):
            return
        if address == BOUNDARY:
            state["path"].append("boundary")
        elif address == LEGACY_ENTRY:
            state["path"].append("legacy_extension")
        elif address == CONTINUE:
            state["path"].append("continue")
            state["continue_esp"] = cpu.reg_read(UC_X86_REG_ESP)
            state["marker_pending"] = read_u32(cpu, state["continue_esp"])
            state["flags"] = cpu.reg_read(UC_X86_REG_EFLAGS)
            state["sentinels"] = {
                name: cpu.reg_read(register)
                for name, (register, _) in SENTINELS.items()
            }
        elif address == APPEND_LEAF:
            stack_pointer = cpu.reg_read(UC_X86_REG_ESP)
            pointer = read_u32(cpu, stack_pointer + 4)
            length = read_u32(cpu, stack_pointer + 8)
            require(0 < length <= 16, f"unexpected append length {length}")
            state["tape"].extend(cpu.mem_read(pointer, length))
            return_address = read_u32(cpu, stack_pointer)
            cpu.reg_write(UC_X86_REG_EAX, 0)
            cpu.reg_write(UC_X86_REG_ESP, stack_pointer + 12)
            cpu.reg_write(UC_X86_REG_EIP, return_address)
        elif address == AFTER_WRITER:
            cpu.emu_stop()

    machine.hook_add(UC_HOOK_CODE, on_code)
    guarded_emu_start(
        machine, start, STOP, case_label=case_label, phase=phase,
        meta=meta, state=state, count=50000, timeout=5_000_000,
    )
    require(state["continue_esp"] == esp - 4, "marker not pending at continuation")
    require(machine.reg_read(UC_X86_REG_ESP) == esp, "marker writer stack imbalance")
    require(state["flags"] == 0x246, "EFLAGS changed by marker path")
    for name, (_, expected) in SENTINELS.items():
        require(state["sentinels"][name] == expected, f"{name} changed by marker path")
    return state, machine


def run_legacy_case(image, return_address, parent_return, slot, count, arg4,
                    mode, null_frame, *, case_label="unnamed-legacy",
                    phase="legacy"):
    machine = prepare_machine(image)
    set_sentinels(machine)
    put_u32(machine, FRAME, 0 if null_frame else FRAME + 0x1000)
    put_u32(machine, FRAME + 4, return_address)
    put_u32(machine, FRAME + 0x1004, parent_return)
    for displacement, value in (
        (0x8, 0x10),
        (0xC, slot),
        (0x10, count),
        (0x14, arg4),
        (0x18, 0),
    ):
        put_u32(machine, FRAME + displacement, value)

    if mode == "real":
        call_site = return_address - 5
        require(image.bytes_at(call_site, 5) ==
                b"\xe8" + struct.pack("<i", rec.PRODUCER - call_site - 5),
                f"{return_address:#x} is not a real producer CALL")
        entry_esp = STACK + 0x8000
        for offset, value in enumerate((0x10, slot, count, arg4, 0)):
            put_u32(machine, entry_esp + offset * 4, value)
        machine.reg_write(UC_X86_REG_ESP, entry_esp)
        machine.reg_write(UC_X86_REG_EBP, FRAME + 0x1000)
        start = call_site
        boundary_esp = entry_esp - 8
    elif mode == "synthetic":
        # The accepted fixture keeps synthetic frame records in FRAME while
        # executing on the separate mapped STACK. ESP must never start at the
        # FRAME base, because the first real pushfd would write below it.
        entry_esp = STACK + 0x8000
        machine.reg_write(UC_X86_REG_ESP, entry_esp)
        machine.reg_write(UC_X86_REG_EBP, FRAME)
        start = BOUNDARY
        boundary_esp = entry_esp
    else:
        raise AssertionError(f"unknown mode {mode}")

    state = {
        "path": [],
        "tape": bytearray(),
        "continue_esp": None,
        "marker_pending": None,
        "flags": None,
        "sentinels": None,
    }

    def on_code(cpu, address, size, _):
        if address == PROLOGUE_STOP and mode == "real":
            frame = cpu.reg_read(UC_X86_REG_EBP)
            require(frame == boundary_esp, "producer frame not installed")
            require(read_u32(cpu, frame + 4) == return_address, "saved return differs")
            cpu.reg_write(UC_X86_REG_EIP, BOUNDARY)
        elif address == BOUNDARY:
            state["path"].append("boundary")
        elif address == LEGACY_ENTRY:
            state["path"].append("legacy_extension")
        elif address == CONTINUE:
            state["path"].append("continue")
            state["continue_esp"] = cpu.reg_read(UC_X86_REG_ESP)
            state["marker_pending"] = read_u32(cpu, state["continue_esp"])
            state["flags"] = cpu.reg_read(UC_X86_REG_EFLAGS)
            state["sentinels"] = {
                name: cpu.reg_read(register)
                for name, (register, _) in SENTINELS.items()
            }
        elif address == APPEND_LEAF:
            stack_pointer = cpu.reg_read(UC_X86_REG_ESP)
            pointer = read_u32(cpu, stack_pointer + 4)
            length = read_u32(cpu, stack_pointer + 8)
            require(0 < length <= 16, f"unexpected append length {length}")
            state["tape"].extend(cpu.mem_read(pointer, length))
            return_address_value = read_u32(cpu, stack_pointer)
            cpu.reg_write(UC_X86_REG_EAX, 0)
            cpu.reg_write(UC_X86_REG_ESP, stack_pointer + 12)
            cpu.reg_write(UC_X86_REG_EIP, return_address_value)
        elif address == AFTER_WRITER:
            cpu.emu_stop()

    machine.hook_add(UC_HOOK_CODE, on_code)
    guarded_emu_start(
        machine, start, STOP, case_label=case_label, phase=phase,
        meta={
            "return": hex(return_address),
            "parent": hex(parent_return),
            "slot": slot,
            "count": count,
            "arg4": arg4,
            "mode": mode,
            "nullFrame": null_frame,
            "boundaryEsp": hex(boundary_esp),
        },
        state=state, count=50000, timeout=5_000_000,
    )
    require(state["continue_esp"] == boundary_esp - 4,
            "legacy marker not pending at continuation")
    require(machine.reg_read(UC_X86_REG_ESP) == boundary_esp,
            "legacy writer stack imbalance")
    require(state["flags"] == 0x246, "legacy EFLAGS changed")
    for name, (_, expected) in SENTINELS.items():
        require(state["sentinels"][name] == expected, f"legacy {name} changed")
    return {
        "marker": state["marker_pending"],
        "tape": list(state["tape"]),
        "path": state["path"],
        "mode": mode,
        "callSite": hex(return_address - 5) if mode == "real" else None,
    }


def set_inner_outer_frames(machine, *, inner=None, outer=None, grand=None):
    inner = dict(STRICT_INNER if inner is None else inner)
    outer = dict(STRICT_OUTER if outer is None else outer)
    inner_address = FRAME + 0x1000
    outer_address = FRAME + 0x2000
    grand_address = FRAME + 0x3000
    put_u32(machine, inner_address, outer_address)
    put_u32(machine, outer_address, grand_address)
    put_u32(machine, grand_address, 0)
    put_u32(machine, inner_address + 4, inner.get("return", rec.INNER_RETURN))
    put_u32(machine, outer_address + 4, outer.get("return", rec.OUTER_RETURN))
    put_u32(machine, grand_address + 4, rec.OUTER_PARENT_RETURN)
    for displacement, key in ((8, "source"), (0xC, "slot"),
                              (0x10, "count"), (0x14, "arg4"), (0x18, "arg5")):
        put_u32(machine, inner_address + displacement, inner[key])
        put_u32(machine, outer_address + displacement, outer[key])
    return inner_address, outer_address, grand_address


def run_synthetic_recursive_case(image, *, inner=None, outer=None,
                                 inner_parent=None, grand_return=None,
                                 case_label="unnamed-synthetic-recursive",
                                 phase="synthetic-recursive"):
    machine = prepare_machine(image)
    set_sentinels(machine)
    inner_address, outer_address, grand_address = set_inner_outer_frames(
        machine, inner=inner, outer=outer
    )
    if inner_parent is not None:
        put_u32(machine, inner_address, inner_parent)
    if grand_return is not None:
        put_u32(machine, grand_address + 4, grand_return)
    state = {"path": [], "tape": bytearray(), "continue_esp": None,
             "marker_pending": None, "flags": None, "sentinels": None}

    def on_code(cpu, address, size, _):
        if address == BOUNDARY:
            state["path"].append("boundary")
        elif address == LEGACY_ENTRY:
            state["path"].append("legacy_extension")
        elif address == CONTINUE:
            state["path"].append("continue")
            state["continue_esp"] = cpu.reg_read(UC_X86_REG_ESP)
            state["marker_pending"] = read_u32(cpu, state["continue_esp"])
            state["flags"] = cpu.reg_read(UC_X86_REG_EFLAGS)
            state["sentinels"] = {
                name: cpu.reg_read(register)
                for name, (register, _) in SENTINELS.items()
            }
        elif address == APPEND_LEAF:
            stack_pointer = cpu.reg_read(UC_X86_REG_ESP)
            pointer = read_u32(cpu, stack_pointer + 4)
            length = read_u32(cpu, stack_pointer + 8)
            state["tape"].extend(cpu.mem_read(pointer, length))
            return_address = read_u32(cpu, stack_pointer)
            cpu.reg_write(UC_X86_REG_EAX, 0)
            cpu.reg_write(UC_X86_REG_ESP, stack_pointer + 12)
            cpu.reg_write(UC_X86_REG_EIP, return_address)
        elif address == AFTER_WRITER:
            cpu.emu_stop()

    machine.hook_add(UC_HOOK_CODE, on_code)
    machine.reg_write(UC_X86_REG_EBP, inner_address)
    machine.reg_write(UC_X86_REG_ESP, inner_address)
    guarded_emu_start(
        machine, BOUNDARY, STOP, case_label=case_label, phase=phase,
        meta={
            "innerFrame": hex(inner_address),
            "outerFrame": hex(outer_address),
            "grandFrame": hex(grand_address),
            "innerArgs": inner,
            "outerArgs": outer,
            "grandReturn": None if grand_return is None else hex(grand_return),
        },
        state=state, count=50000, timeout=5_000_000,
    )
    require(state["continue_esp"] == inner_address - 4,
            "recursive marker not pending at continuation")
    require(machine.reg_read(UC_X86_REG_ESP) == inner_address,
            "recursive writer stack imbalance")
    require(state["flags"] == 0x246, "recursive EFLAGS changed")
    for name, (_, expected) in SENTINELS.items():
        require(state["sentinels"][name] == expected, f"recursive {name} changed")
    return {"marker": state["marker_pending"], "tape": list(state["tape"]),
            "path": state["path"]}


def run_recursive_route(image, *, fixture="real-fixed-prefix", injected=None,
                        resolution_source=None, resolution_destination=None,
                        grand_return=None, expect_recursion=True,
                        case_label="unnamed-recursive-route",
                        phase="recursive-route"):
    """Run the real CALL and recursive branch with an explicit fixture mode.

    real-fixed-prefix executes the binary's real 0x12ACB6F..0x12ACB78 PUSH
    sequence. injected-before-call starts at the real CALL and writes all five
    ABI arguments explicitly, which is required for outer-slot/count and
    arg4=2 negatives. injected-resolution-state additionally supplies the two
    local values that the real source/destination resolver would leave before
    0x25FA362; it is used for a source=0 case that must not enter recursion.
    """
    machine = prepare_machine(image)
    set_sentinels(machine)
    entry_esp = STACK + 0x6000
    ui_frame = STACK + 0x6800
    grand_return = rec.OUTER_PARENT_RETURN if grand_return is None else grand_return
    put_u32(machine, ui_frame + 4, grand_return)
    machine.reg_write(UC_X86_REG_EBP, ui_frame)
    machine.reg_write(UC_X86_REG_ESP, entry_esp)
    machine.reg_write(UC_X86_REG_ECX, FAKE_WINDOW)

    if fixture == "real-fixed-prefix":
        require(injected is None and resolution_source is None and
                resolution_destination is None,
                "real fixed prefix cannot also inject route state")
        expected_outer = dict(STRICT_OUTER)
        start = rec.OUTER_CALL - 9
        outer_address = entry_esp - 0x1C
        machine.reg_write(UC_X86_REG_EDX, expected_outer["source"])
    elif fixture in ("injected-before-call", "injected-resolution-state"):
        require(injected is not None, "injected route requires five arguments")
        expected_outer = dict(injected)
        start = rec.OUTER_CALL
        outer_address = entry_esp - 8
        for offset, key in enumerate(("source", "slot", "count", "arg4", "arg5")):
            put_u32(machine, entry_esp + offset * 4, expected_outer[key])
        machine.reg_write(UC_X86_REG_EDX, expected_outer["source"])
        if fixture == "injected-before-call":
            require(resolution_source is None and resolution_destination is None,
                    "injected-before-call does not accept resolution overrides")
    else:
        raise AssertionError(f"unknown route fixture {fixture}")

    resolved_source = (expected_outer["source"] if resolution_source is None
                       else resolution_source)
    resolved_destination = (-1 if resolution_destination is None
                            else resolution_destination)

    state = {
        "path": [], "tape": bytearray(), "continue_esp": None,
        "marker_pending": None, "flags": None, "callFixture": fixture,
        "injected": None if injected is None else dict(injected),
        "resolutionFixture": {
            "source": resolved_source,
            "destination": resolved_destination,
        },
        "outerFrame": None, "innerFrame": None, "outerArgs": None,
        "innerArgs": None, "recursiveEntered": False,
        "noRecursiveExit": False,
    }

    def args_at(cpu, frame):
        return dict(
            source=read_i32(cpu, frame + 8),
            slot=read_i32(cpu, frame + 0xC),
            count=read_i32(cpu, frame + 0x10),
            arg4=read_i32(cpu, frame + 0x14),
            arg5=read_i32(cpu, frame + 0x18),
        )

    def on_code(cpu, address, size, _):
        if address == rec.OUTER_CALL:
            state["path"].append("outer_call")
        elif address == PROLOGUE_STOP:
            frame = cpu.reg_read(UC_X86_REG_EBP)
            if state["outerFrame"] is None:
                require(frame == outer_address, "outer producer frame mismatch")
                require(read_u32(cpu, frame) == ui_frame, "outer saved EBP mismatch")
                require(read_u32(cpu, frame + 4) == rec.OUTER_RETURN,
                        "outer saved return mismatch")
                actual_outer = args_at(cpu, frame)
                require(actual_outer == expected_outer, "outer producer args mismatch")
                require(read_u32(cpu, ui_frame + 4) == grand_return,
                        "authenticated parent return mismatch")
                state["outerFrame"] = frame
                state["outerArgs"] = actual_outer
                state["path"].extend(["outer_prologue", "pre_recursion_body_stubbed"])
                cpu.reg_write(UC_X86_REG_EBX, FAKE_WINDOW)
                cpu.reg_write(UC_X86_REG_EDI, FAKE_ITEM)
                cpu.reg_write(UC_X86_REG_ESI, actual_outer["arg4"])
                cpu.reg_write(UC_X86_REG_EIP, 0x25FA28B)
            else:
                require(frame != state["outerFrame"], "recursive frame did not nest")
                require(read_u32(cpu, frame) == state["outerFrame"],
                        "inner saved EBP mismatch")
                require(read_u32(cpu, frame + 4) == rec.INNER_RETURN,
                        "inner saved return mismatch")
                state["innerFrame"] = frame
                state["innerArgs"] = args_at(cpu, frame)
                state["path"].extend(["inner_prologue", "inner_pre_marker_body_stubbed"])
                cpu.reg_write(UC_X86_REG_EIP, BOUNDARY)
        elif address == 0x25FA298:
            require(cpu.reg_read(UC_X86_REG_EBP) == state["outerFrame"],
                    "outer source-save frame changed")
            state["path"].append("source_destination_resolution_stubbed")
            put_u32(cpu, state["outerFrame"] - 0x18, resolved_source)
            put_u32(cpu, state["outerFrame"] - 0x24, resolved_destination)
            cpu.reg_write(UC_X86_REG_EIP, 0x25FA362)
        elif address == 0x25FA362:
            state["path"].append("source_test")
        elif address == 0x25FA36A:
            state["path"].append("destination_test")
        elif address == 0x25FA372:
            state["path"].append("destination_range_test")
        elif address == 0x25FA3B2:
            state["path"].append("resolution_minus_one_branch")
        elif address == 0x2531A80:
            cpu.reg_write(UC_X86_REG_EAX, FAKE_WINDOW)
            resume_return(cpu)
        elif address == 0x25FA3D7:
            state["path"].append("window_lookup_stubbed")
            cpu.reg_write(UC_X86_REG_EAX, FAKE_WINDOW)
            cpu.reg_write(UC_X86_REG_EBX, FAKE_WINDOW)
            cpu.reg_write(UC_X86_REG_ESI, 0)
            cpu.reg_write(UC_X86_REG_EIP, 0x25FA3FC)
        elif address == 0x24A5A20:
            require(read_u32(cpu, cpu.reg_read(UC_X86_REG_ESP)) == 0x25FA405,
                    "unexpected 0x24A5A20 return")
            cpu.reg_write(UC_X86_REG_EAX, 0)
            resume_return(cpu, 8)
        elif address == 0x25EB680:
            require(read_u32(cpu, cpu.reg_read(UC_X86_REG_ESP)) in
                    (0x25FA438, 0x25FA43D), "unexpected bag-slot lookup return")
            cpu.reg_write(UC_X86_REG_EAX, 0)
            resume_return(cpu, 4)
        elif address == 0x25FA4AC:
            state["noRecursiveExit"] = True
            state["path"].append("producer_no_recursion_exit")
            if not expect_recursion:
                cpu.emu_stop()
        elif address == rec.RECURSIVE_CALL:
            state["recursiveEntered"] = True
            state["path"].append("recursive_call")
        elif address == BOUNDARY:
            state["path"].append("boundary")
        elif address == LEGACY_ENTRY:
            state["path"].append("legacy_extension")
        elif address == CONTINUE:
            state["path"].append("continue")
            state["continue_esp"] = cpu.reg_read(UC_X86_REG_ESP)
            state["marker_pending"] = read_u32(cpu, state["continue_esp"])
            state["flags"] = cpu.reg_read(UC_X86_REG_EFLAGS)
        elif address == APPEND_LEAF:
            stack_pointer = cpu.reg_read(UC_X86_REG_ESP)
            pointer = read_u32(cpu, stack_pointer + 4)
            length = read_u32(cpu, stack_pointer + 8)
            state["tape"].extend(cpu.mem_read(pointer, length))
            return_address = read_u32(cpu, stack_pointer)
            cpu.reg_write(UC_X86_REG_EAX, 0)
            cpu.reg_write(UC_X86_REG_ESP, stack_pointer + 12)
            cpu.reg_write(UC_X86_REG_EIP, return_address)
        elif address == AFTER_WRITER:
            cpu.emu_stop()

    machine.hook_add(UC_HOOK_CODE, on_code)
    guarded_emu_start(
        machine, start, STOP, case_label=case_label, phase=phase,
        meta={
            "fixture": fixture,
            "injected": None if injected is None else injected,
            "resolutionSource": resolution_source,
            "resolutionDestination": resolution_destination,
            "grandReturn": (None if grand_return is None
                            else hex(grand_return)),
            "expectRecursion": expect_recursion,
        },
        state=state, count=100000, timeout=8_000_000,
    )
    if not expect_recursion:
        require(state["noRecursiveExit"] and not state["recursiveEntered"],
                "route unexpectedly entered recursive producer")
        state["tape"] = list(state["tape"])
        return state
    for item in ("outer_call", "outer_prologue", "source_test",
                 "resolution_minus_one_branch", "recursive_call",
                 "inner_prologue", "boundary"):
        require(item in state["path"], f"missing route step {item}")
    inner = state["innerArgs"]
    outer = state["outerArgs"]
    require(inner is not None and outer is not None,
            "recursive route did not record both producer frames")
    require(inner["source"] != -1, "recursive source remained -1")
    require(inner["slot"] == rec.TARGET_SLOT, "recursive destination differs")
    require(inner["count"] == outer["count"] + 1,
            "inner/outer count relation failed")
    require(inner["arg4"] == rec.INNER_ARG4, "recursive arg4 differs")
    require(inner["arg5"] == rec.INNER_ARG5, "recursive arg5 differs")
    require(state["marker_pending"] is not None, "marker continuation not reached")
    # Match the legacy and synthetic runners: return a JSON/list-friendly tape
    # so the main assertion compares equal lengths and byte values.
    state["tape"] = list(state["tape"])
    return state


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise SystemExit("--output must be a new path")
    available_memory()
    source = args.input.read_bytes()
    candidate = args.candidate.read_bytes()
    expected, receipt = build_candidate.build(source)
    require(candidate == expected, "candidate differs from deterministic builder")
    baseline_image = Image.from_bytes(source)
    candidate_image = Image.from_bytes(candidate)

    legacy_rows = []
    v177_rows = []
    for name, ret, parent, slot, count, arg4, expected_v179, mode, null_frame in ORIGINAL_CASES:
        old = run_legacy_case(
            baseline_image, ret, parent, slot, count, arg4, mode, null_frame,
            case_label=name, phase="legacy-v179-baseline",
        )
        new = run_legacy_case(
            candidate_image, ret, parent, slot, count, arg4, mode, null_frame,
            case_label=name, phase="legacy-candidate",
        )
        require(old == new, f"{name}: V179 baseline/candidate behavior differs")
        require(old["marker"] == expected_v179 and new["marker"] == expected_v179,
                f"{name}: V179 marker mismatch")
        require(old["tape"] == [expected_v179 & 0xFF] and
                new["tape"] == [expected_v179 & 0xFF],
                f"{name}: tape mismatch")
        legacy_rows.append({"name": name, "return": hex(ret), "parent": hex(parent),
                            "slot": slot, "count": count, "arg4": arg4,
                            "expectedV179": expected_v179,
                            "beforeV179": old, "afterV179": new, "passed": True})
        before_v177 = V177_HISTORICAL_BEFORE[name]
        v177_rows.append({
            "name": name,
            "beforeV177": before_v177,
            "afterV179": expected_v179,
            "redBeforeV179": before_v177 != expected_v179,
            "usedForCurrentAssertion": False,
        })

    boundary_cases = []
    synthetic = [
        ("recursive-positive", {}, {}, None, None, 2),
        ("wrong-inner-return", {"return": 0x25FA396}, {}, None, None, 0),
        ("wrong-outer-return", {}, {"return": 0x12ACB30}, None, None, 0),
        ("wrong-outer-source", {}, {"source": 0}, None, None, 0),
        ("wrong-outer-slot", {}, {"slot": 12}, None, None, 0),
        ("wrong-outer-count", {}, {"count": 2}, None, None, 0),
        ("wrong-outer-arg4", {}, {"arg4": 2}, None, None, 0),
        ("wrong-outer-arg5", {}, {"arg5": 1}, None, None, 0),
        ("wrong-inner-source", {"source": -1}, {}, None, None, 0),
        ("wrong-inner-slot", {"slot": 12}, {}, None, None, 0),
        ("wrong-inner-count", {"count": 3}, {}, None, None, 0),
        ("wrong-inner-arg4", {"arg4": 2}, {}, None, None, 0),
        ("wrong-inner-arg5", {"arg5": 0}, {}, None, None, 0),
        ("wrong-grandparent", {}, {}, None, 0x12AE884, 0),
        ("null-parent", {}, {}, 0, None, 0),
        ("far-parent", {}, {}, 0xB0000000, None, 0),
    ]
    for name, inner_patch, outer_patch, parent_override, grand_override, want in synthetic:
        inner = dict(STRICT_INNER)
        outer = dict(STRICT_OUTER)
        inner.update(inner_patch)
        outer.update(outer_patch)
        before = run_synthetic_recursive_case(
            baseline_image, inner=inner, outer=outer,
            inner_parent=parent_override, grand_return=grand_override,
            case_label=name, phase="synthetic-recursive-baseline",
        )
        after = run_synthetic_recursive_case(
            candidate_image, inner=inner, outer=outer,
            inner_parent=parent_override, grand_return=grand_override,
            case_label=name, phase="synthetic-recursive-candidate",
        )
        require(before["marker"] == 0, f"{name}: baseline marker changed")
        require(after["marker"] == want, f"{name}: candidate marker {after['marker']}")
        require(after["tape"] == [want & 0xFF], f"{name}: candidate tape")
        boundary_cases.append({"name": name, "expected": want,
                               "before": before, "after": after})

    def injected_with(**changes):
        values = dict(STRICT_OUTER)
        values.update(changes)
        return values

    route_cases = [
        {
            "name": "recursive-route-positive",
            "fixture": "real-fixed-prefix",
            "expectRecursion": True,
            "baselineMarker": 0,
            "candidateMarker": 2,
            "requiredSteps": ["outer_call", "source_test",
                              "resolution_minus_one_branch", "recursive_call",
                              "inner_prologue", "boundary"],
        },
        {
            "name": "recursive-route-wrong-grandparent",
            "fixture": "real-fixed-prefix",
            "grandReturn": 0x12AE884,
            "expectRecursion": True,
            "baselineMarker": 0,
            "candidateMarker": 0,
            "requiredSteps": ["recursive_call", "boundary"],
        },
        {
            "name": "recursive-route-wrong-slot-injected",
            "fixture": "injected-before-call",
            "injected": injected_with(slot=12),
            "expectRecursion": True,
            "baselineMarker": 0,
            "candidateMarker": 0,
            "requiredSteps": ["recursive_call", "boundary"],
        },
        {
            "name": "recursive-route-wrong-count-injected",
            "fixture": "injected-before-call",
            "injected": injected_with(count=2),
            "expectRecursion": True,
            "baselineMarker": 0,
            "candidateMarker": 0,
            "requiredSteps": ["recursive_call", "boundary"],
        },
        {
            "name": "recursive-route-arg4-two-injected",
            "fixture": "injected-before-call",
            "injected": injected_with(arg4=2),
            "expectRecursion": False,
            "requiredSteps": ["source_test", "resolution_minus_one_branch",
                              "producer_no_recursion_exit"],
            "forbiddenSteps": ["recursive_call", "boundary"],
        },
        {
            "name": "recursive-route-source-zero-resolution-state",
            "fixture": "injected-resolution-state",
            "injected": injected_with(source=0),
            "resolutionSource": 0,
            "resolutionDestination": 12,
            "expectRecursion": False,
            "requiredSteps": ["source_test", "destination_test",
                              "destination_range_test", "producer_no_recursion_exit"],
            "forbiddenSteps": ["resolution_minus_one_branch", "recursive_call",
                               "boundary"],
        },
    ]

    def route_summary(state):
        return {
            "markerPending": state["marker_pending"],
            "tape": list(state["tape"]),
            "path": state["path"],
            "callFixture": state["callFixture"],
            "injected": state["injected"],
            "resolutionFixture": state["resolutionFixture"],
            "outerArgs": state["outerArgs"],
            "innerArgs": state["innerArgs"],
            "recursiveEntered": state["recursiveEntered"],
            "noRecursiveExit": state["noRecursiveExit"],
        }

    route_rows = []
    for spec in route_cases:
        name = spec["name"]
        kwargs = {
            "fixture": spec["fixture"],
            "injected": spec.get("injected"),
            "resolution_source": spec.get("resolutionSource"),
            "resolution_destination": spec.get("resolutionDestination"),
            "grand_return": spec.get("grandReturn"),
            "expect_recursion": spec["expectRecursion"],
            "case_label": name,
        }
        before = run_recursive_route(
            baseline_image, phase="recursive-route-baseline", **kwargs
        )
        after = run_recursive_route(
            candidate_image, phase="recursive-route-candidate", **kwargs
        )
        expected_outer = (spec.get("injected") if spec.get("injected") is not None
                          else STRICT_OUTER)
        for side, state in (("baseline", before), ("candidate", after)):
            require(state["outerArgs"] == expected_outer,
                    f"{name}: {side} outer arguments differ")
            for required in spec.get("requiredSteps", []):
                require(required in state["path"],
                        f"{name}: {side} missing {required}")
            for forbidden in spec.get("forbiddenSteps", []):
                require(forbidden not in state["path"],
                        f"{name}: {side} unexpectedly entered {forbidden}")
            if spec["expectRecursion"]:
                expected = (spec["baselineMarker"] if side == "baseline"
                            else spec["candidateMarker"])
                require(state["marker_pending"] == expected,
                        f"{name}: {side} marker")
                require(state["tape"] == [expected & 0xFF],
                        f"{name}: {side} tape")
            else:
                require(state["marker_pending"] is None and not state["tape"],
                        f"{name}: {side} unexpectedly reached marker writer")
                require(state["noRecursiveExit"] and not state["recursiveEntered"],
                        f"{name}: {side} recursive boundary was not preserved")
        route_rows.append({
            "name": name,
            "fixture": spec["fixture"],
            "expectRecursion": spec["expectRecursion"],
            "grandReturn": (None if spec.get("grandReturn") is None
                            else hex(spec["grandReturn"])),
            "baseline": route_summary(before),
            "candidate": route_summary(after),
            "passed": True,
        })

    report = {
        "schema": "title-recursive-bounded.v2",
        "executed": True,
        "passed": True,
        "inputSha256": sha256(source),
        "candidateSha256": sha256(candidate),
        "builderReceipt": receipt,
        "legacyCases": legacy_rows,
        "v177HistoricalEvidence": v177_rows,
        "recursiveBoundaryCases": boundary_cases,
        "recursiveRouteCases": route_rows,
        "scope": {
            "outerCallExecuted": hex(rec.OUTER_CALL),
            "outerReturn": hex(rec.OUTER_RETURN),
            "outerParentReturn": hex(rec.OUTER_PARENT_RETURN),
            "recursiveCallExecuted": hex(rec.RECURSIVE_CALL),
            "innerReturn": hex(rec.INNER_RETURN),
            "markerGetter": hex(rec.WRITER_GETTER),
            "markerWriter": hex(rec.WRITER_U8),
            "appendLeafAbiStub": hex(APPEND_LEAF),
            "legacyBaseline": "the raw df496 image already contains the V179 rules; baseline and candidate are both asserted against expectedV179",
            "v177HistoricalEvidenceAsserted": False,
            "routeFixtures": {
                "real-fixed-prefix": "real 0x12ACB6F..0x12ACB78 fixed PUSH sequence",
                "injected-before-call": "real CALL with five ABI arguments explicitly injected",
                "injected-resolution-state": "real CALL plus explicit source/destination resolver output before the real 0x25FA362 branch",
            },
            "stubBoundary": [
                "outer producer UI/serialization prelude before 0x25FA28B is skipped after the real outer CALL/prologue",
                "the real source/destination resolver body at 0x25FA298 is replaced by the caller-selected fixture values immediately before the real 0x25FA362 branch",
                "window lookup at 0x25FA3D7 is shortened to the real empty-slot scan at 0x25FA3FC",
                "0x2531A80 and 0x25EB680 are bounded inventory/window lookup stubs",
                "inner producer pre-marker body after the real recursive CALL/prologue is skipped by jumping to the real boundary",
                "only the terminal append leaf is an ABI stub; marker getter, writer, extension, redirect, and continuation execute",
            ],
            "notClaimed": [
                "full producer packet serialization",
                "real UI event-to-producer route",
                "game GUI or gameplay acceptance",
                "packed client execution",
            ],
        },
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"passed": True, "legacyCases": len(legacy_rows),
                      "recursiveBoundaryCases": len(boundary_cases),
                      "recursiveRouteCases": len(route_rows)}))


if __name__ == "__main__":
    main()
