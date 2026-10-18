"""Independent regression for the exact v136-to-v137 class1/424 change."""
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
import unittest

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_EBP, UC_X86_REG_ECX,
    UC_X86_REG_EDI, UC_X86_REG_EIP, UC_X86_REG_ESI, UC_X86_REG_ESP,
)

import patch_item_link_gate_v137 as patch


STACK = 0x10000800
PACKET = 0x20000000
IMAGE_BASE = 0x400000
PACKET_CLASSES = (0, 1, 2, 255)
REQUESTED_CLASS0 = frozenset((47, 323, 504, 505, 578, 1229, 1230, 1239))
REQUESTED_CLASS1 = frozenset((51, 424))
INHERITED_CLASS0 = frozenset(range(1483, 1492))
INHERITED_CLASS1 = frozenset((658, 668, 820, 868, 889))
EXPECTED_INPUT_SHA256 = "22ec56fe82317d0419dfdd6b76e3ee79b287856cf7cccb499177b4dc636e782f"
EXPECTED_OUTPUT_SHA256 = "10478ee7b4d8273da2ca8dfae5f520e3b5ceedda7aa42ec4e6de014c0ea0941f"
EXPECTED_HELPER_SIZE = 228
EXPECTED_INPUT_HELPER_SIZE = 217


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
                section_count=count, size_image=struct.unpack_from("<I", image, opt + 56)[0],
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


class ItemLinkGatePatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get("DNF_ITEM_LINK_INPUT_EXE")
        candidate = os.environ.get("DNF_ITEM_LINK_CANDIDATE_EXE")
        if not source or not candidate:
            raise unittest.SkipTest("set DNF_ITEM_LINK_INPUT_EXE and DNF_ITEM_LINK_CANDIDATE_EXE")
        cls.source = Path(source).resolve()
        cls.candidate = Path(candidate).resolve()
        cls.before = cls.source.read_bytes()
        cls.after = cls.candidate.read_bytes()
        cls.before_layout = layout(cls.before)
        cls.after_layout = layout(cls.after)
        cls.generated, cls.receipt = patch.build(cls.before)
        cls.completed = set()
        cls.matrix = None
        cls.scope = None
        cls.protection = None
        cls.helper_check = None

    def test_all_u16_actual_callsite_decisions_and_classes(self):
        self.assertEqual(tuple(patch.CLASS0), (47, 323, 504, 505, 578, 1229, 1230, 1239))
        self.assertEqual(tuple(patch.CLASS1), (51, 424))
        self.assertEqual(set(patch.CLASS0), set(REQUESTED_CLASS0))
        self.assertEqual(set(patch.CLASS1), set(REQUESTED_CLASS1))
        requested = {0: REQUESTED_CLASS0, 1: REQUESTED_CLASS1, 2: frozenset(), 255: frozenset()}
        machines = (vm(self.before, self.before_layout), vm(self.after, self.after_layout))
        saved = {UC_X86_REG_EDI: PACKET, UC_X86_REG_ESI: 0x12345678,
                 UC_X86_REG_EBX: 0x23456789, UC_X86_REG_EBP: 0x34567890}
        changed = []
        changed_by_class = {}
        originally_open = {}
        executions = 0
        print("v137 matrix: warming gate VMs", file=sys.stderr, flush=True)
        for packet_class in PACKET_CLASSES:
            class_changes = []
            open_before = []
            for opcode in range(0x10000):
                before = run_decision(machines[0], packet_class, opcode, saved)
                after = run_decision(machines[1], packet_class, opcode, saved)
                executions += 2
                if before != after:
                    class_changes.append({"opcode": opcode, "before": before, "after": after})
                    changed.append((packet_class, opcode, before, after))
                if before == 1:
                    open_before.append(opcode)
            if packet_class in (0, 1):
                missing = sorted(requested[packet_class] - set(open_before))
                if packet_class == 0:
                    self.assertEqual(missing, [])
                else:
                    self.assertEqual(missing, [424])
            changed_by_class[str(packet_class)] = class_changes
            originally_open[str(packet_class)] = open_before
            print("v137 matrix: class %d complete, %d executions" % (packet_class, executions),
                  file=sys.stderr, flush=True)
        self.assertEqual(executions, 524288)
        self.assertEqual(changed, [(1, 424, 0, 1)], "only class1/424 may change")
        self.assertEqual(changed_by_class["0"], [])
        self.assertEqual(changed_by_class["1"], [{"opcode": 424, "before": 0, "after": 1}])
        for packet_class in (2, 255):
            self.assertEqual(changed_by_class[str(packet_class)], [])
        for opcode in sorted(INHERITED_CLASS0):
            self.assertEqual(run_decision(machines[1], 0, opcode, saved), 1)
        for opcode in sorted(INHERITED_CLASS1):
            self.assertEqual(run_decision(machines[1], 1, opcode, saved), 1)
        for opcode in sorted(REQUESTED_CLASS0):
            self.assertEqual(run_decision(machines[0], 0, opcode, saved), 1)
            self.assertEqual(run_decision(machines[1], 0, opcode, saved), 1)
        self.assertEqual(run_decision(machines[0], 1, 51, saved), 1)
        self.assertEqual(run_decision(machines[1], 1, 51, saved), 1)
        self.assertEqual(run_decision(machines[0], 1, 424, saved), 0)
        self.assertEqual(run_decision(machines[1], 1, 424, saved), 1)
        self.assertEqual(run_decision(machines[0], 0, 370, saved), 1)
        self.assertEqual(run_decision(machines[1], 0, 370, saved), 1)
        self.assertEqual(run_decision(machines[0], 0, 371, saved), 0)
        self.assertEqual(run_decision(machines[1], 0, 371, saved), 0)
        self.assertEqual(run_decision(machines[0], 0, 1239, saved), 1)
        self.assertEqual(run_decision(machines[1], 0, 1239, saved), 1)
        self.assertEqual(run_decision(machines[0], 1, 1288, saved), 1)
        self.assertEqual(run_decision(machines[1], 1, 1288, saved), 1)
        type(self).matrix = dict(
            native_executions=executions,
            changed_decisions=[{"class": c, "opcode": o, "before": b, "after": a}
                               for c, o, b, a in changed],
            changed_by_class=changed_by_class,
            originally_open_requested_by_class={
                "0": sorted(set(originally_open["0"]) & set(REQUESTED_CLASS0)),
                "1": sorted(set(originally_open["1"]) & set(REQUESTED_CLASS1)),
            },
            class0_370_before=1, class0_370_after=1,
            class0_371_before=0, class0_371_after=0,
            class0_1239_before=1, class0_1239_after=1,
            class1_424_before=0, class1_424_after=1,
            class1_1288_before=1, class1_1288_after=1,
            class2_255_unchanged=True,
            inherited_admissions_unchanged=True,
            callee_saved_registers_and_stack_verified=True,
        )
        type(self).completed.add("matrix")

    def test_reproducible_scope_section_rx_and_independent_checksum(self):
        self.assertEqual(sha(self.before), EXPECTED_INPUT_SHA256)
        self.assertEqual(len(self.before), 111812608)
        self.assertEqual(sha(self.after), EXPECTED_OUTPUT_SHA256)
        self.assertEqual(self.generated, self.after)
        self.assertEqual(len(self.after), len(self.before))
        self.assertEqual(self.receipt["output_sha256"], EXPECTED_OUTPUT_SHA256)

        before_info, after_info = self.before_layout, self.after_layout
        self.assertEqual(after_info["section_count"], 24)
        self.assertEqual(after_info["section_count"], before_info["section_count"])
        self.assertEqual(after_info["size_image"], before_info["size_image"])
        helper = patch.helper()
        self.assertEqual(len(helper), EXPECTED_HELPER_SIZE)
        self.assertEqual(self.receipt["helper_size"], EXPECTED_HELPER_SIZE)
        self.assertEqual(self.receipt["helper_sha256"], sha(helper))
        self.assertEqual(patch.NEW_RVA, after_info["sections"][-1]["va"])
        self.assertEqual(patch.NEW_VA, IMAGE_BASE + after_info["sections"][-1]["va"])
        self.assertEqual(patch.NEW_RAW, after_info["sections"][-1]["raw"])
        self.assertEqual(patch.NEW_SIZE, after_info["sections"][-1]["raw_size"])
        section = after_info["sections"][-1]
        self.assertEqual(section["name"], b".clsgate")
        self.assertEqual(section["flags"], 0x60000020)
        self.assertEqual(section["virtual_size"], EXPECTED_HELPER_SIZE)
        self.assertEqual(section["raw_size"], 0x1000)
        self.assertEqual(struct.unpack_from("<I", self.before,
                                            before_info["sections"][-1]["header"] + 8)[0],
                         EXPECTED_INPUT_HELPER_SIZE)
        self.assertEqual(self.after[patch.NEW_RAW:patch.NEW_RAW + EXPECTED_HELPER_SIZE], helper)

        callsite = rva_offset(after_info, patch.CALL_VA, 5)
        self.assertEqual(patch.FRAGMENT_VA, patch.CALL_VA - 7)
        self.assertLess(patch.FRAGMENT_VA, patch.CALL_VA)
        self.assertLess(patch.CALL_VA, patch.FRAGMENT_END)
        expected_call = b"\xe8" + struct.pack("<i", patch.NEW_VA - patch.CALL_VA - 5)
        self.assertEqual(self.before[callsite:callsite + 5], expected_call)
        self.assertEqual(self.after[callsite:callsite + 5], expected_call)

        header = after_info["sections"][-1]["header"]
        expected_ranges = sorted([(after_info["opt"] + 64, after_info["opt"] + 68),
                                  (header + 8, header + 12),
                                  (patch.NEW_RAW, patch.NEW_RAW + EXPECTED_HELPER_SIZE)])
        actual_ranges = [tuple(item) for item in self.receipt["original_ranges"]]
        self.assertEqual(actual_ranges, expected_ranges)
        last = 0
        for start, end in expected_ranges:
            self.assertEqual(self.before[last:start], self.after[last:start])
            last = end
        self.assertEqual(self.before[last:], self.after[last:])
        self.assertNotEqual(self.before[patch.NEW_RAW:patch.NEW_RAW + EXPECTED_HELPER_SIZE],
                            self.after[patch.NEW_RAW:patch.NEW_RAW + EXPECTED_HELPER_SIZE])
        self.assertEqual(self.after[patch.NEW_RAW + EXPECTED_HELPER_SIZE:
                                    patch.NEW_RAW + patch.NEW_SIZE],
                         self.before[patch.NEW_RAW + EXPECTED_HELPER_SIZE:
                                     patch.NEW_RAW + patch.NEW_SIZE])

        bitmap = rva_offset(after_info, patch.pe.BITMAP_VA, 0x100)
        self.assertEqual(self.before[bitmap:bitmap + 0x100], self.after[bitmap:bitmap + 0x100])
        shared_gate = rva_offset(after_info, 0x6E83610, 0x40)
        self.assertEqual(self.before[shared_gate:shared_gate + 0x40],
                         self.after[shared_gate:shared_gate + 0x40])

        def bitmap_bit(opcode):
            at = rva_offset(after_info, patch.pe.BITMAP_VA + opcode // 8)
            return bool(self.before[at] & (1 << (opcode % 8)))

        self.assertTrue(bitmap_bit(370), "class0/370 must stay natively admitted")
        self.assertFalse(bitmap_bit(371), "class0/371 must stay rejected")
        self.assertFalse(bitmap_bit(424), "class1/424 must use the adapter")
        self.assertFalse(bitmap_bit(1239), "class0/1239 must keep the v136 adapter")
        self.assertTrue(bitmap_bit(1288), "class1/1288 must stay natively admitted")

        checksum_field = after_info["opt"] + 64
        self.assertEqual(independent_checksum(self.after, checksum_field),
                         struct.unpack_from("<I", self.after, checksum_field)[0])
        type(self).scope = dict(
            input_sha256=sha(self.before), output_sha256=sha(self.after),
            original_ranges=[list(item) for item in expected_ranges],
            helper_size=EXPECTED_HELPER_SIZE, input_helper_size=EXPECTED_INPUT_HELPER_SIZE,
            section_flags=hex(section["flags"]), section_virtual_size=section["virtual_size"],
            section_raw_size=section["raw_size"], callsite=hex(patch.CALL_VA),
            checksum_independent=True, other_bytes_identical=True,
            existing_fixes_unchanged=True,
            shared_bitmap_unchanged=True, shared_gate_unchanged=True,
            size_image_unchanged=True, file_size_unchanged=True,
        )
        type(self).completed.add("scope")

    @unittest.skipUnless(os.name == "nt", "Windows ImageHlp checksum check")
    def test_windows_imagehlp_checksum(self):
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

    def test_input_and_output_protection(self):
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
        type(self).protection = dict(input_hash_stable=True, output_hash_stable=True,
                                     existing_output_preserved=True, same_path_rejected=True,
                                     wrong_sha_and_length_rejected=True)
        type(self).completed.add("protection")

    def test_helper_generation_has_no_mutable_global_state(self):
        mutable = {}
        for name, value in vars(patch).items():
            if name.startswith("__"):
                continue
            if isinstance(value, (list, dict, set, bytearray)):
                mutable[name] = copy.deepcopy(value)
        first = patch.helper()
        second = patch.helper()
        self.assertEqual(first, second)
        self.assertEqual(first, self.after[patch.NEW_RAW:patch.NEW_RAW + EXPECTED_HELPER_SIZE])
        for name, value in mutable.items():
            self.assertEqual(vars(patch)[name], value, "helper mutated global %s" % name)
        for code in code_units(patch.helper.__code__):
            for instruction in dis.get_instructions(code):
                self.assertNotIn(instruction.opname,
                                 {"STORE_GLOBAL", "STORE_NAME", "DELETE_GLOBAL", "DELETE_NAME"},
                                 "helper writes module state at %s" % instruction.opname)
        type(self).helper_check = dict(mutable_globals=sorted(mutable),
                                       helper_stable=True, no_global_stores=True)
        type(self).completed.add("helper")

    @classmethod
    def tearDownClass(cls):
        destination = os.environ.get("DNF_ITEM_LINK_RECEIPT")
        if not destination:
            return
        expected = {"matrix", "scope", "imagehlp", "protection", "helper"}
        receipt = dict(passed=expected <= cls.completed,
                       completed=sorted(cls.completed), missing=sorted(expected - cls.completed),
                       input_sha256=sha(cls.before), output_sha256=sha(cls.after),
                       input_size=len(cls.before), output_size=len(cls.after),
                       actual_callsite=hex(patch.CALL_VA),
                       fragment=[hex(patch.FRAGMENT_VA), hex(patch.FRAGMENT_END)],
                       requested_by_class={"0": sorted(REQUESTED_CLASS0),
                                           "1": sorted(REQUESTED_CLASS1)},
                       full_ui_acceptance=False)
        if cls.matrix is not None:
            receipt.update(cls.matrix)
        if cls.scope is not None:
            receipt.update(cls.scope)
        if cls.protection is not None:
            receipt.update(cls.protection)
        if cls.helper_check is not None:
            receipt.update(cls.helper_check)
        target = Path(destination)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    unittest.main(verbosity=2)
