"""Verify the current-image native registration plus late retry opportunities.

Full network mode0/mode1 parsing, native insertion side effects and rendering
remain outside this emulator. Lifecycle events/getters are explicit fixtures.
"""
from pathlib import Path
import argparse, importlib.util, json, sys

sys.dont_write_bytecode = True
HERE=Path(__file__).resolve().parent
import build_candidate as current
base=current.base
base.build=current.build

def verify(raw):
    saved=sys.modules.get('build_candidate')
    sys.modules['build_candidate']=base
    try:
        spec=importlib.util.spec_from_file_location('current_lifecycle_validation',HERE.parent/'party-room-lifecycle-registration/verify_candidate.py')
        v=importlib.util.module_from_spec(spec);spec.loader.exec_module(v)
    finally:
        if saved is None:sys.modules.pop('build_candidate',None)
        else:sys.modules['build_candidate']=saved
    result=v.verify(raw)
    candidate,receipt=base.build(raw)
    old=v.Runner(v.pefile.PE(data=raw,fast_load=True).get_memory_mapped_image(),receipt)
    new=v.Runner(v.pefile.PE(data=candidate,fast_load=True).get_memory_mapped_image(),receipt)
    extra=[]
    for local in range(4):
        for missing in ['scene','room']:
            for ready_at in [1,2,3]:
                for runner in [old,new]:
                    options={'scene':1} if missing=='scene' else {'room':False}
                    runner.reset(local=local,models=range(4),registered=(),**options)
                    runner.add_local_membership()
                    runner.run('registered');runner.run('mode1')
                    assert len(runner.members())==1
                    for retry in [1,2,3]:
                        if retry==ready_at:runner.options.update(scene=3,room=True)
                        runner.run('mode1')
                assert len(old.members())==1 and len(new.members())==4
                assert len(new.inserts)==3 and len(set(new.inserts))==3
                extra.append(dict(local=local,lateContext=missing,readyAtRetry=ready_at,oldMissing=3,newMissing=0))
    result.update(pairedRetryCases=extra,pairedRetryCaseCount=len(extra),
                  autonomousSceneReadiness=False,clientNativeAcknowledgement=False,
                  remainingLimit='Readiness later than the bounded recovery window still needs a subsequent native event; live model/monster/skill regression pending.')
    return result

if __name__=='__main__':
    ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--input',type=Path,required=True);ap.add_argument('--output',type=Path,required=True)
    args=ap.parse_args();result=verify(args.input.read_bytes())
    with args.output.open('x',encoding='utf-8') as f:json.dump(result,f,indent=2)
    print(json.dumps({k:result[k] for k in ['passed','outputSHA256','lifecycleCaseCount','previousNOTI120Cases','pairedRetryCaseCount']}))
