"""Candidate v4 builder for remote party peer dungeon initialization (all classes).

Root cause (established from matching client memory and disassembly):
  1. In town (sceneType == 3, CTownScene), DNFBRIDGE_PEER_SKILL_PAGE_TOWN is off so remote peers
     have 0 learned skills in [actor + 0x4574 .. +0x4d73].
  2. On dungeon entry, CDungeonScene (sceneType == 2, sm+0xb8, vt=0x04d22fe4) runs
     CDungeonScene::onEnter (0x03737310 -> 0x0370db10), which calls CPartyUser::onInitDungeon(2, 0, 1)
     (0x0296a700) BEFORE peer_skill_page (USERINFO mode1) populates remote peers' skills.
     CSwordman::onInitDungeon (0x03059e20) sees skill[76] == NULL and leaves [remoteBerserker + 0x8068]
     == NULL; all other 27 CUserCharac subclasses' +0x664 onInitDungeon / CSkill::onInitDungeon /
     Squirrel onInitDungeon hooks similarly run on empty skills (all 17 unique +0x664 overrides have
     ZERO isLocalPlayer gate).
  3. During dungeon loading (sceneType == 2), go-server sends USERINFO mode1 (peer_skill_page)
     populating all remote skills (e.g. Berserker skill 76 Lv20/22) and NOTI120, but the native
     client never re-runs CPartyUser::onInitDungeon after skills arrive.
  4. (Why v2/v3 did not trigger): .rprior checks sceneType == 3 because 3 is CTownScene (Town);
     CDungeonScene (Dungeon) is sceneType == 2! Both v2 and v3 rejected all dungeon calls as
     skip_not_dungeon with last_scene_type == 2.

Fix (v4):
  Shared core helper CORE_INIT_VA (0x06eaf500, ESI=actor, EDI=CPartyUser*, preserves all regs/flags):
    - Validates remote peer (esi != 0, edi != 0, esi != [0x057b6fb0], [edi+0x4f8] == esi)
    - Validates non-PvP dungeon ([0x057b9080] == 0, sceneMgr->getSceneType() == 2, stage != 0)
    - Auto-resets 8-slot inited_actors[] if dungeon stage pointer changed
    - Skips if actor already in inited_actors[] (safe against repeated NOTI120 & mid-combat mode1)
    - Verifies at least one learned skill exists in [esi+0x4574..+0x4d73] (skips WITHOUT recording
      in dedup table if skills not loaded yet)
    - Runs exact native CDungeonScene::onEnter per-slot initializer sequence on edi:
        0x0296a410 -> 0x014570f0, 0x0296a700(2,0,1), 0x0296a8b0(1),
        optional 0x02974460(edi) -> 0x0296a7e0(slot < 8), 0x0296a640()
    - Records actor in inited_actors[0..7]
  Five clean 5-byte hooks in .tp3 (.rprior and multi270 untouched):
    1. 0x01ff2f34 (NOTI120 entry: jmp 0x06f88600 -> jmp 0x06eaf700): sweeps party slots 0..7 in
       [0x056a3768] calling CORE_INIT_VA on remote peers, then tail-jumps to original 0x06f88600.
    2. 0x022c407a (USERINFO mode1 end: call 0x072e35d0 -> call 0x06eaf780): runs original 0x072e35d0,
       marks pending_sync=1 for remote peers, then calls CORE_INIT_VA immediately.
    3. 0x0370db65 (CDungeonScene::onEnter: call 0x01c5d530 -> call 0x06eaf7e0): epoch++, clear dedup,
       set pending_sync=1.
    4. 0x03703663 (RetryDungeon: call 0x0296e5e0 -> call 0x06eaf830): epoch++, clear dedup,
       set pending_sync=1.
    5. 0x03739cf2 (CDungeonScene::update: call 0x014de7b0 -> call 0x06eaf880): order-independent
       safety net. Fast-path is 1 branch (cmp [pending_sync], 0; je orig). When pending_sync != 0,
       sweeps slots 0..7 calling CORE_INIT_VA and clears pending_sync=0 once all present remote
       peers are recorded in inited_actors[0..7].
"""

from pathlib import Path
import argparse
import hashlib
import json
import struct
import pefile
import capstone


INPUT_SHA256 = "D798B52293F8994926B2574DB3DEE075F07136A2C5096636D42F2844F8B82F1B"
IMAGE_BASE = 0x00400000
DUNGEON_SCENE_TYPE = 2  # CDungeonScene (sm+0xb8, vt=0x04d22fe4); CTownScene is 3

