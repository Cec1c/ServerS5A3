"""Bounded native regression for the class0/584 participation NULL path.

The complete 01FF4940 callback, native packet readers, the real 1A64E80 and
1A64F10 state owners, and native vector mutation execute.  The own-raid getter
1A553E0 is replaced by an explicit per-call fixture only; its unmodified machine
code is checked separately below.  This never launches the game.
"""
import argparse
import collections
import hashlib
import json
from pathlib import Path
import struct
import traceback

from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID, UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
import patch_raid_participation_null as patch


INPUT_SHA256 = '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24'
INPUT_SIZE = 112541696
CALLBACK = 0x01FF4940
OWN_RAID_GETTER = 0x01A553E0
OWN_RAID_GETTER_CODE = bytes.fromhex(
    '568bf1837e080075118b460483f8ff740950e859ffffff8946088b46085ec3')
FAULT_READ = 0x01FF4CC7
ZERO_SENTINEL = 0xABCDE000
MODEL = 0x60000000
OWN_RAID = MODEL + 0x6000
STUB = 0x21000000
RETURN = 0x21000F00
PACKET = 0x20002000
STACK = 0x22008000


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def participation_body(rows):
    """Build the native one-row-count packet used by production projection."""
    out = bytearray([len(rows)])
    for dungeon, operation, members in rows:
        if not 0 <= operation <= 4 or len(members) > 255:
            raise ValueError('invalid 584 test row')
        out += struct.pack('<I', dungeon)
        out += bytes((operation, len(members)))
        for member in members:
            out += struct.pack('<H', member)
    return bytes(out)


def at_bytes(data, sections, va, size):
    offset = patch.pe.offset(sections, va, size)
    return data[offset:offset + size]


