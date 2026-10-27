"""Run UI-only candidate lookup against the real v159 native cache routines."""
import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *

import aux_cache
from build_candidate import PE


BASE_SHA256 = "db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0"
CANDIDATE_SHA256 = "a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1"

PE_BASE = PE_NEW = None
CODE = 0
SITES = ()
WINDOWS = {}
RECEIPT = {}


def parse_args(argv=None):
    if not __debug__:
        raise SystemExit("optimized Python unsupported")
    if any(arg == "-O" or arg.startswith("-O") for arg in sys.argv[1:]):
        raise SystemExit("optimized Python unsupported")
    parser = argparse.ArgumentParser(
        description="Verify the portable HUD local lookup candidate.")
    parser.add_argument("--input", type=Path, required=True,
                        help="fixed v159 EXE (SHA-256 {})".format(BASE_SHA256))
    parser.add_argument("--candidate", type=Path, required=True,
                        help="candidate EXE (SHA-256 {})".format(CANDIDATE_SHA256))
    parser.add_argument("--build-receipt", type=Path, required=True,
                        help="candidate build receipt JSON")
    parser.add_argument("--receipt", type=Path, required=True,
                        help="new JSON receipt to create")
    return parser.parse_args(argv)


def load_inputs(args):
    for label, path in (("input", args.input),
                        ("candidate", args.candidate),
                        ("build receipt", args.build_receipt)):
        if not path.is_file():
            raise SystemExit("{} file does not exist: {}".format(label, path))
    base = args.input.read_bytes()
    candidate = args.candidate.read_bytes()
    base_sha = hashlib.sha256(base).hexdigest()
    candidate_sha = hashlib.sha256(candidate).hexdigest()
    if base_sha != BASE_SHA256:
        raise SystemExit("fixed v159 input SHA-256 mismatch: {}".format(base_sha))
    if candidate_sha != CANDIDATE_SHA256:
        raise SystemExit("candidate SHA-256 mismatch: {}".format(candidate_sha))
    receipt = json.loads(args.build_receipt.read_text(encoding="utf-8"))
    if not isinstance(receipt, dict):
        raise SystemExit("build receipt is not a JSON object")
    if receipt.get("baseSha256") != base_sha:
        raise SystemExit("build receipt baseSha256 does not match the input")
    if receipt.get("candidateSha256") != candidate_sha:
        raise SystemExit("build receipt candidateSha256 does not match the candidate")
    if receipt.get("size") is not None and receipt["size"] != len(candidate):
        raise SystemExit("build receipt size does not match the candidate")
    for key in ("helperAddress", "helperSize", "patches"):
        if key not in receipt:
            raise SystemExit("build receipt is missing {}".format(key))
    return base, candidate, receipt


def configure(base, candidate, receipt):
    global PE_BASE, PE_NEW, CODE, SITES, WINDOWS, RECEIPT
    PE_BASE, PE_NEW = PE(base), PE(candidate)
    CODE = int(receipt["helperAddress"], 16)
    SITES = [int(p["site"], 16) for p in receipt["patches"][:3]]
    WINDOWS = {**aux_cache.RANGES, 0x2974890: 0x19, 0x2973110: 0x24,
               0x2964560: 0x120, 0x2964680: 0x120, 0x29614D0: 7,
               0x296E5E0: 0x50, 0x261E440: 4, 0x221F37B: 0xBD,
               0x224A3BB: 0x71, 0x10F1D10: 7, 0x2974390: 0x84, 0x224A2C3: 0x2A}
    RECEIPT = receipt


MANAGER,SCENE,SELF,PARTY,ACTIVE,AUX,STAT,HUD,CONTEXT,MENU=range(0x100000,0x10A000,0x1000)
STACK=0x208000
STOP=0x7000000


