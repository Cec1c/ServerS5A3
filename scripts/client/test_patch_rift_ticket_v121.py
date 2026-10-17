"""Execute both original and candidate particular-hell arithmetic sites."""
import math
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_rift_ticket_v121 as patch
try:
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
    from unicorn.x86_const import *
except ImportError:
    Uc = None


@unittest.skipIf(Uc is None, 'install Unicorn to execute the native checks')
class RiftTicketPatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not os.environ.get('DNF_RIFT_INPUT_EXE') or not os.environ.get('DNF_RIFT_CANDIDATE_EXE'):
            raise unittest.SkipTest('set both DNF_RIFT_INPUT_EXE and DNF_RIFT_CANDIDATE_EXE')
        cls.before = Path(os.environ['DNF_RIFT_INPUT_EXE']).read_bytes()
        cls.after = Path(os.environ['DNF_RIFT_CANDIDATE_EXE']).read_bytes()
        if patch.sha(cls.before) != patch.INPUT_SHA256:
            raise ValueError('unexpected baseline EXE')

    def vm(self, image):
        pe, opt, table, sections = patch.headers(image)
        uc = Uc(UC_ARCH_X86, UC_MODE_32)
        size = struct.unpack_from('<I', image, opt + 56)[0]
        uc.mem_map(patch.IMAGE_BASE, patch.align(size, 4096))
        for section in sections:
            if section['rawsize']:
                uc.mem_write(patch.IMAGE_BASE + section['rva'], image[section['offset']:section['offset'] + section['rawsize']])
        uc.mem_map(0x10000000, 0x10000)
        uc.reg_write(UC_X86_REG_FPCW, 0x37f)
        return uc

    def cost(self, vm, site, used, weight, base=2):
        frame = 0x10009000
        if site == 'ui':
            start, end, at = 0x158f6c1, 0x158f728, frame - 0x220
        else:
            start, end, at = 0x1fab5c7, 0x1fab5eb, frame - 0xa84
        vm.mem_write(0x5681b70, struct.pack('<f', weight))
        vm.mem_write(at, struct.pack('<i', base))
        for reg, value in [(UC_X86_REG_EBP, frame), (UC_X86_REG_ESP, frame - 0x1000), (UC_X86_REG_EAX, used),
                           (UC_X86_REG_ESI, 0x12345678), (UC_X86_REG_EBX, 0x23456789), (UC_X86_REG_EDI, 0x3456789a)]:
            vm.reg_write(reg, value)
        top = vm.reg_read(UC_X86_REG_FPSW) & 0x3800
        vm.emu_start(start, end, count=3000)
        self.assertEqual(vm.reg_read(UC_X86_REG_EIP), end)
        self.assertEqual(vm.reg_read(UC_X86_REG_ESP), frame - 0x1000)
        self.assertEqual(vm.reg_read(UC_X86_REG_EBP), frame)
        self.assertEqual(vm.reg_read(UC_X86_REG_EDI), 0x3456789a)
        self.assertEqual(vm.reg_read(UC_X86_REG_FPSW) & 0x3800, top)
        if site == 'ui':
            self.assertEqual(vm.reg_read(UC_X86_REG_EBX), used)
            self.assertEqual(vm.reg_read(UC_X86_REG_ESI), used + 1)
        else:
            self.assertEqual(vm.reg_read(UC_X86_REG_EBX), 0x23456789)
            self.assertEqual(vm.reg_read(UC_X86_REG_ESI), 0x12345678)
        return struct.unpack('<i', bytes(vm.mem_read(at, 4)))[0]

    def test_actual_display_and_precheck_match_cost(self):
        old, new = self.vm(self.before), self.vm(self.after)
        for site in ('ui', 'entry'):
            self.assertEqual([self.cost(old, site, used, 1.0) for used in range(3)], [2, 2, 2])
            self.assertEqual([self.cost(new, site, used, 1.0) for used in range(3)], [2, 4, 6])
            for weight in (0.0, 0.5, 1.0, 1.5, 2.0, 3.0):
                for used in range(3):
                    with self.subTest(site=site, weight=weight, used=used):
                        self.assertEqual(self.cost(new, site, used, weight), int(2 * math.pow(used + 1, weight)))

    def test_only_two_sites_and_new_section_change(self):
        old_pe, old_opt, table, old_sections = patch.headers(self.before)
        pe, opt, _, sections = patch.headers(self.after)
        self.assertEqual(len(sections), 19)
        section = sections[-1]
        self.assertEqual(section['name'], b'.riftfee')
        self.assertEqual(self.after[section['offset']:section['offset'] + len(patch.POWER)], patch.POWER)
        ui = patch.offset_for_rva(old_sections, patch.UI_START - patch.IMAGE_BASE)
        call = patch.offset_for_rva(old_sections, patch.ENTRY_CALL - patch.IMAGE_BASE)
        header = table + len(old_sections) * 40
        ranges = sorted([(ui, ui + len(patch.OLD_UI)), (call, call + 5), (header, header + 40),
                         (pe + 6, pe + 8), (opt + 4, opt + 8), (opt + 56, opt + 60), (opt + 64, opt + 68)])
        last = 0
        for start, end in ranges:
            self.assertEqual(self.before[last:start], self.after[last:start])
            last = end
        self.assertEqual(self.before[last:], self.after[last:len(self.before)])
        self.assertEqual(len(self.after), len(self.before) + section['rawsize'])
        self.assertEqual(struct.unpack_from('<I', self.after, opt + 64)[0], patch.checksum(self.after, opt + 64))

    def test_all_notification_gate_decisions_preserved(self):
        before, after = self.vm(self.before), self.vm(self.after)
        stop, stack = 0x20000000, 0x10000000
        for vm in (before, after):
            vm.mem_map(stop, 4096)
        for opcode in range(65536):
            values = []
            for vm in (before, after):
                vm.mem_write(stack, struct.pack('<II', stop, opcode))
                vm.reg_write(UC_X86_REG_ESP, stack)
                vm.emu_start(0x6e83610, stop, count=40)
                self.assertEqual(vm.reg_read(UC_X86_REG_EIP), stop)
                self.assertEqual(vm.reg_read(UC_X86_REG_ESP), stack + 8)
                values.append(vm.reg_read(UC_X86_REG_EAX))
            self.assertEqual(values[0], values[1], opcode)

    def test_wrong_input_and_existing_output_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1] + bytes([self.before[-1] ^ 1]))
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'DNF.exe'
            target.write_bytes(b'preserve-existing')
            with self.assertRaises(ValueError):
                patch.write_candidate(Path(os.environ['DNF_RIFT_INPUT_EXE']), target)
            self.assertEqual(target.read_bytes(), b'preserve-existing')


if __name__ == '__main__':
    unittest.main(verbosity=2)
