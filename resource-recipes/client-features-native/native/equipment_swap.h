#pragma once
#include "swap_hotkey.h"
#include <intrin.h>
#pragma intrinsic(_InterlockedCompareExchange,_InterlockedExchange)
static void*roleActor;
static unsigned roleUnique,epoch,sequence,pendingSequence,requestAt,lastQuery;
static unsigned char snapshot[512],incoming[512];
static volatile long responseLock,responseReady;
static bool registered,waiting,haveSnapshot;
static unsigned pendingAction,pendingPreset,pendingOrdinal;
static unsigned swapPreset,swapOrdinal,swapTotal,swapMissing,swapFailed,wearAt;
static bool swapping,wearWaiting;
static void*dController;
static unsigned selectedProfile,hotkeyDraft;static bool editHotkey,previewDirty;
static unsigned savedHotkeys[6];static Byte previousKeys[256];static volatile long queuedSwapKey;
static Byte inspectedSnapshot[512];static unsigned inspectedTarget,inspectedAt;
static const unsigned uiDestinations[29]={12,13,14,15,16,17,18,19,20,21,22,23,0,1,2,3,4,5,6,7,8,9,10,26,27,28,29,32,25};
static const unsigned wireDestinations[29]={0,1,2,3,4,5,6,7,8,9,10,12,13,14,15,16,17,18,19,20,21,22,23,25,26,27,28,29,32};

static unsigned u16(const Byte*p,unsigned off){return p[off]|(p[off+1]<<8);}
static unsigned u32(const Byte*p,unsigned off){return *(const unsigned*)(p+off);}
static void*actor(){return ((void*(__cdecl*)())0x29610e0)();}
static unsigned itemId(void*item){
 if(!readable(item,0x24)||!readable((void*)value(item),0x2c))return 0;
 void*info=((void*(__thiscall*)(void*))value((void*)value(item),0x28))(item);
 return readable(info,0x20)?((unsigned(__thiscall*)(void*))0x994e30)((Byte*)info+0x14):0;
}
static void*itemAt(int list,unsigned slot){return ((void*(__cdecl*)(int,unsigned))0x2531b60)(list,slot);}
extern "C" __declspec(dllexport) void __cdecl FeatureReceive(){
 unsigned n=*(unsigned*)0x57f9ad4;void*body=*(void**)0x57f9ad0;
 if(n!=512||!readable(body,n)||_InterlockedCompareExchange(&responseLock,1,0))return;
 ((void(__cdecl*)(void*,unsigned))0x37e8430)(incoming,n);
 _InterlockedExchange(&responseReady,1);_InterlockedExchange(&responseLock,0);
}
extern "C" __declspec(dllexport) __declspec(naked) void FeatureGate(){__asm{
 cmp byte ptr[edi],0
 jne previous
 cmp word ptr[esp+4],7f30h
 jne previous
 mov eax,1
 ret 4
previous:
 push 07101bffh
 ret}}
