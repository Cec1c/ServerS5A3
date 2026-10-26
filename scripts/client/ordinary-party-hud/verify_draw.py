"""Execute the HUD draw helpers and actual actor portrait dispatcher."""
import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True

from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *

from build_candidate import PE,DRAW


BASE_SHA256 = "db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0"
CANDIDATE_SHA256 = "a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1"

RECEIPT = {}
PE_IMAGE = None


def parse_args(argv=None):
    if not __debug__:
        raise SystemExit("optimized Python unsupported")
    if any(arg == "-O" or arg.startswith("-O") for arg in sys.argv[1:]):
        raise SystemExit("optimized Python unsupported")
    parser = argparse.ArgumentParser(
        description="Verify the portable HUD local draw candidate.")
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
    if "helpers" not in receipt:
        raise SystemExit("build receipt is missing helpers")
    return base, candidate, receipt


ACTOR,MODEL,MANAGER,AVT,MVT,GRAPHICS,OTHER=range(0x100000,0x107000,0x1000)
FRAME,STACK=0x208000,0x206000
HUD,CONTROL=0x110000,0x112000


def case(d,variant,xy,has_model):
    u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set();events=[];exits=[];positions=[]
    phase='draw'
    def put(a,data):
        for p in range(a&~4095,(a+len(data)+4095)&~4095,4096):
            if p not in pages:u.mem_map(p,4096);pages.add(p)
        u.mem_write(a,data)
    def w(a,x):put(a,struct.pack('<I',x&0xffffffff))
    def r(a):return int.from_bytes(u.mem_read(a,4),'little')
    def ret(pop=0,value=0xAAAABBBB):
        sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
        u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
        for reg,result in ((UC_X86_REG_EAX,value),(UC_X86_REG_ECX,0xCCCCDDDD),(UC_X86_REG_EDX,0xEEEEFFFF)):u.reg_write(reg,result)
    for a in range(ACTOR,OTHER+4096,4096):put(a,bytes(4096))
    put(STACK-0x4000,bytes(0x8000))
    put(0,bytes(4096));put(HUD,bytes(0x3000));w(0x55B3BDC,0x12345678)
    w(HUD+0x10E4+3*8,CONTROL)
    for a,n in ((d['site'],6),(0x2974890,0x19),(0x29674E0,0xC3)):
        put(a,PE_IMAGE.read(a,n))
    helper=next(x for x in RECEIPT['helpers'] if int(x['address'],16)==d['entry'])
    put(d['entry'],PE_IMAGE.read(d['entry'],helper['size']))
    put(d['tail'],PE_IMAGE.read(d['tail'],0x20))
    put(0x2207430,PE_IMAGE.read(0x2207430,0x220))
    for a in (0x1FA2790,0x2525AA0,0x2526B50,0x25253D0,0x2303380,
              0x22FF610,0x296DD80,0x2961B40,0x37EA170):put(a,b'\xcc')
    for a in (d['next'],d['done'],0x2965BD0,0x301000,0x2966C60):put(a,b'\xcc')
    w(0x56A3768,MANAGER);w(0x581A298,GRAPHICS)
    w(MANAGER+0x48+4*3,ACTOR if variant=='same_scene' else OTHER if variant=='other_scene' else 0)
    w(ACTOR,AVT);w(AVT+4,0x29674E0)
    w(ACTOR+0x34,0x10);w(ACTOR+0x4F4,MODEL if has_model else 0)
    w(MODEL,MVT);w(MVT+0x370,0x301000)
    w(FRAME+d['actor'],0 if variant=='null' else ACTOR);w(FRAME+d['slot'],3)
    w(FRAME-0x690,0xA1B2C3D4);w(FRAME-0x688,0xB1C2D3E4)
    before=bytes(u.mem_read(MANAGER,4096));stack_guard=bytes(range(64));put(STACK,stack_guard)
    x,y=xy;expected=[(x+2)&0xffffffff,(y+2)&0xffffffff,24,24,1,0,255]
    def hook(_u,a,_s,_):
        sp=u.reg_read(UC_X86_REG_ESP)
        if phase=='tail':
            if a==0x2207430:
                assert [r(sp+4*i) for i in range(1,5)]==[(x+0x70)&0xffffffff,(y+2)&0xffffffff,3,ACTOR], 'status-tail coordinates'
                assert u.reg_read(UC_X86_REG_ECX)==HUD
            elif a==0x1FA2790:ret(value=OTHER)
            elif a in (0x2525AA0,0x25253D0):ret(value=0)
            elif a in (0x2526B50,0x22FF610):ret(value=1)
            elif a==0x2303380:
                assert r(sp+4)==0x969;ret(pop=4,value=OTHER)
            elif a==0x296DD80:ret(value=OTHER)
            elif a==0x2961B40:ret(value=8)
            elif a==0x37EA170:
                assert u.reg_read(UC_X86_REG_ECX)==CONTROL
                positions.append([r(sp+4),r(sp+8)])
                assert positions==[[(x+0x70)&0xffffffff,(y+2)&0xffffffff]], 'status-control position'
                u.emu_stop()
            return
        if a==0x29674E0:
            assert u.reg_read(UC_X86_REG_ECX)==ACTOR
            assert [r(sp+4*i) for i in range(1,8)]==expected
            events.append('native-portrait-dispatch')
        elif a==0x2965BD0:
            assert u.reg_read(UC_X86_REG_ECX)==ACTOR and r(sp+4)==0
            events.append('model-construction-fixture');w(ACTOR+0x4F4,MODEL);ret(4)
        elif a==0x301000:
            assert u.reg_read(UC_X86_REG_ECX)==MODEL
            assert [r(sp+4*i) for i in range(1,7)]==expected[:4]+[0xFFFFFFFF,0]
            events.append('renderer-fixture');ret(24)
        elif a==0x2966C60:raise AssertionError('unexpected static fallback')
        elif a in (d['next'],d['done']):exits.append(a);u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    saved={UC_X86_REG_EBX:y&0xffffffff,UC_X86_REG_EDI:x&0xffffffff,UC_X86_REG_ESI:0x12345678,UC_X86_REG_EBP:FRAME}
    for reg,value in saved.items():u.reg_write(reg,value)
    u.reg_write(UC_X86_REG_ESP,STACK);u.reg_write(UC_X86_REG_EFLAGS,0x246)
    u.emu_start(d['site'],0,count=5000)
    draws=variant=='aux'
    assert exits==[d['done'] if draws else d['next']]
    assert events==(['native-portrait-dispatch']+([] if has_model else ['model-construction-fixture'])+['renderer-fixture'] if draws else [])
    assert u.reg_read(UC_X86_REG_ESP)==STACK and bytes(u.mem_read(STACK,64))==stack_guard
    assert all(u.reg_read(reg)==value for reg,value in saved.items())
    assert u.reg_read(UC_X86_REG_EFLAGS)==0x246
    if not draws:assert u.reg_read(UC_X86_REG_EDX if d['reg']=='edx' else UC_X86_REG_ECX)==3
    assert bytes(u.mem_read(MANAGER,4096))==before
    for offset,old in ((-0x690,0xA1B2C3D4),(-0x688,0xB1C2D3E4)):
        expected_local=(y+2)&0xffffffff if draws and offset==d['saved_y'] else old
        assert r(FRAME+offset)==expected_local, 'status-coordinate local'
    if draws:
        # Execute the actual later caller argument setup and native status
        # control positioning path. Intervening HUD decorations are not run.
        phase='tail';u.reg_write(UC_X86_REG_ESI,HUD)
        u.emu_start(d['tail'],0,count=1000)
        assert u.reg_read(UC_X86_REG_EIP)==0x37EA170 and len(positions)==1
        assert bytes(u.mem_read(MANAGER,4096))==before
    return dict(site=hex(d['site']),variant=variant,xy=xy,existingModel=has_model,events=events,
                exit=hex(exits[0]),registersAndStackPreserved=True,partyTableUnchanged=True,
                statusCoordinatePreserved=True,statusPositions=positions)