class Native:
    REGISTERS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
                 UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI,
                 UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EFLAGS)
    OWNER_CALLS = (0x1A64E80, 0x1A64F10)

    def __init__(self, data):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        image_size = struct.unpack_from('<I', data, opt + 56)[0]
        self.u.mem_map(patch.pe.IMAGE_BASE, image_size)
        for va, size, raw in sections:
            if size:
                self.u.mem_write(va, data[raw:raw + size])
        for va, size in ((0, 0x1000), (0x20000000, 0x20000),
                         (STUB, 0x1000), (0x22000000, 0x10000),
                         (MODEL, 0x1000000)):
            self.u.mem_map(va, size)
        self.u.mem_write(RETURN, b'\xCC')

        # The callback's native x86 SEH prologue needs the logical FS:0 head.
        # This page is deliberately unmapped at the post-getter site.  It is
        # never made present to hide the [EAX+3] access under test.
        self.put(0, ZERO_SENTINEL)
        self.zero_mapped = True
        self.put(0x56A3778, MODEL)
        self.u.mem_write(OWN_RAID + 3, b'\x07')

        self.allocation = MODEL + 0x10000
        self.records = {}
        self.writes = []
        self.faults = []
        self.visited = set()
        self.owner_calls = []
        self.site_eax = []
        self.getter_values = []
        self.getter_schedule = collections.deque()
        self.next_row_visits = 0
        self.zero_unmaps = 0
        self.seh_row_remaps = 0
        self.seh_cleanup_remaps = 0
        self.seh_heads = []
        self.finished = False
        self.error = None

        self.hooks = {
            0x008A9480: lambda: self.ret(value=self.alloc(self.arg())),
            0x003E70882: lambda: self.ret(),
            0x003E72CE4: lambda: self.ret(),
            0x01C6D840: self.vector_init,
            0x037DDA80: self.vector_reserve,
            0x03D40F20: lambda: self.ret(value=self.alloc(self.arg() * 4), pop=4),
            0x03C5F720: self.vector_copy,
            0x01A64C20: self.model_record,
            0x021E3C30: lambda: self.copy_words(4),
            0x00E76E60: lambda: self.copy_words(20),
            0x032B7020: lambda: self.erase(4),
            0x01A5E6E0: lambda: self.erase(20),
            0x01A553E0: self.own_raid_fixture,
            0x01A572F0: lambda: self.ret(value=0, pop=8),
            0x01F65090: lambda: self.ret(value=0),
            0x03E738B1: self.iterator_invariant,
        }
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.u.hook_add(UC_HOOK_MEM_INVALID, self.bad_memory)
        self.u.hook_add(UC_HOOK_MEM_WRITE, self.write)

    def put(self, address, value):
        self.u.mem_write(address, struct.pack('<I', value & 0xffffffff))

    def read(self, address):
        return struct.unpack('<I', self.u.mem_read(address, 4))[0]

    def reg(self, register):
        return self.u.reg_read(register)

    def arg(self, index=0):
        return self.read(self.reg(UC_X86_REG_ESP) + 4 + index * 4)

    def ret(self, value=None, pop=0):
        sp = self.reg(UC_X86_REG_ESP)
        if value is not None:
            self.u.reg_write(UC_X86_REG_EAX, value & 0xffffffff)
        self.u.reg_write(UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)

    def alloc(self, size):
        address = self.allocation
        self.allocation += (max(1, size) + 255) // 256 * 256
        if self.allocation >= MODEL + 0xF00000:
            raise AssertionError('native model fixture exhausted')
        return address

    def vector(self, address, size=4096):
        proxy, begin = self.alloc(8), self.alloc(size)
        self.put(address, proxy)
        self.put(proxy, address)
        self.put(address + 4, begin)
        self.put(address + 8, begin)
        self.put(address + 12, begin + size)

    def vector_init(self):
        ecx = self.reg(UC_X86_REG_ECX)
        self.put(ecx + 8, self.read(ecx + 4))
        self.ret()

    def vector_reserve(self):
        count = self.arg()
        begin = self.alloc(max(256, count * 4))
        ecx = self.reg(UC_X86_REG_ECX)
        old = self.read(ecx + 8) - self.read(ecx + 4)
        if old:
            self.u.mem_write(begin, bytes(self.u.mem_read(self.read(ecx + 4), old)))
        self.put(ecx + 4, begin)
        self.put(ecx + 8, begin + old)
        self.put(ecx + 12, begin + count * 4)
        self.ret(pop=4)

    def vector_copy(self):
        begin, end, destination = self.arg(2), self.arg(5), self.arg(6)
        if end < begin or end - begin >= 0x10000:
            raise AssertionError('native vector copy bounds')
        if end > begin:
            self.u.mem_write(destination, bytes(self.u.mem_read(begin, end - begin)))
        self.ret(value=destination + end - begin, pop=28)

    def copy_words(self, width):
        destination = self.arg()
        ecx = self.reg(UC_X86_REG_ECX)
        self.u.mem_write(destination, bytes(self.u.mem_read(ecx, width)))
        self.put(ecx + 8, self.read(ecx + 8) + width)
        self.ret(value=destination, pop=8)

    def erase(self, width):
        address = self.reg(UC_X86_REG_ECX)
        sp = self.reg(UC_X86_REG_ESP)
        destination, position = self.arg(0), self.read(sp + 16)
        begin, end = self.read(address + 4), self.read(address + 8)
        if not begin <= position < end or (position - begin) % width:
            raise AssertionError('native vector erase position')
        if position + width < end:
            self.u.mem_write(position, bytes(self.u.mem_read(position + width, end - position - width)))
        self.put(address + 8, end - width)
        if width == 20:
            for row in range(self.read(address + 4), self.read(address + 8), 20):
                self.put(self.read(row), row)
        self.u.mem_write(destination, struct.pack('<III', self.read(address), 0, position))
        self.ret(pop=4)

    def model_record(self):
        dungeon = self.arg()
        if dungeon not in self.records:
            record = self.alloc(64)
            self.vector(record + 8)
            self.records[dungeon] = record
        self.ret(value=self.records[dungeon], pop=4)

    def own_raid_fixture(self):
        if not self.getter_schedule:
            raise AssertionError('unexpected additional own-raid getter call')
        value = self.getter_schedule.popleft()
        self.getter_values.append(value)
        self.ret(value=value)

    def iterator_invariant(self):
        raise AssertionError('native iterator invariant')

    def bad_memory(self, _u, access, address, size, _value, _context):
        self.faults.append(dict(ip=hex(self.reg(UC_X86_REG_EIP)),
                                address=hex(address), size=size, access=access))
        return False

    def write(self, _u, _access, address, size, value, _context):
        if MODEL <= address < MODEL + 0x1000000:
            self.writes.append(dict(address=hex(address - MODEL), size=size,
                                    value=value, instruction=hex(self.reg(UC_X86_REG_EIP))))

    def hook(self, _u, at, _size, _context):
        self.visited.add(at)
        if at in self.OWNER_CALLS:
            self.owner_calls.append(at)
        if at == OWN_RAID_GETTER:
            self.own_raid_fixture()
            return
        if at == patch.SITE:
            self.site_eax.append(self.reg(UC_X86_REG_EAX))
            if self.zero_mapped:
                head = self.read(0)
                if head != self.reg(UC_X86_REG_EBP) - 0xC:
                    raise AssertionError('native callback SEH head is not active')
                self.seh_heads.append(head)
                self.u.mem_unmap(0, 0x1000)
                self.zero_mapped = False
                self.zero_unmaps += 1
        if at == patch.NEXT_ROW:
            self.next_row_visits += 1
        if at == 0x01FF4A00 and not self.zero_mapped:
            # A following row enters another native state-owner SEH frame.
            # Restore only the callback's active head for that frame; the next
            # post-getter site removes the page again before [EAX+3].
            self.u.mem_map(0, 0x1000)
            self.put(0, self.reg(UC_X86_REG_EBP) - 0xC)
            self.zero_mapped = True
            self.seh_row_remaps += 1
        if at == 0x01FF4E59:
            # All row/getter decisions are complete.  Restore only the native
            # FS exception-chain fixture required by the callback epilogue.
            if not self.zero_mapped:
                self.u.mem_map(0, 0x1000)
                self.put(0, ZERO_SENTINEL)
                self.zero_mapped = True
                self.seh_cleanup_remaps += 1
        if at == RETURN:
            self.finished = True
            self.u.emu_stop()
            return
        if at in self.hooks:
            self.hooks[at]()

    def squads(self):
        result = {}
        for dungeon, record in self.records.items():
            vector = record + 8
            rows = []
            for row in range(self.read(vector + 4), self.read(vector + 8), 20):
                rows.append([self.read(pos) for pos in range(self.read(row + 4), self.read(row + 8), 4)])
            result[str(dungeon)] = rows
        return result

    def run_packet(self, body, getters):
        if not self.zero_mapped:
            raise AssertionError('a faulted machine cannot be reused')
        self.getter_schedule = collections.deque(getters)
        self.owner_calls = []
        self.site_eax = []
        self.getter_values = []
        self.next_row_visits = 0
        self.zero_unmaps = 0
        self.seh_row_remaps = 0
        self.seh_cleanup_remaps = 0
        self.seh_heads = []
        self.faults = []
        self.finished = False
        self.error = None
        write_mark = len(self.writes)
        self.u.mem_write(PACKET, body or b'\0')
        self.put(0x57F9AD0, PACKET)
        self.put(0x57F9AD4, len(body))

        saved = ((UC_X86_REG_EBX, 0x0BADF00D),
                 (UC_X86_REG_ESI, 0xCAFE0100),
                 (UC_X86_REG_EDI, 0xCAFE0200),
                 (UC_X86_REG_EBP, 0xCAFE0300))
        for register in (UC_X86_REG_EAX, UC_X86_REG_ECX, UC_X86_REG_EDX):
            self.u.reg_write(register, 0)
        for register, value in saved:
            self.u.reg_write(register, value)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x246)
        self.put(STACK, RETURN)
        self.put(STACK + 4, 0x11223344)

        try:
            self.u.emu_start(CALLBACK, 0xffffffff, count=200000, timeout=20000000)
        except UcError as exc:
            self.error = f'{type(exc).__name__}: {exc}'
            if not self.faults:
                raise AssertionError(f'unexpected Unicorn stop: {self.error}') from exc

        if not self.finished and not self.faults and self.error is None:
            raise AssertionError(f'callback did not return or fault; eip={self.reg(UC_X86_REG_EIP):#x}')
        remaining = self.read(0x57F9AD4)
        esp = self.reg(UC_X86_REG_ESP)
        callee_saved = all(self.reg(register) == value for register, value in saved)
        fs_restored = self.zero_mapped and self.read(0) == ZERO_SENTINEL
        return dict(
            finished=self.finished,
            error=self.error,
            faults=[dict(ip=item['ip'], address=item['address'], size=item['size'], access=item['access'])
                    for item in self.faults],
            end_eip=hex(self.reg(UC_X86_REG_EIP)),
            esp=hex(esp),
            stack_clean=esp == STACK + 4,
            callee_saved=callee_saved,
            fs_restored=fs_restored,
            remaining=remaining,
            consumed=len(body) - remaining,
            getter_values=[hex(value) for value in self.getter_values],
            site_eax=[hex(value) for value in self.site_eax],
            next_row_visits=self.next_row_visits,
            owner_calls=[hex(value) for value in self.owner_calls],
            zero_unmaps=self.zero_unmaps,
            seh_row_remaps=self.seh_row_remaps,
            seh_cleanup_remaps=self.seh_cleanup_remaps,
            seh_heads=[hex(value) for value in self.seh_heads],
            squads=self.squads(),
            model_sha256=sha256(bytes(self.u.mem_read(MODEL, 0x1000000))),
            native_model_writes=self.writes[write_mark:],
        )