# 5 clean 5-byte hook sites in .tp3 (verified against baseline D798B522...)
NOTI120_SITE_VA = 0x01FF2F34
NOTI120_EXPECTED = bytes.fromhex("e9c756f904")  # jmp 0x06f88600
FN_NOTI120_ORIG = 0x06F88600

MODE1_SITE_VA = 0x022C407A
MODE1_EXPECTED = bytes.fromhex("e851f50105")  # call 0x072e35d0
FN_MODE1_ORIG = 0x072E35D0

ONENTER_SITE_VA = 0x0370DB65
ONENTER_EXPECTED = bytes.fromhex("e8c6f954fe")  # call 0x01c5d530
FN_ONENTER_ORIG = 0x01C5D530

RETRY_SITE_VA = 0x03703663
RETRY_EXPECTED = bytes.fromhex("e878af26ff")  # call 0x0296e5e0
FN_RETRY_ORIG = 0x0296E5E0

TICK_SITE_VA = 0x03739CF2
TICK_EXPECTED = bytes.fromhex("e8b94adafd")  # call 0x014de7b0
FN_TICK_ORIG = 0x014DE7B0

CODE_SECTION = ".a4rx"
CORE_INIT_VA = 0x06EAF500
NOTI120_WRAPPER_VA = 0x06EAF700
MODE1_WRAPPER_VA = 0x06EAF780
ONENTER_RESET_VA = 0x06EAF7E0
RETRY_RESET_VA = 0x06EAF830
TICK_WRAPPER_VA = 0x06EAF880

DATA_SECTION = ".a4rw"
DATA_VA = 0x06EB0160
# Layout in .a4rw (26 dwords = 0x68 bytes):
#   +0x00 hook_calls
#   +0x04 initialized
#   +0x08 skipped_non_remote
#   +0x0c skipped_not_dungeon
#   +0x10 skipped_dedup
#   +0x14 last_actor
#   +0x18 last_pu
#   +0x1c last_scene_type
#   +0x20 dungeon_epoch
#   +0x24 last_vtable
#   +0x28 last_stage
#   +0x2c reentry_guard
#   +0x30..+0x4c inited_actors[8]
#   +0x50 noti120_calls
#   +0x54 mode1_calls
#   +0x58 skipped_no_skills
#   +0x5c pending_sync
#   +0x60 tick_sweeps
#   +0x64 reserved
DATA_SIZE = 0x68

OFF_HOOK_CALLS = 0x00
OFF_INITIALIZED = 0x04
OFF_SKIP_NON_REMOTE = 0x08
OFF_SKIP_NOT_DUNGEON = 0x0C
OFF_SKIP_DEDUP = 0x10
OFF_LAST_ACTOR = 0x14
OFF_LAST_PU = 0x18
OFF_LAST_SCENE_TYPE = 0x1C
OFF_DUNGEON_EPOCH = 0x20
OFF_LAST_VTABLE = 0x24
OFF_LAST_STAGE = 0x28
OFF_REENTRY_GUARD = 0x2C
OFF_INITED_ACTORS = 0x30
OFF_NOTI120_CALLS = 0x50
OFF_MODE1_CALLS = 0x54
OFF_SKIP_NO_SKILLS = 0x58
OFF_PENDING_SYNC = 0x5C
OFF_TICK_SWEEPS = 0x60

# Native addresses verified in baseline DNF.exe (D798B522...)
G_LOCAL_ACTOR_VA = 0x057B6FB0
G_PVP_MGR_VA = 0x057B9080
G_PARTY_MGR_VA = 0x056A3768
FN_GET_SCENE_MGR = 0x01FA2790
FN_GET_SCENE_TYPE = 0x02526B50
FN_GET_DUNGEON_STAGE = 0x02524FB0
FN_GET_PARTY_USER_BY_SLOT = 0x02974890
FN_FIND_PARTY_SLOT = 0x02974460
FN_GET_SUB_ACTORS = 0x0296A410
FN_PREP_SUB_ACTORS = 0x014570F0
FN_ON_INIT_DUNGEON = 0x0296A700
FN_SET_IN_DUNGEON = 0x0296A8B0
FN_SET_PARTY_SLOT = 0x0296A7E0
FN_SYNC_DUNGEON_META = 0x0296A640