def main(argv=None):
    global RECEIPT,PE_IMAGE
    args=parse_args(argv)
    if args.receipt.exists():raise SystemExit('receipt already exists: {}'.format(args.receipt))
    base,candidate,receipt=load_inputs(args)
    RECEIPT=receipt;PE_IMAGE=PE(candidate)
    rows=[case(d,v,xy,model) for d in DRAW for v in ('aux','same_scene','other_scene','null')
          for xy in ((101,202),(-20,-30),(0,0)) for model in (False,True)]
    result=dict(passed=True,candidateSha256=RECEIPT['candidateSha256'],cases=rows,
          actual=['two appended draw helpers','original party slot getter','29674E0 portrait dispatcher',
                  'later HUD status argument setup','2207430 through 37EA170 position call'],
          fixtures=['model constructor','renderer draw leaf','synthetic actor and UI frame',
                    'status game-context predicates and position leaf'],
          limitations=['does not execute allocation or v159 scene-registration guard in the same run',
                       'intervening HUD decorations skipped; status path stops at position leaf',
                       'not a graphical render, actual fashion-resource load, lifecycle or acceptance'],processAccess=False)
    args.receipt.parent.mkdir(parents=True,exist_ok=True)
    with args.receipt.open('x',encoding='utf8') as handle:
        handle.write(json.dumps(result,indent=2))
    print(json.dumps(dict(passed=True,cases=len(rows),output=str(args.receipt))))


if __name__=='__main__':
    if not __debug__:raise SystemExit('optimized Python unsupported')
    main()