COMPARE_FIELDS = (
    'finished', 'faults', 'end_eip', 'esp', 'stack_clean', 'callee_saved',
    'fs_restored', 'remaining', 'consumed', 'getter_values', 'site_eax',
    'next_row_visits', 'owner_calls', 'zero_unmaps', 'seh_cleanup_remaps',
    'seh_row_remaps',
    'squads', 'model_sha256', 'native_model_writes')


def assert_clean(result, body, label):
    if not result['finished']:
        raise AssertionError(f'{label}: callback did not finish: {result}')
    if result['faults']:
        raise AssertionError(f'{label}: unexpected fault: {result["faults"]}')
    if result['remaining'] != 0 or result['consumed'] != len(body):
        raise AssertionError(f'{label}: packet not fully consumed: {result}')
    if result['end_eip'] != hex(RETURN) or not result['stack_clean']:
        raise AssertionError(f'{label}: callback return/stack mismatch: {result}')
    if not result['callee_saved']:
        raise AssertionError(f'{label}: callee-saved register mismatch: {result}')
    if not result['fs_restored']:
        raise AssertionError(f'{label}: native FS:0 cleanup mismatch: {result}')


def assert_fault3(result, label):
    if result['finished'] or len(result['faults']) != 1:
        raise AssertionError(f'{label}: expected one terminal fault: {result}')
    fault = result['faults'][0]
    if fault['ip'] != hex(FAULT_READ) or fault['address'] != '0x3':
        raise AssertionError(f'{label}: wrong fault: {fault}')


