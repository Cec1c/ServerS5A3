"""Bounded ABI contrast for the fixed-v170 title marker candidate."""
import argparse
import json
import struct
from pathlib import Path

from unicorn import Uc, UC_ARCH_X86, UC_HOOK_CODE, UC_HOOK_MEM_WRITE, UC_MODE_32
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

from pe_support import INPUT_SHA256, INPUT_SIZE, Image, available_memory


OP19UI = 0x6E95000
CONTINUE = 0x25FA994
MARKER_FUNCTION = 0x374EA10
STACK = 0x10000000
FRAME = 0x11000000
PARENT_FRAME = 0x11001000
STOP = 0x12000000

SIMPLE_EXISTING = (0x2499E10, 0x249A344, 0x249A3DF, 0x249A455)
STACK_CHECKED_EXISTING = (0x249C880, 0x2605F6D)
TITLE_CALL_PARENTS = {
    0x12A3474: (0x12AEF1B, 0x12AF00E, 0x12AB1CD),
    0x12AB194: (0x12AE884,),
    0x12ACB2B: (0x12AE59F,),
    0x12ACB78: (0x12AE59F,),
    0x12AF918: (0x0A5A5000,),
}
OTHER_CALLER = 0x2499E10
UNKNOWN_CALLER = 0x1FDF58D
SENTINELS = {
    "ebx": (UC_X86_REG_EBX, 0x11112222),
    "ecx": (UC_X86_REG_ECX, 0x22223333),
    "edx": (UC_X86_REG_EDX, 0x33334444),
    "esi": (UC_X86_REG_ESI, 0x44445555),
    "edi": (UC_X86_REG_EDI, 0x55556666),
}


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def put_u32(uc, address, value):
    import struct

    uc.mem_write(address, struct.pack("<I", value))


