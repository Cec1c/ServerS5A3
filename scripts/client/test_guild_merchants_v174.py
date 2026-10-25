"""Bounded merchant sender, real ACK dispatcher, notification and PE checks.

Resource lookup, UI effects and transport are adapters. Packet construction,
receive gates, response dispatch and field reads execute original machine code.
"""
from pathlib import Path
import os, struct, tempfile, unittest
from unicorn import UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import native, windows_image
import patch_guild_merchants_v174 as patch

class Lab(WarNative):
    OBJ, DEF, VIEW, VT, STR, CFG, ROW = (0x20010000,0x20012000,0x20014000,0x20016000,0x20017000,0x20018000,0x20019000)
    def __init__(self,data):
        super().__init__(data)
        self.u.mem_map(self.NETWORK+0x100000,0x200000)
        self.put(self.NETWORK+8,1)
        self.put(self.OBJ,self.VT);self.put(self.VIEW,self.VT)
        self.put(self.VT+0x7c,self.STOP+0x100)
        self.put(self.VT+0x344,self.STOP+0x104)
        self.current=1;self.maximum=10;self.allowed=True;self.mode=4;self.visible=False
        self.opened=[];self.sent=[];self.closed=0;self.sounds=[];self.writes=[]
        self.put(self.STR+0x14,7)
        self.u.mem_write(self.STR+0x40,'ok\0'.encode('utf-16le'))
        self.hooks.update({
            self.STOP+0x100:self.close,
            self.STOP+0x104:lambda:self.ret(value=self.DEF),
            0x292AA40:lambda:self.ret(value=self.DEF),
            0x265F630:lambda:self.ret(value=self.current),
            0x1B4E7A0:self.max_iterator,
            0x3A90240:lambda:self.ret(value=self.ROW),
            0x2978B20:lambda:self.ret(8,int(self.allowed)),
            0x32BEA50:self.open_ui,
            0x29BA8C0:lambda:self.ret(8,1),
            0x1195600:lambda:self.ret(value=1),
            0x29BACA0:self.control,
            0x37DE240:lambda:self.ret(value=self.STR+0x40),
            0x391BA20:lambda:self.ret(value=self.STR),
            0x889480:self.compare,
            0x374F280:self.start,
            0x38C7F70:self.sink,
            0x3750AC0:lambda:self.ret(),
            0x32B5C90:lambda:self.ret(8,self.VIEW if self.visible else 0),
            0x2AD70A0:lambda:self.ret(value=self.mode),
            0x2531940:self.sound,
            0x32C2EF0:lambda:self.ret(),
            0x37E0840:self.small_format,
            0x3754450:lambda:self.ret(8,self.DEF),
        })
    def close(self):self.closed+=1;self.ret()
    def max_iterator(self):self.put(self.ROW,self.maximum);self.ret(4,self.arg())
    def open_ui(self):
        ident=self.arg()
        if ident==519:self.opened.append(list(struct.unpack('<III',self.u.mem_read(self.arg(1),12))))
        self.ret(12)
    def control(self):out=self.arg();self.put(out,self.VIEW,0);self.ret(8,out)
    def compare(self):
        caller=self.read(self.reg(UC_X86_REG_ESP));self.ret(16,0 if caller==0x1193D4E else 1)
    def start(self):self.op=self.arg();self.put(self.NETWORK+0x2bcc2c,0);self.ret(4)
    def sink(self):self.sent.append({'opcode':self.op,'body':bytes(self.u.mem_read(self.arg(),self.arg(1))).hex()});self.ret(8,0)
    def sound(self):self.sounds.append(self.arg());self.ret()
    def small_format(self):out=self.arg();self.put(out,self.STR+0x40,0);self.ret(value=out)
    def initialize(self,values):
        self.put(self.PACKET,*values);self.run(0x1195FC0,(self.PACKET,0),ecx=self.OBJ)
        assert list(struct.unpack('<III',self.u.mem_read(self.OBJ+0x1dc,12)))==list(values)
    def sender(self,poison,fixed=False):
        self.u.mem_write(self.STACK-0x400,bytes([poison])*0x400)
        prefix=self.STACK-4-0x2c
        def writes(u,access,address,size,value,ctx):
            if address<prefix+13 and address+size>prefix:self.writes.append([hex(address),size,hex(value)])
        h=self.u.hook_add(UC_HOOK_MEM_WRITE,writes)
        self.run(0x1193CB0,(1,13,0,0),ecx=self.OBJ+0x1b0)
        self.u.hook_del(h)
        body=bytes.fromhex(self.sent[-1]['body'])
        assert len(body)==25 and body[:13]==(bytes(13) if fixed else bytes([poison])*13)
        assert self.read(self.NETWORK+0x2bcc2c)==25 and bool(self.writes)==fixed
        return self.sent[-1]
    def notification(self,index,level):
        body=struct.pack('<BI',index,level)
        self.u.mem_write(self.PACKET,body);self.put(0x57F9AD0,self.PACKET,len(body),0,0)
        self.run(0x10687A0)
        assert self.read(0x57F9AD0)==self.PACKET+5 and self.read(0x57F9AD4)==0
        return list(struct.unpack('<HH',self.u.mem_read(self.DATA+0x367b,4)))
    def ack840(self,body):
        self.put(0x2000C000,0x1066D70,0)
        wire=bytearray(16)+body;wire[0]=1
        struct.pack_into('<H',wire,1,840);struct.pack_into('<I',wire,3,len(wire))
        self.u.mem_write(self.PACKET,bytes(wire));self.messages.clear()
        self.run(0x25239A0,(0,self.PACKET,0),ecx=self.NETWORK)
        return list(self.messages)
    def decrypt(self,at):
        raw=bytes(self.u.mem_read(at,4));seed=raw[1]&254
        length=(((seed<<7)|seed)^struct.unpack_from('<H',raw,2)[0])*2
        self.run(0x37DD990,(at+4,length,self.PACKET,seed|0x9a714ca0),pop=0)
        return bytes(self.u.mem_read(self.PACKET,length)).decode('utf-16le').rstrip('\0')

class MerchantTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('GUILD_MERCHANT_V174_EXE')
        if not path: raise unittest.SkipTest('set GUILD_MERCHANT_V174_EXE')
        cls.before=Path(path).read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def test_sender_initialized_prefix_and_preserved_fields(self):
        for npc in (389,390):
            for current in (1,4,9):
                for fixed,data in ((False,self.before),(True,self.after)):
                    lab=Lab(data)
                    lab.initialize((npc,current,current+1))
                    for poison in (0xa5,0x5a):
                        lab.writes.clear()
                        body=bytes.fromhex(lab.sender(poison,fixed)['body'])
                        self.assertEqual(body[13:],struct.pack('<III',npc,current,current+1))

    def test_real_ack_dispatch(self):
        for data,fixed in ((self.before,False),(self.after,True)):
            for body in (b'\1',b'\0\x03',b'\0\x16'):
                lab=Lab(data); consumed=[]
                lab.hooks[0x1066D70]=lambda: (consumed.append(True),lab.hooks.pop(0x1066D70))
                lab.ack840(body)
                self.assertEqual(bool(consumed),fixed)
                if fixed:self.assertEqual(lab.read(0x57F9AD4),0)

    def test_original_notification_and_window_refresh(self):
        for index in (0,1):
            for level in (1,2,10):
                lab=Lab(self.after);lab.put(lab.DATA+0x367b,1|(1<<16))
                self.assertEqual(lab.notification(index,level)[index],level)
        for visible,mode in ((False,4),(True,4),(True,3)):
            lab=Lab(self.after);lab.visible=visible;lab.mode=mode
            lab.notification(0,2)
            self.assertEqual(lab.closed,int(visible and mode==4))

    def test_complete_gate_matrix(self):
        for cls in (0,1,2,255):
            before=native(self.before,(cls,),range(65536));after=native(self.after,(cls,),range(65536))
            changes=[i for i,(a,b) in enumerate(zip(before,after)) if a!=b]
            self.assertEqual(changes,[778] if cls==0 else [840] if cls==1 else [])
            for i in changes:self.assertEqual((before[i],after[i]),(0,1))

    def test_go_production_responses(self):
        root=os.environ.get('GUILD_MERCHANT_PACKET_DIR')
        if not root:self.skipTest('set GUILD_MERCHANT_PACKET_DIR')
        files=list(Path(root).glob('merchant-*.bin'))
        self.assertGreaterEqual(len(files),18)
        for path in files:
            body=path.read_bytes();lab=Lab(self.after)
            if path.name.endswith('-840.bin'):
                lab.ack840(body);self.assertEqual(lab.read(0x57F9AD4),0)
            elif path.name.endswith('-778.bin'):
                index,level=struct.unpack('<BI',body)
                self.assertEqual(lab.notification(index,level)[index],level)
            elif path.name.endswith('-1354.bin'):
                lab.put(0x2000C000,0x1073D70,0)
                lab.hooks[0x32B94A0]=lambda:lab.ret(12)
                lab.run(0x1066110,(0,))
                wire=bytearray(16)+body;wire[0]=1
                struct.pack_into('<H',wire,1,1354);struct.pack_into('<I',wire,3,len(wire))
                lab.u.mem_write(lab.PACKET,bytes(wire))
                lab.run(0x25239A0,(0,lab.PACKET,0),ecx=lab.NETWORK)
                self.assertEqual(lab.u.mem_read(0x5660140,1),b'\x01')
                self.assertEqual(lab.read(0x57F9AD4),0)
            else:self.fail('unexpected production body '+path.name)

    def test_exact_diff_and_windows_image(self):
        allowed=self.receipt['changedRanges']
        for i,(a,b) in enumerate(zip(self.before,self.after)):
            if a!=b:self.assertTrue(any(lo<=i<hi for lo,hi in allowed),hex(i))
        self.assertEqual(len(self.before),len(self.after))
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'candidate.exe';path.write_bytes(self.after)
            if os.name=='nt':windows_image(path)
        with self.assertRaises(ValueError):patch.build(self.after)

if __name__=='__main__':unittest.main()