class AsmBuilder:
    def __init__(self, base_va: int):
        self.base_va = base_va
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[int, str, int]] = []

    @property
    def cur_va(self) -> int:
        return self.base_va + len(self.buf)

    def label(self, name: str) -> None:
        self.labels[name] = len(self.buf)

    def raw(self, hex_str: str) -> None:
        self.buf.extend(bytes.fromhex(hex_str))

    def call_abs(self, target_va: int) -> None:
        rel = target_va - (self.cur_va + 5)
        self.buf.extend(b"\xe8" + struct.pack("<i", rel))

    def jmp_abs(self, target_va: int) -> None:
        rel = target_va - (self.cur_va + 5)
        self.buf.extend(b"\xe9" + struct.pack("<i", rel))

    def jmp_label(self, name: str) -> None:
        self.buf.append(0xE9)
        pos = len(self.buf)
        self.buf.extend(b"\x00\x00\x00\x00")
        self.fixups.append((pos, name, len(self.buf)))

    def jcc_label(self, opcode_hex: str, name: str) -> None:
        self.raw(opcode_hex)
        pos = len(self.buf)
        self.buf.extend(b"\x00\x00\x00\x00")
        self.fixups.append((pos, name, len(self.buf)))

    def finish(self) -> bytes:
        for pos, name, end_off in self.fixups:
            rel = self.labels[name] - end_off
            struct.pack_into("<i", self.buf, pos, rel)
        return bytes(self.buf)


def u32(val: int) -> str:
    return struct.pack("<I", val).hex()


