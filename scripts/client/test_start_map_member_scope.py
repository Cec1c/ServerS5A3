"""Exercise the verified client's START_MAP member-room binding window.

The native selector branch, eight-slot traversal, actor accessors and room
setter run unmodified. The one-byte packet reader and coordinate-to-room
lookup are explicit fixtures. Rendering, networking and game startup do not
run.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

CLIENT_INPUTS = {
    '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24': 112541696,
    'b63d35bb28ba70353af5388cdaea4f2f8294cacc5064fcbb31db175519a02783': 112549888,
}
NATIVE_START = 0x01FF9993
NATIVE_STOP = 0x01FF9A64
NATIVE_WINDOW = ('0x01FF9993', '0x01FF9A64')

# These are the only two call sites replaced by fixtures in the native window.
PACKET_READER_CALL = 0x037E82E0
ROOM_LOOKUP_CALL = 0x026230D0

SCRATCH_BASE = 0x20000000
SCRATCH_SIZE = 0x00100000
STACK = 0x200F0000
MANAGER = 0x20010000
ROOM = 0x20020000
ACTOR_BASE = 0x20030000
ROOM_TABLE_BASE = 0x20040000
PRELOADED_ADDRESS = 0x20050000
MANAGER_POINTER = 0x056A3768

SLOT_SETS = ((0, 1), (0, 1, 2), (0, 1, 2, 3), (0, 2, 3))
EXPECTED_CASE_COUNT = 32
FIXTURES = (
    'one-byte START_MAP member selector reader',
    'coordinate-to-room lookup',
)


def read_u32(uc, address):
    return struct.unpack('<I', uc.mem_read(address, 4))[0]


def write_u32(uc, address, value):
    uc.mem_write(address, struct.pack('<I', value))


def return_from_call(uc, value=0, stack_args=0):
    from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EIP, UC_X86_REG_ESP

    esp = uc.reg_read(UC_X86_REG_ESP)
    return_address = read_u32(uc, esp)
    uc.reg_write(UC_X86_REG_EAX, value)
    uc.reg_write(UC_X86_REG_EIP, return_address)
    uc.reg_write(UC_X86_REG_ESP, esp + 4 + stack_args)


def load_verified_client(source):
    source = Path(source)
    try:
        data = source.read_bytes()
    except OSError as exc:
        raise ValueError(f'unable to read client executable {source}: {exc}') from exc
    digest = hashlib.sha256(data).hexdigest()
    if CLIENT_INPUTS.get(digest) != len(data):
        raise ValueError(
            f'client SHA256/size is not an exact verified input: {digest}/{len(data)}'
        )
    return data


def execute_native_window(data):
    import pefile
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
    from unicorn.x86_const import (
        UC_X86_REG_EAX,
        UC_X86_REG_EBX,
        UC_X86_REG_ECX,
        UC_X86_REG_EDX,
        UC_X86_REG_EBP,
        UC_X86_REG_EDI,
        UC_X86_REG_EIP,
        UC_X86_REG_EFLAGS,
        UC_X86_REG_ESI,
        UC_X86_REG_ESP,
    )

    pe = pefile.PE(data=data, fast_load=True)
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    image_base = int(pe.OPTIONAL_HEADER.ImageBase)
    image_size = (int(pe.OPTIONAL_HEADER.SizeOfImage) + 0xFFF) & ~0xFFF
    uc.mem_map(image_base, image_size)
    for section in pe.sections:
        uc.mem_write(
            image_base + int(section.VirtualAddress),
            section.get_data(),
        )
    uc.mem_map(SCRATCH_BASE, SCRATCH_SIZE)

    state = {}

    def hook(running, address, size, user_data):
        del size, user_data
        if address == PACKET_READER_CALL:
            esp = running.reg_read(UC_X86_REG_ESP)
            if read_u32(running, esp + 8) != 1:
                raise AssertionError('packet reader fixture expected a one-byte read')
            running.mem_write(
                read_u32(running, esp + 4),
                bytes((state['selector'],)),
            )
            return_from_call(running, 1)
        elif address == ROOM_LOOKUP_CALL:
            return_from_call(running, state['room'], stack_args=8)
        elif address == NATIVE_STOP:
            running.emu_stop()

    uc.hook_add(UC_HOOK_CODE, hook)
    rows = []
    for slots in SLOT_SETS:
        for selector in list(slots) + [255]:
            for missing in (None, slots[-1]):
                write_u32(uc, MANAGER_POINTER, MANAGER)
                for slot in range(8):
                    actor = ACTOR_BASE + slot * 0x1000
                    model = actor + 0x800
                    present = slot in slots and slot != missing
                    write_u32(uc, MANAGER + 0x48 + slot * 4, actor if present else 0)
                    write_u32(uc, actor + 0x4F8, model)
                    write_u32(uc, model + 0xD4, ROOM_TABLE_BASE + slot * 0x10)

                state.update(selector=selector, room=ROOM)
                for register in (
                    UC_X86_REG_EAX,
                    UC_X86_REG_EBX,
                    UC_X86_REG_ECX,
                    UC_X86_REG_EDX,
                    UC_X86_REG_ESI,
                ):
                    uc.reg_write(register, 0)
                uc.reg_write(UC_X86_REG_EBP, STACK)
                uc.reg_write(UC_X86_REG_ESP, STACK - 0x800)
                uc.reg_write(UC_X86_REG_EDI, ROOM)
                uc.reg_write(UC_X86_REG_EFLAGS, 2)
                uc.mem_write(STACK - 0x412, b'\x00\x00')
                write_u32(uc, STACK - 0x420, PRELOADED_ADDRESS)
                uc.emu_start(NATIVE_START, NATIVE_STOP, count=1500)
                if uc.reg_read(UC_X86_REG_EIP) != NATIVE_STOP:
                    raise AssertionError('native window did not stop at its exact end')

                changed = [
                    slot
                    for slot in range(8)
                    if read_u32(uc, ACTOR_BASE + slot * 0x1000 + 0x800 + 0xD4) == ROOM
                ]
                if selector == 255 or selector == missing:
                    wanted = [slot for slot in slots if slot != missing]
                else:
                    wanted = [selector]
                if changed != wanted:
                    raise AssertionError(
                        f'slots={slots}, selector={selector}, missing={missing}: '
                        f'bound={changed}, wanted={wanted}'
                    )
                rows.append(
                    {
                        'slots': slots,
                        'selector': selector,
                        'missing': missing,
                        'boundSlots': changed,
                    }
                )

    if len(rows) != EXPECTED_CASE_COUNT:
        raise AssertionError(
            f'expected {EXPECTED_CASE_COUNT} cases, produced {len(rows)}'
        )
    return {
        'exeSha256': hashlib.sha256(data).hexdigest(),
        'nativeWindow': list(NATIVE_WINDOW),
        'fixtures': list(FIXTURES),
        'cases': rows,
        'passed': True,
        'liveGame': False,
        'limits': [
            'executes only the member-room selector and room-setter native window',
            'packet parsing uses a one-byte selector fixture',
            'coordinate-to-room lookup is a fixture',
            'rendering, networking, persistence and game startup are not executed',
            'no player memory or live game process is accessed',
        ],
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        '--exe',
        required=True,
        type=Path,
        help='client executable with the exact verified SHA256',
    )
    parser.add_argument(
        '--output',
        required=True,
        type=Path,
        help='JSON report path',
    )
    args = parser.parse_args(argv)
    try:
        data = load_verified_client(args.exe)
    except ValueError as exc:
        parser.error(str(exc))

    result = execute_native_window(data)
    try:
        args.output.write_text(json.dumps(result, indent=2), encoding='utf8')
    except OSError as exc:
        parser.error(f'unable to write report {args.output}: {exc}')
    print(
        json.dumps(
            {
                'exeSha256': result['exeSha256'],
                'cases': len(result['cases']),
                'passed': result['passed'],
            }
        )
    )


if __name__ == '__main__':
    main()
