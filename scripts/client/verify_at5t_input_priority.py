"""Run packaged AT-5T helper and original input evaluator/reader in Unicorn.

Scene, character eligibility, position, and device rows are explicit fixtures.
The priority wrapper, cache loop, descriptor state machine, shared_ptr handling,
and registered-key reader execute from the exact input/candidate EXE bytes.
Requires: pip install unicorn
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED, UC_HOOK_MEM_WRITE
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
    UC_X86_REG_ESP, UC_X86_REG_EIP)
import patch_at5t_input_priority as patch


class Native:
    INPUT, ACTOR, VEHICLE, SCENE = 0x11000000, 0x11010000, 0x11020000, 0x11030000
    AVT, POS, PVT, MANAGER = 0x11040000, 0x11050000, 0x11060000, 0x11070000
    STOP, STACK = 0x12000000, 0x111F0000

    def __init__(self, data, count=35, mount_index=31):
        self.data = data
        self.opt, self.sections = patch.pe.headers(data)
        self.image_size = struct.unpack_from('<I', data, self.opt + 56)[0]
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages, self.stubs, self.trace = set(), {}, []
        self.heap = 0x11200000
        self.u.hook_add(UC_HOOK_MEM_UNMAPPED, self.missing)
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.cache_writes = []
        self.u.hook_add(UC_HOOK_MEM_WRITE, self.record_write)
        self.map(0); self.map(self.STOP)
        self.local_actor = self.ACTOR
        self.input_result = self.INPUT
        self.qualification = 0
        self.distance = 40
        self.vehicle_action = 0
        self.object_live = 1
        self.modal = 0
        self.down = self.fresh = 0
        self.logical = 4
        self.w(self.INPUT, 0x04D263C4)  # original concrete input vtable
        self.w(self.INPUT + 0x9C, self.MANAGER)
        self.w(self.INPUT + 0x4C, 10000)
        self.w(self.ACTOR, self.AVT)
        self.w(self.ACTOR + 0xC8, self.SCENE)
        self.w(self.AVT + 0xD18, 0x12000100)
        self.w(self.AVT + 0xCAC, 0x12000110)
        self.w(self.AVT + 0x5C, 0x12000120)
        self.w(self.VEHICLE, patch.VEHICLE_VTABLE)
        self.w(self.VEHICLE + 0x400, 61235)
        self.w(self.VEHICLE + 0x908, self.ACTOR)
        self.w(self.VEHICLE + 0xC8, self.SCENE)
        self.w(self.VEHICLE + 0x5F60, 1)
        self.w(self.POS, self.PVT)
        for slot, value_at, code_at in ((0, self.POS+16, 0x12000200),
                                        (12, self.POS+20, 0x12000210)):
            self.w(self.PVT + slot, code_at)
            self.write(code_at, b'\xd9\x05' + struct.pack('<I', value_at) + b'\xc2\x04\x00')
            self.w(value_at, 0)
        elements = self.alloc(8)
        self.w(elements, self.ACTOR); self.w(elements+4, self.VEHICLE)
        self.w(self.SCENE+12, elements); self.w(self.SCENE+16, elements+8)
        self.pairs = [self.descriptor(4 if i in (0, mount_index) else 99+i)
                      for i in range(count)]
        self.mount, self.mount_control = self.pairs[mount_index]
        self.skill = self.pairs[0][0]
        self.w(self.ACTOR+0x3DE0, self.mount)
        self.w(self.ACTOR+0x3DE4, self.mount_control)
        self.cache = self.alloc(count*8)
        self.write(self.cache, b''.join(struct.pack('<II', *p) for p in self.pairs))
        self.vector(self.INPUT+0x6C, self.cache, self.cache+count*8)
        self.rows = self.alloc(24)
        self.vector(self.INPUT+0x34, self.rows, self.rows+24)
        self.original_order = self.read(self.cache, count*8)
        self.original_counts = [self.read(control+4, 8) for _, control in self.pairs]
        self.stub(patch.LOCAL_ACTOR, lambda: self.local_actor)
        self.stub(0x12000100, lambda: self.input_result)
        self.stub(0x12000110, lambda: self.qualification)
        self.stub(0x12000120, lambda: self.POS)
        self.stub(0x0288E9F0, lambda: self.POS)
        self.stub(0x026795D0, lambda: self.distance, 8)
        self.stub(0x0244B030, lambda: self.vehicle_action)
        self.stub(0x02676330, lambda: self.object_live)
        self.stub(0x02A122A0, lambda: self.modal, 4)
        self.stub(0x02A122C0, lambda: 0)
        self.stub(0x02A122B0, lambda: 0)
        self.stub(0x02A1E870, lambda: 0)
        self.stubs[0x03765810] = self.produce

    def map(self, address):
        page = address & ~4095
        if page in self.pages:
            return
        assert page == 0 or patch.pe.IMAGE_BASE <= page < patch.pe.IMAGE_BASE+self.image_size or 0x11000000 <= page <= 0x12010000, hex(page)
        self.u.mem_map(page, 4096); self.pages.add(page)
        if patch.pe.IMAGE_BASE <= page < patch.pe.IMAGE_BASE+self.image_size:
            try:
                offset = patch.pe.offset(self.sections, page, 4096)
                self.u.mem_write(page, self.data[offset:offset+4096])
            except ValueError:
                pass  # zero-filled image data

    def missing(self, u, access, address, size, value, user):
        for page in range(address & ~4095, (address+size+4095) & ~4095, 4096):
            self.map(page)
        return True

    def write(self, address, data):
        self.missing(None, None, address, len(data), None, None)
        self.u.mem_write(address, data)

    def read(self, address, size):
        self.missing(None, None, address, size, None, None)
        return bytes(self.u.mem_read(address, size))

    def w(self, address, value):
        self.write(address, struct.pack('<I', value))

    def r(self, address):
        return struct.unpack('<I', self.read(address, 4))[0]

    def alloc(self, size):
        result = self.heap
        self.heap += (size+15) & ~15
        self.write(result, bytes(size))
        return result

    def vector(self, address, begin, end):
        proxy = self.alloc(8)
        self.w(proxy, address)
        self.write(address, struct.pack('<IIII', proxy, begin, end, end))

    def descriptor(self, logical):
        key = self.alloc(0x34)
        rule, keys = self.alloc(28), self.alloc(4)
        self.w(keys, logical)
        self.vector(rule, keys, keys+4)
        self.w(rule+24, 300)
        self.vector(key, rule, rule+28)
        self.vector(key+20, 0, 0)
        self.w(key+0x28, 1)
        self.w(key+0x30, 1)
        control = self.alloc(12)
        self.w(control+4, 2); self.w(control+8, 1)
        return key, control

    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_EIP, self.r(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp+4+pop)

    def stub(self, pc, value, pop=0):
        self.stubs[pc] = lambda: self.ret(value(), pop)

    def hook(self, u, pc, size, user):
        self.trace.append(pc)
        if pc == self.STOP:
            u.emu_stop()
        elif pc in self.stubs:
            self.stubs[pc]()

    def produce(self):
        self.write(self.rows, struct.pack('<III4BII', self.logical, 100, 10000,
                                         self.down, self.fresh, 0, 0, 0, 0))
        self.ret()

    def record_write(self, u, access, address, size, value, user):
        if hasattr(self, 'original_order') and self.cache <= address < self.cache+len(self.original_order):
            self.cache_writes.append((address,size,value))

    def call(self, pc, ecx, args=()):
        self.trace = []
        saved = {UC_X86_REG_EBX: 0x11111111, UC_X86_REG_ESI: 0x22222222,
                 UC_X86_REG_EDI: 0x33333333, UC_X86_REG_EBP: 0x44444444}
        for reg, value in saved.items(): self.u.reg_write(reg, value)
        self.w(self.STACK, self.STOP)
        for i, value in enumerate(args): self.w(self.STACK+4+i*4, value)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.reg_write(UC_X86_REG_ECX, ecx)
        try:
            self.u.emu_start(pc, 0, count=200000)
        except Exception as exc:
            raise AssertionError((hex(pc), hex(self.u.reg_read(UC_X86_REG_EIP)),
                                  [hex(x) for x in self.trace[-15:]])) from exc
        assert self.u.reg_read(UC_X86_REG_EIP) == self.STOP
        assert self.u.reg_read(UC_X86_REG_ESP) == self.STACK+4+4*len(args), 'stack balance'
        for reg, value in saved.items(): assert self.u.reg_read(reg) == value, 'callee-saved register'
        return self.u.reg_read(UC_X86_REG_EAX)

    def poll(self, down, fresh):
        self.down, self.fresh = down, fresh
        self.call(patch.SITE, self.INPUT)
        poll_trace = self.trace[:]
        self.last_poll_trace = poll_trace
        if not (down and fresh):
            assert 0x12000110 not in poll_trace, 'hold/release called class eligibility'
        assert self.read(self.cache, len(self.original_order)) == self.original_order
        assert not self.cache_writes, 'cache row addresses must keep their referents throughout poll'
        assert [self.read(control+4, 8) for _, control in self.pairs] == self.original_counts
        # Native by-value shared_ptr reader consumes its temporary reference.
        self.w(self.mount_control+4, self.r(self.mount_control+4)+1)
        pressed = self.call(0x03761840, self.INPUT, (self.mount, self.mount_control, 1)) & 255
        assert self.read(self.mount_control+4, 8) == self.original_counts[self.pairs.index((self.mount,self.mount_control))]
        return {'mount': self.r(self.mount+0x2C), 'skill': self.r(self.skill+0x2C),
                'pressed': pressed, 'evaluators': poll_trace.count(patch.EVALUATOR)}


def validate(parent, candidate):
    expected, receipt = patch.build(parent)
    assert candidate == expected
    try:
        patch.build(candidate)
    except ValueError:
        pass
    else:
        raise AssertionError('reapplication accepted')
    cases = []
    for fixed, data in ((False, parent), (True, candidate)):
        n = Native(data)
        phases = [n.poll(down, fresh) for down, fresh in ((1,1),(1,0),(0,0),(1,1))]
        assert [p['pressed'] for p in phases] == ([1,0,0,1] if fixed else [0,0,0,0])
        assert [p['mount'] for p in phases] == ([1,2,0,1] if fixed else [0,0,0,0])
        cases.append({'fixed': fixed, 'phases': phases})
    rejected = {
        'no_local_actor': lambda n: setattr(n, 'local_actor', 0),
        'other_input': lambda n: setattr(n, 'input_result', n.INPUT+0x100),
        'modal_managed_input': lambda n: setattr(n, 'modal', 1),
        'disabled_mount_rule': lambda n: n.w(n.mount+0x30, 0),
        'actor_already_riding': lambda n: n.w(n.ACTOR+0x6710, n.VEHICLE),
        'actor_ride_lock': lambda n: n.w(n.ACTOR+0x6970, 1),
        'actor_entry_gate': lambda n: n.w(n.ACTOR+0x67E4, 1),
        'actor_busy': lambda n: setattr(n, 'qualification', 1),
        'actor_special_gate': lambda n: setattr(n, 'qualification', 2),
        'actor_height': lambda n: n.write(n.POS+16, struct.pack('<f', 1)),
        'actor_velocity': lambda n: n.write(n.POS+20, struct.pack('<f', 1)),
        'actor_height_nan': lambda n: n.write(n.POS+16, struct.pack('<f', float('nan'))),
        'no_scene': lambda n: n.w(n.ACTOR+0xC8, 0),
        'empty_scene': lambda n: n.w(n.SCENE+16, n.r(n.SCENE+12)),
        'null_scene_element': lambda n: n.w(n.r(n.SCENE+12)+4, 0),
        'other_vehicle_type': lambda n: n.w(n.VEHICLE, n.AVT),
        'other_vehicle_marker': lambda n: n.w(n.VEHICLE+0x400, 1),
        'other_owner': lambda n: n.w(n.VEHICLE+0x908, n.ACTOR+0x100),
        'other_scene': lambda n: n.w(n.VEHICLE+0xC8, n.SCENE+0x100),
        'occupied_vehicle': lambda n: n.w(n.VEHICLE+0x5C84, n.ACTOR),
        'interaction_state': lambda n: n.w(n.VEHICLE+0x1D8, 1),
        'vehicle_lock': lambda n: n.w(n.VEHICLE+0x5F64, 1),
        'vehicle_action20': lambda n: setattr(n, 'vehicle_action', 20),
        'vehicle_action22': lambda n: setattr(n, 'vehicle_action', 22),
        'vehicle_not_local': lambda n: setattr(n, 'object_live', 0),
        'distance120': lambda n: setattr(n, 'distance', 120),
        'distance_far': lambda n: setattr(n, 'distance', 500),
        'unregistered_actor_rule': lambda n: n.w(n.ACTOR+0x3DE0, n.descriptor(4)[0]),
    }
    for mode in (2,3,4,5,0xFFFFFFFF):
        rejected['vehicle_mode'+str(mode)] = lambda n, mode=mode: n.w(n.VEHICLE+0x5F60, mode)
    for name, setup in rejected.items():
        n = Native(candidate)
        setup(n)
        result = n.poll(1,1)
        assert result['skill'] == 1 and result['pressed'] == 0, (name, result)
        cases.append({'case': name, 'result': result})
        if name in ('no_scene','empty_scene','other_owner','distance120','distance_far'):
            assert 0x12000110 not in n.last_poll_trace, 'ordinary context called eligibility'
    for name, setup in {
        'distance119': lambda n: setattr(n, 'distance', 119),
        'vehicle_mode0': lambda n: n.w(n.VEHICLE+0x5F60, 0),
        'nonpositive_vertical_velocity': lambda n: n.write(n.POS+20, struct.pack('<f', -1)),
    }.items():
        n = Native(candidate); setup(n)
        result = n.poll(1,1)
        assert result['pressed'] == 1 and result['skill'] == 0, (name,result)
        cases.append({'case': name, 'result': result})
    for count, index in ((2,0),(2,1),(35,1),(35,17),(35,34)):
        n = Native(candidate, count, index)
        result = n.poll(1,1)
        assert result['pressed'] == 1
        cases.append({'case': 'cache_layout', 'count': count, 'index': index, 'result': result})
    # Unmatched mount key must not reorder the relative priority of other skills.
    n = Native(candidate)
    for key, _ in (n.pairs[0], n.pairs[1]):
        n.w(n.r(n.r(key+4)+4), 77)
    n.logical = 77
    result = n.poll(1,1)
    assert result['pressed'] == 0 and result['skill'] == 1 and n.r(n.pairs[1][0]+0x2C) == 0
    cases.append({'case': 'other_key_preserves_skill_order', 'result': result})
    n = Native(candidate)
    assert n.poll(1,1)['pressed'] == 1
    n.distance = 120
    n.poll(0,0)
    result = n.poll(1,1)
    assert result['skill'] == 1 and result['pressed'] == 0
    n.poll(0,0); n.distance = 119
    assert n.poll(1,1)['pressed'] == 1
    cases.append({'case': 'leave_and_reenter', 'result': result})
    # Independently verify the file delta, existing image tail, and PE checksum.
    opt, sections = patch.pe.headers(parent)
    pe_at = opt-24
    table_end = opt+struct.unpack_from('<H',parent,pe_at+20)[0]+28*40
    site = patch.pe.offset(sections, patch.SITE, 5)
    loop = patch.pe.offset(sections, patch.LOOP_SITE, 7)
    allowed = [(site,site+5),(loop,loop+7),(pe_at+6,pe_at+8),(opt+4,opt+8),(opt+56,opt+60),
               (opt+64,opt+68),(table_end,table_end+40)]
    cursor = 0
    for lo, hi in sorted(allowed):
        assert parent[cursor:lo] == candidate[cursor:lo]
        cursor = hi
    assert parent[cursor:] == candidate[cursor:len(parent)]
    assert len(candidate) == len(parent)+4096
    assert patch.pe.checksum(candidate,opt+64) == struct.unpack_from('<I',candidate,opt+64)[0]
    return {'passed': True, 'input_sha256': patch.INPUT_SHA256,
            'output_sha256': hashlib.sha256(candidate).hexdigest(), 'cases': cases,
            'case_count': len(cases), 'exact_delta_only': True,
            'native_ui_acceptance': False,
            'fixtures': ['scene/actor/vehicle state', 'native eligibility return',
                         'native distance/position return', 'device logical input rows']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    result = validate(args.input.read_bytes(), args.candidate.read_bytes())
    args.receipt.write_text(json.dumps(result, indent=2), encoding='utf-8')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
