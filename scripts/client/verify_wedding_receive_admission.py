"""独立核对婚姻接收准入候选；执行原生 x86，不启动客户端。

C0/C1 每张映像完整执行 65536 个 opcode。C2..255 使用严格的
opcode 无关回落结构证明，辅以逐类别目标/边界实跑，不宣称执行 16M 原生调用。
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import time

import capstone
import pefile
import unicorn
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
    UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
    UC_X86_REG_EIP, UC_X86_REG_EFLAGS,
)

import patch_wedding_receive_admission as patch


INPUT_SHA = "a5f1d5ef432064cccaeac6362d5f1405fceddd62739da85661154fc008d47f7d"
OUTPUT_SHA = "7ae08949bd575990db56003f848dee511717fb3a382e15561171bc6fa37fad6e"
INPUT_SIZE = 112549888
CLASS0 = (997, 998, 1000, 1002, 1003, 1004, 1007, 1008, 1009, 1013)
CLASS1 = (1032, 1033, 1035, 1036, 1037, 1039, 1045, 1046)
CALL, OLD, NEW = 0x2523A13, 0x6F06100, 0x6F8B400
HELPER_SIZE = 249
HELPER_SHA = "77cb333cec087105695ec16271c89ba3f5619389ccbbf2891c32ff73eb2e0285"
RANGES = ((216, 220), (4064, 4068), (34748947, 34748952), (112546816, 112547065))
TARGETS = {(0, n) for n in CLASS0} | {(1, n) for n in CLASS1}
ALREADY_ALLOWED = {(0, 997), (0, 1000)}
ADDITIONS = TARGETS - ALREADY_ALLOWED
REGISTERS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
             UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP)
NONVOLATILE = (UC_X86_REG_EBX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def file_offset(pe, va, size):
    section = pe.get_section_by_rva(va - pe.OPTIONAL_HEADER.ImageBase)
    require(section is not None, "address outside mapped sections")
    rva = va - pe.OPTIONAL_HEADER.ImageBase
    require(rva + size <= section.VirtualAddress + section.SizeOfRawData, "address exceeds raw section")
    return pe.get_offset_from_rva(rva)


def verify_layout(before, after, report):
    require(len(before) == len(after) == INPUT_SIZE, "EXE grew or wrong input size")
    require(sha(before) == INPUT_SHA and sha(after) == OUTPUT_SHA, "exact input/candidate SHA256 required")
    require(tuple(patch.CLASS0) == CLASS0 and tuple(patch.CLASS1) == CLASS1, "patcher target set differs from reviewed inventory")
    require(report["input_sha256"] == INPUT_SHA and report["output_sha256"] == OUTPUT_SHA, "patch report identity differs")
    require(report["changed_ranges"] == [list(row) for row in RANGES], "patch report changed ranges differ")
    old_pe, new_pe = pefile.PE(data=before, fast_load=True), pefile.PE(data=after, fast_load=True)
    for pe in (old_pe, new_pe):
        require(pe.FILE_HEADER.Machine == 0x14C and pe.OPTIONAL_HEADER.ImageBase == 0x400000, "not reviewed i386 image")
        require(pe.FILE_HEADER.NumberOfSections == 93 and pe.OPTIONAL_HEADER.SizeOfHeaders == 4096, "PE header layout changed")
        require(pe.OPTIONAL_HEADER.SectionAlignment == pe.OPTIONAL_HEADER.FileAlignment == 4096, "PE alignment changed")
        require(pe.verify_checksum(), "PE checksum invalid")
        require(not pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].VirtualAddress and not pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size, "unexpected certificate directory")
    require(old_pe.OPTIONAL_HEADER.SizeOfImage == new_pe.OPTIONAL_HEADER.SizeOfImage, "SizeOfImage grew")
    old_last, new_last = old_pe.sections[-1], new_pe.sections[-1]
    for section in (old_last, new_last):
        require(section.Name.rstrip(b"\0") == b".skprobe" and section.Characteristics == 0x60000020, "owned RX reserve differs")
        require(section.SizeOfRawData == 4096 and section.PointerToRawData + 4096 == INPUT_SIZE, "last raw section differs")
    require(old_last.Misc_VirtualSize == 990 and new_last.Misc_VirtualSize == 1273, "unexpected VirtualSize adjustment")
    require(old_pe.OPTIONAL_HEADER.get_field_absolute_offset("CheckSum") == RANGES[0][0], "checksum offset differs")
    require(old_last.get_file_offset() + 8 == RANGES[1][0], "VirtualSize offset differs")
    require(file_offset(old_pe, CALL, 5) == RANGES[2][0] and file_offset(old_pe, NEW, HELPER_SIZE) == RANGES[3][0], "patch locations differ")
    require(before[RANGES[2][0]:RANGES[2][1]] == b"\xe8" + struct.pack("<i", OLD - CALL - 5), "old caller changed")
    require(after[RANGES[2][0]:RANGES[2][1]] == b"\xe8" + struct.pack("<i", NEW - CALL - 5), "new caller differs")
    require(before[RANGES[3][0]:RANGES[3][1]] == bytes(HELPER_SIZE), "helper reserve was not zero")
    require(sha(after[RANGES[3][0]:RANGES[3][1]]) == HELPER_SHA, "helper differs from approved 249B plan")
    # This whole-file comparison also protects every old helper, native handler,
    # bitmap, import and resource byte outside the four approved ranges.
    restored = bytearray(after)
    actual_runs, changed_count = [], 0
    for lo, hi in RANGES:
        run = None
        for at in range(lo, hi):
            if before[at] != after[at]:
                changed_count += 1
                if run is None:
                    run = at
            elif run is not None:
                actual_runs.append([run, at])
                run = None
        if run is not None:
            actual_runs.append([run, hi])
        restored[lo:hi] = before[lo:hi]
    require(restored == before, "byte changed outside approved ranges")
    require(changed_count == 147, "unexpected actual changed-byte count")
    return dict(pe_checksum_valid=True, unchanged_size=True, size=INPUT_SIZE,
                section_count=93, old_virtual_size=990, new_virtual_size=1273,
                authorized_ranges=[list(row) for row in RANGES], actual_changed_runs=actual_runs,
                actual_changed_bytes=changed_count, old_receive_chain_and_bitmap_unchanged=True,
                all_previous_feature_bytes_unchanged_except_gate_call=True, added_dll=False)


def verify_fallback_structure(after):
    pe = pefile.PE(data=after, fast_load=True)
    at = file_offset(pe, NEW, HELPER_SIZE)
    body = after[at:at + HELPER_SIZE]
    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_32)
    instructions = list(md.disasm(body, NEW))
    require(sum(i.size for i in instructions) == HELPER_SIZE, "helper has undecoded bytes")
    require(body[:3] == bytes.fromhex("803f00") and body[3:5] == b"\x0f\x84", "class0 predicate differs")
    require(body[9:12] == bytes.fromhex("803f01") and body[12:14] == b"\x0f\x85", "class1 predicate differs")
    class0_target = NEW + 9 + struct.unpack_from("<i", body, 5)[0]
    fallback = NEW + 18 + struct.unpack_from("<i", body, 14)[0]
    require(class0_target == NEW + 116 and fallback == NEW + 244, "branch targets differ")
    require(body[244] == 0xE9 and NEW + 249 + struct.unpack_from("<i", body, 245)[0] == OLD, "fallback no longer tail-jumps to original entry")
    require(body[236:244] == bytes.fromhex("b801000000c20400"), "allow path is not EAX1/RET4")
    return dict(classifications=list(range(2, 256)), all_u16_opcodes_proved_by_control_flow=True,
                explanation="For every class2..255, the first four actual instructions branch to the unchanged OLD entry before reading the opcode or changing registers/stack.",
                prefix=[dict(va=hex(i.address), hex=i.bytes.hex(), instruction=(i.mnemonic + " " + i.op_str).strip()) for i in instructions[:4]],
                fallback_va=hex(OLD), full_254_by_65536_native_crossproduct_executed=False)


class Native:
    SCRATCH, SCRATCH_SIZE = 0x10000000, 0x200000
    STACK, HEADER = 0x10010000, 0x10020000
    OPCODES, RESULTS = 0x10030000, 0x10060000
    DRIVER, STOP = 0x100B0000, 0x100B1000

    def __init__(self, data):
        self.u = unicorn.Uc(unicorn.UC_ARCH_X86, unicorn.UC_MODE_32)
        pe = pefile.PE(data=data, fast_load=True)
        base = pe.OPTIONAL_HEADER.ImageBase
        self.u.mem_map(base, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) & ~4095)
        self.u.mem_write(base, data[:pe.OPTIONAL_HEADER.SizeOfHeaders])
        for section in pe.sections:
            self.u.mem_write(base + section.VirtualAddress, section.get_data())
        self.u.mem_map(self.SCRATCH, self.SCRATCH_SIZE)
        self._old_hook = None
        self._old_entries = None
        self._old_argument = None

    def observe_old_entry(self):
        if self._old_hook is not None:
            return

        def at_old(machine, address, size, _):
            if self._old_entries is None:
                return
            self._old_entries.append({reg: machine.reg_read(reg) for reg in REGISTERS})
            require(machine.reg_read(UC_X86_REG_ESP) == self.STACK - 4, "fallback changed entry ESP")
            require(bytes(machine.mem_read(self.STACK - 4, 8)) == struct.pack("<II", CALL + 5, self._old_argument), "fallback changed return address or opcode argument")

        # Batches deliberately compile the native chain without CODE hooks.
        # Unicorn does not retroactively instrument those translation blocks:
        # install one permanent observer, then evict the exact old entry range.
        # Reusing this observer avoids add/remove instrumentation on later calls.
        self._old_hook = self.u.hook_add(unicorn.UC_HOOK_CODE, at_old, begin=OLD, end=OLD)
        self.u.ctl_remove_cache(OLD, OLD + 166)

    def batch(self, classification, opcodes, target):
        opcodes = tuple(opcodes)
        require(0 < len(opcodes) <= 65536, "invalid batch size")
        self.u.mem_write(self.HEADER, bytes([classification]) + bytes(15))
        self.u.mem_write(self.OPCODES, struct.pack("<" + "H" * len(opcodes), *opcodes))
        # Real native callee invoked once per input inside x86, with no Python
        # instruction hooks, synthetic leaf returns or bitmap substitutions.
        code = bytearray(bytes.fromhex("0fb706668947018bc850"))
        code += b"\xe8" + struct.pack("<i", target - (self.DRIVER + len(code) + 5))
        code += bytes.fromhex("890383c60283c3044d")
        code += b"\x0f\x85" + struct.pack("<i", -(len(code) + 6))
        code += b"\xe9" + struct.pack("<i", self.STOP - (self.DRIVER + len(code) + 5))
        self.u.mem_write(self.DRIVER, bytes(code))
        for reg, value in ((UC_X86_REG_ESP, self.STACK), (UC_X86_REG_EDI, self.HEADER),
                           (UC_X86_REG_ESI, self.OPCODES), (UC_X86_REG_EBX, self.RESULTS),
                           (UC_X86_REG_EBP, len(opcodes)), (UC_X86_REG_EFLAGS, 0x202)):
            self.u.reg_write(reg, value)
        self.u.emu_start(self.DRIVER, self.STOP, count=len(opcodes) * 2048)
        require(self.u.reg_read(UC_X86_REG_EIP) == self.STOP and self.u.reg_read(UC_X86_REG_ESP) == self.STACK, "native batch did not return with balanced stack")
        require(self.u.reg_read(UC_X86_REG_EDI) == self.HEADER and self.u.reg_read(UC_X86_REG_EBP) == 0, "native callee corrupted batch nonvolatile registers")
        require(self.u.reg_read(UC_X86_REG_ESI) == self.OPCODES + 2 * len(opcodes) and self.u.reg_read(UC_X86_REG_EBX) == self.RESULTS + 4 * len(opcodes), "native callee corrupted batch pointers")
        values = bytes(self.u.mem_read(self.RESULTS, 4 * len(opcodes)))
        require(all(n[0] in (0, 1) for n in struct.iter_unpack("<I", values)), "gate returned a non-boolean value")
        return values

    def call_site(self, classification, opcode, high_word=0):
        self.observe_old_entry()
        argument = (high_word << 16) | opcode
        header = bytes([classification]) + struct.pack("<H", opcode) + bytes(range(13))
        self.u.mem_write(self.HEADER, header)
        guard = bytes(range(64))
        self.u.mem_write(self.STACK, struct.pack("<I", argument) + guard)
        initial = {UC_X86_REG_EAX: 0x12345678, UC_X86_REG_EBX: 0x23456789,
                   UC_X86_REG_ECX: 0x3456789A, UC_X86_REG_EDX: 0x456789AB,
                   UC_X86_REG_ESI: 0x56789ABC, UC_X86_REG_EDI: self.HEADER,
                   UC_X86_REG_EBP: 0x6789ABCD, UC_X86_REG_ESP: self.STACK}
        for reg, value in initial.items():
            self.u.reg_write(reg, value)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x202)
        self._old_entries = []
        self._old_argument = argument
        try:
            self.u.emu_start(CALL, CALL + 5, count=2048)
        finally:
            entries = self._old_entries
            self._old_entries = None
            self._old_argument = None
        final = {reg: self.u.reg_read(reg) for reg in REGISTERS}
        require(self.u.reg_read(UC_X86_REG_EIP) == CALL + 5 and final[UC_X86_REG_ESP] == self.STACK + 4, "actual caller/RET4 ABI differs")
        require(all(final[reg] == initial[reg] for reg in NONVOLATILE), "nonvolatile register changed")
        require(bytes(self.u.mem_read(self.STACK + 4, 64)) == guard and bytes(self.u.mem_read(self.HEADER, 16)) == header, "caller stack/header guard changed")
        require(final[UC_X86_REG_EAX] in (0, 1), "non-boolean caller result")
        return final, entries, initial


def verify_full_matrices(original, candidate):
    rows, deltas = [], []
    for classification in (0, 1):
        old_values = original.batch(classification, range(65536), OLD)
        new_values = candidate.batch(classification, range(65536), NEW)
        for opcode, (old, new) in enumerate(zip(struct.iter_unpack("<I", old_values), struct.iter_unpack("<I", new_values))):
            expected = old[0] or ((classification, opcode) in TARGETS)
            require(new[0] == int(expected), "unexpected admission/loss at class%d/op%d" % (classification, opcode))
            if old != new:
                deltas.append(dict(classification=classification, opcode=opcode, before=old[0], after=new[0]))
        rows.append(dict(classification=classification, opcodes_per_image=65536,
                         baseline_results_sha256=sha(old_values), candidate_results_sha256=sha(new_values),
                         actual_native_chains_executed=True, synthetic_fallback=False))
    require({(row["classification"], row["opcode"]) for row in deltas} == ADDITIONS and len(deltas) == 16, "admission delta is not exactly the reviewed 16 pairs")
    return dict(status="passed", classes=rows, deltas=deltas,
                native_calls_baseline=131072, native_calls_candidate=131072,
                total_actual_native_gate_calls=262144)


def verify_abi(original, candidate, classifications=range(256)):
    opcodes = sorted(set(CLASS0 + CLASS1 + (0, 1, 996, 999, 1001, 1014, 1031, 1034, 1038, 1040, 1044, 1047, 0x7F01, 0x7F10, 65535)))
    cases, retained, digest = 0, [], hashlib.sha256()
    for classification in classifications:
        for opcode in opcodes:
            old, old_entries, _ = original.call_site(classification, opcode)
            new, entries, initial = candidate.call_site(classification, opcode)
            target = (classification, opcode) in TARGETS
            require(len(old_entries) == 1, "baseline did not enter OLD exactly once")
            if target:
                require(not entries and new[UC_X86_REG_EAX] == 1 and new[UC_X86_REG_ECX] == initial[UC_X86_REG_ECX], "target path failed to allow/preserve ECX")
            else:
                require(len(entries) == 1 and new == old, "non-target native result/registers changed")
                for reg in (UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP):
                    require(entries[0][reg] == old_entries[0][reg], "fallback input ABI differs")
            record = dict(classification=classification, opcode=opcode, target=target,
                          old_eax=old[UC_X86_REG_EAX], new_eax=new[UC_X86_REG_EAX], fallback_entries=len(entries))
            digest.update(json.dumps(record, sort_keys=True).encode("ascii"))
            if target or (classification in (0, 1, 2, 255) and opcode in (0, 1001, 65535)):
                retained.append(record)
            cases += 1
    # The helper and the old chain both consume the complete u16 argument,
    # independently of unused upper stack-argument bits.
    high_word_cases = 0
    for classification, opcode in ((0, 1002), (1, 1032), (0, 1001), (1, 65535), (255, 1032)):
        old, _, _ = original.call_site(classification, opcode, 0xCAFE)
        new, entries, initial = candidate.call_site(classification, opcode, 0xCAFE)
        if (classification, opcode) in TARGETS:
            require(new[UC_X86_REG_EAX] == 1 and new[UC_X86_REG_ECX] == initial[UC_X86_REG_ECX] and not entries, "u16 target ABI differs with high argument bits")
        else:
            require(new == old and len(entries) == 1, "u16 non-target ABI differs with high argument bits")
        high_word_cases += 1
    return dict(status="passed", cases_per_image=cases, high_word_cases_per_image=high_word_cases,
                classifications_tested=len(tuple(classifications)), sampled_opcodes=opcodes,
                native_call_site=hex(CALL), fallback_entry_checked=hex(OLD),
                observer="permanent OLD-entry hook installed after matrix, with OLD..OLD+166 translation-cache eviction",
                preserved=["EBX", "ESI", "EDI", "EBP", "RET4/ESP", "caller stack/header guard"],
                ecx="targets preserve incoming ECX; non-targets preserve exact baseline result and fallback entry ECX",
                case_summary_sha256=digest.hexdigest(), selected_cases=retained)


def output_paths(input_path, report_path):
    input_path, report_path = Path(input_path).resolve(), Path(report_path).resolve()
    assets = report_path.parent / (report_path.stem + "-assets")
    original, candidate = assets / "input-A5.exe", assets / "candidate.exe"
    require(len({input_path, report_path, original, candidate}) == 4, "input/in-place output paths overlap")
    require(not report_path.exists() and not assets.exists(), "preserve existing report/assets; choose a new evidence path")
    return input_path, report_path, assets, original, candidate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    input_path, report_path, assets, original_path, candidate_path = output_paths(args.input, args.report)
    before = input_path.read_bytes()
    started = time.perf_counter()
    after, patch_report = patch.build(before)
    layout = verify_layout(before, after, patch_report)
    structure = verify_fallback_structure(after)
    prepare_seconds = time.perf_counter() - started
    # Keep both immutable source images independently of the later installation.
    assets.mkdir(parents=True, exist_ok=False)
    with original_path.open("xb") as stream:
        stream.write(before)
    with candidate_path.open("xb") as stream:
        stream.write(after)
    original, candidate = Native(before), Native(after)
    matrix_started = time.perf_counter()
    matrices = verify_full_matrices(original, candidate)
    matrix_seconds = time.perf_counter() - matrix_started
    abi_started = time.perf_counter()
    abi = verify_abi(original, candidate)
    abi_seconds = time.perf_counter() - abi_started
    require(sha(input_path.read_bytes()) == INPUT_SHA and sha(original_path.read_bytes()) == INPUT_SHA and sha(candidate_path.read_bytes()) == OUTPUT_SHA, "source or independent proof assets changed")
    report = dict(status="passed", input_sha256=INPUT_SHA, output_sha256=OUTPUT_SHA,
                  input_path=str(input_path), original_proof_image=str(original_path), candidate_proof_image=str(candidate_path),
                  layout=layout, other_classes_control_flow=structure, matrices=matrices, abi=abi,
                  timings_seconds=dict(build_and_layout=prepare_seconds, full_native_matrices=matrix_seconds,
                                       sampled_native_abi=abi_seconds, total=time.perf_counter() - started),
                  tools=dict(python=__import__("sys").version, unicorn=unicorn.__version__, capstone=capstone.__version__, pefile=pefile.__version__),
                  adapters=[], gameplay_acceptance="pending", no_client_process_started=True, input_unchanged=True,
                  limitations=["C2..255全u16采用opcode无关控制流证明和逐类别抽样，不宣称16M原生调用", "只验证原生接收准入及ABI，非图形UI/联网游戏验收"])
    with report_path.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps(dict(status=report["status"], output_sha256=OUTPUT_SHA, report=str(report_path),
                          full_matrix_native_calls=262144, abi_cases_per_image=abi["cases_per_image"]), ensure_ascii=False))


if __name__ == "__main__":
    main()
