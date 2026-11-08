"""Embed native XUI; keep original keyboard settings untouched."""
from pathlib import Path
import xml.etree.ElementTree as E
import json,hashlib
S=Path(__file__).parent

def label(parent,ident,text,x,y,width,color='230, 220, 190',height=16):
 return E.SubElement(parent,'CNUIControlText',dict(ID='STR:'+ident,Pos=f'VECTOR:{x}, {y}',Size=f'VECTOR:{width}, {height}',TextColor='COLOR:'+color,font='STR:system font',isBold='BOOL:false',isOutLine='BOOL:true',isSortToEachLine='BOOL:false',lineGap='NUM:0',lineNum='NUM:0',sortType='NUM:0',string='STR:'+text,stringLenth='NUM:'+str(width),IsEnable='BOOL:false'))
def generate():
 original=E.fromstring((S/'HotKeyWindow.xui').read_text('utf8').rstrip('\0'))
 board=next(original.iter('CNBoardImage'));check=next(x for x in original.iter('CNCheckBox') if x.get('ID')=='STR:15')
 panel=E.Element('ROOT',ScriptVer='1.0');E.SubElement(panel,'TOOL',ToolVer='1.0.0.1')
 a=dict(original.find('POPUPWINDOW').attrib)
 a.update(Size='VECTOR:456, 338',TileType='NUM:0',IsShowTitleBar='BOOL:true',IsShowCloseButton='BOOL:true',TitleString='STR:按键连发',TitleAccessoryType='NUM:-1')
 E.SubElement(panel,'POPUPWINDOW',a);pc=E.SubElement(panel,'CONTROL')
 def box(ident,x,y,w,h):
  a=dict(board.attrib);a.update(ID='STR:'+ident,Pos=f'VECTOR:{x}, {y}',Size=f'VECTOR:{w}, {h}',WidthUnit='NUM:'+str(w),HeightUnit='NUM:'+str(h),IsEnable='BOOL:false');E.SubElement(pc,'CNBoardImage',a)
 def button(ident,text,x,y,w=112):
  a=dict(ImagePack='STR:Interface/WindowCommon.img',UpImg='NUM:12',OverImg='NUM:13',DownImg='NUM:14',DisableImg='NUM:15',ButtonDrawType='STR:PATTERN',ButtonName='STR:'+text,FontTypeStr='STR:system font',IsFontShadow='BOOL:true',ID='STR:'+ident,Pos=f'VECTOR:{x}, {y}',Size=f'VECTOR:{w}, 23',PatternImgWidth='NUM:'+str(w),patternImgPack='STR:Interface/WindowCommon.img')
  for state,start in [('Up',172),('Over',175),('Down',178),('Disable',181)]:
   for part,offset in [('Left',0),('Middle',1),('Right',2)]:a[f'Pattern{part}ImgIndex_{state}']='NUM:'+str(start+offset)
  return E.SubElement(pc,'CNButton',a)
 box('KR_BACKGROUND',0,0,456,338)
 label(pc,'KR_HINT1','按住连发，松开停止',20,15,280,'235, 199, 115')
 label(pc,'KR_HINT2','勾选需要启用的槽位，保存后生效',20,37,410,'190, 190, 190')
 for col in range(2):
  x=14+col*222;box('KR_BOARD_'+str(col),x,61,206,202);label(pc,'KR_HEAD_ENABLE_'+str(col),'启用',x+10,74,28,'170, 162, 140');label(pc,'KR_HEAD_SLOT_'+str(col),'槽位',x+39,74,28,'170, 162, 140');label(pc,'KR_HEAD_KEY_'+str(col),'按键',x+119,74,56,'170, 162, 140')
 for i in range(10):
  x=26+(i//5)*222;y=98+(i%5)*31
  a=dict(check.attrib);a.update(ID=f'STR:KR_CHECK_{i}',Pos=f'VECTOR:{x}, {y+4}',Size='VECTOR:17, 15');E.SubElement(pc,'CNCheckBox',a)
  label(pc,f'KR_NUMBER_{i}',f'{i+1:02}',x+29,y+4,24,'218, 184, 111');button(f'KR_BIND_{i}','',x+65,y);label(pc,f'KR_KEY_{i}','未设置',x+65,y+4,112)
 label(pc,'KR_STATUS','点击按键格进行设置',20,271,416,'225, 207, 155');label(pc,'KR_HELP','录入时：Esc 取消 / Delete 清空',20,291,416,'155, 155, 155');label(pc,'KR_COUNT','已启用 0 / 10',20,315,180,'185, 180, 162')
 button('KR_SAVE','保存',280,310,72);button('KR_CANCEL','取消',364,310,72)
 menu=E.fromstring((S/'MainMenu-original.xui').read_text('utf8').rstrip('\0\r\n'));mc=menu.find('CONTROL')
 E.SubElement(mc,'CNButton',dict(ID='STR:7200',Pos='VECTOR:232, 216',Size='VECTOR:50, 65',ButtonDrawType='STR:NORMALIMAGE',ImagePack='STR:Interface2/hud/Menu_iconVer2.img',UpImg='NUM:30',OverImg='NUM:109',DownImg='NUM:30',DisableImg='NUM:31'))
 # Caption, plate and hover glow are drawn by the original native menu row renderer.
 result={};header='// Generated UTF-8 native XUI. Original native assets only.\n'
 for name,tree in [('MainMenu',menu),('KeyRepeat',panel)]:
  E.indent(tree,space='  ');data=E.tostring(tree,encoding='utf-8',xml_declaration=True);(S/(name+'.xui')).write_bytes(data);result[name]=hashlib.sha256(data).hexdigest();data+=b'\0'
  header+=f'static const unsigned char k{name}Xml[]={{\n'+'\n'.join(','.join(str(n) for n in data[i:i+32])+',' for i in range(0,len(data),32))+'\n};\n'
 (S/'layout.h').write_text(header,'utf8');return result
if __name__=='__main__':print(json.dumps(generate(),indent=2))
