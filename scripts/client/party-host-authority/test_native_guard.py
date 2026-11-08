#!/usr/bin/env python3
"""Execute the scene callsite and candidate x86 guard in Unicorn.

The native local-record resolver, 0x29614D0 and leader getter are image code.
Only the manager->local-actor and UID->actor registry lookups are test leaves.
No process, network, or output EXE is used.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--input", type=Path, required=True, help="exact ACK candidate PE32")
parser.add_argument("--deps", type=Path, required=True, help="directory containing pefile/unicorn")
parser.add_argument("--output", type=Path, required=True, help="new JSON evidence path")
args = parser.parse_args()
sys.path.insert(0, str(args.deps.resolve()))
from build_candidate import build, CALLSITE, GETTER, HELPER, INPUT_SHA, LOCAL_GETTER, UID_ACTOR
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDI, UC_X86_REG_EDX, UC_X86_REG_EBP, UC_X86_REG_EIP,
    UC_X86_REG_ESI, UC_X86_REG_ESP, UC_X86_REG_EFLAGS)

data = args.input.read_bytes()
assert hashlib.sha256(data).hexdigest() == INPUT_SHA
candidate, receipt = build(data)
pe = pefile.PE(data=candidate, fast_load=True)
base_pe = pefile.PE(data=data, fast_load=True)
base = pe.OPTIONAL_HEADER.ImageBase

UIDS = (1001, 1002, 1003, 1004)  # synthetic IDs only
ACTORS = {uid: 0x600000 + i*0x1000 for i, uid in enumerate(UIDS)}
RECORDS = {uid: 0x700000 + i*0x1000 for i, uid in enumerate(UIDS)}
OUTSIDER = 0x712000
FOREIGN_LEADER_RECORD = 0x713000
MANAGER = 0x900000
STACK = 0x808000


def u32(uc, address):
    return struct.unpack("<I", uc.mem_read(address, 4))[0]


def put32(uc, address, value):
    uc.mem_write(address, struct.pack("<I", value))


def run(self_uid, query_uid, *, roster=None, attachment=None,
        local_slot=3, committed_slot=3, local_present=True,
        missing_leader=False, query_record=None, through_compare=False,
        baseline=False, query_slot_override=None):
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    for address, size in ((0x17E9000,0x1000), (0x2961000,0x1000),
                          (0x296D000,0x2000), (0x2973000,0x1000),
                          (0x6F9D000,0x1000), (0x56A3000,0x1000),
                          (0x600000,0x20000), (0x700000,0x30000),
                          (0x800000,0x10000), (0x900000,0x1000)):
        uc.mem_map(address, size)
    for address, size in ((CALLSITE,9), (0x29614D0,16),
                          (LOCAL_GETTER,80), (GETTER,32),
                          (HELPER,receipt["helper_bytes"])):
        image = base_pe if baseline and address == CALLSITE else pe
        uc.mem_write(address, image.get_data(address-base,size))
    put32(uc,0x56A3758,MANAGER)  # real 0x296E590's manager pointer
    put32(uc,0x56A3768,MANAGER)  # real 0x20180A4's committed manager pointer
    put32(uc,MANAGER+0x68,committed_slot)
    for uid, actor in ACTORS.items():
        put32(uc,actor+0x4C8,RECORDS[uid] if attachment is None else attachment.get(uid,RECORDS[uid]))
    slots = {uid: (local_slot if uid == self_uid else 0) for uid in UIDS}
    if query_slot_override is not None and query_uid != self_uid:
        slots[query_uid] = query_slot_override
    for uid, record in RECORDS.items():
        members = (roster if uid == self_uid and roster is not None
                   else UIDS + (0xFFFF,)*4)
        if missing_leader and uid == self_uid:
            members = tuple(7777 if i == local_slot else value
                            for i,value in enumerate(members))
        for index, member in enumerate(members):
            uc.mem_write(record+0x2C+4*index,struct.pack("<H",member))
        put32(uc,record+0x4C,slots[uid])
    for index, member in enumerate(UIDS+(0xFFFF,)*4):
        uc.mem_write(OUTSIDER+0x2C+4*index,struct.pack("<H",member))
    put32(uc,OUTSIDER+0x4C,0)
    for index, member in enumerate((9001,9002,9003,9004)+(0xFFFF,)*4):
        uc.mem_write(FOREIGN_LEADER_RECORD+0x2C+4*index,struct.pack("<H",member))
    put32(uc,FOREIGN_LEADER_RECORD+0x4C,0)
    query = query_record if query_record is not None else RECORDS[query_uid]
    before = (bytes(uc.mem_read(0x600000,0x20000)),
              bytes(uc.mem_read(0x700000,0x30000)),
              bytes(uc.mem_read(MANAGER,0x1000)))
    initial = {UC_X86_REG_EBX:0x12345678, UC_X86_REG_ESI:ACTORS[query_uid],
               UC_X86_REG_EDI:0x87654321, UC_X86_REG_EBP:0x808800}
    for reg,value in initial.items():
        uc.reg_write(reg,value)
    uc.reg_write(UC_X86_REG_ECX,query)
    uc.reg_write(UC_X86_REG_ESP,STACK)
    uc.reg_write(UC_X86_REG_EIP,CALLSITE)
    calls = {"local_getter":0,"uid_lookup":[],"original_getter":0,
             "manager_actor_lookup":0}

    def hook(uc, address, size, _):
        if address == LOCAL_GETTER:
            calls["local_getter"] += 1
        elif address == GETTER:
            calls["original_getter"] += 1
        elif address in (0x296DD80,UID_ACTOR):
            sp = uc.reg_read(UC_X86_REG_ESP)
            ret = u32(uc,sp)
            if address == 0x296DD80:
                calls["manager_actor_lookup"] += 1
                assert uc.reg_read(UC_X86_REG_ECX) == MANAGER
                answer = ACTORS[self_uid] if local_present else 0
            else:
                uid = u32(uc,sp+4)
                calls["uid_lookup"].append(uid)
                answer = ACTORS.get(uid,0)
            uc.reg_write(UC_X86_REG_EAX,answer)
            uc.reg_write(UC_X86_REG_ESP,sp+4)
            uc.reg_write(UC_X86_REG_EIP,ret)
    uc.hook_add(UC_HOOK_CODE,hook)
    stop = CALLSITE+9 if through_compare else CALLSITE+5
    uc.emu_start(CALLSITE,stop,count=5000)
    assert uc.reg_read(UC_X86_REG_EIP) == stop
    assert uc.reg_read(UC_X86_REG_ESP) == STACK
    checked = (UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EBP) if through_compare else tuple(initial)
    assert all(uc.reg_read(reg) == initial[reg] for reg in checked), "nonvolatile register changed"
    assert calls["original_getter"] == 1
    assert calls["local_getter"] == (0 if baseline else 1)
    assert before == (bytes(uc.mem_read(0x600000,0x20000)),
                      bytes(uc.mem_read(0x700000,0x30000)),
                      bytes(uc.mem_read(MANAGER,0x1000))), "party state mutated"
    actor = uc.reg_read(UC_X86_REG_EAX)
    selected = next((uid for uid,value in ACTORS.items() if value == actor),None)
    if through_compare:
        assert uc.reg_read(UC_X86_REG_EDI) == 0  # original xor edi,edi
        equal = bool(uc.reg_read(UC_X86_REG_EFLAGS) & 0x40)
        assert equal == (selected == query_uid), "native render comparison differs"
        calls["render_equal"] = equal
    return selected,calls


def main():
    rows=[]
    for new_slot in (1,2,3,0):
        for self_uid in UIDS:
            for query_uid in UIDS:
                stale_peer_slot = 3 if new_slot == 0 else 0
                kw={"local_slot":new_slot,"committed_slot":new_slot,
                    "query_slot_override":stale_peer_slot}
                old,calls_old=run(self_uid,query_uid,baseline=True,**kw)
                leader,calls=run(self_uid,query_uid,local_slot=new_slot,
                                 committed_slot=new_slot,
                                 query_slot_override=stale_peer_slot)
                assert old == (UIDS[new_slot] if query_uid == self_uid else UIDS[stale_peer_slot])
                assert leader == UIDS[new_slot],(new_slot,self_uid,query_uid,leader)
                rows.append({"new_slot":new_slot,"self":self_uid,"query":query_uid,
                             "stale_peer_slot":stale_peer_slot,
                             "ack_baseline":old,"guarded":leader,
                             "uid_lookups":len(calls["uid_lookup"])})
    query = 1002
    fallback = {
        "member_removed":{"roster":(1001,0xFFFF,1003,1004)+(0xFFFF,)*4},
        "actor_detached":{"attachment":{1002:0}},
        "other_record_attached":{"attachment":{1002:OUTSIDER}},
        "missing_local":{"local_present":False},
        "invalid_authority_slot":{"local_slot":9},
        "duplicate_roster":{"roster":(1001,1002,1002,1004)+(0xFFFF,)*4},
        "self_commit_pending":{"committed_slot":0},
        "missing_leader_actor":{"missing_leader":True},
        "outsider_record":{"query_record":OUTSIDER},
        "ambiguous_attachment":{"attachment":{1003:RECORDS[1002]}},
        "leader_detached":{"attachment":{1004:0}},
        "leader_foreign_record":{"attachment":{1004:FOREIGN_LEADER_RECORD}},
    }
    fallbacks=[]
    for name,kwargs in fallback.items():
        old,_=run(1001,query,baseline=True,**kwargs)
        leader,calls=run(1001,query,**kwargs)
        assert old == leader == 1001,(name,old,leader)
        fallbacks.append({"case":name,"ack_baseline":old,"guarded":leader,
                          "uid_lookups":len(calls["uid_lookup"])})
    render=[]
    for query_uid in UIDS:
        old,old_calls=run(1001,query_uid,through_compare=True,baseline=True)
        leader,calls=run(1001,query_uid,through_compare=True)
        assert calls["render_equal"] == (query_uid == 1004)
        render.append({"actor":query_uid,"ack_baseline":old,
                       "baseline_cmp_equal":old_calls["render_equal"],
                       "guarded":leader,"guarded_cmp_equal":calls["render_equal"]})
    zero=[]
    for local_present in (False,True):
        kw={"query_slot_override":0xFFFF,"local_present":local_present}
        old,_=run(1001,query,baseline=True,**kw)
        leader,_=run(1001,query,**kw)
        assert old is None and leader == (1004 if local_present else None)
        zero.append({"local_present":local_present,"ack_baseline":old,"guarded":leader})
    regroup=[]
    for name,kw,expect in (
        ("left",{"roster":(1001,0xFFFF,1003,1004)+(0xFFFF,)*4,
                 "attachment":{1002:0}},1001),
        ("rejoined",{},1004),
        ("moved_to_other_record",{"attachment":{1002:OUTSIDER}},1001)):
        old,_=run(1001,query,baseline=True,**kw)
        leader,_=run(1001,query,**kw)
        assert old == 1001 and leader == expect
        regroup.append({"phase":name,"ack_baseline":old,"guarded":leader})
    result={"input_sha256":INPUT_SHA,
        "candidate_sha256":receipt["output_sha256"],
        "callsite":"0x17E985B","helper":"0x6F9DC40",
        "executed":"candidate callsite, candidate x86 guard, native local-record and leader getters",
        "test_leaves":["0x296DD80 manager actor lookup","0x296D730 UID actor lookup"],
        "transfer_rows":rows,"fallback_rows":fallbacks,"render_compare_rows":render,
        "zero_getter_rows":zero,"regroup_rows":regroup,
        "pass":"native_x86_guard_abi_state_and_fallback"}
    with args.output.open("x",encoding="utf-8") as stream:
        json.dump(result,stream,indent=2)
        stream.write("\n")
    print(json.dumps({"pass":result["pass"],"rows":len(rows),
                      "fallbacks":len(fallbacks),"output":str(args.output),
                      "candidate_sha256":receipt["output_sha256"]}))


if __name__ == "__main__":
    main()