static bool request(unsigned action,unsigned preset=0,unsigned ordinal=0){
 if(waiting||!roleActor||!epoch)return false;
 unsigned q[6]={0x31514643,2|(action<<16)|(preset<<24),++sequence,epoch,ordinal,0};
 auto net=((void*(__cdecl*)())0x374ea10)();if(!net)return false;
 ((void(__thiscall*)(void*,unsigned))0x374f280)(net,0x7f30);
 for(unsigned v:q)((void(__thiscall*)(void*,unsigned))0x374f3f0)(net,v);
 ((void(__cdecl*)())0x3750ac0)();
 waiting=true;pendingSequence=sequence;pendingAction=action;pendingPreset=preset;pendingOrdinal=ordinal;requestAt=Time();return true;
}
void renderSwap(){

 auto&p=panels[1];if(!p.shown||!owned(p,true))return;
 for(unsigned i=0;i<29;i++){
  wchar_t name[16];Format(name,L"%u",60+i);void*c=control(p,name);if(!readable(c,0x518)||value(c)!=0x48da95c)continue;
  unsigned id=0;if(haveSnapshot&&snapshot[7]==selectedProfile)for(unsigned j=0;j<29;j++)if(wireDestinations[j]==uiDestinations[i])id=u32(snapshot,256+j*4);
  if(id&&value(c,0x3bc)!=id){*(unsigned*)((Byte*)c+0x3bc)=id;((void(__thiscall*)(void*,void*))0x1d53d90)(c,nullptr);}
  visible(c,id!=0);
 }
 wchar_t key[96]{};unsigned bind=editHotkey?hotkeyDraft:savedHotkeys[selectedProfile];
 if(!bind)Format(key,editHotkey?L"请按快捷键":L"未设置");
 else {wchar_t n[48]{};auto map=(decltype(&MapVirtualKeyW))Proc(Module(L"user32.dll"),"MapVirtualKeyW");auto name=(decltype(&GetKeyNameTextW))Proc(Module(L"user32.dll"),"GetKeyNameTextW");if(map&&name)name(map(bind&255,0)<<16,n,48);Format(key,L"%s%s%s%s",(bind&0x20000)?L"Ctrl+":L"",(bind&0x10000)?L"Alt+":L"",(bind&0x40000)?L"Shift+":L"",n);}
 text(control(p,L"20"),key);visible(control(p,L"205"),!editHotkey);visible(control(p,L"206"),editHotkey);

}
void swapEvent(unsigned preset,unsigned action){
 if(swapping||waiting){status(panels[1],L"正在处理，请稍候");return;}
 if(!haveSnapshot||preset>=6){status(panels[1],L"角色数据尚未就绪");return;}
 if(!action){if(request(1,preset))status(panels[1],L"正在保存当前穿戴…");return;}
 swapTotal=u16(snapshot,36+preset*2);if(!swapTotal||swapTotal>29){status(panels[1],L"此方案尚未保存");return;}
 swapPreset=preset;swapOrdinal=swapMissing=swapFailed=wearAt=0;wearWaiting=false;swapping=true;
 status(panels[1],L"正在按原生穿戴规则切换…");
}
static void finishSwap(){
 wchar_t s[128];Format(s,L"切换完成：%u 件已穿戴，%u 件缺失，%u 件未成功",swapTotal-swapMissing-swapFailed,swapMissing,swapFailed);
 swapping=wearWaiting=false;status(panels[1],s);lastQuery=0;
}
static void processFeatureResponse(){
 if(!responseReady||_InterlockedCompareExchange(&responseLock,1,0))return;
 Byte b[512];memcpy(b,incoming,512);responseReady=0;_InterlockedExchange(&responseLock,0);
 if(!waiting||u32(b,0)!=0x31524643||u16(b,4)!=2||u32(b,8)!=pendingSequence||u32(b,12)!=epoch||b[6]!=pendingAction||b[7]!=pendingPreset)return;
 waiting=false;unsigned result=u16(b,20);
 if(b[6]==6){if(result==0&&u32(b,16)==inspectedTarget){memcpy(inspectedSnapshot,b,512);inspectedAt=Time();}return;}
 if(b[6]!=2){
  if(result==0){memcpy(snapshot,b,512);haveSnapshot=true;for(unsigned i=0;i<6;i++)savedHotkeys[i]=u32(b,384+i*4);renderSwap();}
  if(b[6]==3)status(panels[1],result==0?L"已清空此方案及快捷键":L"清空失败");
  if(b[6]==5)status(panels[1],result==0?L"快捷键已保存":L"快捷键保存失败");
  if(b[6]==1)status(panels[1],result==0?L"已保存当前穿戴":L"请在城镇保存换装方案");return;
 }
 if(!swapping||u16(b,22)!=swapOrdinal)return;
 if(result==0){swapOrdinal++;wearWaiting=false;wearAt=0;}
 else if(result==3||result==4){swapOrdinal++;swapMissing++;wearWaiting=false;wearAt=0;}
 else if(result!=1){swapOrdinal++;swapFailed++;wearWaiting=false;wearAt=0;}
 else if(wearWaiting){if(Time()-wearAt>=4000){swapOrdinal++;swapFailed++;wearWaiting=false;wearAt=0;}}
 else{
  unsigned list=u16(b,24),source=u16(b,26),dest=u16(b,28),id=u32(b,32);
  void*container=((void*(__cdecl*)(int))0x2531a80)(list);
  if((list!=0&&list!=1&&list!=7&&list!=38)||dest>32||!container||itemId(itemAt(list,source))!=id){swapOrdinal++;swapFailed++;}
  else{wearAt=Time();wearWaiting=true;((bool(__thiscall*)(void*,int,unsigned,int,int,int))0x25f9a70)(container,source,dest,1,6,0);}
 }
 if(swapOrdinal>=swapTotal)finishSwap();
}
void swapTick(){
 void*nowActor=actor();void*manager=*(void**)0x56a3758;
 void*user=readable(manager,0x74)?((void*(__thiscall*)(void*))0x296dd80)(manager):nullptr;
 unsigned uid=readable(user,0x22)?*(unsigned short*)((Byte*)user+0x20):0;
 if(nowActor!=roleActor||uid!=roleUnique){
  roleActor=nowActor;roleUnique=uid;inspectedTarget=inspectedAt=0;memset(inspectedSnapshot,0,sizeof(inspectedSnapshot));selectedProfile=hotkeyDraft=0;editHotkey=false;previewDirty=true;memset(savedHotkeys,0,sizeof(savedHotkeys));epoch++;if(!epoch)epoch++;waiting=haveSnapshot=swapping=wearWaiting=false;lastQuery=0;memset(snapshot,0,sizeof(snapshot));renderSwap();
 }
 if(!roleActor||!roleUnique)return;
 void*controller=((void*(__cdecl*)())0x1fa2790)();
 if(controller&&controller!=dController){((void(__thiscall*)(void*,unsigned,void*,unsigned))0x251e280)(controller,0x7f30,(void*)&FeatureReceive,0);dController=controller;}
 processFeatureResponse();unsigned now=Time();
 long key=_InterlockedExchange(&queuedSwapKey,0);
 if(key==100){if(panels[1].shown)closePanel(panels[1]);else{previewDirty=true;openPanel(panels[1]);}}
 else if(key==101){closePanel(panels[1]);editHotkey=false;}
 else if(key>=1&&key<=6)swapEvent(key-1,1);
 else if(key>=0x1000000){hotkeyDraft=(unsigned)key&0xffffff;renderSwap();}

 if(waiting&&now-requestAt>6000){waiting=false;if(swapping){swapping=false;status(panels[1],L"服务器响应超时，已停止换装");}else if(panels[1].shown)status(panels[1],L"读取超时，请检查配套服务端");}
 if(waiting)return;
 if(swapping){if(!wearWaiting||now-requestAt>=400)request(2,swapPreset,swapOrdinal);}
 else if(previewDirty){if(request(4,selectedProfile))previewDirty=false;}
 else if(inspectedTarget&&(!inspectedAt||now-inspectedAt>=2000)){if(request(6,0,inspectedTarget))inspectedAt=now;}
 else if(!lastQuery||now-lastQuery>=2000){lastQuery=now;request(0,selectedProfile);}
}

