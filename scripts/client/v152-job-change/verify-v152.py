#!/usr/bin/env python3
"""Independent receive-door verifier for the v152 job-change EXE.

This script does not import or modify the patch producer.  It parses the
baseline and candidate PE images, checks the exact byte/edit contract, decodes
the .jobgate helper, and optionally executes the actual receive callsite with
Unicorn.  Emulation is deliberately single-image: baseline first, release the
emulator, then candidate.
"""
from __future__ import annotations

import argparse
import ctypes
import gc
import hashlib
import json
import os
import struct
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, List, Optional, Sequence, Tuple

IMAGE_BASE = 0x400000
PAGE = 0x1000

DEFAULT_BASELINE = Path("DNF-v151.exe")
DEFAULT_CANDIDATE = Path("DNF.exe")
DEFAULT_RECEIPT = Path("patch-receipt.json")
DEFAULT_BASELINE_SHA = (
    "2def3cc35c1ee00a9c697fe81d1f31259860468d042d6d477cd3b4be08f0e6eb"
)
DEFAULT_CANDIDATE_SHA = (
    "44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e"
)

CALL_VA = 0x2523A13
RETURN_VA = 0x2523A18
CONTEXT_VA = 0x2523A05
CONTEXT = bytes.fromhex(
    "0fb74f0183c410518b0d44fa7e05e8e85598048b0d44fa7e05"
)
OLD_GATE_VA = 0x6EA9000
NEW_GATE_VA = 0x6EAA000
SHARED_GATE_VA = 0x6E83610
BITMAP_VA = 0x6E92300
BITMAP_OPCODE_MAX = 0x5C2
ORIGINAL_TRAMPOLINE_VA = 0x376E6C0
FRAGMENT_VA = 0x2523A05
FRAGMENT_END = 0x2523A18

STACK_TOP = 0x10008000
SCRATCH_VA = 0x20000000
IMAGE_GLOBAL_VA = 0x57EFA44

DEFAULT_CLASSES = (0, 1)
DEFAULT_PROBE_CLASSES = (2, 255)
DEFAULT_ADDITIONS = ((0, 1150), (1, 502), (1, 785))
HELPER_BYTES = 74
SECTION_SIZE = 0x1000

KNOWN_CLASS_SPECIFIC = {
    0: (
        47, 323, 504, 505, 578, 584, 1093, 1229, 1230, 1239,
        1388, 1389, 1390, 1391, 1392, 1393, 1394, 1395, 1396, 1397,
        1398, 1399, 1400, 1402, 1483, 1484, 1485, 1486, 1487, 1488,
        1489, 1490, 1491, 1530,
    ),
    1: (
        51, 418, 424, 629, 630, 658, 668, 820, 868, 889,
        1113, 1342, 1457, 1458, 1465, 1466, 1554,
    ),
}

DEFAULT_PYTHON_DEPS = ()


class VerifyError(RuntimeError):
    pass


@dataclass(frozen=True)
class Section:
    index: int
    name: str
    vsize: int
    va: int
    rsize: int
    raw: int
    chars: int


