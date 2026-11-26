"""Candidate v1 builder for Fiend War (超时空之战) Rosen boss room black screen + delayed 0xC0000005 crash.

Root cause (proven from anonymized crash log + DNF.exe disassembly):
  1. CND3D::Present (0x03815b17): When IDirect3DDevice9::Present() returns D3DERR_DEVICELOST,
     CND3D::ResetDevices (0x03814390) calls ReleaseAll() BEFORE checking TestCooperativeLevel(),
     freeing SpriteBatch VBs ([0x6590bac..ba0]), RT[0..3], UpscaleShader, and ScreenEffectShader.
     When ResetDevices returns 0x80000003 (still DEVICELOST), the fullscreen foreground fallback
     at 0x03815b17 calls raw IDirect3DDevice9::Reset(&PresentParams) WITHOUT recreating BackBuffer,
     SpriteBatch VBs, RT[0..3], or shaders. Once raw Reset() succeeds, Present() returns S_OK and
     SpriteBatch::UpdateDeviceLost clears [0x6590bcc]=0, permanently trapping the client in a black
     screen (audio still plays, CNImgMgr PeriodicGC keeps running) while VBs/RTs/shaders stay NULL.
  2. SpriteBatch::CreateVBs (0x0387ceb0): Ignores all 4 CreateVertexBuffer/CreateIndexBuffer
     HRESULTs (D3DPOOL_DEFAULT) and returns 1 (success) at 0x0387d00d even when [0x6590bac..ba0]
     failed under 32-bit VA pressure (VS = 3.15..3.20 GiB in Rosen boss room).
  3. Partial Reset / OOM Never Retried: Once IDirect3DDevice9::Reset() succeeds, if CreateVBs,
     RecreateRes (RT[1..3]), UpscaleShader, or ScreenEffectShader fails, Present() returns S_OK
     next frame and ResetDevices is never retried -> permanent black screen in windowed OR fullscreen.
  4. Delayed 0xC0000005 Crash (~1 min later): SpriteBatch::Draw/DrawMesh NULL-check [0x6590bac]
     (silently producing a black screen), while 3 primitive draw functions (0x0387da80, 0x0387df00,
     0x0387e190) dereference [0x6590bac] WITHOUT NULL check (0x0387da80 doesn't even check
     [0x6590bcc]!), causing 0xC0000005 reading 0x00000000 when UI/skill/boss primitive draw runs.
  5. CND3D::RenderViaTexture(1) (0x03815649): Overwrites valid edi (RT[1]) with NULL if
     UpscaleShader::GetTexture() returns NULL (missing fallback that ScreenEffectShader already has).
  6. Secondary OOM Crash Paths at VS = 3.2 GiB: CNImg::GetSprite (0x038080c0) NULL this from failed
     LoadImg, CNImg::LoadVer5 (0x037f7711) unchecked malloc passed to fread, and ScreenEffectShader
     (0x038490b0) unchecked [esi+0x6c] ID3DXEffect* dereference.

Fix (v1, built on 101 baseline D7774894..., all 94 PE sections & prior patches preserved):
  - Code cave in .a4rx starting at 0x06EAF980 (after party-attack-sync-20261125 v4 at 0x06EAF500..0x06EAF922)
  - RW state/telemetry block in .a4rw at 0x06EB0200..0x06EB023F (after party-attack-sync at 0x06EB0160..0x06EB01C7)
  - 12 surgical hooks covering all 6 root causes with zero hot-path overhead.
"""

from pathlib import Path
import argparse
import hashlib
import json
import struct
import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

INPUT_SHA256 = "D7774894A291D3596F2F3B5C3833BAFDECC9BA5C7EB95EA5E2EE98EC2A6135DA"
IMAGE_BASE = 0x00400000

# Existing game VAs / globals
VA_D3D_DEVICE_PTR = 0x05846610
VA_ND3D_DEVICE_PTR = 0x05845FCC
VA_PP_WIDTH = 0x058465D8
VA_PP_HEIGHT = 0x058465DC
VA_SB_VB = 0x06590BAC
VA_SB_IB0 = 0x06590BA8
VA_SB_IB1 = 0x06590BA4
VA_SB_IB2 = 0x06590BA0
VA_SB_LOST = 0x06590BCC
VA_SB_INBATCH = 0x06590BCD