def build_core_init() -> bytes:
    """Shared helper: ESI=actor, EDI=CPartyUser*. Preserves ALL flags and GPRs."""
    c = AsmBuilder(CORE_INIT_VA)

    def inc_mem(off: int) -> None:
        c.raw("ff05" + u32(DATA_VA + off))

    c.raw("9c 60 fc")  # pushfd; pushad; cld
    inc_mem(OFF_HOOK_CALLS)

    # Validate esi (actor) != 0 and edi (CPartyUser*) != 0
    c.raw("85f6")  # test esi, esi
    c.jcc_label("0f84", "skip_non_remote")
    c.raw("85ff")  # test edi, edi
    c.jcc_label("0f84", "skip_non_remote")

    # Validate remote peer only: esi != [0x057b6fb0]
    c.raw("3b35" + u32(G_LOCAL_ACTOR_VA))  # cmp esi, dword ptr [0x057b6fb0]
    c.jcc_label("0f84", "skip_non_remote")

    # Validate CPartyUser owns this actor: [edi + 0x4f8] == esi
    c.raw("39b7f8040000")  # cmp dword ptr [edi + 0x4f8], esi
    c.jcc_label("0f85", "skip_non_remote")

    # Validate not PvP: [0x057b9080] == 0
    c.raw("833d" + u32(G_PVP_MGR_VA) + "00")  # cmp dword ptr [0x057b9080], 0
    c.jcc_label("0f85", "skip_not_dungeon")

    # Validate dungeon scene: sceneMgr = 0x01fa2790(); sceneMgr->0x02526b50() == 2 (CDungeonScene)
    c.call_abs(FN_GET_SCENE_MGR)
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f84", "skip_not_dungeon")
    c.raw("8bd8")  # mov ebx, eax (sceneMgr)
    c.raw("8bcb")  # mov ecx, ebx
    c.call_abs(FN_GET_SCENE_TYPE)
    c.raw("a3" + u32(DATA_VA + OFF_LAST_SCENE_TYPE))  # mov [DATA+0x1c], eax
    c.raw(f"83f8{DUNGEON_SCENE_TYPE:02x}")  # cmp eax, 2
    c.jcc_label("0f85", "skip_not_dungeon")

    # Validate active dungeon stage: stage = sceneMgr->0x02524fb0() != NULL
    c.raw("8bcb")  # mov ecx, ebx
    c.call_abs(FN_GET_DUNGEON_STAGE)
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f84", "skip_not_dungeon")

    # If stage changed from last_stage, update last_stage and clear inited_actors[0..7]
    c.raw("3b05" + u32(DATA_VA + OFF_LAST_STAGE))  # cmp eax, [DATA+0x28]
    c.jcc_label("0f84", "check_dedup")
    c.raw("a3" + u32(DATA_VA + OFF_LAST_STAGE))  # mov [DATA+0x28], eax
    c.raw("33c0")  # xor eax, eax
    c.raw("b908000000")  # mov ecx, 8
    c.raw("ba" + u32(DATA_VA + OFF_INITED_ACTORS))  # mov edx, DATA+0x30
    c.label("zero_stage_loop")
    c.raw("8902")  # mov [edx], eax
    c.raw("83c204")  # add edx, 4
    c.raw("49")  # dec ecx
    c.jcc_label("0f85", "zero_stage_loop")

    # Dedup check: if esi is already in inited_actors[0..7], skip
    c.label("check_dedup")
    c.raw("33c9")  # xor ecx, ecx
    c.raw("ba" + u32(DATA_VA + OFF_INITED_ACTORS))  # mov edx, DATA+0x30
    c.label("dedup_loop")
    c.raw("39348a")  # cmp dword ptr [edx + ecx*4], esi
    c.jcc_label("0f84", "skip_dedup")
    c.raw("41")  # inc ecx
    c.raw("83f908")  # cmp ecx, 8
    c.jcc_label("0f82", "dedup_loop")

    # Verify peer_skill_page has populated at least one skill in [esi + 0x4574 .. +0x4d73]
    c.raw("8d8e74450000")  # lea ecx, [esi + 0x4574]
    c.raw("ba00020000")  # mov edx, 0x200
    c.label("skill_scan")
    c.raw("833900")  # cmp dword ptr [ecx], 0
    c.jcc_label("0f85", "has_skills")
    c.raw("83c104")  # add ecx, 4
    c.raw("4a")  # dec edx
    c.jcc_label("0f85", "skill_scan")
    c.jmp_label("skip_no_skills")

    c.label("has_skills")
    # Re-entrancy guard
    c.raw("b801000000")  # mov eax, 1
    c.raw("8705" + u32(DATA_VA + OFF_REENTRY_GUARD))  # xchg [DATA+0x2c], eax
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f85", "skip_dedup")

    # Record diagnostics (last_actor, last_pu, last_vtable)
    c.raw("8935" + u32(DATA_VA + OFF_LAST_ACTOR))  # mov [DATA+0x14], esi
    c.raw("893d" + u32(DATA_VA + OFF_LAST_PU))  # mov [DATA+0x18], edi
    c.raw("8b06")  # mov eax, [esi] (vtable)
    c.raw("a3" + u32(DATA_VA + OFF_LAST_VTABLE))  # mov [DATA+0x24], eax

    # Run exact native CDungeonScene::onEnter per-slot sequence on edi (CPartyUser*):
    # (1) Optional sub-actor prep: 0x0296a410 -> 0x014570f0
    c.raw("8bcf")  # mov ecx, edi
    c.call_abs(FN_GET_SUB_ACTORS)
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f84", "skip_sub")
    c.raw("8bc8")  # mov ecx, eax
    c.call_abs(FN_PREP_SUB_ACTORS)

    c.label("skip_sub")
    # (2) CPartyUser::onInitDungeon(2, 0, 1)
    c.raw("6a01 6a00 6a02 8bcf")  # push 1; push 0; push 2; mov ecx, edi
    c.call_abs(FN_ON_INIT_DUNGEON)

    # (3) CPartyUser::setIsInDungeon(1)
    c.raw("6a01 8bcf")  # push 1; mov ecx, edi
    c.call_abs(FN_SET_IN_DUNGEON)

    # (4) If edi is registered in party manager [0x056a3768] at slot 0..7, setPartySlot(slot)
    c.raw("8b0d" + u32(G_PARTY_MGR_VA))  # mov ecx, [0x056a3768]
    c.raw("85c9")  # test ecx, ecx
    c.jcc_label("0f84", "skip_slot")
    c.raw("57")  # push edi
    c.call_abs(FN_FIND_PARTY_SLOT)  # thiscall ret 4 -> eax = slot or -1
    c.raw("83f808")  # cmp eax, 8
    c.jcc_label("0f83", "skip_slot")  # jae skip_slot
    c.raw("50 8bcf")  # push eax; mov ecx, edi
    c.call_abs(FN_SET_PARTY_SLOT)

    c.label("skip_slot")
    # (5) CPartyUser::syncDungeonMeta()
    c.raw("8bcf")  # mov ecx, edi
    c.call_abs(FN_SYNC_DUNGEON_META)

    # Store esi in first free slot of inited_actors[0..7] (fallback slot 0 if full)
    c.raw("33c9")  # xor ecx, ecx
    c.raw("ba" + u32(DATA_VA + OFF_INITED_ACTORS))  # mov edx, DATA+0x30
    c.label("store_loop")
    c.raw("833c8a00")  # cmp dword ptr [edx + ecx*4], 0
    c.jcc_label("0f84", "do_store")
    c.raw("41")  # inc ecx
    c.raw("83f908")  # cmp ecx, 8
    c.jcc_label("0f82", "store_loop")
    c.raw("33c9")  # xor ecx, ecx
    c.label("do_store")
    c.raw("89348a")  # mov dword ptr [edx + ecx*4], esi

    inc_mem(OFF_INITIALIZED)
    c.raw("c705" + u32(DATA_VA + OFF_REENTRY_GUARD) + "00000000")  # mov [DATA+0x2c], 0
    c.jmp_label("done")

    c.label("skip_non_remote")
    inc_mem(OFF_SKIP_NON_REMOTE)
    c.jmp_label("done")

    c.label("skip_not_dungeon")
    inc_mem(OFF_SKIP_NOT_DUNGEON)
    c.jmp_label("done")

    c.label("skip_no_skills")
    inc_mem(OFF_SKIP_NO_SKILLS)
    c.jmp_label("done")

    c.label("skip_dedup")
    inc_mem(OFF_SKIP_DEDUP)

    c.label("done")
    c.raw("61 9d c3")  # popad; popfd; ret
    return c.finish()