class PEImage:
    def __init__(self, path: Path):
        self.path = Path(path)
        try:
            self.data = self.path.read_bytes()
        except OSError as exc:
            raise VerifyError(f"cannot read {self.path}: {exc}") from exc
        self.sha256 = hashlib.sha256(self.data).hexdigest()
        if len(self.data) < 0x40 or self.data[:2] != b"MZ":
            raise VerifyError(f"{self.path}: not an MZ image")
        self.pe = struct.unpack_from("<I", self.data, 0x3C)[0]
        if self.pe + 24 > len(self.data):
            raise VerifyError(f"{self.path}: PE header outside file")
        if self.data[self.pe : self.pe + 4] != b"PE\0\0":
            raise VerifyError(f"{self.path}: bad PE signature")
        self.machine = struct.unpack_from("<H", self.data, self.pe + 4)[0]
        self.num_sections = struct.unpack_from("<H", self.data, self.pe + 6)[0]
        self.opt_size = struct.unpack_from("<H", self.data, self.pe + 20)[0]
        self.opt = self.pe + 24
        self.magic = struct.unpack_from("<H", self.data, self.opt)[0]
        self.image_base = struct.unpack_from("<I", self.data, self.opt + 28)[0]
        self.size_of_code = struct.unpack_from("<I", self.data, self.opt + 4)[0]
        self.size_of_image = struct.unpack_from("<I", self.data, self.opt + 56)[0]
        self.size_of_headers = struct.unpack_from("<I", self.data, self.opt + 60)[0]
        self.checksum_field = self.opt + 64
        self.checksum = struct.unpack_from("<I", self.data, self.checksum_field)[0]
        self.section_alignment = struct.unpack_from("<I", self.data, self.opt + 32)[0]
        self.file_alignment = struct.unpack_from("<I", self.data, self.opt + 36)[0]
        self.cert_rva, self.cert_size = struct.unpack_from("<II", self.data, self.opt + 128)
        self.table = self.opt + self.opt_size
        self.sections: List[Section] = []
        need = self.table + self.num_sections * 40
        if need > len(self.data):
            raise VerifyError(f"{self.path}: section table outside file")
        for i in range(self.num_sections):
            row = self.table + i * 40
            name = self.data[row : row + 8].split(b"\0", 1)[0].decode(
                "ascii", "replace"
            )
            vsize, rva, rsize, raw = struct.unpack_from("<IIII", self.data, row + 8)
            chars = struct.unpack_from("<I", self.data, row + 36)[0]
            if raw + rsize > len(self.data):
                raise VerifyError(
                    f"{self.path}: section {i} raw range 0x{raw:x}+0x{rsize:x} "
                    "is outside file"
                )
            self.sections.append(
                Section(i, name, vsize, self.image_base + rva, rsize, raw, chars)
            )

    @property
    def size(self) -> int:
        return len(self.data)

    def offset(self, va: int, size: int = 1) -> int:
        for sec in self.sections:
            if sec.va <= va and va + size <= sec.va + sec.rsize:
                return sec.raw + (va - sec.va)
        raise VerifyError(f"{self.path}: VA 0x{va:x} (+0x{size:x}) is not file-backed")

    def bytes_at(self, va: int, size: int) -> bytes:
        result = bytearray()
        while size:
            section = next((s for s in self.sections if s.va <= va < s.va+s.rsize), None)
            if section is None:
                raise VerifyError(f"unbacked VA {va:x}")
            count = min(size, section.va+section.rsize-va)
            at = section.raw+va-section.va
            result.extend(self.data[at:at+count])
            va += count
            size -= count
        return bytes(result)

    def page_bytes(self, page_va: int) -> bytes:
        out = bytearray(PAGE)
        for sec in self.sections:
            if sec.vsize == 0:
                continue
            lo = max(page_va, sec.va)
            hi = min(page_va + PAGE, sec.va + sec.rsize, sec.va + sec.vsize)
            if lo < hi:
                src = sec.raw + (lo - sec.va)
                out[lo - page_va : hi - page_va] = self.data[src : src + (hi - lo)]
        return bytes(out)


def pe_checksum(data: bytes, field: int) -> int:
    n = len(data)
    import array
    words = array.array('H', data[:n-(n&1)])
    if sys.byteorder != 'little':
        words.byteswap()
    total = sum(words) - words[field//2] - words[field//2+1]
    if n & 1:
        total += data[-1]
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + n) & 0xFFFFFFFF


def diff_runs(a: bytes, b: bytes, block: int = 0x10000) -> List[Tuple[int, int]]:
    """Return half-open byte ranges in the common prefix where a != b."""
    n = min(len(a), len(b))
    runs: List[Tuple[int, int]] = []
    active = False
    start = 0
    for base in range(0, n, block):
        end = min(n, base + block)
        if a[base:end] == b[base:end]:
            if active:
                runs.append((start, base))
                active = False
            continue
        for i in range(base, end):
            if a[i] != b[i]:
                if not active:
                    start = i
                    active = True
            elif active:
                runs.append((start, i))
                active = False
    if active:
        runs.append((start, n))
    return runs


def rel32(buf: bytes, at: int) -> int:
    return struct.unpack_from("<i", buf, at)[0]


def near_target(buf: bytes, op_at: int) -> int:
    return op_at + 6 + rel32(buf, op_at + 2)


def jmp_target(buf: bytes, op_at: int) -> int:
    return op_at + 5 + rel32(buf, op_at + 1)


def build_expected_helper(new_va: int, previous_va: int) -> bytes:
    code = bytearray()
    fixups: List[Tuple[int, str]] = []
    labels = {}

    def emit(value: bytes) -> None:
        code.extend(value)

    def near(op: bytes, label: str) -> None:
        emit(op)
        fixups.append((len(code), label))
        code.extend(b"\0\0\0\0")

    emit(bytes.fromhex("0fb7442404"))
    emit(bytes.fromhex("803f00"))
    near(bytes.fromhex("0f84"), "class0")
    emit(bytes.fromhex("803f01"))
    near(bytes.fromhex("0f85"), "fallback")
    for opcode in (502, 785):
        code.append(0x3D)
        code.extend(struct.pack("<I", opcode))
        near(bytes.fromhex("0f84"), "allow")
    near(bytes.fromhex("e9"), "fallback")
    labels["class0"] = len(code)
    code.append(0x3D)
    code.extend(struct.pack("<I", 1150))
    near(bytes.fromhex("0f84"), "allow")
    labels["fallback"] = len(code)
    near(bytes.fromhex("e9"), "previous")
    labels["allow"] = len(code)
    emit(bytes.fromhex("b801000000c20400"))

    for at, label in fixups:
        if label == "previous":
            target = previous_va - (new_va + at + 4)
        else:
            target = labels[label] - (at + 4)
        struct.pack_into("<i", code, at, target)
    return bytes(code)


