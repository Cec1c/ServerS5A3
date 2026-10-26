"""Bounded x86 execution of the candidate; no GUI/process/network access.

Real code: all three installed hooks/helpers, 32B6530, 192EAE0,
192B0D0/192B140, 1A553E0, cached 296DD80, 29614E0, 600 loop tail.
Fixtures: parsed per-block payloads, STL/model lookup leaves, UI effects.
"""
import argparse
import json
import struct
import sys
from pathlib import Path
sys.dont_write_bytecode=True
import build_readiness as b
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

STACK,FRAME=0x7008000,0x7018000
UI,RM,RAID,MODEL,OUTER,SCENE,ACTOR=0x30000000,0x30010000,0x30020000,0x30030000,0x30040000,0x30050000,0x30060000
VT,CALLBACK=0x30070000,0x30071000
REGS=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,UC_X86_REG_EFLAGS]


class Harness:
    def __init__(self,image,receipt):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32); self.pages=set(); self.events=[]
        self.image=image; self.sections=b.pe.headers(image)[1]; self.data=receipt['data_va']
        self.stop=0; self.owned_type=1; self.row=True; self.special=False; self.location=1
        self.completed=0; self.kind=3; self.count=1; self.capture=None; self.writes=[]
        self.original_entry=None
        self.mark_entry=None;self.mark_restored=False
        for va,size in [(0x192F6ED,5),(0x2001639,5),(0x202FA88,0x2A4),
                        (0x32B6530,0x60),(0x192EAE0,0x10),(0x192B0D0,0xC0),
                        (0x1A553E0,0x20),(0x296DD80,0xAA),(0x29614E0,5),
                        (0x1A64B80,0x20)]: self.native(va,size)
        if len(image)>b.INPUT_SIZE:
            self.native(receipt['code_va'],receipt['code_size'])
        self.put(self.data,bytes(4096))
        for va,sz in [(STACK-0x6000,0x9000),(FRAME-0x1000,0x2000),(UI,0x10000),
                      (RM,0x1000),(RAID,0x1000),(MODEL,0x1000),(OUTER,0x1000),
                      (SCENE,0x1000),(ACTOR,0x1000),(VT,0x2000)]: self.put(va,bytes(sz))
        for va in [0x192F0A0,0x1A55570,0x1A54FF0,0x35A8560,0x192FAE0,
                   0x192AF00,0x192AD70,0x1A55350,0x296D730,
                   0x192D960,CALLBACK,0x3E70882,0x1699900,0x32B8840,0x192EEA0]: self.put(va,b'\xc3')
        for va,value in [(0x56A3798,UI),(0x56A3778,RM),(0x56A3758,SCENE),
                         (UI+0xC3CC,OUTER),(RM+4,1234),(RM+8,RAID),
                         (RAID+0x24,0),(MODEL+4,1),(MODEL,VT),(VT,CALLBACK),
                         (SCENE+0x70,ACTOR),(ACTOR+0x20,123),(OUTER+0x420,3),
                         (FRAME-0x14,MODEL),(FRAME-0x50,MODEL)]: self.put(va,value)
        self.u.hook_add(UC_HOOK_CODE,self.step)
        self.u.hook_add(UC_HOOK_MEM_WRITE,lambda u,a,addr,size,value,_: self.writes.append((addr,size,value)))
    def put(self,va,blob):
        if isinstance(blob,int): blob=struct.pack('<I',blob & 0xffffffff)
        for page in range(va&~4095,(va+len(blob)+4095)&~4095,4096):
            if page not in self.pages: self.u.mem_map(page,4096); self.pages.add(page)
        self.u.mem_write(va,blob)
    def native(self,va,size):
        off=b.pe.offset(self.sections,va,size); self.put(va,self.image[off:off+size])
    def word(self,va):return struct.unpack('<I',self.u.mem_read(va,4))[0]
    def regs(self):return [self.u.reg_read(x) for x in REGS]
    def ret(self,value=None,argc=0):
        sp=self.u.reg_read(UC_X86_REG_ESP)
        if value is not None:self.u.reg_write(UC_X86_REG_EAX,value)
        self.u.reg_write(UC_X86_REG_EIP,self.word(sp)); self.u.reg_write(UC_X86_REG_ESP,sp+4+argc*4)
    def step(self,u,va,size,_):
        sp=u.reg_read(UC_X86_REG_ESP); ecx=u.reg_read(UC_X86_REG_ECX)
        if va==self.stop: u.emu_stop();return
        if va==0x202FCC8:self.mark_entry=self.regs()
        elif va==0x202FCCE and self.mark_entry is not None:
            expected=list(self.mark_entry);expected[2]=self.word(0x56A3798)
            assert self.regs()==expected,'mark helper must preserve all except relocated ECX load'
            self.mark_restored=True
        if va==0x192F0A0:
            self.original_entry=self.regs(); self.events.append(('state',ecx,self.word(sp+4)))
            self.put(ecx+0x420,self.word(sp+4))
            u.reg_write(UC_X86_REG_ECX,0xABCD1234);u.reg_write(UC_X86_REG_EDX,0x1122EEFF)
            u.reg_write(UC_X86_REG_EFLAGS,0x202);self.ret(0xAABBCCDD,1)
        elif va==0x32B6530:
            self.original_entry=self.regs();self.events.append(('open-wrapper',ecx))
        elif va==0x1A55570:self.ret(int(self.special))
        elif va==0x1A54FF0:self.ret(self.location)
        elif va==0x35A8560:self.events.append(('special-ui',self.word(sp+4)));self.ret(0x12345678,1)
        elif va==0x192FAE0:
            self.events.append(('open-leaf',ecx));self.put(ecx+0x420,1);self.put(ecx+0x428,b'\0')
            u.reg_write(UC_X86_REG_ECX,0x11113333);u.reg_write(UC_X86_REG_EDX,0xAAAA8888)
            u.reg_write(UC_X86_REG_EFLAGS,0x246);self.ret(0x12345566)
        elif va==0x192AF00:
            args=tuple(self.word(sp+i) for i in (4,8,12))
            assert ecx==MODEL and args[:2]==(0,123),('ownership args',hex(ecx),args)
            self.events.append(('ownership',*args))
            self.ret(0x30072000 if args[2]==self.owned_type else 0,3)
        elif va==0x192AD70:
            assert ecx==MODEL and (self.word(sp+4),self.word(sp+8))==(2,0)
            self.events.append(('row0',ecx));self.ret(0x30073000 if self.row else 0,2)
        elif va in (0x1A55350,0x296D730):raise AssertionError('unexpected lazy lookup')
        elif va==0x202FA91:
            # Decoded block fixture: real control flow resumes before native
            # D960 call; payload/string/vector parsing is not claimed here.
            assert self.word(self.data+16)==0
            self.put(FRAME-0x78,2 if self.kind==3 else 0)
            for off in (0x60,0x64,0x98,0xa0,0x6c,0x80):self.put(FRAME-off,0)
            self.put(FRAME-0x40,123)
            u.reg_write(UC_X86_REG_ECX,2 if self.kind==3 else 0)
            u.reg_write(UC_X86_REG_EIP,0x202FC26)
        elif va==0x192D960:
            assert ecx==MODEL and self.word(self.data+16)==0
            self.completed+=1;self.events.append(('block-update',self.completed));self.ret(0,2)
        elif va==CALLBACK:self.ret(0,4)
        elif va in (0x3E70882,0x1699900):self.ret(0)
        elif va==0x32B8840:
            dest=self.word(sp+4);self.put(dest,self.word(UI+0xC3CC));self.put(dest+4,0);self.ret(dest,1)
        elif va==0x192EEA0:self.events.append(('party-refresh',ecx));self.ret(0)
    def run(self,start,end,ecx=OUTER,arg=None,stack_delta=0):
        values=[0x10101010,0,ecx,0x30303030,OUTER,0,FRAME,STACK,0x246]
        for reg,val in zip(REGS,values):self.u.reg_write(reg,val)
        if arg is not None:self.put(STACK,arg)
        self.stop=end;self.original_entry=None;self.writes=[]
        before=self.regs();self.u.emu_start(start,0,count=2500)
        assert self.u.reg_read(UC_X86_REG_EIP)==end,('instruction budget',hex(self.u.reg_read(UC_X86_REG_EIP)))
        assert self.u.reg_read(UC_X86_REG_ESP)==STACK+(4 if arg is not None else 0)+stack_delta
        return before,self.regs()
    def reset(self):return self.run(0x2001639,0x200163E,UI)
    def mark(self,kind=3,count=1):
        self.kind,self.count,self.completed=kind,count,0
        self.put(FRAME-0x31,bytes([kind]));self.put(FRAME-0x32,bytes([count]))
        result=self.run(0x202FA88,0x202FD2C)
        assert self.completed==count
        assert self.mark_restored
        return result
    def gate(self,arg=4):return self.run(0x192F6ED,0x192F6F2,OUTER,arg)