def run_case(image, return_address, parent_return, slot=13, count=1, call_site=None):
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    mapped = (image.size_of_image + 0xFFF) & ~0xFFF
    uc.mem_map(0x400000, mapped)
    for section in image.sections:
        if section["raw_size"]:
            uc.mem_write(
                section["va"],
                image.data[
                    section["raw"] : section["raw"] + section["raw_size"]
                ],
            )
    uc.mem_map(STACK, 0x10000)
    uc.mem_map(FRAME, 0x10000)
    uc.mem_map(STOP, 0x1000)

    put_u32(uc, FRAME, PARENT_FRAME)
    put_u32(uc, FRAME + 4, return_address)
    put_u32(uc, PARENT_FRAME + 4, parent_return)
    for displacement, value in (
        (0x8, 0x10),
        (0xC, slot),
        (0x10, count),
        (0x14, 0),
        (0x18, 0),
    ):
        put_u32(uc, FRAME + displacement, value)

    entry_esp = STACK + 0x8000
    put_u32(uc, entry_esp, return_address)
    uc.reg_write(UC_X86_REG_ESP, entry_esp)
    uc.reg_write(UC_X86_REG_EBP, FRAME)
    uc.reg_write(UC_X86_REG_EFLAGS, 0x246)
    for _, (register, value) in SENTINELS.items():
        uc.reg_write(register, value)
    uc.reg_write(UC_X86_REG_EAX, 0x66778899)

    expected_esp = entry_esp
    start = OP19UI
    if call_site is not None:
        # Execute the real CALL and producer frame prologue. The CPU supplies
        # the saved return; a test fixture cannot silently supply a CALL VA.
        require(image.bytes_at(call_site, 5) == b'\xe8' + struct.pack('<i', 0x25F9A70-call_site-5), 'not an actual OP19 producer CALL')
        for offset, value in enumerate((0x10, slot, count, 0, 0)):
            put_u32(uc, entry_esp + offset * 4, value)
        uc.reg_write(UC_X86_REG_EBP, PARENT_FRAME)
        expected_esp = entry_esp - 8
        start = call_site

    marker = []
    other_writes = []

    def on_code(machine, address, size, _):
        if call_site is not None and address == 0x25F9A73:
            frame = machine.reg_read(UC_X86_REG_EBP)
            require(frame == expected_esp, 'producer frame not installed')
            require(struct.unpack('<I', machine.mem_read(frame+4,4))[0] == return_address, 'CPU saved return differs')
            # Argument/packet construction between the prologue and marker
            # decision is outside this test; it does not change the EBP frame.
            machine.reg_write(UC_X86_REG_EIP, OP19UI)
        elif address == MARKER_FUNCTION:
            marker.append(struct.unpack("<I", machine.mem_read(machine.reg_read(UC_X86_REG_ESP) + 4, 4))[0])
            stack_pointer = machine.reg_read(UC_X86_REG_ESP)
            marker_return = struct.unpack("<I", machine.mem_read(stack_pointer, 4))[0]
            machine.reg_write(UC_X86_REG_EAX, 0x77777777)
            machine.reg_write(UC_X86_REG_ESP, stack_pointer + 8)
            machine.reg_write(UC_X86_REG_EIP, marker_return)
        elif address in (CONTINUE, STOP):
            machine.emu_stop()

    def on_write(machine, access, address, size, value, _):
        if not (STACK <= address < STACK + 0x10000):
            other_writes.append((address, size, value))

    uc.hook_add(UC_HOOK_CODE, on_code)
    uc.hook_add(UC_HOOK_MEM_WRITE, on_write)
    uc.emu_start(start, STOP, count=10000, timeout=2_000_000)
    require(len(marker) == 1, "marker constructor call count")
    require(uc.reg_read(UC_X86_REG_EIP) == CONTINUE, "did not reach common continuation")
    require(uc.reg_read(UC_X86_REG_ESP) == expected_esp, "stack is not balanced")
    require(uc.reg_read(UC_X86_REG_EFLAGS) == 0x246, "EFLAGS changed")
    require(not other_writes, f"unexpected memory writes: {other_writes}")
    for name, (register, expected) in SENTINELS.items():
        require(uc.reg_read(register) == expected, f"{name} changed")
    require(uc.reg_read(UC_X86_REG_EAX) == 0x77777777, "stub return not visible")
    return marker[0]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--published-v173", action="store_true")
    args = parser.parse_args()
    available = available_memory()
    baseline = Image(args.baseline,
        '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060' if args.published_v173 else INPUT_SHA256,
        111915008 if args.published_v173 else INPUT_SIZE)
    candidate = Image(args.candidate)
    cases = []

    def compare(name, ret, parent, slot=13, count=1, before=None, after=None, call_site=None):
        got_before = run_case(baseline, ret, parent, slot, count, call_site)
        got_after = run_case(candidate, ret, parent, slot, count, call_site)
        record = {
            "name": name,
            "return": f"{ret:#x}",
            "parentReturn": f"{parent:#x}",
            "slot": slot,
            "count": count,
            "before": got_before,
            "after": got_after,
            "actualCall": None if call_site is None else hex(call_site),
        }
        if before is not None:
            require(got_before == before, f"{name}: baseline marker {got_before}")
        if after is not None:
            require(got_after == after, f"{name}: candidate marker {got_after}")
        if before is None and after is None:
            require(got_before == got_after, f"{name}: unexpected mismatch")
        cases.append(record)

    for ret in SIMPLE_EXISTING:
        compare(f"existing-simple-{ret:#x}", ret, 0xA5A50000, after=2, before=2)

    for call_site, parents in TITLE_CALL_PARENTS.items():
        ret = call_site + 5
        for parent in parents:
            compare(f"title-{ret:#x}-parent-{parent:#x}", ret, parent, before=0, after=2, call_site=call_site)
        compare(f"title-{ret:#x}-wrong-slot", ret, parents[0], slot=12, before=0, after=0, call_site=call_site)
        compare(f"title-{ret:#x}-wrong-count", ret, parents[0], count=2, before=0, after=0, call_site=call_site)
        if call_site != 0x12AF918:
            compare(f"title-{ret:#x}-wrong-parent", ret, 0xA5A50000, before=0, after=0, call_site=call_site)

    compare("unknown-return", UNKNOWN_CALLER, 0xA5A50000, before=0, after=0)
    compare("other-existing-return", OTHER_CALLER, 0xA5A50000, slot=16, before=2, after=2)

    # Compare every other actual producer caller. These synthetic arguments pass
    # the generic slot/count gate, so any accidental global widening would change
    # at least one marker. Complex pre-existing stack-auth routes are covered by
    # the unchanged baseline/candidate bytes outside the exact changed ranges.
    static_path = args.evidence
    if static_path is not None:
        analysis = json.loads(static_path.read_text(encoding="utf-8"))
        skip_returns = set(SIMPLE_EXISTING) | set(STACK_CHECKED_EXISTING) | {x+5 for x in TITLE_CALL_PARENTS}
        for record in analysis["producerDirectCallers"]:
            call_va = int(record["call_va"], 16)
            ret = call_va + 5
            if ret in skip_returns:
                continue
            compare(
                f"unchanged-producer-{ret:#x}",
                ret,
                0xA5A50000,
                slot=13,
                count=1,
            )

    report = {
        "schema": "add07-title-request-abi-v1",
        "baseline": {"path": str(args.baseline), "sha256": baseline.sha256},
        "candidate": {"path": str(args.candidate), "sha256": candidate.sha256},
        "scope": (
            "bounded .op19ui marker decision only; marker constructor and continuation "
            "are ABI stubs; no client launch, process memory, packet transport, or GUI"
        ),
        "availableBytes": available,
        "protectionFloorGiB": 3,
        "cases": cases,
        "passed": True,
    }
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"passed": True, "cases": len(cases), "candidateSha256": candidate.sha256}))


if __name__ == "__main__":
    main()
