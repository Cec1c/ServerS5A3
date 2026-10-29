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

    def check_title(name,ret,parent,slot=13,count=1,want=0,call_site=None):
        marker=title.run_case(image,ret,parent,slot,count,call_site=call_site)
        if marker!=want:raise ValueError(f'{name}: title marker {marker}, expected {want}')
        title_cases.append(dict(name=name,returnAddress=ret,parent=parent,slot=slot,count=count,
                                marker=marker,expectedMarker=want,actualCall=call_site))

    for call_site,parents in title.TITLE_CALL_PARENTS.items():
        # The native CALL supplies its five-byte successor as the saved return.
        # Passing call_site also exercises the real producer frame prologue.
        ret=call_site+5
        for parent in parents:
            for slot,count in ((13,1),(12,1),(13,2)):
                want=2 if slot==13 and count==1 else 0
                check_title(f'title-{ret:#x}-parent-{parent:#x}-slot-{slot}-count-{count}',
                            ret,parent,slot,count,want,call_site)
        if call_site!=0x12AF918:
            check_title(f'title-{ret:#x}-wrong-parent',ret,0xA5A50000,call_site=call_site)
    for ret in title.SIMPLE_EXISTING:
        check_title(f'existing-simple-{ret:#x}',ret,0xA5A50000,want=2)
    check_title('unknown-return',title.UNKNOWN_CALLER,0xA5A50000)
    report=dict(passed=True,outputSha256=build.SHA(candidate),fullReconstruction=True,windowsImageMapping=mapped,
                skillCases=results,profileCases=profile_cases,titleCases=title_cases,
                scope='Actual emitted helpers and fixed native serializer/vector reader; title paths execute actual CALL/prologue before the marker gate, with packet-body construction skipped and constructor/continuation ABI stubs. Selected actor/UI/allocator leaves are bounded fixtures. GUI acceptance remains separate.',guiAccepted=False)
    a.output.write_text(json.dumps(report,indent=2),encoding='utf8')
    print(json.dumps(dict(passed=True,sha256=report['outputSha256'],skillCases=len(results),profileCases=len(profile_cases),titleCases=len(title_cases))))

if __name__=='__main__':main()
