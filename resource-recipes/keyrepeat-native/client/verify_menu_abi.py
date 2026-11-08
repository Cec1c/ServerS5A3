"""Reproduce 002 failure with the original native rejection branch, then test 003.

Only external state getters are adapted; the native SEH prologue, rejection,
epilogue, ret 8 and compiled wrapper execute in Unicorn. No game is launched.
"""
from pathlib import Path
import json, hashlib
from verify_native import Machine
from unicorn.x86_const import UC_X86_REG_ECX, UC_X86_REG_ESP

from verification_paths import D, BASE, old002_build

def machine(rev):
    p=old002_build() if rev=='UI-20261105-002' else D/'build'
    return Machine(p/'key_repeat.payload',p/'key_repeat.map',BASE)

def native_rejection(m):
    m.put(0,bytes(4096)) # isolated x86 SEH/FS page
    m.write(0x55b3bdc,0x48a90123)
    m.put(0x1226c30,m.native(0x1226c30,0x120))
    assert m.native(0x1226d0c,3)==b'\xc2\x08\x00'
    m.mock(0x29610e0,0)
    m.mock(0x243bd80,3) # native login/no-character rejection
    m.mock(0x3e72ce4,0) # stack cookie endpoint, native restore executes
    calls=[]
    if 'menuLayout' in m.g:m.mock(m.g['menuLayout'],lambda:calls.append('layout'))
    return calls

def verify():
    old=machine('UI-20261105-002');native_rejection(old)
    try:
        old.call(old.exports['MenuShow'],(0x13579bdf,0x2468ace0),8,ecx=old.S+0x1000)
    except (RuntimeError,AssertionError) as e:
        reproduced={'error':str(e),'trace':old.trace,'stack':hex(old.u.reg_read(UC_X86_REG_ESP))}
    else:raise AssertionError('002 negative control unexpectedly passed')
    m=machine('UI-20261105-006');calls=native_rejection(m)
    assert (m.call(m.exports['MenuShow'],(0x13579bdf,0x2468ace0),8,ecx=m.S+0x1000)&255)==0
    assert calls==[], 'native denial must not touch menu controls'
    cases=[]
    for result in [0,1]:
        for args in [(0,0),(1,-1),(0x13579bdf,0x2468ace0)]:
            m=machine('UI-20261105-006');self=m.S+0x1000;seen=[];layouts=[]
            def native():
                assert m.u.reg_read(UC_X86_REG_ECX)==self
                seen.append((m.arg(),m.arg(1)))
                return 0x12345600|result # BOOL contract is AL
            m.mock(0x1226c30,native,8)

            ret=m.call(m.exports['MenuShow'],args,8,ecx=self)&255
            assert ret==result and seen==[tuple(x&0xffffffff for x in args)]
            assert layouts==[]
            cases.append({'result':result,'arguments':list(args),'passed':True})
    # Other patched callbacks also run with their own native stack contracts.
    m=machine('UI-20261105-006');seen=[];self=m.S+0x1000
    m.mock(0x1229c20,lambda:seen.append(('update',m.u.reg_read(UC_X86_REG_ECX))))

    m.call(m.exports['MenuUpdate'],ecx=self)
    assert seen==[('update',self)]
    m=machine('UI-20261105-006');seen=[]
    def event():
        seen.append((m.u.reg_read(UC_X86_REG_ECX),tuple(m.arg(i) for i in range(4))))
        return 0x71abcd25
    m.mock(0x1228760,event,16)
    assert m.call(m.exports['MenuEvent'],(1001,13,42,55),16,ecx=self)==0x71abcd25
    assert seen==[(self,(1001,13,42,55))]
    return {'passed':True,'old002FailureReproduced':reproduced,'realNativeLoginRejectionPreserved':True,
      'showArgumentsAndReturnCases':cases,'calleeSavedRegistersAndStackChecked':True,
      'updateAndUnknownEventABI':True,'payloadSha256':hashlib.sha256((D/'build/key_repeat.payload').read_bytes()).hexdigest(),
      'gameExecuted':False,'runtimeCrashAddressAvailable':False}

if __name__=='__main__':
    r=verify();(D/'menu-abi-tests.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r,indent=2))
