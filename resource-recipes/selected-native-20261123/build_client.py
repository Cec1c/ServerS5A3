"""Build the selected native fixes on the complete public 2026-11-22 client."""
import argparse,hashlib,json
from pathlib import Path
import pefile
import pickup,slider,sprites,atlas_import

def build(data):
    original=pefile.PE(data=data,fast_load=True)
    original.parse_data_directories(directories=[1])
    out,pickup_info=pickup.build(data)
    out,symbols=slider.build(out)
    out=sprites.build(out)
    out=atlas_import.build(out)
    final=pefile.PE(data=out,fast_load=True)
    final.parse_data_directories(directories=[1])
    def imports(p):return [(m.dll,[(i.name,i.ordinal,i.address) for i in m.imports]) for m in p.DIRECTORY_ENTRY_IMPORT]
    assert imports(final)[:-1]==imports(original)
    assert final.DIRECTORY_ENTRY_IMPORT[-1].dll==b'AtlasWorkers101.dll'
    assert final.verify_checksum()
    return out,dict(input=pickup.INPUT_SHA256,exe=hashlib.sha256(out).hexdigest(),pickup=pickup_info,symbols=symbols,gameplayVerified=False)

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',type=Path,required=True);p.add_argument('--out',type=Path,required=True)
    a=p.parse_args();out,info=build(a.input.read_bytes())
    with a.out.open('xb') as f:f.write(out)
    a.out.with_suffix('.json').write_text(json.dumps(info,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(info))
