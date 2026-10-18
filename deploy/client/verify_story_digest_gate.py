"""Bounded x86 tests for story_digest_gate (pefile, capstone, unicorn required).

Native: original/copied auto function, descriptor/scene lookups, selected actor
getter with an existing actor, complete 30000-entry loader loop, three-tree clear,
new snapshot/clear/gate code, 1377 reader and 65536 receive-gate calls.
Stubs: interval insertion/allocation/free, encoded level, video/UI/clock/network.
The native 356 decompressor and entire 124 handler are NOT executed.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import re
import struct

import capstone
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import story_digest_gate as patch

DIGEST_EXPORT_SHA256='1ba3d6399dd4ff52c86438b6051747bd9d5eb9eec4552bc36eb544712e9f89f7'


def descriptors(text):
    result=[]
    for m in re.finditer(r'`(\d+)`\s*\[level\]\s*(\d+)\s+(\d+)\s*\[opening\]\s*(\d+)\s*\[scene\]\s*([\d\s]+?)\[ending\]\s*(\d+)',text):
        i,lo,hi,opening,scenes,ending=m.groups()
        result.append(dict(id=int(i),lo=int(lo),hi=int(hi),opening=int(opening),scenes=list(map(int,scenes.split())),ending=int(ending)))
    if len(result)!=17:raise ValueError('expected the matched 17 segment export')
    return result


class Native:
    def __init__(self,data,entries,segments):
        self.data,self.entries,self.segments=data,entries,segments
        self.pe=pefile.PE(data=data,fast_load=True)
        self.u=Uc(UC_ARCH_X86,UC_MODE_32)
        self.u.mem_map(0x400000,patch.align(self.pe.OPTIONAL_HEADER.SizeOfImage,4096))
        self.u.mem_write(0x400000,data[:self.pe.OPTIONAL_HEADER.SizeOfHeaders])
        for s in self.pe.sections:self.u.mem_write(0x400000+s.VirtualAddress,s.get_data())
        for a,n in ((0,4096),(0x10000000,0x100000),(0x20000000,0x200000),(0x30000000,4096)):
            self.u.mem_map(a,n)
        self.stack,self.stop=0x10080000,0x30000000
        self.manager,self.player,self.stats,self.desc=0x20000000,0x20010000,0x20020000,0x20030000
        self.table,self.buffer,self.context=0x20050000,0x20060000,0x20070000
        self.state={};self.trace=[];self.visited=set()
        self.u.hook_add(UC_HOOK_CODE,self.hook)
        self.w(0x4588670,self.stop+0x100)
        self.reset(30,0)

    def w(self,a,v):self.u.mem_write(a,struct.pack('<I',v&0xffffffff))
    def rd(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]

    def ret(self,pop=0,value=0):
        sp=self.u.reg_read(UC_X86_REG_ESP);target=self.rd(sp)
        self.u.reg_write(UC_X86_REG_EAX,value&0xffffffff)
        self.u.reg_write(UC_X86_REG_ESP,sp+4+pop);self.u.reg_write(UC_X86_REG_EIP,target)

    def hook(self,uc,a,size,user):
        if a in (self.stop,self.state.get('end')):self.u.emu_stop();return
        self.visited.add(a);self.trace.append(a)
        if len(self.trace)>20:del self.trace[0]
        sp=self.u.reg_read(UC_X86_REG_ESP);this=self.u.reg_read(UC_X86_REG_ECX)
        if a==0x91a120:self.ret(value=self.state.get('mode_block',0))
        elif a==0x3c1bea0:self.ret(value=self.stats)
        elif a in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):
            assert this in (self.stats+0x14,0x57b7218),hex(this)
            self.w(self.rd(sp+8),self.state['level'] if this==self.stats+0x14 else 0);self.ret(8)
        elif a==0x296d730 and self.state.get('no_actor'):self.ret(value=0)
        elif a==0x37de240:self.ret(value=self.buffer+0x100)
        elif a==0x3c8e2e0:self.state['events'].append('load_scene_table');self.ret()
        elif a==0x374ea10:self.ret(value=self.buffer+0x200)
        elif a==0x374f280:self.state['events'].append('header');self.state['building']=self.rd(sp+4);self.ret(4)
        elif a in (0x374f390,0x374f3c0,0x374f3f0,0x374f420):raise AssertionError('unexpected body')
        elif a==0x3750ac0:self.state['events'].append('send');self.state['packets'].append(self.state.pop('building'));self.ret()
        elif a==self.stop+0x100:self.ret(value=12345)
        elif a==0x1453210:self.state['events'].append('start_state');self.w(this+0x30,self.rd(sp+4));self.ret(4)
        elif a==0x1453f00:self.state['events'].append('play_ui');self.ret()
        elif a==0x29219c0:
            # Observe the native loader's actual source indices. Tree insertion
            # is unrelated to the candidate's independent exact snapshot bits.
            pair=self.rd(self.rd(sp+8));self.state['inserted'].append(pair&65535)
            assert pair&65535==pair>>16
            self.ret(8,value=self.rd(sp+4))
        elif a==0x3e70882:self.state['freed'].append(self.rd(sp+4));self.ret()

    def run(self,address,this,args=(),pop=0,end=None):
        self.trace.clear();self.state['end']=end
        self.w(self.stack,self.stop)
        for i,v in enumerate(args):self.w(self.stack+(0 if end else 4)+i*4,v)
        saved={UC_X86_REG_EBX:0x11223344,UC_X86_REG_ESI:0x22334455,
               UC_X86_REG_EDI:0x33445566,UC_X86_REG_EBP:0x44556677}
        for r,v in saved.items():self.u.reg_write(r,v)
        for r in (UC_X86_REG_EAX,UC_X86_REG_EDX):self.u.reg_write(r,0)
        self.u.reg_write(UC_X86_REG_ESP,self.stack);self.u.reg_write(UC_X86_REG_ECX,this)
        self.u.reg_write(UC_X86_REG_EFLAGS,0x202)
        try:self.u.emu_start(address,0,count=1000000)
        except Exception as error:raise AssertionError((hex(address),list(map(hex,self.trace)))) from error
        assert self.u.reg_read(UC_X86_REG_EIP)==(end or self.stop)
        assert self.u.reg_read(UC_X86_REG_ESP)==self.stack+(0 if end else 4)+pop
        for r,v in saved.items():assert self.u.reg_read(r)==v,('callee-saved',r)
        return self.u.reg_read(UC_X86_REG_EAX)&255

    def reset(self,level,last,actor=None,mode_block=0):
        self.state=dict(level=level,events=[],packets=[],inserted=[],freed=[],mode_block=mode_block)
        self.u.mem_write(self.manager,bytes(0x100));self.u.mem_write(self.desc,bytes(0x10000))
        self.w(0x566ee2c,self.manager);self.w(patch.PLAYER_MANAGER,self.context)
        self.w(self.context+0x70,self.player if actor is None else actor)
        self.w(self.manager+0x44,last);self.w(self.manager+0x20,self.desc)
        self.w(self.manager+0x24,self.desc+len(self.segments)*0x44)
        for j,s in enumerate(self.segments):
            p=self.desc+j*0x44;v=self.desc+0x2000+j*0x100;proxy=v+0xf0
            for off,value in ((0x20,s['lo']),(0x24,s['hi']),(0x28,s['opening']),(0x2c,s['ending']),
                              (0x30,proxy),(0x34,v),(0x38,v+len(s['scenes'])*4)):
                self.w(p+off,value)
            self.w(proxy,p+0x30)
            for k,n in enumerate(s['scenes']):self.w(v+k*4,n)
        if 'state' in self.entries:self.u.mem_write(self.entries['state'],bytes(8))

    def snapshot(self,completed=()):
        body=bytearray(30000)
        for i in completed:body[i]=1
        self.u.mem_write(self.table,bytes(body));self.state['inserted']=[]
        # Execute the actual call instruction with its verified original ECX.
        self.run(patch.SNAPSHOT_CALL,patch.COLLECTION,(self.table,0),8,end=patch.SNAPSHOT_CALL+5)
        assert self.state['inserted']==sorted(completed)

    def clear(self,entry=patch.CLEAR_ENTRY,end=None):
        for i in range(3):
            obj=patch.COLLECTION+i*0x14;head=self.buffer+0x300+i*0x80;node=head+0x30
            self.w(obj+8,head);self.w(obj+12,1)
            for off,v in ((0,node),(4,node),(8,node)):self.w(head+off,v)
            self.u.mem_write(head+0x15,b'\x01')
            for off,v in ((0,head),(4,head),(8,head)):self.w(node+off,v)
            self.u.mem_write(node+0x15,b'\x00')
        self.run(entry,patch.COLLECTION,end=end)
        for i in range(3):
            obj=patch.COLLECTION+i*0x14;head=self.rd(obj+8)
            assert self.rd(obj+12)==0 and all(self.rd(head+off)==head for off in (0,4,8))

    def auto(self):return self.run(patch.AUTO_CALL,self.manager,(0,),4,end=patch.AUTO_CALL+5)

    def lifecycle(self,kind,success):
        if kind=='select':start,end=(0x1fb2230,0x1fb223f if success else 0x1fb34b9)
        else:start,end=(0x1fb9123,0x1fb9133 if success else 0x1fb9329)
        self.state['end']=end;self.w(self.stack+0x10c,int(success))
        regs={UC_X86_REG_EAX:0x1122,UC_X86_REG_EBX:0,UC_X86_REG_ECX:0x2233,
              UC_X86_REG_EDX:0x3344,UC_X86_REG_ESI:0x4455,UC_X86_REG_EDI:0x5566,
              UC_X86_REG_EBP:self.stack+0x100,UC_X86_REG_ESP:self.stack,UC_X86_REG_EFLAGS:0x202}
        for r,v in regs.items():self.u.reg_write(r,v)
        self.u.emu_start(start,0,count=1000)
        assert self.u.reg_read(UC_X86_REG_EIP)==end
        return {str(r):self.u.reg_read(r)for r in regs}

    def capture(self,label,result):
        row=dict(case=label,return_bool=bool(result),last=self.rd(self.manager+0x44),
                 packets=self.state['packets'][:],events=self.state['events'][:])
        if self.state['packets']:
            row.update(segment=self.rd(self.manager+0x34),first=self.rd(self.manager+0x38))
            assert row['packets']==[1443]
            assert row['events'].index('send')<row['events'].index('start_state')<row['events'].index('play_ui')
        return row


def verify(source,candidate,receipt,segments,snapshots=()):
    assert patch.sha256(source)==patch.SOURCE_SHA256
    assert patch.sha256(candidate)==receipt['candidate_sha256']
    p=pefile.PE(data=candidate,fast_load=True);assert p.verify_checksum()
    e=receipt['entries'];src=Native(source,{},segments);dst=Native(candidate,e,segments)
    rows=[]
    # Relocations independently checked with Capstone instruction decoding.
    md=capstone.Cs(capstone.CS_ARCH_X86,capstone.CS_MODE_32)
    def raw(n,a,z):return n.pe.get_data(a-0x400000,z)
    orig=list(md.disasm(raw(src,patch.ORIGINAL,0x1f9),patch.ORIGINAL))
    copy={i.address-e['clone']:i for i in md.disasm(raw(dst,e['clone'],0x1f9),e['clone'])}
    for i in orig:
        off=i.address-patch.ORIGINAL
        if i.mnemonic=='call':assert copy[off].op_str==i.op_str,(hex(off),copy[off],i)
        if i.mnemonic.startswith('j') and i.op_str.startswith('0x') and off!=0x12d:
            assert int(copy[off].op_str,16)-e['clone']==int(i.op_str,16)-patch.ORIGINAL
    assert raw(dst,patch.ORIGINAL,patch.CLONE_SIZE)==raw(src,patch.ORIGINAL,patch.CLONE_SIZE)
    assert struct.unpack('<i',raw(dst,patch.SNAPSHOT_CALL+1,4))[0]+patch.SNAPSHOT_CALL+5==e['snapshot']
    rows.append(dict(case='original_function_retained_and_clone_relocations',passed=True))
    # Same-level original red cases, then candidate closure/opening matrix.
    for label,level,last,completed,want in [
        ('24_without_3190',24,23,(),False),('24_active0_unsubmitted',24,23,(),False),
        ('24_completed_3190',24,23,(3190,),True),('30_without_3215',30,29,(3190,),False),
        ('30_completed_3215',30,29,(3215,),True),('30_cross_only3215',30,0,(3215,),False),
        ('30_cross_only3190',30,0,(3190,),False),('30_cross_both',30,0,(3190,3215),True),
        ('90_cross_only3215',90,0,(3215,),False),('90_cross_both',90,0,(3190,3215),True),
        ('90_seen30_no_new_protected',90,30,(),True),('already_seen30',30,30,(),False),
        ('old_account90',30,90,(),False),('negative_last',30,0xffffffff,(),False),
        ('invalid_last',30,1000,(),False),('no_segment',16,0,(),False),
        ('chapter1_no_gate',17,0,(),True),('same_chapter1',23,17,(),False),
        ('35_completed_3215',35,29,(3215,),True)]:
        src.reset(level,last);baseline=src.capture(label,src.auto())
        dst.reset(level,last);dst.snapshot(completed)
        before=bytes(dst.u.mem_read(dst.manager,0x100));result=dst.auto();row=dst.capture(label,result)
        assert bool(result)==want,(label,row)
        if want:
            assert {k:v for k,v in row.items() if k!='case'}=={k:v for k,v in baseline.items()if k!='case'}
        else:
            assert not row['packets'] and row['last']==last
            assert bytes(dst.u.mem_read(dst.manager,0x100))==before
        row['original_played']=baseline['return_bool'];rows.append(row)
    for level,last in ((24,23),(30,29),(90,0)):
        dst.reset(level,last);assert not dst.auto()
        rows.append(dst.capture('missing_356_'+str(level),0))
    # Bind exact incoming snapshot, not additive historical collection content.
    dst.reset(30,0);dst.snapshot((3190,3215));dst.snapshot(())
    assert not dst.auto();rows.append(dst.capture('replacement_empty_snapshot_denies',0))
    dst.reset(30,0);dst.snapshot((3190,3215));dst.clear()
    assert dst.rd(e['state'])==0 and dst.rd(e['state']+4)==0
    assert not dst.auto();rows.append(dst.capture('clear_three_trees_invalidates_even_same_pointer',0))
    dst.snapshot((3190,));assert not dst.auto()
    dst.snapshot((3190,3215));assert dst.auto()
    rows.append(dst.capture('new_snapshot_after_clear_reopens',1))
    for label,entry,end in (('noti1_clear_fragment',0x202396a,0x2023974),
                            ('second_cleanup_caller_fragment',0x691c7c1,0x691c7cc)):
        dst.reset(30,0);dst.snapshot((3190,3215));dst.clear(entry,end)
        assert dst.rd(e['state'])==0 and not dst.auto()
        rows.append(dst.capture(label,0))
    dst.reset(30,0);dst.snapshot((3190,3215));dst.w(dst.context+0x70,dst.player+0x400)
    assert not dst.auto();rows.append(dst.capture('actor_pointer_changed_without_clear_denied',0))
    dst.snapshot(());assert not dst.auto()
    rows.append(dst.capture('second_actor_empty_snapshot_denied',0))
    # Completion refresh alone does not play. Retry only the normal later call.
    dst.reset(30,29);dst.snapshot((3190,));assert not dst.auto()
    dst.snapshot((3190,3215));assert not dst.state['events'] and not dst.state['packets']
    assert dst.auto();rows.append(dst.capture('completion_deferred_until_next_safe_call',1))
    dst.state['events']=[];dst.state['packets']=[];assert not dst.auto()
    rows.append(dst.capture('skip_interrupt_reentry_keeps_accepted_last',0))
    # Unmodified original entry can still be invoked independently of auto gate.
    dst.reset(30,29);assert dst.run(patch.ORIGINAL,dst.manager,(0,),4)
    rows.append(dst.capture('other_original_entry_unchanged_not_manual_ui_acceptance',1))
    # Check protected ending even with no protected scene in the new suffix.
    dst.reset(30,0);dst.w(dst.desc+2*0x44+0x38,dst.desc+0x2204)
    dst.snapshot(());assert not dst.auto()
    rows.append(dst.capture('ending903_alone_still_requires3215',0))
    dst.reset(30,29,mode_block=1);dst.snapshot((3190,3215));assert not dst.auto()
    rows.append(dst.capture('native_mode_block_unchanged',0))
    dst.reset(30,29,actor=0);dst.state['no_actor']=True
    dst.snapshot((3190,3215));assert dst.rd(e['state'])==0 and not dst.auto()
    rows.append(dst.capture('snapshot_without_selected_actor_not_ready',0))
    dst.reset(30,0);dst.w(dst.buffer,29)
    dst.w(0x57f9ad0,dst.buffer);dst.w(0x57f9ad4,4);dst.w(0x57f9af8,0)
    dst.run(0x1455210,0)
    assert dst.rd(dst.manager+0x44)==29 and not dst.state['events'] and not dst.auto()
    rows.append(dst.capture('1377_does_not_grant_quest_readiness',0))
    for kind in ('select','return'):
        for success in (False,True):
            src.reset(30,0);dst.reset(30,0);dst.snapshot((3190,3215))
            expected=src.lifecycle(kind,success);actual=dst.lifecycle(kind,success)
            assert actual==expected,(kind,success,expected,actual)
            assert bool(dst.rd(e['state'])) != success
            result=dst.auto();assert bool(result) != success
            rows.append(dst.capture(kind+('_success_invalidates_same_pointer'if success else '_failure_keeps_current_snapshot'),result))
    for fixture in snapshots:
        dst.reset(30,0)
        body=base64.b64decode(fixture['table']);assert len(body)==30000
        dst.u.mem_write(dst.table,body)
        dst.run(patch.SNAPSHOT_CALL,patch.COLLECTION,(dst.table,0),8,end=patch.SNAPSHOT_CALL+5)
        result=dst.auto();assert bool(result)==fixture['want']
        rows.append(dst.capture('go_builder_'+fixture['name'],result))
    # Gate machine code/bitmap intact, including 475. Full u16 domain is native.
    gate=0x6e83610;bitmap=raw(src,0x6e92300,185)
    assert raw(dst,gate,33)==raw(src,gate,33) and raw(dst,0x6e92300,185)==bitmap
    for opcode in range(65536):
        expected=opcode<=1474 and bool(bitmap[opcode//8]&(1<<(opcode%8)))
        assert bool(dst.run(gate,0,(opcode,),4))==expected,opcode
    rows.append(dict(case='all_65536_native_receive_admissions_preserved_including475',passed=True))
    return dict(status='PASS',count=len(rows),cases=rows,candidate_sha256=patch.sha256(candidate),
                source_sha256=patch.SOURCE_SHA256,executed_addresses=len(dst.visited),
                stubs=['interval insertion','allocation/free','encoded level','video/UI','clock','network'],
                not_executed=['356 decompressor','whole124','real network','manual UI','video rendering'])


def main():
    p=argparse.ArgumentParser(description=__doc__)
    for n in ('source','candidate','receipt','digest','pvf','output'):p.add_argument('--'+n,type=Path,required=True)
    p.add_argument('--snapshots',type=Path,help='optional JSON of actual Go builder snapshots')
    a=p.parse_args()
    assert hashlib.sha256(a.pvf.read_bytes()).hexdigest()==patch.PVF_SHA256
    assert hashlib.sha256(a.digest.read_bytes()).hexdigest()==DIGEST_EXPORT_SHA256
    receipt=json.loads(a.receipt.read_text(encoding='utf-8'))
    result=verify(a.source.read_bytes(),a.candidate.read_bytes(),receipt,descriptors(a.digest.read_text(encoding='utf-8-sig')),
                  json.loads(a.snapshots.read_text()) if a.snapshots else ())
    a.output.write_text(json.dumps(result,indent=2),encoding='utf-8')
    print(json.dumps(dict(status=result['status'],cases=result['count'],executed_addresses=result['executed_addresses'])))


if __name__=='__main__':main()
