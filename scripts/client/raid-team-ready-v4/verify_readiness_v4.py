"""Execute six shared v4 hooks with native parser/lifecycle windows and bounded leaves."""
import argparse,json,struct,sys
from pathlib import Path
sys.dont_write_bytecode=True
import build_readiness_v4 as b
import verify_readiness_v2 as v2
from unicorn.x86_const import *
old=v2.old
UI,RM,RAID,MODEL,OUTER,SCENE=v2.UI,v2.RM,v2.RAID,v2.MODEL,v2.OUTER,v2.SCENE
FRAME,STACK=v2.FRAME,v2.STACK

class Harness(v2.Harness):
    def __init__(self,image,r):
        super().__init__(image,r)
        self.native(0x2001517,6)
        self.native(0x20012D7,7)
    def step(self,u,va,size,arg):
        if va==self.stop:u.emu_stop();return
        if va==0x202FA91:
            # Same decoded-payload fixture as v1. Full parser/STL not executed.
            self.put(FRAME-0x78,2 if self.kind==3 else 0)
            for off in (0x60,0x64,0x98,0xa0,0x6c,0x80):self.put(FRAME-off,0)
            self.put(FRAME-0x40,123)
            u.reg_write(UC_X86_REG_ECX,2 if self.kind==3 else 0)
            u.reg_write(UC_X86_REG_EIP,0x202FC26);return
        if va==0x192D960:
            assert u.reg_read(UC_X86_REG_ECX)==MODEL
            self.completed+=1;self.events.append(('block-update',self.completed));self.ret(0,2);return
        super().step(u,va,size,arg)
    def phase_clear(self):return self.run(0x2001517,0x200151D)
    def zero_clear(self):return self.run(0x20012D7,0x20012DE)
    def leave(self,manager=RM):return self.run(0x1A64B80,0x1A64B97,manager,stack_delta=-8)

