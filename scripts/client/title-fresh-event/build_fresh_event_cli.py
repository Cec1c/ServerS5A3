"""Build only an internal unprotected exact-input title candidate."""
from pathlib import Path
import argparse,json
from build_fresh_event import build
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',required=True,type=Path);p.add_argument('--output',required=True,type=Path);a=p.parse_args()
 receipt=a.output.with_suffix(a.output.suffix+'.json')
 if a.output.exists() or receipt.exists():p.error('output and receipt must be new files')
 if a.input.resolve()==a.output.resolve():p.error('input must not be overwritten')
 data,result=build(a.input.read_bytes())
 with a.output.open('xb') as f:f.write(data)
 with receipt.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
 print(json.dumps(dict(outputSha256=result['outputSha256'],outputSize=result['outputSize'],antiREApplied=False,published=False)))
if __name__=='__main__':main()
