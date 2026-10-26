"""V179 challenge producers and complete failure receivers, with actor/UI adapters."""
import hashlib
import os
from pathlib import Path
import struct
import unittest

from test_guild_social_channel_v179 import SocialSender
from unicorn.x86_const import *

class TownPVPNative(SocialSender):
 def __init__(self, data):
  super().__init__(data)
  self.peer=0x20018000;self.actor_data=0x20019000;self.dialog=0x20011000;self.dialogs=[];self.closed=0;self.click=None
  self.put(self.actor_data,self.NAME)
  self.put(self.ACTOR+0x20,1);self.put(self.peer+0x20,5);self.put(0x56a3768,0)
  self.hooks.update({0x2527d90:lambda:self.ret(value=0),0x299e2e0:lambda:self.ret(value=0),0x296d730:lambda:self.ret(value=self.peer),0x3c1bea0:lambda:self.ret(value=self.actor_data),0x29614d0:lambda:self.ret(value=self.actor_data),0x2974980:lambda:self.ret(value=0),0x29c18a0:lambda:self.ret(8,0),0x32b55e0:lambda:self.ret(4,0),0x2046d70:lambda:self.ret(value=0),0x32bea50:self.open_dialog,0x3361970:lambda:self.ret(),0x3826970:self.button,0x22000100:self.close})
  for va in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):self.hooks[va]=self.protected
  self.hooks.update({0x299f800:lambda:self.ret(value=0),0x32b94a0:lambda:self.ret(12),0x32bac90:lambda:self.ret(value=0),0xe91820:lambda:self.ret(value=0)})
 def protected(self):
  self.put(self.arg(1),1 if self.reg(UC_X86_REG_ECX)==0x57b7218 else 90);self.ret(8)
 def open_dialog(self):
  self.dialogs.append([self.arg(),self.arg(1),self.arg(2)]);self.ret(12,0)
 def button(self):self.ret(value=int(self.reg(UC_X86_REG_ECX)==self.click))
 def close(self):self.closed+=1;self.ret()
 def init_dialog(self,kind):
  self.put(self.dialog,0x20012000);self.put(0x20012000+0x7c,0x22000100)
  self.put(self.dialog+0x224,kind);self.put(0x568da3c,1)
  self.put(self.dialog+0x218,2);self.u.mem_write(self.dialog+0x214,b'\1')
  for at,v in ((0x1c0,0x20013000),(0x1c8,0x20013100),(0x50,0x20013200)):self.put(self.dialog+at,v)
 def receive(self,entry,body):
  self.u.mem_write(self.PACKET,body);self.put(0x57f9ad0,self.PACKET,len(body),0,0)
  self.run(entry,(7,0),pop=0)
  assert self.read(0x57f9ad4)==0

class TownPVPV179Tests(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  path=os.environ.get('DNF_GUILD_SOCIAL_V179_CLIENT')
  if not path:raise unittest.SkipTest('exact V179 client required')
  cls.data=Path(path).read_bytes()
  assert hashlib.sha256(cls.data).hexdigest()=='72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'

 def test_native_invite_accept_reject_cancel_timeout(self):
  for kind,mode in ((0,4),(1,13)):
   lab=TownPVPNative(self.data);lab.run(0x36b78e0,(5,kind),pop=0)
   self.assertEqual(lab.sent,[(10,struct.pack('<HBII',5,mode,0,0))])
   for action,entry in (('accept',0x23d2780),('reject',0x23d2820),('cancel',0x23d2980),('timeout',0x23d2980)):
    lab=TownPVPNative(self.data);lab.init_dialog(kind)
    if action=='cancel':lab.click=0x20013100
    if action=='timeout':lab.u.mem_write(lab.dialog+0x21c,b'\1')
    lab.run(entry,ecx=lab.dialog)
    self.assertEqual(lab.sent,[(11,struct.pack('<HBI',1,mode,int(action=='accept')))])
    if action in ('cancel','timeout'):
     lab.run(entry,ecx=lab.dialog);self.assertEqual(len(lab.sent),1)

 def test_real_go_failure_packets_consume_exactly_and_close_right_dialog(self):
  folder=os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
  if not folder:self.skipTest('Go export directory required')
  for mode in (4,13):
   for op,code,entry,suffix in ((10,3,0x1fc2be0,'request-failure'),(10,133,0x1fc2be0,'op10-terminal'),(11,133,0x1fc3760,'op11-terminal')):
    lab=TownPVPNative(self.data);closes=[]
    lab.hooks.update({0x35ca060:lambda:lab.ret(),0x3810650:lambda:lab.ret(value=lab.CHAT),0x2a02ac0:lambda:lab.ret(4),0x2a00580:lambda:lab.ret(32),0x8ef130:lambda:lab.ret(value=0),0x32b94a0:lambda:(closes.append(lab.arg()),lab.ret(12)),0x32b9570:lambda:lab.ret(4)})
    lab.put(0x56a3798,lab.CHAT)
    body=Path(folder,f'town-pvp-mode{mode}-{suffix}.bin').read_bytes()
    self.assertEqual(body[:3],bytes((0,code,mode)))
    lab.u.mem_write(lab.PACKET,body[2:]);lab.put(0x57f9ad0,lab.PACKET,len(body)-2,0,0)
    lab.run(entry,(op,0,code,0),pop=0)
    self.assertEqual(lab.read(0x57f9ad4),0)
    if code==3:self.assertIn(6553,lab.messages)
    else:self.assertIn(0x156 if op==10 else 0x155,closes)

if __name__=='__main__':unittest.main()
