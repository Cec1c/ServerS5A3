"""Exhaustive offline verifier for fiendwar-rosen-blackscreen-20261126 candidate v1.

Verifies:
  1. Strict allowed-byte-offset diff against baseline D7774894... (ZERO unintended edits).
  2. PE integrity: 94 sections preserved, identical file size, recalculated checksum valid.
  3. All prior patches intact (party-attack-sync-20261125 v4 hooks & helpers, .rprior, multi270).
  4. Capstone disassembly of all 12 patched sites (with surrounding context) and all 12 helpers.
  5. Unicorn x86-32 emulation of 20 normal + fault/recovery scenarios (with 0x00000000 UNMAPPED).
"""

from pathlib import Path
import argparse
import hashlib
import json
import struct
import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
    UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP,
    UC_X86_REG_EIP, UC_X86_REG_EFLAGS, UC_X86_REG_GDTR, UC_X86_REG_FS,
    UC_X86_REG_CS, UC_X86_REG_DS, UC_X86_REG_ES, UC_X86_REG_SS,
)

import build_blackscreen as bc

BASE_PATH: Path
CAND_PATH: Path
RECEIPT_PATH: Path
VERIFY_OUT_PATH: Path


def va_to_off(pe: pefile.PE, va: int) -> int:
    rva = va - bc.IMAGE_BASE
    for s in pe.sections:
        if s.VirtualAddress <= rva < s.VirtualAddress + max(s.Misc_VirtualSize, s.SizeOfRawData):
            return s.PointerToRawData + (rva - s.VirtualAddress)
    raise ValueError(f"VA 0x{va:08x} unmapped")


def verify_diff_and_pe(orig: bytes, cand: bytes, receipt: dict) -> dict:
    assert len(orig) == len(cand) == 114032640, "file size changed"
    orig_sha = hashlib.sha256(orig).hexdigest().upper()
    cand_sha = hashlib.sha256(cand).hexdigest().upper()
    assert orig_sha == bc.INPUT_SHA256
    assert cand_sha == receipt["output_sha256"]

    pe_orig = pefile.PE(data=orig, fast_load=True)
    pe_cand = pefile.PE(data=cand, fast_load=True)
    assert len(pe_orig.sections) == len(pe_cand.sections) == 94

    # Verify PE checksum
    gen_csum = pe_cand.generate_checksum()
    hdr_csum = pe_cand.OPTIONAL_HEADER.CheckSum
    assert gen_csum == hdr_csum, f"checksum mismatch: hdr=0x{hdr_csum:08x} gen=0x{gen_csum:08x}"

    # Verify all prior party-attack-sync-20261125 v4 hooks & code untouched
    prior_checks = [
        (0x01FF2F34, 5),
        (0x022C407A, 5),
        (0x0370DB65, 5),
        (0x03703663, 5),
        (0x03739CF2, 5),
        (0x06EAF500, 0x422),
        (0x01649F22, 8),
        (0x028E817F, 8),
    ]
    for va, sz in prior_checks:
        off = va_to_off(pe_orig, va)
        assert orig[off : off + sz] == cand[off : off + sz], f"prior patch at 0x{va:08x} modified!"

    # Build strict allowed file-offset intervals
    allowed = []
    csum_off = int(receipt["checksum_file_offset"], 16)
    allowed.append((csum_off, csum_off + 4, "pe_checksum"))
    for su in receipt["section_header_updates"]:
        off = int(su["vsize_file_offset"], 16)
        allowed.append((off, off + 4, f"section_vsize:{su['section']}"))
    cc_off = int(receipt["code_cave"]["file_offset"], 16)
    cc_sz = receipt["code_cave"]["size"]
    allowed.append((cc_off, cc_off + cc_sz, "code_cave:.a4rx"))
    for hs in receipt["hook_sites"]:
        off = int(hs["file_offset"], 16)
        allowed.append((off, off + hs["size"], f"hook:{hs['name']}"))

    def is_allowed(idx: int) -> bool:
        for lo, hi, _ in allowed:
            if lo <= idx < hi:
                return True
        return False

    # Fast block scan for exact changed byte count
    diff_offsets = []
    block = 65536
    for start in range(0, len(orig), block):
        end = min(start + block, len(orig))
        if orig[start:end] != cand[start:end]:
            for i in range(start, end):
                if orig[i] != cand[i]:
                    assert is_allowed(i), f"UNALLOWED byte diff at file offset 0x{i:08x}"
                    diff_offsets.append(i)

    return {
        "status": "PASSED",
        "size_bytes": len(cand),
        "sections": len(pe_cand.sections),
        "checksum": f"0x{hdr_csum:08x}",
        "changed_byte_count": len(diff_offsets),
        "prior_patches_intact": True,
    }


