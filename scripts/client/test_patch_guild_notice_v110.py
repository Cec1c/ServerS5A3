import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_guild_notice_v110 as patch
from patch_guild_member_menu_v110 import headers,offset_for_rva,checksum


@unittest.skipUnless(os.environ.get("DNF_GUILD_MENU_FIXED_EXE"), "requires exact member-menu-fixed V110")
class NoticePatchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path=Path(os.environ["DNF_GUILD_MENU_FIXED_EXE"])
        cls.before=cls.path.read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def test_preserves_previous_fix_bitmap_and_one_time_toast(self):
        ranges=sorted(self.receipt['allowed_changed_ranges'])
        for lo,hi in zip([0]+[hi for lo,hi in ranges],[lo for lo,hi in ranges]+[len(self.before)]):
            self.assertEqual(self.before[lo:hi],self.after[lo:hi],(lo,hi))
        _,opt,_,sections=headers(self.after)
        self.assertEqual(sections[-1]['flags'],0x60000020)
        self.assertEqual(sections[-1]['name'].rstrip(b'\0'),b'.gnotice')
        self.assertEqual(checksum(self.after,opt+64),struct.unpack_from('<I',self.after,opt+64)[0])

    def emulate(self,image,text,model=True):
        from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
        from unicorn.x86_const import UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,UC_X86_REG_EFLAGS,UC_X86_REG_EIP
        uc=Uc(UC_ARCH_X86,UC_MODE_32);sections=headers(image)[3];pages=set()
        def map_page(address):
            page=address&~0xfff
            if page not in pages:uc.mem_map(page,0x1000);pages.add(page)
        def code(rva,size):
            address=0x400000+rva;map_page(address)
            pos=offset_for_rva(sections,rva,size);uc.mem_write(address,image[pos:pos+size])
        code(0x1c3ecc2,12) # original already-shown branch
        code(patch.HOOK_RVA,6)
        if image is self.after:code(int(self.receipt['thunk_rva'],16),len(bytes.fromhex(self.receipt['thunk_hex'])))
        for address in [0x5682e98,0x56a3788,0x100000,0x101000,0x300000,0x400000,0x37e3000]:map_page(address)
        uc.mem_write(0x5682e98,b'\x01')
        uc.mem_write(0x56a3788,struct.pack('<I',0x300000 if model else 0))
        uc.mem_write(0x300018,struct.pack('<I',0x400000))
        uc.mem_write(0x400000,'旧公告\0'.encode('utf-16-le'))
        frame=0x101000
        uc.mem_write(frame-0x2e0,(text+'\0').encode('utf-16-le'))
        regs=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,UC_X86_REG_EFLAGS]
        initial=[0x123456,0,0xabcdef,0x567890,0x45678,0x19283,frame,0x100080,0x246]
        for reg,value in zip(regs,initial):uc.reg_write(reg,value)
        calls=[]
        def setter(uc,address,size,user):
            if address!=0x37e3000:return
            sp=uc.reg_read(UC_X86_REG_ESP)
            ret,source=struct.unpack('<II',uc.mem_read(sp,8))
            self.assertEqual(uc.reg_read(UC_X86_REG_ECX),0x300018)
            raw=bytearray()
            for off in range(0,512,2):
                pair=bytes(uc.mem_read(source+off,2));raw.extend(pair)
                if pair==b'\0\0':break
            calls.append(bytes(raw));uc.mem_write(0x400000,bytes(raw))
            # The native string allocator is deliberately a stub: verify the
            # real hook/branch ABI separately from allocation and UI behavior.
            uc.reg_write(UC_X86_REG_EAX,0x300018);uc.reg_write(UC_X86_REG_EDX,0x876543)
            uc.reg_write(UC_X86_REG_EFLAGS,0x202);uc.reg_write(UC_X86_REG_ESP,sp+8);uc.reg_write(UC_X86_REG_EIP,ret)
        uc.hook_add(UC_HOOK_CODE,setter)
        uc.emu_start(0x203ecc2,0x400000+patch.RETURN_RVA,count=50)
        self.assertEqual(bytes(uc.mem_read(0x5682e98,1)),b'\x01')
        return [uc.reg_read(reg) for reg in regs],calls

    def test_already_shown_notice_updates_without_relogin(self):
        for text in ['新公告','', '  每晚组队\n欢迎冒险  ', '会'*127+'a']:
            with self.subTest(text=text[:8]):
                before,calls=self.emulate(self.before,text)
                self.assertEqual(calls,[]) # original branch skips cache setter
                after,calls=self.emulate(self.after,text)
                self.assertEqual(calls,[(text+'\0').encode('utf-16-le')])
                self.assertEqual(before,after)

    def test_missing_model_does_not_introduce_null_write(self):
        before,_=self.emulate(self.before,'新公告',False)
        after,calls=self.emulate(self.after,'新公告',False)
        self.assertEqual(before,after);self.assertEqual(calls,[])

    def test_unknown_input_and_existing_output_rejected(self):
        with self.assertRaises(ValueError):patch.build(b'unknown')
        with self.assertRaises(ValueError):patch.write_candidate(self.path,self.path)
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'existing';p.write_bytes(b'keep')
            with self.assertRaises(ValueError):patch.write_candidate(self.path,p)
            self.assertEqual(p.read_bytes(),b'keep')


if __name__=='__main__':unittest.main()
