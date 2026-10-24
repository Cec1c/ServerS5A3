"""Bounded native HUD portrait create/reuse/reset/recreate integration.

The model allocation, resource constructors, virtual render/destructor leaves,
protected identity values and registry callbacks remain fixtures. The actual
portrait dispatcher, factory, actor model writer, registration guard and reset
execute together. This does not load real fashion resources or run the GUI.
"""
import hashlib
import itertools
import json
import struct
import sys

sys.dont_write_bytecode = True
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
from build_candidate import DRAW, PE
from verify_draw import parse_args, load_inputs

WINDOWS = {0x2974890:0x19, 0x29674E0:0xC3, 0x2965BD0:0x260,
           0x29658D0:0xE0, 0x2965750:0x17D, 0x6EC0200:41}
ACTOR,MANAGER,SCENE,STAT,DESC,ROW,REGISTRY,AVT,MVT,META,OTHER = range(0x100000,0x10B000,0x1000)
MODELS = (0x120000,0x122000,0x124000)
FRAME,STACK,STOP = 0x208000,0x206000,0x7000000
LOAD,METADATA,RENDER,DTOR = 0x301000,0x301100,0x301200,0x301300


def run(pe, receipt, d, existing, descriptor, ui, remote, active_other):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set();events=[];allocations=[];draws=[];destroys=[]
    def put(a,b):
        for p in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if p not in pages:u.mem_map(p,4096);pages.add(p)
        u.mem_write(a,b)
    def w(a,x):put(a,struct.pack('<I',x&0xffffffff))
    def r(a):return int.from_bytes(u.mem_read(a,4),'little')
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
        u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
        for reg,val in ((UC_X86_REG_EAX,value),(UC_X86_REG_ECX,0xCCCCDDDD),(UC_X86_REG_EDX,0xEEEEFFFF)):u.reg_write(reg,val)
    put(0,bytes(4096));put(ACTOR,bytes(0xB000));put(MODELS[0],bytes(0x6000))
    put(STACK-0x5000,bytes(0xB000));put(STOP,b'\xcc')
    for a,n in WINDOWS.items():put(a,pe.read(a,n))
    put(d['site'],pe.read(d['site'],6))
    helper=next(h for h in receipt['helpers'] if int(h['address'],16)==d['entry'])
    put(d['entry'],pe.read(d['entry'],helper['size']))
    hooks=(0x37F2170,0x26BD1E0,LOAD,METADATA,RENDER,DTOR,
           0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010,
           0x37DF550,0x37DF5C0,0x37DF630,0x37DF6A0,0x37DF7F0,
           0x269E170,0x26A1440,0x269E700,0x26CFB40,0x352F8B0,
           0x2964340,0x296D730,0x2971210,0x32B4680,
           0x2047360,0x2046E80,0x2046F50,0x2884460,0x1338D30,
           0x1338CA0,0xB27940,0x2879C90,0x12FB5E0,
           d['next'],d['done'])
    for a in hooks:put(a,b'\xcc')
    w(0x55B3BDC,0x12345678);w(0x56A3768,MANAGER);w(0x56A3758,SCENE)
    w(0x581A298,OTHER);w(0x57B6FB8,OTHER);w(0x57B6FB4,0);w(0x57B6FB0,0);w(0x566B114,REGISTRY)
    w(ACTOR,AVT);w(AVT+4,0x29674E0);w(ACTOR+0x20,20);w(ACTOR+0x34,0x10)
    w(ACTOR+0x4F8,STAT);w(ACTOR+0x4FC,DESC if descriptor else 0)
    for a in MODELS:w(a,MVT)
    for offset,fn in ((0,DTOR),(0x34C,LOAD),(0x38,METADATA),(0x370,RENDER)):w(MVT+offset,fn)
    w(FRAME+d['actor'],ACTOR);w(FRAME+d['slot'],3)
    if existing:w(ACTOR+0x4F4,MODELS[0])
    next_model=1 if existing else 0
    table=bytes(u.mem_read(MANAGER,4096));scene=bytes(u.mem_read(SCENE,4096))
    actor_before=bytes(u.mem_read(ACTOR,4096))
    guard=bytes(range(64));put(STACK,guard)

    def hook(_u,a,_n,_):
        nonlocal next_model
        sp=u.reg_read(UC_X86_REG_ESP);this=u.reg_read(UC_X86_REG_ECX)
        model=r(ACTOR+0x4F4)
        if a==0x37F2170:
            assert r(sp+4)==0x13CC
            selected=MODELS[next_model];next_model+=1;allocations.append(selected)
            events.append('allocate-fixture');ret(selected)
        elif a==0x26BD1E0:
            assert this==allocations[-1];events.append('constructor-fixture');ret(this)
        elif a==0x29658D0:
            assert this==ACTOR and r(sp+4)==allocations[-1]
            events.append('native-model-bind')
        elif a==LOAD:
            assert this==model and [r(sp+i*4) for i in range(1,5)]==[ACTOR+0x30,0,0,0]
            events.append('appearance-load-fixture');ret(pop=16)
        elif a==METADATA:assert this==model;ret(META)
        elif a in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):
            assert this==META+4 and r(r(sp+4))==20
            ret(pop=8)
        elif a in (0x37DF550,0x37DF5C0,0x37DF630,0x37DF6A0,0x37DF7F0):
            assert this==0x57B7218 and r(sp+4)==0x57B721C
            w(r(sp+8),19);ret(pop=8)
        elif a==0x269E170:
            assert this==model and r(sp+4)==ACTOR;ret(pop=4)
        elif a in (0x26A1440,0x269E700):assert this==model;ret()
        elif a==0x26CFB40:
            assert this==model and r(sp+4)==ACTOR+0x606;ret(pop=4)
        elif a==0x352F8B0:
            assert [r(sp+i*4) for i in range(1,5)]==[ACTOR,model,0,1];ret()
        elif a==0x2964340:assert this==ACTOR;ret(int(remote))
        elif a==0x296D730:
            assert r(sp+4)==20;events.append('guard-lookup-fixture');ret(OTHER if active_other else 0)
        elif a==0x2971210:raise AssertionError('aux portrait registered as a scene actor')
        elif a==0x32B4680:raise AssertionError('aux portrait replaced self UI')
        elif a==RENDER:
            assert this==model and model!=0
            assert [r(sp+i*4) for i in range(1,7)]==[103,204,24,24,0xFFFFFFFF,0]
            draws.append(model);events.append('render-fixture');ret(pop=24)
        elif a==0x2047360:
            assert r(sp+4)==model;ret(model,4)
        elif a==0x2046E80:assert this==model;ret(REGISTRY if ui else 0)
        elif a==0x2046F50:assert this==model;ret(REGISTRY)
        elif a==0x2884460:
            assert this==REGISTRY and r(sp+8)==model and r(sp+12)==0
            events.append('unlink-ui-fixture');ret(pop=12)
        elif a==0x1338D30:
            assert r(sp+4)==model;ret(3,4)
        elif a==0x1338CA0:
            assert r(sp+4)==3;w(ROW+0x30,model);ret(ROW,4)
        elif a==0xB27940:
            assert this==ROW and [r(sp+4),r(sp+8)]==[OTHER,1]
            events.append('detach-row-fixture');ret(pop=8)
        elif a==0x2879C90:
            assert this==REGISTRY and r(sp+4)==model;ret(1,4)
        elif a==0x12FB5E0:
            assert this==DESC and r(sp+4)==model
            events.append('unlink-description-fixture');ret(pop=4)
        elif a==DTOR:
            assert this==model and r(sp+4)==1
            destroys.append(model);events.append('destroy-fixture');ret(pop=4)
        elif a==d['next']:raise AssertionError('aux draw took original surface branch')
        elif a in (d['done'],STOP):u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)

    def invoke(reset=False):
        saved={UC_X86_REG_EBX:202,UC_X86_REG_EDI:101,UC_X86_REG_ESI:0x12345678,UC_X86_REG_EBP:FRAME}
        for reg,value in saved.items():u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_ECX,ACTOR)
        u.reg_write(UC_X86_REG_EFLAGS,0x246)
        if reset:
            w(STACK-4,STOP);u.reg_write(UC_X86_REG_ESP,STACK-4)
        else:u.reg_write(UC_X86_REG_ESP,STACK)
        try:u.emu_start(0x2965750 if reset else d['site'],0,count=5000)
        except Exception:
            print(json.dumps(dict(site=hex(d['site']),existing=existing,descriptor=descriptor,ui=ui,remote=remote,activeOther=active_other,
                reset=reset,eip=hex(u.reg_read(UC_X86_REG_EIP)),events=events)))
            raise
        assert u.reg_read(UC_X86_REG_EIP)==(STOP if reset else d['done'])
        assert u.reg_read(UC_X86_REG_ESP)==STACK and bytes(u.mem_read(STACK,64))==guard
        assert all(u.reg_read(reg)==value for reg,value in saved.items())
        if not reset:
            assert u.reg_read(UC_X86_REG_EFLAGS)==0x246
            assert r(FRAME+d['saved_y'])==204
        assert r(ACTOR+0x4F8)==STAT and r(ACTOR+0x4FC)==(DESC if descriptor else 0)
        after=bytes(u.mem_read(ACTOR,4096))
        assert after[:0x4F4]==actor_before[:0x4F4] and after[0x4F8:]==actor_before[0x4F8:]
        assert bytes(u.mem_read(MANAGER,4096))==table and bytes(u.mem_read(SCENE,4096))==scene
        assert r(0x57B6FB8)==OTHER

    invoke();first=r(ACTOR+0x4F4);assert first==MODELS[0]
    allocation_count=len(allocations)
    invoke();assert r(ACTOR+0x4F4)==first and len(allocations)==allocation_count
    invoke(True);assert r(ACTOR+0x4F4)==0 and destroys==[first]
    event_count=len(events)
    invoke(True);assert len(events)==event_count and r(ACTOR+0x4F4)==0
    invoke();second=r(ACTOR+0x4F4);assert second==MODELS[1] and second!=first
    invoke(True);assert r(ACTOR+0x4F4)==0 and destroys==[first,second]
    assert draws==[first,first,second]
    assert len(allocations)==(1 if existing else 2)
    for name in ('constructor-fixture','native-model-bind','appearance-load-fixture'):
        assert events.count(name)==len(allocations)
    assert events.count('guard-lookup-fixture')==(0 if remote else len(allocations))
    assert events.count('unlink-ui-fixture')==(2 if ui else 0)
    assert events.count('detach-row-fixture')==(2 if ui else 0)
    assert events.count('unlink-description-fixture')==(2 if descriptor else 0)
    return dict(site=hex(d['site']),existingModel=existing,descriptor=descriptor,ui=ui,
                remoteNamespace=remote,activeOther=active_other,allocations=len(allocations),
                drawModels=[hex(m) for m in draws],destroyedModels=[hex(m) for m in destroys],
                events=events,cachedDrawReused=True,secondResetNoOp=True,
                sceneAndPartyUnchanged=True,statAndDescriptorPreserved=True)


