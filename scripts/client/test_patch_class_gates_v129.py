"""Execute the actual patched receive call for every u16 and packet class."""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EIP, UC_X86_REG_ESP,
    UC_X86_REG_ECX, UC_X86_REG_EDI, UC_X86_REG_ESI, UC_X86_REG_EBX, UC_X86_REG_EBP)
import patch_class_gates_v129 as patch

STACK = 0x10000800
PACKET = 0x20000000


def vm(image):
    opt, sections = patch.pe.headers(image)
    machine = Uc(UC_ARCH_X86, UC_MODE_32)
    size = struct.unpack_from('<I',image,opt+56)[0]
    machine.mem_map(patch.pe.IMAGE_BASE,(size+4095)&-4096)
    for va, count, raw in sections:
        if count:
            machine.mem_write(va,image[raw:raw+count])
    machine.mem_map(0x10000000,4096)
    machine.mem_map(PACKET,4096)
    return machine


class ClassGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ['DNF_CLASS_INPUT_EXE'])
        cls.before = cls.source.read_bytes()
        cls.after = Path(os.environ['DNF_CLASS_CANDIDATE_EXE']).read_bytes()

    def test_all_u16_actual_callsite_decisions_and_classes(self):
        machines = (vm(self.before),vm(self.after))
        decisions = {}
        saved = {UC_X86_REG_EDI:PACKET,UC_X86_REG_ESI:0x12345678,
                 UC_X86_REG_EBX:0x23456789,UC_X86_REG_EBP:0x34567890}
        for packet_class in (0,1,2,255):
            changes = []
            for opcode in range(65536):
                values = []
                for machine in machines:
                    machine.mem_write(PACKET,struct.pack('<BH',packet_class,opcode))
                    machine.reg_write(UC_X86_REG_ESP,STACK)
                    machine.reg_write(UC_X86_REG_ECX,opcode)
                    for register, value in saved.items():
                        machine.reg_write(register,value)
                    machine.emu_start(patch.FRAGMENT_VA,patch.FRAGMENT_END,count=100)
                    self.assertEqual(machine.reg_read(UC_X86_REG_EIP),patch.FRAGMENT_END)
                    self.assertEqual(machine.reg_read(UC_X86_REG_ESP),STACK)
                    for register,value in saved.items():
                        self.assertEqual(machine.reg_read(register),value)
                    values.append(machine.reg_read(UC_X86_REG_EAX))
                if values[0] != values[1]:
                    self.assertEqual(values,[0,1])
                    changes.append(opcode)
            expected = list(patch.CLASS0 if packet_class==0 else patch.CLASS1 if packet_class==1 else ())
            self.assertEqual(changes,expected)
            decisions[str(packet_class)] = changes
        receipt = dict(passed=True,native_executions=524288,actual_callsite=hex(patch.CALL_VA),
            changed_decisions_by_class=decisions,callee_saved_registers_and_stack_verified=True,
            payload_consumers_executed=False,full_ui_acceptance=False)
        if os.environ.get('DNF_CLASS_GATE_RECEIPT'):
            Path(os.environ['DNF_CLASS_GATE_RECEIPT']).write_text(json.dumps(receipt,indent=2),encoding='utf-8')

    def test_original_byte_ranges_and_readonly_executable_section(self):
        result,receipt = patch.build(self.before)
        self.assertEqual(result,self.after)
        last=0
        for start,end in receipt['original_ranges']:
            self.assertEqual(self.before[last:start],self.after[last:start])
            last=end
        self.assertEqual(self.before[last:],self.after[last:len(self.before)])
        opt,sections = patch.pe.headers(self.after)
        self.assertEqual(len(sections),24)
        self.assertEqual(sections[-1],(patch.NEW_VA,patch.NEW_SIZE,patch.NEW_RAW))
        table=opt+224
        self.assertEqual(struct.unpack_from('<I',self.after,table+23*40+36)[0],0x60000020)
        self.assertEqual(struct.unpack_from('<I',self.after,opt+56)[0],patch.NEW_RVA+patch.NEW_SIZE)
        # Independent PE checksum implementation, folded once per word.
        value=0
        for at in range(0,len(self.after),2):
            if opt+64<=at<opt+68:
                continue
            value+=struct.unpack_from('<H',self.after,at)[0]
            value=(value&65535)+(value>>16)
        value=(value&65535)+(value>>16)
        self.assertEqual(value+len(self.after),struct.unpack_from('<I',self.after,opt+64)[0])

    def test_wrong_input_repeat_and_output_overwrite_rejected(self):
        with self.assertRaises(ValueError):
            patch.build(self.after)
        with self.assertRaises(ValueError):
            patch.build(self.before[:-1]+bytes([self.before[-1]^1]))
        with tempfile.TemporaryDirectory() as directory:
            destination=Path(directory)/'DNF.exe'
            destination.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source,destination)
            self.assertEqual(destination.read_bytes(),b'preserve')
        self.assertEqual(patch.pe.sha(self.source.read_bytes()),patch.INPUT_SHA256)


if __name__=='__main__':
    unittest.main(verbosity=2)
