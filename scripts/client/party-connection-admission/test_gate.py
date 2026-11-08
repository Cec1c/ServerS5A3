"""Exhaustive actual native receive-gate differential, no predicate fixtures."""
import argparse
import importlib.util
import json
from pathlib import Path
import build_candidate as b

ap=argparse.ArgumentParser();ap.add_argument('--exe',type=Path,required=True)
a=ap.parse_args();before=a.exe.read_bytes();after,receipt=b.build(before)
path=Path(__file__).resolve().parents[1]/'r195-receivers/verify_receivers.py'
spec=importlib.util.spec_from_file_location('native_gate',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
changes=[]
for cls in [0,1,2,255]:
    left=m.native(before,(cls,),range(65536));right=m.native(after,(cls,),range(65536))
    delta=[(cls,op) for op,(l,r) in enumerate(zip(left,right)) if l!=r]
    assert delta==([(1,b.OPCODE)] if cls==1 else []),delta
    changes.extend(delta)
print(json.dumps(dict(passed=True,build=receipt,casesPerImage=4*65536,changes=changes,realClientAcceptance=False),indent=2))