def verify_disassembly(cand: bytes, pe_cand: pefile.PE, receipt: dict) -> dict:
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    site_summaries = {}
    for hs in receipt["hook_sites"]:
        va = int(hs["va"], 16)
        sz = hs["size"]
        off = va_to_off(pe_cand, va)
        insns = list(md.disasm(cand[off : off + sz + 8], va))
        covered = 0
        hook_lines = []
        for ins in insns:
            if covered < sz:
                hook_lines.append(f"{ins.address:08x}: {ins.mnemonic} {ins.op_str}".strip())
                covered += ins.size
            else:
                hook_lines.append(f"[next] {ins.address:08x}: {ins.mnemonic} {ins.op_str}".strip())
                break
        assert covered == sz, f"instruction boundary broken at {hs['name']} 0x{va:08x}: covered={covered} != {sz}"
        site_summaries[hs["name"]] = hook_lines

    cc_va = int(receipt["code_cave"]["start_va"], 16)
    cc_off = int(receipt["code_cave"]["file_offset"], 16)
    cc_sz = receipt["code_cave"]["size"]
    cc_bytes = cand[cc_off : cc_off + cc_sz]
    ordered = sorted([(hs["name"], int(hs["helper_va"], 16)) for hs in receipt["hook_sites"]], key=lambda x: x[1])
    helper_counts = {}
    for idx, (nm, hva) in enumerate(ordered):
        end_va = ordered[idx + 1][1] if idx + 1 < len(ordered) else cc_va + cc_sz
        raw = cc_bytes[hva - cc_va : end_va - cc_va].rstrip(b"\xcc")
        insns = list(md.disasm(raw, hva))
        assert sum(i.size for i in insns) == len(raw), f"helper {nm} bad decode"
        helper_counts[nm] = len(insns)
    return {"status": "PASSED", "sites": site_summaries, "helper_insn_counts": helper_counts}


def gdt_entry(base: int, limit: int, access: int, flags: int) -> bytes:
    return struct.pack(
        "<HHBBBB",
        limit & 0xFFFF,
        base & 0xFFFF,
        (base >> 16) & 0xFF,
        access & 0xFF,
        ((limit >> 16) & 0x0F) | ((flags & 0x0F) << 4),
        (base >> 24) & 0xFF,
    )


