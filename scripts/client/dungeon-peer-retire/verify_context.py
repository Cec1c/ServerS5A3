#!/usr/bin/env python3
"""Verify the exact V176 town-context retirement helper.

The input is the published V176 EXE. The candidate must be byte-for-byte the
output of repair_v176_context.build(input). The native windows execute the
actual kind3 callsite/helper, slot cleanup, local-party getter and departure
predicate. The actual 2207E90 16-control hide loop and 224C418 local-party
redraw gate also execute. Actor lookup, scene/channel getters, virtual hide
targets, layout rendering endpoints, peer manager reset and GPU drawing remain
fixtures; this is not a UI or gameplay acceptance run.
"""

import argparse
import hashlib
import json
from pathlib import Path

from verify_candidate import *
import helpers
import repair_v176_context


INPUT_SHA256 = "2a6cc14f3353e3d9d142b8580e432fe76ff3c1bbdc5be6818092347f9003bc74"
INPUT_SIZE = 111919104
HELPER_BASE = 0x6EC5000
TOWN = 0x3000A000
TOWN_PEER = 0x3000B000
UI = 0x35000000
CONTROLS = 0x35003000
CONTROL_VTABLE = 0x35006000
VIRTUAL_HIDE = 0x35008000


class FullClear(Harness):
    """Harness for one contextual op9 kind3 retirement."""

    def __init__(self, artifact, owner, *, patched, scene=3, channel=11, record_channel=11, route=1):
        self.probing = False
        self.edge = None
        self.run_end = None
        self.scene = scene
        self.channel = channel
        super().__init__(artifact)
        for address in (0x2526B50, 0x2964680):
            self.put(address, b"\xcc")
        self.namespace_owner = channel
        self.put(0x296E590, artifact.original.read(0x296E590, 0x42))
        self.put(0x201782A, artifact.original.read(0x201782A, 0x49))
        for edge in (0x2017874, 0x2017F95, 0x2018B7B, 0x2017D7C):
            self.put(edge, b"\xcc")
        self.w(0x56A3758, 0x30009000)
        self.w(ACTOR + 0x4C8, owner)

        for record, key in ((TOWN, 1104), (TOWN_PEER, 1020)):
            self.put(record, bytes(0x1000))
            self.apply_parser_fields(record, route=route, owner=record_channel)
            self.w(record + 4, key)
        for record in (SELF, PEER, TOWN, TOWN_PEER):
            for slot in range(8):
                self.w(record + 0x2C + slot * 4, 0xFFFF)

        if patched:
            fixed, _ = helpers.emit(HELPER_BASE, preserve_local=True, preserve_dungeon_town=True)
            self.put(HELPER_BASE, fixed + bytes(0x200 - len(fixed)))
        else:
            original, _ = helpers.emit(HELPER_BASE, preserve_local=True)
            self.put(HELPER_BASE, original + bytes(0x200 - len(original)))

        self.u.hook_add(UC_HOOK_CODE, self.probe_hook)

    def hook(self, uc, address, size, data):
        if address == 0x2526B50:
            self.ret(self.scene)
            return
        if address == 0x2964680:
            self.ret(self.channel)
            return
        if address == 0x296DD80:
            self.ret(ACTOR)
            return
        super().hook(uc, address, size, data)

    def probe_hook(self, uc, address, size, data):
        if address == self.run_end:
            uc.emu_stop()
            return
        if self.probing and address in (0x2017874, 0x2017F95, 0x2018B7B, 0x2017D7C):
            self.edge = address
            uc.emu_stop()

    def local(self):
        self.w(STACK, STOP)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.emu_start(0x296E590, STOP, count=1000)
        require(self.u.reg_read(UC_X86_REG_EIP) == STOP, "local-party getter did not return")
        return self.u.reg_read(UC_X86_REG_EAX)

    def clear(self, record):
        before = self.local()
        for slot in range(8):
            self.put(REMOVE_WINDOW, self.artifact.original.read(REMOVE_WINDOW, REMOVE_END - REMOVE_WINDOW))
            for reg, value in (
                (UC_X86_REG_ESP, STACK),
                (UC_X86_REG_EBP, STACK + 0x200),
                (UC_X86_REG_EBX, record),
                (UC_X86_REG_ESI, slot),
            ):
                self.u.reg_write(reg, value)
            self.run_end = REMOVE_END
            self.u.emu_start(REMOVE_WINDOW, STOP, count=30000)
            self.run_end = None
            require(self.u.reg_read(UC_X86_REG_EIP) == REMOVE_END, "kind3 window did not return")
            require(self.u.reg_read(UC_X86_REG_ESP) == STACK, "kind3 window stack differs")

        after = self.local()
        frame = STACK + 0x4000
        self.put(frame - 0x356, bytes([before == record]))
        self.put(frame - 0x351, b"\x03")
        self.u.reg_write(UC_X86_REG_EBP, frame)
        self.u.reg_write(UC_X86_REG_EBX, record)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.probing = True
        self.edge = None
        self.u.emu_start(0x201782A, STOP, count=1000)
        self.probing = False
        require(self.edge is not None, "departure predicate did not reach a known edge")
        return {
            "record": h32(record),
            "before": h32(before),
            "after": h32(after),
            "edge": h32(self.edge),
            "networkResetPath": self.edge == 0x2017874,
        }


