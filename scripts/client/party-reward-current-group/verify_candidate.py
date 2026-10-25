#!/usr/bin/env python3
"""Bounded native checks for the isolated v169 route2 P33 candidate."""
from __future__ import annotations

import hashlib
import json
import sys
import argparse
from pathlib import Path

sys.dont_write_bytecode = True
if not __debug__:
    raise RuntimeError("assertions required")

try:
    import unicorn
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
    from unicorn.x86_const import *
except ImportError as exc:
    raise RuntimeError("unicorn 2.1.4 is required") from exc

ROOT = Path(__file__).resolve().parent

PRED, HELPER = 0x06EC2000, 0x06EC2180
DRAW, INPUT_GATE, PASS = 0x06EC2300, 0x06EC2340, 0x06EC2380
ORIG_DRAW, ORIG_INPUT, ORIG_PASS = 0x03829190, 0x03826930, 0x03829F50
PARENT_GET, ARENA, RAID_GET = 0x01F1AAE0, 0x018C2800, 0x01A553E0
PARTY_CONTAINS, PARTY_OWNER, PARTY_ROUTE = 0x02973110, 0x02964680, 0x02964560

ROW, ROOTOBJ, SCENE, SELF, PARTY, RAID, VEC, STACK, STOP = (
    0x10000000, 0x10010000, 0x10020000, 0x10030000,
    0x10040000, 0x10050000, 0x10060000, 0x20000000, 0x30000000,
)
MANAGER = 0x10070000
SCENE_GLOBAL, MANAGER_GLOBAL = 0x056A3758, 0x056A3778
NONVOL = {UC_X86_REG_EBX: 0x11110001, UC_X86_REG_ESI: 0x22220002,
          UC_X86_REG_EDI: 0x33330003, UC_X86_REG_EBP: STACK + 0x800}


def require(value, message):
    if not value:
        raise ValueError(message)


def u32(data, at):
    return int.from_bytes(data[at:at+4], "little")


class PE:
    def __init__(self, data):
        require(data[:2] == b"MZ", "missing MZ")
        self.data = data
        self.nt = u32(data, 0x3C)
        require(data[self.nt:self.nt+4] == b"PE\0\0", "missing PE")
        self.count = int.from_bytes(data[self.nt+6:self.nt+8], "little")
        self.opt = self.nt + 24
        self.base = u32(data, self.opt + 28)
        opt_size = int.from_bytes(data[self.nt+20:self.nt+22], "little")
        table = self.opt + opt_size
        self.sections = []
        for i in range(self.count):
            at = table + i * 40
            virt, rva, raw_size, raw = (int.from_bytes(data[at+8+j:at+12+j], "little") for j in (0,4,8,12))
            self.sections.append((data[at:at+8].rstrip(b"\0").decode(), rva, raw_size, raw, virt))

    def offset(self, va, size=1):
        rva = va - self.base
        hits = [(raw + rva - srva) for _, srva, raw_size, raw, _ in self.sections
                if srva <= rva and rva + size <= srva + raw_size]
        require(len(hits) == 1, f"VA 0x{va:x} mapping count {len(hits)}")
        return hits[0]

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at+size]