def main():
    args=parse_args()
    if args.receipt.exists():raise SystemExit('receipt already exists')
    base,candidate,receipt=load_inputs(args)
    pe=PE(candidate)
    rows=[run(pe,receipt,d,*v) for d in DRAW for v in itertools.product((False,True),repeat=5)]
    result=dict(passed=True,candidateSha256=receipt['candidateSha256'],caseCount=len(rows),cases=rows,
        windows=[dict(va=hex(a),size=n,sha256=hashlib.sha256(pe.read(a,n)).hexdigest()) for a,n in WINDOWS.items()],
        actual=['both HUD draw helpers','party slot getter','portrait dispatcher','model factory',
                'actor model writer','v159 portrait-registration guard','portrait reset'],
        fixtures=['allocator','resource constructor and appearance loader','render and destructor virtual leaves',
                  'protected local identity decoding and metadata identity setter',
                  'model initialization subcalls 269E170/26A1440/269E700/26CFB40/352F8B0',
                  'namespace predicate and active actor lookup','UI/description registry callbacks'],
        limitations=['no real fashion-resource load or GPU','no complete HUD or user interaction','not user acceptance'],
        processAccess=False,deployed=False,guiAccepted=False)
    args.receipt.parent.mkdir(parents=True,exist_ok=True)
    with args.receipt.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
    print(json.dumps(dict(passed=True,cases=len(rows),receipt=str(args.receipt))))


if __name__=='__main__':
    if not __debug__:raise SystemExit('optimized Python unsupported')
    main()