def make_emu(cand: bytes, pe_cand: pefile.PE):
    mu = Uc(UC_ARCH_X86, UC_MODE_32)

    # Note: 0x00000000 is intentionally UNMAPPED so any NULL deref immediately faults!
    regions = [
        (0x037F7000, 0x2000),  # LoadVer5
        (0x03808000, 0x1000),  # GetSprite
        (0x03814000, 0x3000),  # ResetDevices + RenderViaTexture + Present
        (0x03823000, 0x1000),  # PeriodicGC stub
        (0x03849000, 0x1000),  # ScreenEffectShader
        (0x0387C000, 0x3000),  # SpriteBatch
        (0x038E7000, 0x1000),  # UpscaleShader stub
        (0x03E7C000, 0x2000),  # fread stub
        (0x05845000, 0x2000),  # D3D globals
        (0x06590000, 0x1000),  # SpriteBatch globals
        (0x06E85000, 0x1000),  # IAT
        (0x06EAF000, 0x2000),  # .a4rx + .a4rw
        (0x00100000, 0x10000), # Stack
        (0x00200000, 0x3000),  # TIB (FS base @ 0x200000) + scratch + GDT (@ 0x202000)
        (0x00300000, 0x1000),  # Synthetic stubs & return sentinels
    ]
    for base, sz in regions:
        mu.mem_map(base, sz)

    # Configure real 32-bit GDT so FS points to TIB at 0x00200000 while 0x00000000 stays unmapped
    GDT_ADDR = 0x00202000
    gdt = (
        gdt_entry(0, 0, 0, 0)                            # 0x00 null
        + gdt_entry(0, 0xFFFFF, 0x9A, 0xC)               # 0x08 flat code
        + gdt_entry(0, 0xFFFFF, 0x92, 0xC)               # 0x10 flat data
        + gdt_entry(0x00200000, 0x0FFF, 0x92, 0x4)       # 0x18 RW FS -> 0x00200000
    )
    mu.mem_write(GDT_ADDR, gdt)
    mu.reg_write(UC_X86_REG_GDTR, (0, GDT_ADDR, len(gdt) - 1, 0))
    mu.reg_write(UC_X86_REG_CS, 0x08)
    mu.reg_write(UC_X86_REG_DS, 0x10)
    mu.reg_write(UC_X86_REG_ES, 0x10)
    mu.reg_write(UC_X86_REG_SS, 0x10)
    mu.reg_write(UC_X86_REG_FS, 0x18)

    # Load patched bytes from candidate EXE for all code regions + .a4rx
    for va, sz in [
        (0x037F7700, 0x20),
        (0x038080C0, 0x10),
        (0x03814480, 0x20),
        (0x03815640, 0x20),
        (0x03815B10, 0x80),
        (0x038490B0, 0x20),
        (0x0387C7DC, 0x10),
        (0x0387D008, 0x10),
        (0x0387DAA0, 0x10),
        (0x0387DF45, 0x10),
        (0x0387E1F1, 0x10),
        (0x06EAF980, 0x600),
    ]:
        off = va_to_off(pe_cand, va)
        mu.mem_write(va, cand[off : off + sz])

    STUB_GETTICK = 0x00300100
    mu.mem_write(STUB_GETTICK, b"\xc3")
    mu.mem_write(bc.IAT_GETTICKCOUNT, struct.pack("<I", STUB_GETTICK))

    mu.mem_write(bc.FN_CND3D_RESET_DEVICES, b"\xc2\x0c\x00")
    mu.mem_write(bc.FN_CNIMGMGR_PERIODIC_GC, b"\xc3")
    mu.mem_write(bc.FN_SB_UPDATE_DEVICE_LOST, b"\xc3")
    mu.mem_write(bc.FN_UPSCALE_GET_TEXTURE, b"\xc3")
    mu.mem_write(bc.FN_FREAD, b"\xc3")

    def wr32(va, val):
        mu.mem_write(va, struct.pack("<I", val & 0xFFFFFFFF))

    def rd32(va):
        return struct.unpack("<I", mu.mem_read(va, 4))[0]

    def wr8(va, val):
        mu.mem_write(va, bytes([val & 0xFF]))

    def rd8(va):
        return mu.mem_read(va, 1)[0]

    def reset_state():
        mu.mem_write(bc.DATA_VA, b"\x00" * bc.DATA_SIZE)
        for v in (bc.VA_SB_VB, bc.VA_SB_IB0, bc.VA_SB_IB1, bc.VA_SB_IB2):
            wr32(v, 0x11110000 + (v & 0xFF))
        wr8(bc.VA_SB_LOST, 0)
        wr8(bc.VA_SB_INBATCH, 0)
        wr32(bc.VA_ND3D_DEVICE_PTR, 0x22220000)
        wr32(bc.VA_D3D_DEVICE_PTR, 0x33330000)
        wr32(bc.VA_PP_WIDTH, 1280)
        wr32(bc.VA_PP_HEIGHT, 720)
        mu.reg_write(UC_X86_REG_ESP, 0x00108000)
        mu.reg_write(UC_X86_REG_EBP, 0x00108100)

    return mu, STUB_GETTICK, wr32, rd32, wr8, rd8, reset_state


