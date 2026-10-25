"""Read back the complete candidate and exercise its emitted native helpers."""
from pathlib import Path
import argparse
import itertools
import json
import sys

sys.dont_write_bytecode=True
import build_candidate as build
import skill_default as skill
import verify_skill_default as skill_verify
import verify_profile_refresh as profile_verify

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--baseline',type=Path,required=True);ap.add_argument('--candidate',type=Path,required=True);ap.add_argument('--output',type=Path,required=True)
    a=ap.parse_args()
    if a.output.exists():raise ValueError('verification output exists')
    baseline=a.baseline.read_bytes();candidate=a.candidate.read_bytes()
    expected,receipt=build.build(baseline)
    if candidate!=expected:raise ValueError('candidate differs from complete reconstruction')
    pe=build.peutils.PE(candidate);before=build.peutils.PE(baseline)
    mapping=build.module('readonly_image_probe',build.ROOT/'quiet-profile-refresh/verify_candidate.py')
    mapped=mapping.probe_image(a.candidate)
    if not mapped['mappingSucceeded']:raise ValueError('Windows image mapping failed')
    results=[]
    h=receipt['helpers']['skill']
    for patched in (False,True):
        for command in ([],[1,4],[3,4],[12,4]):
            for held,actions in (([],[]),([],[3,4]),([3],[4]),([],[0,1,2,4]),([0,3],[4])):
                results.append(skill_verify.run(pe.read if patched else before.read,patched,command,held,actions,
                    helper_address=h['address'],helper_bytes=pe.read(h['address'],h['size']) if patched else None))
    profile_cases=[]
    for kind in ('detail','quiet'):
        h=receipt['helpers'][kind]
        for bits in itertools.product((False,True),repeat=8):
            profile_cases.append(profile_verify.case(pe.read,kind,*bits,helper_address=h['address'],helper_bytes=pe.read(h['address'],h['size'])))
    h=receipt['helpers']['lookup']
    for scene,key,owner,route in itertools.product((False,True),repeat=4):
        profile_cases.append(profile_verify.case(pe.read,'lookup',True,False,key,owner,route,scene,True,helper_address=h['address'],helper_bytes=pe.read(h['address'],h['size'])))
    title=build.module('title_verify',build.ROOT/'title-request/verify_candidate.py')
    image=title.Image.from_bytes(candidate);title_cases=[]
    for ret,parents in title.TITLE_PAREMS.items():
        for parent in parents:
            for slot,count in ((13,1),(12,1),(13,2)):
                row=title.run_case(image,ret,parent,slot,count)
                want=2 if slot==13 and count==1 else 0
                if row!=want:raise ValueError('title request marker mismatch')
                title_cases.append(dict(returnAddress=ret,parent=parent,slot=slot,count=count,marker=row))
        if ret!=0x12AF918:
            if title.run_case(image,ret,0xA5A50000)!=0:raise ValueError('wrong title parent accepted')
    for ret in title.SIMPLE_EXISTING:
        if title.run_case(image,ret,0xA5A50000)!=2:raise ValueError('ordinary manual wear changed')
    if title.run_case(image,title.UNKNOWN_CALLER,0xA5A50000)!=0:raise ValueError('automatic reassert widened')
    report=dict(passed=True,outputSha256=build.SHA(candidate),fullReconstruction=True,windowsImageMapping=mapped,
                skillCases=results,profileCases=profile_cases,titleCases=title_cases,
                scope='Actual emitted helpers and fixed native serializer/vector reader; selected actor/UI/allocator leaves are bounded fixtures. GUI acceptance remains separate.',guiAccepted=False)
    a.output.write_text(json.dumps(report,indent=2),encoding='utf8')
    print(json.dumps(dict(passed=True,sha256=report['outputSha256'],skillCases=len(results),profileCases=len(profile_cases),titleCases=len(title_cases))))

if __name__=='__main__':main()