def assert_equal(source, candidate, label):
    for field in COMPARE_FIELDS:
        if source[field] != candidate[field]:
            raise AssertionError(f'{label}: changed {field}\nsource={source[field]!r}\ncandidate={candidate[field]!r}')


def checksum_fixture(source, candidate):
    if len(source) != INPUT_SIZE or sha256(source) != INPUT_SHA256:
        raise AssertionError('exact verified baseline SHA256/size required')
    opt, source_sections = patch.pe.headers(source)
    _, candidate_sections = patch.pe.headers(candidate)
    if at_bytes(source, source_sections, OWN_RAID_GETTER, len(OWN_RAID_GETTER_CODE)) != OWN_RAID_GETTER_CODE:
        raise AssertionError('01A553E0 getter structure changed')
    if at_bytes(candidate, candidate_sections, OWN_RAID_GETTER, len(OWN_RAID_GETTER_CODE)) != OWN_RAID_GETTER_CODE:
        raise AssertionError('candidate changed the global 01A553E0 getter')
    expected = (
        (patch.SITE, patch.ORIGINAL),
        (0x01FF4CA3, bytes.fromhex('8b0d78376a05e83207a6ff')),
        (FAULT_READ, bytes.fromhex('0fb640030fb709')),
        (patch.NEXT_ROW, bytes.fromhex('8b85ccfeffff')),
    )
    for address, code in expected:
        if at_bytes(source, source_sections, address, len(code)) != code:
            raise AssertionError(f'source fingerprint changed at {address:#x}')
    branch = at_bytes(candidate, candidate_sections, patch.SITE, 6)
    helper = int(patch.build(source)[1]['helperVA'], 16)
    want = b'\xe9' + struct.pack('<i', helper - patch.SITE - 5) + b'\x90'
    if branch != want:
        raise AssertionError('candidate site does not branch to the bounded helper')
    return dict(getter_va=hex(OWN_RAID_GETTER), getter_bytes=OWN_RAID_GETTER_CODE.hex(),
                getter_fixture='explicit per-call return value; native getter is not executed',
                callback_va=hex(CALLBACK), fault_va=hex(FAULT_READ), site_va=hex(patch.SITE),
                next_row_va=hex(patch.NEXT_ROW), helper_va=hex(helper),
                source_image_size=len(source), candidate_image_size=len(candidate),
                zero_page_policy='mapped only for native FS:0 SEH; unmapped at each post-getter site; '
                                 'restored only for the next row native SEH and final callback cleanup')


