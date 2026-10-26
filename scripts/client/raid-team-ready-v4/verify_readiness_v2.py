"""Execute v2 hooks, native 601 parser/setter/getter and bounded reward flow.

Immutable v1 Harness supplies native tick/loop/wrapper checks. This file does
not rewrite v1 artifacts. Packet-copy, raid map and UI/STL leaves are fixtures.
"""
import argparse
import json
import struct
import sys
from pathlib import Path
sys.dont_write_bytecode=True
import verify_readiness as old
import build_readiness_v2 as b
from unicorn.x86_const import *
from capstone import Cs,CS_ARCH_X86,CS_MODE_32
UI,RM,RAID,MODEL,OUTER,SCENE,ACTOR=old.UI,old.RM,old.RAID,old.MODEL,old.OUTER,old.SCENE,old.ACTOR
STACK,FRAME=old.STACK,old.FRAME
GENERIC=0x30080000


class Harness(old.Harness):
    def __init__(self,image,r):
        super().__init__(image,r)
        self.native(0x201DC5E,0x79);self.native(0x2678190,0x10)
        self.put(GENERIC,b'\xa5'*4096);self.put(0x56A37D0,GENERIC)
        for va in (0x37E82E0,0x37E83C0,0x37E8350):self.put(va,b'\xc3')
        self.body=b'';self.position=0;self.capture_entry=None;self.capture_restored=False
        self.lookup_result=RAID;self.lookup_count=0;self.lookup_change=None
    def step(self,u,va,size,arg):
        if va==self.stop:u.emu_stop();return
        if va==0x201DCBF:self.capture_entry=self.regs()
        elif va==0x201DCC5:
            expected=list(self.capture_entry);expected[2]=GENERIC
            assert self.regs()==expected,'capture restore + relocated original ECX'
            self.capture_restored=True
        if va in (0x37E82E0,0x37E83C0,0x37E8350):
            sp=u.reg_read(UC_X86_REG_ESP);dest=self.word(sp+4)
            count=self.word(sp+8) if va==0x37E82E0 else (4 if va==0x37E83C0 else 2)
            assert self.position+count<=len(self.body)
            self.put(dest,self.body[self.position:self.position+count]);self.position+=count
            self.ret(1);return
        if va==0x1A55350:
            sp=u.reg_read(UC_X86_REG_ESP)
            assert u.reg_read(UC_X86_REG_ECX)==RM and self.word(sp+4)==self.word(RM+4)
            self.lookup_count+=1
            if self.lookup_change:self.put(*self.lookup_change)
            self.ret(self.lookup_result,1);return
        super().step(u,va,size,arg)
    def receive601(self,result=0,phase=0,option=0):
        self.body=struct.pack('<BBIHBB',result,phase,1234,2,4,option)
        self.position=0;self.capture_entry=None;self.capture_restored=False
        before=bytes(self.u.mem_read(GENERIC,4096))
        registers=self.run(0x201DC5E,0x201DCD7)
        after=bytes(self.u.mem_read(GENERIC,4096))
        assert self.position==10 and self.capture_restored
        assert before[:0x360]==after[:0x360] and before[0x361:]==after[0x361:]
        assert after[0x360]==int(option==0)
        assert self.word(self.data)==self.word(self.data+16)==self.word(self.data+20)==0
        return registers
    def stage3(self):self.put(OUTER+0x420,3) # native intervening UI progression fixture


