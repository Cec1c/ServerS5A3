"""Hash-matched V124 offline communication evidence; never writes an EXE.

Requires pefile and Unicorn 2.x. Executes the native receive gate, COMMAND475
handler/u32 reader/UI wrapper, send decision path and quest membership lookup.
Rendering, allocation, wallet virtual methods and transport are explicit stubs.
The harness composes gate and handler, not the whole network dispatcher.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EIP, UC_X86_REG_ESP

SHA256 = "eaa2485609fa9cc2c9ca99fadd743b5206a37b5517dd07d195eb35b3c0a29d50"
p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--exe", type=Path, required=True)
p.add_argument("--candidate", type=Path, help="optional generated EXE; prove its complete byte delta before native verification")
p.add_argument("--receipt", type=Path, required=True)
args = p.parse_args()
data = args.exe.read_bytes()
assert hashlib.sha256(data).hexdigest() == SHA256, "unsupported client hash"
pe = pefile.PE(data=data, fast_load=True)
base = pe.OPTIONAL_HEADER.ImageBase
assert base == 0x400000
u = Uc(UC_ARCH_X86, UC_MODE_32)
u.mem_map(base, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) // 4096 * 4096)
u.mem_write(base, data[:pe.OPTIONAL_HEADER.SizeOfHeaders])
for section in pe.sections:
    u.mem_write(base + section.VirtualAddress, section.get_data())
for address, size in [(0, 4096), (0x10000000, 0x20000), (0x20000000, 0x20000), (0x30000000, 4096)]:
    u.mem_map(address, size)

def w(address, value):
    u.mem_write(address, struct.pack("<I", value & 0xffffffff))

def rd(address):
    return struct.unpack("<I", u.mem_read(address, 4))[0]

stack, stop = 0x10010000, 0x30000000
window, wallet, vtable, dialog, buffer, config = [0x20000000 + n * 0x1000 for n in range(1, 7)]
setter, getter = stop + 0x100, stop + 0x200
w(wallet, vtable)
w(vtable + 0x40, setter)
w(vtable + 0x48, getter)
w(config, 10000)
state = {"mode": "gate", "calls": []}

def ret(pop=0, value=0):
    sp = u.reg_read(UC_X86_REG_ESP)
    target = rd(sp)
    u.reg_write(UC_X86_REG_EAX, value & 0xffffffff)
    u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
    u.reg_write(UC_X86_REG_EIP, target)

def hook(uc, address, size, user):
    if address == stop:
        u.emu_stop()
        return
    if state["mode"] not in ("receiver", "sender"):
        return
    sp = u.reg_read(UC_X86_REG_ESP)
    if address == 0x032ba120:
        assert rd(sp + 4) == 0x363
        ret(4, window if state["window"] else 0)
    elif address == 0x03e73cb6:
        ret(value=window if state["window"] else 0)
    elif address == 0x025ea540:
        ret(value=wallet)
    elif address == setter:
        assert u.reg_read(UC_X86_REG_ECX) == wallet and rd(sp + 8) == 1
        state["calls"].append({"wallet_set": rd(sp + 4)})
        ret(8)
    elif address == getter:
        ret(value=state["gold"])
    elif address == 0x023f0890:
        ret(value=state["npc"])
    elif address == 0x02047360:
        ret(4, window)
    elif address == 0x020470a0:
        ret(value=window)
    elif address == 0x0287a8e0:
        ret(value=0)  # no previously open communication dialog
    elif address == 0x037f2170:
        ret(value=dialog)
    elif address == 0x023c91d0:
        ret(value=dialog)
    elif address == 0x0288a260:
        ret(32)
    elif address == 0x023c8d90:
        state["calls"].append({"open_npc": rd(sp + 4)})
        ret(4)
    elif address == 0x020a5180:
        ret(value=config)
    elif address == 0x0374ea10:
        ret(value=window)
    elif address == 0x0374f280:
        state["calls"].append({"begin_command": rd(sp + 4)})
        ret(4)
    elif address == 0x03750ac0:
        state["calls"].append({"send": True})
        ret()
    elif address in (0x0374f390, 0x0374f3c0, 0x0374f3f0):
        raise AssertionError("unexpected payload appender")
    elif address == 0x039a6620:
        state["calls"].append({"notice_id": rd(sp + 4)})
        ret(value=buffer)
    elif address == 0x008ef130:
        ret()
    elif address == 0x032bea50:
        ret(12)
    elif address == 0x03e72ce4:
        ret()

u.hook_add(UC_HOOK_CODE, hook)

def run(entry, args=(), this=0, pop=0):
    w(stack, stop)
    for i, value in enumerate(args):
        w(stack + 4 + 4 * i, value)
    u.reg_write(UC_X86_REG_ESP, stack)
    u.reg_write(UC_X86_REG_ECX, this)
    u.emu_start(entry, 0, count=4000)
    assert u.reg_read(UC_X86_REG_EIP) == stop, hex(u.reg_read(UC_X86_REG_EIP))
    assert u.reg_read(UC_X86_REG_ESP) == stack + 4 + pop
    return u.reg_read(UC_X86_REG_EAX)

gate, bitmap, opcode = 0x06e83610, 0x06e92300, 475
byte_address = bitmap + opcode // 8
original = bytes(u.mem_read(byte_address, 1))[0]
counterfactual = original | (1 << (opcode % 8))
assert original == 0 and counterfactual == 8
candidate_sha256 = None
if args.candidate:
    candidate = args.candidate.read_bytes()
    offset = pe.get_offset_from_rva(byte_address - base)
    assert len(candidate) == len(data), "candidate size changed"
    assert candidate[:offset] == data[:offset] and candidate[offset + 1:] == data[offset + 1:], "unexpected candidate byte change"
    assert candidate[offset] == counterfactual, "candidate did not enable only 475"
    candidate_sha256 = hashlib.sha256(candidate).hexdigest()
changed = []
for code in range(65536):
    values = []
    for byte in (original, counterfactual):
        u.mem_write(byte_address, bytes([byte]))
        values.append(run(gate, [code], pop=4))
    if values[0] != values[1]:
        changed.append(code)
assert changed == [475]

receivers = []
for npc in (2000, 2001, -1):
    for gold in (0, 123456, 200000000):
        for exists in (True, False):
            for admitted in (False, True):
                state.update(mode="gate", calls=[], npc=npc, window=exists)
                u.mem_write(byte_address, bytes([counterfactual if admitted else original]))
                assert run(gate, [475], pop=4) == int(admitted)
                w(buffer, 10000)
                w(buffer + 4, gold)
                w(0x057f9ad0, buffer)
                w(0x057f9ad4, 8)
                w(0x057f9af8, 0)
                if admitted:
                    state["mode"] = "receiver"
                    run(0x01fbec30, [0, 1, 0])
                expected = []
                if admitted and exists:
                    expected.append({"wallet_set": gold})
                    if npc >= 0:
                        expected.append({"open_npc": npc})
                assert state["calls"] == expected, state
                assert rd(0x057f9ad4) == (0 if admitted else 8)
                receivers.append(dict(npc=npc, gold=gold, window=exists, admitted=admitted, calls=expected))

failures = []
for error in (1, 10, 22):
    state.update(mode="predicate", calls=[])
    assert run(0x008bab80, [475]) & 0xff == 0  # generic dispatcher consumes u8 error
    state["mode"] = "receiver"
    w(0x057f9ad4, 0)
    run(0x01fbec30, [0, 0, error])
    assert state["calls"] == [] and rd(0x057f9ad4) == 0
    failures.append(dict(error=error, no_ui_or_wallet_callback=True))

senders = []
for gold in (9999, 10000, 20000):
    for npc in (2000, 2001, -1):
        state.update(mode="sender", calls=[], gold=gold, npc=npc)
        run(0x023f0cd0, this=window)
        expected = [{"begin_command": 475}, {"send": True}] if gold >= 10000 else [{"notice_id": 0x7582}]
        assert state["calls"] == expected, state
        senders.append(dict(gold=gold, local_npc=npc, calls=expected, body_bytes=0))

memberships = []
manager, vector, definition = 0x20008000, 0x20009000, 0x2000a000
for active in ([], [3734], [3741], [3734, 3741]):
    w(manager + 8, vector)
    w(manager + 12, vector + 12 * len(active))
    for i, quest in enumerate(active):
        w(vector + 12 * i, definition + 0x100 * i)
        w(definition + 0x100 * i, quest)
    for query in (3734, 3741):
        state["mode"] = "membership"
        found = bool(run(0x01c95180, [query], this=manager, pop=4) & 0xff)
        assert found == (query in active)
        memberships.append(dict(active=active, query=query, found=found))

assert hashlib.sha256(args.exe.read_bytes()).hexdigest() == SHA256
if args.candidate:
    assert hashlib.sha256(args.candidate.read_bytes()).hexdigest() == candidate_sha256
receipt = dict(passed=True, exe_sha256=SHA256, image_base=hex(base),
               candidate_sha256=candidate_sha256, disk_candidate_verified=bool(args.candidate),
               compared_opcodes=65536, changed_admissions=changed,
               original_gate_byte=original, memory_only_gate_byte=counterfactual,
               gate_rva=hex(gate-base), bitmap_byte_rva=hex(byte_address-base),
               receiver_cases=receivers, failure_cases=failures, sender_cases=senders,
               membership_cases=memberships, disk_exe_unchanged=True,
               scope=__doc__, native_ui_acceptance=False)
args.receipt.write_text(json.dumps(receipt, indent=2), encoding="utf-8")
print(json.dumps(dict(passed=True, gate_opcodes=65536, receiver_cases=len(receivers),
                     failure_cases=len(failures), sender_cases=len(senders),
                     membership_cases=len(memberships), disk_exe_unchanged=True)))