def check_helper(buf: bytes, new_va: int, previous_va: int, expected: bytes) -> dict:
    if len(buf) != HELPER_BYTES:
        raise VerifyError(f"helper length is {len(buf)}, expected {HELPER_BYTES}")
    if buf != expected:
        first = next((i for i, (a, b) in enumerate(zip(buf, expected)) if a != b), None)
        raise VerifyError(f"helper bytes differ from canonical encoding at {first}")
    if buf[0:5] != bytes.fromhex("0fb7442404"):
        raise VerifyError("helper does not read word [esp+4]")
    if buf[5:8] != bytes.fromhex("803f00") or buf[14:17] != bytes.fromhex("803f01"):
        raise VerifyError("helper class comparisons are not class0/class1")
    if near_target(buf, 8) != 50:
        raise VerifyError("class0 branch target is not the 1150 block")
    if near_target(buf, 17) != 61:
        raise VerifyError("class1 fallback branch target is not the old-gate jump")
    if struct.unpack_from("<I", buf, 24)[0] != 502:
        raise VerifyError("first class1 immediate is not 502")
    if near_target(buf, 28) != 66:
        raise VerifyError("502 branch does not go to allow")
    if struct.unpack_from("<I", buf, 35)[0] != 785:
        raise VerifyError("second class1 immediate is not 785")
    if near_target(buf, 39) != 66:
        raise VerifyError("785 branch does not go to allow")
    if jmp_target(buf, 45) != 61:
        raise VerifyError("class1 fall-through does not go to fallback")
    if struct.unpack_from("<I", buf, 51)[0] != 1150:
        raise VerifyError("class0 immediate is not 1150")
    if near_target(buf, 55) != 66:
        raise VerifyError("1150 branch does not go to allow")
    old_target = new_va + jmp_target(buf, 61)
    if old_target != previous_va:
        raise VerifyError(
            f"fallback jumps to 0x{old_target:x}, expected 0x{previous_va:x}"
        )
    if buf[66:74] != bytes.fromhex("b801000000c20400"):
        raise VerifyError("allow block is not EAX=1; ret 4")
    return {
        "va": f"0x{new_va:x}",
        "size": len(buf),
        "sha256": hashlib.sha256(buf).hexdigest(),
        "fallback": f"0x{previous_va:x}",
        "class0_allowed": [1150],
        "class1_allowed": [502, 785],
    }


def parse_ints(value: str) -> Tuple[int, ...]:
    if not value.strip():
        return ()
    out = []
    for item in value.split(","):
        item = item.strip()
        if not item:
            continue
        number = int(item, 0)
        if not 0 <= number <= 255:
            raise argparse.ArgumentTypeError(f"class out of range: {item}")
        if number not in out:
            out.append(number)
    return tuple(out)


def parse_additions(value: str) -> Tuple[Tuple[int, int], ...]:
    out = []
    for item in value.split(","):
        item = item.strip()
        if not item:
            continue
        cls_text, op_text = item.split(":", 1)
        cls, opcode = int(cls_text, 0), int(op_text, 0)
        if not 0 <= cls <= 255 or not 0 <= opcode <= 0xFFFF:
            raise argparse.ArgumentTypeError(f"bad addition: {item}")
        out.append((cls, opcode))
    return tuple(sorted(set(out)))


