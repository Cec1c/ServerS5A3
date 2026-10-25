"""Consume Go-produced daily/weekly count pages with the exact v169 reader.

Static PE loading and bounded Unicorn only; no game process is started. Packet
reads, item construction, map allocation and resource release are controlled
dependencies. The released reader and both native setters execute unchanged.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from test_patch_fatigue_count_v153 import FatigueCountPatchTests

EXE_SHA256 = '3e779126f50d02c689b27fbe3e1445ff19c8893a9c9bea9dcd8d4d5a6c432c15'


def verify(exe, fixture_path):
    data = exe.read_bytes()
    if hashlib.sha256(data).hexdigest() != EXE_SHA256:
        raise ValueError('requires the exact released v169 image')
    fixture_raw = fixture_path.read_bytes()
    fixture = json.loads(fixture_raw)
    definitions = {row['id']: row for row in fixture['definitions']}
    if len(definitions) != 662 or len(fixture['scenarios']) != 3:
        raise ValueError('requires the complete Go census fixture')
    test = FatigueCountPatchTests()
    uc, x, unicorn = test.machine(data)
    uc.mem_map(0, 4096)
    obj, vtable, definition = 0x20004000, 0x20005000, 0x20006000
    stack, stop = 0x20001000, 0x30000000
    uc.mem_write(obj, struct.pack('<I', vtable))
    for offset, address in [(0x28, stop+0x100), (0x5C, stop+0x110), (0xC, stop+0x120)]:
        uc.mem_write(vtable+offset, struct.pack('<I', address))
    state = {'body': b'', 'cursor': 0, 'keys': {}, 'next': 0x20007000}

    def return_from(machine, value=0, pop=0):
        esp = machine.reg_read(x.UC_X86_REG_ESP)
        ret = struct.unpack('<I', machine.mem_read(esp, 4))[0]
        machine.reg_write(x.UC_X86_REG_EAX, value)
        machine.reg_write(x.UC_X86_REG_ESP, esp+4+pop)
        machine.reg_write(x.UC_X86_REG_EIP, ret)

    def hook(machine, address, size, _):
        esp = machine.reg_read(x.UC_X86_REG_ESP)
        arg = lambda n: struct.unpack('<I', machine.mem_read(esp+4*n, 4))[0]
        if address in (0x37E82E0, 0x37E83C0):
            width = arg(2) if address == 0x37E82E0 else 4
            assert width in (1, 4)
            begin = state['cursor']
            assert begin+width <= len(state['body'])
            machine.mem_write(arg(1), state['body'][begin:begin+width])
            state['cursor'] += width
            return_from(machine)
        elif address == 0x2430FB0:
            return_from(machine, 0x20009000)
        elif address == 0x2A32B30:
            item_id = arg(1)
            row = definitions[item_id]
            machine.mem_write(definition+0x18, struct.pack('<I', item_id))
            machine.mem_write(obj+0x20, struct.pack('<I', item_id))
            machine.mem_write(obj+0x468, struct.pack('<III', *[
                row[key] & 0xFFFFFFFF for key in ('group', 'daily', 'weekly')]))
            return_from(machine, obj)
        elif address == stop+0x100:
            return_from(machine, definition)
        elif address == stop+0x110:
            return_from(machine, 1, 4)
        elif address == stop+0x120:
            return_from(machine, 0, 4)
        elif address in (0x985650, 0x3E4B210, 0x3E72CE4):
            return_from(machine)
        elif address == 0x25E4A20:
            table = machine.reg_read(x.UC_X86_REG_ECX)
            key = struct.unpack('<I', machine.mem_read(arg(1), 4))[0]
            identity = (table, key)
            if identity not in state['keys']:
                assert state['next']+8 < 0x20009000
                state['keys'][identity] = state['next']
                state['next'] += 8
            return_from(machine, state['keys'][identity], 4)
        elif address == 0x1D506C0:
            table = machine.reg_read(x.UC_X86_REG_ECX)
            for identity in list(state['keys']):
                if identity[0] == table:
                    del state['keys'][identity]
            return_from(machine)

    targets = (0x37E82E0, 0x37E83C0, 0x2430FB0, 0x2A32B30, 0x985650,
               0x3E4B210, 0x3E72CE4, 0x25E4A20, 0x1D506C0,
               stop+0x100, stop+0x110, stop+0x120)
    for address in targets:
        uc.hook_add(unicorn.UC_HOOK_CODE, hook, begin=address, end=address)
    packets = 0
    outcomes = []
    for scenario in fixture['scenarios']:
        for encoded in scenario['bodies']:
            state['body'], state['cursor'] = bytes.fromhex(encoded), 0
            uc.mem_write(stack, struct.pack('<I', stop))
            uc.reg_write(x.UC_X86_REG_ESP, stack)
            uc.reg_write(x.UC_X86_REG_EBP, 0)
            uc.emu_start(0x202E240, stop, count=200000)
            assert uc.reg_read(x.UC_X86_REG_EIP) == stop
            assert uc.reg_read(x.UC_X86_REG_ESP) == stack+4
            assert state['cursor'] == len(state['body'])
            packets += 1
        expected = {}
        for kind, table, group_table in [('daily', 0x56CA5C0, 0x56CA5F8),
                                          ('weekly', 0x56CA630, 0x56CA668)]:
            for item, used in scenario[kind].items():
                item_id = int(item)
                row = definitions[item_id]
                identity = (group_table, row['group']) if row['group'] >= 0 else (table, item_id)
                expected[identity] = (used, row[kind])
        actual = {identity: struct.unpack('<II', uc.mem_read(pointer, 8))
                  for identity, pointer in state['keys'].items()}
        assert actual == expected, f'native count mismatch after {scenario["hours"]}h'
        outcomes.append({'hours': scenario['hours'], 'native_rows': len(actual), 'passed': True})
    assert hashlib.sha256(exe.read_bytes()).hexdigest() == EXE_SHA256
    return {'passed': True, 'exe_sha256': EXE_SHA256,
            'fixture_sha256': hashlib.sha256(fixture_raw).hexdigest(),
            'packets': packets, 'scenarios': outcomes,
            'game_launched': False, 'game_accepted': False,
            'controlled_dependencies': 'packet IO, item factory, map storage, release helpers; native reader/setters execute'}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--exe', type=Path, required=True)
    parser.add_argument('--fixture', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        parser.error('output already exists')
    result = verify(args.exe, args.fixture)
    with args.output.open('x', encoding='utf-8', newline='\n') as target:
        json.dump(result, target, indent=2)
        target.write('\n')
    print(json.dumps(result))