def run_unicorn_tests(cand: bytes, pe_cand: pefile.PE) -> dict:
    mu, STUB_GETTICK, wr32, rd32, wr8, rd8, reset_state = make_emu(cand, pe_cand)
    results = []

    state = {
        "tick": 10000,
        "reset_hr": 1,
        "reset_restores_vbs": True,
        "reset_calls": [],
        "gc_calls": 0,
        "upd_lost_calls": 0,
        "upscale_ret": 0xAABBCCDD,
        "fread_calls": 0,
        "stop_addrs": set(),
    }

    def hook_code(uc, address, size, user_data):
        if address in state["stop_addrs"]:
            uc.emu_stop()
            return
        if address == STUB_GETTICK:
            uc.reg_write(UC_X86_REG_EAX, state["tick"])
        elif address == bc.FN_CND3D_RESET_DEVICES:
            esp = uc.reg_read(UC_X86_REG_ESP)
            w = struct.unpack("<I", uc.mem_read(esp + 4, 4))[0]
            h = struct.unpack("<I", uc.mem_read(esp + 8, 4))[0]
            force = struct.unpack("<I", uc.mem_read(esp + 12, 4))[0]
            ecx = uc.reg_read(UC_X86_REG_ECX)
            state["reset_calls"].append((ecx, w, h, force))
            if state["reset_restores_vbs"]:
                for v in (bc.VA_SB_VB, bc.VA_SB_IB0, bc.VA_SB_IB1, bc.VA_SB_IB2):
                    wr32(v, 0x44440000 + (v & 0xFF))
            uc.reg_write(UC_X86_REG_EAX, state["reset_hr"] & 0xFFFFFFFF)
        elif address == bc.FN_CNIMGMGR_PERIODIC_GC:
            state["gc_calls"] += 1
        elif address == bc.FN_SB_UPDATE_DEVICE_LOST:
            state["upd_lost_calls"] += 1
        elif address == bc.FN_UPSCALE_GET_TEXTURE:
            uc.reg_write(UC_X86_REG_EAX, state["upscale_ret"] & 0xFFFFFFFF)
        elif address == bc.FN_FREAD:
            state["fread_calls"] += 1
            uc.reg_write(UC_X86_REG_EAX, 1)

    mu.hook_add(UC_HOOK_CODE, hook_code)

    def clear_mocks():
        state["reset_calls"].clear()
        state["gc_calls"] = 0
        state["upd_lost_calls"] = 0
        state["fread_calls"] = 0
        state["stop_addrs"] = set()

    # T01: raw_reset_fix succeeds & restores VBs -> jumps to 0x03815B41
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0)
    state["tick"] = 5000; state["reset_hr"] = 1; state["reset_restores_vbs"] = True
    state["stop_addrs"] = {0x03815B29, 0x03815B41}
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    mu.emu_start(0x03815B17, 0x03815B50)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B41
    assert state["reset_calls"] == [(0x0581A400, 1280, 720, 1)]
    assert state["upd_lost_calls"] == 1
    assert rd32(bc.DATA_CNT_RAW_RESET_FIXED) == 1
    assert rd32(bc.DATA_RECOVER_BUSY) == 0
    results.append("T01_raw_reset_fix_success: PASS")

    # T02: raw_reset_fix fails (0x80000003) -> jumps to 0x03815B29
    reset_state(); clear_mocks()
    state["tick"] = 6000; state["reset_hr"] = 0x80000003; state["reset_restores_vbs"] = False
    state["stop_addrs"] = {0x03815B29, 0x03815B41}
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    mu.emu_start(0x03815B17, 0x03815B50)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B29
    assert rd32(bc.DATA_LAST_RECOVER_HR) == 0x80000003
    assert rd32(bc.DATA_RECOVER_BUSY) == 0
    results.append("T02_raw_reset_fix_fail: PASS")

    # T03: raw_reset_fix 250ms cooldown blocks rapid spam, allows retry at +250ms
    clear_mocks()
    state["tick"] = 6100; state["reset_hr"] = 1; state["reset_restores_vbs"] = True
    state["stop_addrs"] = {0x03815B29, 0x03815B41}
    mu.emu_start(0x03815B17, 0x03815B50)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B29
    assert state["reset_calls"] == []
    state["tick"] = 6250
    mu.emu_start(0x03815B17, 0x03815B50)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B41
    assert len(state["reset_calls"]) == 1
    results.append("T03_raw_reset_fix_cooldown: PASS")

    # T04: raw_reset_fix returns 1 but VB still NULL -> does NOT jump to 0x03815B41
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_IB1, 0)
    state["tick"] = 7000; state["reset_hr"] = 1; state["reset_restores_vbs"] = False
    state["stop_addrs"] = {0x03815B29, 0x03815B41}
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    mu.emu_start(0x03815B17, 0x03815B50)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B29
    results.append("T04_raw_reset_fix_null_vb_rejected: PASS")

    # T05: resetdevices_exit success clears need_recovery & restores FS:[0]
    reset_state(); clear_mocks()
    wr32(bc.DATA_NEED_RECOVERY, 1)
    ebp = mu.reg_read(UC_X86_REG_EBP)
    wr32(ebp - 0x0C, 0xDEADBEEF)
    mu.reg_write(UC_X86_REG_EAX, 1)
    state["stop_addrs"] = {0x0381448D}
    mu.emu_start(0x03814483, 0x03814495)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0381448D
    assert mu.reg_read(UC_X86_REG_EAX) == 1
    assert rd32(bc.DATA_NEED_RECOVERY) == 0
    assert rd32(0x00200000) == 0xDEADBEEF
    results.append("T05_resetdevices_exit_ok: PASS")

    # T06: resetdevices_exit negative HR sets need_recovery=1 & preserves EAX
    reset_state(); clear_mocks()
    ebp = mu.reg_read(UC_X86_REG_EBP)
    wr32(ebp - 0x0C, 0x12345678)
    mu.reg_write(UC_X86_REG_EAX, 0x8876086C)
    state["stop_addrs"] = {0x0381448D}
    mu.emu_start(0x03814483, 0x03814495)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0381448D
    assert mu.reg_read(UC_X86_REG_EAX) == 0x8876086C
    assert rd32(bc.DATA_NEED_RECOVERY) == 1
    assert rd32(0x00200000) == 0x12345678
    results.append("T06_resetdevices_exit_fail_hr: PASS")

    # T07: resetdevices_exit positive HR with NULL VB sets need_recovery=1
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_IB1, 0)
    mu.reg_write(UC_X86_REG_EAX, 1)
    state["stop_addrs"] = {0x0381448D}
    mu.emu_start(0x03814483, 0x03814495)
    assert rd32(bc.DATA_NEED_RECOVERY) == 1
    results.append("T07_resetdevices_exit_null_vb: PASS")

    # T08: present_selfheal healthy fast path -> ONLY PeriodicGC, zero ResetDevices
    reset_state(); clear_mocks()
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    state["stop_addrs"] = {0x03815B7D}
    mu.emu_start(0x03815B78, 0x03815B80)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B7D
    assert state["gc_calls"] == 1
    assert state["reset_calls"] == []
    assert rd32(bc.DATA_CNT_SELF_HEAL_TRY) == 0
    results.append("T08_present_selfheal_healthy_fastpath: PASS")

    # T09: present_selfheal triggers when VB==NULL, calls ResetDevices(w,h,1), clears need_recovery
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0)
    wr32(bc.DATA_NEED_RECOVERY, 1)
    state["tick"] = 10000; state["reset_hr"] = 1; state["reset_restores_vbs"] = True
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    state["stop_addrs"] = {0x03815B7D}
    mu.emu_start(0x03815B78, 0x03815B80)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x03815B7D
    assert state["gc_calls"] == 1
    assert state["reset_calls"] == [(0x0581A400, 1280, 720, 1)]
    assert state["upd_lost_calls"] == 1
    assert rd32(bc.DATA_CNT_SELF_HEAL_TRY) == 1
    assert rd32(bc.DATA_CNT_SELF_HEAL_OK) == 1
    assert rd32(bc.DATA_NEED_RECOVERY) == 0
    assert rd32(bc.DATA_RECOVER_BUSY) == 0
    results.append("T09_present_selfheal_recovery_ok: PASS")

    # T10: present_selfheal OOM failure -> 500ms cooldown -> retry succeeds
    reset_state(); clear_mocks()
    wr32(bc.DATA_NEED_RECOVERY, 1)
    wr32(bc.VA_SB_VB, 0)
    state["tick"] = 20000; state["reset_hr"] = 0x8007000E; state["reset_restores_vbs"] = False
    mu.reg_write(UC_X86_REG_EBX, 0x0581A400)
    state["stop_addrs"] = {0x03815B7D}
    mu.emu_start(0x03815B78, 0x03815B80)
    assert rd32(bc.DATA_CNT_SELF_HEAL_TRY) == 1
    assert rd32(bc.DATA_CNT_SELF_HEAL_OK) == 0
    assert rd32(bc.DATA_NEED_RECOVERY) == 1
    state["tick"] = 20200; state["reset_hr"] = 1; state["reset_restores_vbs"] = True
    mu.emu_start(0x03815B78, 0x03815B80)
    assert rd32(bc.DATA_CNT_SELF_HEAL_TRY) == 1
    assert rd32(bc.DATA_CNT_SELF_HEAL_OK) == 0
    state["tick"] = 20500
    mu.emu_start(0x03815B78, 0x03815B80)
    assert rd32(bc.DATA_CNT_SELF_HEAL_TRY) == 2
    assert rd32(bc.DATA_CNT_SELF_HEAL_OK) == 1
    assert rd32(bc.DATA_NEED_RECOVERY) == 0
    results.append("T10_present_selfheal_oom_cooldown_retry: PASS")

    # T11 & T12: createvbs_check all valid vs each VB NULL
    reset_state(); clear_mocks()
    state["stop_addrs"] = {0x0387D012}
    mu.emu_start(0x0387D00D, 0x0387D016)
    assert mu.reg_read(UC_X86_REG_EAX) == 1
    assert rd32(bc.DATA_CNT_VB_ALLOC_FAIL) == 0
    for idx, v in enumerate((bc.VA_SB_VB, bc.VA_SB_IB0, bc.VA_SB_IB1, bc.VA_SB_IB2), 1):
        reset_state()
        wr32(bc.DATA_CNT_VB_ALLOC_FAIL, idx - 1)
        wr32(v, 0)
        state["stop_addrs"] = {0x0387D012}
        mu.emu_start(0x0387D00D, 0x0387D016)
        assert mu.reg_read(UC_X86_REG_EAX) == 0x8007000E
        assert rd32(bc.DATA_CNT_VB_ALLOC_FAIL) == idx
    results.append("T11_T12_createvbs_check_all_paths: PASS")

    # T13: update_lost normal vs missing VB vs device lost
    RET_SENTINEL = 0x00300200
    mu.mem_write(RET_SENTINEL, b"\x90")
    for al_in, null_vb, expected_lost in [(0, None, 0), (0, bc.VA_SB_VB, 1), (0, bc.VA_SB_IB2, 1), (1, None, 1)]:
        reset_state(); clear_mocks()
        if null_vb:
            wr32(null_vb, 0)
        esp = mu.reg_read(UC_X86_REG_ESP)
        wr32(esp, RET_SENTINEL)
        mu.reg_write(UC_X86_REG_EAX, al_in)
        state["stop_addrs"] = {RET_SENTINEL}
        mu.emu_start(0x0387C7DC, RET_SENTINEL + 1)
        assert rd8(bc.VA_SB_LOST) == expected_lost
    results.append("T13_update_lost_sync: PASS")

    # T14: sb_da80_guard normal (flags check) vs NULL VB vs lost==1
    reset_state(); clear_mocks()
    mu.reg_write(UC_X86_REG_EAX, 0x100)
    state["stop_addrs"] = {0x0387DAA5, 0x0387DDB0}
    mu.emu_start(0x0387DAA0, 0x0387DDB5)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387DAA5
    ef = mu.reg_read(UC_X86_REG_EFLAGS)
    assert (ef & 0x80) != 0
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0)
    state["stop_addrs"] = {0x0387DAA5, 0x0387DDB0}
    mu.emu_start(0x0387DAA0, 0x0387DDB5)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387DDB0
    assert rd32(bc.DATA_CNT_VB_GUARD_HIT) == 1
    reset_state(); clear_mocks()
    wr8(bc.VA_SB_LOST, 1)
    state["stop_addrs"] = {0x0387DAA5, 0x0387DDB0}
    mu.emu_start(0x0387DAA0, 0x0387DDB5)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387DDB0
    assert rd32(bc.DATA_CNT_VB_GUARD_HIT) == 1
    results.append("T14_sb_da80_guard: PASS")

    # T15: sb_df00_guard normal vs NULL VB
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0x12345678)
    state["stop_addrs"] = {0x0387DF4A, 0x0387E128}
    mu.emu_start(0x0387DF45, 0x0387E130)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387DF4A
    assert mu.reg_read(UC_X86_REG_EAX) == 0x12345678
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0); wr8(bc.VA_SB_INBATCH, 1)
    state["stop_addrs"] = {0x0387DF4A, 0x0387E128}
    mu.emu_start(0x0387DF45, 0x0387E130)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387E128
    assert (mu.reg_read(UC_X86_REG_EAX) & 0xFF) == 1
    assert rd32(bc.DATA_CNT_VB_GUARD_HIT) == 1
    results.append("T15_sb_df00_guard: PASS")

    # T16: sb_e190_guard normal vs NULL VB
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0x87654321)
    state["stop_addrs"] = {0x0387E1F7, 0x0387E3E5}
    mu.emu_start(0x0387E1F1, 0x0387E3F0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387E1F7
    assert mu.reg_read(UC_X86_REG_ECX) == 0x87654321
    reset_state(); clear_mocks()
    wr32(bc.VA_SB_VB, 0); wr8(bc.VA_SB_INBATCH, 1)
    state["stop_addrs"] = {0x0387E1F7, 0x0387E3E5}
    mu.emu_start(0x0387E1F1, 0x0387E3F0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x0387E3E5
    assert (mu.reg_read(UC_X86_REG_EAX) & 0xFF) == 1
    assert rd32(bc.DATA_CNT_VB_GUARD_HIT) == 1
    results.append("T16_sb_e190_guard: PASS")

    # T17: upscale_gettex non-NULL vs NULL fallback to edi (RT[1])
    reset_state(); clear_mocks()
    state["upscale_ret"] = 0xAABBCCDD
    mu.reg_write(UC_X86_REG_EDI, 0x55667788)
    state["stop_addrs"] = {0x03815650}
    mu.emu_start(0x03815649, 0x03815652)
    assert mu.reg_read(UC_X86_REG_EDI) == 0xAABBCCDD
    assert rd32(bc.DATA_CNT_UPSCALE_FALLBACK) == 0
    reset_state(); clear_mocks()
    state["upscale_ret"] = 0
    mu.reg_write(UC_X86_REG_EDI, 0x55667788)
    state["stop_addrs"] = {0x03815650}
    mu.emu_start(0x03815649, 0x03815652)
    assert mu.reg_read(UC_X86_REG_EDI) == 0x55667788
    assert rd32(bc.DATA_CNT_UPSCALE_FALLBACK) == 1
    results.append("T17_upscale_gettex_fallback: PASS")

    # T18: getsprite_guard non-NULL vs NULL this
    reset_state(); clear_mocks()
    mu.reg_write(UC_X86_REG_ECX, 0x09001234)
    esp0 = mu.reg_read(UC_X86_REG_ESP)
    state["stop_addrs"] = {0x038080C5}
    mu.emu_start(0x038080C0, 0x038080CC)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x038080C5
    assert mu.reg_read(UC_X86_REG_EBP) == esp0 - 4
    assert mu.reg_read(UC_X86_REG_ESP) == esp0 - 8
    reset_state(); clear_mocks()
    OUT_SP = 0x00201000
    wr32(OUT_SP, 0xCCCCCCCC); wr32(OUT_SP + 4, 0xDDDDDDDD)
    esp0 = mu.reg_read(UC_X86_REG_ESP)
    wr32(esp0, RET_SENTINEL)
    wr32(esp0 + 4, OUT_SP)
    wr32(esp0 + 8, 7)
    mu.reg_write(UC_X86_REG_ECX, 0)
    state["stop_addrs"] = {RET_SENTINEL}
    mu.emu_start(0x038080C0, RET_SENTINEL + 1)
    assert mu.reg_read(UC_X86_REG_EIP) == RET_SENTINEL
    assert mu.reg_read(UC_X86_REG_EAX) == OUT_SP
    assert mu.reg_read(UC_X86_REG_ESP) == esp0 + 12
    assert rd32(OUT_SP) == 0 and rd32(OUT_SP + 4) == 0
    assert rd32(bc.DATA_CNT_OOM_GUARD_HIT) == 1
    results.append("T18_getsprite_guard: PASS")

    # T19: loadver5_fread_guard non-NULL buf vs NULL buf
    reset_state(); clear_mocks()
    esp0 = mu.reg_read(UC_X86_REG_ESP)
    wr32(esp0 + 0, 0x00201100)
    wr32(esp0 + 4, 0x100)
    wr32(esp0 + 8, 1)
    wr32(esp0 + 12, 0x00201200)
    wr32(esp0 + 16, 0x100)
    state["stop_addrs"] = {0x037F7716, 0x037F79A6}
    mu.emu_start(0x037F7711, 0x037F79B0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x037F7716
    assert state["fread_calls"] == 1
    assert mu.reg_read(UC_X86_REG_ESP) == esp0
    reset_state(); clear_mocks()
    esp0 = mu.reg_read(UC_X86_REG_ESP)
    wr32(esp0 + 0, 0)
    state["stop_addrs"] = {0x037F7716, 0x037F79A6}
    mu.emu_start(0x037F7711, 0x037F79B0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x037F79A6
    assert state["fread_calls"] == 0
    assert mu.reg_read(UC_X86_REG_ESP) == esp0 + 0x14
    assert rd32(bc.DATA_CNT_OOM_GUARD_HIT) == 1
    results.append("T19_loadver5_fread_guard: PASS")

    # T20: screeneff_guard valid vs out-of-range vs NULL [esi+0x6c]
    OBJ_SE = 0x00201300
    reset_state(); clear_mocks()
    wr32(OBJ_SE + 0x6C, 0x77770000)
    mu.reg_write(UC_X86_REG_ESI, OBJ_SE)
    mu.reg_write(UC_X86_REG_EAX, 2)
    state["stop_addrs"] = {0x038490C6, 0x038491C6}
    mu.emu_start(0x038490BD, 0x038491D0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x038490C6
    assert rd32(bc.DATA_CNT_OOM_GUARD_HIT) == 0
    mu.reg_write(UC_X86_REG_EAX, 5)
    mu.emu_start(0x038490BD, 0x038491D0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x038491C6
    assert rd32(bc.DATA_CNT_OOM_GUARD_HIT) == 0
    wr32(OBJ_SE + 0x6C, 0)
    mu.reg_write(UC_X86_REG_EAX, 2)
    mu.emu_start(0x038490BD, 0x038491D0)
    assert mu.reg_read(UC_X86_REG_EIP) == 0x038491C6
    assert rd32(bc.DATA_CNT_OOM_GUARD_HIT) == 1
    results.append("T20_screeneff_guard: PASS")

    return {"status": "PASSED", "cases_passed": len(results), "cases": results}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline", required=True, type=Path)
    parser.add_argument("--candidate", required=True, type=Path)
    parser.add_argument("--build-receipt", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    global BASE_PATH, CAND_PATH, RECEIPT_PATH, VERIFY_OUT_PATH
    BASE_PATH = args.baseline
    CAND_PATH = args.candidate
    RECEIPT_PATH = args.build_receipt
    VERIFY_OUT_PATH = args.report
    VERIFY_OUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    orig = BASE_PATH.read_bytes()
    cand = CAND_PATH.read_bytes()
    receipt = json.loads(RECEIPT_PATH.read_text(encoding="utf-8"))
    pe_cand = pefile.PE(data=cand, fast_load=True)

    diff_res = verify_diff_and_pe(orig, cand, receipt)
    dis_res = verify_disassembly(cand, pe_cand, receipt)
    emu_res = run_unicorn_tests(cand, pe_cand)

    out = {
        "task_id": "fiendwar-rosen-blackscreen-20261126",
        "revision": "v1",
        "candidate_sha256": receipt["output_sha256"],
        "diff_and_pe": diff_res,
        "disassembly": dis_res,
        "unicorn_emulation": emu_res,
    }
    VERIFY_OUT_PATH.write_text(json.dumps(out, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print("DIFF & PE:", diff_res)
    print("DISASM:   PASSED (", len(dis_res["sites"]), "hook sites,", sum(dis_res["helper_insn_counts"].values()), "helper insns )")
    print("UNICORN:  PASSED (", emu_res["cases_passed"], "groups )")
    for c in emu_res["cases"]:
        print(" ", c)


if __name__ == "__main__":
    main()
