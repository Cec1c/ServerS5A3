"""Repackage native keycap pixels without recoloring/resampling. No base NPK edits."""
from pathlib import Path
import struct,zlib,json,hashlib
import native_assets as native
import argparse
def sha(b):return hashlib.sha256(b).hexdigest()
def build(src,out,report):
 assert sha(src.read_bytes())=='fc457ccb645c8372baa5d3cc512cc159ae556c4db76be6816c1475a26512468c'
 assert not out.exists() and not report.exists()
 ent=next(e for e in native.read_index(src) if e[2].endswith('/keyoption.img'))
 info,rows,images=native.read_img(src,ent)
 records=[];pixels=[];audit=[]
 for i,frame in enumerate([7,7,7,0]):
  im=images[frame];assert im.size==(26,26)
  raw=im.tobytes('raw','BGRA');packed=zlib.compress(raw,9);y=4 if i==2 else 3
  records.append(struct.pack('<9i',16,6,26,26,len(packed),12,y,50,31));pixels.append(packed)
  audit.append(dict(frame=i,sourceFrame=frame,pixelsSha256=sha(raw),offset=[12,y],canvas=[50,31],unchangedPixels=True))
 blob=b'Neople Img File\0'+struct.pack('<4I',4*36,0,2,4)+b''.join(records)+b''.join(pixels)
 name=b'sprite/interface/privatekeyrepeat002/menu.img';name=name+bytes(256-len(name));encrypted=bytes(a^b for a,b in zip(name,native.KEY))
 header=b'NeoplePack_Bill\0'+struct.pack('<I',1)+struct.pack('<II',316,len(blob))+encrypted
 assert len(header)==284
 digest=hashlib.sha256(header[:len(header)//17*17]).digest()
 data=header+digest+blob
 out.parent.mkdir(parents=True,exist_ok=True)
 with out.open('xb') as f:f.write(data)
 check=native.read_index(out);_,newrows,newimages=native.read_img(out,check[0])
 for i,frame in enumerate([7,7,7,0]):assert newimages[i].tobytes()==images[frame].tobytes()
 receipt=dict(passed=True,source=str(src),sourceSHA256=sha(src.read_bytes()),outputSHA256=sha(data),entries=1,frames=audit,noOriginalAssetOverwritten=True)
 assert sha(data)=='3b22698ecb4cb1d066285654aefe447033eb784d33b042297b199fe9b3a1dc50'
 with report.open('x',encoding='utf8') as f:json.dump(receipt,f,ensure_ascii=False,indent=2)
 return receipt
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('source',type=Path);p.add_argument('output',type=Path);p.add_argument('report',type=Path);a=p.parse_args()
 print(json.dumps(build(a.source,a.output,a.report),ensure_ascii=False,indent=2))
