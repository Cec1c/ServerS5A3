"""Reproducible x86 UI bridge and owned native test process (no game execution)."""
import argparse,hashlib,json,os,struct,subprocess
from pathlib import Path
R=Path(__file__).resolve().parents[0]
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
def main():
 p=argparse.ArgumentParser();p.add_argument('--out',type=Path,required=True);p.add_argument('--zig',type=Path,required=True);a=p.parse_args();out=a.out.resolve();out.mkdir(exist_ok=False,parents=True);zig=a.zig.resolve()
 version=subprocess.check_output([str(zig),'version'],text=True).strip();assert version=='0.13.0'
 def cc(name,source,extra):
  with (out/(name+'.log')).open('wb') as log:subprocess.run([str(zig),'cc','-target','x86-windows-gnu','-O2','-g0','-Wall','-Wextra','-Werror',str(source),*extra,'-o',str(out/name)],stdout=log,stderr=subprocess.STDOUT,check=True)
 cc('Arad.Helper.NativeUI.dll',R/'warehouse.c',['-shared','-fno-stack-protector','-luser32'])
 cc('Arad.NativeFixture.exe',R/'tests/fixture.c',[])
 fixture=out/'Arad.NativeFixture.exe';d=bytearray(fixture.read_bytes());opt=struct.unpack_from('<I',d,0x3c)[0]+24;assert struct.unpack_from('<I',d,opt+28)[0]==0x400000;flags=struct.unpack_from('<H',d,opt+70)[0];struct.pack_into('<H',d,opt+70,flags&~0x40);fixture.write_bytes(d)
 env=dict(os.environ,GOOS='windows',GOARCH='386',CGO_ENABLED='0',GOTOOLCHAIN='local');dll=sha(out/'Arad.Helper.NativeUI.dll')
 for name,extra in [('Arad.Helper.NativeUI.exe',''),('Arad.NativeFixtureBroker.exe',' -X main.expectedGameSHA='+sha(fixture)+' -X main.expectedGameName=Arad.NativeFixture.exe')]:subprocess.run(['go','build','-buildvcs=false','-trimpath','-ldflags=-s -w -buildid= -X main.expectedDLLSHA='+dll+extra,'-o',str(out/name),'.'],cwd=R,env=env,check=True)
 record=dict(architecture='x86',compiler=version,goVersion=subprocess.check_output(['go','version'],text=True).strip(),sourceFiles={str(p.relative_to(R)):sha(p) for p in R.rglob('*') if p.is_file() and '__pycache__' not in p.parts},production={n:sha(out/n) for n in ['Arad.Helper.NativeUI.exe','Arad.Helper.NativeUI.dll']},fixtureFiles={n:sha(out/n) for n in ['Arad.NativeFixture.exe','Arad.NativeFixtureBroker.exe']},gameExecuted=False)
 (out/'build.json').write_text(json.dumps(record,indent=2)+'\n');print(json.dumps(record['production']))
if __name__=='__main__':main()