def build_noti120_wrapper() -> bytes:
    """Hook at 0x01ff2f34 (jmp 0x06f88600): sweep party slots 0..7, then tail-jmp to 0x06f88600."""
    c = AsmBuilder(NOTI120_WRAPPER_VA)
    c.raw("9c 60 fc")  # pushfd; pushad; cld
    c.raw("ff05" + u32(DATA_VA + OFF_NOTI120_CALLS))  # inc [DATA+0x50]
    c.raw("833d" + u32(G_PVP_MGR_VA) + "00")  # cmp dword ptr [0x057b9080], 0
    c.jcc_label("0f85", "done")
    c.raw("33db")  # xor ebx, ebx (slot = 0)
    c.label("slot_loop")
    c.raw("8b0d" + u32(G_PARTY_MGR_VA))  # mov ecx, [0x056a3768]
    c.raw("85c9")  # test ecx, ecx
    c.jcc_label("0f84", "done")
    c.raw("53")  # push ebx
    c.call_abs(FN_GET_PARTY_USER_BY_SLOT)  # thiscall ret 4 -> eax = CPartyUser*
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f84", "next_slot")
    c.raw("8bf8")  # mov edi, eax (CPartyUser*)
    c.raw("8bb7f8040000")  # mov esi, [edi + 0x4f8] (actor)
    c.raw("85f6")  # test esi, esi
    c.jcc_label("0f84", "next_slot")
    c.raw("3b35" + u32(G_LOCAL_ACTOR_VA))  # cmp esi, [0x057b6fb0]
    c.jcc_label("0f84", "next_slot")
    c.call_abs(CORE_INIT_VA)  # preserves ALL regs/flags
    c.label("next_slot")
    c.raw("43")  # inc ebx
    c.raw("83fb08")  # cmp ebx, 8
    c.jcc_label("0f82", "slot_loop")
    c.label("done")
    c.raw("61 9d")  # popad; popfd
    c.jmp_abs(FN_NOTI120_ORIG)  # jmp 0x06f88600
    return c.finish()


def build_mode1_wrapper() -> bytes:
    """Hook at 0x022c407a (call 0x072e35d0): run original, flag pending_sync for remote, call CORE_INIT_VA."""
    c = AsmBuilder(MODE1_WRAPPER_VA)
    c.raw("ff742404")  # push dword ptr [esp + 4]
    c.call_abs(FN_MODE1_ORIG)  # call 0x072e35d0
    c.raw("83c404")  # add esp, 4
    c.raw("9c")  # pushfd
    c.raw("ff05" + u32(DATA_VA + OFF_MODE1_CALLS))  # inc [DATA+0x54]
    # If esi is a remote peer, mark pending_sync=1 so CDungeonScene::update catches pre-transition mode1 too
    c.raw("85f6")  # test esi, esi
    c.jcc_label("0f84", "call_core")
    c.raw("3b35" + u32(G_LOCAL_ACTOR_VA))  # cmp esi, [0x057b6fb0]
    c.jcc_label("0f84", "call_core")
    c.raw("c705" + u32(DATA_VA + OFF_PENDING_SYNC) + "01000000")  # mov [DATA+0x5c], 1
    c.label("call_core")
    c.raw("9d")  # popfd
    c.call_abs(CORE_INIT_VA)  # preserves ALL regs/flags
    c.raw("c3")  # ret
    return c.finish()


def build_epoch_reset_stub(stub_va: int, orig_target_va: int) -> bytes:
    """Increment dungeon_epoch, clear inited_actors[0..7], set pending_sync=1, tail-jmp original."""
    c = AsmBuilder(stub_va)
    c.raw("9c 50 51 52")  # pushfd; push eax; push ecx; push edx
    c.raw("ff05" + u32(DATA_VA + OFF_DUNGEON_EPOCH))  # inc dword ptr [DATA+0x20]
    c.raw("c705" + u32(DATA_VA + OFF_PENDING_SYNC) + "01000000")  # mov dword ptr [DATA+0x5c], 1
    c.raw("33c0")  # xor eax, eax
    c.raw("b908000000")  # mov ecx, 8
    c.raw("ba" + u32(DATA_VA + OFF_INITED_ACTORS))  # mov edx, DATA+0x30
    c.label("zloop")
    c.raw("8902")  # mov [edx], eax
    c.raw("83c204")  # add edx, 4
    c.raw("49")  # dec ecx
    c.jcc_label("0f85", "zloop")
    c.raw("5a 59 58 9d")  # pop edx; pop ecx; pop eax; popfd
    c.jmp_abs(orig_target_va)
    return c.finish()