def available_physical_mb() -> Optional[int]:
    if os.name != "nt":
        return None

    class MEMORYSTATUSEX(ctypes.Structure):
        _fields_ = [
            ("dwLength", ctypes.c_ulong),
            ("dwMemoryLoad", ctypes.c_ulong),
            ("ullTotalPhys", ctypes.c_ulonglong),
            ("ullAvailPhys", ctypes.c_ulonglong),
            ("ullTotalPageFile", ctypes.c_ulonglong),
            ("ullAvailPageFile", ctypes.c_ulonglong),
            ("ullTotalVirtual", ctypes.c_ulonglong),
            ("ullAvailVirtual", ctypes.c_ulonglong),
            ("ullAvailExtendedVirtual", ctypes.c_ulonglong),
        ]

    stat = MEMORYSTATUSEX()
    stat.dwLength = ctypes.sizeof(stat)
    if not ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(stat)):
        return None
    return int(stat.ullAvailPhys // (1024 * 1024))


def load_unicorn(deps: Sequence[str]):
    for dep in deps:
        if dep and dep not in sys.path:
            sys.path.insert(0, dep)
    try:
        import unicorn
        from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
        from unicorn.x86_const import (
            UC_X86_REG_EAX,
            UC_X86_REG_EBX,
            UC_X86_REG_ECX,
            UC_X86_REG_EDX,
            UC_X86_REG_EDI,
            UC_X86_REG_EBP,
            UC_X86_REG_EIP,
            UC_X86_REG_ESI,
            UC_X86_REG_ESP,
        )
    except Exception as exc:
        tried = "\n  ".join(deps)
        raise VerifyError(
            "unicorn is unavailable.  Install it or pass --python-deps to a "
            f"known dependency directory.  Tried:\n  {tried}\n"
            f"import error: {exc}"
        ) from exc
    return {
        "module": unicorn,
        "Uc": Uc,
        "UC_ARCH_X86": UC_ARCH_X86,
        "UC_MODE_32": UC_MODE_32,
        "EAX": UC_X86_REG_EAX,
        "EBX": UC_X86_REG_EBX,
        "ECX": UC_X86_REG_ECX,
        "EDX": UC_X86_REG_EDX,
        "EDI": UC_X86_REG_EDI,
        "EBP": UC_X86_REG_EBP,
        "EIP": UC_X86_REG_EIP,
        "ESI": UC_X86_REG_ESI,
        "ESP": UC_X86_REG_ESP,
    }


def emulate_callsite(
    image: PEImage,
    classes: Sequence[int],
    opcodes: Sequence[int],
    u: dict,
    min_available_mb: int,
    label: str,
) -> dict:
    """Run the actual call at 0x2523a13 and stop after it returns to 0x2523a18."""
    avail = available_physical_mb()
    if avail is not None and avail < min_available_mb:
        raise VerifyError(
            f"refusing to start {label} emulator: available physical memory "
            f"{avail} MiB is below --min-available-mb {min_available_mb}"
        )
    if len(opcodes) == 0:
        raise VerifyError("empty opcode set")

    uc = u["Uc"](u["UC_ARCH_X86"], u["UC_MODE_32"])
    try:
        pages = [0x2523000, 0x376E000, 0x6E83000, 0x6E92000, 0x57EF000]
        for page in pages:
            uc.mem_map(page, PAGE)
            uc.mem_write(page, image.page_bytes(page))

        chain_start = 0x6EA0000
        chain_end = 0x6EAB000 if image.num_sections >= 33 else 0x6EAA000
        chain = bytearray()
        for page in range(chain_start, chain_end, PAGE):
            chain.extend(image.page_bytes(page))
        uc.mem_map(chain_start, len(chain))
        uc.mem_write(chain_start, bytes(chain))

        uc.mem_map(0x10000000, 0x10000)
        uc.mem_write(0x10000000, bytes(0x10000))
        uc.mem_map(SCRATCH_VA, PAGE)
        uc.mem_write(SCRATCH_VA, bytes(PAGE))
        uc.mem_write(IMAGE_GLOBAL_VA, b"\0\0\0\0")

        decisions = bytearray()
        stack_base = STACK_TOP
        total = len(classes) * len(opcodes)
        done = 0
        for cls in classes:
            for opcode in opcodes:
                uc.mem_write(SCRATCH_VA, bytes((cls,)) + struct.pack("<H", opcode))
                # The real call site has ECX loaded from [0x57efa44] and the
                # opcode already pushed as the sole stack argument.
                uc.mem_write(stack_base, struct.pack("<I", opcode & 0xFFFF))
                uc.reg_write(u["EIP"], CALL_VA)
                uc.reg_write(u["ESP"], stack_base)
                uc.reg_write(u["EAX"], 0xDEADBEEF)
                uc.reg_write(u["EBX"], 0x11111111)
                uc.reg_write(u["ECX"], 0)
                uc.reg_write(u["EDX"], 0x22222222)
                uc.reg_write(u["ESI"], 0x33333333)
                uc.reg_write(u["EDI"], SCRATCH_VA)
                uc.reg_write(u["EBP"], 0x44444444)
                try:
                    uc.emu_start(CALL_VA, RETURN_VA, count=256)
                except Exception as exc:
                    raise VerifyError(
                        f"{label} class={cls} opcode={opcode}: emulation failed: {exc}"
                    ) from exc
                eip = uc.reg_read(u["EIP"])
                esp = uc.reg_read(u["ESP"])
                eax = uc.reg_read(u["EAX"])
                if eip != RETURN_VA:
                    raise VerifyError(
                        f"{label} class={cls} opcode={opcode}: EIP=0x{eip:x}, "
                        f"expected 0x{RETURN_VA:x}"
                    )
                if esp != stack_base + 4:
                    raise VerifyError(
                        f"{label} class={cls} opcode={opcode}: ESP=0x{esp:x}, "
                        f"expected 0x{stack_base + 4:x}"
                    )
                if eax not in (0, 1):
                    raise VerifyError(
                        f"{label} class={cls} opcode={opcode}: EAX=0x{eax:x}, "
                        "expected 0 or 1"
                    )
                expected_nonvolatile = {
                    "EBX": 0x11111111,
                    "ESI": 0x33333333,
                    "EDI": SCRATCH_VA,
                    "EBP": 0x44444444,
                }
                for name, expected in expected_nonvolatile.items():
                    actual = uc.reg_read(u[name])
                    if actual != expected:
                        raise VerifyError(
                            f"{label} class={cls} opcode={opcode}: {name}="
                            f"0x{actual:x}, expected 0x{expected:x}"
                        )
                decisions.append(eax)
                done += 1
                if done % 65536 == 0:
                    print(
                        f"[native] {label}: {done}/{total} cases",
                        file=sys.stderr,
                        flush=True,
                    )
        return {
            "decisions": bytes(decisions),
            "available_mb_before": avail,
            "available_mb_after": available_physical_mb(),
            "cases": len(decisions),
        }
    finally:
        del uc
        gc.collect()


def expected_edit_ranges(base: PEImage) -> List[Tuple[int, int]]:
    call_off = base.offset(CALL_VA, 5)
    header_off = base.table + base.num_sections * 40
    return sorted(
        [
            (base.pe + 6, base.pe + 8),
            (base.opt + 4, base.opt + 8),
            (base.opt + 56, base.opt + 60),
            (base.opt + 64, base.opt + 68),
            (header_off, header_off + 40),
            (call_off + 1, call_off + 5),
        ]
    )


def covered_by(lo: int, hi: int, ranges: Sequence[Tuple[int, int]]) -> bool:
    return any(a <= lo and hi <= b for a, b in ranges)


def probe_opcodes(bitmap_allowed: Sequence[int], additions: Sequence[Tuple[int, int]]) -> List[int]:
    values = {0, 1, 2, 47, 51, 323, 418, 424, 502, 504, 505, 578, 584, 629, 630,
              658, 668, 785, 820, 868, 889, 1093, 1113, 1150, 1229, 1230, 1239,
              1340, 1341, 1342, 1388, 1400, 1402, 1457, 1458, 1465, 1466,
              1483, 1491, 1530, 1554, 0x5C2, 0x5C3, 0xFFFF}
    values.update(bitmap_allowed)
    values.update(op for _, op in additions)
    for ops in KNOWN_CLASS_SPECIFIC.values():
        values.update(ops)
    return sorted(v for v in values if 0 <= v <= 0xFFFF)


def verify(args) -> dict:
    checks: dict = {}
    errors: List[str] = []
    warnings: List[str] = []

    def check(name: str, condition: bool, detail: str = "") -> bool:
        checks[name] = bool(condition)
        if not condition:
            errors.append(f"{name}: {detail}" if detail else name)
        return bool(condition)

    base = PEImage(args.baseline)
    cand = PEImage(args.candidate)
    check("baseline_sha256", base.sha256 == args.baseline_sha256,
          f"{base.sha256} != {args.baseline_sha256}")
    check("candidate_sha256", cand.sha256 == args.candidate_sha256,
          f"{cand.sha256} != {args.candidate_sha256}")
    check("baseline_pe32", base.machine == 0x14C and base.magic == 0x10B,
          f"machine=0x{base.machine:x} magic=0x{base.magic:x}")
    check("candidate_pe32", cand.machine == 0x14C and cand.magic == 0x10B,
          f"machine=0x{cand.machine:x} magic=0x{cand.magic:x}")
    check("image_base", base.image_base == IMAGE_BASE and cand.image_base == IMAGE_BASE,
          f"0x{base.image_base:x}/0x{cand.image_base:x}")
    check("section_alignments",
          base.section_alignment == PAGE and base.file_alignment == PAGE
          and cand.section_alignment == PAGE and cand.file_alignment == PAGE)
    check("certificate_absent", base.cert_rva == 0 and base.cert_size == 0
          and cand.cert_rva == 0 and cand.cert_size == 0)
    check("baseline_sections", base.num_sections == 32, str(base.num_sections))
    check("candidate_sections", cand.num_sections == 33, str(cand.num_sections))
    check("baseline_size", base.size == 111845376, f"0x{base.size:x}")
    check("candidate_size", cand.size == 111849472, f"0x{cand.size:x}")
    check("size_delta", cand.size - base.size == SECTION_SIZE,
          f"{cand.size - base.size}")
    check("baseline_size_of_image", base.size_of_image == base.size,
          f"0x{base.size_of_image:x} vs 0x{base.size:x}")
    check("candidate_size_of_image", cand.size_of_image == cand.size,
          f"0x{cand.size_of_image:x} vs 0x{cand.size:x}")
    check("size_of_code_delta", cand.size_of_code - base.size_of_code == SECTION_SIZE,
          f"{cand.size_of_code - base.size_of_code}")
    check("baseline_checksum", pe_checksum(base.data, base.checksum_field) == base.checksum,
          f"0x{pe_checksum(base.data, base.checksum_field):x} != 0x{base.checksum:x}")
    check("candidate_checksum", pe_checksum(cand.data, cand.checksum_field) == cand.checksum,
          f"0x{pe_checksum(cand.data, cand.checksum_field):x} != 0x{cand.checksum:x}")

    header_off = base.table + base.num_sections * 40
    expected_header = struct.pack(
        "<8sIIIIIIHHI", b".jobgate", HELPER_BYTES, base.size,
        SECTION_SIZE, base.size, 0, 0, 0, 0, 0x60000020,
    )
    check("header_space_was_zero", not any(base.data[header_off : header_off + 40]),
          "baseline has non-zero bytes in the 33rd section slot")
    check("new_section_header", cand.data[header_off : header_off + 40] == expected_header,
          "candidate 33rd section header differs")
    check("existing_section_headers_unchanged",
          base.data[base.table : base.table + base.num_sections * 40]
          == cand.data[base.table : base.table + base.num_sections * 40])
    last = cand.sections[-1]
    check("new_section_shape",
          last.name == ".jobgate" and last.va == IMAGE_BASE + base.size
          and last.vsize == HELPER_BYTES and last.rsize == SECTION_SIZE
          and last.raw == base.size and last.chars == 0x60000020,
          f"{last}")

    call_off = base.offset(CALL_VA, 5)
    base_call = base.data[call_off : call_off + 5]
    cand_call = cand.data[call_off : call_off + 5]
    base_target = CALL_VA + 5 + rel32(base_call, 1) if base_call[0] == 0xE8 else -1
    cand_target = CALL_VA + 5 + rel32(cand_call, 1) if cand_call[0] == 0xE8 else -1
    check("baseline_call_target", base_target == OLD_GATE_VA,
          f"target=0x{base_target:x}")
    check("candidate_call_target", cand_target == NEW_GATE_VA,
          f"target=0x{cand_target:x}")
    ctx_off = base.offset(CONTEXT_VA, len(CONTEXT))
    check("baseline_context", base.data[ctx_off : ctx_off + len(CONTEXT)] == CONTEXT)
    expected_cand_ctx = CONTEXT[:14] + cand_call + CONTEXT[19:]
    check("candidate_context",
          cand.data[ctx_off : ctx_off + len(CONTEXT)] == expected_cand_ctx)

    new_va = IMAGE_BASE + base.size_of_image
    helper = cand.bytes_at(new_va, HELPER_BYTES)
    expected_helper = build_expected_helper(new_va, OLD_GATE_VA)
    helper_info = check_helper(helper, new_va, OLD_GATE_VA, expected_helper)
    appended = cand.data[base.size :]
    check("appended_size", len(appended) == SECTION_SIZE, str(len(appended)))
    check("appended_helper_prefix", appended[:HELPER_BYTES] == expected_helper)
    check("appended_tail_zero", not any(appended[HELPER_BYTES:]),
          "non-zero bytes after the 74-byte helper")

    for name, lo, hi in (
        ("old_chain", 0x6EA0000, 0x6EA9000),
        ("shared_gate", 0x6E83000, 0x6E84000),
        ("bitmap", 0x6E92000, 0x6E93000),
        ("original_trampoline", 0x376E000, 0x376F000),
    ):
        a = base.bytes_at(lo, hi - lo)
        b = cand.bytes_at(lo, hi - lo)
        check(f"{name}_unchanged", a == b, f"bytes changed in 0x{lo:x}..0x{hi:x}")

    bitmap = base.bytes_at(BITMAP_VA, 0x200)
    target_bits = {}
    for cls, opcode in DEFAULT_ADDITIONS:
        admitted = bool(bitmap[opcode // 8] & (1 << (opcode % 8)))
        target_bits[f"{cls}:{opcode}"] = admitted
        check(f"target_not_bitmap_admitted_{cls}_{opcode}", not admitted,
              "target was already admitted by the shared bitmap")

    edits = expected_edit_ranges(base)
    runs = diff_runs(base.data, cand.data[: base.size])
    unexpected = [(lo, hi) for lo, hi in runs if not covered_by(lo, hi, edits)]
    check("no_unexpected_byte_changes", not unexpected, str(unexpected[:8]))
    check("no_prefix_growth_outside_append", cand.size == base.size + SECTION_SIZE)

    receipt_info = None
    if args.receipt:
        try:
            receipt = json.loads(Path(args.receipt).read_text(encoding="utf-8"))
        except Exception as exc:
            check("receipt_read", False, str(exc))
            receipt = {}
        receipt_info = receipt
        check("receipt_input_sha256", receipt.get("input_sha256") == base.sha256)
        check("receipt_output_sha256", receipt.get("output_sha256") == cand.sha256)
        check("receipt_input_size", receipt.get("input_size") == base.size)
        check("receipt_output_size", receipt.get("output_size") == cand.size)
        check("receipt_call_va", receipt.get("call_va", "").lower() == "0x2523a13")
        check("receipt_helper_va", receipt.get("helper_va", "").lower() == "0x6eaa000")
        check("receipt_section_va", receipt.get("section_va", "").lower() == "0x6eaa000")
        check("receipt_helper_bytes", receipt.get("helper_bytes") == HELPER_BYTES)
        check("receipt_fallback", receipt.get("fallback", "").lower() == "0x6ea9000")
        check("receipt_class0", receipt.get("class0") == [1150])
        check("receipt_class1", receipt.get("class1") == [502, 785])
        check("receipt_original_ranges",
              [tuple(x) for x in receipt.get("original_ranges", [])] == edits,
              f"{receipt.get('original_ranges')} != {edits}")
        check("receipt_appended_range",
              receipt.get("appended_range") == [base.size, cand.size])
        if receipt.get("published") or receipt.get("deployed"):
            warnings.append("receipt reports published/deployed true")

    report = {
        "verifier": "verify-v152.py",
        "mode": "static-only" if args.static_only else "native",
        "passed": False,
        "baseline": {
            "path": str(base.path),
            "sha256": base.sha256,
            "size": base.size,
            "sections": base.num_sections,
            "call_target": f"0x{base_target:x}",
        },
        "candidate": {
            "path": str(cand.path),
            "sha256": cand.sha256,
            "size": cand.size,
            "sections": cand.num_sections,
            "call_target": f"0x{cand_target:x}",
        },
        "diff": {
            "changed_runs": [list(x) for x in runs],
            "allowed_edit_ranges": [list(x) for x in edits],
            "unexpected_ranges": [list(x) for x in unexpected],
            "appended_range": [base.size, cand.size],
        },
        "helper": helper_info,
        "shared_bitmap_target_bits": target_bits,
        "receipt": receipt_info,
        "checks": checks,
        "errors": errors,
        "warnings": warnings,
    }

    if errors:
        report["passed"] = False
        return report

    if args.static_only:
        report["native"] = {"skipped": True, "reason": "--static-only"}
        report["passed"] = True
        return report

    classes = list(args.classes)
    probe_classes = list(args.probe_classes)
    limit = args.limit
    if limit != 0x10000 and not args.allow_partial:
        raise VerifyError("--limit below 65536 requires --allow-partial")
    if set(classes) != {0, 1} and not args.allow_partial:
        raise VerifyError("default full classes are 0 and 1; use --allow-partial for other sets")
    deps = list(args.python_deps)
    u = load_unicorn(deps)
    full_opcodes = list(range(limit))
    bitmap_allowed = [
        op for op in range(min(limit, BITMAP_OPCODE_MAX + 1))
        if bitmap[op // 8] & (1 << (op % 8))
    ]
    probes = probe_opcodes(bitmap_allowed, args.additions)

    print(
        f"[native] full classes={classes} limit={limit}; "
        f"probe classes={probe_classes} probes={len(probes)}",
        file=sys.stderr,
        flush=True,
    )
    started = time.time()
    base_run = emulate_callsite(
        base, classes, full_opcodes, u, args.min_available_mb, "baseline"
    )
    gc.collect()
    cand_run = emulate_callsite(
        cand, classes, full_opcodes, u, args.min_available_mb, "candidate"
    )
    gc.collect()
    base_dec = base_run["decisions"]
    cand_dec = cand_run["decisions"]
    width = len(full_opcodes)
    diffs = []
    for ci, cls in enumerate(classes):
        for opcode in range(limit):
            b = base_dec[ci * width + opcode]
            c = cand_dec[ci * width + opcode]
            if b != c:
                diffs.append((cls, opcode, b, c))
    probe_diffs = []
    probe_base = {}
    probe_cand = {}
    if probe_classes and probes:
        pb = emulate_callsite(
            base, probe_classes, probes, u, args.min_available_mb, "baseline-probes"
        )
        pc = emulate_callsite(
            cand, probe_classes, probes, u, args.min_available_mb, "candidate-probes"
        )
        pw = len(probes)
        for ci, cls in enumerate(probe_classes):
            for j, opcode in enumerate(probes):
                b = pb["decisions"][ci * pw + j]
                c = pc["decisions"][ci * pw + j]
                probe_base[(cls, opcode)] = b
                probe_cand[(cls, opcode)] = c
                if b != c:
                    probe_diffs.append((cls, opcode, b, c))
    else:
        pb = pc = None

    added = sorted((cls, op) for cls, op, b, c in diffs + probe_diffs if b == 0 and c == 1)
    removed = sorted((cls, op) for cls, op, b, c in diffs + probe_diffs if b == 1 and c == 0)
    expected_added = sorted(args.additions)
    check("native_added_exact", added == expected_added, f"{added} != {expected_added}")
    check("native_removed_none", not removed, str(removed))
    check("native_no_other_changes",
          len(diffs) + len(probe_diffs) == len(added) + len(removed),
          f"diffs={diffs[:12]} probe_diffs={probe_diffs[:12]}")

    allowed_full = {}
    for ci, cls in enumerate(classes):
        vals = [
            op for op in range(limit)
            if base_dec[ci * width + op]
        ]
        allowed_full[cls] = vals
    full_counts = {str(c): len(v) for c, v in allowed_full.items()}
    class_specific = {}
    for cls in (0, 1):
        if cls in allowed_full:
            class_specific[str(cls)] = [
                op for op in allowed_full[cls] if op not in set(bitmap_allowed)
            ]

    report["native"] = {
        "cases": (base_run["cases"] + (pb["cases"] if pb else 0)),
        "elapsed_seconds": round(time.time() - started, 3),
        "full_classes": classes,
        "limit": limit,
        "probe_classes": probe_classes,
        "probe_opcodes": probes,
        "baseline_allowed_counts": full_counts,
        "bitmap_allowed_count": len(bitmap_allowed),
        "pre_existing_class_specific": class_specific,
        "known_class_specific_fixture": {
            str(k): list(v) for k, v in KNOWN_CLASS_SPECIFIC.items()
        },
        "added": [list(x) for x in added],
        "removed": [list(x) for x in removed],
        "available_mb_before": base_run["available_mb_before"],
        "available_mb_after": base_run["available_mb_after"],
        "probe_diffs": [list(x) for x in probe_diffs],
        "single_emulator": True,
    }
    report["passed"] = not errors
    return report


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, default=DEFAULT_BASELINE)
    parser.add_argument("--candidate", type=Path, default=DEFAULT_CANDIDATE)
    parser.add_argument("--receipt", type=Path, default=DEFAULT_RECEIPT)
    parser.add_argument("--baseline-sha256", default=DEFAULT_BASELINE_SHA)
    parser.add_argument("--candidate-sha256", default=DEFAULT_CANDIDATE_SHA)
    parser.add_argument(
        "--additions", type=parse_additions, default=parse_additions(
            "0:1150,1:502,1:785"
        )
    )
    parser.add_argument("--classes", type=parse_ints, default=DEFAULT_CLASSES)
    parser.add_argument("--probe-classes", type=parse_ints, default=DEFAULT_PROBE_CLASSES)
    parser.add_argument("--limit", type=lambda s: int(s, 0), default=0x10000)
    parser.add_argument("--static-only", action="store_true")
    parser.add_argument("--allow-partial", action="store_true")
    parser.add_argument("--min-available-mb", type=int, default=1536)
    parser.add_argument("--python-deps", action="append", default=[])
    parser.add_argument("--json-out", type=Path, default=None)
    parser.add_argument("--no-json", action="store_true")
    args = parser.parse_args(argv)
    if not args.python_deps:
        args.python_deps = list(DEFAULT_PYTHON_DEPS)
    else:
        args.python_deps = list(args.python_deps) + list(DEFAULT_PYTHON_DEPS)
    if args.json_out is None:
        args.json_out = Path(__file__).resolve().with_name("verify-v152-result.json")

    started = time.time()
    try:
        report = verify(args)
    except Exception as exc:
        print(f"VERIFY ERROR: {exc}", file=sys.stderr)
        return 2
    report["elapsed_seconds"] = round(time.time() - started, 3)
    if not args.no_json:
        try:
            args.json_out.write_text(
                json.dumps(report, indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )
        except OSError as exc:
            print(f"WARNING: cannot write {args.json_out}: {exc}", file=sys.stderr)
    if report.get("passed"):
        print("PASS", report.get("mode"))
    else:
        print("FAIL", report.get("mode"))
    print("baseline:", report["baseline"]["sha256"], report["baseline"]["size"])
    print("candidate:", report["candidate"]["sha256"], report["candidate"]["size"])
    print("changed_runs:", len(report["diff"]["changed_runs"]))
    if report.get("native") and not report["native"].get("skipped"):
        native = report["native"]
        print("native_cases:", native["cases"])
        print("allowed_counts:", native["baseline_allowed_counts"])
        print("added:", native["added"])
        print("removed:", native["removed"])
        print("class_specific:", {k: len(v) for k, v in native["pre_existing_class_specific"].items()})
    if report["errors"]:
        for error in report["errors"][:20]:
            print("ERROR:", error)
    for warning in report["warnings"]:
        print("WARNING:", warning)
    print("json:", args.json_out)
    return 0 if report.get("passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())
