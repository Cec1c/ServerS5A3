"""Emulate the actual consignment fee/submit paths; external UI/network leaves are adapters."""
import argparse
import json
from pathlib import Path
import re
import struct
import sys
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import patch_client as patch

STOP, STACK, OBJECT, COMBO, TEXT, VTABLE, STR, GETTEXT = (0x11000000, 0x11010000, 0x11020000,
                                                       0x11021000, 0x11022000, 0x11023000,
                                                       0x11024000, 0x11025000)


class Machine:
    def __init__(self, data):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32)
        image=pefile.PE(data=data);base=image.OPTIONAL_HEADER.ImageBase
        self.u.mem_map(base,(image.OPTIONAL_HEADER.SizeOfImage+0xfff)&~0xfff)
        self.u.mem_write(base,data[:image.OPTIONAL_HEADER.SizeOfHeaders])
        for s in image.sections:self.u.mem_write(base+s.VirtualAddress,s.get_data())
        self.u.mem_map(STOP,0x30000)
        self.u.hook_add(UC_HOOK_CODE,self.step)

    def w(self,p,v):self.u.mem_write(p,struct.pack('<I',v&0xffffffff))
    def r(self,p):return struct.unpack('<I',self.u.mem_read(p,4))[0]
    def ret(self,value=0,args=0):
        sp=self.u.reg_read(UC_X86_REG_ESP)
        self.u.reg_write(UC_X86_REG_EAX,value&0xffffffff)
        self.u.reg_write(UC_X86_REG_EIP,self.r(sp))
        self.u.reg_write(UC_X86_REG_ESP,sp+4+args)

    def step(self,u,va,n,_):
        sp=u.reg_read(UC_X86_REG_ESP)
        if va==STOP:u.emu_stop()
        elif va==0x3858FA0:
            assert u.reg_read(UC_X86_REG_ECX)==COMBO
            self.ret(self.item)
        elif va==GETTEXT:
            assert u.reg_read(UC_X86_REG_ECX)==TEXT
            self.ret(STR)
        elif va==0x3E76CF7:
            assert self.r(sp+4)==STR
            self.ret(self.price)
        elif va==0x1FA2790:self.ret(0x11026000)
        elif va==0x2526D80:self.ret(int(self.coupon),4)
        elif va==0x374EA10:self.ret(0x11027000)
        elif va in (0x374F280,0x374F390,0x374F3C0,0x374F3F0):
            self.fields.append((va,self.r(sp+4)));self.ret(0,4)
        elif va==0x3750AC0:self.flush+=1;self.ret()
        elif va==0x39A6620:self.ret(STR)  # native invalid-text message lookup
        elif va==0x32BEA50:self.ret(0,12)  # native invalid-text popup, no transaction

    def run(self,entry,item,price,average=0,coupon=False):
        self.item,self.price,self.coupon=item,price,coupon
        self.fields=[];self.flush=0
        self.u.mem_write(OBJECT,bytes(0x1000));self.w(OBJECT+0x1b0,COMBO)
        self.w(OBJECT+0x1b8,TEXT);self.w(TEXT,VTABLE);self.w(VTABLE+0xe4,GETTEXT)
        self.w(OBJECT+0x1d8,average);self.w(OBJECT+0x210,24)
        self.u.mem_write(OBJECT+0x214,struct.pack('<f',price/average if average else price))
        self.u.mem_write(STR,(str(price)+'\0').encode('utf-16le'))
        registers={UC_X86_REG_EBX:0x11111111,UC_X86_REG_ESI:0x22222222,
                   UC_X86_REG_EDI:0x33333333,UC_X86_REG_EBP:0x44444444}
        for reg,v in registers.items():self.u.reg_write(reg,v)
        sp=STACK+0x8000;self.w(sp,STOP)
        self.u.reg_write(UC_X86_REG_ESP,sp);self.u.reg_write(UC_X86_REG_ECX,OBJECT)
        self.u.emu_start(entry,STOP,count=5000)
        assert self.u.reg_read(UC_X86_REG_EIP)==STOP,'native path did not return'
        assert self.u.reg_read(UC_X86_REG_ESP)==sp+4,'native stack mismatch'
        for reg,v in registers.items():assert self.u.reg_read(reg)==v,'callee-saved register changed'
        return self.u.reg_read(UC_X86_REG_EAX)


