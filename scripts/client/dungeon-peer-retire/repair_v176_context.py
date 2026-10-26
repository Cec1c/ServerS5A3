"""Build exact V176 candidate for stale town-record retirement in a dungeon."""
import argparse,json,struct
from pathlib import Path
from build_candidate import PE,checksum,sha,require
import helpers

INPUT_SHA='2a6cc14f3353e3d9d142b8580e432fe76ff3c1bbdc5be6818092347f9003bc74'
INPUT_SIZE=111919104
BASE=0x6EC5000

def build(data):
    require(len(data)==INPUT_SIZE and sha(data)==INPUT_SHA,'exact V176 required')
    pe=PE(data)
    old,kind0=helpers.emit(BASE,preserve_local=True)
    new,unchanged=helpers.emit(BASE,preserve_local=True,preserve_dungeon_town=True)
    require(kind0==unchanged and len(new)<=0x200,'helper allocation differs')
    require(pe.read(BASE,0x200)==old+bytes(0x200-len(old)),'V176 helper differs')
    require(pe.read(BASE+0x200,len(kind0))==kind0,'V176 kind0 differs')
    out=bytearray(data);at=pe.offset(BASE,0x200);cs=pe.opt+64
    out[at:at+0x200]=new+bytes(0x200-len(new))
    struct.pack_into('<I',out,cs,checksum(out,cs))
    changes=[i for i,(x,y) in enumerate(zip(data,out)) if x!=y]
    require(all(at<=i<at+0x200 or cs<=i<cs+4 for i in changes),'unexpected byte delta')
    return bytes(out),dict(inputSha256=sha(data),outputSha256=sha(out),length=len(out),
                          helperVA=hex(BASE),helperSize=len(new),changedOffsets=changes,
                          gameAcceptance=False,published=False)

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('input','output','receipt'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();require(not a.output.exists() and not a.receipt.exists(),'new output required')
    output,receipt=build(a.input.read_bytes())
    a.output.write_bytes(output)
    require(sha(a.output.read_bytes())==receipt['outputSha256'],'readback differs')
    a.receipt.write_text(json.dumps(receipt,indent=2),encoding='utf8')
    print(json.dumps({k:v for k,v in receipt.items() if k!='changedOffsets'}))
