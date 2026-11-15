"""Apply the reviewed NPC shelf fix to the exact diagnostic-free formal client."""
from pathlib import Path
import argparse,hashlib,importlib.util,json

ROOT=Path(__file__).resolve().parent
BASE_SHA='6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4'
OUTPUT_SHA='28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'
def sha(raw):return hashlib.sha256(raw).hexdigest()

def build(raw):
 profile_raw=(ROOT/'profile.json').read_bytes();profile=json.loads(profile_raw)
 original=ROOT/'exact_pe_recipe.py'
 if sha(original.read_bytes())!=profile['formalization']['referenceBuilderSHA256']:
  raise ValueError('reviewed recipe implementation changed')
 if sha(raw)!=BASE_SHA or len(raw)!=112812032 or profile['input']['sha256']!=BASE_SHA:
  raise ValueError('exact diagnostic-free formal base required')
 if profile['outputs']['expected_output_sha256']!=OUTPUT_SHA:
  raise ValueError('formal output pin changed')
 spec=importlib.util.spec_from_file_location('formal_npc_shelf_reviewed_recipe',original)
 module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
 # The original exact-input build body retains every PE/header/section/import/
 # relocation/allowlist check. Only its explicitly pinned base identity changes.
 module.INPUT_SHA=BASE_SHA
 output,receipt=module.build(raw,profile)
 if sha(output)!=OUTPUT_SHA:raise ValueError('formal output differs')
 receipt.update(formalDiagnosticFree=True,testClientExcluded=True,profile_sha256=sha(profile_raw),
                wrapper_sha256=sha(Path(__file__).read_bytes()),reference_builder_sha256=sha(original.read_bytes()))
 return output,receipt

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--input',type=Path,required=True)
 parser.add_argument('--output-dir',type=Path,required=True)
 args=parser.parse_args()
 if not args.input.is_absolute() or not args.output_dir.is_absolute() or args.output_dir.exists():
  raise ValueError('absolute input and fresh output directory required')
 data,receipt=build(args.input.read_bytes())
 args.output_dir.mkdir(parents=True)
 with (args.output_dir/'DNF.exe').open('xb') as stream:stream.write(data)
 with (args.output_dir/'build-receipt.json').open('x',encoding='utf8') as stream:json.dump(receipt,stream,indent=2)
 print(json.dumps(dict(passed=True,formalDiagnosticFree=True,sha256=sha(data),bytes=len(data))))

if __name__=='__main__':main()