class HudClear(FullClear):
    """Run the actual V176 hide loop and local-party redraw gate."""

    def __init__(self, artifact, owner, *, patched, scene=3, channel=11, record_channel=11, route=1):
        self.hidden = []
        self.layouts = []
        self.tick = False
        super().__init__(
            artifact,
            owner,
            patched=patched,
            scene=scene,
            channel=channel,
            record_channel=record_channel,
            route=route,
        )
        for start, size in (
            (0x2207E90, 0xCA),
            (0x224C418, 0x53),
            (0x224CA70, 0x20),
            (0x296E5E0, 0x50),
        ):
            self.put(start, artifact.original.read(start, size))
        self.put(UI, bytes(0x2000))
        self.w(CONTROL_VTABLE + 8, VIRTUAL_HIDE)
        for address in (
            VIRTUAL_HIDE,
            0x2525AA0,
            0x25253D0,
            0x1578370,
            0x39ABA70,
            0x224AEA0,
            0x224A260,
            0x224C46B,
        ):
            self.put(address, b"\xcc")
        for index in range(8):
            for base, offset in ((CONTROLS, 0x3E0), (CONTROLS + 0x1000, 0x10E4)):
                control = base + index * 0x40
                self.w(control, CONTROL_VTABLE)
                self.w(UI + offset + index * 8, control)

    def hook(self, uc, address, size, data):
        if address == VIRTUAL_HIDE:
            stack = uc.reg_read(UC_X86_REG_ESP)
            require(self.r(stack + 4) == 0, "hide leaf received a non-zero argument")
            self.hidden.append(uc.reg_read(UC_X86_REG_ECX))
            self.ret(pop=4)
            return
        if address == 0x2525AA0:
            self.ret(0)
            return
        if address == 0x25253D0:
            self.ret(1)
            return
        if address == 0x1578370:
            self.ret(UI)
            return
        if address == 0x39ABA70:
            self.ret(7)
            return
        if address in (0x224AEA0, 0x224A260):
            self.layouts.append(address)
            self.ret()
            return
        if self.tick and address == 0x224C46B:
            uc.emu_stop()
            return
        super().hook(uc, address, size, data)

    def hide(self):
        self.hidden = []
        self.w(STACK, STOP)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.reg_write(UC_X86_REG_ECX, UI)
        self.u.emu_start(0x2207E90, STOP, count=10000)
        require(self.u.reg_read(UC_X86_REG_EIP) == STOP, "HUD hide loop did not return")
        require(len(self.hidden) == 16 and len(set(self.hidden)) == 16, "HUD hide loop did not hide 16 distinct controls")

    def redraw(self):
        self.layouts = []
        self.tick = True
        self.w(STACK, STOP)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.reg_write(UC_X86_REG_ESI, UI)
        self.u.emu_start(0x224C418, STOP, count=10000)
        self.tick = False
        require(self.u.reg_read(UC_X86_REG_EIP) == 0x224C46B, "HUD redraw gate did not reach its endpoint")
        return bool(self.layouts)


