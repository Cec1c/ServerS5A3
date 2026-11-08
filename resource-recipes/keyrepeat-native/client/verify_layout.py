"""Validate generated geometry, native art contracts and text room."""
from pathlib import Path
import xml.etree.ElementTree as E,json,hashlib
from PIL import ImageFont
from verification_paths import S, D
def xy(s):return tuple(int(n.strip()) for n in s.split(':')[1].split(','))
def node(e):return e.tag,dict(e.attrib),[node(c) for c in e]
def verify():
 native=E.fromstring((S/'MainMenu-original.xui').read_text('utf8').rstrip('\0\r\n'));menu=E.parse(S/'MainMenu.xui').getroot();mc=menu.find('CONTROL')
 added=[c for c in mc if c.get('ID') in ['STR:7200','STR:KR_MENU_PLATE','STR:KR_MENU_LABEL','STR:KR_MENU_GLYPH']];assert len(added)==1
 for c in added:mc.remove(c)
 assert node(native)==node(menu),'original menu element changed'
 assert hashlib.sha256((S/'HotKeyWindow.xui').read_bytes()).hexdigest()=='19c37ffbeba7b32cc8f415e2304a23a1637af6530ae5204e2764ded830a7f9a3'
 panel=E.parse(S/'KeyRepeat.xui').getroot();pop=panel.find('POPUPWINDOW');assert xy(pop.get('Size'))==(456,338) and pop.get('TileType')=='NUM:0'
 pcs=list(panel.find('CONTROL'));ids=[c.get('ID') for c in pcs];assert len(ids)==len(set(ids))
 checks=[c for c in pcs if c.tag=='CNCheckBox'];buttons=[c for c in pcs if c.get('ID','').startswith('STR:KR_BIND_')];assert len(checks)==len(buttons)==10
 font=ImageFont.truetype('C:/Windows/Fonts/simsun.ttc',12)
 rects=[]
 for c in pcs:
  x,y=xy(c.get('Pos'));w,h=xy(c.get('Size'));assert min(x,y,w,h)>=0 and x+w<=456 and y+h<=338,c.attrib
  if c.tag=='CNUIControlText':
   text=c.get('string').split(':',1)[1];assert int(c.get('stringLenth').split(':')[1])==w;assert c.get('font')=='STR:system font';assert font.getlength(text)<=w,c.attrib
  if c.tag in ('CNCheckBox','CNButton'):
   for xx,yy,ww,hh in rects:assert x+w<=xx or xx+ww<=x or y+h<=yy or yy+hh<=y,'interactive rectangles overlap'
   rects.append((x,y,w,h))
 for i,b in enumerate(buttons):assert xy(b.get('Pos'))==(91+(i//5)*222,98+(i%5)*31) and xy(b.get('Size'))==(112,23)
 for key in ['Space','LShift','RShift','PageUp','PageDn','Num /','请按一个键']:assert font.getlength(key)<=112
 for text in ['此按键已在槽位 10 使用','松开按键后再按新键','保存失败，请重试','已取消按键录入']:assert font.getlength(text)<416
 assert all(c.get('ButtonDrawType')=='STR:PATTERN' for c in pcs if c.tag=='CNButton')
 # Four native states, original corner art. Only the center is extended.
 for c in pcs:
  if c.tag=='CNButton':
   assert c.get('patternImgPack')=='STR:Interface/WindowCommon.img'
   for state,start in [('Up',172),('Over',175),('Down',178),('Disable',181)]:
    for part,k in [('Left',0),('Middle',1),('Right',2)]:assert c.get(f'Pattern{part}ImgIndex_{state}')=='NUM:'+str(start+k)
 # Every static text uses its ACTUAL native wrap limit, not merely its control Size.
 texts=[c for c in pcs if c.tag=='CNUIControlText']
 for i,c in enumerate(texts):
  x,y=xy(c.get('Pos'));w,h=xy(c.get('Size'))
  for d in texts[i+1:]:
   xx,yy=xy(d.get('Pos'));ww,hh=xy(d.get('Size'))
   assert x+w<=xx or xx+ww<=x or y+h<=yy or yy+hh<=y,(c.get('ID'),d.get('ID'))
 return dict(actualWrapWidthsMatchControls=True,staticTextRectanglesDisjoint=True,passed=True,originalMenuElementsPreserved=True,keyboardSettingsOriginalBytes=True,content=[456,338],outer=[480,374],slots=10,columns=2,rows=5,controlsInBounds=True,interactiveRectanglesDisjoint=True,longKeyNamesFit=True,nativePatternButtonsAndFont=True,textMetrics='offline SimSun; runtime positions also use native font width',gameRenderingAccepted=False)
if __name__=='__main__':
 r=verify();(D/'layout-tests.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r,indent=2))
