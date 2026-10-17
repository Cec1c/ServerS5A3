"""Execute both pinned receive gates for every uint16 opcode using Unicorn."""

import argparse
import json
from pathlib import Path
import struct
import sys

from story_pause_gate import BITMAP_VA, GATE_VA, OPCODE, build, pe_layout, sha256


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("--emulator-deps", type=Path)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    if args.receipt.exists():
        parser.error("receipt must be a new file")
    if args.emulator_deps:
        sys.path.insert(0, str(args.emulator_deps))
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
    from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_EAX, UC_X86_REG_EIP

    old = args.source.read_bytes()
    candidate, patch = build(old)
    if args.candidate.read_bytes() != candidate:
        parser.error("candidate differs from the pinned one-bit patch")
    base, size, sections = pe_layout(candidate)
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    uc.mem_map(base, (size + 4095) // 4096 * 4096)
    for va, length, offset, _ in sections:
        if length:
            uc.mem_write(va, candidate[offset:offset + length])
    stack, stop = 0x10000000, 0x20000000
    uc.mem_map(stack, 4096)
    uc.mem_map(stop, 4096)
    change = patch["changes"][0]
    delta = []
    before_allowed = after_allowed = 0
    for opcode in range(65536):
        results = []
        for patched in (False, True):
            uc.mem_write(BITMAP_VA + OPCODE // 8, bytes([change["after"] if patched else change["before"]]))
            uc.mem_write(stack, struct.pack("<II", stop, opcode))
            uc.reg_write(UC_X86_REG_ESP, stack)
            uc.emu_start(GATE_VA, stop, count=30)
            if uc.reg_read(UC_X86_REG_EIP) != stop or uc.reg_read(UC_X86_REG_ESP) != stack + 8:
                raise RuntimeError("native gate did not return with the expected stack")
            value = uc.reg_read(UC_X86_REG_EAX)
            if value not in (0, 1):
                raise RuntimeError("native gate returned a non-boolean value")
            results.append(value)
        before_allowed += results[0]
        after_allowed += results[1]
        if results[0] != results[1]:
            delta.append(opcode)
            if results != [0, 1]:
                raise RuntimeError("unexpected admission removal")
    if delta != [OPCODE]:
        raise RuntimeError("unexpected native gate delta")
    receipt = dict(passed=True, compared_opcodes=65536, native_executions=131072,
                   before_allowed=before_allowed, after_allowed=after_allowed,
                   newly_admitted_opcodes=delta, sha256=sha256(candidate),
                   scope="actual native receive gate; excludes dispatch, protected final setter and GUI",
                   native_acceptance=False)
    with args.receipt.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