def verify_case(artifact, *, owner, slot, patched, scene=3, channel=11, record_channel=11, route=1):
    harness = FullClear(
        artifact,
        owner,
        patched=patched,
        scene=scene,
        channel=channel,
        record_channel=record_channel,
        route=route,
    )
    for record in (SELF, PEER, TOWN, TOWN_PEER):
        harness.w(record + 0x2C + slot * 4, 1104)
        harness.w(record + 0x2C + (1 - slot) * 4, 1020)

    # Town records precede dungeon records; self is last within each context.
    steps = [harness.clear(record) for record in (TOWN_PEER, TOWN, PEER, SELF)]
    require(harness.local() == 0, "local-party association was not cleared")
    require(harness.r(OTHER + 0x4F8) == 0, "departing peer model remains")
    require(
        all(harness.r(record + 0x2C + i * 4) == 0xFFFF for record in (SELF, PEER, TOWN, TOWN_PEER) for i in range(8)),
        "one or more old record slots remain",
    )
    require(harness.r(ACTOR + 0x4F8) == (MODEL if patched else 0), "local model result differs")
    if patched and owner:
        require(any(step["networkResetPath"] for step in steps), "bound record missed network reset")
    return {
        "initial": h32(owner),
        "slot": slot,
        "patched": patched,
        "scene": scene,
        "channel": channel,
        "recordChannel": record_channel,
        "route": route,
        "steps": steps,
        "allOldSlotsCleared": True,
        "localModelPreserved": harness.r(ACTOR + 0x4F8) == MODEL,
        "peerModelRemoved": True,
    }


def verify_hud_case(artifact, *, owner, slot, patched):
    harness = HudClear(artifact, owner, patched=patched)
    for record in (SELF, PEER, TOWN, TOWN_PEER):
        harness.w(record + 0x2C + slot * 4, 1104)
        harness.w(record + 0x2C + (1 - slot) * 4, 1020)
    records = (TOWN_PEER, TOWN, PEER, SELF) if patched else (PEER, SELF)
    for record in records:
        harness.hide()
        harness.clear(record)
    redraw = harness.redraw()
    expected = not patched and owner in (TOWN, TOWN_PEER)
    require(redraw == expected, "HUD redraw differs from the retirement sequence")
    require(harness.r(ACTOR + 0x4F8) == MODEL, "HUD case lost the local model")
    return {
        "initial": h32(owner),
        "slot": slot,
        "patched": patched,
        "hiddenControls": 16,
        "redrawDispatched": redraw,
        "localModelPreserved": True,
    }


