"""Exact-image structural and bounded native tests for notification 541.

Set DNF_FATIGUE_CLIENT to the original V153 EXE. Native checks require Unicorn;
they execute receive-gate and counter arithmetic windows in isolated memory.
"""
import hashlib
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_fatigue_count_v153 as patch


class FatigueCountPatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_FATIGUE_CLIENT')
        if not path:
            raise unittest.SkipTest('requires exact V153 DNF_FATIGUE_CLIENT')
        cls.source = Path(path)
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_only_declared_bytes_change(self):
        self.assertEqual(len(self.before), len(self.after))
        intervals = sorted(self.receipt['changed_ranges'])
        cursor = 0
        for start, end in intervals:
            self.assertEqual(self.before[cursor:start], self.after[cursor:start])
            cursor = end
        self.assertEqual(self.before[cursor:], self.after[cursor:])
        opt, _ = patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I', self.after, opt+64)[0],
                         patch.pe.checksum(self.after, opt+64))

    def test_input_identity_and_separate_output(self):
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1])
        with tempfile.TemporaryDirectory() as tmp:
            destination = Path(tmp)/'exists.exe'
            destination.write_bytes(b'keep')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, destination)
            self.assertEqual(destination.read_bytes(), b'keep')
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source)
        self.assertEqual(hashlib.sha256(self.source.read_bytes()).hexdigest(), patch.INPUT_SHA256)

    def machine(self, data):
        try:
            import unicorn
            from unicorn import x86_const as x
        except ImportError:
            self.skipTest('requires Unicorn for bounded native execution')
        uc = unicorn.Uc(unicorn.UC_ARCH_X86, unicorn.UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        size = struct.unpack_from('<I', data, opt+56)[0]
        uc.mem_map(patch.pe.IMAGE_BASE, size)
        for va, n, raw in sections:
            uc.mem_write(va, data[raw:raw+n])
        uc.mem_map(0x20000000, 0x10000)
        uc.mem_map(0x30000000, 0x1000)
        return uc, x, unicorn

    def test_all_u16_gate_results_and_abi(self):
        before, x, _ = self.machine(self.before)
        after, _, _ = self.machine(self.after)
        changes = []
        stack, header = 0x20001000, 0x20008000
        preserved = (x.UC_X86_REG_EBX, x.UC_X86_REG_ECX, x.UC_X86_REG_EDX,
                     x.UC_X86_REG_ESI, x.UC_X86_REG_EBP)
        for uc in (before, after):
            uc.reg_write(x.UC_X86_REG_EDI, header)
            for i, reg in enumerate(preserved):
                uc.reg_write(reg, 0x12340000+i)
        for kind in (0, 1, 2, 255):
            before.mem_write(header, bytes([kind]))
            after.mem_write(header, bytes([kind]))
            for opcode in range(65536):
                results = []
                for uc in (before, after):
                    uc.mem_write(stack, struct.pack('<I', opcode))
                    uc.reg_write(x.UC_X86_REG_ESP, stack)
                    uc.emu_start(patch.CALL_VA, patch.CALL_VA+5, count=160)
                    self.assertEqual(uc.reg_read(x.UC_X86_REG_EIP), patch.CALL_VA+5)
                    self.assertEqual(uc.reg_read(x.UC_X86_REG_ESP), stack+4)
                    results.append(uc.reg_read(x.UC_X86_REG_EAX))
                if results[0] != results[1]:
                    changes.append((kind, opcode, *results))
            for uc in (before, after):
                self.assertEqual(uc.reg_read(x.UC_X86_REG_EDI), header)
                for i, reg in enumerate(preserved):
                    self.assertEqual(uc.reg_read(reg), 0x12340000+i)
        self.assertEqual(changes, [(0, 541, 0, 1)])

    def test_native_tooltip_subtraction(self):
        uc, x, unicorn = self.machine(self.after)
        obj, stack, stop = 0x20004000, 0x20001000, 0x30000000
        used = [0]

        def getter(machine, address, size, _):
            if address == 0x25DC590:
                esp = machine.reg_read(x.UC_X86_REG_ESP)
                ret = struct.unpack('<I', machine.mem_read(esp, 4))[0]
                machine.reg_write(x.UC_X86_REG_EAX, used[0])
                machine.reg_write(x.UC_X86_REG_ESP, esp+4)
                machine.reg_write(x.UC_X86_REG_EIP, ret)

        uc.hook_add(unicorn.UC_HOOK_CODE, getter, begin=0x25DC590, end=0x25DC590)
        for count, expected in [(0, 3), (1, 2), (2, 1), (3, 0), (4, 0), (255, 0)]:
            used[0] = count
            uc.mem_write(obj+0x46C, struct.pack('<I', 3))
            uc.mem_write(stack, struct.pack('<I', stop))
            uc.reg_write(x.UC_X86_REG_ESP, stack)
            uc.reg_write(x.UC_X86_REG_ECX, obj)
            uc.emu_start(0x25DC680, stop, count=40)
            self.assertEqual(uc.reg_read(x.UC_X86_REG_EAX), expected)
            self.assertEqual(uc.reg_read(x.UC_X86_REG_ESP), stack+4)

    def test_native_daily_weekly_reader_and_setter(self):
        uc, x, unicorn = self.machine(self.after)
        uc.mem_map(0, 4096)  # isolated SEH head used by the native function
        obj, vtable, definition = 0x20004000, 0x20005000, 0x20006000
        stack, stop = 0x20001000, 0x30000000
        uc.mem_write(obj, struct.pack('<I', vtable))
        for offset, va in [(0x28, stop+0x100), (0x5C, stop+0x110), (0xC, stop+0x120)]:
            uc.mem_write(vtable+offset, struct.pack('<I', va))
        state = {'body': b'', 'cursor': 0, 'rows': {}, 'keys': {}, 'next': 0x20007000}

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
                self.assertIn(width, (1, 4))
                begin = state['cursor']
                self.assertLessEqual(begin+width, len(state['body']))
                machine.mem_write(arg(1), state['body'][begin:begin+width])
                state['cursor'] += width
                return_from(machine)
            elif address == 0x2430FB0:
                return_from(machine, 0x20009000)
            elif address == 0x2A32B30:
                item_id = arg(1)
                # Item factory is a controlled dependency; native reader and
                # setter execute unchanged. 416 uses ID, second row uses group.
                group = 77 if item_id == 10015080 else 0xFFFFFFFF
                machine.mem_write(definition+0x18, struct.pack('<I', item_id))
                machine.mem_write(obj+0x20, struct.pack('<I', item_id))
                machine.mem_write(obj+0x468, struct.pack('<III', group, 3, 7))
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
        for va in targets:
            uc.hook_add(unicorn.UC_HOOK_CODE, hook, begin=va, end=va)

        def receive(body):
            state['body'], state['cursor'] = body, 0
            uc.mem_write(stack, struct.pack('<I', stop))
            uc.reg_write(x.UC_X86_REG_ESP, stack)
            uc.reg_write(x.UC_X86_REG_EBP, 0)
            uc.emu_start(0x202E240, stop, count=1500)
            self.assertEqual(uc.reg_read(x.UC_X86_REG_EIP), stop)
            self.assertEqual(state['cursor'], len(body))
            return {identity: struct.unpack('<II', uc.mem_read(va, 8))
                    for identity, va in state['keys'].items()}

        body = b'\x02'+struct.pack('<IBIB', 416, 1, 10015080, 2)+b'\x01'+struct.pack('<IB', 416, 4)
        self.assertEqual(receive(body), {(0x56CA5C0, 416): (1, 3),
                                        (0x56CA5F8, 77): (2, 3),
                                        (0x56CA630, 416): (4, 7)})
        # Non-empty daily data overwrites just its entries; weekly zero clears.
        self.assertEqual(receive(b'\x01'+struct.pack('<IB', 416, 3)+b'\0'),
                         {(0x56CA5C0, 416): (3, 3), (0x56CA5F8, 77): (2, 3)})
        self.assertEqual(receive(b'\0\0'), {})


if __name__ == '__main__':
    unittest.main()