// Ported upstream HandleControlEvent actions, through the S5A3 pointer/ID adapter.
void swapControl(int id,int argument){
 int tab=id>=101&&id<=106?id-101:id==100?argument:-1;
 if(tab>=0&&tab<6){if(selectedProfile!=(unsigned)tab){selectedProfile=tab;previewDirty=true;editHotkey=false;_InterlockedExchange(&queuedSwapKey,0);renderSwap();}return;}
 if(id==207){closePanel(panels[1]);editHotkey=false;return;}
 if(id==205){editHotkey=true;hotkeyDraft=0;_InterlockedExchange(&queuedSwapKey,0);renderSwap();status(panels[1],L"按下组合键后点击保存，Esc取消");return;}
 if(id==206){if(waiting)return;if(!hotkeyDraft){status(panels[1],L"请先按下快捷键");return;}for(unsigned i=0;i<6;i++)if(i!=selectedProfile&&savedHotkeys[i]==hotkeyDraft){status(panels[1],L"此快捷键已被其他方案使用");return;}if(request(5,selectedProfile,hotkeyDraft)){editHotkey=false;renderSwap();}return;}
 if(id==202)swapEvent(selectedProfile,0);
 else if(id==203)swapEvent(selectedProfile,1);
 else if(id==204&&!waiting&&!swapping){if(request(3,selectedProfile))status(panels[1],L"正在清空此方案…");}
}
void swapKeys(const Byte* physical,bool eligible){
 if(eligible){
  unsigned mods=((physical[56]|physical[184])&128?1:0)|((physical[29]|physical[157])&128?2:0)|((physical[42]|physical[54])&128?4:0);
  for(unsigned scan=1;scan<256;scan++)if((physical[scan]&128)&&!(previousKeys[scan]&128)){
   unsigned vk=swap_hotkey::key(scan,scanMap());if(!vk||swap_hotkey::modifier(vk))continue;
   if(editHotkey&&panels[1].shown){if(vk==VK_ESCAPE){editHotkey=false;queuedSwapKey=101;}else if(vk!=VK_END)queuedSwapKey=0x1000000|vk|(mods<<16);}
   else if(vk==VK_END&&!mods)queuedSwapKey=100;
   else if(vk==VK_ESCAPE&&panels[1].shown)queuedSwapKey=101;
   else if(!panels[0].shown)for(unsigned i=0;i<6;i++)if(swap_hotkey::valid(savedHotkeys[i])&&(vk|(mods<<16))==savedHotkeys[i])queuedSwapKey=i+1;
  }
 }
 memcpy(previousKeys,physical,256);
}
