"""Independent EXE-level regression for the exact v137-to-v138 gate change.

The expectations below are written independently of the patch module's class
sets.  The test executes the real receive fragment in both images for every
u16 opcode in classes 0, 1, 2 and 255, then checks the PE layout, RX section,
checksum, file scope, high-ID bitmap behaviour and output protection.
"""
import copy
import ctypes
from ctypes import wintypes
import dis
import json
import os
from pathlib import Path
import struct
import sys
import tempfile
import time
import unittest

EMULATOR_DEPS = Path(r"H:\86JP启动器\_dist\selftest\new-exe-loader-ipc-20261003\emulator-deps")
SCRIPT_DIR = Path(__file__).resolve().parent
for _path in (EMULATOR_DEPS, SCRIPT_DIR):
    if str(_path) not in sys.path:
        sys.path.insert(0, str(_path))

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_MEM_READ
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_EBP, UC_X86_REG_ECX,
    UC_X86_REG_EDI, UC_X86_REG_EIP, UC_X86_REG_ESI, UC_X86_REG_ESP,
)

import patch_party_feature_gates_v138 as patch


STACK = 0x10000800
PACKET = 0x20000000
IMAGE_BASE = 0x400000
PACKET_CLASSES = (0, 1, 2, 255)

# Independent v138 expectations.  These are deliberately not derived from
# patch.CLASS0, patch.CLASS1 or patch.ADDED_CLASS0/1.
ADDED_CLASS0_ORDER = tuple(range(1388, 1401)) + (1402, 1530)
ADDED_CLASS1_ORDER = (1457, 1458, 1465, 1466, 1554)
ADDED_CLASS0 = frozenset(ADDED_CLASS0_ORDER)
ADDED_CLASS1 = frozenset(ADDED_CLASS1_ORDER)
PRESERVED_EXISTING_NOTIFICATIONS = (1401, 1403)
EXPECTED_CLASS0 = (47, 323, 504, 505, 578, 1229, 1230, 1239) + ADDED_CLASS0_ORDER
EXPECTED_CLASS1 = (51, 424) + ADDED_CLASS1_ORDER

OLD_CLASS0_OPEN = frozenset((
    47, 323, 370, 504, 505, 578, 1229, 1230, 1239, 1401, 1403,
)) | frozenset(range(1483, 1492))
OLD_CLASS1_OPEN = frozenset((
    51, 424, 658, 668, 820, 868, 889, 1288, 1450, 1451, 1452, 1453,
))
OLD_CLOSED = ((0, 371), (1, 1454), (0, 1555), (0, 65535),
              (1, 1555), (1, 65535))

EXPECTED_INPUT_SHA256 = "10478ee7b4d8273da2ca8dfae5f520e3b5ceedda7aa42ec4e6de014c0ea0941f"
EXPECTED_OUTPUT_SHA256 = "9363574786928ec58ab6f82be0d40847a814d6e4ef42753a1a54750134a09d9c"
EXPECTED_INPUT_SIZE = 111812608
EXPECTED_OUTPUT_SIZE = 111812608
EXPECTED_SECTION_RVA = 111808512
EXPECTED_HELPER_SIZE = 448
EXPECTED_INPUT_HELPER_SIZE = 228
EXPECTED_SECTION_SIZE = 4096
EXPECTED_SECTION_COUNT = 24
EXPECTED_CALL_VA = 0x2523A13
EXPECTED_FRAGMENT_VA = 0x2523A0C
EXPECTED_FRAGMENT_END = 0x2523A18
EXPECTED_OLD_ENTRY_VA = 0x376E6C0
EXPECTED_BITMAP_VA = 0x6E92300
EXPECTED_BITMAP_SIZE = 0x100

ROOT = Path(r"E:\90\client-v138-party-feature-gates-20261018")
DEFAULT_INPUT = Path(r"E:\90\client-v137-item-link-20261018\client\DNF.exe")
DEFAULT_CANDIDATE = ROOT / "client" / "DNF.exe"
DEFAULT_RECEIPT = ROOT / "test-receipt.json"


def sha(data):
    import hashlib
    return hashlib.sha256(data).hexdigest()


