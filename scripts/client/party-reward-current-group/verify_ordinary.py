"""Run the existing native consumer matrix against the route2 candidate.

The ordinary-route fixture supplies no current raid manager. Negative controls
restore only the three original vtable entries in synthetic memory. All native
function windows and candidate helpers execute bytes from the supplied images.
"""
from pathlib import Path
import argparse,hashlib,json,struct,sys
sys.dont_write_bytecode=True
if not __debug__:raise RuntimeError('assertions required')

def main():
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input-exe',type=Path,required=True)
    ap.add_argument('--build-dir',type=Path,required=True)
    args=ap.parse_args()
    source=Path(__file__).resolve().parent.parent/'party-reward-row'
    sys.path.insert(0,str(source))
    import verify_core as core
    import verify_render_path as render
    import verify_direct_input as direct
    import verify_completion as completion
    import verify_callback as callback
    import build_candidate as builder
    receipt=json.loads((args.build_dir/'build-receipt.json').read_text(encoding='utf8'))
    candidate=args.build_dir/'DNF-v169-p33-route2-group.exe'
    base=args.input_exe.read_bytes();new=candidate.read_bytes()
    core.require(len(base)==builder.INPUT_SIZE and hashlib.sha256(base).hexdigest()==builder.INPUT_SHA256,'base mismatch')
    core.require(hashlib.sha256(new).hexdigest()==receipt['candidate']['sha256'],'candidate mismatch')
    core.BASE,core.NEW=base,new
    core.BASE_SHA,core.CANDIDATE_SHA=builder.INPUT_SHA256,receipt['candidate']['sha256']
    core.OLD_PE,core.NEW_PE=core.PE(base),core.PE(new)
    windows=dict(core.WINDOWS)
    windows.update({0x37EA920:0x99,0x1F209D0:0x3D,0x1F1F370:0xCF,0x1F20720:0x11C,
                    0x8ED470:0x2A7,0x1F19B30:0x10,0x1F1BC90:0x8A,0x1F197B0:0xB,
                    0x190CC50:0x36,0x190C900:0x64})
    for va,size in windows.items():
        core.require(core.OLD_PE.read(va,size)==core.NEW_PE.read(va,size),'native window changed')
    core.RECEIPT['candidateSha256']=core.CANDIDATE_SHA
    core.RECEIPT['helpers']=[{'address':row['va'],'size':row['size']} for row in
                            [receipt['predicate'],receipt['groupHelper'],*receipt['wrappers'].values()]]
    original=core.Machine.__init__
    def init(self,*a,**kw):
        original(self,*a,**kw)
        self.w(0x056A3778,0)
        if not self.patched:
            for va,target in ((0x0491A074,0x03829F50),(0x0491A078,0x03829190),(0x0491A0C4,0x03826930)):
                self.w(va,target)
    core.Machine.__init__=init
    output=core.create_output_dir(args.build_dir/'ordinary-native')
    results=[]
    for name,run,count in [('core',core.run_suite,429),('render',render.run_suite,26),
                           ('direct',direct.run_suite,17),('completion',completion.run_suite,120),
                           ('callback',callback.run_suite,28)]:
        result=core.run_logged(name,run,name+'.json',count,output)
        results.append(dict(name=name,passed=result['passed'],count=result['caseCount']))
    summary=dict(passed=True,caseCount=sum(r['count'] for r in results),groups=results,
                 candidateSha256=core.CANDIDATE_SHA,inputSha256=core.BASE_SHA,
                 nativeWindowHashes={hex(va):hashlib.sha256(core.NEW_PE.read(va,size)).hexdigest() for va,size in windows.items()},
                 verifierSources={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in source.glob('verify_*.py')},
                 controls='synthetic original-vtable controls; current raid manager absent',
                 limitations=['synthetic object graph and fixture leaves','no live GUI or GPU','no physical row compaction'])
    core.write_result(output/'summary.json',summary)
    print(json.dumps({k:summary[k] for k in ('passed','caseCount','candidateSha256')}),flush=True)

if __name__=='__main__':main()