def verify(data):
    out,receipt=patch.build(data)
    old=Machine(data);new=Machine(out);count=0
    # The old real fee routine reproduces rejection below the historical 40% floor.
    assert old.run(patch.FEE_SITE,2681725,1000,12500)==0xffffffff
    assert new.run(patch.FEE_SITE,2681725,1000,12500)==5
    count+=2
    for item,minimum in patch.TIERS:
        for price in (minimum-1,minimum,minimum+1):
            for avg in (0,minimum*100):
                for coupon in (False,True):
                    want=0xffffffff if price<minimum else (1 if coupon else 5)
                    assert new.run(patch.FEE_SITE,item,price,avg,coupon)==want,(item,price,avg,coupon)
                    count+=1
            new.run(0x233F600,item,price)
            if price<minimum:
                assert not new.fields and not new.flush and not new.r(OBJECT+0x1fc)
            else:
                assert [v for _,v in new.fields]==[183,1,0,0,item,1,0xffffffff,price,0,24]
                assert new.flush==1 and new.r(OBJECT+0x1fc)==1
            count+=1
    for item in (0,2681724,2681737,2683068,2683074,0x7fffffff):
        assert new.run(patch.FEE_SITE,item,200000,0)==0xffffffff
        new.run(0x233F600,item,200000)
        assert not new.fields and not new.flush
        count+=2
    for price,want in ((1499,5),(1599,30),(1699,40),(1799,50),(1801,95)):
        assert new.run(patch.FEE_SITE,2681725,price,1000)==old.run(patch.FEE_SITE,2681725,price,1000)==want
        count+=1
    # Existing native float equality behavior is preserved, not silently changed
    # to a new fee policy as part of the minimum-price task.
    for price in (1500,1600,1700,1800,0x7fffffff):
        assert new.run(patch.FEE_SITE,2681725,price,1000)==old.run(patch.FEE_SITE,2681725,price,1000)
        count+=1
    # Wire/owner prices are capped at signed INT32_MAX. Reject parser high-bit
    # results here without claiming to emulate the parser's overflow behavior.
    for price in (0,-1,0x80000000,0xffffffff):
        assert new.run(patch.FEE_SITE,2681725,price)==0xffffffff
        new.run(0x233F600,2681725,price)
        assert not new.fields and not new.flush
        count+=2
    # All client amounts must match the authoritative server tier map.
    source=(Path(__file__).resolve().parents[2]/'go-server/internal/modules/dnf/auction/goldconsign.go').read_text('utf8')
    tiers={int(i):int(amount.replace('_',''))//1000 for i,amount in re.findall(r'ItemID: (\d+), Amount: ([\d_]+)',source)}
    assert tiers==dict(patch.TIERS)
    mapped=pefile.PE(data=out)
    assert mapped.OPTIONAL_HEADER.CheckSum==mapped.generate_checksum()
    sp=STACK+0x8000;new.u.reg_write(UC_X86_REG_ESP,sp)
    new.u.emu_start(patch.HINT_SITE,patch.HINT_SITE+10,count=10)
    assert new.u.reg_read(UC_X86_REG_ESP)==sp-4
    hint=new.u.reg_read(UC_X86_REG_EAX)
    assert bytes(new.u.mem_read(hint,len((patch.HINT+'\0').encode('utf-16le'))))==(patch.HINT+'\0').encode('utf-16le')
    count+=1
    return dict(passed=True,cases=count,receipt=receipt,baselineRejectedFixedPrice=True,
                originalFeeLadderPreserved=True,serverTierParity=True,
                externalAdapters=['combo selected item','price UI text','native integer conversion','coupon query','network writer','invalid-text message/popup'],
                gameExecuted=False,playerAcceptance='pending')


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--input',required=True,type=Path)
    parser.add_argument('--report',required=True,type=Path);a=parser.parse_args()
    result=verify(a.input.read_bytes());a.report.write_text(json.dumps(result,ensure_ascii=False,indent=2),'utf8')
    print(json.dumps({k:v for k,v in result.items() if k!='receipt'},ensure_ascii=False))
