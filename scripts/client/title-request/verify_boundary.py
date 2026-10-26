"""Boundary ABI test for the title claim entry (return 0x24A288E).

Two entry modes, both explicit:

  real      start at the actual CALL (ret-5), verify its bytes target 0x25F9A70,
            let the CPU push the real return address, run the real prologue,
            then jump to the real boundary 0x25FA98D.
  synthetic no real caller exists (unknown return / null frame): start directly
            at 0x25FA98D with a frame that is marked as synthetic in the
            report. Nothing claims this is a real caller.

From the boundary on, every case is the same real path: thunk -> real getter
0x374EA10 ("mov eax,[0x57ED8D4]; ret", no argument cleanup) -> real writer
0x374F390 ("ret 4", one byte) -> one stubbed leaf 0x38C7F70 (memory append,
ret 8) whose (pointer,count) arguments become the byte tape.

At CONTINUE the marker must still be on the stack (ESP == boundary_esp-4) and
after the writer the stack must be back to boundary_esp, with a one-byte tape
equal to the marker.

Status: authored but NOT executed.

    python -B verify_boundary.py --v177 <V177.exe> --output <report.json>
"""
import argparse
import json
import struct
from pathlib import Path

from unicorn import Uc, UC_ARCH_X86, UC_HOOK_CODE, UC_MODE_32
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
    UC_X86_REG_EFLAGS, UC_X86_REG_EIP, UC_X86_REG_EBP, UC_X86_REG_EDI,
    UC_X86_REG_ESI, UC_X86_REG_ESP,
)

from pe_support import Image, available_memory, pe_checksum, sha256
from title_extension import CONTINUE, OLD_VIRTUAL_SIZE, OP19UI, title_extension

V177_SHA256 = '027eca60196f8e22b7d05a9cad4ae37fe0098ab48e3048714b8ceb9270f5e752'
V177_SIZE = 111919104

PRODUCER = 0x25F9A70
BOUNDARY = 0x25FA98D
PROLOGUE_STOP = 0x25F9A73
GETTER = 0x374EA10
WRITER_GLOBAL = 0x57ED8D4
WRITER_U8 = 0x374F390
APPEND_LEAF = 0x38C7F70
AFTER_WRITER = 0x25FA99B
CLAIM_CALL_SITE = 0x24A2889
CLAIM_RETURN = 0x24A288E

STACK = 0x10000000
FRAME = 0x11000000
PARENT_FRAME = 0x11001000
FAKE_WRITER = 0x13000000
FAKE_SIZE = (0x2BCC40 + 0x1FFF) & ~0xFFF  # mem_map requires a page-aligned size
STOP = 0x12000000

