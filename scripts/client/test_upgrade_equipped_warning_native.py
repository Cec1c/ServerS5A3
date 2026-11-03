"""Exact-input offline native sender checks; no running game is touched."""
import hashlib
import os
from pathlib import Path
import struct
import unittest
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED
from unicorn import x86_const as X

import patch_upgrade_equipped_warning as patch
from patch_receivers_v128 import headers, offset

class Native:
    """Run only the image instructions reached by this sender fixture."""

    STACK = 0x20010000
    STOP = 0x12000000

    def __init__(self, data):
        self.pe = pefile.PE(data=data, fast_load=True)
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages, self.hooks = set(), {}
        self.u.hook_add(UC_HOOK_MEM_UNMAPPED, self.unmapped)
        self.u.hook_add(UC_HOOK_CODE, self.code)
        self.ensure(0, 4096)
        self.ensure(self.STACK - 0x20000, 0x30000)
        self.ensure(self.STOP, 4096)

    def ensure(self, address, size):
        for page in range(address & ~4095, (address + size + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.u.mem_map(page, 4096)
                self.pages.add(page)
                if 0x400000 <= page < 0x400000 + self.pe.OPTIONAL_HEADER.SizeOfImage:
                    self.u.mem_write(page, self.pe.get_data(page - 0x400000, 4096))

    def unmapped(self, _u, _access, address, size, _value, _user):
        if 0x400000 <= address < 0x400000 + self.pe.OPTIONAL_HEADER.SizeOfImage:
            self.ensure(address, size)
            return True
        raise AssertionError('unexpected memory ' + hex(address))

    def put(self, address, *values):
        self.ensure(address, 4 * len(values))
        self.u.mem_write(address, struct.pack('<' + 'I' * len(values), *values))

    def read(self, address):
        return struct.unpack('<I', self.u.mem_read(address, 4))[0]

    def arg(self, index=0):
        return self.read(self.u.reg_read(X.UC_X86_REG_ESP) + 4 + 4 * index)

    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(X.UC_X86_REG_ESP)
        self.u.reg_write(X.UC_X86_REG_EAX, value)
        self.u.reg_write(X.UC_X86_REG_EIP, self.read(sp))
        self.u.reg_write(X.UC_X86_REG_ESP, sp + 4 + pop)

    def code(self, u, address, _size, _user):
        if address == self.STOP:
            u.emu_stop()
        elif address in self.hooks:
            self.hooks[address]()


@unittest.skipUnless(os.environ.get('DNF_UPGRADE_WARNING_CLIENT'), 'exact V207 input required')
class Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.before = Path(os.environ['DNF_UPGRADE_WARNING_CLIENT']).read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def exercise(self, data, mode, worn, resolved_list=3, worn_slot=12):
        n = Native(data)
        lookups, strings, popups, continued = [], [], [], []
        target, material, manager, text = 700, 9100, 0x11000000, 0x11001000
        n.put(manager, *([0]*40))
        n.put(text, 0)
        n.hooks[0x1fa2790] = lambda:n.ret(manager)
        n.hooks[0x2526cd0] = lambda:n.ret(0, 4)
        def inventory_lookup():
            item = n.arg()
            lookups.append(item)
            n.ret((-1 & 0xffffffff) if worn and item == target else (9 if item == target else 121), 4)
        def equipped_lookup():
            self.assertEqual(n.arg(1), target)
            n.put(n.arg(), resolved_list)
            n.ret(worn_slot & 0xffffffff)
        def lookup_string():
            strings.append(n.arg())
            n.ret(text)
        def popup():
            popups.append((n.arg(), n.arg(1), n.arg(2)))
            n.ret(0, 12)
        def resume():
            continued.append((n.read(n.u.reg_read(X.UC_X86_REG_EBP)-0x6c),
                              n.read(n.u.reg_read(X.UC_X86_REG_EBP)-0x70)))
            n.u.emu_stop()
        n.hooks.update({0x25ece10:inventory_lookup,0x25ed1b0:equipped_lookup,
                        0x39a6620:lookup_string,0x32bea50:popup,
                        0x3e72ce4:lambda:n.ret(),patch.RESUME:resume})
        n.put(n.STACK,n.STOP,target,material,mode)
        n.u.reg_write(X.UC_X86_REG_ESP,n.STACK)
        n.u.reg_write(X.UC_X86_REG_EBP,n.STACK+0x1000)
        n.u.reg_write(X.UC_X86_REG_ECX,manager)
        n.u.reg_write(X.UC_X86_REG_EBX,0x12345678)
        n.u.reg_write(X.UC_X86_REG_ESI,0x23456789)
        n.u.reg_write(X.UC_X86_REG_EDI,0x3456789a)
        n.u.emu_start(0x25fdab0,0,count=100000)
        if not continued:
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_EIP), n.STOP)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ESP), n.STACK+16)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_EAX)&255,0)
            for reg,value in [(X.UC_X86_REG_EBX,0x12345678),(X.UC_X86_REG_ESI,0x23456789),(X.UC_X86_REG_EDI,0x3456789a)]:
                self.assertEqual(n.u.reg_read(reg),value)
        return lookups,strings,popups,continued

    def test_original_resolves_worn_container_but_continues_sending(self):
        for mode in (0,1):
            lookups,strings,popups,continued=self.exercise(self.before,mode,True)
            self.assertEqual(lookups,[700,9100])
            self.assertEqual((strings,popups,continued),([],[],[(3,12)]))

    def test_original_wire_header_is_mode_byte_then_container_byte(self):
        # Execute the actual writer prefix after target resolution. The server's
        # old u16 decoder reads 00 03 as 768 and returns generic missing-item 4.
        for mode in (0,1):
            n = Native(self.before)
            frame, manager = 0x11010000, 0x11020000
            n.put(frame-0x70,12,3)
            n.put(frame+0x10,mode)
            n.put(manager,0)
            wire, opcodes = bytearray(), []
            n.hooks[0x374ea10] = lambda:n.ret(manager)
            def begin():
                opcodes.append(n.arg())
                n.ret(0,4)
            def append(size):
                wire.extend((n.arg() & ((1 << (size*8))-1)).to_bytes(size,'little'))
                n.ret(0,4)
            n.hooks[0x374f280] = begin
            n.hooks[0x374f390] = lambda:append(1)
            n.hooks[0x374f3c0] = lambda:append(2)
            n.hooks[0x25fe123] = lambda:n.u.emu_stop()
            n.put(n.STACK,n.STOP)
            n.u.reg_write(X.UC_X86_REG_ESP,n.STACK)
            n.u.reg_write(X.UC_X86_REG_EBP,frame)
            n.u.emu_start(0x25fe0e5,0,count=10000)
            self.assertEqual(opcodes,[80])
            self.assertEqual(wire,bytes([mode,3,12,0]))
            self.assertEqual(struct.unpack_from('<H',wire)[0],768+mode)

    def test_worn_reinforce_and_amplify_use_native_messages_without_material_lookup(self):
        for mode,string_id in [(0,1651),(1,21302)]:
            lookups,strings,popups,continued=self.exercise(self.after,mode,True)
            self.assertEqual(lookups,[700])
            self.assertEqual(strings,[string_id])
            self.assertEqual(popups,[(0x2d6,0x11001000,0)])
            self.assertEqual(continued,[])

    def amplify_window(self, data, sender_result=None, repeat=False):
        """Actual coupon caller, sender and failure cleanup, including its ABI.

        Item lookup, precondition queries, text/modal leaves and expensive UI
        object leaves are adapters. The caller's dispatch, field clearing,
        security cookie, sender guard and native returns execute from the PE.
        """
        n = Native(data)
        manager, ui, text = 0x11000000, 0x11002000, 0x11001000
        n.put(manager, *([0]*40))
        n.put(ui, *([0]*280))
        n.put(text, 0)
        n.put(0x56a379c, manager)
        n.put(0x57e1924, 0)
        strings, states, lookups = [], [], []
        n.hooks[0x3816310] = lambda:n.ret(100)
        n.hooks[0x25ea1b0] = lambda:n.ret(1)
        n.hooks[0x330d290] = lambda:n.ret(1)
        n.hooks[0x1fa2790] = lambda:n.ret(manager)
        n.hooks[0x2526cd0] = lambda:n.ret(0,4)
        def inventory_lookup():
            lookups.append(n.arg())
            n.ret(0xffffffff if n.arg()==700 else 121,4)
        def equipped_lookup():
            self.assertEqual(n.arg(1),700)
            n.put(n.arg(),3)
            n.ret(12)
        def lookup_string():
            strings.append(n.arg())
            n.ret(text)
        def state():
            states.append(n.arg())
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ECX),ui)
            n.put(ui+0x1ec,n.arg())
            n.ret(0,4)
        n.hooks.update({0x25ece10:inventory_lookup,0x25ed1b0:equipped_lookup,
                        0x39a6620:lookup_string,0x32bea50:lambda:n.ret(0,12),
                        0x8ccec0:lambda:n.ret(0,4),0xc3b0a0:lambda:n.ret(0,4),
                        0xf11f90:lambda:n.ret(),0x33138f0:state,
                        0x32b4730:lambda:n.ret(),0x32ba120:lambda:n.ret(0,4),
                        0x2531a60:lambda:n.ret(),0x37de240:lambda:n.ret(text),
                        0x2531940:lambda:n.ret()})
        def forbidden_send():
            self.fail('rejected worn coupon reached network output')
        n.hooks[0x374ea10] = forbidden_send
        results = []
        for result in ([sender_result,0,1] if repeat else [sender_result]):
            if result is not None:
                n.hooks[0x25fdab0] = lambda result=result:n.ret(result,12)
            n.put(ui+0x1ec,1)
            n.put(ui+0x220,700)
            n.put(ui+0x228,9100)
            n.put(n.STACK,n.STOP)
            n.u.reg_write(X.UC_X86_REG_ESP,n.STACK)
            n.u.reg_write(X.UC_X86_REG_EBP,n.STACK+0x1000)
            n.u.reg_write(X.UC_X86_REG_ECX,ui)
            saved = [(X.UC_X86_REG_EBX,0x12345678),(X.UC_X86_REG_ESI,0x23456789),
                     (X.UC_X86_REG_EDI,0x3456789a)]
            for reg,value in saved:n.u.reg_write(reg,value)
            # No cookie hook: its actual comparison/return must preserve the
            # handled-reject EAX marker while AL remains a false bool result.
            n.u.emu_start(0x33122c0,0,count=100000)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_EIP),n.STOP)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_ESP),n.STACK+4)
            self.assertEqual(n.u.reg_read(X.UC_X86_REG_EBP),n.STACK+0x1000)
            for reg,value in saved:self.assertEqual(n.u.reg_read(reg),value)
            self.assertEqual(n.read(manager+0x6c),0)
            self.assertEqual(n.read(manager+0x70),0)
            results.append((list(strings),list(states),n.read(ui+0x220),
                            n.read(ui+0x228),n.read(ui+0x1ec)))
        return results,lookups

    @unittest.skipUnless(os.environ.get('DNF_UPGRADE_WARNING_PREVIOUS_CLIENT'),'previous exact candidate required')
    def test_previous_popup_reproduces_duplicate_busy_warning(self):
        previous = Path(os.environ['DNF_UPGRADE_WARNING_PREVIOUS_CLIENT']).read_bytes()
        self.assertEqual(hashlib.sha256(previous).hexdigest(),patch.PREVIOUS_INPUT)
        result,_ = self.amplify_window(previous)
        self.assertEqual(result,[([21302,1659],[0],0,0,0)])
        upgraded,_ = patch.build(previous)
        self.assertEqual(upgraded,self.after)

    def test_rejected_coupon_caller_shows_once_and_clears_selection(self):
        result,lookups = self.amplify_window(self.after)
        self.assertEqual(result,[([21302],[0],0,0,0)])
        self.assertEqual(lookups,[700])

    def test_regular_busy_and_success_caller_paths_remain_unchanged(self):
        for result in (0,1,0x123400):
            self.assertEqual(self.amplify_window(self.before,result),
                             self.amplify_window(self.after,result))
        result,_ = self.amplify_window(self.after,repeat=True)
        self.assertEqual(result[0],([21302],[0],0,0,0))
        self.assertEqual(result[1],([21302,1659],[0,0],0,0,0))
        self.assertEqual(result[2],([21302,1659],[0,0,2],700,9100,2))

    def test_reinforce_caller_preserves_saved_ebx_and_other_results(self):
        for result in (0,1,0x123400,0x6f8b100):
            for data in (self.before,self.after):
                n = Native(data)
                strings=[]
                n.put(n.STACK,0x12345678,0x3456789a,0x23456789,n.STOP)
                n.u.reg_write(X.UC_X86_REG_ESP,n.STACK)
                n.u.reg_write(X.UC_X86_REG_EAX,result)
                n.put(0x11001000,0)
                n.hooks[0x39a6620] = lambda:(strings.append(n.arg()),n.ret(0x11001000))
                n.hooks[0x32bea50] = lambda:n.ret(0,12)
                n.u.emu_start(0x17f3c2a,0,count=10000)
                self.assertEqual(n.u.reg_read(X.UC_X86_REG_EIP),n.STOP)
                self.assertEqual(n.u.reg_read(X.UC_X86_REG_ESP),n.STACK+16)
                for reg,value in [(X.UC_X86_REG_EBX,0x12345678),
                                  (X.UC_X86_REG_EDI,0x3456789a),
                                  (X.UC_X86_REG_ESI,0x23456789)]:
                    self.assertEqual(n.u.reg_read(reg),value)
                want=[] if result&255 else [1659]
                if data is self.after and result==0x6f8b100:want=[]
                self.assertEqual(strings,want)

    def test_backpack_and_other_modes_preserve_original_path(self):
        for mode in range(6):
            self.assertEqual(self.exercise(self.before,mode,False), self.exercise(self.after,mode,False))
        for mode in range(2,6):
            self.assertEqual(self.exercise(self.before,mode,True), self.exercise(self.after,mode,True))
        self.assertEqual(self.exercise(self.before,0,True,2),self.exercise(self.after,0,True,2))

    def test_missing_target_is_not_mislabeled_as_equipped(self):
        for mode in (0,1):
            before = self.exercise(self.before,mode,True,worn_slot=-1)
            after = self.exercise(self.after,mode,True,worn_slot=-1)
            self.assertEqual(before,after)
            self.assertEqual(after[1],[])

    def test_only_declared_ranges_change_with_same_rx_flags_size_and_sections(self):
        self.assertEqual(len(self.after),len(self.before))
        ranges=sorted(self.receipt['changed_ranges'])
        last=0
        for start,end in ranges:
            self.assertEqual(self.after[last:start],self.before[last:start])
            last=end
        self.assertEqual(self.after[last:],self.before[last:])
        self.assertEqual(headers(self.before)[1],headers(self.after)[1])
        self.assertNotEqual(self.after,self.before)

    @unittest.skipUnless(os.environ.get('DNF_UPGRADE_WARNING_DSTR'), 'audited dstr fixture not supplied')
    def test_audited_native_messages_match_the_unmodified_player_dstr(self):
        from patch_character_server_name import resources, decode
        import pefile
        data = Path(os.environ['DNF_UPGRADE_WARNING_DSTR']).read_bytes()
        self.assertEqual(hashlib.sha256(data).hexdigest(), 'ee8904c8540ef63807dc984604a3ea0f5cc4040228e5ac2e95d490c03d21ec08')
        pe = pefile.PE(data=data)
        row = next(r for r in resources(pe) if r[:2] == (102,1042))
        text = decode(data[row[2]:row[2]+row[3]])
        self.assertIn('1651>无法强化穿戴着的装备', text)
        self.assertIn('21302>无法对穿戴中的装备进行装备增幅。', text)

    def test_unknown_inputs_and_repeat_application_are_rejected(self):
        for bad in (b'',self.before[:-1],self.before[:-1]+bytes([self.before[-1]^1]),self.after):
            with self.assertRaises(ValueError):patch.build(bad)


if __name__ == '__main__':
    unittest.main()