def layout(image):
    """Parse PE32 headers without using the patch module's PE helpers."""
    if len(image) < 0x40 or image[:2] != b"MZ":
        raise AssertionError("not an MZ image")
    peoff = struct.unpack_from("<I", image, 0x3C)[0]
    if image[peoff:peoff + 4] != b"PE\0\0":
        raise AssertionError("not a PE image")
    machine, count = struct.unpack_from("<HH", image, peoff + 4)
    opt_size = struct.unpack_from("<H", image, peoff + 20)[0]
    opt = peoff + 24
    if machine != 0x14C or struct.unpack_from("<H", image, opt)[0] != 0x10B:
        raise AssertionError("expected PE32/x86 image")
    if struct.unpack_from("<I", image, opt + 28)[0] != IMAGE_BASE:
        raise AssertionError("unexpected PE image base")
    table = opt + opt_size
    sections = []
    for index in range(count):
        header = table + index * 40
        name = image[header:header + 8].rstrip(b"\0")
        virtual_size, va, raw_size, raw = struct.unpack_from("<IIII", image, header + 8)
        flags = struct.unpack_from("<I", image, header + 36)[0]
        sections.append(dict(header=header, name=name, virtual_size=virtual_size,
                             va=va, raw_size=raw_size, raw=raw, flags=flags))
    return dict(peoff=peoff, opt=opt, opt_size=opt_size, table=table,
                section_count=count,
                size_image=struct.unpack_from("<I", image, opt + 56)[0],
                sections=sections)


def rva_offset(info, va, size=1):
    for section in info["sections"]:
        section_va = IMAGE_BASE + section["va"]
        span = max(section["virtual_size"], section["raw_size"])
        if section_va <= va and va + size <= section_va + span:
            return section["raw"] + va - section_va
    raise AssertionError("address outside file-backed sections: %#x" % va)


def vm(image, info):
    machine = Uc(UC_ARCH_X86, UC_MODE_32)
    size = (info["size_image"] + 0xFFF) & ~0xFFF
    machine.mem_map(IMAGE_BASE, size)
    for section in info["sections"]:
        if section["raw_size"]:
            start = IMAGE_BASE + section["va"]
            machine.mem_write(start, image[section["raw"]:section["raw"] + section["raw_size"]])
    machine.mem_map(0x10000000, 0x1000)
    machine.mem_map(PACKET, 0x1000)
    return machine


def run_decision(machine, packet_class, opcode, saved):
    machine.mem_write(PACKET, struct.pack("<BH", packet_class, opcode))
    machine.reg_write(UC_X86_REG_ESP, STACK)
    machine.reg_write(UC_X86_REG_ECX, opcode)
    machine.reg_write(UC_X86_REG_EAX, 0xDEADBEEF)
    for register, value in saved.items():
        machine.reg_write(register, value)
    machine.emu_start(patch.FRAGMENT_VA, patch.FRAGMENT_END, count=256)
    if machine.reg_read(UC_X86_REG_EIP) != patch.FRAGMENT_END:
        raise AssertionError("fragment did not stop at FRAGMENT_END")
    if machine.reg_read(UC_X86_REG_ESP) != STACK:
        raise AssertionError("fragment did not preserve the caller stack")
    for register, value in saved.items():
        if machine.reg_read(register) != value:
            raise AssertionError("fragment changed callee-saved register %#x" % register)
    result = machine.reg_read(UC_X86_REG_EAX)
    if result not in (0, 1):
        raise AssertionError("unexpected gate result %#x at class %d opcode %d" %
                             (result, packet_class, opcode))
    return result


def run_with_reads(machine, packet_class, opcode, saved):
    reads = []

    def hook(uc, access, address, size, value, user_data):
        reads.append((address, size))

    handle = machine.hook_add(UC_HOOK_MEM_READ, hook)
    try:
        result = run_decision(machine, packet_class, opcode, saved)
    finally:
        machine.hook_del(handle)
    return result, reads


def independent_checksum(image, field):
    """Fold each PE word, skipping the checksum field, then add the file size."""
    value = 0
    for at in range(0, len(image), 2):
        if field <= at < field + 4:
            continue
        value += struct.unpack_from("<H", image, at)[0]
        value = (value & 0xFFFF) + (value >> 16)
    value = (value & 0xFFFF) + (value >> 16)
    return value + len(image)