# name, return, parent, slot, count, arg4, before, after, mode, null_frame
CASES = (
    ('simple-2499E10', 0x2499E10, 0xA5A50000, 13, 1, 0, 2, 2, 'real', False),
    ('legacy-12A3479-parent-AEF1B', 0x12A3479, 0x12AEF1B, 13, 1, 2, 2, 2, 'real', False),
    ('legacy-12AB199-parent-AE884', 0x12AB199, 0x12AE884, 13, 1, 2, 2, 2, 'real', False),
    ('legacy-12ACB30-parent-AE59F', 0x12ACB30, 0x12AE59F, 13, 1, 2, 2, 2, 'real', False),
    ('legacy-12ACB7D-parent-AE59F', 0x12ACB7D, 0x12AE59F, 13, 1, 0, 2, 2, 'real', False),
    ('legacy-12AF91D-noParent', 0x12AF91D, 0xA5A50000, 13, 1, 2, 2, 2, 'real', False),
    ('claim-24A288E-positive', CLAIM_RETURN, 0xA5A50000, 13, 1, 0, 0, 2, 'real', False),
    ('claim-24A288E-nullFrame', CLAIM_RETURN, 0x0, 13, 1, 0, 0, 2, 'synthetic', True),
    ('claim-24A288E-wrongSlot12', CLAIM_RETURN, 0xA5A50000, 12, 1, 0, 0, 0, 'real', False),
    ('claim-24A288E-wrongCount2', CLAIM_RETURN, 0xA5A50000, 13, 2, 0, 0, 0, 'real', False),
    ('claim-24A288E-wrongArg4', CLAIM_RETURN, 0xA5A50000, 13, 1, 2, 0, 0, 'real', False),
    ('uncovered-25FA396', 0x25FA396, 0xA5A50000, 13, 1, 0, 0, 0, 'real', False),
    ('uncovered-25FA493', 0x25FA493, 0xA5A50000, 13, 1, 0, 0, 0, 'real', False),
    ('uncovered-25FCC38', 0x25FCC38, 0xA5A50000, 13, 1, 0, 0, 0, 'real', False),
    ('uncovered-25FCCFC', 0x25FCCFC, 0xA5A50000, 13, 1, 0, 0, 0, 'real', False),
    ('unknown-1FDF58D', 0x1FDF58D, 0xA5A50000, 13, 1, 0, 0, 0, 'synthetic', False),
    ('claim-24A288E-slot26', CLAIM_RETURN, 0xA5A50000, 26, 1, 0, 0, 0, 'real', False),
)
SENTINELS = {
    'ebx': (UC_X86_REG_EBX, 0x11112222),
    'ecx': (UC_X86_REG_ECX, 0x22223333),
    'edx': (UC_X86_REG_EDX, 0x33334444),
    'esi': (UC_X86_REG_ESI, 0x44445555),
    'edi': (UC_X86_REG_EDI, 0x55556666),
}


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def inject(data, *, baseline_sha256=V177_SHA256):
    require(baseline_sha256 in (V177_SHA256, 'eae47c4b805c8cc6d7c81c26380fe78785968f50ddcd9aeff4e12047f9ab00ac'),
            'unsupported published baseline')
    image = Image.from_bytes(data)
    require(image.sha256 == baseline_sha256 and len(data) == V177_SIZE,
            'exact selected published image required')
    va = OP19UI + OLD_VIRTUAL_SIZE
    extension = title_extension(va)
    section = image.section('.op19ui')
    require(section['va'] == OP19UI and section['raw_size'] == 0x1000,
            'unexpected .op19ui section')
    require(image.bytes_at(0x6E95050, 5) ==
            b'\xe9' + struct.pack('<i', va - 0x6E95050 - 5),
            'fallback does not target the extension')
    require(image.bytes_at(GETTER, 6) ==
            b'\xa1' + struct.pack('<I', WRITER_GLOBAL) + b'\xc3',
            'getter is not mov eax,[global]; ret')
    require(image.bytes_at(BOUNDARY, 5) ==
            b'\xe9' + struct.pack('<i', OP19UI - BOUNDARY - 5),
            'boundary is not jmp .op19ui')
    require(image.bytes_at(APPEND_LEAF, 1) == b'\xe9',
            'append leaf is not a jmp thunk')
    out = bytearray(data)
    offset = image.file_offset(va, len(extension))
    out[offset:offset + len(extension)] = extension
    struct.pack_into('<I', out, section['header'] + 8, OLD_VIRTUAL_SIZE + len(extension))
    struct.pack_into('<I', out, image.optional + 64, pe_checksum(out, image.optional + 64))
    return bytes(out)