def build_tick_wrapper() -> bytes:
    """Hook at 0x03739cf2 (CDungeonScene::update -> call 0x014de7b0).

    Fast-path when pending_sync == 0 is a single cmp + je to original 0x014de7b0.
    When pending_sync != 0, sweeps party slots 0..7 calling CORE_INIT_VA on remote peers
    and clears pending_sync = 0 once all present remote peers are recorded in inited_actors[0..7].
    """
    c = AsmBuilder(TICK_WRAPPER_VA)
    c.raw("833d" + u32(DATA_VA + OFF_PENDING_SYNC) + "00")  # cmp dword ptr [DATA+0x5c], 0
    c.jcc_label("0f84", "tail_orig")

    c.raw("9c 60 fc")  # pushfd; pushad; cld
    c.raw("ff05" + u32(DATA_VA + OFF_TICK_SWEEPS))  # inc [DATA+0x60]

    # If PvP active, clear pending_sync and bail
    c.raw("833d" + u32(G_PVP_MGR_VA) + "00")  # cmp dword ptr [0x057b9080], 0
    c.jcc_label("0f85", "clear_and_done")

    c.raw("33ed")  # xor ebp, ebp (uninitialized remote peer count)
    c.raw("33db")  # xor ebx, ebx (slot = 0)
    c.label("slot_loop")
    c.raw("8b0d" + u32(G_PARTY_MGR_VA))  # mov ecx, [0x056a3768]
    c.raw("85c9")  # test ecx, ecx
    c.jcc_label("0f84", "after_loop")
    c.raw("53")  # push ebx
    c.call_abs(FN_GET_PARTY_USER_BY_SLOT)  # thiscall ret 4 -> eax = CPartyUser*
    c.raw("85c0")  # test eax, eax
    c.jcc_label("0f84", "next_slot")
    c.raw("8bf8")  # mov edi, eax
    c.raw("8bb7f8040000")  # mov esi, [edi + 0x4f8]
    c.raw("85f6")  # test esi, esi
    c.jcc_label("0f84", "next_slot")
    c.raw("3b35" + u32(G_LOCAL_ACTOR_VA))  # cmp esi, [0x057b6fb0]
    c.jcc_label("0f84", "next_slot")

    # Attempt initialization (dedups instantly if already initialized)
    c.call_abs(CORE_INIT_VA)

    # Check whether esi is now in inited_actors[0..7]; if not, increment ebp (keep pending_sync=1)
    c.raw("33c9")  # xor ecx, ecx
    c.raw("ba" + u32(DATA_VA + OFF_INITED_ACTORS))  # mov edx, DATA+0x30
    c.label("chk_loop")
    c.raw("39348a")  # cmp [edx + ecx*4], esi
    c.jcc_label("0f84", "next_slot")
    c.raw("41")  # inc ecx
    c.raw("83f908")  # cmp ecx, 8
    c.jcc_label("0f82", "chk_loop")
    c.raw("45")  # inc ebp (still waiting for this remote peer's skills/stage)

    c.label("next_slot")
    c.raw("43")  # inc ebx
    c.raw("83fb08")  # cmp ebx, 8
    c.jcc_label("0f82", "slot_loop")

    c.label("after_loop")
    c.raw("85ed")  # test ebp, ebp
    c.jcc_label("0f85", "restore_done")

    c.label("clear_and_done")
    c.raw("c705" + u32(DATA_VA + OFF_PENDING_SYNC) + "00000000")  # mov [DATA+0x5c], 0

    c.label("restore_done")
    c.raw("61 9d")  # popad; popfd
    c.label("tail_orig")
    c.jmp_abs(FN_TICK_ORIG)  # jmp 0x014de7b0
    return c.finish()


def pe_checksum(buf: bytes, checksum_offset: int) -> int:
    total = 0
    n = len(buf)
    for i in range(0, n - (n & 1), 2):
        if i == checksum_offset or i == checksum_offset + 2:
            continue
        total += buf[i] | (buf[i + 1] << 8)
        total = (total & 0xFFFF) + (total >> 16)
    if n & 1:
        total += buf[-1]
        total = (total & 0xFFFF) + (total >> 16)
    total = (total & 0xFFFF) + (total >> 16)
    return (total + n) & 0xFFFFFFFF