def run(site,slot=1,variant='aux',patched=True,click=False,render=False,count_gate=False,ui_mode=0):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set();lookups=[];opens=[];finished=[]
    def put(a,b):
        for p in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if p not in pages:u.mem_map(p,4096);pages.add(p)
        u.mem_write(a,b)
    def w(a,x):put(a,struct.pack('<I',x&0xffffffff))
    def r(a):return int.from_bytes(u.mem_read(a,4),'little')
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
        # Native caller-save registers may be destroyed by each boundary.
        u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_ECX,0xCAFEBABE);u.reg_write(UC_X86_REG_EDX,0xFACEB00C)
        u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
    for a in range(MANAGER,MENU+0x1000,0x1000):put(a,bytes(4096))
    put(STACK-0x6000,bytes(0xC000));put(0,bytes(4096));put(STOP,b'\xcc')
    w(0x55B3BDC,0x12345678)
    for a,n in WINDOWS.items():put(a,(PE_NEW if patched else PE_BASE).read(a,n))
    put(CODE,PE_NEW.read(CODE,RECEIPT['helperSize']))
    if count_gate:put(CODE+0x400,PE_NEW.read(CODE+0x400,0x59))
    for p in SITES:put(p,(PE_NEW if patched else PE_BASE).read(p,5))
    # Namespace lock and obfuscated-value decode are explicit fixtures;
    # the native zero/raw namespace getters and cache search execute in full.
    fixture=[0x18C2800,0x37E3940,0x37E3970,0x37DF550,0x37DF5C0,
             0x37DF630,0x37DF6A0,0x37DF7F0,0x296DD80,0x3826970,0x3549600]
    for a in fixture:put(a,b'\xc3')
    w(0x56A3758,0 if variant=='no_scene' else SCENE);w(0x56A3768,MANAGER)
    w(SCENE+0x70,0 if variant=='no_self' else SELF)
    w(SELF+0x20,19);w(SELF+0x4C8,0 if variant=='left_party' else PARTY)
    w(SELF+0x604,0x205 if variant=='raw_namespace' else 0)
    for i in range(8):w(PARTY+0x2C+4*i,0xFFFF)
    owner=0 if variant!='party_owner' else 5
    if variant=='wrong_owner':owner=6
    if variant=='wide_owner':owner=0x105
    w(PARTY+0x70,3 if variant=='wrong_route' else 2);w(PARTY+0x74,owner)
    if variant!='self_missing':w(PARTY+0x2C+4*((slot+1)%8),19)
    key=20
    if variant=='empty_slot':key=0xFFFF
    if variant=='zero_key':key=0
    if variant=='wide_key':key=0x10014
    if 0<=slot<8:w(PARTY+0x2C+4*slot,key)
    if variant=='active' and 0<=slot<8:w(MANAGER+0x48+4*slot,ACTIVE)
    if count_gate:w(MANAGER+0x48+4*((slot+1)%8),SELF)
    for actor in (ACTIVE,AUX):
        w(actor+0x20,21 if variant=='wrong_actor_key' and actor==AUX else 20)
        w(actor+0x604,0x205)
        w(actor+0x4F8,0 if variant=='no_stats' and actor==AUX else STAT)
    if variant=='wrong_actor_owner':w(AUX+0x604,0x206)
    if variant=='wrong_actor_route':w(AUX+0x604,0x305)
    cache={aux_cache.identity(20,5,2):AUX}
    if variant=='cache_miss':cache={}
    if variant=='old_zero_owner':cache={aux_cache.identity(20,0,2):AUX}
    if variant=='wrong_cache_route':cache={aux_cache.identity(20,5,3):AUX}
    if variant=='wrong_cache_key':cache={aux_cache.identity(21,5,2):AUX}
    aux_cache.install(u,put,w,{4:cache},{a:PE_BASE.read(a,n) for a,n in aux_cache.RANGES.items()})
    w(HUD+0x3C,CONTEXT);w(CONTEXT+0x60,MENU)
    w(0x56A3798,CONTEXT);w(CONTEXT+4,ui_mode)
    controls=[0x110000+i*0x100 for i in range(8)]
    for i,c in enumerate(controls):w(HUD+0x3E0+i*8,c)
    tracked=[(a,bytes(u.mem_read(a,0x1000))) for a in range(MANAGER,MENU+0x1000,0x1000)]
    cache_ranges=[(0x5676000,0x1000)]+[(0x400000+0x2000*i,0x2000) for i in range(5)]
    cache_before=[(a,bytes(u.mem_read(a,n))) for a,n in cache_ranges]
    def hook(_u,a,_n,_):
        if a==0x18C2800:ret(int(variant=='arena'))
        elif a in (0x37E3940,0x37E3970):ret()
        elif a in (0x37DF550,0x37DF5C0,0x37DF630,0x37DF6A0,0x37DF7F0):
            sp=u.reg_read(UC_X86_REG_ESP);context=u.reg_read(UC_X86_REG_ECX)
            assert context in (0x56A1E80,0x56A1E74)
            w(r(sp+8),5 if context==0x56A1E80 else 2);ret(pop=8)
        elif a==0x296DD80:ret(SELF)
        elif a==0x3826970:ret(int(u.reg_read(UC_X86_REG_ECX)==controls[slot]))
        elif a==0x3549600:
            sp=u.reg_read(UC_X86_REG_ESP)
            args=[r(sp+4+4*i) for i in range(4)];opens.append(args)
            assert u.reg_read(UC_X86_REG_ECX)==MENU
            ret(pop=16)
        elif a==0x1A5A5E0:
            sp=u.reg_read(UC_X86_REG_ESP);lookups.append([r(sp+4+i*4) for i in range(3)])
        elif a in (0x3E738B1,0x2965BD0,0x2977870,0x2971210):
            raise AssertionError('unexpected object/scene operation '+hex(a))
        elif render and a in (0x224A409,0x224A42C):finished.append(hex(a));u.emu_stop()
        elif count_gate and a in (0x224A2ED,0x224AE7A):finished.append(hex(a));u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    for reg,val in [(UC_X86_REG_EBX,0x11110001),(UC_X86_REG_ESI,0x22220002),
                    (UC_X86_REG_EDI,0x33330003),(UC_X86_REG_EBP,STACK+0x200),
                    (UC_X86_REG_ECX,MANAGER),(UC_X86_REG_ESP,STACK)]:u.reg_write(reg,val)
    if count_gate:
        start,end=0x224A2C3,STOP
    elif click:
        u.reg_write(UC_X86_REG_ESI,HUD);start,end=0x221F37B,0x221F438
    elif render:
        w(STACK,slot);u.reg_write(UC_X86_REG_ESI,HUD);start,end=site,STOP
    else:w(STACK,slot);start,end=site,site+5
    try:
        u.emu_start(start,end,count=15000)
    except Exception:
        print(json.dumps(dict(site=hex(site),slot=slot,variant=variant,patched=patched,click=click,render=render,
                              eip=hex(u.reg_read(UC_X86_REG_EIP)),eax=hex(u.reg_read(UC_X86_REG_EAX)),
                              ecx=hex(u.reg_read(UC_X86_REG_ECX)),esp=hex(u.reg_read(UC_X86_REG_ESP)))))
        raise
    acceptable=variant in ('aux','raw_namespace','party_owner') and 0<=slot<8
    expected=ACTIVE if variant=='active' and 0<=slot<8 else AUX if patched and acceptable else 0
    if count_gate:
        member_count=2 if variant=='active' or patched and acceptable else 1
        show=member_count>1 or ui_mode>1
        assert finished==[hex(0x224A2ED if show else 0x224AE7A)],(variant,patched,ui_mode,finished)
        assert u.reg_read(UC_X86_REG_ESP)==STACK
    elif click:
        assert opens==([[expected,0,0,0]] if expected else []),(variant,slot,opens,expected)
        assert u.reg_read(UC_X86_REG_ESP)==STACK
        assert u.reg_read(UC_X86_REG_EBP)==STACK+0x200 and u.reg_read(UC_X86_REG_ESI)==HUD
    elif render:
        assert finished==[hex(0x224A42C if expected else 0x224A409)],(variant,finished,expected)
        assert u.reg_read(UC_X86_REG_ESP)==STACK+4
        assert u.reg_read(UC_X86_REG_EBP)==STACK+0x200 and u.reg_read(UC_X86_REG_ESI)==HUD
        assert u.reg_read(UC_X86_REG_EBX)==0x11110001 and u.reg_read(UC_X86_REG_EDI)==0x33330003
    else:
        assert u.reg_read(UC_X86_REG_EAX)==expected,(hex(site),variant,slot,hex(u.reg_read(UC_X86_REG_EAX)),hex(expected))
        assert u.reg_read(UC_X86_REG_ESP)==STACK+4
        for reg,val in [(UC_X86_REG_EBX,0x11110001),(UC_X86_REG_ESI,0x22220002),(UC_X86_REG_EDI,0x33330003),(UC_X86_REG_EBP,STACK+0x200)]:
            assert u.reg_read(reg)==val,(variant,'register',reg)
    for a,b in tracked:assert bytes(u.mem_read(a,len(b)))==b,(variant,hex(a),'object modified')
    for a,b in cache_before:assert bytes(u.mem_read(a,len(b)))==b,(variant,'cache modified',hex(a))
    if variant=='active':assert lookups==[],('active priority',lookups)
    if lookups:assert lookups==[[20,5,2]],(variant,lookups)
    return dict(site=hex(site),slot=slot,variant=variant,patched=patched,click=click,render=render,countGate=count_gate,uiMode=ui_mode,
                returnedActor=hex(expected),cacheLookups=len(lookups),menus=len(opens),objectsUnchanged=True)


