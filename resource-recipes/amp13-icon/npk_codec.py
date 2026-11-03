from pathlib import Path
import struct,zlib,json,hashlib
from PIL import Image,ImageDraw
D=Path(__file__).parent
KEY=('puchikon@neople dungeon and fighter '+'DNF'*73+'\0').encode()
def entries(data):
 assert data.startswith(b'NeoplePack_Bill')
 n=struct.unpack_from('<I',data,16)[0]
 for i in range(n):
  row=data[20+264*i:284+264*i];off,size=struct.unpack_from('<II',row)
  name=bytes(x^y for x,y in zip(row[8:],KEY)).split(b'\0')[0].decode()
  yield name,data[off:off+size]
def frames(blob):
 idx,keep,version,n=struct.unpack_from('<4i',blob,16);assert version==2
 records=[];pos=32;data=32+idx
 for i in range(n):
  fmt=struct.unpack_from('<i',blob,pos)[0]
  if fmt==17:
   link=struct.unpack_from('<i',blob,pos+4)[0];records.append((blob[pos:pos+8],b'',link));pos+=8
  else:
   row=struct.unpack_from('<9i',blob,pos);fmt,extra,w,h,size,x,y,mw,mh=row
   if extra==5:size=w*h*(4 if fmt==16 else 2)
   assert extra in (5,6)
   records.append((blob[pos:pos+36],blob[data:data+size],None));data+=size;pos+=36
 assert pos==32+idx and data==len(blob)
 return records
def frame_image(rows,i):
 record,blob,link=rows[i]
 if link is not None:return frame_image(rows,link)
 fmt,extra,w,h,size,x,y,mw,mh=struct.unpack('<9i',record)
 raw=zlib.decompress(blob) if extra==6 else blob
 assert fmt==16,(i,fmt)
 return Image.frombytes('RGBA',(w,h),raw,'raw','BGRA')