def verify(source, cases):
    candidate, receipt = patch.build(source)
    fixtures = checksum_fixture(source, candidate)

    single = participation_body([(210, 1, [19])])
    old = Native(source)
    old_single = old.run_packet(single, [0])
    assert_fault3(old_single, 'missing own-raid baseline')
    if old_single['site_eax'] != ['0x0'] or old_single['getter_values'] != ['0x0']:
        raise AssertionError(f'missing own-raid fixture did not reach getter with NULL: {old_single}')
    new = Native(candidate)
    new_single = new.run_packet(single, [0])
    assert_clean(new_single, single, 'missing own-raid candidate')
    if new_single['getter_values'] != ['0x0'] or new_single['site_eax'] != ['0x0']:
        raise AssertionError(f'candidate did not consume the NULL fixture: {new_single}')
    if new_single['next_row_visits'] < 1 or new_single['zero_unmaps'] != 1:
        raise AssertionError(f'candidate NULL branch did not resume at next row: {new_single}')
    if new_single['squads'] != {'210': [[19]]}:
        raise AssertionError(f'candidate lost the participation state update: {new_single["squads"]}')
    cases.append(dict(id='missing_ownraid_single_row', source=old_single, candidate=new_single,
                      passed=True))

    for operation in range(5):
        body = participation_body([(210, operation, [55])])
        needs_getter = operation in (1, 3, 4)
        getters = [0] if needs_getter else []
        old = Native(source)
        before = old.run_packet(body, getters)
        new = Native(candidate)
        after = new.run_packet(body, getters)
        if needs_getter:
            assert_fault3(before, f'missing own-raid op{operation} baseline')
        else:
            assert_clean(before, body, f'missing own-raid op{operation} unaffected baseline')
        assert_clean(after, body, f'missing own-raid op{operation} candidate')
        if before['squads'] != after['squads']:
            raise AssertionError(f'missing own-raid op{operation} lost native state: '
                                 f'{before["squads"]!r} != {after["squads"]!r}')
        cases.append(dict(id=f'missing_ownraid_op{operation}', source=before, candidate=after,
                          passed=True))

    for operation in range(5):
        body = participation_body([(210, operation, [])])
        old = Native(source)
        before = old.run_packet(body, [])
        new = Native(candidate)
        after = new.run_packet(body, [])
        assert_clean(before, body, f'empty members op{operation} baseline')
        assert_clean(after, body, f'empty members op{operation} candidate')
        assert_equal(before, after, f'empty members op{operation}')
        if before['site_eax'] or before['getter_values']:
            raise AssertionError(f'empty members op{operation}: getter unexpectedly called')
        cases.append(dict(id=f'empty_members_op{operation}', source=before, candidate=after,
                          passed=True))

    seed = participation_body([(210, 1, [91])])
    for operation in range(5):
        body = participation_body([(210, operation, [77])])
        getters = [OWN_RAID] if operation in (1, 3, 4) else []
        old = Native(source)
        old_seed = old.run_packet(seed, [OWN_RAID])
        old_state = old.run_packet(body, getters)
        new = Native(candidate)
        new_seed = new.run_packet(seed, [OWN_RAID])
        new_state = new.run_packet(body, getters)
        assert_clean(old_seed, seed, f'valid own-raid op{operation} source seed')
        assert_clean(new_seed, seed, f'valid own-raid op{operation} candidate seed')
        assert_equal(old_seed, new_seed, f'valid own-raid op{operation} seed')
        assert_clean(old_state, body, f'valid own-raid op{operation} source')
        assert_clean(new_state, body, f'valid own-raid op{operation} candidate')
        assert_equal(old_state, new_state, f'valid own-raid op{operation}')
        cases.append(dict(id=f'valid_ownraid_op{operation}', source=old_state,
                          candidate=new_state, passed=True))

    rows = [(210, 1, [101]), (210, 1, [102]), (210, 1, [103])]
    multi = participation_body(rows)
    expected_rows = [[101], [102], [103]]
    old = Native(source)
    old_valid = old.run_packet(multi, [OWN_RAID, OWN_RAID, OWN_RAID])
    new = Native(candidate)
    new_valid = new.run_packet(multi, [OWN_RAID, OWN_RAID, OWN_RAID])
    assert_clean(old_valid, multi, 'valid own-raid multi-row source')
    assert_clean(new_valid, multi, 'valid own-raid multi-row candidate')
    assert_equal(old_valid, new_valid, 'valid own-raid multi-row')
    if old_valid['getter_values'] != [hex(OWN_RAID)] * 3 or old_valid['next_row_visits'] != 3:
        raise AssertionError(f'valid multi-row did not execute every getter/row: {old_valid}')
    if old_valid['squads'] != {'210': expected_rows}:
        raise AssertionError(f'valid multi-row state mismatch: {old_valid["squads"]}')
    cases.append(dict(id='valid_ownraid_multi_row', source=old_valid, candidate=new_valid,
                      passed=True))

    old = Native(source)
    old_late = old.run_packet(multi, [0, OWN_RAID, OWN_RAID])
    assert_fault3(old_late, 'late getter baseline')
    if old_late['remaining'] <= 0:
        raise AssertionError(f'late getter baseline did not stop inside the packet: {old_late}')
    new = Native(candidate)
    new_late = new.run_packet(multi, [0, OWN_RAID, OWN_RAID])
    assert_clean(new_late, multi, 'late getter candidate')
    if new_late['getter_values'] != ['0x0', hex(OWN_RAID), hex(OWN_RAID)]:
        raise AssertionError(f'late getter schedule did not advance: {new_late}')
    if new_late['site_eax'] != ['0x0', hex(OWN_RAID), hex(OWN_RAID)]:
        raise AssertionError(f'late getter decisions did not reach every row: {new_late}')
    if new_late['next_row_visits'] != 3:
        raise AssertionError(f'late getter candidate loop count: {new_late}')
    if new_late['squads'] != {'210': expected_rows}:
        raise AssertionError(f'late getter candidate state mismatch: {new_late["squads"]}')
    cases.append(dict(id='missing_then_valid_getter_multiple_rows', source=old_late,
                      candidate=new_late, passed=True))

    return dict(
        passed=True,
        input=dict(sha256=sha256(source), size=len(source)),
        candidateReceipt=receipt,
        fixtureBoundary=fixtures,
        cases=cases,
        counts=dict(
            total=len(cases),
            missingOwnRaidBaselineFaults=sum(
                1 for case in cases if case['id'].startswith('missing_ownraid')
                and bool(case['source']['faults'])),
            candidateStateTransitions=sum(
                1 for case in cases if case['candidate']['finished']),
            emptyMemberOperations=sum(1 for case in cases if case['id'].startswith('empty_members')),
        ),
        executed=[
            'complete 01FF4940 callback for every case',
            'native byte/word/u32 packet readers through 37E82E0/37E8350/37E83C0',
            'real 1A64E80 and 1A64F10 participation state owners',
            'real vector copy/reserve/erase support reached by those owners',
        ],
        fixtures=[
            'per-call 1A553E0 own-raid getter return fixture; exact getter bytes checked unmodified',
            'allocation, indexed dungeon record lookup, announcement/UI lookups, and STL generic leaves',
            'FS:0 page only for native SEH setup and final cleanup',
        ],
        gameAcceptance='pending',
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    source = args.input.read_bytes()
    cases = []
    report = dict(passed=False, input=dict(path=str(args.input.resolve()),
                                           sha256=sha256(source), size=len(source)),
                  harness=dict(path=str(Path(__file__).resolve()),
                               sha256=sha256(Path(__file__).read_bytes())))
    try:
        result = verify(source, cases)
        report.update(result)
    except BaseException as exc:
        report.update(cases=cases, error=f'{type(exc).__name__}: {exc}',
                      traceback=traceback.format_exc(),
                      gameAcceptance='not evaluated')
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2), encoding='utf-8')
        raise
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(report, indent=2), encoding='utf-8')
    print(json.dumps(dict(passed=True, cases=result['counts']['total'],
                          candidate_sha256=result['candidateReceipt']['outputSha256'],
                          report=str(args.report.resolve()))))


if __name__ == '__main__':
    main()