def verify_pe(original,candidate,r):
    assert b.pe.sha(original)==b.INPUT_SHA
    assert b.pe.sha(candidate)==r['output_sha256']
    assert len(candidate)==len(original)+8192
    opt,sections=b.pe.headers(original)
    p=struct.unpack_from('<I',original,0x3c)[0]
    table=opt+struct.unpack_from('<H',original,p+20)[0]
    header=table+37*40
    allowed=[(p+6,p+8),(opt+4,opt+12),(opt+56,opt+60),(opt+64,opt+68),(header,header+80)]
    for name,(va,raw) in b.SITES.items():
        off=b.pe.offset(sections,va,len(raw)); assert original[off:off+len(raw)]==raw
        expected=(b'\xe9' if name=='mark' else b'\xe8')+struct.pack('<i',r['entries'][name]-va-5)+b'\x90'*(len(raw)-5)
        assert candidate[off:off+len(raw)]==expected
        allowed.append((off,off+len(raw)))
    cursor=0
    for lo,hi in sorted(allowed):assert original[cursor:lo]==candidate[cursor:lo];cursor=hi
    assert original[cursor:]==candidate[cursor:len(original)]
    assert struct.unpack_from('<H',candidate,p+6)[0]==39
    for field in (opt+4,opt+8,opt+56):
        expected=struct.unpack_from('<I',original,field)[0]+(8192 if field==opt+56 else 4096)
        assert struct.unpack_from('<I',candidate,field)[0]==expected
    code,entries=b.helpers(r['code_va'],r['data_va']);assert entries==r['entries']
    assert candidate[len(original):len(original)+len(code)]==code
    assert not any(candidate[len(original)+len(code):])
    for i,(name,size,flags) in enumerate([(b'.rdyrx',len(code),0x60000020),(b'.rdyrw',20,0xC0000040)]):
        expected=struct.pack('<8sIIIIIIHHI',name,size,len(original)+i*4096,4096,len(original)+i*4096,0,0,0,0,flags)
        assert candidate[header+i*40:header+(i+1)*40]==expected
    assert struct.unpack_from('<I',candidate,opt+64)[0]==b.pe.checksum(candidate,opt+64)
    for va,size,sha in b.ANCHORS:
        off=b.pe.offset(sections,va,size);assert b.pe.sha(original[off:off+size])==sha
    return {'allowed_ranges':sorted(allowed),'rx_section':'.rdyrx','rw_section':'.rdyrw','anchors':len(b.ANCHORS)}


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output-dir',type=Path,required=True);a=ap.parse_args()
    available=b.memory_guard();original=a.input.read_bytes()
    candidate=(a.output_dir/'DNF-r195-readiness.exe').read_bytes()
    r=json.loads((a.output_dir/'build.json').read_text())
    pe_result=verify_pe(original,candidate,r); results=[]
    def fresh():return Harness(candidate,r)
    def record(name,**kw):results.append({'case':name,**kw})

    # Exact wrapper differential against the unmodified CALL. Real 32B6530
    # executes its RET or tail-jump; only environment/UI leaves are fixtures.
    for state,special,location in [(0,False,1),(3,False,1),(4,False,1),(0,True,1),(0,True,0),(0,True,2)]:
        patched,base=fresh(),Harness(original,r)
        for h in (patched,base):h.put(OUTER+0x420,state);h.special=special;h.location=location
        pa=patched.reset(); ba=base.reset()
        assert pa==ba and patched.events==base.events
        assert patched.original_entry==base.original_entry
        assert patched.word(patched.data)==OUTER and patched.word(patched.data+16)==0
        record('native-reset-wrapper',state=state,special=special,location=location,events=patched.events)

    h=fresh();h.reset();before,after=h.gate()
    assert not any(x[0]=='state' for x in h.events)
    assert before[:7]+before[8:]==after[:7]+after[8:] and h.word(OUTER+0x420)==3
    assert all(STACK-0x1000<=addr<STACK+8 for addr,size,val in h.writes)
    record('qualified-no-kind3-waits')
    for owner in (1,2):
        for count in (1,2,4):
            h=fresh();h.owned_type=owner;h.reset();h.mark(count=count)
            assert h.word(h.data+16)==MODEL
            _,after=h.gate();assert h.events[-1]==('state',OUTER,4)
            base=Harness(original,r);base.gate()
            assert h.original_entry==base.original_entry and after==base.regs()
            record('fresh-same-model-row0-passes',ownership_type=owner,blocks=count)
    for state in (0,3,4):
        h=fresh();h.reset();h.put(OUTER+0x420,state);h.mark()
        assert h.word(h.data+16)==MODEL
        assert any(x[0]=='party-refresh' for x in h.events)==(state!=0)
        record('kind3-before-window-gate',state=state)
    for kind,count in [(0,1),(1,1),(2,1),(4,1),(3,0)]:
        h=fresh();h.reset();h.mark(kind,count);assert h.word(h.data+16)==0
        h.gate();assert h.word(OUTER+0x420)==3
        record('not-fresh',kind=kind,blocks=count)
    h=fresh();h.reset();h.mark();h.reset()
    assert h.word(h.data+16)==0;h.gate();assert h.word(OUTER+0x420)==3
    h.mark();h.gate();assert h.word(OUTER+0x420)==4
    record('second-587-same-model-stale-row-waits-until-new-kind3')
    h=fresh();h.mark();assert h.word(h.data+16)==0
    h.reset();h.gate();assert h.word(OUTER+0x420)==3
    record('reverse-600-before-587-not-accepted')
    h=fresh();h.reset();h.mark();h.row=False;h.gate();assert h.word(OUTER+0x420)==3
    record('fresh-but-row0-absent-waits')
    h=fresh();h.reset();h.put(h.data+16,MODEL+0x80);h.gate();assert h.word(OUTER+0x420)==3
    record('fresh-model-mismatch-waits')
    # Explicit unresolved reconnect: 587 -> UI advances to state3 -> tick ->
    # late kind3, with no party ownership. In-order wire does not exclude ticks.
    h=fresh();h.owned_type=0;h.put(OUTER+0x420,0);h.reset()
    assert h.word(OUTER+0x420)==1 and h.word(h.data+16)==0
    h.put(OUTER+0x420,3)  # fixture for intervening native UI progression
    h.gate();assert h.events[-1]==('state',OUTER,4)
    states_before=[x for x in h.events if x[0]=='state']
    h.mark();assert h.word(h.data+16)==MODEL and h.word(OUTER+0x420)==4
    assert [x for x in h.events if x[0]=='state']==states_before
    record('UNRESOLVED-fresh-client-team-reconnect-no-party-ownership',
           wire_order='587state4 then kind3',intervening_tick='passes original state4',
           protected_by_wait_gate=False,late_kind3='fresh marker only; no rebuild/replay')
    # Inspect only the installed marker helper, excluding existing native loop
    # writes. It must only mutate the dedicated fresh-model slot.
    h=fresh();h.reset();h.put(FRAME-0x31,b'\x03');h.put(FRAME-0x32,b'\x01')
    h.run(0x202FCC8,0x202FCCE)
    assert h.word(h.data+16)==MODEL
    assert all(STACK-0x1000<=addr<STACK or addr==h.data+16 for addr,size,val in h.writes)
    record('marker-source-object-write-exclusion')
    h=fresh();h.reset()
    assert all(STACK-0x1000<=addr<STACK or h.data<=addr<h.data+20 for addr,size,val in h.writes)
    record('reset-source-object-write-exclusion')

    # All bypass paths must reach original with exactly the original register,
    # flags and stack inputs. The stub mutates volatile outputs deliberately.
    bypass=[('arg5',None,None,5),('other-arg',None,None,6),('other-mode',MODEL+4,2,4),
            ('third-mode',MODEL+4,3,4),('raid-mode-changed',RAID+0x24,1,4),
            ('outer-state',OUTER+0x420,4,4),('skip-flag',OUTER+0x428,1,4),
            ('raid-key-changed',RM+4,5678,4),('raid-key-left',RM+4,0xffffffff,4),
            ('raid-cache-cleared',RM+8,0,4),('raid-cache-changed',RM+8,RAID+0x100,4),
            ('raid-manager-null',0x56A3778,0,4),('raid-manager-changed',0x56A3778,RM+0x100,4),
            ('ui-manager-null',0x56A3798,0,4),('outer-replaced',UI+0xC3CC,OUTER+0x800,4),
            ('local-missing',SCENE+0x70,0,4),('scene-missing',0x56A3758,0,4),
            ('model-null',FRAME-0x14,0,4),('not-armed',None,None,4),('unqualified',None,None,4)]
    for name,addr,val,arg in bypass:
        h,base=fresh(),Harness(original,r)
        if name!='not-armed':h.reset()
        if name=='unqualified':h.owned_type=0
        for x in (h,base):
            if addr is not None:x.put(addr,val)
        h.events=[]
        try:before,after=h.gate(arg);base.gate(arg)
        except Exception as exc:raise RuntimeError((name,hex(h.u.reg_read(UC_X86_REG_EIP)),h.events)) from exc
        assert h.original_entry==base.original_entry,(name,'original input')
        assert after==base.regs(),(name,'original output')
        assert h.events[-1]==('state',OUTER,arg)
        assert all(STACK-0x1000<=addr<STACK+8 for addr,size,val in h.writes)
        record('passthrough',reason=name,arg=arg)
    for name,addr,val in [('key',RM+4,456),('cache',RM+8,0),('outer',UI+0xC3CC,OUTER+0x800),('mode',MODEL+4,2)]:
        h=fresh();h.reset();h.put(addr,val);h.mark();assert h.word(h.data+16)==0
        record('mark-identity-reject',reason=name)

    # Real manager-exit identity writes, stop before unrelated cleanup leaves.
    h=fresh();h.reset();h.run(0x1A64B80,0x1A64B97,RM,stack_delta=-8)
    assert h.word(RM+4)==0xffffffff and h.word(RM+8)==0
    h.gate();assert h.events[-1]==('state',OUTER,4)
    record('native-manager-exit-prefix-releases-gate')

    result={'passed':len(results),'cases':results,'pe':pe_result,
        'input_sha256':b.INPUT_SHA,'output_sha256':r['output_sha256'],
        'protectionFloorGiB':3,'available_bytes_before':available,
        'boundaries':['UI and model/STL leaves are fixtures','per-block payload parsing is fixture',
                      'no GUI acceptance','no live process or deployment','no Go or PVF'],
        'native_anchors':[{'va':hex(va),'size':size,'sha256':sha} for va,size,sha in b.ANCHORS]}
    (a.output_dir/'verify.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
    md=Cs(CS_ARCH_X86,CS_MODE_32);off=b.pe.offset(b.pe.headers(candidate)[1],r['code_va'],r['code_size'])
    (a.output_dir/'helper.asm').write_text('\n'.join(f'{i.address:08X} {i.bytes.hex():22} {i.mnemonic} {i.op_str}' for i in md.disasm(candidate[off:off+r['code_size']],r['code_va']))+'\n',encoding='utf-8')
    print(json.dumps({'passed':len(results),'output_sha256':r['output_sha256']}))

if __name__=='__main__':main()
