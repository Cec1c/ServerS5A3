"""Native op424 production, exact receive gate and complete NOTI371 consumption.

Scene/strings, transport and final UI/storage operations are adapters. Native
selection, scalar/blob writers, dispatcher, receiver and 148-byte row reader
execute unchanged. Item storage is observed before final chat rendering.
"""
import os,re,struct,tempfile,unittest
from pathlib import Path
import patch_guild_item_link as patch
from test_guild_social_channel_v179 import native
from test_patch_fatigue_count_v164 import windows_image
from test_main_whisper_v179 import MainWhisperNative
from unicorn.x86_const import *
class GuildItemNative(MainWhisperNative):
 def __init__(self,data):
  super().__init__(data);self.items=[];self.heap=0x24000000;self.u.mem_map(self.heap,0x10000)
  self.hooks.update({0x25244A0:lambda:self.ret(4,self.NETWORK),0x2321490:lambda:self.ret(),0x21E7C50:self.item,0x21E7B70:lambda:self.ret()})
 def string(self,at,value):
  self.u.mem_write(at,bytes(32))
  if len(value)<=14:
   self.u.mem_write(at+4,value+b'\0\0');self.put(at+20,len(value)//2,7)
  else:
   target=self.heap;self.heap+=(len(value)+17)&~15
   self.u.mem_write(target,value+b'\0\0');self.put(at+4,target);self.put(at+20,len(value)//2,len(value)//2)
 def wstring_copy(self):
  at,source=self.reg(UC_X86_REG_ECX),self.arg()
  assert self.arg(1)==0 and self.arg(2)==0xFFFFFFFF
  self.string(at,self.wide(self.read(source+4) if self.read(source+24)>7 else source+4));self.ret(12,at)
 def item(self):
  self.items.append(bytes(self.u.mem_read(self.arg(),148)));self.ret()
 def receive(self,body):
  self.u.mem_write(self.PACKET,body);self.put(0x57F9AD0,self.PACKET,len(body),0,0)
  original=self.hooks[0x29FFDC0]
  def enqueue():
   record=self.arg();self.link_flags.append((self.read(record+0x3C)&255,self.read(record+0x1C)))
   original()
  self.link_flags=[];self.hooks[0x29FFDC0]=enqueue
  wire=bytearray(16)+body;wire[0]=0;struct.pack_into('<H',wire,1,371);struct.pack_into('<I',wire,3,len(wire))
  self.u.mem_write(self.PACKET,bytes(wire));self.put(0x2000C000,0x2A04520,0)
  self.run(0x25239A0,(0,self.PACKET,0),ecx=self.NETWORK)

  return self.shown
 def send_items(self,rows,message="hello",message_type=6):
  frame=self.STACK+0x2000;self.put(self.ACTOR+0x3AC,0)
  for offset,value in ((0x35C,self.ACTOR),(0x348,message_type),(0x364,-10),(0x360,0xFFFF),(0x34C,self.NAME),(0x340,self.CHAT)):self.put(frame-offset,value)
  self.u.mem_write(frame-0x210,(message+'\0').encode('utf-16le'))
  array=0x2001A000
  self.u.mem_write(array,b''.join(rows));self.put(self.INPUT+0x4C0,0,array,array+148*len(rows),array+148*len(rows))
  self.u.reg_write(UC_X86_REG_EBP,frame);self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_EDI,self.CHAT);self.u.reg_write(UC_X86_REG_EBX,0)
  self.u.emu_start(0x2A03782,0x2A039EE,count=10000)
  assert self.reg(UC_X86_REG_EIP)==0x2A039EE
  return self.sent


def dstr(value):return struct.pack('<I',len(value))+value

def captured_item():
 text=(Path(__file__).resolve().parents[2]/'go-server/internal/services/dnfbridge/online_item_hyperlink_test.go').read_text('utf-8')
 raw=bytes.fromhex(re.search('currentHyperlinkCaptureOneBody = "(.*?)"',text)[1])
 return raw[12+struct.unpack_from('<I',raw,7)[0]:]

class GuildItemTests(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  value=os.environ.get('DNF_GUILD_ITEM_CLIENT')
  if not value:raise unittest.SkipTest('exact cumulative social input required')
  cls.source=Path(value);cls.before=cls.source.read_bytes();cls.after,cls.receipt=patch.build(cls.before)
 def test_native_producer(self):
  row=captured_item()
  for count in (1,2,5):
   lab=GuildItemNative(self.after);sent=lab.send_items([row]*count)
   expected=bytes.fromhex('06ffff00000000')+dstr(b'hello')+bytes([count])+row*count
   self.assertEqual(sent,[(424,expected)])
   folder=os.environ.get('DNF_GUILD_ITEM_NATIVE_DIR')
   if folder:
    Path(folder).mkdir(parents=True,exist_ok=True);Path(folder,f'request-{count}.bin').write_bytes(sent[0][1])
  # The same native producer still emits the ordinary area hyperlink route.
  self.assertEqual(GuildItemNative(self.after).send_items([row],message_type=3)[0][1][0],3)
 def test_all_receive_ids(self):
  for kind in (0,1,2,3,127,255):
   a=native(self.before,(kind,),range(65536));b=native(self.after,(kind,),range(65536))
   self.assertEqual([i for i in range(65536) if a[i]!=b[i]],[371] if kind==0 else [])
 def test_native_receiver_and_old_gate(self):
  row=captured_item()
  for count in (1,2,5):
   for message in ('hello','物品链接','a'*255):
    body=bytes([6,0,0,0])+dstr('来来来'.encode('gb18030'))+bytes([0])+dstr(message.encode('gb18030'))+bytes([count])+row*count+dstr(b'')
    lab=GuildItemNative(self.after);self.assertEqual(lab.receive_main_ui(body,'other'),[dict(message=message,type=6,name='来来来')])
    self.assertEqual(lab.items,[row]*count);self.assertEqual(lab.link_flags,[(1,0)]);self.assertEqual(lab.read(0x57F9AD4),0)
    # Guild echo must also reach the sender's main input UI.
    own=GuildItemNative(self.after);self.assertEqual(len(own.receive_main_ui(body,'来来来')),1)
    old=GuildItemNative(self.before);self.assertEqual(old.receive_main_ui(body,'other'),[]);self.assertEqual(old.items,[])
 def test_go_production_notices(self):
  folder=os.environ.get('DNF_GUILD_ITEM_NATIVE_DIR')
  if not folder:self.skipTest('Go production fixture directory required')
  row=captured_item()
  for count in (1,2,5):
   body=Path(folder,f'notice-{count}.bin').read_bytes();lab=GuildItemNative(self.after)
   self.assertEqual(lab.receive_main_ui(body,'other'),[dict(message='hello',type=6,name='角色29')]);self.assertEqual(lab.items,[row]*count);self.assertEqual(lab.link_flags[0][0],1);self.assertEqual(lab.read(0x57F9AD4),0)
 def test_exact_ranges_and_image(self):
  cursor=0
  for lo,hi in self.receipt['changedRanges']:
   self.assertLessEqual(cursor,lo);self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
  self.assertEqual(self.before[cursor:],self.after[cursor:]);self.assertEqual(len(self.before),len(self.after))
  for invalid in (self.after,self.before[:-1],b'X'+self.before[1:]):
   with self.assertRaises(ValueError):patch.build(invalid)
  with tempfile.TemporaryDirectory() as folder:
   out=Path(folder,'candidate.exe');patch.write_candidate(self.source,out);self.assertEqual(out.read_bytes(),self.after);windows_image(out)
   with self.assertRaises(ValueError):patch.write_candidate(self.source,out)
  with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)

if __name__=='__main__':unittest.main()
