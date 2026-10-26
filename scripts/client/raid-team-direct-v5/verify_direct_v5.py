"""22 bounded native checks; decoded payload, containers and UI leaves are fixtures."""
import argparse
import json
import struct
import sys
from pathlib import Path
sys.dont_write_bytecode=True
import build_direct_v5 as builder
# The builder adds only the sibling public support directory to sys.path.
import verify_readiness_v4 as v
from unicorn.x86_const import *
b=builder.v4

class Direct(v.Harness):
    def __init__(self,image,receipt,decoded=3,mode=0):
        super().__init__(image,receipt)
        self.decoded=decoded;self.direct_entry=None;self.direct_after=None
        for va,n in [(0x192F570,0xEF),(0x1930D60,0xAC),(0x190CEC0,0xD),(0x91A120,0x10D),(0x3E4EBB0,8),(0x192EEA0,0xE7),(0x190CC50,0x36),(0x192EC90,0x20B),(0x26D2B60,3)]:self.native(va,n)
        for va in (0x769AD0,0x243BD90,0x192C1C0,0x192B480,0x37E3940,0x37E3970,0x37DF630,0x192F740,0x3A01CE0,0x8CFE80,0x8ED470,0x190C6D0,0xF11F90):self.put(va,b'\xc3')
        self.put(0,bytes(4096));self.put(0x55B3BDC,0x12345678)
        self.put(v.OUTER+0x420,2);self.put(v.RAID+0x24,mode);self.put(v.MODEL+4,mode+1)
        for offset in (0x3BC,0x3D4,0x3EC):self.put(v.OUTER+offset,0x30090000)
        self.put(0x30090000,bytes(4096));self.put(0x300903F4,2)
        self.put(0x30091000,bytes(4096));self.put(v.OUTER+0x3C4,0x30091000)
        self.put(v.OUTER+0x3CC,0x30092000)
    def step(self,u,va,size,arg):
        if va==self.stop:u.emu_stop();return
        if va==0x192F60D:self.direct_entry=self.regs()
        if va==0x192F612:self.direct_after=self.regs()
        if va==0x192EEA0:self.events.append(('party-refresh',v.OUTER));return
        if va in (0x3A01CE0,0x8CFE80):self.ret(1);return
        if va==0x8ED470:self.ret(0);return
        if va==0x190C6D0:
            assert u.reg_read(UC_X86_REG_ECX)==0x30091000
            self.events.append(('party-state',self.word(u.reg_read(UC_X86_REG_ESP)+4)));self.ret(0,1);return
        if va==0x26D2B60:self.events.append(('native-team-notify-noop',));return
        if va==0xF11F90:self.ret(0);return
        if va in (0x769AD0,0x243BD90,0x37E3940,0x37E3970):self.ret(0);return
        if va==0x192C1C0:
            self.dest=self.word(u.reg_read(UC_X86_REG_ESP)+4);self.ret(v.MODEL,1);return
        if va==0x192B480:
            self.put(self.dest,v.MODEL);self.put(self.dest+4,0);self.ret(self.dest);return
        if va==0x37DF630:
            sp=u.reg_read(UC_X86_REG_ESP)
            assert u.reg_read(UC_X86_REG_ECX)==0x56A1E98 and self.word(sp+4)==0x56A1E9C
            self.put(self.word(sp+8),self.decoded);self.events.append(('decoded-global',self.decoded));self.ret(0,2);return
        if va==0x192F740:self.events.append(('state4-tick',));self.ret(0);return
        super().step(u,va,size,arg)
    def tick(self):
        self.direct_entry=self.direct_after=None
        self.run(0x1930D60,0x192F4C0)
    def team_notify(self):
        # Actual category2 callback callee, four args; decoded record is a fixture.
        va=0x300A0000
        code=bytes.fromhex('6a006a006a7b6a02b9')+struct.pack('<I',v.OUTER)
        code+=b'\xe8'+struct.pack('<i',0x192EC90-(va+len(code)+5))
        self.put(va,code);self.run(va,va+len(code))
    def check_wait_abi(self):
        a,c=self.direct_entry,self.direct_after
        assert a is not None and c is not None
        assert a[:7]+a[8:]==c[:7]+c[8:] and c[7]==a[7]+4
        assert self.word(v.OUTER+0x420)==2

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True,help='exact original v156 image')
    parser.add_argument('--candidate-dir',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True,help='new JSON report file')
    args=parser.parse_args()
    assert not args.output.exists(), 'report must not exist'
    available=b.old.memory_guard()
    src=args.input.read_bytes()
    out=(args.candidate_dir/'candidate.exe').read_bytes()
    meta=json.loads((args.candidate_dir/'build.json').read_text(encoding='utf8'))
    assert b.pe.sha(src)==builder.INPUT_SHA
    assert b.pe.sha(out)==builder.OUTPUT_SHA==meta['output_sha256']
    sec=b.pe.headers(out)[1]
    def raw(image,va,n):
        off=b.pe.offset(sec,va,n)
        return image[off:off+n]
    cases=[]
    def record(name,**kw):cases.append(dict(case=name,**kw))
    # Independent output boundaries/checksum/call targets, no candidate writes.
    last=0
    for lo,hi in meta['changed_ranges']:assert src[last:lo]==out[last:lo];last=hi
    assert src[last:]==out[last:] and len(src)==len(out)
    opt,_=b.pe.headers(out)
    assert struct.unpack_from('<I',out,opt+64)[0]==b.pe.checksum(out,opt+64)
    for name,(va,oldraw) in b.SITES.items():
        assert raw(out,va,len(oldraw))==(b'\xe8' if name=='gate' else b'\xe9')+struct.pack('<i',meta['entries'][name]-va-5)+b'\x90'*(len(oldraw)-5)
    assert raw(out,0x192F60D,5)==b'\xe8'+struct.pack('<i',meta['entries']['gate']-0x192F612)
    refs=[dict(va=hex(va)) for va in (0x192F53F,0x192F60D,0x192F6ED,0x192F7C0,0x192F8EE,0x192F9BE,0x192FB07,0x192FB3B,0x192FBBB,0x192FBEB)]
    argsets=[ [5],[3,4],[4,5],[5],[6],[2],[1],[5],[0],[0] ]
    xrefs=[]
    for ref,states in zip(refs,argsets):
        va=int(ref['va'],16);target=va+5+struct.unpack('<i',raw(out,va+1,4))[0]
        assert target==(meta['entries']['gate'] if va in (0x192F60D,0x192F6ED) else 0x192F0A0)
        xrefs.append(dict(va=hex(va),argument_set=states,target=hex(target)))
    # Old v156 direct bypass with real completion getter and protected wrapper.
    oldmeta=builder.baseline_metadata()
    h=Direct(src,oldmeta);h.receive601();h.tick();assert h.word(v.OUTER+0x420)==4
    record('v156-direct-bypass-real-predicate-wrappers')
    for disposition in ('unknown','eligible','denied','invalid'):
        h=Direct(out,meta)
        if disposition!='unknown':h.receive601(result=int(disposition=='invalid'),option=int(disposition=='denied'))
        h.tick()
        if disposition in ('unknown','eligible'):
            h.check_wait_abi();h.tick();h.check_wait_abi()
            if disposition=='unknown':h.receive601()
            h.mark(count=2);assert h.word(v.OUTER+0x420)==2
            assert ('party-refresh',v.OUTER) in h.events
            h.tick();assert h.word(v.OUTER+0x420)==4
            h.tick();assert sum(e[0]=='state' and e[2]==4 for e in h.events)==1
        else:assert h.word(v.OUTER+0x420)==4
        record('direct-disposition-and-retry',disposition=disposition)
    for value in (0,1,2,4):
        h=Direct(out,meta,decoded=value);h.tick();assert h.word(v.OUTER+0x420)==3
        record('native-protected-global-other-value-keeps-state3',decoded=value)
    for mode in (1,2):
        h=Direct(out,meta,mode=mode);h.tick();assert h.word(v.OUTER+0x420)==3
        assert not any(e[0]=='decoded-global' for e in h.events)
        record('other-raid-mode-never-enters-direct-shortcut',mode=mode)
    h=Direct(out,meta);h.put(0x300903F4,1);h.tick();assert h.word(v.OUTER+0x420)==2 and h.direct_entry is None
    record('intro-not-finished-no-gate-call')
    for why in ('emptykind3','party','missingrow','identity','clear','kind3-before601'):
        h=Direct(out,meta)
        if why=='kind3-before601':h.mark();h.receive601()
        else:
            h.receive601();h.mark(kind=1 if why=='party' else 3,count=0 if why=='emptykind3' else 1)
            if why=='missingrow':h.row=False
            if why=='identity':h.put(v.RM+4,4321)
            if why=='clear':h.phase_clear()
        h.tick();h.check_wait_abi();record('unready-direct-remains-state2',reason=why)
    # Passing call must preserve incoming setter ABI exactly, including flags.
    for decoded in (2,3):
        hs=[Direct(src,oldmeta,decoded),Direct(out,meta,decoded)]
        for h in hs:h.receive601();h.mark();h.tick()
        assert hs[0].original_entry==hs[1].original_entry and hs[0].direct_after==hs[1].direct_after
        record('baseline-setter-abi-exact',decoded=decoded)
    for image,receipt,label in [(src,oldmeta,'v156'),(out,meta,'candidate')]:
        h=Direct(image,receipt);h.receive601();h.tick()
        early=h.word(v.OUTER+0x420)
        assert early==(4 if label=='v156' else 2)
        n=sum(e[0]=='state' for e in h.events)
        h.mark(count=2);h.team_notify()
        assert h.word(v.OUTER+0x420)==early and sum(e[0]=='state' for e in h.events)==n
        assert ('native-team-notify-noop',) in h.events and ('party-state',3) in h.events
        assert h.word(0x300903F4)==2
        h.tick();assert h.word(v.OUTER+0x420)==4
        record('late-kind3-real-party-refresh-and-team-notify',image=label,outer_before_packet=early,callback_rebuild=False)
    receipt=dict(sha256=meta['output_sha256'],passed=len(cases),cases=cases,available_before=available,source_candidate_unchanged=True,
        native=['1930D60 state2 dispatch','192F570 full prologue/body/epilogue','190CEC0 completion getter','91A120 protected-global wrapper/equality','candidate hooks','601 parser window','600 decoded-block loop/tail','192EEA0/190CC50 party refresh','192EC90 category2 callback/26D2B60 RET16'],
        fixtures=['model/shared_ptr acquisition leaves','decoded protected-global value and mutex leaves','outer setter/UI effects','state4 tick leaf','600 per-block payload/STL/model update and row lookup','party visibility/row completion and party state setter leaves','stop before common192F4C0 validation tail'],
        limits=['No live flag capture','No renderer/network/process','Existing 10 direct setter xrefs only; indirect calls/arbitrary field stores not globally proven'])
    receipt['setter_xrefs']=xrefs
    args.output.parent.mkdir(parents=True,exist_ok=True)
    args.output.write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf8')
    print(json.dumps(dict(passed=len(cases),sha256=meta['output_sha256'],receipt=str(args.output))))

if __name__=='__main__':main()