IAT_GETTICKCOUNT = 0x06E85A24

FN_CND3D_RESET_DEVICES = 0x03814390
FN_CNIMGMGR_PERIODIC_GC = 0x03823BE0
FN_SB_UPDATE_DEVICE_LOST = 0x0387C7D0
FN_UPSCALE_GET_TEXTURE = 0x038E7E80
FN_FREAD = 0x03E7CE7A

# Section allocation (reusing existing .a4rx & .a4rw slack; all 94 sections preserved)
CODE_SECTION = ".a4rx"
CODE_BASE_VA = 0x06EAF980
CODE_LIMIT_VA = 0x06EB0000

DATA_SECTION = ".a4rw"
DATA_VA = 0x06EB0200
DATA_SIZE = 0x40

DATA_LAST_RECOVER_TICK = DATA_VA + 0x00
DATA_RECOVER_BUSY = DATA_VA + 0x04
DATA_CNT_RAW_RESET_FIXED = DATA_VA + 0x08
DATA_CNT_VB_ALLOC_FAIL = DATA_VA + 0x0C
DATA_CNT_SELF_HEAL_TRY = DATA_VA + 0x10
DATA_CNT_SELF_HEAL_OK = DATA_VA + 0x14
DATA_CNT_VB_GUARD_HIT = DATA_VA + 0x18
DATA_CNT_UPSCALE_FALLBACK = DATA_VA + 0x1C
DATA_CNT_OOM_GUARD_HIT = DATA_VA + 0x20
DATA_LAST_RECOVER_HR = DATA_VA + 0x24
DATA_NEED_RECOVERY = DATA_VA + 0x28
DATA_LAST_RAW_RESET_TICK = DATA_VA + 0x2C

# Exact 12 hook sites and expected original bytes
HOOK_SITES = {
    "raw_reset_fix": (0x03815B17, bytes.fromhex("a1106684058b088b514068d865840550ffd2")),
    "resetdevices_exit": (0x03814483, bytes.fromhex("8b4df464890d00000000")),
    "present_selfheal": (0x03815B78, bytes.fromhex("e863e00000")),
    "createvbs_check": (0x0387D00D, bytes.fromhex("b801000000")),
    "update_lost": (0x0387C7DC, bytes.fromhex("a2cc0b5906")),
    "sb_da80_guard": (0x0387DAA0, bytes.fromhex("3d401f0000")),
    "sb_df00_guard": (0x0387DF45, bytes.fromhex("a1ac0b5906")),
    "sb_e190_guard": (0x0387E1F1, bytes.fromhex("8b0dac0b5906")),
    "upscale_gettex": (0x03815649, bytes.fromhex("e832280d00")),
    "getsprite_guard": (0x038080C0, bytes.fromhex("558bec6aff")),
    "loadver5_fread_guard": (0x037F7711, bytes.fromhex("e864576800")),
    "screeneff_guard": (0x038490BD, bytes.fromhex("83f8040f8700010000")),
}