def verify(input_path: Path, candidate_path: Path) -> dict:
    data = input_path.read_bytes()
    require(len(data) == INPUT_SIZE, "exact published V176 size required")
    require(hashlib.sha256(data).hexdigest() == INPUT_SHA256, "exact published V176 SHA256 required")

    candidate_data = candidate_path.read_bytes()
    expected, build_receipt = repair_v176_context.build(data)
    require(candidate_data == expected, "candidate is not the exact combined V176 context helper")

    artifact = Artifact(
        data,
        candidate_data,
        {"base": hex(HELPER_BASE), "helpers": [{"size": 0x200}, {"size": 0x100}]},
    )
    image = artifact.original
    old, kind0 = helpers.emit(HELPER_BASE, preserve_local=True)
    fixed, unchanged = helpers.emit(HELPER_BASE, preserve_local=True, preserve_dungeon_town=True)
    require(unchanged == kind0, "kind0 helper changed")
    require(image.read(HELPER_BASE, 0x200) == old + bytes(0x200 - len(old)), "V176 kind3 helper differs")
    require(image.read(HELPER_BASE + 0x200, len(kind0)) == kind0, "V176 kind0 helper differs")
    require(len(fixed) <= 0x200, "context helper exceeds its exact slot")
    require(
        artifact.candidate.read(REMOVE_WINDOW, REMOVE_END - REMOVE_WINDOW)
        == image.read(REMOVE_WINDOW, REMOVE_END - REMOVE_WINDOW),
        "candidate changed the retirement callsite",
    )

    cases = []
    for owner in (SELF, PEER, TOWN, TOWN_PEER, 0):
        for slot in (0, 1):
            for patched in (False, True):
                cases.append(verify_case(artifact, owner=owner, slot=slot, patched=patched))

    hud_cases = []
    for owner in (SELF, PEER, TOWN, TOWN_PEER, 0):
        for slot in (0, 1):
            for patched in (False, True):
                hud_cases.append(verify_hud_case(artifact, owner=owner, slot=slot, patched=patched))
    old_town_redraws = sum(
        case["redrawDispatched"] for case in hud_cases if not case["patched"] and case["initial"] in (h32(TOWN), h32(TOWN_PEER))
    )
    new_redraws = sum(case["redrawDispatched"] for case in hud_cases if case["patched"])
    require(old_town_redraws == 4, "dungeon-only cleanup did not reproduce the four town redraws")
    require(new_redraws == 0, "town+dungeon cleanup still dispatched a local-party redraw")

    guards = []
    guard_inputs = (
        (1, 11, 11, 1),  # town scene
        (8, 11, 11, 1),  # other scene
        (3, 11, 12, 1),  # foreign channel
        (3, 11, 11, 2),  # foreign route
        (3, 0, 11, 1),   # channel not representable as an op9 owner byte
    )
    for scene, channel, record_channel, route in guard_inputs:
        states = []
        for patched in (False, True):
            harness = FullClear(
                artifact,
                TOWN,
                patched=patched,
                scene=scene,
                channel=channel,
                record_channel=record_channel,
                route=route,
            )
            harness.w(TOWN + 0x2C, 1104)
            harness.w(TOWN + 0x30, 1020)
            step = harness.clear(TOWN)
            states.append(
                {
                    "local": harness.local(),
                    "model": harness.r(ACTOR + 0x4F8),
                    "peerModel": harness.r(OTHER + 0x4F8),
                    "step": step,
                }
            )
        require(states[0] == states[1], "candidate changed a negative-context result")
        guards.append(
            {
                "scene": scene,
                "channel": channel,
                "recordChannel": record_channel,
                "route": route,
                "nativeBehaviorPreserved": True,
            }
        )

    # The unchanged V176 kind0 wrapper must not replace a non-null town binding.
    for slot, key in ((0, 1104), (1, 1020)):
        kind0_case = FullClear(artifact, TOWN, patched=False)
        kind0_case.w(SELF + 0x2C, 1104)
        kind0_case.w(SELF + 0x30, 1020)
        kind0_case.run_kind0_callsite(slot=slot, key=key, patched=True)
        require(kind0_case.local() == TOWN, "kind0 replaced a non-null town association")

    network_reset_observed = any(
        step["networkResetPath"] for case in cases if case["patched"] for step in case["steps"]
    )
    require(network_reset_observed, "context cases never reached the network-reset branch")

    return {
        "passed": True,
        "sourceSha256": INPUT_SHA256,
        "candidateSha256": hashlib.sha256(candidate_data).hexdigest(),
        "candidateSize": len(candidate_data),
        "combinationReceipt": {
            "inputSha256": build_receipt["inputSha256"],
            "outputSha256": build_receipt["outputSha256"],
            "helperVA": build_receipt["helperVA"],
            "helperSize": build_receipt["helperSize"],
        },
        "cases": cases,
        "hudCases": hud_cases,
        "guards": guards,
        "hudRetirement": {
            "hiddenControls": 16,
            "oldTownCasesRedraw": old_town_redraws,
            "newCasesRedraw": new_redraws,
        },
        "repeatedKind0RetainsTownAssociation": True,
        "networkResetEdgeObserved": network_reset_observed,
        "networkResetBodyExecuted": False,
        "networkDispatcherExecuted": False,
        "hudRenderingExecuted": False,
        "gpuRenderingExecuted": False,
        "fullUiAcceptance": False,
        "gameAcceptance": False,
        "published": False,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="exact published V176 EXE")
    parser.add_argument("--candidate", type=Path, required=True, help="exact combined context-helper candidate")
    parser.add_argument("--receipt", type=Path, required=True, help="new JSON receipt path")
    args = parser.parse_args()
    require(not args.receipt.exists(), "new receipt path required")
    result = verify(args.input, args.candidate)
    with args.receipt.open("x", encoding="utf8") as stream:
        json.dump(result, stream, indent=2)
        stream.write("\n")
    print(json.dumps({
        "passed": True,
        "cases": len(result["cases"]),
        "hudCases": len(result["hudCases"]),
        "guards": len(result["guards"]),
        "candidateSha256": result["candidateSha256"],
    }))


if __name__ == "__main__":
    main()