def run_case(image, ret, parent, slot, count, arg4, mode, null_frame):
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    mapped = (image.size_of_image + 0xFFF) & ~0xFFF
    uc.mem_map(0x400000, mapped)
    for section in image.sections:
        if section['raw_size']:
            uc.mem_write(section['va'],
                         image.data[section['raw']:section['raw'] + section['raw_size']])
    uc.mem_map(STACK, 0x10000)
    uc.mem_map(FRAME, 0x10000)
    uc.mem_map(FAKE_WRITER, FAKE_SIZE)
    uc.mem_map(STOP, 0x1000)

    def put(address, value):
        uc.mem_write(address, struct.pack('<I', value))

    put(WRITER_GLOBAL, FAKE_WRITER)
    uc.mem_write(FAKE_WRITER + 8, b'\x01')
    put(FAKE_WRITER + 0x2BCC2C, 0)

    for displacement, value in ((0x8, 0x10), (0xC, slot), (0x10, count),
                                (0x14, arg4), (0x18, 0)):
        put(FRAME + displacement, value)
    put(FRAME + 4, ret)
    put(FRAME, 0 if null_frame else PARENT_FRAME)
    put(PARENT_FRAME + 4, parent)

    stack_top = STACK + 0x8000
    if mode == 'real':
        call_site = ret - 5
        require(image.bytes_at(call_site, 5) ==
                b'\xe8' + struct.pack('<i', PRODUCER - call_site - 5),
                '%#x is not a real producer CALL' % call_site)
        for offset, value in enumerate((0x10, slot, count, arg4, 0)):
            put(stack_top + offset * 4, value)
        uc.reg_write(UC_X86_REG_ESP, stack_top)
        uc.reg_write(UC_X86_REG_EBP, PARENT_FRAME)
        expected_esp = stack_top - 8
        start = call_site
        esp_at_boundary = expected_esp
    elif mode == 'synthetic':
        uc.reg_write(UC_X86_REG_ESP, stack_top)
        uc.reg_write(UC_X86_REG_EBP, FRAME)
        expected_esp = FRAME
        start = BOUNDARY
        esp_at_boundary = stack_top
    else:
        raise AssertionError('unknown mode %r' % mode)

    uc.reg_write(UC_X86_REG_EFLAGS, 0x246)
    for _, (register, value) in SENTINELS.items():
        uc.reg_write(register, value)
    uc.reg_write(UC_X86_REG_EAX, 0x66778899)

    state = {'path': [], 'tape': bytearray(), 'continue_esp': None,
             'continue_top': None}

    def read32(address):
        return struct.unpack('<I', uc.mem_read(address, 4))[0]

    def on_code(machine, address, size, _):
        if address == PROLOGUE_STOP:
            require(mode == 'real', 'prologue reached in synthetic mode')
            frame = machine.reg_read(UC_X86_REG_EBP)
            require(frame == expected_esp, 'producer frame not installed')
            require(read32(frame + 4) == ret, 'CPU saved return differs')
            machine.reg_write(UC_X86_REG_EIP, BOUNDARY)
        elif address == BOUNDARY:
            state['path'].append('boundary')
            require(machine.reg_read(UC_X86_REG_EBP) == expected_esp,
                    'boundary frame differs')
            require(machine.reg_read(UC_X86_REG_ESP) == esp_at_boundary,
                    'boundary stack differs')
        elif address == OP19UI:
            state['path'].append('thunk')
        elif address == CONTINUE:
            state['path'].append('continue')
            esp = machine.reg_read(UC_X86_REG_ESP)
            state['continue_esp'] = esp
            state['continue_top'] = read32(esp)
            state['flags'] = machine.reg_read(UC_X86_REG_EFLAGS)
            state['sentinels'] = {name: machine.reg_read(register)
                                  for name, (register, _) in SENTINELS.items()}
        elif address == GETTER:
            pass  # real getter
        elif address == APPEND_LEAF:
            esp = machine.reg_read(UC_X86_REG_ESP)
            pointer = read32(esp + 4)
            length = read32(esp + 8)
            require(0 < length <= 16, 'unexpected append length %d' % length)
            state['tape'].extend(machine.mem_read(pointer, length))
            machine.reg_write(UC_X86_REG_EAX, 0)
            machine.reg_write(UC_X86_REG_ESP, esp + 12)  # ret 8
            machine.reg_write(UC_X86_REG_EIP, read32(esp))
        elif address == AFTER_WRITER:
            machine.emu_stop()

    uc.hook_add(UC_HOOK_CODE, on_code)
    uc.emu_start(start, STOP, count=20000, timeout=3_000_000)

    require(state['path'] == ['boundary', 'thunk', 'continue'],
            'path %r' % (state['path'],))
    require(state['continue_esp'] == esp_at_boundary - 4,
            'marker not left on the stack at continue')
    require(uc.reg_read(UC_X86_REG_ESP) == esp_at_boundary,
            'writer did not consume exactly one dword argument')
    require(state['flags'] == 0x246, 'EFLAGS changed by the thunk')
    for name, (_, expected) in SENTINELS.items():
        require(state['sentinels'][name] == expected,
                '%s changed by the thunk' % name)
    return {'marker': state['continue_top'], 'tape': list(state['tape']),
            'continue_esp': state['continue_esp'], 'path': state['path'],
            'mode': mode, 'call_site': (hex(ret - 5) if mode == 'real' else None)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--v177', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    available_memory()
    source = args.v177.read_bytes()
    baseline = Image.from_bytes(source)
    candidate_bytes = inject(source)
    patched = Image.from_bytes(candidate_bytes)
    rows = []
    for name, ret, parent, slot, count, arg4, before, after, mode, null_frame in CASES:
        got_before = run_case(baseline, ret, parent, slot, count, arg4, mode, null_frame)
        got_after = run_case(patched, ret, parent, slot, count, arg4, mode, null_frame)
        require(got_before['marker'] == before,
                '%s baseline marker %r' % (name, got_before['marker']))
        require(got_after['marker'] == after,
                '%s candidate marker %r' % (name, got_after['marker']))
        require(got_before['tape'] == [before & 0xFF],
                '%s baseline tape %r' % (name, got_before['tape']))
        require(got_after['tape'] == [after & 0xFF],
                '%s candidate tape %r' % (name, got_after['tape']))
        rows.append({'name': name, 'return': hex(ret), 'slot': slot,
                     'count': count, 'arg4': arg4, 'mode': mode,
                     'nullFrame': null_frame, 'before': got_before,
                     'after': got_after})
    report = {
        'schema': 'title-new-entry-boundary.v3',
        'executed': True,
        'inputSha256': sha256(source),
        'candidateSha256': sha256(candidate_bytes),
        'candidateExtensionBytes': len(title_extension(OP19UI + OLD_VIRTUAL_SIZE)),
        'cases': rows,
        'abi': {'getter': hex(GETTER), 'getterCleanup': 0,
                'writer': hex(WRITER_U8), 'writerBytes': 1, 'writerCleanup': 4,
                'appendLeaf': hex(APPEND_LEAF), 'appendLeafCleanup': 8},
        'claims': {'onlyStub': hex(APPEND_LEAF),
                   'syntheticCases': [r['name'] for r in rows if r['mode'] == 'synthetic'],
                   'note': 'Tape must be exactly one byte equal to the marker.'},
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + '\n', encoding='utf8')
    print(json.dumps({'cases': len(rows)}))


if __name__ == '__main__':
    main()
