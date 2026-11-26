"""Offline PE + strict byte-delta + Unicorn x86 emulation verification for v4 candidate DNF.exe."""

from pathlib import Path
import argparse
import hashlib
import json
import struct
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (
    UC_X86_REG_EAX,
    UC_X86_REG_EBX,
    UC_X86_REG_ECX,
    UC_X86_REG_EDX,
    UC_X86_REG_ESI,
    UC_X86_REG_EDI,
    UC_X86_REG_EBP,
    UC_X86_REG_ESP,
    UC_X86_REG_EIP,
)

CANDIDATE: Path
BASELINE: Path
RECEIPT: Path

IMAGE_BASE = 0x00400000
SCENE_DUNGEON = 2  # CDungeonScene
SCENE_TOWN = 3     # CTownScene

NOTI120_SITE_VA = 0x01FF2F34
NOTI120_ORIG_VA = 0x06F88600
MODE1_SITE_VA = 0x022C407A
MODE1_RET_VA = 0x022C407F
ONENTER_SITE_VA = 0x0370DB65
ONENTER_RET_VA = 0x0370DB6A
RETRY_SITE_VA = 0x03703663
RETRY_RET_VA = 0x03703668
TICK_SITE_VA = 0x03739CF2
TICK_RET_VA = 0x03739CF7

CODE_VA = 0x06EAF500
DATA_VA = 0x06EB0160
DATA_SIZE = 0x68

G_LOCAL_ACTOR_VA = 0x057B6FB0
G_PVP_MGR_VA = 0x057B9080
G_PARTY_MGR_VA = 0x056A3768

STUBS = {
    0x072E35D0: ("origMode1Tail", 0),       # cdecl (caller pops 4)
    0x01C5D530: ("origOnEnter", 0),         # cdecl
    0x0296E5E0: ("origRetry", 0),           # cdecl
    0x014DE7B0: ("origTick", 0),            # cdecl
    0x01FA2790: ("getSceneMgr", 0),
    0x02526B50: ("getSceneType", 0),
    0x02524FB0: ("getDungeonStage", 0),
    0x02974890: ("getPartyUserBySlot", 4),  # thiscall ret 4
    0x02974460: ("findPartySlot", 4),       # thiscall ret 4
    0x0296A410: ("getSubActors", 0),
    0x014570F0: ("prepSubActors", 0),
    0x0296A700: ("onInitDungeon", 12),      # thiscall ret 12
    0x0296A8B0: ("setIsInDungeon", 4),      # thiscall ret 4
    0x0296A7E0: ("setPartySlot", 4),        # thiscall ret 4
    0x0296A640: ("syncDungeonMeta", 0),
}


def check_pe_and_delta():
    base_bytes = BASELINE.read_bytes()
    cand_bytes = CANDIDATE.read_bytes()
    receipt = json.loads(RECEIPT.read_text(encoding="utf-8"))
    expected_input = "D798B52293F8994926B2574DB3DEE075F07136A2C5096636D42F2844F8B82F1B"
    if hashlib.sha256(base_bytes).hexdigest().upper() != expected_input:
        raise ValueError("baseline SHA-256 must match the accepted D798 image")
    if receipt.get("inputSHA256", "").upper() != expected_input:
        raise ValueError("receipt input SHA-256 must match the accepted D798 image")
    assert hashlib.sha256(cand_bytes).hexdigest().upper() == receipt["outputSHA256"]
    assert len(cand_bytes) == len(base_bytes)

    pe = pefile.PE(data=cand_bytes, fast_load=True)
    assert pe.verify_checksum(), "PE checksum verification failed!"
    assert len(pe.sections) == 94

    restored = bytearray(cand_bytes)
    for lo, hi in receipt["allowedFileRanges"]:
        restored[lo:hi] = base_bytes[lo:hi]
    assert bytes(restored) == base_bytes, "Unexpected byte diffs outside allowed ranges!"
    return pe, cand_bytes