class Asm:
    """Minimal deterministic 32-bit x86 assembler with local label fixups."""

    def __init__(self, base_va: int):
        self.base_va = base_va
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[int, str, int, bool]] = []  # (offset, target_or_label, size, is_rel)

    @property
    def cur_va(self) -> int:
        return self.base_va + len(self.buf)

    def label(self, name: str):
        assert name not in self.labels, f"duplicate label {name}"
        self.labels[name] = self.cur_va

    def align(self, n: int = 16, pad: int = 0xCC):
        rem = len(self.buf) % n
        if rem:
            self.buf.extend(bytes([pad]) * (n - rem))

    def emit(self, raw: bytes | str):
        if isinstance(raw, str):
            raw = bytes.fromhex(raw)
        self.buf.extend(raw)

    def _emit_rel32(self, op: bytes, target: int | str):
        self.buf.extend(op)
        pos = len(self.buf)
        self.buf.extend(b"\x00\x00\x00\x00")
        if isinstance(target, str):
            self.fixups.append((pos, target, 4, True))
        else:
            rel = target - (self.base_va + pos + 4)
            struct.pack_into("<i", self.buf, pos, rel)

    def jmp(self, target: int | str):
        self._emit_rel32(b"\xe9", target)

    def call(self, target: int | str):
        self._emit_rel32(b"\xe8", target)

    def jcc(self, cc: str, target: int | str):
        ops = {
            "je": b"\x0f\x84", "jz": b"\x0f\x84",
            "jne": b"\x0f\x85", "jnz": b"\x0f\x85",
            "js": b"\x0f\x88", "jns": b"\x0f\x89",
            "jb": b"\x0f\x82", "jae": b"\x0f\x83",
            "ja": b"\x0f\x87", "jbe": b"\x0f\x86",
        }
        self._emit_rel32(ops[cc], target)

    def call_iat(self, iat_va: int):
        self.emit(b"\xff\x15" + struct.pack("<I", iat_va))

    def inc_mem32(self, va: int):
        self.emit(b"\xff\x05" + struct.pack("<I", va))

    def cmp_mem32_imm8(self, va: int, imm8: int):
        self.emit(b"\x83\x3d" + struct.pack("<I", va) + struct.pack("<b", imm8))

    def cmp_mem8_imm8(self, va: int, imm8: int):
        self.emit(b"\x80\x3d" + struct.pack("<I", va) + struct.pack("<B", imm8))

    def mov_mem32_imm32(self, va: int, imm32: int):
        self.emit(b"\xc7\x05" + struct.pack("<I", va) + struct.pack("<I", imm32))

    def mov_mem32_eax(self, va: int):
        self.emit(b"\xa3" + struct.pack("<I", va))

    def mov_eax_mem32(self, va: int):
        self.emit(b"\xa1" + struct.pack("<I", va))

    def mov_ecx_mem32(self, va: int):
        self.emit(b"\x8b\x0d" + struct.pack("<I", va))

    def mov_edx_mem32(self, va: int):
        self.emit(b"\x8b\x15" + struct.pack("<I", va))

    def mov_al_mem8(self, va: int):
        self.emit(b"\xa0" + struct.pack("<I", va))

    def mov_mem8_al(self, va: int):
        self.emit(b"\xa2" + struct.pack("<I", va))

    def push_mem32(self, va: int):
        self.emit(b"\xff\x35" + struct.pack("<I", va))

    def finalize(self) -> bytes:
        for pos, lbl, size, is_rel in self.fixups:
            assert lbl in self.labels, f"unresolved label {lbl}"
            target_va = self.labels[lbl]
            if is_rel and size == 4:
                rel = target_va - (self.base_va + pos + 4)
                struct.pack_into("<i", self.buf, pos, rel)
            else:
                raise ValueError("unsupported fixup")
        return bytes(self.buf)


