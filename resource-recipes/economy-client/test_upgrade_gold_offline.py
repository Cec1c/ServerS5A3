#!/usr/bin/env python3
"""Run 2160 isolated Unicorn cases against a private upgrade-gold candidate.

This is an offline emulator test.  The candidate image is parsed and selected
sections are mapped into Unicorn; the operating system never executes it.  The
protected-int accessors, window getter, and scene objects are contract stubs.
This is not a game-process, full native pipeline, network, or production test.

Dependencies: Python 3.8+, pefile, and unicorn.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib
import json
import re
import struct
import tempfile
from pathlib import Path


GETTERS = (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0)
SETTERS = (0x37DF130, 0x37DF1B0, 0x37DF230, 0x37DF2B0, 0x37DF010)
WINDOW_GETTER = 0x32BAC90
FAKE_HOOK_RETURN = 0x3A3DAF1
FAKE_EMULATION_RETURN = 0x10000000
PAYLOAD_SECTIONS = (b".fe012c", b".fe012r", b".fe012d")
MAP_SYMBOLS = ("PrivateUpgradeGold", "PrivateUpgradeGoldGate")
PUBLIC_LINE = re.compile(
    r"^\s*(?P<section>[0-9A-Fa-f]+):(?P<offset>[0-9A-Fa-f]+)\s+"
    r"(?P<name>\S+)\s+(?P<address>[0-9A-Fa-f]+)\s+"
)
PREFERRED_LOAD = re.compile(
    r"^\s*Preferred load address is\s+(?P<address>[0-9A-Fa-f]+)\s*$",
    re.IGNORECASE | re.MULTILINE,
)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def read_map(path: Path) -> tuple[str, dict[str, int], int]:
    data = path.read_bytes()
    try:
        text = data.decode("utf-8-sig")
    except UnicodeDecodeError:
        text = data.decode("cp1252")

    preferred = PREFERRED_LOAD.findall(text)
    assert len(preferred) == 1, "build map must contain one preferred load address"
    preferred_address = int(preferred[0], 16)
    exports: dict[str, int] = {}
    for line in text.splitlines():
        match = PUBLIC_LINE.match(line)
        if not match:
            continue
        name = match.group("name")
        bare_name = name[1:] if name.startswith("_") else name
        if bare_name not in MAP_SYMBOLS:
            continue
        section = int(match.group("section"), 16)
        if section == 0:
            continue
        offset = int(match.group("offset"), 16)
        address = int(match.group("address"), 16)
        assert section == 1 and address == preferred_address + 0x1000 + offset, (
            "build map symbol address disagrees with its section offset",
            bare_name,
            hex(address),
        )
        previous = exports.get(bare_name)
        assert previous is None or previous == address, (
            "build map contains conflicting symbol addresses",
            bare_name,
        )
        exports[bare_name] = address

    assert set(exports) == set(MAP_SYMBOLS), (
        "build map lacks required private upgrade symbols",
        sorted(set(MAP_SYMBOLS) - set(exports)),
    )
    for name, address in exports.items():
        assert address < 0x100000000, ("build map symbol is outside the x86 address space", name)
    return sha256(data), exports, preferred_address


def load_dependencies(parser: argparse.ArgumentParser):
    try:
        pefile = importlib.import_module("pefile")
        unicorn = importlib.import_module("unicorn")
        x86 = importlib.import_module("unicorn.x86_const")
    except ImportError as exc:
        parser.error(
            "missing Python dependency: "
            f"{exc.name or exc}. Install pefile and unicorn in the active Python environment."
        )
    return pefile, unicorn, x86


def run_case(
    unicorn_module,
    x86,
    pe,
    exports: dict[str, int],
    kind: str,
    fee: int,
    alignment: int,
    scene: str,
    gate: bool,
):
    image_base = pe.OPTIONAL_HEADER.ImageBase
    emulator = unicorn_module.Uc(unicorn_module.UC_ARCH_X86, unicorn_module.UC_MODE_32)
    mapped = set()
    calls = []

    def put(address: int, data: bytes) -> None:
        first_page = address & ~4095
        final_page = (address + len(data) + 4095) & ~4095
        for page in range(first_page, final_page, 4096):
            if page not in mapped:
                emulator.mem_map(page, 4096)
                mapped.add(page)
        emulator.mem_write(address, data)

    def write_u32(address: int, value: int) -> None:
        put(address, struct.pack("<I", value & 0xFFFFFFFF))

    def read_u32(address: int) -> int:
        return struct.unpack("<I", emulator.mem_read(address, 4))[0]

    for section in pe.sections:
        if section.Name.rstrip(b"\0") in PAYLOAD_SECTIONS:
            put(image_base + section.VirtualAddress, section.get_data())

    for address in GETTERS + SETTERS + (WINDOW_GETTER, FAKE_HOOK_RETURN, FAKE_EMULATION_RETURN):
        put(address, b"\xc3")

    stack = 0x11000000
    frame = stack + 0x1000
    stack_pointer = stack + 0x800
    result = 0x12000100 + alignment
    put(stack, bytes(0x2000))
    put(result, b"\xa5" * 0x200)
    write_u32(result + 0xA0, fee)

    holder = 0x13000000
    amplifier = 0x14000000
    manager = 0x15000000
    window = 0x16000000
    npc = 0x17000000
    write_u32(0x56CA6F8, holder if kind != "uninitialized" else 0)
    write_u32(holder, amplifier)
    write_u32(0x56A3798, 0 if scene == "no_manager" else manager)
    write_u32(window + 0x1F4, npc if scene in ("ordinary", "conversion") else 0)
    write_u32(npc + 0x6A8, 1 if scene == "conversion" else 0)

    table = 0x56C8B70 if kind == "reinforce" else amplifier if kind == "amplify" else 0x56C9598
    write_u32(frame - 0x38, table)
    write_u32(frame + 0xC, result)
    before = bytes(emulator.mem_read(result, 0x200))

    registers = {
        x86.UC_X86_REG_EAX: 0x12345678,
        x86.UC_X86_REG_EBX: 0x23456789,
        x86.UC_X86_REG_ECX: 0x34567890,
        x86.UC_X86_REG_EDX: 0x45678901,
        x86.UC_X86_REG_ESI: 0x56789012,
        x86.UC_X86_REG_EDI: 0x67890123,
        x86.UC_X86_REG_EBP: frame,
    }
    for register, value in registers.items():
        emulator.reg_write(register, value)
    emulator.reg_write(x86.UC_X86_REG_ESP, stack_pointer)
    emulator.reg_write(x86.UC_X86_REG_EFLAGS, 0x246)

    entry = exports["PrivateUpgradeGoldGate" if gate else "PrivateUpgradeGold"]
    stop = FAKE_HOOK_RETURN if gate else FAKE_EMULATION_RETURN
    if not gate:
        write_u32(stack_pointer, stop)
        write_u32(stack_pointer + 4, table)
        write_u32(stack_pointer + 8, result)

    def hook(uc, address, size, user_data):
        del size, user_data
        if address == stop:
            uc.emu_stop()
            return
        stack_address = uc.reg_read(x86.UC_X86_REG_ESP)
        cell = uc.reg_read(x86.UC_X86_REG_ECX)
        if address in GETTERS or address in SETTERS:
            index = alignment // 4 if alignment % 4 == 0 else 4
            assert address == (GETTERS[index] if address in GETTERS else SETTERS[index])
            assert cell == result + 0xA0
            if address in GETTERS:
                assert read_u32(stack_address + 4) == cell + 4
                write_u32(read_u32(stack_address + 8), read_u32(cell))
                calls.append("get")
            else:
                assert read_u32(stack_address + 8) == cell + 4
                write_u32(cell, read_u32(read_u32(stack_address + 4)))
                calls.append("set")
            uc.reg_write(x86.UC_X86_REG_ESP, stack_address + 12)
            uc.reg_write(x86.UC_X86_REG_EIP, read_u32(stack_address))
            return
        if address == WINDOW_GETTER:
            assert cell == manager
            uc.reg_write(x86.UC_X86_REG_EAX, 0 if scene == "no_window" else window)
            uc.reg_write(x86.UC_X86_REG_ESP, stack_address + 4)
            uc.reg_write(x86.UC_X86_REG_EIP, read_u32(stack_address))

    emulator.hook_add(unicorn_module.UC_HOOK_CODE, hook)
    emulator.emu_start(entry, 0, count=3000)
    assert emulator.reg_read(x86.UC_X86_REG_EIP) == stop

    scale = kind in ("reinforce", "amplify") and not (
        kind == "reinforce" and scene == "conversion"
    )
    expected = (fee // 7 + (fee % 7 != 0)) if scale and fee > 0 else fee
    assert read_u32(result + 0xA0) == expected & 0xFFFFFFFF, (
        kind,
        fee,
        alignment,
        scene,
        gate,
        read_u32(result + 0xA0),
        expected,
    )
    after = bytes(emulator.mem_read(result, 0x200))
    assert before[:0xA0] == after[:0xA0] and before[0xA4:] == after[0xA4:]
    if gate:
        for register, value in registers.items():
            expected_ecx = frame - 0xB8 if register == x86.UC_X86_REG_ECX else value
            assert emulator.reg_read(register) == expected_ecx
        assert emulator.reg_read(x86.UC_X86_REG_ESP) == stack_pointer
        assert (
            emulator.reg_read(x86.UC_X86_REG_EFLAGS) & 0x8D5
            == 0x246 & 0x8D5
        )
    return {
        "kind": kind,
        "fee": fee,
        "alignment": alignment,
        "scene": scene,
        "gate": gate,
        "expected": expected,
        "calls": calls,
    }


def write_report(output_directory: Path, proof: dict) -> Path:
    output_directory.mkdir(parents=True, exist_ok=True)
    target = output_directory / "economy-offline-tests.json"
    with tempfile.NamedTemporaryFile(
        mode="w",
        encoding="utf-8",
        dir=output_directory,
        prefix=".economy-offline-tests-",
        suffix=".tmp",
        delete=False,
    ) as handle:
        json.dump(proof, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        temporary = Path(handle.name)
    temporary.replace(target)
    return target


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Run 2160 isolated Unicorn assertions for the private upgrade-gold "
            "candidate and write economy-offline-tests.json."
        )
    )
    parser.add_argument("--exe", required=True, type=Path, help="candidate PE image")
    parser.add_argument("--expected-sha256", required=True, help="expected candidate SHA256 from build receipt")
    parser.add_argument("--map", required=True, type=Path, help="MSVC build map for its payload")
    parser.add_argument("--output", required=True, type=Path, help="report directory")
    return parser


def main() -> int:
    parser = build_parser()
    if not __debug__:
        parser.error("run without Python -O because the test uses assertions")
    args = parser.parse_args()
    assert args.exe.is_file(), f"candidate is not a file: {args.exe}"
    assert args.map.is_file(), f"build map is not a file: {args.map}"

    pefile_module, unicorn_module, x86 = load_dependencies(parser)
    map_sha256, exports, preferred_address = read_map(args.map)
    blob = args.exe.read_bytes()
    client_sha256 = sha256(blob)
    assert client_sha256 == args.expected_sha256.lower(), "candidate does not match build receipt"
    pe = pefile_module.PE(data=blob, fast_load=False)

    image_base = pe.OPTIONAL_HEADER.ImageBase
    assert pe.FILE_HEADER.Machine == 0x014C, "candidate is not an x86 PE image"
    assert image_base < 0x100000000, "candidate image base is outside the x86 address space"
    assert preferred_address >= image_base, "map payload lies below the candidate image"

    for name, address in exports.items():
        rva = address - image_base
        assert rva >= 0, ("map symbol is below the candidate image base", name)
        assert any(
            section.Name.rstrip(b"\0") == b".fe012c" and image_base + section.VirtualAddress == preferred_address + 0x1000
            and section.VirtualAddress <= rva < section.VirtualAddress + section.Misc_VirtualSize
            for section in pe.sections
        ), ("map symbol is outside candidate PE sections", name, hex(address))

    results = [
        run_case(unicorn_module, x86, pe, exports, kind, fee, alignment, scene, gate)
        for kind in ("reinforce", "amplify", "other", "uninitialized")
        for fee in (-1, 0, 1, 6, 7, 8, 84975, 846072, 2147483647)
        for alignment in (0, 1, 4, 8, 12, 15)
        for scene in ("no_manager", "no_window", "no_npc", "ordinary", "conversion")
        for gate in (False, True)
    ]
    assert len(results) == 2160

    proof = {
        "passed": True,
        "cases": len(results),
        "clientSHA256": client_sha256,
        "mapSHA256": map_sha256,
        "exeFile": args.exe.name,
        "mapFile": args.map.name,
        "imageBase": hex(image_base),
        "mapPreferredLoadAddress": hex(preferred_address),
        "exports": {name: hex(exports[name]) for name in MAP_SYMBOLS},
        "scope": (
            "Offline Unicorn execution of the selected candidate helper/gate with synthetic fixtures; "
            "native protected-int/window APIs are stubs. No game process, full native price pipeline, "
            "network, production access, build, or OS-level execution was performed."
        ),
        "limitations": [
            "protected-int accessors and the window getter are contract stubs",
            "the candidate is mapped into Unicorn and not executed by the operating system",
            "no game process or complete native price pipeline is exercised",
            "success does not constitute native or in-game validation",
        ],
        "results": results,
    }
    report_path = write_report(args.output, proof)
    print(json.dumps({key: value for key, value in proof.items() if key != "results"}, indent=2))
    print(f"report: {report_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
