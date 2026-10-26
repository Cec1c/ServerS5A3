"""Execute original/patched name functions in bounded Unicorn; exact whole-file diff."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import random
import struct
import sys

sys.dont_write_bytecode = True
import patch_name as patch
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
    UC_X86_REG_ESP, UC_X86_REG_EIP, UC_X86_REG_EFLAGS)

REGS = (UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_ESI,
        UC_X86_REG_EDI, UC_X86_REG_EBP)
STACK, STOP = 0x7000800, 0x7100000
MAPPING = {
    63840: 66071, 63843: 66071,
    71026: 66068, 71027: 66069, 71028: 66068, 71029: 66069,
    0x660B5A3: 66073, 0x660B5A5: 66073, 0x660B5A7: 66070,
    0x660B8D6: 66070, 0x660BBE7: 66067, 0x660BBEA: 66072,
    0x660BBEE: 66067, 0x660BBEF: 66072,
}


class Harness:
    def __init__(self, code):
        self.uc = Uc(UC_ARCH_X86, UC_MODE_32)
        self.uc.mem_map(patch.START & ~0xFFF, 0x1000)
        self.uc.mem_write(patch.START, code)
        self.uc.mem_map(STACK & ~0xFFF, 0x1000)
        self.uc.mem_map(STOP, 0x1000)
        self.code = code

    def run(self, value, seed):
        uc = self.uc
        values = [((seed + i * 0x1734ABCD) ^ 0xCAFE1234) & 0xFFFFFFFF for i in range(len(REGS))]
        for reg, initial in zip(REGS, values):
            uc.reg_write(reg, initial)
        uc.reg_write(UC_X86_REG_EAX, seed)
        uc.reg_write(UC_X86_REG_ESP, STACK)
        uc.reg_write(UC_X86_REG_EFLAGS, 0x202 | (seed & 0xCD5))
        before = bytearray(b'\xa5' * 32)
        struct.pack_into('<II', before, 8, STOP, value)
        uc.mem_write(STACK - 8, bytes(before))
        uc.emu_start(patch.START, STOP, count=80)
        patch.require(uc.reg_read(UC_X86_REG_EIP) == STOP, 'bounded execution did not return')
        patch.require(uc.reg_read(UC_X86_REG_ESP) == STACK + 8, 'ret4 stack mismatch')
        patch.require([uc.reg_read(r) for r in REGS] == values, 'register preservation')
        struct.pack_into('<I', before, 4, values[-1])  # only PUSH EBP writes memory
        patch.require(bytes(uc.mem_read(STACK - 8, 32)) == before, 'unexpected stack write')
        patch.require(bytes(uc.mem_read(patch.START, len(self.code))) == self.code, 'code write')
        return uc.reg_read(UC_X86_REG_EAX)


def exact_diff(original, candidate):
    patch.require(len(original) == len(candidate), 'PE length changed')
    allowed = {}
    for _, off, replacement in patch.edits(original):
        allowed.update({off + i: b for i, b in enumerate(replacement)})
    checksum_off = patch.checksum_offset(original)
    checksum = patch.pe_checksum(candidate)
    allowed.update({checksum_off + i: b for i, b in enumerate(struct.pack('<I', checksum))})
    changed = []
    for start in range(0, len(original), 65536):
        left, right = original[start:start + 65536], candidate[start:start + 65536]
        if left != right:
            for i, (a, b) in enumerate(zip(left, right)):
                if a != b:
                    off = start + i
                    patch.require(off in allowed and b == allowed[off], 'unexpected PE difference')
                    changed.append(off)
    for off, expected in allowed.items():
        patch.require(candidate[off] == expected, 'missing patch byte')
    patch.require(len([p for p in changed if not checksum_off <= p < checksum_off + 4]) == 15,
                  'expected one displacement plus fourteen cave bytes outside checksum')
    return changed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    available = patch.memory_guard()
    original = patch.checked_input(args.input)
    candidate = args.candidate.read_bytes()
    changed = exact_diff(original, candidate)
    off = patch.offset(original, patch.START, len(patch.FUNCTION))
    old, new = Harness(original[off:off + len(patch.FUNCTION)]), Harness(candidate[off:off + len(patch.FUNCTION)])
    values = {0, 1, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFE, 0xFFFFFFFF, 63847}
    for center in [*MAPPING, 63847, 0x80000000, 0]:
        values.update((center + delta) & 0xFFFFFFFF for delta in range(-4, 5))
    values.update(range(63832, 63856))
    rng = random.Random(0x19563847)
    random_values = [rng.getrandbits(32) for _ in range(4096)]
    values.update(random_values)
    observed = []
    for i, value in enumerate(sorted(values)):
        if i % 512 == 0:
            patch.memory_guard()
        for seed in (0x12345678, 0x87654321):
            a, b = old.run(value, seed), new.run(value, seed)
            expected = MAPPING.get(value, 0xFFFFFFFF)
            patch.require(a == expected, f'original mapping {value:#x}: {a:#x}')
            patch.require(b == (66071 if value == 63847 else expected), f'patched mapping {value:#x}: {b:#x}')
            if a != b and value not in observed:
                observed.append(value)
    patch.require(observed == [63847], 'unexpected semantic differences')
    # Anti-misuse: byte mutations outside allowed regions must fail verification.
    rejected = []
    for label, position in [('PE header', 0), ('function body', off + 1), ('next function', off + len(patch.FUNCTION))]:
        altered = bytearray(candidate)
        altered[position] ^= 1
        try:
            exact_diff(original, altered)
        except ValueError:
            rejected.append(label)
        else:
            raise ValueError('unexpected modification accepted')
        del altered
    receipt = dict(passed=True, input_sha256=patch.sha(original), output_sha256=patch.sha(candidate),
                   unique_inputs=len(values), executions=len(values) * 4, random_uint32_samples=4096,
                   seed='0x19563847', changed_inputs=observed, original_mappings=MAPPING,
                   changed_file_offsets=changed, rejected_mutations=rejected,
                   stack_and_registers_preserved=True, native_function_executed=True, stubs=[],
                   exhaustive_uint32_execution=False,
                   all_uint32_reason='Only the displaced JNE path changes. EAX=(input-63843) mod 2^32; '
                       'CMP EAX,4 selects exactly 63847, which reaches this path. All other inputs '
                       'resume the same original default OR EAX,-1. Existing successful mappings bypass cave.',
                   protocol_receive_bits_unchanged=True, pvf_accessed=False,
                   pe_headers_unchanged_except_checksum=True,
                   protectionFloorGiB=3, availableGiB=available, gui_acceptance=False, deployed=False)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    patch.write_json(args.receipt, receipt)
    print(json.dumps(receipt, ensure_ascii=False))


if __name__ == '__main__':
    main()