def build_helpers() -> tuple[bytes, dict[str, int]]:
    a = Asm(CODE_BASE_VA)
    entries: dict[str, int] = {}

    def check_all_vbs(fail_label: str):
        for va in (VA_SB_VB, VA_SB_IB0, VA_SB_IB1, VA_SB_IB2):
            a.cmp_mem32_imm8(va, 0)
            a.jcc("je", fail_label)

    # 1. HELPER_RAW_RESET_FIX_VA (jumped from 0x03815B17)
    entries["raw_reset_fix"] = a.cur_va
    a.inc_mem32(DATA_CNT_RAW_RESET_FIXED)
    a.cmp_mem32_imm8(DATA_RECOVER_BUSY, 0)
    a.jcc("jne", 0x03815B29)
    a.call_iat(IAT_GETTICKCOUNT)  # eax = now
    a.mov_edx_mem32(DATA_LAST_RAW_RESET_TICK)
    a.emit("85 d2")  # test edx, edx
    a.jcc("je", "raw_run")
    a.emit("8b c8")  # mov ecx, eax
    a.emit("2b ca")  # sub ecx, edx
    a.emit("81 f9 fa 00 00 00")  # cmp ecx, 250
    a.jcc("jb", 0x03815B29)
    a.label("raw_run")
    a.emit("85 c0")  # test eax, eax
    a.jcc("jne", "raw_nz")
    a.emit("40")  # inc eax
    a.label("raw_nz")
    a.mov_mem32_eax(DATA_LAST_RAW_RESET_TICK)
    a.mov_mem32_imm32(DATA_RECOVER_BUSY, 1)
    a.emit("6a 01")  # push 1 (force=1)
    a.push_mem32(VA_PP_HEIGHT)
    a.push_mem32(VA_PP_WIDTH)
    a.emit("8b cb")  # mov ecx, ebx (CND3D*)
    a.call(FN_CND3D_RESET_DEVICES)
    a.mov_mem32_eax(DATA_LAST_RECOVER_HR)
    a.mov_mem32_imm32(DATA_RECOVER_BUSY, 0)
    a.emit("50")  # push eax
    a.call(FN_SB_UPDATE_DEVICE_LOST)
    a.emit("58")  # pop eax
    a.emit("85 c0")  # test eax, eax
    a.jcc("js", 0x03815B29)
    check_all_vbs("raw_fail_ret")
    a.jmp(0x03815B41)  # return 2 (reset succeeded)
    a.label("raw_fail_ret")
    a.jmp(0x03815B29)  # return 0x80000003 (still lost)

    # 2. HELPER_RESETDEVICES_EXIT_VA (jumped from 0x03814483)
    a.align(16)
    entries["resetdevices_exit"] = a.cur_va
    a.emit("85 c0")  # test eax, eax
    a.jcc("js", "rd_need")
    check_all_vbs("rd_need")
    a.mov_mem32_imm32(DATA_NEED_RECOVERY, 0)
    a.jmp("rd_done")
    a.label("rd_need")
    a.mov_mem32_imm32(DATA_NEED_RECOVERY, 1)
    a.label("rd_done")
    a.emit("8b 4d f4")  # mov ecx, dword ptr [ebp - 0xc]
    a.emit("64 89 0d 00 00 00 00")  # mov dword ptr fs:[0], ecx
    a.jmp(0x0381448D)

    # 3. HELPER_PRESENT_SELFHEAL_VA (called from 0x03815B78)
    a.align(16)
    entries["present_selfheal"] = a.cur_va
    a.call(FN_CNIMGMGR_PERIODIC_GC)  # original PeriodicGC(ecx)
    a.cmp_mem32_imm8(DATA_NEED_RECOVERY, 0)
    a.jcc("jne", "sh_check")
    check_all_vbs("sh_check")
    a.emit("c3")  # ret (hot path: 5 mem cmps only!)
    a.label("sh_check")
    a.emit("85 db")  # test ebx, ebx
    a.jcc("je", "sh_ret")
    a.cmp_mem32_imm8(VA_ND3D_DEVICE_PTR, 0)
    a.jcc("je", "sh_ret")
    a.cmp_mem32_imm8(VA_D3D_DEVICE_PTR, 0)
    a.jcc("je", "sh_ret")
    a.cmp_mem32_imm8(DATA_RECOVER_BUSY, 0)
    a.jcc("jne", "sh_ret")
    a.call_iat(IAT_GETTICKCOUNT)
    a.mov_edx_mem32(DATA_LAST_RECOVER_TICK)
    a.emit("85 d2")  # test edx, edx
    a.jcc("je", "sh_run")
    a.emit("8b c8")  # mov ecx, eax
    a.emit("2b ca")  # sub ecx, edx
    a.emit("81 f9 f4 01 00 00")  # cmp ecx, 500
    a.jcc("jb", "sh_ret")
    a.label("sh_run")
    a.emit("85 c0")  # test eax, eax
    a.jcc("jne", "sh_nz")
    a.emit("40")  # inc eax
    a.label("sh_nz")
    a.mov_mem32_eax(DATA_LAST_RECOVER_TICK)
    a.mov_mem32_imm32(DATA_RECOVER_BUSY, 1)
    a.inc_mem32(DATA_CNT_SELF_HEAL_TRY)
    a.emit("6a 01")  # push 1 (force=1)
    a.push_mem32(VA_PP_HEIGHT)
    a.push_mem32(VA_PP_WIDTH)
    a.emit("8b cb")  # mov ecx, ebx (CND3D*)
    a.call(FN_CND3D_RESET_DEVICES)
    a.mov_mem32_eax(DATA_LAST_RECOVER_HR)
    a.emit("85 c0")  # test eax, eax
    a.jcc("js", "sh_after")
    check_all_vbs("sh_after")
    a.inc_mem32(DATA_CNT_SELF_HEAL_OK)
    a.mov_mem32_imm32(DATA_NEED_RECOVERY, 0)
    a.label("sh_after")
    a.call(FN_SB_UPDATE_DEVICE_LOST)
    a.mov_mem32_imm32(DATA_RECOVER_BUSY, 0)
    a.label("sh_ret")
    a.emit("c3")

    # 4. HELPER_CREATEVBS_CHECK_VA (called from 0x0387D00D)
    a.align(16)
    entries["createvbs_check"] = a.cur_va
    check_all_vbs("cvb_fail")
    a.emit("b8 01 00 00 00")  # mov eax, 1
    a.emit("c3")
    a.label("cvb_fail")
    a.inc_mem32(DATA_CNT_VB_ALLOC_FAIL)
    a.emit("b8 0e 00 07 80")  # mov eax, 0x8007000E (E_OUTOFMEMORY)
    a.emit("c3")

    # 5. HELPER_UPDATE_LOST_VA (jumped from 0x0387C7DC)
    a.align(16)
    entries["update_lost"] = a.cur_va
    a.emit("84 c0")  # test al, al
    a.jcc("jne", "ul_store")
    check_all_vbs("ul_lost")
    a.jmp("ul_store")
    a.label("ul_lost")
    a.emit("b0 01")  # mov al, 1
    a.label("ul_store")
    a.mov_mem8_al(VA_SB_LOST)
    a.emit("c3")

    # 6. HELPER_SB_DA80_GUARD_VA (jumped from 0x0387DAA0)
    a.align(16)
    entries["sb_da80_guard"] = a.cur_va
    a.cmp_mem8_imm8(VA_SB_LOST, 0)
    a.jcc("jne", "da80_fail")
    a.cmp_mem32_imm8(VA_SB_VB, 0)
    a.jcc("je", "da80_fail")
    a.emit("3d 40 1f 00 00")  # cmp eax, 0x1f40
    a.jmp(0x0387DAA5)
    a.label("da80_fail")
    a.inc_mem32(DATA_CNT_VB_GUARD_HIT)
    a.jmp(0x0387DDB0)

    # 7. HELPER_SB_DF00_GUARD_VA (jumped from 0x0387DF45)
    a.align(16)
    entries["sb_df00_guard"] = a.cur_va
    a.mov_eax_mem32(VA_SB_VB)
    a.emit("85 c0")  # test eax, eax
    a.jcc("je", "df00_fail")
    a.jmp(0x0387DF4A)
    a.label("df00_fail")
    a.inc_mem32(DATA_CNT_VB_GUARD_HIT)
    a.mov_al_mem8(VA_SB_INBATCH)
    a.jmp(0x0387E128)

    # 8. HELPER_SB_E190_GUARD_VA (jumped from 0x0387E1F1)
    a.align(16)
    entries["sb_e190_guard"] = a.cur_va
    a.mov_ecx_mem32(VA_SB_VB)
    a.emit("85 c9")  # test ecx, ecx
    a.jcc("je", "e190_fail")
    a.jmp(0x0387E1F7)
    a.label("e190_fail")
    a.inc_mem32(DATA_CNT_VB_GUARD_HIT)
    a.mov_al_mem8(VA_SB_INBATCH)
    a.jmp(0x0387E3E5)

    # 9. HELPER_UPSCALE_GETTEX_VA (called from 0x03815649)
    a.align(16)
    entries["upscale_gettex"] = a.cur_va
    a.call(FN_UPSCALE_GET_TEXTURE)
    a.emit("85 c0")  # test eax, eax
    a.jcc("jne", "up_ok")
    a.inc_mem32(DATA_CNT_UPSCALE_FALLBACK)
    a.emit("8b c7")  # mov eax, edi (preserve valid RT[1]!)
    a.label("up_ok")
    a.emit("c3")

    # 10. HELPER_GETSPRITE_GUARD_VA (jumped from 0x038080C0)
    a.align(16)
    entries["getsprite_guard"] = a.cur_va
    a.emit("85 c9")  # test ecx, ecx
    a.jcc("jne", "gs_normal")
    a.inc_mem32(DATA_CNT_OOM_GUARD_HIT)
    a.emit("8b 44 24 04")  # mov eax, dword ptr [esp + 4] (out shared_ptr*)
    a.emit("85 c0")  # test eax, eax
    a.jcc("je", "gs_ret")
    a.emit("c7 00 00 00 00 00")  # mov dword ptr [eax], 0
    a.emit("c7 40 04 00 00 00 00")  # mov dword ptr [eax + 4], 0
    a.label("gs_ret")
    a.emit("c2 08 00")  # ret 8
    a.label("gs_normal")
    a.emit("55 8b ec 6a ff")  # push ebp; mov ebp, esp; push -1
    a.jmp(0x038080C5)

    # 11. HELPER_LOADVER5_FREAD_GUARD_VA (called from 0x037F7711)
    a.align(16)
    entries["loadver5_fread_guard"] = a.cur_va
    a.emit("83 7c 24 04 00")  # cmp dword ptr [esp + 4], 0
    a.jcc("jne", FN_FREAD)  # tail-jump to fread!
    a.inc_mem32(DATA_CNT_OOM_GUARD_HIT)
    a.emit("83 c4 18")  # add esp, 0x18 (ret_addr + 5 cdecl args)
    a.jmp(0x037F79A6)  # clean false return (xor al, al + SEH restore + ret 8)

    # 12. HELPER_SCREENEFF_GUARD_VA (jumped from 0x038490BD)
    a.align(16)
    entries["screeneff_guard"] = a.cur_va
    a.emit("83 7e 6c 00")  # cmp dword ptr [esi + 0x6c], 0
    a.jcc("je", "se_fail")
    a.emit("83 f8 04")  # cmp eax, 4
    a.jcc("ja", 0x038491C6)
    a.jmp(0x038490C6)
    a.label("se_fail")
    a.inc_mem32(DATA_CNT_OOM_GUARD_HIT)
    a.jmp(0x038491C6)

    code = a.finalize()
    assert CODE_BASE_VA + len(code) <= CODE_LIMIT_VA, f"code overflow: {len(code):#x}"
    return code, entries