def code_units(code):
    yield code
    for value in code.co_consts:
        if isinstance(value, type(code)):
            yield from code_units(value)


def wait_for_candidate(path, timeout):
    deadline = time.monotonic() + timeout
    while not path.is_file():
        if time.monotonic() >= deadline:
            raise FileNotFoundError("candidate not generated: %s" % path)
        time.sleep(1.0)


class PartyFeatureGatePatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = Path(os.environ.get("DNF_PARTY_GATE_INPUT_EXE", DEFAULT_INPUT)).resolve()
        candidate = Path(os.environ.get("DNF_PARTY_GATE_CANDIDATE_EXE", DEFAULT_CANDIDATE)).resolve()
        wait_for_candidate(candidate, float(os.environ.get("DNF_PARTY_GATE_WAIT_SECONDS", "600")))
        cls.source = source
        cls.candidate = candidate
        cls.before = source.read_bytes()
        cls.after = candidate.read_bytes()
        cls.before_layout = layout(cls.before)
        cls.after_layout = layout(cls.after)
        cls.generated, cls.receipt = patch.build(cls.before)
        cls.completed = set()
        cls.matrix = None
        cls.boundary = None
        cls.scope = None
        cls.protection = None
        cls.helper_check = None

    def test_01_all_u16_actual_receive_fragment_matrix(self):
        self.assertEqual(patch.INPUT_SHA256, EXPECTED_INPUT_SHA256)
        self.assertEqual(patch.INPUT_SIZE, EXPECTED_INPUT_SIZE)
        self.assertEqual(patch.CALL_VA, EXPECTED_CALL_VA)
        self.assertEqual(patch.FRAGMENT_VA, EXPECTED_FRAGMENT_VA)
        self.assertEqual(patch.FRAGMENT_END, EXPECTED_FRAGMENT_END)
        self.assertEqual(patch.OLD_ENTRY_VA, EXPECTED_OLD_ENTRY_VA)
        self.assertEqual(patch.NEW_RVA, EXPECTED_SECTION_RVA)
        self.assertEqual(patch.NEW_VA, IMAGE_BASE + EXPECTED_SECTION_RVA)
        self.assertEqual(patch.NEW_RAW, EXPECTED_SECTION_RVA)
        self.assertEqual(patch.NEW_SIZE, EXPECTED_SECTION_SIZE)
        self.assertEqual(tuple(patch.CLASS0), EXPECTED_CLASS0)
        self.assertEqual(tuple(patch.CLASS1), EXPECTED_CLASS1)
        self.assertEqual(tuple(patch.ADDED_CLASS0), ADDED_CLASS0_ORDER)
        self.assertEqual(tuple(patch.ADDED_CLASS1), ADDED_CLASS1_ORDER)
        self.assertEqual(set(patch.ADDED_CLASS0), set(ADDED_CLASS0))
        self.assertEqual(set(patch.ADDED_CLASS1), set(ADDED_CLASS1))
        for opcode in PRESERVED_EXISTING_NOTIFICATIONS:
            self.assertNotIn(opcode, patch.ADDED_CLASS0)
            self.assertNotIn(opcode, patch.CLASS0)

        machines = (vm(self.before, self.before_layout), vm(self.after, self.after_layout))
        saved = {UC_X86_REG_EDI: PACKET, UC_X86_REG_ESI: 0x12345678,
                 UC_X86_REG_EBX: 0x23456789, UC_X86_REG_EBP: 0x34567890}

        allowed = {(0, opcode) for opcode in ADDED_CLASS0}
        allowed.update((1, opcode) for opcode in ADDED_CLASS1)
        target_by_class = {0: ADDED_CLASS0, 1: ADDED_CLASS1,
                           2: frozenset(), 255: frozenset()}
        changed = []
        changed_by_class = {str(item): [] for item in PACKET_CLASSES}
        target_already_open = {str(item): [] for item in PACKET_CLASSES}
        executions = 0
        print("v138 matrix: executing real receive fragment", file=sys.stderr, flush=True)
        for packet_class in PACKET_CLASSES:
            targets = target_by_class[packet_class]
            for opcode in range(0x10000):
                before = run_decision(machines[0], packet_class, opcode, saved)
                after = run_decision(machines[1], packet_class, opcode, saved)
                executions += 2
                if before != after:
                    self.assertIn((packet_class, opcode), allowed,
                                  "non-target decision changed at class %d opcode %d" %
                                  (packet_class, opcode))
                    self.assertEqual((before, after), (0, 1),
                                     "target change must be exactly 0->1")
                    changed.append((packet_class, opcode, before, after))
                    changed_by_class[str(packet_class)].append(opcode)
                if opcode in targets:
                    self.assertEqual(after, 1, "requested target must be admitted")
                    if before == 1:
                        target_already_open[str(packet_class)].append(opcode)
                else:
                    self.assertEqual(after, before,
                                     "non-target admission/rejection changed at class %d opcode %d" %
                                     (packet_class, opcode))
            print("v138 matrix: class %d complete, %d executions" %
                  (packet_class, executions), file=sys.stderr, flush=True)

        self.assertEqual(executions, 4 * 0x10000 * 2)
        expected_changes = {(0, opcode) for opcode in ADDED_CLASS0}
        expected_changes.update((1, opcode) for opcode in ADDED_CLASS1)
        self.assertEqual({(c, o) for c, o, _, _ in changed}, expected_changes)
        self.assertEqual(len(changed), len(expected_changes))
        self.assertTrue(all((b, a) == (0, 1) for _, _, b, a in changed))
        self.assertEqual(changed_by_class["0"], sorted(ADDED_CLASS0))
        self.assertEqual(changed_by_class["1"], sorted(ADDED_CLASS1))
        self.assertEqual(changed_by_class["2"], [])
        self.assertEqual(changed_by_class["255"], [])

        for opcode in sorted(OLD_CLASS0_OPEN):
            self.assertEqual(run_decision(machines[0], 0, opcode, saved), 1)
            self.assertEqual(run_decision(machines[1], 0, opcode, saved), 1)
        for opcode in sorted(OLD_CLASS1_OPEN):
            self.assertEqual(run_decision(machines[0], 1, opcode, saved), 1)
            self.assertEqual(run_decision(machines[1], 1, opcode, saved), 1)
        for packet_class, opcode in OLD_CLOSED:
            self.assertEqual(run_decision(machines[0], packet_class, opcode, saved), 0)
            self.assertEqual(run_decision(machines[1], packet_class, opcode, saved), 0)
        for opcode in PRESERVED_EXISTING_NOTIFICATIONS:
            self.assertEqual(run_decision(machines[0], 0, opcode, saved), 1)
            self.assertEqual(run_decision(machines[1], 0, opcode, saved), 1)

        type(self).matrix = dict(
            native_executions=executions,
            changed_decisions=[{"class": c, "opcode": o, "before": b, "after": a}
                               for c, o, b, a in changed],
            changed_decisions_by_class={key: [{"opcode": opcode,
                                               "before": 0, "after": 1}
                                              for opcode in values]
                                        for key, values in changed_by_class.items()},
            target_already_open_by_class=target_already_open,
            requested_all_admitted=True,
            non_target_decisions_identical=True,
            preserved_existing_notifications=list(PRESERVED_EXISTING_NOTIFICATIONS),
            preserved_notifications_still_open=True,
            old_admissions_verified=True,
            class2_255_unchanged=True,
            callee_saved_registers_and_stack_verified=True,
        )
        type(self).completed.add("matrix")

    def test_02_high_boundary_and_out_of_range_bitmap_evidence(self):
        machines = (vm(self.before, self.before_layout), vm(self.after, self.after_layout))
        saved = {UC_X86_REG_EDI: PACKET, UC_X86_REG_ESI: 0x12345678,
                 UC_X86_REG_EBX: 0x23456789, UC_X86_REG_EBP: 0x34567890}
        bitmap_lo = EXPECTED_BITMAP_VA
        bitmap_hi = bitmap_lo + EXPECTED_BITMAP_SIZE
        window_lo = bitmap_lo - 0x100
        window_hi = bitmap_hi + 0x200

        cases = (
            ("class0_1530", 0, 1530, 1),
            ("class1_1554", 1, 1554, 1),
            ("oob_class0_1555", 0, 1555, 0),
            ("oob_class0_65535", 0, 65535, 0),
            ("oob_class1_1555", 1, 1555, 0),
            ("oob_class1_65535", 1, 65535, 0),
        )
        evidence = {}
        for name, packet_class, opcode, expected in cases:
            before, before_reads = run_with_reads(machines[0], packet_class, opcode, saved)
            after, after_reads = run_with_reads(machines[1], packet_class, opcode, saved)
            self.assertEqual(after, expected)
            reads = [(address, size) for address, size in after_reads
                     if window_lo <= address < window_hi]
            evidence[name] = {
                "class": packet_class,
                "opcode": opcode,
                "before": before,
                "after": after,
                "bitmap_window_reads": [[hex(address), size] for address, size in reads],
            }
            if name.startswith("oob_"):
                self.assertEqual(before, after)
                self.assertEqual(reads, [], "out-of-range probe touched the bitmap window")
            else:
                self.assertEqual((before, after), (0, 1))
                self.assertEqual(reads, [], "high target indexed the native bitmap")

        controls = {}
        for opcode in PRESERVED_EXISTING_NOTIFICATIONS:
            before, before_reads = run_with_reads(machines[0], 0, opcode, saved)
            after, after_reads = run_with_reads(machines[1], 0, opcode, saved)
            self.assertEqual((before, after), (1, 1))
            reads = [(address, size) for address, size in after_reads
                     if window_lo <= address < window_hi]
            self.assertTrue(reads, "native preserved notification did not read the bitmap")
            self.assertTrue(all(bitmap_lo <= address < bitmap_hi for address, _ in reads))
            controls[str(opcode)] = {
                "before": before, "after": after,
                "bitmap_window_reads": [[hex(address), size] for address, size in reads],
            }

        neighbors = {}
        neighbor_cases = [
            (0, 1529), (0, 1531), (0, 1553), (0, 1555), (0, 1556),
            (0, 65534),
            (1, 1553), (1, 1555), (1, 1556), (1, 65534),
        ]
        for packet_class, opcode in neighbor_cases:
            before = run_decision(machines[0], packet_class, opcode, saved)
            after = run_decision(machines[1], packet_class, opcode, saved)
            self.assertEqual(before, after,
                             "out-of-range probe changed neighbor class %d opcode %d" %
                             (packet_class, opcode))
            neighbors["%d/%d" % (packet_class, opcode)] = [before, after]

        type(self).boundary = dict(
            high_boundary_oob={
                "bitmap_va": hex(bitmap_lo),
                "bitmap_size": EXPECTED_BITMAP_SIZE,
                "native_bitmap_bound": 0x5C2,
                "cases": evidence,
                "preserved_native_controls": controls,
                "neighbor_decisions": neighbors,
                "out_of_range_bitmap_reads": [],
                "high_ids_bypass_native_bitmap": True,
                "high_ids_decision_only": True,
                "neighbors_unchanged": True,
                "all_bitmap_window_reads_within_256_bytes": True,
            }
        )
        type(self).completed.add("boundary")

    def test_03_reproducible_scope_pe_rx_and_independent_checksum(self):
        self.assertEqual(sha(self.before), EXPECTED_INPUT_SHA256)
        self.assertEqual(len(self.before), EXPECTED_INPUT_SIZE)
        self.assertEqual(sha(self.after), EXPECTED_OUTPUT_SHA256)
        self.assertEqual(len(self.after), EXPECTED_OUTPUT_SIZE)
        self.assertEqual(self.generated, self.after)
        self.assertEqual(self.receipt["input_sha256"], EXPECTED_INPUT_SHA256)
        self.assertEqual(self.receipt["output_sha256"], EXPECTED_OUTPUT_SHA256)
        self.assertEqual(self.receipt["input_size"], EXPECTED_INPUT_SIZE)
        self.assertEqual(self.receipt["output_size"], EXPECTED_OUTPUT_SIZE)
        self.assertEqual(self.receipt["helper_size"], EXPECTED_HELPER_SIZE)
        self.assertEqual(self.receipt["input_helper_size"], EXPECTED_INPUT_HELPER_SIZE)
        self.assertEqual(self.receipt["helper_capacity"], EXPECTED_SECTION_SIZE)
        self.assertEqual(self.receipt["max_requested_class0"], max(ADDED_CLASS0))
        self.assertEqual(self.receipt["max_requested_class1"], max(ADDED_CLASS1))
        self.assertEqual(self.receipt["preserved_existing_notifications"],
                         list(PRESERVED_EXISTING_NOTIFICATIONS))
        self.assertEqual(self.receipt["added_class0"], list(ADDED_CLASS0_ORDER))
        self.assertEqual(self.receipt["added_class1"], list(ADDED_CLASS1_ORDER))
        self.assertEqual(self.receipt["class0"], list(EXPECTED_CLASS0))
        self.assertEqual(self.receipt["class1"], list(EXPECTED_CLASS1))
        self.assertTrue(self.receipt["shared_bitmap_and_bound_unchanged"])
        self.assertTrue(self.receipt["high_ids_use_class_adapter_without_bitmap_access"])
        self.assertTrue(self.receipt["adapter_section_reused"])
        self.assertEqual(self.receipt["file_size_unchanged"], True)

        before_info, after_info = self.before_layout, self.after_layout
        self.assertEqual(after_info["section_count"], EXPECTED_SECTION_COUNT)
        self.assertEqual(after_info["section_count"], before_info["section_count"])
        self.assertEqual(after_info["size_image"], before_info["size_image"])
        for image, info in ((self.before, before_info), (self.after, after_info)):
            for section in info["sections"]:
                self.assertGreaterEqual(section["raw"], 0)
                self.assertLessEqual(section["raw"] + section["raw_size"], len(image))
                self.assertLessEqual(section["va"] + max(section["virtual_size"],
                                                          section["raw_size"]),
                                     info["size_image"])
        self.assertLessEqual(after_info["opt"] + 68, len(self.after))

        helper = patch.helper()
        self.assertEqual(len(helper), EXPECTED_HELPER_SIZE)
        self.assertEqual(self.receipt["helper_sha256"], sha(helper))
        self.assertEqual(patch.NEW_RVA, after_info["sections"][-1]["va"])
        self.assertEqual(patch.NEW_VA, IMAGE_BASE + after_info["sections"][-1]["va"])
        self.assertEqual(patch.NEW_RAW, after_info["sections"][-1]["raw"])
        self.assertEqual(patch.NEW_SIZE, after_info["sections"][-1]["raw_size"])
        section = after_info["sections"][-1]
        self.assertEqual(section["name"], b".clsgate")
        self.assertEqual(section["flags"], 0x60000020)
        self.assertEqual(section["flags"] & 0x80000000, 0)
        self.assertEqual(section["flags"] & 0x20000000, 0x20000000)
        self.assertEqual(section["flags"] & 0x40000000, 0x40000000)
        self.assertEqual(section["virtual_size"], EXPECTED_HELPER_SIZE)
        self.assertEqual(section["raw_size"], EXPECTED_SECTION_SIZE)
        self.assertEqual(section["header"], after_info["table"] + 23 * 40)
        self.assertEqual(struct.unpack_from("<I", self.before,
                                            before_info["sections"][-1]["header"] + 8)[0],
                         EXPECTED_INPUT_HELPER_SIZE)
        self.assertEqual(EXPECTED_SECTION_RVA + EXPECTED_SECTION_SIZE, EXPECTED_INPUT_SIZE)
        self.assertEqual(self.before[EXPECTED_SECTION_RVA + EXPECTED_INPUT_HELPER_SIZE:
                                     EXPECTED_SECTION_RVA + EXPECTED_SECTION_SIZE],
                         bytes(EXPECTED_SECTION_SIZE - EXPECTED_INPUT_HELPER_SIZE))
        self.assertEqual(self.after[EXPECTED_SECTION_RVA:EXPECTED_SECTION_RVA + EXPECTED_HELPER_SIZE],
                         helper)
        self.assertEqual(self.after[EXPECTED_SECTION_RVA + EXPECTED_HELPER_SIZE:
                                    EXPECTED_SECTION_RVA + EXPECTED_SECTION_SIZE],
                         bytes(EXPECTED_SECTION_SIZE - EXPECTED_HELPER_SIZE))

        callsite = rva_offset(after_info, EXPECTED_CALL_VA, 5)
        expected_call = b"\xe8" + struct.pack("<i", IMAGE_BASE + EXPECTED_SECTION_RVA
                                                - EXPECTED_CALL_VA - 5)
        self.assertEqual(self.before[callsite:callsite + 5], expected_call)
        self.assertEqual(self.after[callsite:callsite + 5], expected_call)
        self.assertEqual(patch.FRAGMENT_VA, EXPECTED_CALL_VA - 7)
        self.assertEqual(patch.FRAGMENT_END, EXPECTED_CALL_VA + 5)
        self.assertLess(patch.FRAGMENT_VA, patch.CALL_VA)
        self.assertLess(patch.CALL_VA, patch.FRAGMENT_END)

        header = after_info["sections"][-1]["header"]
        expected_ranges = sorted([
            (after_info["opt"] + 64, after_info["opt"] + 68),
            (header + 8, header + 12),
            (EXPECTED_SECTION_RVA, EXPECTED_SECTION_RVA + EXPECTED_HELPER_SIZE),
        ])
        actual_ranges = [tuple(item) for item in self.receipt["original_ranges"]]
        self.assertEqual(actual_ranges, expected_ranges)
        last = 0
        for start, end in expected_ranges:
            self.assertEqual(self.before[last:start], self.after[last:start])
            last = end
        self.assertEqual(self.before[last:], self.after[last:])

        bitmap_at = rva_offset(after_info, EXPECTED_BITMAP_VA, EXPECTED_BITMAP_SIZE)
        self.assertEqual(self.before[bitmap_at:bitmap_at + EXPECTED_BITMAP_SIZE],
                         self.after[bitmap_at:bitmap_at + EXPECTED_BITMAP_SIZE])
        shared_gate = rva_offset(after_info, 0x6E83610, 0x40)
        self.assertEqual(self.before[shared_gate:shared_gate + 0x40],
                         self.after[shared_gate:shared_gate + 0x40])

        def bitmap_bit(opcode):
            at = rva_offset(after_info, EXPECTED_BITMAP_VA + opcode // 8)
            return bool(self.before[at] & (1 << (opcode % 8)))

        self.assertTrue(bitmap_bit(370))
        self.assertFalse(bitmap_bit(371))
        self.assertFalse(bitmap_bit(424))
        self.assertFalse(bitmap_bit(1239))
        self.assertTrue(bitmap_bit(1288))
        self.assertTrue(bitmap_bit(1401))
        self.assertTrue(bitmap_bit(1403))
        self.assertFalse(bitmap_bit(1530))
        self.assertFalse(bitmap_bit(1554))

        checksum_field = after_info["opt"] + 64
        self.assertEqual(independent_checksum(self.after, checksum_field),
                         struct.unpack_from("<I", self.after, checksum_field)[0])
        type(self).scope = dict(
            input_sha256=sha(self.before), output_sha256=sha(self.after),
            original_ranges=[list(item) for item in expected_ranges],
            helper_size=EXPECTED_HELPER_SIZE, input_helper_size=EXPECTED_INPUT_HELPER_SIZE,
            section_name=".clsgate", section_flags=hex(section["flags"]),
            section_virtual_size=section["virtual_size"],
            section_raw_size=section["raw_size"],
            callsite=hex(EXPECTED_CALL_VA),
            checksum_independent=True, other_bytes_identical=True,
            existing_fixes_unchanged=True, shared_bitmap_unchanged=True,
            shared_gate_unchanged=True, preserved_existing_notifications=list(
                PRESERVED_EXISTING_NOTIFICATIONS),
            size_image_unchanged=True, file_size_unchanged=True, pe_valid=True,
        )
        type(self).completed.add("scope")

    @unittest.skipUnless(os.name == "nt", "Windows ImageHlp checksum check")
    def test_04_windows_imagehlp_checksum(self):
        image = ctypes.create_string_buffer(self.after)
        header, computed = wintypes.DWORD(), wintypes.DWORD()
        api = ctypes.WinDLL("imagehlp").CheckSumMappedFile
        api.argtypes = [ctypes.c_void_p, wintypes.DWORD,
                        ctypes.POINTER(wintypes.DWORD), ctypes.POINTER(wintypes.DWORD)]
        api.restype = ctypes.c_void_p
        self.assertTrue(api(image, len(self.after), ctypes.byref(header), ctypes.byref(computed)))
        stored = struct.unpack_from("<I", self.after, self.after_layout["opt"] + 64)[0]
        self.assertEqual(header.value, stored)
        self.assertEqual(computed.value, stored)
        type(self).completed.add("imagehlp")

    def test_05_input_and_output_protection(self):
        input_hash = sha(self.before)
        output_hash = sha(self.after)
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1])
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1] + bytes([self.before[-1] ^ 1]))
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "DNF.exe"
            destination.write_bytes(b"preserve")
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, destination)
            self.assertEqual(destination.read_bytes(), b"preserve")
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, self.source)
            fresh = Path(directory) / "candidate" / "DNF.exe"
            receipt = patch.write_candidate(self.source, fresh)
            self.assertEqual(fresh.read_bytes(), self.after)
            self.assertEqual(receipt["output_sha256"], EXPECTED_OUTPUT_SHA256)
        self.assertEqual(sha(self.before), input_hash)
        self.assertEqual(sha(self.after), output_hash)
        type(self).protection = dict(
            input_hash_stable=True, output_hash_stable=True,
            existing_output_preserved=True, same_path_rejected=True,
            wrong_sha_and_length_rejected=True,
        )
        type(self).completed.add("protection")

    def test_06_helper_generation_is_stable(self):
        mutable = {}
        for name, value in vars(patch).items():
            if name.startswith("__"):
                continue
            if isinstance(value, (list, dict, set, bytearray)):
                mutable[name] = copy.deepcopy(value)
        first = patch.helper()
        second = patch.helper()
        self.assertEqual(first, second)
        self.assertEqual(first, self.after[EXPECTED_SECTION_RVA:
                                          EXPECTED_SECTION_RVA + EXPECTED_HELPER_SIZE])
        for name, value in mutable.items():
            self.assertEqual(vars(patch)[name], value, "helper mutated global %s" % name)
        for code in code_units(patch.helper.__code__):
            for instruction in dis.get_instructions(code):
                self.assertNotIn(instruction.opname,
                                 {"STORE_GLOBAL", "STORE_NAME", "DELETE_GLOBAL", "DELETE_NAME"},
                                 "helper writes module state at %s" % instruction.opname)
        type(self).helper_check = dict(
            mutable_globals=sorted(mutable), helper_stable=True, no_global_stores=True,
        )
        type(self).completed.add("helper")

    @classmethod
    def tearDownClass(cls):
        destination = Path(os.environ.get("DNF_PARTY_GATE_RECEIPT", DEFAULT_RECEIPT))
        expected = {"matrix", "boundary", "scope", "imagehlp", "protection", "helper"}
        passed = expected <= cls.completed
        receipt = {
            "schema": "dnf90-v138-party-feature-gates-independent-exe-test-v1",
            "all_passed": passed,
            "passed": passed,
            "completed": sorted(cls.completed),
            "missing": sorted(expected - cls.completed),
            "input_path": str(cls.source),
            "candidate_path": str(cls.candidate),
            "input_sha256": sha(cls.before),
            "output_sha256": sha(cls.after),
            "expected_input_sha256": EXPECTED_INPUT_SHA256,
            "expected_output_sha256": EXPECTED_OUTPUT_SHA256,
            "input_size": len(cls.before),
            "output_size": len(cls.after),
            "independent_added_class0": list(ADDED_CLASS0_ORDER),
            "independent_added_class1": list(ADDED_CLASS1_ORDER),
            "preserved_existing_notifications": list(PRESERVED_EXISTING_NOTIFICATIONS),
            "native_executions": 0,
            "changed_decisions": [],
            "high_boundary_oob": None,
            "full_ui_acceptance": False,
            "payload_consumers_executed": False,
            "limitations": [
                "full_ui_acceptance=false",
                "payload consumers were not executed",
                "no game launch, network, server, PVF or EXE publication",
            ],
        }
        if cls.matrix is not None:
            receipt.update(cls.matrix)
        if cls.boundary is not None:
            receipt.update(cls.boundary)
        if cls.scope is not None:
            receipt.update(cls.scope)
        if cls.protection is not None:
            receipt.update(cls.protection)
        if cls.helper_check is not None:
            receipt.update(cls.helper_check)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    unittest.main(verbosity=2)
