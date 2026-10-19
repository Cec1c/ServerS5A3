"""Offline Unicorn execution of v132's actual inventory use/drag gates.

Import API (no execution or file writes on import):
    image = NativeImage.from_path('DNF.exe', expected_sha256=expected_hash)
    result = run_gate('use', [7], image=image)
    result = run_gate('drag', [], [4, 7], image=image)

Pass candidate post-cleanup state lists to run_gate to check admission. This
module neither implements a cleanup policy nor builds/executes a patch. It
executes only the two bounded gate windows; 'continue' means that gate passed,
not that the full UI action succeeded. Gate instructions inspect vector length,
not its elements. Synthetic headers/elements must remain byte-for-byte intact.
No client process, process-memory APIs, network, protected helpers or stubs.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import struct
import sys
from typing import Iterable

ROOT = Path(__file__).resolve().parent
GATES = {
    'use': {
        'entry': 0x02603890, 'continue': 0x026038A2, 'blocked': 0x02605A2A,
        'bytes': '8b47702b476c83e0fc83f8040f8d88210000',
        'boundaries': {0x02603890, 0x02603893, 0x02603896, 0x02603899, 0x0260389C},
    },
    'drag': {
        'entry': 0x02600D67, 'continue': 0x02600D83, 'blocked': 0x02600DE5,
        'bytes': '8b46702b466c83e0fc83f8047d708b4b702b4b6c83e1fc83f9047d62',
        'boundaries': {
            0x02600D67, 0x02600D6A, 0x02600D6D, 0x02600D70, 0x02600D73,
            0x02600D75, 0x02600D78, 0x02600D7B, 0x02600D7E, 0x02600D81,
        },
    },
}


def require(condition: bool, message: str) -> None:
    # Do not lose validation if Python is invoked with -O.
    if not condition:
        raise AssertionError(message)


@dataclass(frozen=True)
class NativeImage:
    data: bytes
    sha256: str
    path: str
    sections: tuple[tuple[int, int, int], ...]

    @classmethod
    def from_path(cls, path, *, expected_sha256=None):
        """Read a user-supplied image with an optional whole-file SHA pin.

        Both native gate windows must match the audited bytes regardless of
        whether a whole-image SHA is supplied. The actual SHA is always reported.
        """
        path = Path(path)
        data = path.read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        if expected_sha256 is not None:
            require(digest == expected_sha256.lower(), f'image SHA mismatch: {digest}')
        require(data[:2] == b'MZ', 'not an MZ image')
        pe = struct.unpack_from('<I', data, 0x3C)[0]
        require(data[pe:pe+4] == b'PE\0\0', 'not a PE image')
        require(struct.unpack_from('<H', data, pe+4)[0] == 0x14C, 'not x86')
        opt = pe + 24
        require(struct.unpack_from('<H', data, opt)[0] == 0x10B, 'not PE32')
        base = struct.unpack_from('<I', data, opt+28)[0]
        require(base == 0x400000, 'unexpected preferred image base')
        table = opt + struct.unpack_from('<H', data, pe+20)[0]
        sections = []
        for i in range(struct.unpack_from('<H', data, pe+6)[0]):
            _, rva, size, raw = struct.unpack_from('<IIII', data, table+i*40+8)
            require(raw+size <= len(data), 'section exceeds disk image')
            sections.append((base+rva, size, raw))
        image = cls(data, digest, str(path), tuple(sections))
        for name, gate in GATES.items():
            expected = bytes.fromhex(gate['bytes'])
            require(image.read(gate['entry'], len(expected)) == expected,
                    f'{name} gate differs from audited v132 instructions')
        return image

    def read(self, va: int, length: int) -> bytes:
        for start, size, raw in self.sections:
            if start <= va and va+length <= start+size:
                return self.data[raw+va-start:raw+va-start+length]
        raise ValueError(f'unmapped disk image VA {va:#x}, length={length}')


def _states(values: Iterable[int]) -> list[int]:
    values = list(values)
    require(len(values) <= 128, 'synthetic state vector exceeds 128 elements')
    require(all(type(v) is int and 0 <= v <= 0xFFFFFFFF for v in values),
            'state values must be uint32 integers')
    return values


def run_gate(gate: str, source_states: Iterable[int],
             target_states: Iterable[int] = (), *, image: NativeImage,
             same_container: bool = False) -> dict:
    """Execute one native gate in a fresh, isolated Unicorn VM.

    source_states/target_states describe the caller's states AFTER any separately
    tested candidate cleanup. No cleanup is performed here. For same_container
    drag, target_states must equal source_states. This also exercises ordinary
    within-inventory moves where source and target object pointers alias.
    """
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE
    from unicorn.x86_const import (
        UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
        UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
        UC_X86_REG_EIP, UC_X86_REG_EFLAGS,
    )
    require(gate in GATES, f'unknown gate: {gate}')
    source = _states(source_states)
    target = _states(target_states)
    require(gate == 'drag' or (not target and not same_container),
            'target/alias options apply to drag only')
    require(not same_container or source == target, 'aliased container states differ')
    spec = GATES[gate]
    native = image.read(spec['entry'], len(bytes.fromhex(spec['bytes'])))
    require(native == bytes.fromhex(spec['bytes']), 'native gate bytes changed')
    vm = Uc(UC_ARCH_X86, UC_MODE_32)
    code_pages = {spec['entry'] & ~0xFFF,
                  spec['continue'] & ~0xFFF, spec['blocked'] & ~0xFFF}
    for page in sorted(code_pages):
        vm.mem_map(page, 0x1000)
    vm.mem_write(spec['entry'], native)  # Exact disk bytes, no instruction patches.
    memory = 0x10000000
    vm.mem_map(memory, 0x4000)
    vm.mem_write(memory, bytes([0xA5])*0x4000)
    src_obj, dst_obj = memory+0x100, memory+0x300
    src_data, dst_data = memory+0x1000, memory+0x1800

    def install(obj, vector, values):
        # +68 iterator-proxy field isn't read by these gates; retain its canary.
        vm.mem_write(obj+0x6C, struct.pack('<III', vector, vector+4*len(values), vector+512))
        if values:
            vm.mem_write(vector, struct.pack('<'+'I'*len(values), *values))

    install(src_obj, src_data, source)
    if same_container:
        dst_obj, dst_data = src_obj, src_data
    else:
        install(dst_obj, dst_data, target)
    registers = {
        UC_X86_REG_EAX: 0xA1A1A1A1, UC_X86_REG_ECX: 0xC1C1C1C1,
        UC_X86_REG_EDX: 0xD1D1D1D1, UC_X86_REG_ESI: src_obj,
        UC_X86_REG_EDI: src_obj, UC_X86_REG_EBX: dst_obj,
        UC_X86_REG_ESP: memory+0x3F00, UC_X86_REG_EBP: memory+0x3F80,
        UC_X86_REG_EFLAGS: 0x202,
    }
    for reg, value in registers.items():
        vm.reg_write(reg, value)
    before = bytes(vm.mem_read(memory, 0x4000))
    trace, writes = [], []
    outcomes = {spec['continue']: 'continue', spec['blocked']: 'blocked'}
    result = {}

    def on_code(uc, address, size, _):
        if address in outcomes:
            result['outcome'] = outcomes[address]
            uc.emu_stop()  # Stop BEFORE either destination's instructions.
            return
        require(address in spec['boundaries'], f'unexpected execution {address:#x}')
        trace.append(address)

    def on_write(uc, access, address, size, value, _):
        writes.append({'address': hex(address), 'size': size, 'value': value})
        uc.emu_stop()

    vm.hook_add(UC_HOOK_CODE, on_code)
    vm.hook_add(UC_HOOK_MEM_WRITE, on_write)
    vm.emu_start(spec['entry'], 0xFFFFFFFF, count=32)
    require(not writes, f'gate unexpectedly wrote memory: {writes}')
    require('outcome' in result, 'gate did not reach an audited branch destination')
    require(bytes(vm.mem_read(memory, 0x4000)) == before, 'synthetic memory changed')
    for reg in (UC_X86_REG_EBX, UC_X86_REG_EDI, UC_X86_REG_ESI,
                UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EDX):
        require(vm.reg_read(reg) == registers[reg], 'unexpected preserved register change')
    require(bytes(vm.mem_read(spec['entry'], len(native))) == native, 'code changed')
    return {
        'gate': gate, 'source_states': source, 'target_states': target if gate == 'drag' else None,
        'same_container': same_container, 'outcome': result['outcome'],
        'blocked': result['outcome'] == 'blocked',
        'stop_va': hex(vm.reg_read(UC_X86_REG_EIP)),
        'trace': [hex(a) for a in trace], 'native_bytes_sha256': hashlib.sha256(native).hexdigest(),
        'state_and_headers_unchanged': True, 'memory_writes': writes,
        'image_sha256': image.sha256,
    }


def regression(image: NativeImage) -> dict:
    # Explicit expectations include 0 and empty-top sentinel 0x6f: only LENGTH
    # governs these windows, never a special exemption for marker 7 or 4.
    fixtures = [([], False), ([7], True), ([4], True), ([7, 4], True),
                ([4, 7], True), ([7, 7], True), ([4, 4], True),
                ([0], True), ([0x6F], True)]
    cases = []
    for source, blocked in fixtures:
        case = run_gate('use', source, image=image)
        require(case['blocked'] == blocked, f'use expectation failed: {source}')
        cases.append(case)
    for source, source_busy in fixtures:
        for target, target_busy in fixtures:
            case = run_gate('drag', source, target, image=image)
            require(case['blocked'] == (source_busy or target_busy),
                    f'drag expectation failed: {source}, {target}')
            # A source rejection must short circuit the target check.
            require(('0x2600d75' in case['trace']) == (not source_busy),
                    'drag target check did not short circuit as expected')
            cases.append(case)
    for states, blocked in fixtures:
        case = run_gate('drag', states, states, image=image, same_container=True)
        require(case['blocked'] == blocked, f'aliased drag expectation failed: {states}')
        cases.append(case)
    return {
        'passed': True, 'scope': __doc__, 'image': {'sha256': image.sha256},
        'counts': {'use': 9, 'drag_cross_product': 81, 'drag_aliased': 9, 'total': len(cases)},
        'limitations': [
            'Only audited native gate windows execute; their branch destinations are not executed.',
            'Synthetic container states; no live client or actual UI acceptance.',
            'No candidate cleanup, protected helper, guard, stack-balance or epilogue validation.',
            'All source/target state vectors are preserved, including [4], [7,4] and [4,7].',
        ],
        'cases': cases,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', type=Path, required=True, help='Disk PE32 image; never launched')
    parser.add_argument('--expected-sha256', help='Optional whole-image SHA-256 pin')
    parser.add_argument('--deps', type=Path, help='Optional directory containing the unicorn package')
    parser.add_argument('--output', type=Path, default=ROOT/'native-gate-regression.json')
    args = parser.parse_args()
    if args.deps:
        require(args.deps.is_dir(), 'dependency directory does not exist')
        sys.path.insert(0, str(args.deps.resolve()))
    image = NativeImage.from_path(args.image, expected_sha256=args.expected_sha256)
    report = regression(image)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
    print(json.dumps({'passed': report['passed'], 'counts': report['counts'],
                      'output': str(args.output), 'image_sha256': image.sha256}))


if __name__ == '__main__':
    main()
