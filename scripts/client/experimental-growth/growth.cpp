#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#include <intrin.h>
#include "binding.h"
#include "diagnostic.h"
#pragma intrinsic(_InterlockedCompareExchange, _InterlockedExchange, _InterlockedIncrement)
#define API(name,addr) (*reinterpret_cast<decltype(&name)*>(addr))
#define Query API(VirtualQuery,0x6e85b18)
#define Thread API(GetCurrentThreadId,0x6e85a90)
#define Process API(GetCurrentProcessId,0x6e85a68)
#define WindowThread API(GetWindowThreadProcessId,0x6e8672c)
#define WindowOwner API(GetWindow,0x6e86728)
#define Visible API(IsWindowVisible,0x6e866f0)
#define ClientRect API(GetClientRect,0x6e866d8)
#define Enumerate API(EnumWindows,0x6f2106c)
extern "C" void* __cdecl memset(void*p,int v,size_t n){auto b=(unsigned char*)p;while(n--)*b++=(unsigned char)v;return p;}
extern "C" void* __cdecl memcpy(void*d,const void*s,size_t n){auto a=(unsigned char*)d;auto b=(const unsigned char*)s;while(n--)*a++=*b++;return d;}
static uintptr_t value(const void*p,unsigned n=0){return *(uintptr_t*)((char*)p+n);}
static bool readable(const void*p,unsigned n){MEMORY_BASIC_INFORMATION m{};if(!p||!Query(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;uintptr_t a=(uintptr_t)p,e=(uintptr_t)m.BaseAddress+m.RegionSize;return a<=e&&n<=e-a;}
static unsigned get16(const unsigned char*b,unsigned i){return b[i]|unsigned(b[i+1])<<8;}
static unsigned get32(const unsigned char*b,unsigned i){return get16(b,i)|get16(b,i+2)<<16;}
enum {ResponseSize=5424};
static unsigned char response[ResponseSize],state[ResponseSize];static volatile long lock;
static bool responseReady,registered,ready,busy,confirming,awaiting,starting;
static volatile unsigned threadId;static unsigned selected=0xffff,started,cursor,metadataID;
static void *manager,*inventoryManager,*controller;
static bool ui(){return threadId&&Thread()==threadId;}
// Getter can run during background initialization. Select the UI thread from
// this process's visible unowned game window, matching the existing activity UI.
static BOOL CALLBACK findGameWindow(HWND window,LPARAM out){DWORD pid=0;DWORD tid=WindowThread(window,&pid);RECT rect{};
 if(pid==Process()&&!WindowOwner(window,GW_OWNER)&&Visible(window)&&ClientRect(window,&rect)&&rect.right>=400&&rect.bottom>=300){*(unsigned*)out=tid;return FALSE;}return TRUE;}
static unsigned now(){return ((unsigned(__stdcall*)())*(void**)0x45881f0)();}
static unsigned mode(){return ((unsigned(__cdecl*)())0x3819d00)();}
static void setMode(unsigned n){void*m=*(void**)0x56a3798;if(readable(m,0xc640))((void(__thiscall*)(void*,unsigned))0x32c0830)(m,n);}
static void hint(const wchar_t*s){((void(__cdecl*)(const wchar_t*,int,int,int,int,int,int))0x2531940)(s,-1,0,0,-1,-1,0);}
static bool windowActive(unsigned id){if(!readable(manager,0xc640)||*(void**)0x56a3798!=manager)return false;auto h=(char*)manager+(id*5+0x41)*4;return value(h,4)!=value(h,8);}
static void* currentController(){return readable(manager,0xc640)?((void*(__thiscall*)(void*))0x32bac90)(manager):nullptr;}
static bool owns(){return controller&&currentController()==controller&&readable(controller,0x4d8)&&value(controller,0x1e8)==metadataID&&value(controller,0x1e4)==(state[39]?12u:11u);}
static bool active(){return (ready||awaiting)&&ui()&&inventoryManager==*(void**)0x56a379c&&unsigned(now()-started)<120000u;}
static unsigned itemID(void*item){auto p=(char*)item+0x20;((void(__cdecl*)(void*,unsigned,unsigned,unsigned))0x3e4b210)(p,4,0x32,1);return value(p);}
static void* itemAt(unsigned slot){return ((void*(__thiscall*)(void*,unsigned))0x25eb680)(inventoryManager,slot);}
static unsigned targetIndex(void*item){if(!active()||!readable(item,0x2a0)||value(item,0x29c)!=(uintptr_t)inventoryManager)return 0xffff;
 int slot=((int(__thiscall*)(void*,void*))0x25ece10)(inventoryManager,item);if(slot<9||slot>64)return 0xffff;
 for(unsigned i=0;i<get16(state,10);i++){unsigned o=48+i*96;if(get16(state,o)==unsigned(slot)&&get32(state,o+4)==itemID(item))return i;}return 0xffff;}
static void cancel(){
 bool previousBusy=busy;busy=true;
 if(owns()){
  if(value(controller,0x1ec)==2)((void(__thiscall*)(void*,int))0x3316a40)(controller,0);
  ((void(__thiscall*)(void*))value((void*)value(controller),0x7c))(controller);
 }
 if(ready&&!confirming&&mode()==cursor)setMode(0);
 ready=confirming=awaiting=false;controller=nullptr;selected=0xffff;
 busy=previousBusy;
}
static bool begin(){
 manager=*(void**)0x56a3798;
 if(!readable(manager,0xc640)||windowActive(0x23b)){diagnostic(20,readable(manager,0xc640),windowActive(0x23b));return false;}
 unsigned k=state[39]?1:0;metadataID=990034000+k*100+state[38];
 // Use real native stackable definitions for display, never create an inventory item.
 void*catalog=*(void**)0x56a36ec;
 void*def=((void*(__thiscall*)(void*,unsigned,int))0x3754450)(catalog,metadataID,1);
 if(!readable(def,0x850)){diagnostic(21,metadataID,0);return false;}
 starting=true;
 controller=((void*(__thiscall*)(void*,int,int))0x32c2560)(manager,state[39]?12:11,0);
 starting=false;
 if(!readable(controller,0x4d8)){diagnostic(22,metadataID,0);return false;}
 ((void(__thiscall*)(void*,unsigned))0x2152d20)(controller,metadataID);
 if(!owns()){diagnostic(23,metadataID,value(controller,0x1e4));return false;}
 ready=true;setMode(cursor);diagnostic(25,metadataID,mode());return true;
}
static bool send(void*target,void*material,unsigned kind){
 if(!ready||!confirming||awaiting||!active()||!owns()||selected>=get16(state,10)||kind!=(state[39]?1u:0u))return false;
 if(targetIndex(target)!=selected||material!=itemAt(get16(state,36))||!readable(material,0x2a0)||itemID(material)!=get32(state,32))return false;
 void*net=((void*(__cdecl*)())0x374ea10)();if(!readable(net,0x2bd06c)||!*((unsigned char*)net+8)||value(net,0x2bcc34)||value(net,0x2bcc2c))return false;
 unsigned slot=get16(state,48+selected*96);unsigned char b[32]={'G','T','Q','1',1,0,1,0};b[8]=(unsigned char)slot;b[9]=(unsigned char)(slot>>8);memcpy(b+16,state+16,16);
 if(((int(__thiscall*)(void*,unsigned))0x374f280)(net,0x7f20)!=1)return false;
 for(unsigned i=0;i<32;i+=4)((int(__thiscall*)(void*,unsigned))0x374f3f0)(net,get32(b,i));ready=false;awaiting=true;diagnostic(50,slot,kind);((int(__cdecl*)())0x3750ac0)();return true;
}
static void result(){
 unsigned n=selected;bool ok=awaiting&&n<get16(state,10)&&inventoryManager==*(void**)0x56a379c;
 unsigned slot=ok?get16(state,48+n*96):0;void*target=ok?itemAt(slot):nullptr;
 ok=ok&&readable(target,0x2a0)&&itemID(target)==get32(state,48+n*96+4);
 unsigned oldLevel=ok?state[48+n*96+2]:0;unsigned amplify=state[39]?1:0;
 cancel();if(!ok){hint(L"成长券已使用，请查看目标装备。");return;}
 // Same original result class and initialization used by the native op80 callback.
 *(unsigned*)0x55a761c=amplify;
 ((void(__cdecl*)(unsigned))0x3425380)(oldLevel);
 ((void*(__thiscall*)(void*,unsigned,void*,unsigned))0x32bea50)(manager,0x41c,target,0);
}
extern "C" __declspec(dllexport) int __cdecl Hover(void*item){if(!ready||!ui()||confirming||mode()!=cursor)return -1;return active()&&targetIndex(item)<get16(state,10)?2:0;}
extern "C" __declspec(naked) int __cdecl ValidateOriginal(){__asm{push ebp
 mov ebp,esp
 push -1
 push 032c9fc5h
 ret}}
extern "C" __declspec(dllexport) int __fastcall Validate(void*self,void*,int action,void*item,void*a,int*error,void*c,void*d){
 if(ready&&ui()&&mode()==cursor&&((cursor==0x14&&action==12)||(cursor==0x1e&&action==64))){int n=active()&&targetIndex(item)<get16(state,10)?2:0;if(n==0&&readable(error,4))*error=0x674;return n;}
 return ((int(__thiscall*)(void*,int,void*,void*,int*,void*,void*))&ValidateOriginal)(self,action,item,a,error,c,d);}
extern "C" __declspec(naked) bool __cdecl ActionOriginal(){__asm{push ebp
 mov ebp,esp
 sub esp,220h
 push 032d2089h
 ret}}
extern "C" __declspec(dllexport) bool __fastcall Action(void*self,void*,int action,void*item,int clicked,int a,int b){

 bool privateAction=ui()&&ready&&mode()==cursor&&((cursor==0x14&&action==12)||(cursor==0x1e&&action==64));
 if(privateAction){
  if(!active()||!owns()){cancel();return true;}
  unsigned n=targetIndex(item);if(n>=get16(state,10))return true;
  if(clicked&255){selected=n;confirming=true;}
 }
 // The original action opens native window 0x309 and its original events.
 bool previousBusy=busy;if(privateAction&&(clicked&255))busy=true;
 bool ok=((bool(__thiscall*)(void*,int,void*,int,int,int))&ActionOriginal)(self,action,item,clicked,a,b);
 busy=previousBusy;return ok;
}
extern "C" __declspec(naked) int __cdecl FindOriginal(){__asm{push ebp
 mov ebp,esp
 sub esp,0ch
 push 025eca16h
 ret}}
extern "C" __declspec(dllexport) int __fastcall FindSource(void*self,void*,unsigned id){
 if(!starting&&ui()&&self==inventoryManager&&id==metadataID&&active()&&owns()){
  void*source=itemAt(get16(state,36));if(readable(source,0x2a0)&&itemID(source)==get32(state,32))return get16(state,36);return -1;
 }
 return ((int(__thiscall*)(void*,unsigned))&FindOriginal)(self,id);
}
extern "C" __declspec(naked) bool __cdecl SendOriginal(){__asm{push ebp
 mov ebp,esp
 push -1
 push 025fdab5h
 ret}}
extern "C" __declspec(dllexport) bool __fastcall NativeSend(void*self,void*,void*target,void*material,unsigned kind){
 if(ui()&&(ready||awaiting)&&confirming&&owns()&&self==inventoryManager){
  if(awaiting)return true;bool ok=send(target,material,kind);if(!ok)hint(L"物品或状态已变化，请重新右键成长券。");return ok;
 }
 return ((bool(__thiscall*)(void*,void*,void*,unsigned))&SendOriginal)(self,target,material,kind);
}
extern "C" __declspec(dllexport) __declspec(naked) void HoverGate(){__asm{pushfd
 pushad
 push esi
 call Hover
 add esp,4
 cmp eax,-1
 je original
 mov [esp+28],eax
 popad
 popfd
 push 01d4c51eh
 ret
 original:
 popad
 popfd
 lea ecx,[ebp-38h]
 mov eax,032b5470h
 call eax
 push 01d4c3c3h
 ret}}
extern "C" __declspec(dllexport) void __cdecl Receive(){unsigned n=*(unsigned*)0x57f9ad4;void*body=*(void**)0x57f9ad0;diagnostic(10,n,0);if(n!=ResponseSize||!readable(body,n)||_InterlockedCompareExchange(&lock,1,0))return;((void(__cdecl*)(void*,unsigned))0x37e8430)(response,n);auto b=response;
 bool valid=b[0]=='G'&&b[1]=='T'&&b[2]=='S'&&b[3]=='1'&&get16(b,4)==1&&get16(b,6)<=3&&get16(b,10)<=56&&b[39]<=4;
 if(get16(b,6)==0)valid=valid&&get32(b,32)>=990033101&&get32(b,32)<=990033105&&get16(b,36)>=9&&get16(b,36)<=64&&b[38]>=1&&b[38]<=31;
 for(unsigned i=0;valid&&i<get16(b,10);i++){unsigned o=48+i*96;if(get16(b,o)<9||get16(b,o)>64||get16(b,o+94)!=0)valid=false;for(unsigned j=0;j<i;j++)if(get16(b,48+j*96)==get16(b,o))valid=false;}
 responseReady=valid;diagnostic(11,valid,get16(b,6));_InterlockedExchange(&lock,0);}
extern "C" __declspec(dllexport) void __cdecl Tick(){

 if(busy)return;if(!threadId){Enumerate(findGameWindow,(LPARAM)&threadId);if(threadId)diagnostic(2,threadId,Thread());}
 if(!ui())return;busy=true;
 if(!registered){void*m=((void*(__cdecl*)())0x1fa2790)();if(m){((void(__thiscall*)(void*,unsigned,void*,unsigned))0x251e280)(m,0x7f20,(void*)&Receive,0);registered=true;diagnostic(3,threadId,0);}}
 if(ready&&(!active()||!owns()||(!confirming&&mode()!=cursor)||(confirming&&value(controller,0x1ec)==0&&!windowActive(0x309))))cancel();
 if(awaiting&&!active())cancel();
 if(!_InterlockedCompareExchange(&lock,1,0)){if(responseReady){responseReady=false;unsigned status=get16(response,6);diagnostic(12,status,get16(response,10));
  if(status==3)result();
  else {cancel();if(status==0){memcpy(state,response,ResponseSize);inventoryManager=*(void**)0x56a379c;started=now();cursor=state[39]?0x1e:0x14;
    if(!get16(state,10))hint(L"背包内没有符合条件的目标装备。");
    else if(!readable(inventoryManager,0x108)||!begin()){cancel();hint(L"无法打开原生用券流程，请关闭其他强化窗口后重试。");}}
   else hint(status==1?L"成长券须至少+1，请先培养或解除锁定。":L"物品或状态已变化，请重新右键成长券。");}}
  _InterlockedExchange(&lock,0);}busy=false;
}
extern "C" __declspec(dllexport) __declspec(naked) void Getter(){__asm{pushfd
 pushad
 call Tick
 popad
 popfd
 push PREVIOUS_GETTER
 ret}}
extern "C" __declspec(dllexport) __declspec(naked) void Gate(){__asm{cmp byte ptr[edi],0
 jne old
 cmp word ptr[esp+4],7f20h
 jne old
 mov eax,1
 ret 4
 old:
 push PREVIOUS_GATE
 ret}}
