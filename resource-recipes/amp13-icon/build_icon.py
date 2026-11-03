from pathlib import Path
import struct,zlib,json,hashlib
from PIL import Image,ImageDraw
import argparse
from npk_codec import entries,frames,frame_image,KEY
parser=argparse.ArgumentParser(description="Build the +13 amplification icon in a new output directory")
parser.add_argument("--output",required=True)
args=parser.parse_args()
SOURCE=Path(__file__).resolve().parent
D=Path(args.output).resolve();D.mkdir(parents=True,exist_ok=False)
# Keep the native purple amplification scroll at its original 28px scale.
# Draw a new +13 pixel inscription; do not overwrite the shared +12 frame 844.
im=Image.open(SOURCE/'source-scroll.png').convert('RGBA')
art=ImageDraw.Draw(im)
art.rectangle((1,0,27,10),fill=(18,17,23,255))
glyphs={'+':['00000','00100','00100','11111','00100','00100','00000'],
        '1':['01100','11100','01100','01100','01100','01100','11111'],
        '3':['11111','00011','00011','01110','00011','00011','11111']}
points=[]
for char,x0 in [('+',1),('1',9),('3',17)]:
 for y,row in enumerate(glyphs[char]):
  shift=(6-y)//2
  for x,v in enumerate(row):
   if v=='1':points.append((x0+x+shift,y+2))
for x,y in points:
 for dx,dy in [(1,1),(0,1),(-1,1),(1,0),(-1,0),(0,-1)]: art.point((x+dx,y+dy),fill=(36,20,7,255))
colors=[(255,255,220,255),(255,249,170,255),(255,234,121,255),(255,218,79,255),(255,202,45,255),(243,169,20,255),(206,123,5,255)]
for x,y in points:art.point((x,y),fill=colors[y-2])
im.save(D/'amp13.png')
im.resize((280,280),Image.Resampling.NEAREST).save(D/'amp13-preview.png')
raw=zlib.compress(im.tobytes('raw','BGRA'),9)
index=struct.pack('<9i',16,6,28,28,len(raw),0,0,28,28)
img=b'Neople Img File'.ljust(16,b'\0')+struct.pack('<qii',len(index),2,1)+index+raw
name='sprite/item/stackable/private_amp13/amp13.img'
nameBytes=name.encode().ljust(256,b'\0')
header=b'NeoplePack_Bill'.ljust(16,b'\0')+struct.pack('<I',1)+struct.pack('<II',316,len(img))+bytes(x^y for x,y in zip(nameBytes,KEY))
npk=header+hashlib.sha256(header[:len(header)//17*17]).digest()+img
output=D/'sprite_item_stackable_private_amp13.NPK';output.write_bytes(npk)
found=list(entries(output.read_bytes()));assert len(found)==1 and found[0][0]==name
check=frame_image(frames(found[0][1]),0);assert check.tobytes()==im.tobytes()
check.save(D/'amp13-roundtrip.png')
report={'npk':str(output),'npkSHA256':hashlib.sha256(npk).hexdigest(),'bytes':len(npk),'imgPath':name,'pvfIconPath':'Item/stackable/private_amp13/amp13.img','frame':0,'dimensions':[28,28],'alpha':True,'pixelRoundtrip':True,'sharedOriginalFrame844Untouched':True,'items':list(range(990027307,990027313)),'deployed':False,'gameAcceptance':False}
(D/'icon-build.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),'utf8');print(json.dumps(report,ensure_ascii=False))