def verify_pe(src,out,r):
    assert b.pe.sha(src)==b.SHA and b.pe.sha(out)==r['output_sha256']
    assert len(out)==len(src)+8192
    opt,sec=b.pe.headers(src);p=struct.unpack_from('<I',src,0x3c)[0]
    hdr=opt+struct.unpack_from('<H',src,p+20)[0]+37*40
    ranges=[(p+6,p+8),(opt+4,opt+12),(opt+56,opt+60),(opt+64,opt+68),(hdr,hdr+80)]
    for name,(va,raw) in b.SITES.items():
        off=b.pe.offset(sec,va,len(raw));assert src[off:off+len(raw)]==raw
        assert out[off:off+len(raw)]==(b'\xe8' if name=='gate' else b'\xe9')+struct.pack('<i',r['entries'][name]-va-5)+b'\x90'*(len(raw)-5)
        ranges.append((off,off+len(raw)))
    assert sorted(ranges)==[tuple(x) for x in r['allowed_ranges']]
    last=0
    for lo,hi in sorted(ranges):assert src[last:lo]==out[last:lo];last=hi
    assert src[last:]==out[last:len(src)]
    assert struct.unpack_from('<H',out,p+6)[0]==39
    for field,inc in [(opt+4,4096),(opt+8,4096),(opt+56,8192)]:
        assert struct.unpack_from('<I',out,field)[0]==struct.unpack_from('<I',src,field)[0]+inc
    for i,(name,size,flags) in enumerate([(b'.a4rx',r['code_size'],0x60000020),(b'.a4rw',44,0xC0000040)]):
        assert out[hdr+i*40:hdr+(i+1)*40]==struct.pack('<8sIIIIIIHHI',name,size,len(src)+i*4096,4096,len(src)+i*4096,0,0,0,0,flags)
    assert not any(out[len(src)+r['code_size']:])
    assert struct.unpack_from('<I',out,opt+64)[0]==b.pe.checksum(out,opt+64)
    return dict(exact_changed_ranges=len(ranges),RX_RW_separate=True,original_587_open_call_unchanged=True)

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True);ap.add_argument('--candidate-dir',type=Path,required=True);a=ap.parse_args()
    available=b.old.memory_guard();src=a.input.read_bytes();out=(a.candidate_dir/'candidate.exe').read_bytes()
    r=json.loads((a.candidate_dir/'build.json').read_text());pe=verify_pe(src,out,r)
    results=[]
    def record(name,**kw):results.append(dict(case=name,**kw))
    def new():return Harness(out,r)
    def calls(h):return [x for x in h.events if x[0]=='state']
    def wait(h):
        count=len(calls(h));before,after=h.gate()
        assert len(calls(h))==count and h.word(OUTER+0x420)==3
        assert before[:7]+before[8:]==after[:7]+after[8:]
        assert all(STACK-0x1000<=addr<STACK+8 for addr,size,value in h.writes)
    def passed(h,arg=4):
        base=Harness(src,r);_,actual=h.gate(arg);base.gate(arg)
        assert h.original_entry==base.original_entry and actual==base.regs()
        assert calls(h)[-1]==('state',OUTER,arg)
    def empty(h):assert not any(h.u.mem_read(h.data,44))

    for phase in (0,1):
        for scene in (True,False):
            h=new();h.owned_type=0
            if not scene:h.put(0x56A3758,0)
            wait(h);h.receive601(phase=phase);wait(h)
            h.mark();passed(h)
            assert not any(x[0]=='ownership' for x in h.events)
            record('early-window-before-601-and-before-587',phase=phase,scene=scene)
    h=Harness(src,r);h.gate();assert calls(h)[-1]==('state',OUTER,4)
    record('baseline-early-window-enters-empty-team',observed_native_gate=True,early_UI_open_is_fixture=True)
    for result,phase,option in [(x,y,z) for x in (0,1) for y in (0,1) for z in (0,1)]+[(0,2,0),(0,0,2),(3,255,255)]:
        h,base=new(),Harness(src,r)
        assert h.receive601(result,phase,option)==base.receive601(result,phase,option)
        expected=option+1 if result==0 and phase<=1 and option<=1 else 3
        assert h.word(h.data+40)==expected
        if expected==1:wait(h)
        else:passed(h)
        record('native-601-parser-disposition',result=result,phase=phase,option=option)
    for stage in ('before601','after601','afterkind3'):
        h=new()
        if stage!='before601':h.receive601()
        if stage=='afterkind3':h.mark()
        before=bytes(h.u.mem_read(h.data,44));h.reset()
        assert before==bytes(h.u.mem_read(h.data,44))
        h.stage3()
        if stage=='afterkind3':passed(h)
        else:wait(h)
        record('587-does-not-revoke-current-result-or-data',stage=stage)
    for oldoption in (0,1):
        h=new();h.receive601(option=oldoption)
        if not oldoption:h.mark()
        base=Harness(src,r);assert h.phase_clear()==base.phase_clear();empty(h)
        h.stage3();wait(h);h.receive601(phase=1);wait(h);h.mark();passed(h)
        record('native-phase-clear-same-pointers-revokes-old-result',oldoption=oldoption)
    for oldoption in (0,1):
        h=new();h.receive601(option=oldoption)
        if not oldoption:h.mark()
        base=Harness(src,r);assert h.leave()==base.leave();empty(h)
        assert h.word(RM+4)==0xffffffff and h.word(RM+8)==0
        h.stage3();passed(h)
        h.put(RM+4,1234);h.put(RM+8,RAID);h.stage3();wait(h)
        h.receive601(option=1);passed(h)
        record('native-leave-and-reused-manager-do-not-retain-result',oldoption=oldoption)
    h=new();h.receive601();h.mark();before=bytes(h.u.mem_read(h.data,44));h.leave(RM+0x100)
    assert before==bytes(h.u.mem_read(h.data,44));record('unrelated-manager-leave-preserves-current-result')
    for option in (0,1):
        h=new();h.receive601();h.mark();h.receive601(phase=1,option=option);assert h.word(h.data+16)==0
        if option:passed(h)
        else:wait(h);h.mark();passed(h)
        record('new-result-revokes-old-freshness',option=option)
    for kind,count in [(0,1),(1,1),(2,1),(4,1),(3,0)]:
        h=new();h.receive601();h.mark(kind,count);assert h.word(h.data+16)==0;wait(h)
        record('only-nonempty-team-packet-makes-ready',kind=kind,count=count)
    for count in (1,2,4,20):
        h=new();h.receive601();h.mark(count=count)
        assert h.completed==count and h.word(h.data+16)==MODEL
        passed(h);record('whole-native-block-loop-completes-before-ready',count=count)
    h=new();h.mark();assert h.word(h.data+16)==0;h.receive601();wait(h)
    record('kind3-before-result-cannot-make-ready')
    h=new();h.receive601();h.mark();h.row=False;wait(h);record('missing-row-cannot-open')
    h=new();h.receive601();h.mark();h.put(h.data+16,MODEL+0x80);wait(h);record('different-model-cannot-open')
    for reason,addr,value in [('key',RM+4,4321),('raid',RM+8,RAID+0x100),('manager',0x56A3778,RM+0x100)]:
        h=new();h.receive601();h.mark()
        if reason=='manager':h.put(RM+0x104,4321);h.put(RM+0x108,RAID)
        h.put(addr,value);wait(h);record('valid-new-Anton-identity-waits-for-own-result',reason=reason)
    for reason,addr,value in [('left',RM+4,0xffffffff),('cache-null',RM+8,0),('manager-null',0x56A3778,0),('othermode',RAID+0x24,1),('fiendwar',RAID+0x24,2),('outer-replaced',UI+0xC3CC,OUTER+0x800),('othermodel',MODEL+4,2),('skip',OUTER+0x428,1)]:
        h=new();h.put(addr,value);passed(h);record('outside-specific-Anton-gate-passthrough',reason=reason)
    h=new();passed(h,5);record('native-skip-state5-passthrough')
    for cached,lookup,expected in [(RAID,RAID,1),(0,RAID,1),(0,0,0)]:
        h=new();h.put(RM+8,cached);h.lookup_result=lookup;h.receive601();assert h.word(h.data+40)==expected
        if expected:wait(h);h.mark();passed(h)
        else:
            passed(h);h.stage3();h.put(RM+8,RAID);wait(h)
        record('native-result-getter-hit-miss-recovery',cached=bool(cached),lookup=bool(lookup))
    for mode in (0,1,2):
        def new_mode():
            h=new();h.put(RAID+0x24,mode);h.put(MODEL+4,mode+1);return h
        for phase in (0,1):
            for option in (0,1):
                h=new_mode();wait(h);h.receive601(phase=phase,option=option)
                assert h.word(h.data+12)==mode+1
                if option:passed(h)
                else:wait(h);h.mark(count=20);passed(h)
                record('shared-mode-current-result-and-whole-team',mode=mode,phase=phase,option=option)
        for clear_name in ('phase_clear','zero_clear'):
            for option in (0,1):
                h=new_mode();h.receive601(option=option)
                if not option:h.mark()
                base=Harness(src,r)
                assert getattr(h,clear_name)()==getattr(base,clear_name)()
                empty(h);h.stage3();wait(h);h.receive601();wait(h);h.mark();passed(h)
                record('both-native-clear-paths-revoke-each-mode',mode=mode,clear=clear_name,option=option)
        h=new_mode();h.receive601();h.mark();h.row=False;wait(h)
        record('shared-missing-team-row-waits',mode=mode)
        for kind,count in ((1,1),(3,0)):
            h=new_mode();h.receive601();h.mark(kind,count);wait(h)
            record('shared-party-or-empty-packet-is-not-real-team',mode=mode,kind=kind,count=count)
        h=new_mode();h.receive601();h.mark();before=bytes(h.u.mem_read(h.data,44));h.reset()
        assert before==bytes(h.u.mem_read(h.data,44));h.stage3();passed(h)
        record('shared-repeated-open-preserves-received-team',mode=mode)
        h=new_mode();h.receive601();h.mark();newmode=(mode+1)%3
        h.put(RAID+0x24,newmode);h.put(MODEL+4,newmode+1);wait(h)
        h.receive601();wait(h);h.mark();passed(h)
        record('same-pointers-different-mode-require-new-result',mode=mode,nextmode=newmode)
        h=new_mode();h.put(OUTER+0x428,1);passed(h,5)
        record('shared-native-skip-state5-unchanged',mode=mode)
    path=a.candidate_dir/'verify.json';assert not path.exists()
    path.write_text(json.dumps(dict(passed=len(results),cases=results,pe=pe,sha256=r['output_sha256'],available_before=available,deployed=False,limitations=['Early UI open is a fixture, not proof that591 opens it','UI/STL/container leaves are fixtures; no renderer or game process','Lifecycle coverage is network587state0/state2 and native1A64B80; other reconnect paths need review']),indent=2)+'\n',encoding='utf-8')
    print(json.dumps(dict(passed=len(results),sha256=r['output_sha256'],receipt=str(path))))

if __name__=='__main__':main()