class EmulatorHarness:
    def __init__(self, pe: pefile.PE, cand_bytes: bytes):
        self.mu = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pe = pe
        self.cand_bytes = cand_bytes

        def map_page(va: int, size: int = 0x2000):
            aligned = va & ~0xFFF
            end = (va + size + 0xFFF) & ~0xFFF
            try:
                self.mu.mem_map(aligned, end - aligned)
            except Exception:
                pass

        for va in [
            NOTI120_SITE_VA,
            NOTI120_ORIG_VA,
            MODE1_SITE_VA,
            ONENTER_SITE_VA,
            RETRY_SITE_VA,
            TICK_SITE_VA,
            CODE_VA,
            DATA_VA,
            G_LOCAL_ACTOR_VA,
            G_PVP_MGR_VA,
            G_PARTY_MGR_VA,
        ]:
            map_page(va, 0x2000)
        map_page(0x00190000, 0x10000)  # stack
        map_page(0x20000000, 0x30000)  # heap mocks
        for stub_va in STUBS:
            map_page(stub_va, 0x1000)

        for va, sz in [
            (NOTI120_SITE_VA, 0x10),
            (MODE1_SITE_VA, 0x10),
            (ONENTER_SITE_VA, 0x10),
            (RETRY_SITE_VA, 0x10),
            (TICK_SITE_VA, 0x10),
            (CODE_VA, 0x500),
        ]:
            off = pe.get_offset_from_rva(va - IMAGE_BASE)
            self.mu.mem_write(va, cand_bytes[off : off + sz])

        self.mu.mem_write(DATA_VA, bytes(DATA_SIZE))
        self.mu.mem_write(NOTI120_ORIG_VA, b"\x90")

        for stub_va, (name, pop_bytes) in STUBS.items():
            if pop_bytes:
                self.mu.mem_write(stub_va, b"\xc2" + struct.pack("<H", pop_bytes))
            else:
                self.mu.mem_write(stub_va, b"\xc3")

        self.state = {
            "scene_mgr": 0x20020000,
            "scene_type": SCENE_DUNGEON,
            "stage": 0x20021000,
            "slots": {},
            "sub_actors": {},
            "trace": [],
            "stop_va": 0,
        }

        def hook_code(uc, address, size, user_data):
            if address in STUBS:
                name, pop_bytes = STUBS[address]
                esp = uc.reg_read(UC_X86_REG_ESP)
                ecx = uc.reg_read(UC_X86_REG_ECX)
                argc = pop_bytes // 4 if pop_bytes else (1 if name == "origMode1Tail" else 0)
                args = [struct.unpack("<I", uc.mem_read(esp + 4 + i * 4, 4))[0] for i in range(argc)]
                self.state["trace"].append((name, ecx, args))
                if name == "getSceneMgr":
                    uc.reg_write(UC_X86_REG_EAX, self.state["scene_mgr"])
                elif name == "getSceneType":
                    uc.reg_write(UC_X86_REG_EAX, self.state["scene_type"])
                elif name == "getDungeonStage":
                    uc.reg_write(UC_X86_REG_EAX, self.state["stage"])
                elif name == "getPartyUserBySlot":
                    uc.reg_write(UC_X86_REG_EAX, self.state["slots"].get(args[0], 0))
                elif name == "findPartySlot":
                    found = 0xFFFFFFFF
                    for s, p in self.state["slots"].items():
                        if p == args[0]:
                            found = s
                            break
                    uc.reg_write(UC_X86_REG_EAX, found)
                elif name == "getSubActors":
                    uc.reg_write(UC_X86_REG_EAX, self.state["sub_actors"].get(ecx, 0))
                elif name == "origOnEnter":
                    uc.reg_write(UC_X86_REG_EAX, 0xCAFE0001)
                elif name == "origRetry":
                    uc.reg_write(UC_X86_REG_EAX, 1)
                elif name == "origTick":
                    uc.reg_write(UC_X86_REG_EAX, 0xBEEF0001)
                else:
                    uc.reg_write(UC_X86_REG_EAX, 0xDEAD0001)
                if name not in ("origMode1Tail", "origTick"):
                    uc.reg_write(UC_X86_REG_ECX, 0xCCCC1111)
                    uc.reg_write(UC_X86_REG_EDX, 0xDDDD2222)
            elif address == self.state["stop_va"]:
                uc.emu_stop()

        self.mu.hook_add(UC_HOOK_CODE, hook_code)

    def set_globals(self, *, local_actor: int, pvp_mgr: int, party_mgr: int):
        self.mu.mem_write(G_LOCAL_ACTOR_VA, struct.pack("<I", local_actor))
        self.mu.mem_write(G_PVP_MGR_VA, struct.pack("<I", pvp_mgr))
        self.mu.mem_write(G_PARTY_MGR_VA, struct.pack("<I", party_mgr))

    def bind_pu_actor(self, pu: int, actor: int, vtable: int = 0x04B6CF04, skills: dict[int, int] | None = None):
        if pu:
            self.mu.mem_write(pu + 0x4F8, struct.pack("<I", actor))
        if actor:
            self.mu.mem_write(actor, struct.pack("<I", vtable))
            self.mu.mem_write(actor + 0x4574, bytes(0x800))
            if skills:
                for sk_id, sk_ptr in skills.items():
                    self.mu.mem_write(actor + 0x4574 + sk_id * 4, struct.pack("<I", sk_ptr))

    def run_noti120(self, *, scene_mgr: int, scene_type: int, stage: int, slots: dict[int, int], sub_actors: dict[int, int]):
        self.state.update(
            scene_mgr=scene_mgr, scene_type=scene_type, stage=stage,
            slots=dict(slots), sub_actors=dict(sub_actors), trace=[], stop_va=NOTI120_ORIG_VA,
        )
        esp0 = 0x00197F00
        self.mu.reg_write(UC_X86_REG_EAX, 0x11111111)
        self.mu.reg_write(UC_X86_REG_EBX, 0x22222222)
        self.mu.reg_write(UC_X86_REG_ECX, 0x33333333)
        self.mu.reg_write(UC_X86_REG_EDX, 0x44444444)
        self.mu.reg_write(UC_X86_REG_ESI, 0x55555555)
        self.mu.reg_write(UC_X86_REG_EDI, 0x66666666)
        self.mu.reg_write(UC_X86_REG_EBP, 0x00198000)
        self.mu.reg_write(UC_X86_REG_ESP, esp0)

        self.mu.emu_start(NOTI120_SITE_VA, NOTI120_ORIG_VA + 1, count=15000)

        assert self.mu.reg_read(UC_X86_REG_EIP) == NOTI120_ORIG_VA
        assert self.mu.reg_read(UC_X86_REG_ESP) == esp0
        assert self.mu.reg_read(UC_X86_REG_EAX) == 0x11111111
        assert self.mu.reg_read(UC_X86_REG_EBX) == 0x22222222
        assert self.mu.reg_read(UC_X86_REG_ECX) == 0x33333333
        assert self.mu.reg_read(UC_X86_REG_EDX) == 0x44444444
        assert self.mu.reg_read(UC_X86_REG_ESI) == 0x55555555
        assert self.mu.reg_read(UC_X86_REG_EDI) == 0x66666666
        assert self.mu.reg_read(UC_X86_REG_EBP) == 0x00198000
        return list(self.state["trace"]), self.read_diag()

    def run_mode1(self, *, actor: int, pu: int, scene_mgr: int, scene_type: int,
                  stage: int, slots: dict[int, int], sub_actors: dict[int, int]):
        self.state.update(
            scene_mgr=scene_mgr, scene_type=scene_type, stage=stage,
            slots=dict(slots), sub_actors=dict(sub_actors), trace=[], stop_va=MODE1_RET_VA,
        )
        esp0 = 0x00197F00
        self.mu.mem_write(esp0, struct.pack("<I", actor))
        self.mu.reg_write(UC_X86_REG_EAX, 0x11111111)
        self.mu.reg_write(UC_X86_REG_EBX, 0x22222222)
        self.mu.reg_write(UC_X86_REG_ECX, 0x33333333)
        self.mu.reg_write(UC_X86_REG_EDX, 0x44444444)
        self.mu.reg_write(UC_X86_REG_ESI, actor)
        self.mu.reg_write(UC_X86_REG_EDI, pu)
        self.mu.reg_write(UC_X86_REG_EBP, 0x00198000)
        self.mu.reg_write(UC_X86_REG_ESP, esp0)

        self.mu.emu_start(MODE1_SITE_VA, MODE1_RET_VA + 1, count=10000)

        assert self.mu.reg_read(UC_X86_REG_EIP) == MODE1_RET_VA
        assert self.mu.reg_read(UC_X86_REG_ESP) == esp0
        assert self.mu.reg_read(UC_X86_REG_ESI) == actor
        assert self.mu.reg_read(UC_X86_REG_EDI) == pu
        assert self.mu.reg_read(UC_X86_REG_EBP) == 0x00198000
        assert self.mu.reg_read(UC_X86_REG_EBX) == 0x22222222
        return list(self.state["trace"]), self.read_diag()

    def run_tick(self, *, scene_mgr: int, scene_type: int, stage: int, slots: dict[int, int], sub_actors: dict[int, int]):
        self.state.update(
            scene_mgr=scene_mgr, scene_type=scene_type, stage=stage,
            slots=dict(slots), sub_actors=dict(sub_actors), trace=[], stop_va=TICK_RET_VA,
        )
        esp0 = 0x00197F00
        self.mu.reg_write(UC_X86_REG_EBX, 0x22222222)
        self.mu.reg_write(UC_X86_REG_ESI, 0x55555555)
        self.mu.reg_write(UC_X86_REG_EDI, 0x66666666)
        self.mu.reg_write(UC_X86_REG_EBP, 0x00198000)
        self.mu.reg_write(UC_X86_REG_ESP, esp0)

        self.mu.emu_start(TICK_SITE_VA, TICK_RET_VA + 1, count=15000)

        assert self.mu.reg_read(UC_X86_REG_EIP) == TICK_RET_VA
        assert self.mu.reg_read(UC_X86_REG_ESP) == esp0
        assert self.mu.reg_read(UC_X86_REG_EAX) == 0xBEEF0001
        assert self.mu.reg_read(UC_X86_REG_EBX) == 0x22222222
        assert self.mu.reg_read(UC_X86_REG_ESI) == 0x55555555
        assert self.mu.reg_read(UC_X86_REG_EDI) == 0x66666666
        assert self.mu.reg_read(UC_X86_REG_EBP) == 0x00198000
        return list(self.state["trace"]), self.read_diag()

    def run_reset(self, site_va: int, ret_va: int):
        self.state.update(trace=[], stop_va=ret_va)
        esp0 = 0x00197F00
        self.mu.reg_write(UC_X86_REG_ECX, 0x88888888)
        self.mu.reg_write(UC_X86_REG_EBX, 0x55555555)
        self.mu.reg_write(UC_X86_REG_ESI, 0x66666666)
        self.mu.reg_write(UC_X86_REG_EDI, 0x77777777)
        self.mu.reg_write(UC_X86_REG_EBP, 0x00198000)
        self.mu.reg_write(UC_X86_REG_ESP, esp0)
        self.mu.emu_start(site_va, ret_va + 1, count=500)
        assert self.mu.reg_read(UC_X86_REG_EIP) == ret_va
        assert self.mu.reg_read(UC_X86_REG_ESP) == esp0
        assert self.mu.reg_read(UC_X86_REG_EBX) == 0x55555555
        assert self.mu.reg_read(UC_X86_REG_ESI) == 0x66666666
        assert self.mu.reg_read(UC_X86_REG_EDI) == 0x77777777
        assert self.mu.reg_read(UC_X86_REG_EBP) == 0x00198000
        return list(self.state["trace"]), self.mu.reg_read(UC_X86_REG_EAX), self.read_diag()

    def read_diag(self):
        vals = struct.unpack("<26I", self.mu.mem_read(DATA_VA, DATA_SIZE))
        return {
            "hook_calls": vals[0],
            "initialized": vals[1],
            "skip_non_remote": vals[2],
            "skip_not_dungeon": vals[3],
            "skip_dedup": vals[4],
            "last_actor": vals[5],
            "last_pu": vals[6],
            "last_scene_type": vals[7],
            "dungeon_epoch": vals[8],
            "last_vtable": vals[9],
            "last_stage": vals[10],
            "reentry_guard": vals[11],
            "inited_actors": list(vals[12:20]),
            "noti120_calls": vals[20],
            "mode1_calls": vals[21],
            "skip_no_skills": vals[22],
            "pending_sync": vals[23],
            "tick_sweeps": vals[24],
        }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--candidate", required=True, type=Path)
    parser.add_argument("--baseline", required=True, type=Path)
    parser.add_argument("--build-receipt", required=True, type=Path)
    args = parser.parse_args()
    global CANDIDATE, BASELINE, RECEIPT
    CANDIDATE = args.candidate
    BASELINE = args.baseline
    RECEIPT = args.build_receipt
    pe, cand_bytes = check_pe_and_delta()
    print("[PASS] PE checksum, section count (94), and strict byte-delta checks passed.")

    MGR = 0x20001000
    PU_LOC = 0x20002000
    PU_REM = 0x20003000
    ACT_LOC = 0x20004000
    ACT_REM = 0x2000A000
    SUB_REM = 0x20015000
    SCENE = 0x20020000
    STAGE1 = 0x20021000
    STAGE2 = 0x20022000

    h = EmulatorHarness(pe, cand_bytes)
    h.set_globals(local_actor=ACT_LOC, pvp_mgr=0, party_mgr=MGR)
    h.bind_pu_actor(PU_LOC, ACT_LOC, 0x04B6CF04, skills={76: 0x20025000})
    h.bind_pu_actor(PU_REM, ACT_REM, 0x04B6CF04, skills={})
    slots = {0: PU_REM, 1: PU_LOC}

    # Case 1: Town mode1 (sceneType == 3, CTownScene) -> skipped cleanly, sets pending_sync=1
    trace, d = h.run_mode1(actor=ACT_REM, pu=PU_REM, scene_mgr=SCENE, scene_type=SCENE_TOWN, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert d["initialized"] == 0 and d["skip_not_dungeon"] == 1 and d["last_scene_type"] == SCENE_TOWN and d["pending_sync"] == 1
    print("[PASS] Case 1 (Town mode1, sceneType=3): safely skipped, pending_sync=1.")

    # Case 2: CDungeonScene::onEnter runs -> epoch=1, dedup cleared, pending_sync=1
    trace, ret_eax, d = h.run_reset(ONENTER_SITE_VA, ONENTER_RET_VA)
    assert trace == [("origOnEnter", 0x88888888, [])] and ret_eax == 0xCAFE0001 and d["dungeon_epoch"] == 1 and d["pending_sync"] == 1
    print("[PASS] Case 2 (CDungeonScene::onEnter reset): epoch=1, pending_sync=1.")

    # Case 3: REAL 101 SCENARIO! op37 mode1 arrives in Dungeon (sceneType=2) with skill 76 -> initializes IMMEDIATELY!
    h.bind_pu_actor(PU_REM, ACT_REM, 0x04B6CF04, skills={76: 0x20026000})
    trace, d = h.run_mode1(actor=ACT_REM, pu=PU_REM, scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    inits = [t for t in trace if t[0] in ("getSubActors", "prepSubActors", "onInitDungeon", "setIsInDungeon", "findPartySlot", "setPartySlot", "syncDungeonMeta")]
    assert inits == [
        ("getSubActors", PU_REM, []),
        ("prepSubActors", SUB_REM, []),
        ("onInitDungeon", PU_REM, [2, 0, 1]),
        ("setIsInDungeon", PU_REM, [1]),
        ("findPartySlot", MGR, [PU_REM]),
        ("setPartySlot", PU_REM, [0]),
        ("syncDungeonMeta", PU_REM, []),
    ], f"Case 3 init trace: {inits}"
    assert d["initialized"] == 1 and d["inited_actors"][0] == ACT_REM and d["reentry_guard"] == 0 and d["last_scene_type"] == SCENE_DUNGEON
    print("[PASS] Case 3 (Dungeon mode1, sceneType=2 & skill 76): full native onInitDungeon ran immediately!")

    # Case 4: NOTI120 arrives after mode1 -> dedup skipped (no HP/MP reset!)
    trace, d = h.run_noti120(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert not any(t[0] == "onInitDungeon" for t in trace)
    assert d["initialized"] == 1 and d["skip_dedup"] == 1 and d["noti120_calls"] == 1
    print("[PASS] Case 4 (NOTI120 after mode1): dedup skipped, HP/MP untouched.")

    # Case 5: First dungeon tick sees all peers already initialized -> clears pending_sync=0
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert not any(t[0] == "onInitDungeon" for t in trace)
    assert d["tick_sweeps"] == 1 and d["pending_sync"] == 0
    print("[PASS] Case 5 (first dungeon tick): verified all peers initialized, cleared pending_sync=0.")

    # Case 6: Second dungeon tick -> ZERO overhead fast-path (only origTick, tick_sweeps unchanged!)
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert trace == [("origTick", 0xCCCCCCCC & 0xFFFFFFFF, [])] or [t[0] for t in trace] == ["origTick"]
    assert d["tick_sweeps"] == 1 and d["pending_sync"] == 0
    print("[PASS] Case 6 (steady-state dungeon tick): zero-overhead fast path (1 branch, 0 extra calls).")

    # Case 7: Order-independent fallback! RetryDungeon + mode1 arrives while still in Town (sceneType=3),
    # then NO NOTI120 arrives -> first CDungeonScene::update tick (sceneType=2) initializes immediately!
    trace, ret_eax, d = h.run_reset(RETRY_SITE_VA, RETRY_RET_VA)
    assert d["dungeon_epoch"] == 2 and d["pending_sync"] == 1
    trace, d = h.run_mode1(actor=ACT_REM, pu=PU_REM, scene_mgr=SCENE, scene_type=SCENE_TOWN, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert d["initialized"] == 1 and d["pending_sync"] == 1
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={PU_REM: SUB_REM})
    assert any(t == ("onInitDungeon", PU_REM, [2, 0, 1]) for t in trace)
    assert d["initialized"] == 2 and d["pending_sync"] == 0 and d["tick_sweeps"] == 2
    print("[PASS] Case 7 (order-independent fallback via dungeon tick): initialized on frame #0 & cleared pending_sync=0!")

    # Case 8: Dungeon tick when peer still has 0 skills -> keeps pending_sync=1 without poisoning dedup,
    # then next tick after skills arrive initializes it and clears pending_sync=0!
    h.run_reset(ONENTER_SITE_VA, ONENTER_RET_VA)
    h.bind_pu_actor(PU_REM, ACT_REM, 0x04B6CF04, skills={})
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={})
    assert not any(t[0] == "onInitDungeon" for t in trace)
    assert d["skip_no_skills"] == 1 and d["pending_sync"] == 1 and all(x == 0 for x in d["inited_actors"])
    h.bind_pu_actor(PU_REM, ACT_REM, 0x04B6CF04, skills={76: 0x20026000})
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots=slots, sub_actors={})
    assert any(t == ("onInitDungeon", PU_REM, [2, 0, 1]) for t in trace)
    assert d["initialized"] == 3 and d["pending_sync"] == 0
    print("[PASS] Case 8 (tick waits for late skills without poisoning dedup): initialized once skills appeared!")

    # Case 9: Solo dungeon (0 remote peers) -> first tick clears pending_sync=0 immediately
    h.run_reset(ONENTER_SITE_VA, ONENTER_RET_VA)
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE1, slots={0: PU_LOC}, sub_actors={})
    assert not any(t[0] == "onInitDungeon" for t in trace)
    assert d["pending_sync"] == 0
    print("[PASS] Case 9 (solo dungeon): first tick clears pending_sync=0 immediately.")

    # Case 10: Stage pointer change auto-resets dedup even without onEnter/Retry
    trace, d = h.run_noti120(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert any(t == ("onInitDungeon", PU_REM, [2, 0, 1]) for t in trace)
    assert d["initialized"] == 4 and d["last_stage"] == STAGE2
    print("[PASS] Case 10 (stage pointer change): auto-reset dedup and re-initialized.")

    # Case 11: Local player mode1 -> skipped as non-remote
    trace, d = h.run_mode1(actor=ACT_LOC, pu=PU_LOC, scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert trace == [("origMode1Tail", 0x33333333, [ACT_LOC])] and d["skip_non_remote"] == 1
    print("[PASS] Case 11 (local player mode1): safely skipped.")

    # Case 12: PvP active -> NOTI120, mode1, and tick all safely skipped
    h.set_globals(local_actor=ACT_LOC, pvp_mgr=0x20029000, party_mgr=MGR)
    h.run_reset(ONENTER_SITE_VA, ONENTER_RET_VA)
    trace, d = h.run_noti120(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert trace == []
    trace, d = h.run_mode1(actor=ACT_REM, pu=PU_REM, scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert trace == [("origMode1Tail", 0x33333333, [ACT_REM])]
    trace, d = h.run_tick(scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert [t[0] for t in trace] == ["origTick"] and d["pending_sync"] == 0
    print("[PASS] Case 12 (PvP mode): NOTI120, mode1, and tick all safely skipped.")

    # Case 13: Actor/PU mismatch -> safely skipped
    h.set_globals(local_actor=ACT_LOC, pvp_mgr=0, party_mgr=MGR)
    h.bind_pu_actor(PU_REM, ACT_REM + 0x100, 0x04B6CF04, skills={76: 0x20026000})
    trace, d = h.run_mode1(actor=ACT_REM, pu=PU_REM, scene_mgr=SCENE, scene_type=SCENE_DUNGEON, stage=STAGE2, slots=slots, sub_actors={})
    assert trace == [("origMode1Tail", 0x33333333, [ACT_REM])] and d["skip_non_remote"] == 2
    print("[PASS] Case 13 (actor/PU mismatch): safely skipped.")

    print("ALL OFFLINE VERIFICATION CHECKS PASSED (13/13 emulation cases + PE checksum + strict byte-delta).")


if __name__ == "__main__":
    main()
