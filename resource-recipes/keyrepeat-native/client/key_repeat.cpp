// Statically integrated into the matched client. No worker, DLL injection,
// synthetic OS input, server packet, global hook or background timer.
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#include "repeat_core.h"
#include "layout.h"
#include "binding.h"
#define API(name,addr) (*reinterpret_cast<decltype(&name)*>(addr))
#define KR_Tick API(GetTickCount,0x6e85a24)
#define KR_Async API(GetAsyncKeyState,0x6e866a0)
#define KR_Query API(VirtualQuery,0x6e85b18)
#define KR_Thread API(GetCurrentThreadId,0x6e85a90)
#define KR_Module API(GetModuleHandleW,0x6e85ab0)
#define KR_Proc API(GetProcAddress,0x6e85a9c)
#define KR_Write API(WritePrivateProfileStructW,0x6e85b58)
#define KR_Read API(GetPrivateProfileStructW,0x6e85b60)
#define KR_Mkdir API(CreateDirectoryW,0x6e85b78)
#define KR_Format API(wsprintfW,0x6e866b8)
extern "C" void* __cdecl memset(void* p,int v,size_t n){auto b=(unsigned char*)p;while(n--)*b++=(unsigned char)v;return p;}
extern "C" void* __cdecl memcpy(void* d,const void* s,size_t n){auto a=(unsigned char*)d;auto b=(const unsigned char*)s;while(n--)*a++=*b++;return d;}
using namespace keyrepeat;
// No registers or flags are borrowed by these fixed-base trampolines.
// The builder verifies the displaced instructions against exact G211 bytes.
extern "C" __declspec(dllexport) __declspec(naked) void* __cdecl PreviousFactory(int,void*){__asm{
 push ebp
 mov ebp,esp
 sub esp,0ch
 push 06ef3fd7h
 ret
}}
extern "C" __declspec(dllexport) __declspec(naked) bool __cdecl PreviousLayout(const wchar_t*,void*){__asm{
 cmp byte ptr ds:[06f02218h],0
 push 06ef4101h
 ret
}}
struct Ref {void* object;void* count;};
static Config saved,draft;static Run runs[Slots];
static bool loaded,creating,shown,failed,waitRelease;
static int capture=-1,windowId;
static Tick captureSince;
static DWORD uiThread;
static void *win,*manager,*root;
static bool placed;static int screenWidth,screenHeight;
static uintptr_t panelVtable[129];
static void __fastcall PanelUpdate(void*,void*);
static void *checks[Slots],*buttons[Slots],*labels[Slots],*statusControl;
static wchar_t configPath[512];
static unsigned char previousPhysical[256];
static const wchar_t panelPath[]=L"UI/PrivateKeyRepeat001/KeyRepeat.xui";
static const wchar_t menuPath[]=L"UI/HUD/MainMenuWindowVer2.xui";
static uintptr_t value(const void*p,unsigned o=0){return *(const uintptr_t*)((const char*)p+o);}
static bool readable(const void*p,unsigned n){MEMORY_BASIC_INFORMATION m{};if(!p||!KR_Query(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;uintptr_t a=(uintptr_t)p,e=(uintptr_t)m.BaseAddress+m.RegionSize;return a<=e&&n<=e-a;}
static bool equal(const void*a,const void*b,unsigned n){auto x=(const Byte*)a,y=(const Byte*)b;while(n--)if(*x++!=*y++)return false;return true;}
static bool pathEqual(const wchar_t*a,const wchar_t*b){if(!a||!b)return false;for(;;a++,b++){wchar_t x=*a,y=*b;if(x>=L'A'&&x<=L'Z')x+=32;if(y>=L'A'&&y<=L'Z')y+=32;if(x==L'\\')x=L'/';if(y==L'\\')y=L'/';if(x!=y)return false;if(!x)return true;}}
static bool ui(){return uiThread&&KR_Thread()==uiThread;}
static const Byte* scanMap(){return (const Byte*)0x6e8c000;}
static bool foreground(){return value((void*)0x584678c)&&value((void*)0x584678c)==value((void*)0x5846770);}
static void physical(Byte* keys){memset(keys,0,256);for(unsigned k=8;k<255;k++)if(mapped(k,scanMap())||k==27||k==46)keys[k]=(KR_Async(k)&0x8000)?1:0;}
static void load(){
 if(loaded)return;loaded=true;defaults(saved);
 auto env=(decltype(&GetEnvironmentVariableW))KR_Proc(KR_Module(L"kernel32.dll"),"GetEnvironmentVariableW");
 if(!env)return;DWORD n=env(L"LOCALAPPDATA",configPath,470);if(!n||n>=470){configPath[0]=0;return;}
 const wchar_t folder[]=L"\\DNF90";for(unsigned i=0;i<sizeof(folder)/2;i++)configPath[n+i]=folder[i];
 KR_Mkdir(configPath,nullptr);n+=6;const wchar_t file[]=L"\\KeyRepeat.ini";for(unsigned i=0;i<sizeof(file)/2;i++)configPath[n+i]=file[i];
 Config c{};if(KR_Read(L"KeyRepeat",L"SlotsV1",&c,sizeof(c),configPath)&&valid(c,scanMap()))saved=c;
}
static bool owned(void* w,int id,bool active){
 if(!w||!readable(manager,0xc640)||*(void**)0x56a3798!=manager||id<=0||id>=0x4d8)return false;
 auto h=(char*)manager+(id*5+(active?0x41:0x1879))*4;auto b=value(h,4),e=value(h,8);
 if(e<b||e-b>64||(e-b)%4||(b!=e&&!readable((void*)b,(unsigned)(e-b))))return false;
 for(;b<e;b+=4)if(*(void**)b==w)return true;return false;
}
static bool owns(bool active){return owned(win,windowId,active);}
static bool freeSlot(int id){
 if(!readable(manager,0xc640)||!value(manager,0xc5b4))return false;
 for(unsigned i=0;i<2;i++){unsigned o=i?0x1879:0x41;auto h=(char*)manager+(id*5+o)*4;if(value(h,4)!=value(h,8))return false;}
 return value((void*)0x3373f58,id*4)==0x3373f41;
}
static void* controlAt(void* tree,const wchar_t* name){Ref r{};if(!readable(tree,0x100))return nullptr;((Ref*(__thiscall*)(void*,Ref*,const wchar_t*))0x37eb120)(tree,&r,name);void*p=r.object;((void(__thiscall*)(Ref*))0xf11f90)(&r);return p;}
static void* control(const wchar_t* name){return controlAt(root,name);}
static void setText(void*p,const wchar_t*t){if(readable(p,0x410)&&value(p)==0x4d4fa4c)((void(__thiscall*)(void*,const wchar_t*))0x3835940)(p,t);}
static void status(const wchar_t*t){if(shown&&owns(true))setText(statusControl,t);}
static const wchar_t* keyName(unsigned k,wchar_t*buf){
 if(!k)return L"未设置";
 if(k>=L'A'&&k<=L'Z'||k>=L'0'&&k<=L'9'){buf[0]=(wchar_t)k;buf[1]=0;return buf;}
 if(k>=VK_F1&&k<=VK_F24){KR_Format(buf,L"F%u",k-VK_F1+1);return buf;}
 if(k>=VK_NUMPAD0&&k<=VK_NUMPAD9){KR_Format(buf,L"Num %u",k-VK_NUMPAD0);return buf;}
 switch(k){case VK_SPACE:return L"Space";case VK_SHIFT:return L"Shift";case VK_LSHIFT:return L"LShift";case VK_RSHIFT:return L"RShift";
 case VK_LEFT:return L"Left";case VK_RIGHT:return L"Right";case VK_UP:return L"Up";case VK_DOWN:return L"Down";
 case VK_HOME:return L"Home";case VK_END:return L"End";case VK_PRIOR:return L"PageUp";case VK_NEXT:return L"PageDn";
 case VK_INSERT:return L"Insert";case VK_DELETE:return L"Delete";case VK_BACK:return L"Back";case VK_CAPITAL:return L"Caps";
 case VK_OEM_1:return L";";case VK_OEM_PLUS:return L"=";case VK_OEM_COMMA:return L",";case VK_OEM_MINUS:return L"-";
 case VK_OEM_PERIOD:return L".";case VK_OEM_2:return L"/";case VK_OEM_3:return L"`";case VK_OEM_4:return L"[";
 case VK_OEM_5:return L"\\";case VK_OEM_6:return L"]";case VK_OEM_7:return L"'";
 case VK_MULTIPLY:return L"Num *";case VK_ADD:return L"Num +";case VK_SUBTRACT:return L"Num -";case VK_DECIMAL:return L"Num .";case VK_DIVIDE:return L"Num /";}
 KR_Format(buf,L"VK %u",k);return buf;
}
static void render(){if(!shown||!owns(true))return;for(unsigned i=0;i<Slots;i++){
 ((void(__thiscall*)(void*,bool))0x3840b20)(checks[i],draft.slot[i].enabled!=0);
 wchar_t name[32];setText(labels[i],capture==(int)i?L"请按一个键":keyName(draft.slot[i].vk,name));
 int w=((int(__thiscall*)(void*))0x3835890)(labels[i]);
 if(w>=0&&w<=112)((void(__thiscall*)(void*,int,int))0x37ea190)(labels[i],91+(i/5)*222+(112-w)/2,102+(i%5)*31);
 }unsigned count=0;for(unsigned i=0;i<Slots;i++)if(draft.slot[i].enabled)count++;
 wchar_t text[40];KR_Format(text,L"已启用 %u / 10",count);setText(control(L"KR_COUNT"),text);}
static void resetRun(){Byte p[256];physical(p);step(saved,runs,KR_Tick(),false,p,(Byte*)0,scanMap());}
static void closePanel(){capture=-1;waitRelease=false;if(owns(true))((void(__thiscall*)(void*,int,int,int))0x32b94a0)(manager,windowId,-1,0);shown=false;resetRun();}
static void forget(){placed=false;win=root=nullptr;shown=false;capture=-1;waitRelease=false;}
static void sync(){if(shown&&!owns(true)){if(owns(false)){shown=false;capture=-1;waitRelease=false;resetRun();}else forget();}}
static void save(){
 if(capture>=0){status(L"请先完成按键设置");return;}
 if(!valid(draft,scanMap())){status(L"按键设置无效");return;}
 Config back{};
 if(!configPath[0]||!KR_Write(L"KeyRepeat",L"SlotsV1",&draft,sizeof(draft),configPath)||
    !KR_Read(L"KeyRepeat",L"SlotsV1",&back,sizeof(back),configPath)||!equal(&back,&draft,sizeof(back))){status(L"保存失败，请重试");return;}
 saved=draft;resetRun();closePanel();
}
static void startCapture(unsigned i){capture=(int)i;waitRelease=true;captureSince=KR_Tick();physical(previousPhysical);render();status(L"松开按键后再按新键");}
static void captureTick(){
 Byte p[256];physical(p);Tick now=KR_Tick();
 if(!foreground()){if(capture>=0){capture=-1;waitRelease=true;render();status(L"已取消按键录入");}memcpy(previousPhysical,p,256);return;}
 bool any=false;for(unsigned k=8;k<255;k++)if(p[k])any=true;
 if(waitRelease){if(!any){waitRelease=false;status(capture>=0?L"请按要连发的按键":L"点击按键格进行设置");}memcpy(previousPhysical,p,256);return;}
 if(capture>=0&&now-captureSince>15000){capture=-1;render();status(L"录入超时，请重试");}
 for(unsigned k=8;k<255;k++)if(p[k]&&!previousPhysical[k]){
  if(k==VK_ESCAPE){if(capture>=0){capture=-1;waitRelease=true;render();status(L"已取消按键录入");}else closePanel();break;}
  if(capture<0)continue;
  if(k==VK_DELETE){draft.slot[capture]={0,0};capture=-1;waitRelease=true;render();status(L"已清空此槽位");break;}
  if(!permitted(k)||!mapped(k,scanMap())){status(L"此键不可用于连发");continue;}
  unsigned duplicate=0;for(unsigned j=0;j<Slots;j++)if((int)j!=capture&&draft.slot[j].vk==k)duplicate=j+1;
  if(duplicate){wchar_t message[48];KR_Format(message,L"此按键已在槽位 %02u 使用",duplicate);status(message);continue;}
  draft.slot[capture].vk=k;capture=-1;waitRelease=true;render();status(L"已设置，保存后生效");break;
 }
 memcpy(previousPhysical,p,256);
}
static int __fastcall PanelEvent(void*,void*,int sender,int kind,int v,int){
 if(!ui()||failed||!shown||!owns(true))return 0;void*p=(void*)sender;
 if(kind==13){if(p==control(L"KR_SAVE")){save();return 1;}if(p==control(L"KR_CANCEL")||p==(void*)value(win,0x50)){closePanel();return 1;}
  for(unsigned i=0;i<Slots;i++)if(p==buttons[i]){startCapture(i);return 1;}}
 if(kind==23)for(unsigned i=0;i<Slots;i++)if(p==checks[i]){
  if(!draft.slot[i].vk){draft.slot[i].enabled=0;render();startCapture(i);return 1;}
  draft.slot[i].enabled=v==1;render();return 1;}
 return 0;
}
static void* events[]={(void*)&PanelEvent};
static bool bind(){
 if(!readable(win,0x1dc)||value(win)!=0x49f03f4||value(win,0x1b0)!=0x49f03ec)return false;
 root=(void*)value(win,0x30);if(!readable(root,0x100))return false;
 for(unsigned i=0;i<Slots;i++){
  wchar_t name[32];KR_Format(name,L"KR_CHECK_%u",i);checks[i]=control(name);
  KR_Format(name,L"KR_BIND_%u",i);buttons[i]=control(name);
  KR_Format(name,L"KR_KEY_%u",i);labels[i]=control(name);
  if(!readable(checks[i],0x579)||!readable(buttons[i],0x504)||!readable(labels[i],0x410)||value(labels[i])!=0x4d4fa4c)return false;
 }
 statusControl=control(L"KR_STATUS");if(!statusControl||!control(L"KR_SAVE")||!control(L"KR_CANCEL"))return false;
 *(void***)((char*)win+0x1b0)=events;
 // The same native popup title/close machinery as the existing auxiliary UIs.
 void*t=(void*)value(win,8);if(!readable(t,0x60))return false;
 *((Byte*)t+0x44)=1;*((Byte*)t+0x45)=1;
 // CNWindow's size virtuals multiply columns/rows by tile dimensions.
 // TileType 0, 38x26 native tiles = 456x338 content (480x374 outer).
 void*props=(void*)value(win,4);if(!readable(props,0x4c))return false;
 *(unsigned*)((char*)props+0x24)=38;*(unsigned*)((char*)props+0x28)=26;
 *(unsigned*)((char*)props+0x44)=12;*(unsigned*)((char*)props+0x48)=13;*((Byte*)props+0x3b)=1;
 ((void(__thiscall*)(void*,int))0x335f0d0)(win,0);
 void*title=(void*)value(win,0x48);void*x=(void*)value(win,0x50);
 if(!readable(title,0x140)||!readable(x,0x504))return false;
 ((void(__thiscall*)(void*,bool))0x3826f40)(title,true);
 ((void(__thiscall*)(void*,bool))value((void*)value(title),8))(title,true);
 ((void(__thiscall*)(void*,bool))value((void*)value(x),8))(x,true);
 void*acc=(void*)value(win,0x58);if(acc)((void(__thiscall*)(void*,bool))value((void*)value(acc),8))(acc,false);
 // Per-instance virtual table: keep RTTI and all original methods, only
 // this panel owns recording updates. Keyboard and other XUI windows stay native.
 memcpy(panelVtable,(void*)(0x49f03f4-4),sizeof(panelVtable));panelVtable[1+4]=(uintptr_t)&PanelUpdate;
 *(uintptr_t**)win=panelVtable+1;
 return true;
}
static int clamp(int n,int lo,int hi){if(hi<lo)hi=lo;return n<lo?lo:n>hi?hi:n;}
static void placePanel(){
 if(!shown||!owns(true)||!readable(root,0x84))return;
 int sw=*(int*)0x55b0318,sh=*(int*)0x55b031c;if(sw<480||sh<374)return;
 if(placed&&sw==screenWidth&&sh==screenHeight)return;
 int xy[2];((void*(__thiscall*)(void*,int*))0x37eb710)(root,xy);
 int x=placed?xy[0]:(sw-456)/2,y=placed?xy[1]:(sh-374)/2+25;
 x=clamp(x,12,sw-456-12);y=clamp(y,25,sh-338-11);
 ((void(__thiscall*)(void*,int,int))0x37ecc40)(root,x,y);
 screenWidth=sw;screenHeight=sh;placed=true;
}
static void __fastcall PanelUpdate(void*self,void*){
 ((void(__thiscall*)(void*))0x3361970)(self);
 sync();if(self==win&&shown&&ui()){placePanel();captureTick();}
}
static void openPanel(){
 if(failed)return;load();if(!uiThread)uiThread=KR_Thread();if(!ui())return;
 if(win&&!owns(true)&&!owns(false))forget();manager=*(void**)0x56a3798;
 if(!readable(manager,0xc640))return;
 if(shown&&owns(true))return;
 if(!win){const int slots[]={1214,1216,1213,992,1068,1065};bool found=false;
  for(unsigned i=0;i<6;i++)if(freeSlot(slots[i])){windowId=slots[i];found=true;break;}if(!found)return;
  creating=true;void*r=((void*(__thiscall*)(void*,int,int,int))0x32bea50)(manager,windowId,0,0);creating=false;
  if(!r||r!=win||!bind()){closePanel();failed=true;return;}
 }else if(((void*(__thiscall*)(void*,int,int,int))0x32bea50)(manager,windowId,0,0)!=win)return;
 draft=saved;capture=-1;waitRelease=true;physical(previousPhysical);shown=true;render();placePanel();status(L"点击按键格进行设置");resetRun();
}
extern "C" __declspec(dllexport) void* __cdecl Factory(int id,void*m){
 if(!creating||!ui()||id!=windowId||m!=manager)return ((void*(__cdecl*)(int,void*))KR_PREVIOUS_FACTORY)(id,m);
 void*memory=((void*(__cdecl*)(unsigned))0x37f2170)(0x1dc);if(!memory)return nullptr;void*answer;const wchar_t*path=panelPath;
 __asm {sub esp,20h
 mov ecx,esp
 push path
 mov eax,0045fea0h
 call eax
 push id
 push m
 mov ecx,memory
 mov eax,029bb9c0h
 call eax
 mov answer,eax}
 win=answer;return answer;
}
extern "C" __declspec(dllexport) bool __cdecl Layout(const wchar_t*path,void*doc){
 const unsigned char*xml=nullptr;
 if(pathEqual(path,menuPath))xml=kMainMenuXml;
 else if(creating&&ui()&&pathEqual(path,panelPath))xml=kKeyRepeatXml;
 if(!xml)return ((bool(__cdecl*)(const wchar_t*,void*))KR_PREVIOUS_LAYOUT)(path,doc);
 if(!readable(doc,0x80)||value(doc)!=0x4627bb4)return false;
 ((void(__thiscall*)(void*,const char*,void*,int))0x3e43eb0)(doc,(const char*)xml,nullptr,0);
 return !*((Byte*)doc+0x48)&&((void*(__thiscall*)(void*))0x3e3f700)(doc);
}
// Register an actual native menu row. Native show/draw/reflow/edit owns all
// geometry, text, glow and checkboxes; no independent overlay is retained.
static bool menuSelected=true,menuLoaded;
static void loadMenu(){
 if(menuLoaded)return;menuLoaded=true;load();unsigned state=1;
 if(configPath[0]&&KR_Read(L"KeyRepeat",L"MenuVisibleV1",&state,sizeof(state),configPath)&&state<=1)menuSelected=state!=0;
}
// Read-only lookup of the verified native RB tree (head +8, root head+4).
// Both value layouts share child/key fields. No synthetic STL nodes are made.
static void* menuNode(void* map,int key){
 if(!readable(map,16))return nullptr;void*head=(void*)value(map,8);
 if(!readable(head,16))return nullptr;void*n=(void*)value(head,4);
 for(unsigned i=0;n&&n!=head&&i<128;i++){
  if(!readable(n,24))return nullptr;int k=(int)value(n,12);if(k==key)return n;
  n=(void*)value(n,key<k?0:8);
 }return nullptr;
}
extern "C" __declspec(dllexport) void* __fastcall MenuRows(void*data,void*){
 // Called only after ALL native Show login/character/window rejection gates.
 loadMenu();if(!readable(data,16)||menuNode(data,7200))return data;
 unsigned pair[13]{},result[4]{};pair[0]=7200;
 ((void*(__thiscall*)(void*,const wchar_t*))0x45fea0)(pair+1,L"按键连发");
 pair[9]=30;pair[10]=31;pair[11]=30;pair[12]=48;
 ((void*(__thiscall*)(void*,void*,const void*))0x2287990)(data,result,pair);
 ((void(__thiscall*)(void*))0x1c5f110)(pair);
 return data;
}
// The native periodic Enable callback rejects unknown types above 46.
// Only our local settings row is additionally eligible; native Show/login,
// visibility, edit-mode hit area and all other feature predicates remain native.
extern "C" __declspec(dllexport) bool __cdecl MenuEnabled(int type){
 if(type==48)return true;
 return ((bool(__cdecl*)(int))0x12249e0)(type);
}
extern "C" __declspec(dllexport) bool __fastcall MenuSelected(void*data,void*,int type){
 if(type==48){loadMenu();return menuSelected;}
 // Exact original getter semantics for every existing native type.
 return *(const unsigned short*)((const char*)data+0x3e+type*4)==0;
}
extern "C" __declspec(dllexport) void __fastcall MenuSave(void*self,void*){
 loadMenu();void*n=menuNode((char*)self+0x254,7200);
 if(n){void*c=(void*)value(n,16);if(readable(c,0x579)){
  unsigned selected=((bool(__thiscall*)(void*))0x3840b50)(c)?1:0,back=2;
  if(configPath[0]&&KR_Write(L"KeyRepeat",L"MenuVisibleV1",&selected,sizeof(selected),configPath)&&
     KR_Read(L"KeyRepeat",L"MenuVisibleV1",&back,sizeof(back),configPath)&&back==selected)menuSelected=selected!=0;
 }}
 // Keep the native 47-entry / 188-byte network contract and bounds check.
 ((void(__thiscall*)(void*))0x1226370)(self);
}
// Only enum 48 extends checkbox creation and animated menu reflow. Enum 47
// and all other unsupported types keep the original rejection branch.
extern "C" __declspec(dllexport) __declspec(naked) void MenuCheckGate(){__asm{
 cmp dword ptr [ebx+3ch],30h
 je accepted
 cmp dword ptr [ebx+3ch],2fh
 jl accepted
 push 01227caah
 ret
accepted:
 push 01227cefh
 ret
}}
extern "C" __declspec(dllexport) __declspec(naked) void MenuReflowGate(){__asm{
 cmp dword ptr [ebx+3ch],30h
 je accepted
 cmp dword ptr [ebx+3ch],2fh
 jl accepted
 push 01228eb3h
 ret
accepted:
 push 01228c13h
 ret
}}
// Kept as explicit ABI regression targets; installed vtables now use the
// original Show and Update directly, with no post-layout repositioning.
extern "C" __declspec(dllexport) bool __fastcall MenuShow(void*self,void*,int arg1,int arg2){
 return ((bool(__thiscall*)(void*,int,int))0x1226c30)(self,arg1,arg2);
}
extern "C" __declspec(dllexport) void __fastcall MenuUpdate(void*self,void*){
 ((void(__thiscall*)(void*))0x1229c20)(self);
}
extern "C" __declspec(dllexport) int __fastcall MenuEvent(void*self,void*,int id,int event,int v,int extra){
 if(id==7200&&event==13){
  void*menu=(char*)self-0x1b0;manager=*(void**)0x56a3798;
  if(owned(menu,0x453,true)&&!*((Byte*)menu+0x1e0)){
   // Same native close path used by ordinary ESC menu actions.
   ((void(__thiscall*)(void*,int,int,int))0x32b94a0)(manager,0x453,-1,0);openPanel();
  }return 1;
 }
 return ((int(__thiscall*)(void*,int,int,int,int))0x1228760)(self,id,event,v,extra);
}
extern "C" __declspec(dllexport) void __cdecl Input(void* keyboard,int result){
 if(ui())sync();load();Byte p[256]{};for(unsigned i=0;i<Slots;i++)if(saved.slot[i].enabled)p[saved.slot[i].vk]=(KR_Async(saved.slot[i].vk)&0x8000)?1:0;
 bool active=result>=0&&*((Byte*)keyboard+4)&&*((Byte*)keyboard+0xc)&&!value(keyboard,0x1310)&&foreground()&&!shown;
 if(active&&!((void*(__cdecl*)())0x29610e0)())active=false;
 if(active){void*main=*(void**)0x57e2698;if(main&&readable(main,8)&&!((bool(__thiscall*)(void*))0x335aa00)(main))active=false;}
 step(saved,runs,KR_Tick(),active,p,(Byte*)keyboard+0x10d,scanMap());
}
extern "C" __declspec(dllexport) __declspec(naked) void InputGate(){__asm{
 pushfd
 pushad
 push eax
 push ecx
 call Input
 add esp,8
 popad
 popfd
 mov edx,03840a30h
 jmp edx
}}
