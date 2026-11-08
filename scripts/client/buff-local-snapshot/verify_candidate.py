"""Execute the snapshot receiver and native actor-reset with explicit leaf fixtures."""
import argparse
import itertools
import json
import struct
from pathlib import Path
import build_candidate as builder
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *


def replay(data, peer_count, selected, *, existing=True, repeats=1, local=True, patched=True, ui=False):
    pe = builder.peutils.PE(data)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    pages = set()
    def put(a,b):
        for page in range(a & ~4095, (a+len(b)+4095) & ~4095, 4096):
            if page not in pages:
                u.mem_map(page,4096); pages.add(page)
        u.mem_write(a,b)
    def w(a,v): put(a,struct.pack('<I',v))
    def r(a): return struct.unpack('<I',u.mem_read(a,4))[0]
    stack, manager, actor, proxy, sentinel, stop = 0x30008000,0x31000000,0x32000000,0x31001000,0x31002000,0x33000000
    put(0,bytes(4096));put(stack-4096,bytes(8192));put(manager,bytes(0x10000));put(stop,b'\xcc')
    for address,size in [(0x998890,0x100),(builder.ACTOR_RESET,0x19d),(builder.HELPER,len(builder.helper()))]:
        put(address,pe.read(address,size))
    if not patched: put(builder.SITE,builder.ORIGINAL)
    w(0x55B3BDC,0x12345678);w(0x5656C08,manager);w(builder.LOCAL_ACTOR,actor if local else 0)
    w(0x56A3798,0x34000000);w(proxy,manager)
    peers={actor+0x1000*(i+1):[14,tuple(range(i+1))] for i in range(peer_count)}
    state=dict(peers)
    if existing: state[actor]=[selected,(3,14,15,16,17,18,22)]
    before=dict(state)
    resets=[];lookups=[];events=[];row_receives=[];read_count=0
    selected_address=manager+0x4000
    def finish(clean=0,value=None):
        sp=u.reg_read(UC_X86_REG_ESP);dest=r(sp)
        u.reg_write(UC_X86_REG_ESP,sp+4+clean)
        if value is not None: u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_ECX,0x12340000);u.reg_write(UC_X86_REG_EDX,0x12340001)
        u.reg_write(UC_X86_REG_EIP,dest)
    def hook(uc,address,size,_):
        nonlocal read_count
        sp=uc.reg_read(UC_X86_REG_ESP)
        if address==stop: uc.emu_stop();return
        if address==builder.GLOBAL_RESET:
            assert uc.reg_read(UC_X86_REG_ECX)==manager
            resets.append(('global',None));state.clear();finish();return
        if address==0xAB5EC0:
            assert uc.reg_read(UC_X86_REG_ECX)==manager
            out,keyptr=r(sp+4),r(sp+8);key=r(keyptr);lookups.append(key)
            put(out,struct.pack('<III',proxy,0,manager+0x3000 if key in state else sentinel))
            finish(8,out);return
        if address==0xA8F6C0:
            assert uc.reg_read(UC_X86_REG_ECX)==manager
            out=r(sp+4);put(out,struct.pack('<III',proxy,0,sentinel));finish(4,out);return
        if address==0x9973E0:
            assert uc.reg_read(UC_X86_REG_ECX)==manager
            key=r(r(sp+4));assert key==actor
            resets.append(('actor',key));state.pop(key,None);finish(4);return
        if address==0x32B5C90:
            assert r(sp+4) in (0xf1,0xf2) and r(sp+8)==0
            finish(8,0x35000000 if ui else 0);return
        if address==0x3E73CB6:
            finish(0,r(sp+4));return
        if address==0xC04720:
            assert r(sp+4)==actor;events.append(actor);finish(4);return
        if address==0x37E8350:
            put(r(sp+4),struct.pack('<H',selected));read_count+=1;finish();return
        if address==0x997A10:
            key=r(r(sp+4));assert key==actor
            if key not in state: state[key]=[0,()]
            w(selected_address,state[key][0]);finish(4,selected_address);return
        if address==0x9985F0:
            # Inventory item lookup and row application are deliberately
            # fixtures; this suite verifies the complete surrounding receiver.
            assert (r(sp+4),r(sp+8))==(0x11111111,0x22222222)
            if local:
                state[actor]=[r(selected_address),(3,14,15,16,17,18,22)]
            row_receives.append(True);finish();return
    leaves=[builder.GLOBAL_RESET,0xAB5EC0,0xA8F6C0,0x9973E0,0x32B5C90,0x3E73CB6,0xC04720,0x37E8350,0x997A10,0x9985F0]
    for a in leaves: put(a,b'\xcc')
    u.hook_add(UC_HOOK_CODE,hook)
    regs=[UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP]
    initial=[0x12345601,0x12345602,0x12345603,0x12345604]
    for _ in range(repeats):
        # get-or-create returns the current selected address. Persist the
        # native receiver's previous write before another emulated receive.
        for reg,value in zip(regs,initial):u.reg_write(reg,value)
        put(stack,struct.pack('<III',stop,0x11111111,0x22222222));u.reg_write(UC_X86_REG_ESP,stack)
        u.emu_start(0x998890,0,count=5000)
        assert u.reg_read(UC_X86_REG_EIP)==stop
        assert u.reg_read(UC_X86_REG_ESP)==stack+4
        assert [u.reg_read(reg) for reg in regs]==initial, (peer_count,selected,existing,repeats,ui,[hex(u.reg_read(reg)) for reg in regs],[hex(v) for v in initial])
    assert read_count==repeats and len(row_receives)==repeats
    if patched and local:
        assert {k:v for k,v in state.items() if k!=actor}==peers
        assert state[actor][0]==selected
        assert lookups==[actor]*repeats
        assert all(kind=='actor' for kind,_ in resets)
        assert len(events)==(2*len(resets) if ui else 0)
    else:
        assert all(k not in state for k in peers)
        assert resets==[('global',None)]*repeats
    return dict(peers=peer_count,selected=selected,existing=existing,repeats=repeats,local=local,patched=patched,ui=ui,
        peerStatePreserved=all(state.get(k)==v for k,v in peers.items()),nativeActorResetExecuted=patched and local,
        resetCalls=resets)


def verify(data):
    candidate,receipt=builder.build(data)
    cases=[]
    for n,skill,exists,repeat,ui in itertools.product(range(4),(0,14,38),(False,True),(1,2),(False,True)):
        cases.append(replay(candidate,n,skill,existing=exists,repeats=repeat,ui=ui))
    for n in (1,3):
        cases.append(replay(candidate,n,14,patched=False))
        cases.append(replay(candidate,n,14,local=False))
    return dict(build=receipt,passed=len(cases),cases=cases,
        limitations=['Native snapshot receiver and actor-reset code execute; STL lookup/erase, packet reader, UI lookup, and inventory row application are explicit fixtures.',
                     'Does not establish gameplay Buff level, item ownership/destruction, or live multiplayer acceptance.'])


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',required=True,type=Path)
    parser.add_argument('--report',required=True,type=Path)
    args=parser.parse_args();assert not args.report.exists()
    result=verify(args.input.read_bytes());args.report.write_text(json.dumps(result,indent=2),'utf8')
    print(json.dumps(dict(passed=result['passed'],outputSha256=result['build']['outputSha256'],limitations=result['limitations'])))