def main(argv=None):
    args=parse_args(argv)
    if args.receipt.exists():raise SystemExit('receipt already exists: {}'.format(args.receipt))
    base,candidate,receipt=load_inputs(args)
    configure(base,candidate,receipt)
    variants=['aux','active','raw_namespace','party_owner','no_scene','no_self','left_party','self_missing',
              'empty_slot','zero_key','wide_key','wrong_owner','wide_owner','wrong_route','cache_miss',
              'old_zero_owner','wrong_cache_route','wrong_cache_key','wrong_actor_key','wrong_actor_owner',
              'wrong_actor_route','no_stats','arena']
    rows=[run(site,slot,variant) for site in SITES for slot in range(8) for variant in variants]
    rows += [run(site,slot) for site in SITES for slot in (-1,8,0xFFFFFFFF)]
    rows += [run(site,slot,variant,False) for site in SITES for slot in (0,7) for variant in ('aux','active')]
    rows += [run(SITES[0],slot,variant,patched,click=True) for slot in range(8)
             for variant in ('aux','active','left_party','cache_miss') for patched in (False,True)]
    rows += [run(SITES[1],slot,variant,patched,render=True) for slot in (0,7)
             for variant in ('aux','active','cache_miss','no_stats') for patched in (False,True)]
    if any(int(p['site'],16)==0x224A2D3 for p in RECEIPT['patches']):
        rows += [run(0x224A2D3,1,v,p,count_gate=True,ui_mode=m) for v in ('aux','active','left_party','cache_miss','no_stats','wrong_owner','old_zero_owner')
                 for p in (False,True) for m in (0,1,2)]
    result=dict(passed=True,cases=rows,baseSha256=RECEIPT['baseSha256'],candidateSha256=RECEIPT['candidateSha256'],
                windows=[dict(va=hex(a),size=n,sha256=hashlib.sha256(PE_BASE.read(a,n)).hexdigest()) for a,n in WINDOWS.items()],
                actual=['candidate helper at all 3 call sites','party slot getter','party key membership scan','native namespace getters','8 native aux cache routines','click slice','first renderer visibility/stat gate'],
                fixtures=['namespace locks and protected value decoding','arena query','click hit test','local actor getter in click gate','menu-opening sink','synthetic cache/party/stat objects'],
                limitations=['not a complete UI render','this lookup suite does not execute portrait drawing','no allocation/reset/destructor lifecycle in this suite','not user acceptance'],processAccess=False)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    with args.receipt.open('x', encoding='utf8') as handle:
        handle.write(json.dumps(result,indent=2))
    print(json.dumps(dict(passed=True,count=len(rows),output=str(args.receipt))))


if __name__=='__main__':
    if not __debug__:raise SystemExit('optimized Python unsupported')
    main()