def verify_pe(src,out,r):
    assert b.pe.sha(src)==b.INPUT_SHA and b.pe.sha(out)==r['output_sha256']
    assert len(out)==len(src)+8192
    opt,sec=b.pe.headers(src);p=struct.unpack_from('<I',src,0x3c)[0]
    hdr=opt+struct.unpack_from('<H',src,p+20)[0]+37*40
    ranges=[(p+6,p+8),(opt+4,opt+12),(opt+56,opt+60),(opt+64,opt+68),(hdr,hdr+80)]
    for name,(va,raw) in b.SITES.items():
        off=b.pe.offset(sec,va,len(raw));assert src[off:off+len(raw)]==raw
        op=b'\xe9' if name in ('capture','mark') else b'\xe8'
        expected=op+struct.pack('<i',r['entries'][name]-va-5)+b'\x90'*(len(raw)-5)
        assert out[off:off+len(raw)]==expected;ranges.append((off,off+len(raw)))
    cursor=0
    for lo,hi in sorted(ranges):assert src[cursor:lo]==out[cursor:lo];cursor=hi
    assert src[cursor:]==out[cursor:len(src)]
    assert struct.unpack_from('<H',out,p+6)[0]==39
    for field,inc in [(opt+4,4096),(opt+8,4096),(opt+56,8192)]:
        assert struct.unpack_from('<I',out,field)[0]==struct.unpack_from('<I',src,field)[0]+inc
    code,entries=b.helpers(r['code_va'],r['data_va']);assert entries==r['entries']
    assert out[len(src):len(src)+len(code)]==code and not any(out[len(src)+len(code):])
    for i,(name,size,flags) in enumerate([(b'.rdyrx',len(code),0x60000020),(b'.rdyrw',44,0xC0000040)]):
        row=struct.pack('<8sIIIIIIHHI',name,size,len(src)+i*4096,4096,len(src)+i*4096,0,0,0,0,flags)
        assert out[hdr+i*40:hdr+(i+1)*40]==row
    assert struct.unpack_from('<I',out,opt+64)[0]==b.pe.checksum(out,opt+64)
    for va,size,sha in b.ANCHORS:
        off=b.pe.offset(sec,va,size);assert b.pe.sha(src[off:off+size])==sha
    return {'four_hook_ranges':sorted(ranges),'anchors':len(b.ANCHORS),'exact':True,'RX_RW_separate':True}


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output-dir',type=Path,required=True);a=ap.parse_args()
    available=b.v1.memory_guard();src=a.input.read_bytes();out=(a.output_dir/'DNF-r195-readiness-v2.exe').read_bytes()
    r=json.loads((a.output_dir/'build-v2.json').read_text());pe_result=verify_pe(src,out,r)
    results=[]
    def new():return Harness(out,r)
    def record(name,**kw):results.append(dict(case=name,**kw))
    def status(h):return h.word(h.data+40)
    def state_calls(h):return [x for x in h.events if x[0]=='state']
    def expect_wait(h):
        calls=len(state_calls(h));before,after=h.gate()
        assert len(state_calls(h))==calls and h.word(OUTER+0x420)==3
        assert before[:7]+before[8:]==after[:7]+after[8:]
    def expect_pass(h,arg=4):
        base=Harness(src,r);_,after=h.gate(arg);base.gate(arg)
        assert h.original_entry==base.original_entry and after==base.regs()
        assert state_calls(h)[-1]==('state',OUTER,arg)

    # Real ten-byte parser + hook + original setter compared to original image.
    for result,phase,option in [(x,y,z) for x in (0,1) for y in (0,1) for z in (0,1)]+[(0,2,0),(0,0,2),(0,255,255),(3,0,0)]:
        h,base=new(),Harness(src,r)
        a_regs=h.receive601(result,phase,option);b_regs=base.receive601(result,phase,option)
        assert a_regs==b_regs
        expected=option+1 if result==0 and phase<=1 and option<=1 else 3
        assert status(h)==expected and h.word(h.data+36)==phase
        assert (h.word(h.data+24),h.word(h.data+28),h.word(h.data+32))==(RM,RAID,1234)
        # Native writes limited to stack/parser frame, original generic setter,
        # dedicated RW section. Cache was already populated in these cases.
        assert all(STACK-0x1000<=addr<STACK or FRAME-0xB0<=addr<FRAME or
                   h.data<=addr<h.data+44 or addr==GENERIC+0x360 for addr,size,value in h.writes)
        h.owned_type=1;h.reset();h.stage3()
        if expected==1:expect_wait(h)
        else:expect_pass(h)
        record('native-parser-disposition-old-party',result=result,phase=phase,option=option,status=expected)

    for phase in (0,1):
        for missing in ('scene','local'):
            h=new();h.owned_type=0;h.put(UI+0xC3CC,0)
            h.put(0x56A3758 if missing=='scene' else SCENE+0x70,0)
            h.receive601(phase=phase);assert status(h)==1 # capture doesn't need outer
            h.put(UI+0xC3CC,OUTER);h.put(OUTER+0x420,0);h.reset();h.stage3()
            assert h.word(h.data+20)==1;expect_wait(h)
            h.mark();expect_pass(h)
            assert not any(x[0]=='ownership' for x in h.events)
            record('fresh-client-eligible-waits-then-kind3',phase=phase,missing=missing)
    for oldparty in (0,1,2):
        h=new();h.owned_type=oldparty;h.receive601(option=1);h.reset();h.stage3();expect_pass(h)
        assert not any(x[0]=='ownership' for x in h.events)
        record('denied-wins-over-party',party_type=oldparty)
    for result,phase,option in [(1,0,0),(0,2,0),(0,0,2),(3,1,255)]:
        h=new();h.receive601();h.reset();h.mark()
        assert h.word(h.data+20)==1 and h.word(h.data+16)==MODEL
        h.receive601(result,phase,option)
        assert status(h)==3 and h.word(h.data)==h.word(h.data+16)==h.word(h.data+20)==0
        h.owned_type=1;h.reset();h.stage3();expect_pass(h)
        assert h.word(h.data+20)==3
        record('invalid-new-601-revokes-old-eligible-fresh-party',result=result,phase=phase,option=option)

    # Execute actual 1A553E0 hit and miss; only its map leaf 1A55350 is fixture.
    for cached,lookup,expected,calls in [(RAID,RAID,1,0),(0,RAID,1,1),(0,0,0,1)]:
        h=new();h.put(RM+8,cached);h.lookup_result=lookup
        regs=h.receive601();assert status(h)==expected and h.lookup_count==calls
        assert h.word(RM+8)==(cached or lookup)
        base=Harness(src,r);assert regs==base.receive601()
        assert all(STACK-0x1000<=addr<STACK or FRAME-0xB0<=addr<FRAME or h.data<=addr<h.data+44 or
                   addr in (GENERIC+0x360,RM+8) for addr,size,value in h.writes)
        record('real-raid-getter',cached=bool(cached),map_hit=bool(lookup),status=expected,map_calls=calls)
    for reason,change in [('key',(RM+4,999)),('manager',(0x56A3778,RM+0x100))]:
        h=new();h.put(RM+8,0);h.lookup_change=change;h.receive601();assert status(h)==0
        record('getter-identity-recheck',change=reason)

    # Old active state and result are invalidated even on invalid/other mode.
    for reason,addr,val in [('othermode',RAID+0x24,1),('thirdmode',RAID+0x24,2),
                          ('manager-null',0x56A3778,0),('key-left',RM+4,0xffffffff)]:
        h=new();h.receive601();h.reset();h.mark();h.put(addr,val);h.receive601()
        assert status(h)==0 and h.word(h.data)==0 and h.word(h.data+16)==0 and h.word(h.data+20)==0
        record('capture-invalidates-old-active-and-record',reason=reason)
    h=new();h.receive601();h.reset();h.mark();h.put(RM+8,0);h.lookup_result=0;h.receive601()
    assert status(h)==0;record('lookup-miss-clears-old-eligible')
    # A later cache recovery cannot adopt a partially captured old 601.
    h.put(RM+8,RAID);h.reset();assert h.word(h.data+20)==0
    h.owned_type=0;h.stage3();expect_pass(h);record('cache-recovery-without-new-601-remains-unknown')

    h=new();h.receive601(phase=0);h.reset();h.mark()
    h.receive601(phase=1,option=1);assert status(h)==2 and h.word(h.data)==0 and h.word(h.data+16)==0
    h.stage3();expect_pass(h) # before next587 no old active eligibility
    h.reset();h.stage3();expect_pass(h)
    record('newphase-same-pointer-invalidates-before-reset')
    h=new();h.owned_type=0;h.receive601(phase=0);h.reset();h.mark()
    h.receive601(phase=1);h.reset();h.stage3();expect_wait(h);h.mark();expect_pass(h)
    record('newphase-same-pointer-eligible-requires-new-kind3')
    for option in (0,1):
        h=new();h.owned_type=0;h.receive601(option=option);h.reset();h.mark();h.reset();h.stage3()
        assert status(h)==option+1 and h.word(h.data+20)==option+1 and h.word(h.data+16)==0
        if option==0:expect_wait(h);h.mark();expect_pass(h)
        else:expect_pass(h)
        record('repeat-587-retains-result-clears-fresh',option=option)

    for reason,addr,val in [('key',RM+4,555),('raid',RM+8,RAID+0x100),
                          ('manager',0x56A3778,RM+0x100)]:
        h=new();h.receive601();h.put(addr,val);h.reset()
        assert h.word(h.data+20)==0
        h.owned_type=0;h.stage3();expect_pass(h)
        record('reset-result-identity-mismatch',reason=reason)
    for reason,addr,val in [('key',RM+4,555),('cache-null',RM+8,0),
                          ('outer',UI+0xC3CC,OUTER+0x800),('mode',RAID+0x24,1)]:
        h=new();h.receive601();h.reset();h.put(addr,val);h.stage3();expect_pass(h)
        record('active-identity-change-passthrough',reason=reason)
    h=new();h.receive601();h.reset();h.run(0x1A64B80,0x1A64B97,RM,stack_delta=-8)
    h.stage3();expect_pass(h);record('native-manager-exit-releases-known-eligible')

    # Unknown preserves v1 ownership semantics; known eligibility skips it.
    for own in (0,1,2):
        h=new();h.owned_type=own;h.reset();h.stage3()
        if own:expect_wait(h);h.mark();expect_pass(h)
        else:expect_pass(h)
        record('unknown-v1-fallback',ownership=own)
    h=new();h.put(0x56A3758,0);h.reset();h.stage3();expect_pass(h)
    record('unknown-no-scene-preserves-v1-bypass')
    for kind,count in [(0,1),(1,1),(2,1),(4,1),(3,0)]:
        h=new();h.owned_type=0;h.receive601();h.reset();h.mark(kind,count);h.stage3()
        assert h.word(h.data+16)==0;expect_wait(h);record('non-kind3-not-ready',kind=kind,blocks=count)
    for count in (1,2,4):
        h=new();h.owned_type=0;h.receive601();h.reset();h.put(OUTER+0x420,0);h.mark(count=count)
        assert h.word(h.data+16)==MODEL;h.stage3();expect_pass(h)
        record('closed-window-complete-loop-fresh',blocks=count)
    h=new();h.receive601();h.reset();h.mark();h.row=False;h.stage3();expect_wait(h);record('fresh-row0-missing')
    h=new();h.receive601();h.mark();assert h.word(h.data+16)==0
    h.reset();h.stage3();expect_wait(h);record('reverse-kind3-before-reset-rejected')
    for arg,modelmode,skip in [(5,1,0),(4,2,0),(4,3,0),(4,1,1)]:
        h=new();h.receive601();h.reset();h.put(MODEL+4,modelmode);h.put(OUTER+0x428,skip)
        h.stage3();expect_pass(h,arg);record('original-passthrough',arg=arg,model_mode=modelmode,skip=skip)
    for state,special,location in [(0,False,1),(3,False,1),(4,False,1),(0,True,1),(0,True,0),(0,True,2)]:
        h,base=new(),Harness(src,r);h.receive601()
        for obj in (h,base):obj.put(OUTER+0x420,state);obj.special=special;obj.location=location
        assert h.reset()==base.reset() and h.original_entry==base.original_entry
        record('native-reset-wrapper-differential',state=state,special=special,location=location)

    # Document assumptions as executable counterexamples, never pass as fixes.
    h=new();h.owned_type=0;h.receive601();h.reset();h.mark();h.stage3();expect_pass(h)
    h.reset();h.stage3();expect_wait(h)
    record('unsupported-isolated-587-after-kind3',behavior='waits until another kind3',server_contract_required=True)
    h=new();h.receive601();h.reset();h.reset()
    assert h.word(h.data+20)==1
    record('same-token-without-new-601-indistinguishable',server_601_identity_order_required=True)

    result={'passed':len(results),'cases':results,'pe':pe_result,'input_sha256':b.INPUT_SHA,
            'output_sha256':r['output_sha256'],'protectionFloorGiB':3,'available_bytes_before':available,
            'native_anchors':r['native_anchors'],'gui_accepted':False,'deployed':False,
            'boundaries':['packet copy, raid map lookup, parsed 600 blocks, model/STL and UI leaves are fixtures',
                          'native parser/setter/getter wrappers and installed helpers execute',
                          '591 pre-601 metadata UI path not covered; main-thread review pending',
                          'server current-result 601-before-network587 prerequisite; same-stage record retained']}
    (a.output_dir/'verify-v2.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
    md=Cs(CS_ARCH_X86,CS_MODE_32);code,_=b.helpers(r['code_va'],r['data_va'])
    (a.output_dir/'helper-v2.asm').write_text('\n'.join(f'{x.address:08X} {x.bytes.hex():22} {x.mnemonic} {x.op_str}' for x in md.disasm(code,r['code_va']))+'\n',encoding='utf-8')
    print(json.dumps({'passed':len(results),'output_sha256':r['output_sha256']}))

if __name__=='__main__':main()