class Machine:
    def __init__(self, pe, receipt, *, route=2, owner=20, self_key=19,
                 owner_group=1, self_group=1, owner_present=True,
                 self_actor_present=True, self_record_present=True,
                 raid_present=True, manager_present=True, party_present=True,
                 party_owner_member=True, party_self_member=True,
                 category=0, party_owner=1, party_route=1,
                 malformed=False):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        self.trace = []
        self.receipt = receipt
        self.party_owner_member = party_owner_member
        self.party_self_member = party_self_member
        self.party_owner = party_owner
        self.party_route = party_route
        for page in (ROW, ROOTOBJ, SCENE, SELF, PARTY, RAID, VEC, MANAGER,
                     0x056A3000, 0x01F1A000, 0x018C2000,
                     0x01A55000, 0x02973000, 0x02964000, 0x03829000,
                     0x03826000, 0x03829000):
            self.put(page, bytes(4096))
        self.put(STACK - 0x2000, bytes(0x4000))
        self.put(PRED, checked_code(pe, receipt, "predicate", PRED, "0x06ec2000"))
        self.put(HELPER, checked_code(pe, receipt, "groupHelper", HELPER, "0x06ec2180"))
        for name, va in (("draw", DRAW), ("input", INPUT_GATE), ("pass", PASS)):
            self.put(va, checked_code(pe, receipt["wrappers"], name, va, hex(va)))
        for stub in (PARENT_GET, ARENA, RAID_GET, PARTY_CONTAINS, PARTY_OWNER,
                     PARTY_ROUTE, ORIG_DRAW, ORIG_INPUT, ORIG_PASS):
            self.put(stub, b"\xc3")
        self.put(STOP, b"\xcc")
        self.w(SCENE_GLOBAL, SCENE if self_actor_present else 0)
        self.w(MANAGER_GLOBAL, MANAGER if manager_present else 0)
        self.w(SCENE + 0x70, SELF)
        self.w(SELF + 0x20, self_key)
        self.w(SELF + 0x4C8, PARTY if party_present else 0)
        self.w(PARTY + 0x70, party_route)
        self.w(PARTY + 0x74, party_owner)
        self.set_party_membership(party_owner_member, party_self_member, owner, self_key)
        self.w(ROW + 0x3E4, owner)
        self.w(ROOTOBJ + 0x3BC, category)
        self.w(RAID + 0x24, route)
        records = []
        if self_record_present:
            records.append((self_key, self_group))
        if owner_present and owner != self_key:
            records.append((owner, owner_group))
        self.set_roster(records, malformed=malformed)
        self.raid_present = raid_present
        self.u.hook_add(UC_HOOK_CODE, self.hook)

    def set_party_membership(self, owner_member, self_member, owner, self_key):
        for i in range(8):
            self.w(PARTY + 0x2C + 4*i, 0xFFFFFFFF)
        slot = 0
        if owner_member:
            self.w(PARTY + 0x2C + 4*slot, owner)
            slot += 1
        if self_member and (not owner_member or owner != self_key):
            self.w(PARTY + 0x2C + 4*slot, self_key)

    def put(self, address, data):
        for page in range(address & ~4095, (address + len(data) + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.u.mem_map(page, 4096)
                self.pages.add(page)
        self.u.mem_write(address, data)

    def w(self, address, value):
        self.put(address, value.to_bytes(4, "little"))

    def set_roster(self, records, malformed=False):
        self.put(VEC, bytes(20 * 0x64))
        if malformed:
            begin, end = VEC + 0x100, VEC
        else:
            begin, end = VEC, VEC + len(records) * 0x64
        self.w(RAID + 0xA4, begin)
        self.w(RAID + 0xA8, end)
        for i, (key, group) in enumerate(records):
            self.w(VEC + i*0x64, key)
            self.put(VEC + i*0x64 + 0x2D, bytes([group]))

    def r(self, address):
        return int.from_bytes(self.u.mem_read(address, 4), "little")

    def ret(self, value, pop=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        target = self.r(sp)
        self.u.reg_write(UC_X86_REG_EAX, value & 0xFFFFFFFF)
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        self.u.reg_write(UC_X86_REG_EIP, target)

    def hook(self, _u, address, _size, _data):
        this = self.u.reg_read(UC_X86_REG_ECX)
        sp = self.u.reg_read(UC_X86_REG_ESP)
        if address == PARENT_GET:
            out = self.r(sp + 4)
            self.w(out, ROOTOBJ)
            self.trace.append("parent")
            self.ret(ROOTOBJ, 4)
        elif address == ARENA:
            self.trace.append("arena")
            self.ret(0)
        elif address == RAID_GET:
            self.trace.append("raid_get")
            self.ret(RAID if self.raid_present else 0)
        elif address == PARTY_CONTAINS:
            arg = self.r(sp + 4)
            self.trace.append(("party_contains", arg))
            found = -1
            for i in range(8):
                if self.r(PARTY + 0x2C + 4*i) == arg:
                    found = i
                    break
            self.ret(found, 4)
        elif address == PARTY_OWNER:
            self.trace.append("party_owner")
            self.ret(self.party_owner)
        elif address == PARTY_ROUTE:
            self.trace.append("party_route")
            self.ret(self.party_route)
        elif address in (ORIG_DRAW, ORIG_INPUT, ORIG_PASS):
            self.trace.append(("original", hex(address)))
            value = 1 if address != ORIG_INPUT else 0
            self.ret(value)
        else:
            return

    def call(self, entry, helper_args=None, wrapper=False):
        self.trace.clear()
        before = [(p, bytes(self.u.mem_read(p, 4096))) for p in (ROW, ROOTOBJ, SCENE, SELF, PARTY, RAID, VEC)]
        for reg, value in NONVOL.items():
            self.u.reg_write(reg, value)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.w(STACK, STOP)
        args = 0
        if helper_args is not None:
            owner, self_key = helper_args
            self.w(STACK + 4, owner)
            self.w(STACK + 8, self_key)
            args = 2
        self.u.reg_write(UC_X86_REG_ECX, ROW)
        try:
            self.u.emu_start(entry, STOP, count=20000)
        except Exception as exc:
            raise RuntimeError(
                f"emulation {exc}; eip=0x{self.u.reg_read(UC_X86_REG_EIP):x} "
                f"esp=0x{self.u.reg_read(UC_X86_REG_ESP):x} "
                f"ebp=0x{self.u.reg_read(UC_X86_REG_EBP):x} trace={self.trace}") from exc
        require(self.u.reg_read(UC_X86_REG_EIP) == STOP, "did not reach stop")
        require(self.u.reg_read(UC_X86_REG_ESP) == STACK + 4, "stack imbalance")
        for reg, value in NONVOL.items():
            require(self.u.reg_read(reg) == value, f"register 0x{reg:x} changed")
        for page, old in before:
            require(bytes(self.u.mem_read(page, 4096)) == old, f"fixture mutated at 0x{page:x}")
        return self.u.reg_read(UC_X86_REG_EAX), list(self.trace)


def checked_code(pe, receipt, key, va, expected_va):
    entry = receipt[key]
    require(entry["va"] == expected_va, f"{key} receipt VA changed")
    raw = pe.read(va, entry["size"])
    require(raw.hex() == entry["bytes"], f"{key} candidate bytes differ from receipt")
    require(hashlib.sha256(raw).hexdigest() == entry["sha256"], f"{key} candidate hash differs from receipt")
    return raw


def run_suite(build_dir):
    build_dir = build_dir.resolve()
    candidate_path = build_dir / "DNF-v169-p33-route2-group.exe"
    receipt_path = build_dir / "build-receipt.json"
    require(unicorn.__version__ == "2.1.4", "unicorn version changed")
    data = candidate_path.read_bytes()
    receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    require(hashlib.sha256(data).hexdigest() == receipt["candidate"]["sha256"], "candidate hash mismatch")
    pe = PE(data)
    require(pe.read(PRED, 1) == b"\x55", "predicate missing")
    require(pe.read(HELPER, 1) == b"\x55", "helper missing")
    require(pe.read(0x0491A074, 4) == PASS.to_bytes(4, "little"), "pass vtable redirect changed")
    require(pe.read(0x0491A078, 4) == DRAW.to_bytes(4, "little"), "draw vtable redirect changed")
    require(pe.read(0x0491A0C4, 4) == INPUT_GATE.to_bytes(4, "little"), "input vtable redirect changed")
    checked_code(pe, receipt, "predicate", PRED, "0x06ec2000")
    checked_code(pe, receipt, "groupHelper", HELPER, "0x06ec2180")
    for name, va in (("draw", DRAW), ("input", INPUT_GATE), ("pass", PASS)):
        checked_code(pe, receipt["wrappers"], name, va, hex(va))
    cases = []

    def helper_case(name, expected, **kw):
        m = Machine(pe, receipt, **kw)
        got, trace = m.call(HELPER, helper_args=(kw.get("owner",20), kw.get("self_key",19)))
        require(got == expected, f"{name}: helper {got} != {expected}; trace={trace}")
        cases.append({"name": name, "kind": "helper", "result": got, "expected": expected, "trace": trace})

    def predicate_case(name, expected, **kw):
        m = Machine(pe, receipt, **kw)
        got, trace = m.call(PRED)
        require(got == expected, f"{name}: predicate {got} != {expected}; trace={trace}")
        cases.append({"name": name, "kind": "predicate", "result": got, "expected": expected, "trace": trace})

    helper_case("route2_group_equal", 1, route=2)
    helper_case("route2_group_different", 0, route=2, owner_group=2)
    helper_case("route2_owner_missing", 0, route=2, owner_present=False)
    helper_case("route2_self_record_missing", 0, route=2, self_record_present=False)
    helper_case("route2_group_zero_equal", 1, route=2, owner_group=0, self_group=0)
    helper_case("route2_group_zero_different", 0, route=2, owner_group=0, self_group=1)
    helper_case("route2_malformed_vector_fallback", 2, route=2, malformed=True)
    helper_case("route0_fallback", 2, route=0)
    helper_case("route1_fallback", 2, route=1)
    helper_case("route3_unknown_fallback", 2, route=3)
    helper_case("route2_missing_raid_fallback", 2, route=2, raid_present=False)
    helper_case("route2_missing_manager_fallback", 2, route=2, manager_present=False)

    predicate_case("route2_same_group_without_party", 1, route=2, party_present=False)
    predicate_case("route2_different_group_hidden", 0, route=2, owner_group=2)
    predicate_case("route2_owner_missing_hidden", 0, route=2, owner_present=False)
    predicate_case("route2_self_record_missing_hidden", 0, route=2, self_record_present=False)
    predicate_case("route2_same_group_after_party_leave_stays_visible", 1,
                   route=2, owner_group=1, party_owner_member=False, party_self_member=True)
    predicate_case("route2_malformed_party_present_fallback_allows", 1,
                   route=2, malformed=True, party_present=True)
    predicate_case("route2_malformed_party_absent_fallback_denies", 0,
                   route=2, malformed=True, party_present=False)
    predicate_case("route2_self_card_visible", 1, route=2, owner=19, party_present=False)
    predicate_case("route2_missing_object_party_allows", 1, route=2, raid_present=False, party_present=True)
    predicate_case("route2_missing_object_party_absent_denies", 0, route=2, raid_present=False, party_present=False)
    predicate_case("route0_same_group_party_allows", 1, route=0, owner_group=2, party_present=True)
    predicate_case("route0_same_group_party_absent_denies", 0, route=0, owner_group=1, party_present=False)
    predicate_case("route0_same_group_after_party_leave_denies", 0,
                   route=0, owner_group=1, party_owner_member=False, party_self_member=True)
    predicate_case("route1_party_allows", 1, route=1, owner_group=2, party_present=True)
    predicate_case("route1_party_absent_denies", 0, route=1, party_present=False)
    predicate_case("route1_same_group_after_party_leave_denies", 0,
                   route=1, owner_group=1, party_owner_member=False, party_self_member=True)
    predicate_case("route3_party_allows", 1, route=3, owner_group=2, party_present=True)
    predicate_case("route3_party_absent_denies", 0, route=3, party_present=False)
    predicate_case("route3_same_group_after_party_leave_denies", 0,
                   route=3, owner_group=1, party_owner_member=False, party_self_member=True)
    predicate_case("route0_self_card_party_absent_visible", 1,
                   route=0, owner=19, party_present=False)
    predicate_case("route1_self_card_party_absent_visible", 1,
                   route=1, owner=19, party_present=False)
    predicate_case("route3_self_card_party_absent_visible", 1,
                   route=3, owner=19, party_present=False)
    predicate_case("category2_frozen_row_unchanged", 1, route=2, owner_group=2, category=2, party_present=False)
    predicate_case("self_actor_missing_fallback_allow", 1, route=2, self_actor_present=False,
                   owner_group=2, party_present=False)

    # Same-object lifecycle: leave only the temporary encounter party while
    # FiendWar group membership remains, then leave/rejoin the raid roster.
    lifecycle = Machine(pe, receipt, route=2, owner_group=1, self_group=1, party_present=True)
    for label, records, expected in (
        ("initial-same-group", [(19, 1), (20, 1)], 1),
        ("party-leave-same-group", [(19, 1), (20, 1)], 1),
        ("depart-raid", [(19, 1)], 0),
        ("rejoin-same-group", [(19, 1), (20, 1)], 1),
        ("rejoin-other-group", [(19, 1), (20, 2)], 0),
        ("rejoin-zero-group", [(19, 0), (20, 0)], 1),
        ("zero-vs-one", [(19, 0), (20, 1)], 0),
    ):
        lifecycle.set_roster(records)
        if label == "party-leave-same-group":
            lifecycle.set_party_membership(False, True, 20, 19)
        else:
            lifecycle.set_party_membership(True, True, 20, 19)
        got, trace = lifecycle.call(PRED)
        require(got == expected, f"lifecycle {label}: {got} != {expected}; trace={trace}")
        cases.append({"name": "lifecycle_" + label, "kind": "sequence",
                      "result": got, "expected": expected, "trace": trace})

    for hidden, groups in (("draw", 2), ("input", 2), ("pass", 2)):
        entry = {"draw": DRAW, "input": INPUT_GATE, "pass": PASS}[hidden]
        m = Machine(pe, receipt, route=2, owner_group=groups, party_present=True)
        got, trace = m.call(entry, wrapper=True)
        expected = {"draw": 1, "input": 0, "pass": 1}[hidden]
        require(got == expected and not any(isinstance(x, tuple) and x[0] == "original" for x in trace), f"wrapper {hidden} not hidden")
        cases.append({"name": f"wrapper_{hidden}_hidden", "kind": "wrapper", "result": got, "trace": trace})
    for visible, groups in (("draw", 1), ("input", 1), ("pass", 1)):
        entry = {"draw": DRAW, "input": INPUT_GATE, "pass": PASS}[visible]
        m = Machine(pe, receipt, route=2, owner_group=groups, party_present=False)
        got, trace = m.call(entry, wrapper=True)
        expected = {"draw": 1, "input": 0, "pass": 1}[visible]
        require(got == expected and any(isinstance(x, tuple) and x[0] == "original" for x in trace), f"wrapper {visible} not visible")
        cases.append({"name": f"wrapper_{visible}_visible", "kind": "wrapper", "result": got, "trace": trace})

    result = {"passed": True, "caseCount": len(cases), "cases": cases,
              "candidateSha256": receipt["candidate"]["sha256"],
              "unicornVersion": unicorn.__version__,
              "scope": "bounded synthetic predicate/helper/wrapper execution",
              "limitations": ["no live GUI", "no packet parser execution", "no GPU/resource lifecycle"]}
    (build_dir / "verification.json").write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps({"passed": True, "caseCount": len(cases), "candidateSha256": result["candidateSha256"]}))


if __name__ == "__main__":
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--build-dir", type=Path, required=True)
        args = parser.parse_args()
        run_suite(args.build_dir)
    except Exception as exc:
        print(f"verification failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        raise