def va_to_off(pe: pefile.PE, va: int) -> int:
    rva = va - IMAGE_BASE
    for s in pe.sections:
        if s.VirtualAddress <= rva < s.VirtualAddress + max(s.Misc_VirtualSize, s.SizeOfRawData):
            return s.PointerToRawData + (rva - s.VirtualAddress)
    raise ValueError(f"VA 0x{va:08x} not mapped")


def make_hook_bytes(site_va: int, orig_len: int, opcode: int, target_va: int) -> bytes:
    assert orig_len >= 5
    rel = target_va - (site_va + 5)
    return bytes([opcode]) + struct.pack("<i", rel) + b"\x90" * (orig_len - 5)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=True, help="D7774894... accepted party initialization EXE")
    ap.add_argument("--output", required=True, help="independent output EXE; source files are never overwritten")
    ap.add_argument("--receipt", required=True, help="independent build receipt JSON")
    args = ap.parse_args()

    in_path = Path(args.input)
    out_path = Path(args.output)
    rec_path = Path(args.receipt)
    if out_path.exists() or out_path.is_symlink() or rec_path.exists() or rec_path.is_symlink():
        raise FileExistsError("Output and receipt must use new paths")
    if out_path.resolve() == rec_path.resolve():
        raise ValueError("Output and receipt paths must differ")

    orig = in_path.read_bytes()
    in_sha = hashlib.sha256(orig).hexdigest().upper()
    assert in_sha == INPUT_SHA256, f"unexpected input SHA256: {in_sha} != {INPUT_SHA256}"

    pe = pefile.PE(data=orig, fast_load=True)
    assert pe.OPTIONAL_HEADER.ImageBase == IMAGE_BASE
    assert len(pe.sections) == 94

    buf = bytearray(orig)
    code, entries = build_helpers()

    # Verify target .a4rx & .a4rw slack is completely zero
    code_off = va_to_off(pe, CODE_BASE_VA)
    data_off = va_to_off(pe, DATA_VA)
    assert all(b == 0 for b in buf[code_off : code_off + len(code)]), ".a4rx cave not zero"
    assert all(b == 0 for b in buf[data_off : data_off + DATA_SIZE]), ".a4rw data block not zero"

    # Write helper code cave
    buf[code_off : code_off + len(code)] = code

    # Patch the 12 hook sites
    hook_opcodes = {
        "raw_reset_fix": 0xE9,       # jmp
        "resetdevices_exit": 0xE9,   # jmp
        "present_selfheal": 0xE8,    # call
        "createvbs_check": 0xE8,     # call
        "update_lost": 0xE9,         # jmp
        "sb_da80_guard": 0xE9,       # jmp
        "sb_df00_guard": 0xE9,       # jmp
        "sb_e190_guard": 0xE9,       # jmp
        "upscale_gettex": 0xE8,      # call
        "getsprite_guard": 0xE9,     # jmp
        "loadver5_fread_guard": 0xE8,# call
        "screeneff_guard": 0xE9,     # jmp
    }
    patched_sites = []
    for name, (va, expected) in HOOK_SITES.items():
        off = va_to_off(pe, va)
        actual = bytes(buf[off : off + len(expected)])
        assert actual == expected, f"site {name} at 0x{va:08x} mismatch: {actual.hex()} != {expected.hex()}"
        patch = make_hook_bytes(va, len(expected), hook_opcodes[name], entries[name])
        buf[off : off + len(patch)] = patch
        patched_sites.append({
            "name": name,
            "va": f"0x{va:08x}",
            "file_offset": f"0x{off:08x}",
            "size": len(expected),
            "orig_hex": expected.hex(),
            "new_hex": patch.hex(),
            "helper_va": f"0x{entries[name]:08x}",
        })

    # Update .a4rx & .a4rw Misc_VirtualSize (both remain <= SizeOfRawData = 0x1000)
    sec_updates = []
    for s in pe.sections:
        nm = s.Name.rstrip(b"\x00").decode("ascii", "ignore")
        if nm == CODE_SECTION:
            new_vsize = (CODE_BASE_VA + len(code)) - (IMAGE_BASE + s.VirtualAddress)
            assert new_vsize <= s.SizeOfRawData
            vsize_off = s.get_file_offset() + 8
            old_vsize = struct.unpack_from("<I", buf, vsize_off)[0]
            struct.pack_into("<I", buf, vsize_off, max(old_vsize, new_vsize))
            sec_updates.append({"section": nm, "vsize_file_offset": f"0x{vsize_off:08x}", "old_vsize": f"0x{old_vsize:x}", "new_vsize": f"0x{max(old_vsize, new_vsize):x}"})
        elif nm == DATA_SECTION:
            new_vsize = (DATA_VA + DATA_SIZE) - (IMAGE_BASE + s.VirtualAddress)
            assert new_vsize <= s.SizeOfRawData
            vsize_off = s.get_file_offset() + 8
            old_vsize = struct.unpack_from("<I", buf, vsize_off)[0]
            struct.pack_into("<I", buf, vsize_off, max(old_vsize, new_vsize))
            sec_updates.append({"section": nm, "vsize_file_offset": f"0x{vsize_off:08x}", "old_vsize": f"0x{old_vsize:x}", "new_vsize": f"0x{max(old_vsize, new_vsize):x}"})

    # Recalculate PE checksum
    pe_tmp = pefile.PE(data=bytes(buf), fast_load=True)
    new_csum = pe_tmp.generate_checksum()
    csum_off = pe_tmp.OPTIONAL_HEADER.get_file_offset() + 64
    old_csum = struct.unpack_from("<I", buf, csum_off)[0]
    struct.pack_into("<I", buf, csum_off, new_csum)

    # Sanity disassemble every helper cleanly
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    ordered = sorted(entries.items(), key=lambda kv: kv[1])
    for idx, (nm, va) in enumerate(ordered):
        end_va = ordered[idx + 1][1] if idx + 1 < len(ordered) else CODE_BASE_VA + len(code)
        slice_bytes = code[va - CODE_BASE_VA : end_va - CODE_BASE_VA].rstrip(b"\xcc")
        decoded = list(md.disasm(slice_bytes, va))
        total_dec = sum(i.size for i in decoded)
        assert total_dec == len(slice_bytes), f"disasm gap in {nm}: {total_dec} != {len(slice_bytes)}"

    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_bytes = bytes(buf)
    with out_path.open("xb") as stream:
        stream.write(out_bytes)
    out_sha = hashlib.sha256(out_bytes).hexdigest().upper()

    receipt = {
        "task_id": "fiendwar-rosen-blackscreen-20261126",
        "revision": "v1",
        "input_exe": str(in_path),
        "input_sha256": in_sha,
        "output_exe": str(out_path),
        "output_sha256": out_sha,
        "size_bytes": len(out_bytes),
        "sections": len(pe.sections),
        "old_checksum": f"0x{old_csum:08x}",
        "new_checksum": f"0x{new_csum:08x}",
        "checksum_file_offset": f"0x{csum_off:08x}",
        "code_cave": {
            "section": CODE_SECTION,
            "start_va": f"0x{CODE_BASE_VA:08x}",
            "end_va": f"0x{CODE_BASE_VA + len(code):08x}",
            "file_offset": f"0x{code_off:08x}",
            "size": len(code),
        },
        "data_block": {
            "section": DATA_SECTION,
            "start_va": f"0x{DATA_VA:08x}",
            "end_va": f"0x{DATA_VA + DATA_SIZE:08x}",
            "size": DATA_SIZE,
            "fields": {
                "last_recover_tick": f"0x{DATA_LAST_RECOVER_TICK:08x}",
                "recover_busy": f"0x{DATA_RECOVER_BUSY:08x}",
                "cnt_raw_reset_fixed": f"0x{DATA_CNT_RAW_RESET_FIXED:08x}",
                "cnt_vb_alloc_fail": f"0x{DATA_CNT_VB_ALLOC_FAIL:08x}",
                "cnt_self_heal_try": f"0x{DATA_CNT_SELF_HEAL_TRY:08x}",
                "cnt_self_heal_ok": f"0x{DATA_CNT_SELF_HEAL_OK:08x}",
                "cnt_vb_guard_hit": f"0x{DATA_CNT_VB_GUARD_HIT:08x}",
                "cnt_upscale_fallback": f"0x{DATA_CNT_UPSCALE_FALLBACK:08x}",
                "cnt_oom_guard_hit": f"0x{DATA_CNT_OOM_GUARD_HIT:08x}",
                "last_recover_hr": f"0x{DATA_LAST_RECOVER_HR:08x}",
                "need_resource_recovery": f"0x{DATA_NEED_RECOVERY:08x}",
                "last_raw_reset_tick": f"0x{DATA_LAST_RAW_RESET_TICK:08x}",
            },
        },
        "section_header_updates": sec_updates,
        "hook_sites": patched_sites,
    }
    rec_path.parent.mkdir(parents=True, exist_ok=True)
    with rec_path.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(f"BUILT OK: {out_path}")
    print(f"  SHA256:   {out_sha}")
    print(f"  CHECKSUM: 0x{new_csum:08x} (was 0x{old_csum:08x})")
    print(f"  CODE:     0x{CODE_BASE_VA:08x}..0x{CODE_BASE_VA+len(code):08x} ({len(code)} bytes)")


if __name__ == "__main__":
    main()
