"""Run native teleport writers/readers with bounded network and UI substitutes."""
import json
import os
from pathlib import Path
import struct
import unittest
import patch_guild_grade_region as image
from test_patch_guild_grade_region import Sender
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import (UC_X86_REG_EAX,UC_X86_REG_EBP,UC_X86_REG_EBX,
    UC_X86_REG_ECX,UC_X86_REG_EDI,UC_X86_REG_EIP,UC_X86_REG_ESI,UC_X86_REG_ESP)

class GuildTeleportNativeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('DNF_GUILD_GRADE_INPUT_EXE')
        if not path:raise unittest.SkipTest('requires formal V166 client')
        data=Path(path).read_bytes()
        if image.sha(data)!=image.INPUT_SHA256:raise ValueError('V166 SHA mismatch')
        cls.lab=Sender(data)

    def run_native(self,address,args=(),body=b'',sender=False,pop=0):
        u=self.lab.u;calls=[];cursor=0;sent=[]
        read32=lambda at:struct.unpack('<I',u.mem_read(at,4))[0]
        def ret(value=0,pop=0):
            sp=u.reg_read(UC_X86_REG_ESP);target=read32(sp)
            u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
        def hook(_u,at,_size,_context):
            nonlocal cursor
            sp=u.reg_read(UC_X86_REG_ESP)
            if at in (0x37E82E0,0x37E8350,0x37E83C0):
                size={0x37E82E0:1,0x37E8350:2,0x37E83C0:4}[at]
                self.assertLessEqual(cursor+size,len(body));u.mem_write(read32(sp+4),body[cursor:cursor+size]);cursor+=size;ret()
            elif at==0x374EA10:ret(0x11003000)
            elif at in (0x374F280,0x374F390,0x374F3C0):
                size=1 if at==0x374F390 else 2;sent.append(read32(sp+4).to_bytes(4,'little')[:size]);ret(0x11003000,4)
            elif at==0x3750AC0:ret()
            elif at in (0x296E590,0x3E734F0,0x8EF130):ret()
            elif at==0x3754450:ret(1,8)
            elif at==0x296DD80:ret(0x11004000)
            elif at==0x39A6620:calls.append(('text',read32(sp+4)));ret(0x11006000)
            elif at==0x32B55E0:ret(0,4)
            elif at==0x32B5C90:ret(0,8)
            elif at==0x32BEA50:
                calls.append(('window',read32(sp+4),read32(sp+12)));ret(0x11005000,12)
            elif at==0x248BAB0:ret(0,4)
            elif at==0x248C020:calls.append(('phase',read32(sp+4),read32(sp+8)));ret(0,8)
            elif at==0x248C760:calls.append(('slot',read32(sp+4),read32(sp+8)));ret(0,8)
            elif at in (0x1FA2790,0x296D730,0x29610E0):ret()
            elif at==0x32B4B20:ret(0)
        u.mem_write(0x56A3768,struct.pack('<I',0x11001000))
        u.mem_write(0x56A3758,b'\0'*4)
        u.mem_write(0x56A3798,struct.pack('<I',0x11002000))
        u.mem_write(0x11001000,b'\0'*512)
        for slot in range(8):u.mem_write(0x11001048+slot*4,struct.pack('<I',0x11004000))
        u.mem_write(0x11001014,b'\x01')
        sp=0x10008000;u.mem_write(sp,struct.pack('<'+'I'*(len(args)+1),0x20000000,*args))
        saved={UC_X86_REG_EBX:0x11112222,UC_X86_REG_ESI:0x33334444,UC_X86_REG_EDI:0x55556666,UC_X86_REG_EBP:0x10009000}
        for reg,value in saved.items():u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_ECX,0x11001000)
        handle=u.hook_add(UC_HOOK_CODE,hook)
        try:
            u.emu_start(address,0x20000000,count=3000)
            self.assertEqual(u.reg_read(UC_X86_REG_EIP),0x20000000)
            self.assertEqual(u.reg_read(UC_X86_REG_ESP),sp+4+pop)
            for reg,value in saved.items():self.assertEqual(u.reg_read(reg),value)
        finally:u.hook_del(handle)
        self.assertEqual(cursor,len(body))
        return calls,b''.join(sent)

    def test_native_request_and_consent_writers(self):
        _,wire=self.run_native(0x1084130,(39,0,500,300,1),sender=True,pop=20)
        self.assertEqual(wire,struct.pack('<HBBHHBB',434,39,0,500,300,5,1))
        for answer in (0,1,3):
            _,wire=self.run_native(0x248BA60,(answer,),sender=True,pop=4)
            self.assertEqual(wire,struct.pack('<HB',435,answer))

    def test_native_377_slot_mapping_and_terminal_states(self):
        for phase in range(4):
            body=bytearray([39,phase,0])+struct.pack('<H',2)
            for slot in range(8):body+=struct.pack('<HB',101+slot if slot in (0,2,3) else 65535,1 if slot==2 else 0)
            body+=b'\0'
            calls,_=self.run_native(0x2039760,body=bytes(body))
            self.assertIn(('window',541,2),calls);self.assertIn(('phase',phase,0),calls)
            self.assertEqual([x for x in calls if x[0]=='slot'],[('slot',0,0),('slot',2,1),('slot',3,0)])

    def test_native_434_failure_consumes_full_trailer(self):
        for code in (7,19,96):
            calls,_=self.run_native(0x1FBE870,(0,0,code),struct.pack('<BIB',39,0,0))
            self.assertIn(('window',726,0),calls)
        self.assertEqual(self.run_native(0x1FBE870,(0,1,0)),([],b''))

    def test_production_go_notifications(self):
        path=os.environ.get('DNF_GUILD_TELEPORT_WIRES')
        if not path:self.skipTest('requires Go generated wire evidence')
        rows=json.loads(Path(path).read_text())
        self.assertEqual(set(rows),{'request','complete','cancel','timeout','failure'})
        for key,phase in [('request',0),('complete',1),('cancel',2),('timeout',3)]:
            body=bytes.fromhex(rows[key]);calls,_=self.run_native(0x2039760,body=body)
            self.assertIn(('phase',phase,0),calls)
        fail=bytes.fromhex(rows['failure']);self.run_native(0x1FBE870,(0,fail[0],fail[1]),fail[2:])

if __name__=='__main__':unittest.main()
