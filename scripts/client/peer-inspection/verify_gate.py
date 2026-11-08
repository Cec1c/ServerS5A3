"""Execute all u16 receive IDs, checking that only class1/1497 changes."""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import sys

def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    value=importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value

def verify(baseline,candidate):
    root=Path(__file__).resolve().parents[1]
    builder=module('host_builder',Path(__file__).with_name('build_candidate.py'))
    actual=module('native_gate',root/'r195-receivers/verify_receivers.py')
    rebuilt,receipt=builder.build(baseline)
    assert rebuilt==candidate, 'candidate differs from deterministic recipe'
    changes=[]
    for cls in (0,1,2,255):
        before=actual.native(baseline,(cls,),range(65536))
        after=actual.native(candidate,(cls,),range(65536))
        delta=[(cls,op) for op,(a,b) in enumerate(zip(before,after)) if a!=b]
        assert delta==([(1,1497)] if cls==1 else []),delta
        if cls==1:
            assert (before[1497],after[1497])==(0,1)
        changes.extend(delta)
    return dict(passed=True,inputSha256=hashlib.sha256(baseline).hexdigest(),
        outputSha256=hashlib.sha256(candidate).hexdigest(),changedPairs=changes,
        casesPerImage=4*65536,deterministicRecipe=True,fullClientAcceptance=False)

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline',type=Path,required=True)
    parser.add_argument('--candidate',type=Path,required=True)
    parser.add_argument('--python-deps',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    sys.path.insert(0,str(args.python_deps))
    result=verify(args.baseline.read_bytes(),args.candidate.read_bytes())
    with args.output.open('x',encoding='utf8') as stream:
        json.dump(result,stream,indent=2)
    print(json.dumps(result))
