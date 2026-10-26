"""Execute audited native exit/clear/disband callbacks with bounded UI stubs."""
import os
from pathlib import Path
import struct
import unittest

import patch_guild_grade_region as image
from test_patch_guild_grade_region import Sender
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDX, UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP,
    UC_X86_REG_EIP, UC_X86_REG_ESP)


class GuildLifecycleNativeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('DNF_GUILD_GRADE_INPUT_EXE')
        if not path: raise unittest.SkipTest('requires exact V166 client')
        data=Path(path).read_bytes()
        if image.sha(data)!=image.INPUT_SHA256: raise ValueError('client SHA mismatch')
        cls.original=data
        cls.lab=Sender(data)

    def execute(self,address,args=(),body=b'',hall=False):
        u=self.lab.u
        calls=[]; cursor=0
        read32=lambda at:struct.unpack('<I',u.mem_read(at,4))[0]
        def ret(value=0,pop=0):
            sp=u.reg_read(UC_X86_REG_ESP); target=read32(sp)
            u.reg_write(UC_X86_REG_EAX,value)
            u.reg_write(UC_X86_REG_ESP,sp+4+pop)
            u.reg_write(UC_X86_REG_EIP,target)
        def hook(_u,at,_size,_context):
            nonlocal cursor
            sp=u.reg_read(UC_X86_REG_ESP)
            if at==0x37E82E0:
                count=read32(sp+8); self.assertEqual(count,1)
                self.assertLess(cursor,len(body))
                u.mem_write(read32(sp+4),body[cursor:cursor+count]);cursor+=count;ret()
            elif at==0x3752120:
                size=struct.unpack_from('<I',body,cursor)[0];cursor+=4
                self.assertLessEqual(cursor+size,len(body))
                name=body[cursor:cursor+size].decode('gb18030');cursor+=size
                u.mem_write(read32(sp+4),name.encode('utf-16le')+b'\0\0')
                calls.append(('read-name',name));ret()
            elif at==0x39A6620:
                calls.append(('text',read32(sp+4)));ret(0x11006000)
            elif at==0x8EF130:
                # The two DSTRs have already been decoded by the exact callback.
                calls.append(('format',));ret()
            elif at==0x32BEA50:
                calls.append(('dialog',read32(sp+4)));ret(0,12)
            elif at in (0x1FA2790,0x1302400): ret(0x11004000)
            elif at==0x2526B50: ret(1 if hall else 0)
            elif at==0x36FEBB0: ret(1)
            elif at==0x2AE75E0: ret(38)
            elif at==0x36FEEE0: ret(0)
            elif at==0x3705A90:
                calls.append(('return-town',read32(sp+4),read32(sp+8)));ret(0,8)
            elif at==0x11007000:
                calls.append(('model-reset',));ret()
            elif at==0x32B94A0:
                calls.append(('close-window',read32(sp+4)));ret(0,12)
            elif at==0x296DD80: ret(0x11005000)
            elif at==0x3C1BEA0: ret(0x11005000)
            elif at in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):
                calls.append(('guild-id',read32(read32(sp+4))));ret(0,8)
            elif at==0x3810650: ret(0x11004000)
            elif at in (0x29F73F0,0x2A02AC0): ret(0,4)
            elif at==0x3E72CE4: ret()
            elif at==0x35CA060: ret(0)
        u.mem_write(0x56A3788,struct.pack('<I',0x11002000))
        u.mem_write(0x11002000,struct.pack('<I',0x11003000))
        u.mem_write(0x11003010,struct.pack('<I',0x11007000))
        sp=0x10008000
        u.mem_write(sp,struct.pack('<'+'I'*(len(args)+1),0x20000000,*args))
        preserved={UC_X86_REG_EBX:0x12345678,UC_X86_REG_ESI:0x23456789,
                   UC_X86_REG_EDI:0x34567890,UC_X86_REG_EBP:0x10009000}
        for reg,value in preserved.items(): u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_ECX,0x11001000);u.reg_write(UC_X86_REG_ESP,sp)
        handle=u.hook_add(UC_HOOK_CODE,hook)
        try:
            u.emu_start(address,0x20000000,count=2000)
            self.assertEqual(u.reg_read(UC_X86_REG_EIP),0x20000000)
            self.assertEqual(u.reg_read(UC_X86_REG_ESP),sp+4)
            for reg,value in preserved.items(): self.assertEqual(u.reg_read(reg),value)
        finally: u.hook_del(handle)
        self.assertEqual(cursor,len(body))
        return calls

    def test_exit_and_kick_ack_read_order_and_return(self):
        def dstr(s):
            b=s.encode('gb18030');return struct.pack('<I',len(b))+b
        for action in (1,2):
            body=bytes([action])+dstr('公会甲')+b'\0'+dstr('会员乙')
            calls=self.execute(0x1FC73D0,(0,1,0),body,hall=True)
            self.assertEqual([x for x in calls if x[0]=='read-name'],[('read-name','公会甲'),('read-name','会员乙')])
            self.assertIn(('text',6700 if action==1 else 6701),calls)
            self.assertEqual(('return-town',38,0) in calls,action==1)

    def test_clear_notification_resets_model_guild_identity_and_hall(self):
        for hall in (False,True):
            calls=self.execute(0x2038880,hall=hall)
            for event in (('model-reset',),('close-window',55),('guild-id',0),('text',6722)):
                self.assertIn(event,calls)
            self.assertEqual(('return-town',38,0) in calls,hall)

    def test_disband_success_consumes_no_body(self):
        self.assertEqual(self.execute(0x1FBEC90,(0,1,0)),[])

    def test_disband_candidate_renders_real_denial_reasons(self):
        import patch_guild_disband_v166 as disband
        candidate,_=disband.build(image.build(self.original)[0])
        previous=self.lab
        self.lab=Sender(candidate)
        try:
            self.assertEqual(self.execute(disband.CALLBACK_VA,(0,1,0)),[])
            for code,text in ((3,6596),(34,6576),(36,36312),(87,46335)):
                calls=self.execute(disband.CALLBACK_VA,(0,0,code))
                self.assertIn(('text',text),calls)
                self.assertIn(('dialog',726),calls)
        finally: self.lab=previous


if __name__=='__main__': unittest.main()