def build(raw: bytes) -> tuple[bytes, dict]:
    in_sha = hashlib.sha256(raw).hexdigest().upper()
    assert in_sha == INPUT_SHA256, f"Baseline SHA256 mismatch: got {in_sha}, expected {INPUT_SHA256}"

    pe = pefile.PE(data=raw, fast_load=True)
    assert pe.OPTIONAL_HEADER.ImageBase == IMAGE_BASE, "Unexpected ImageBase"

    def find_section(name: str):
        for s in pe.sections:
            if s.Name.decode("latin1").rstrip("\x00") == name:
                return s
        raise AssertionError(f"Section {name} not found")

    code_sec = find_section(CODE_SECTION)
    data_sec = find_section(DATA_SECTION)
    assert code_sec.Characteristics == 0x60000020, f"{CODE_SECTION} is not RX"
    assert data_sec.Characteristics == 0xC0000040, f"{DATA_SECTION} is not RW"

    core_code = build_core_init()
    noti120_code = build_noti120_wrapper()
    mode1_code = build_mode1_wrapper()
    onenter_code = build_epoch_reset_stub(ONENTER_RESET_VA, FN_ONENTER_ORIG)
    retry_code = build_epoch_reset_stub(RETRY_RESET_VA, FN_RETRY_ORIG)
    tick_code = build_tick_wrapper()

    blocks = [
        (CORE_INIT_VA, core_code, "core_init"),
        (NOTI120_WRAPPER_VA, noti120_code, "noti120_wrapper"),
        (MODE1_WRAPPER_VA, mode1_code, "mode1_wrapper"),
        (ONENTER_RESET_VA, onenter_code, "onenter_reset"),
        (RETRY_RESET_VA, retry_code, "retry_reset"),
        (TICK_WRAPPER_VA, tick_code, "tick_wrapper"),
    ]
    for i in range(len(blocks) - 1):
        va, blob, name = blocks[i]
        next_va = blocks[i + 1][0]
        assert va + len(blob) <= next_va, f"{name} ({len(blob)}B) overflows into 0x{next_va:08x}"

    total_code_end_va = TICK_WRAPPER_VA + len(tick_code)
    code_delta = (CORE_INIT_VA - IMAGE_BASE) - code_sec.VirtualAddress
    code_total_len = total_code_end_va - CORE_INIT_VA
    data_delta = (DATA_VA - IMAGE_BASE) - data_sec.VirtualAddress

    assert code_sec.Misc_VirtualSize <= code_delta and code_delta + code_total_len <= code_sec.SizeOfRawData
    assert data_sec.Misc_VirtualSize <= data_delta and data_delta + DATA_SIZE <= data_sec.SizeOfRawData

    for va, blob, name in blocks:
        off = pe.get_offset_from_rva(va - IMAGE_BASE)
        assert raw[off : off + len(blob)] == bytes(len(blob)), f"{name} padding occupied at 0x{va:08x}"

    data_off = pe.get_offset_from_rva(DATA_VA - IMAGE_BASE)
    assert raw[data_off : data_off + DATA_SIZE] == bytes(DATA_SIZE), "Data padding occupied"

    out = bytearray(raw)
    allowed_ranges: list[tuple[int, int]] = []

    for va, blob, _ in blocks:
        off = pe.get_offset_from_rva(va - IMAGE_BASE)
        out[off : off + len(blob)] = blob
        allowed_ranges.append((off, off + len(blob)))

    code_vsize_off = code_sec.get_file_offset() + 8
    struct.pack_into("<I", out, code_vsize_off, code_delta + code_total_len)
    allowed_ranges.append((code_vsize_off, code_vsize_off + 4))

    data_vsize_off = data_sec.get_file_offset() + 8
    struct.pack_into("<I", out, data_vsize_off, data_delta + DATA_SIZE)
    allowed_ranges.append((data_vsize_off, data_vsize_off + 4))

    # Patch the 5 clean 5-byte sites in .tp3
    for site_va, expected, opcode, target_va, label in [
        (NOTI120_SITE_VA, NOTI120_EXPECTED, b"\xe9", NOTI120_WRAPPER_VA, "noti120"),
        (MODE1_SITE_VA, MODE1_EXPECTED, b"\xe8", MODE1_WRAPPER_VA, "mode1"),
        (ONENTER_SITE_VA, ONENTER_EXPECTED, b"\xe8", ONENTER_RESET_VA, "onEnter"),
        (RETRY_SITE_VA, RETRY_EXPECTED, b"\xe8", RETRY_RESET_VA, "retry"),
        (TICK_SITE_VA, TICK_EXPECTED, b"\xe8", TICK_WRAPPER_VA, "dungeonTick"),
    ]:
        site_off = pe.get_offset_from_rva(site_va - IMAGE_BASE)
        assert raw[site_off : site_off + 5] == expected, (
            f"Hook site {label} mismatch at 0x{site_va:08x}: got {raw[site_off:site_off+5].hex()}"
        )
        patch = opcode + struct.pack("<i", target_va - (site_va + 5))
        out[site_off : site_off + 5] = patch
        allowed_ranges.append((site_off, site_off + 5))

    checksum_off = pe.OPTIONAL_HEADER.get_file_offset() + 0x40
    new_cs = pe_checksum(out, checksum_off)
    struct.pack_into("<I", out, checksum_off, new_cs)
    allowed_ranges.append((checksum_off, checksum_off + 4))

    out_bytes = bytes(out)
    out_sha = hashlib.sha256(out_bytes).hexdigest().upper()

    md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_32)
    disasm = {
        name: [f"0x{i.address:08x}: {i.mnemonic} {i.op_str}" for i in md.disasm(blob, va)]
        for va, blob, name in blocks
    }

    receipt = {
        "version": "v4-scene2-dungeon-init-with-tick",
        "dungeonSceneType": DUNGEON_SCENE_TYPE,
        "inputSHA256": in_sha,
        "outputSHA256": out_sha,
        "sizeBytes": len(out_bytes),
        "peChecksum": f"0x{new_cs:08x}",
        "sectionCount": len(pe.sections),
        "hookSites": {
            "noti120": {"va": f"0x{NOTI120_SITE_VA:08x}", "origTarget": f"0x{FN_NOTI120_ORIG:08x}", "newTarget": f"0x{NOTI120_WRAPPER_VA:08x}"},
            "mode1": {"va": f"0x{MODE1_SITE_VA:08x}", "origTarget": f"0x{FN_MODE1_ORIG:08x}", "newTarget": f"0x{MODE1_WRAPPER_VA:08x}"},
            "onEnter": {"va": f"0x{ONENTER_SITE_VA:08x}", "origTarget": f"0x{FN_ONENTER_ORIG:08x}", "newTarget": f"0x{ONENTER_RESET_VA:08x}"},
            "retry": {"va": f"0x{RETRY_SITE_VA:08x}", "origTarget": f"0x{FN_RETRY_ORIG:08x}", "newTarget": f"0x{RETRY_RESET_VA:08x}"},
            "dungeonTick": {"va": f"0x{TICK_SITE_VA:08x}", "origTarget": f"0x{FN_TICK_ORIG:08x}", "newTarget": f"0x{TICK_WRAPPER_VA:08x}"},
        },
        "helperCodeSection": CODE_SECTION,
        "helperCodeVA": f"0x{CORE_INIT_VA:08x}",
        "helperCodeSizes": {name: len(blob) for _, blob, name in blocks},
        "diagDataSection": DATA_SECTION,
        "diagDataVA": f"0x{DATA_VA:08x}",
        "diagDataSize": DATA_SIZE,
        "allowedFileRanges": allowed_ranges,
        "disassembly": disasm,
    }
    return out_bytes, receipt


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--input",
        required=True,
        help="D798B522... accepted party initialization baseline",
    )
    parser.add_argument(
        "--output",
        required=True,
        help="independent output EXE; source files are never overwritten",
    )
    parser.add_argument(
        "--receipt",
        required=True,
        help="independent build receipt JSON",
    )
    args = parser.parse_args()

    out_path, receipt_path = Path(args.output), Path(args.receipt)
    if out_path.exists() or out_path.is_symlink() or receipt_path.exists() or receipt_path.is_symlink():
        raise FileExistsError("Output and receipt must use new paths")
    if out_path.resolve() == receipt_path.resolve():
        raise ValueError("Output and receipt paths must differ")

    raw = Path(args.input).read_bytes()
    out_bytes, receipt = build(raw)

    out_path.parent.mkdir(parents=True, exist_ok=True)
    receipt_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("xb") as stream:
        stream.write(out_bytes)
    with receipt_path.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2, ensure_ascii=False)

    print(f"Built v4 candidate DNF.exe -> {out_path}")
    print(f"  inputSHA256 : {receipt['inputSHA256']}")
    print(f"  outputSHA256: {receipt['outputSHA256']}")
    print(f"  peChecksum  : {receipt['peChecksum']}")
    print(f"  codeSizes   : {receipt['helperCodeSizes']}")


if __name__ == "__main__":
    main()
