// Statically merged into the independent EXE. No DLL, HTTP, injector or worker.
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#include <initializer_list>
#include <intrin.h>
#pragma intrinsic(_InterlockedCompareExchange, _InterlockedExchange)
#include "winapi.h"
#include "layout.h"
#include "native_contract.h"
#include "skin_mapping.h"
extern "C" void* __cdecl memset(void* p,int v,size_t n){auto b=(unsigned char*)p;while(n--)*b++=(unsigned char)v;return p;}
extern "C" void* __cdecl memcpy(void* d,const void* s,size_t n){auto a=(unsigned char*)d;auto b=(const unsigned char*)s;while(n--)*a++=*b++;return d;}
static bool equal(const void* a,const void* b,unsigned n){auto x=(const unsigned char*)a,y=(const unsigned char*)b;while(n--)if(*x++!=*y++)return false;return true;}
static bool wequal(const wchar_t* a,const wchar_t* b){while(*a&&*a==*b){a++;b++;}return *a==*b;}
static uintptr_t value(const void*p,unsigned o=0){return *(const uintptr_t*)((const char*)p+o);}
static bool readable(const void*p,unsigned n){MEMORY_BASIC_INFORMATION m{};if(!p||!AP_VirtualQuery(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;uintptr_t a=(uintptr_t)p;uintptr_t end=(uintptr_t)m.BaseAddress+m.RegionSize;return a<=end&&n<=end-a;}
static DWORD uiThread,lastQuery,pendingSince;static unsigned nonce,pendingNonce,pendingClaim,selectedTask;
static bool busy,registered,constructing,visible,hasState,fault,windowPlaced;static void* window;static void* manager;static void* root;static void* property;static int windowId;
static unsigned char state[184];static void* rows[8];static void* prizes[3];static void* claims[3];static void* closeButton;static void* closeTopButton;static void* refreshButton;
static volatile long responseLock;static bool responseReady;static unsigned char response[184];
static bool owns(bool active);
static const wchar_t layoutPath[]=L"UI/PrivateActivity017/Activeness.xui";
static const wchar_t* taskNames[]={L"完成深渊派对",L"通关魔界裂缝",L"通关艾肯副本",L"完成团队攻坚",L"开启赛丽亚的幸运",L"消耗100点疲劳值",L"通关异次元裂缝",L"通关史诗之路"};
static const wchar_t* taskDetails[]={L"击败任意一只深渊怪物即可完成。",L"成功通关一次魔界裂缝。",L"成功通关一次艾肯副本。",L"完成卢克或超时空团队攻坚。",L"开启任一赛丽亚的幸运一次。",L"今日累计实际消耗100点疲劳值。",L"成功通关一次异次元裂缝。",L"成功通关一次史诗之路。"};
static unsigned get16(const unsigned char*b,unsigned o){return b[o]|(unsigned)b[o+1]<<8;}
static unsigned get32(const unsigned char*b,unsigned o){return get16(b,o)|get16(b,o+2)<<16;}
static void put16(unsigned char*b,unsigned o,unsigned v){b[o]=(unsigned char)v;b[o+1]=(unsigned char)(v>>8);}
static void put32(unsigned char*b,unsigned o,unsigned v){put16(b,o,v);put16(b,o+2,v>>16);}
static bool ui(){return uiThread&&AP_GetCurrentThreadId()==uiThread;}
static BOOL CALLBACK findWindow(HWND h,LPARAM param){DWORD pid=0;DWORD thread=AP_GetWindowThreadProcessId(h,&pid);RECT rc{};if(pid==AP_GetCurrentProcessId()&&!AP_GetWindow(h,GW_OWNER)&&AP_IsWindowVisible(h)&&AP_GetClientRect(h,&rc)&&rc.right>=400&&rc.bottom>=300){*(DWORD*)param=thread;return FALSE;}return TRUE;}
static bool contracts(){for(const auto&c:kCodeChecks){if(c.address==0x32becc0||c.address==0x29bbee3)continue;if(!readable((void*)c.address,(unsigned)c.length)||!equal((void*)c.address,c.bytes,(unsigned)c.length))return false;}for(const auto&c:kIconChecks)if(!readable((void*)c.address,(unsigned)c.length)||!equal((void*)c.address,c.bytes,(unsigned)c.length))return false;return true;}
struct Ref{void*value;void*counter;};
static void* find(const wchar_t*name){Ref ref{};((Ref*(__thiscall*)(void*,Ref*,const wchar_t*))0x37eb120)(root,&ref,name);void*p=ref.value;((void(__thiscall*)(Ref*))0xf11f90)(&ref);return p;}
static void* indexed(const wchar_t*prefix,unsigned i){wchar_t name[64];AP_wsprintfW(name,L"%s%u",prefix,i);return find(name);}
static void text(const wchar_t*name,const wchar_t*str){if(!visible||!root||!owns(true))return;void*p=find(name);if(!readable(p,0x410)||value(p)!=0x4d4fa4c){fault=true;return;}((void(__thiscall*)(void*,const wchar_t*))0x3835940)(p,str);}
// Measure through CNUIControlText's actual font; position in its parent's
// logical space. Three/four-character labels must not share a fixed left edge.
// 0x3835890 calls the bound font's width method; 0x37ea190 updates local
// coordinates and invokes the native position propagation virtual method.
static __declspec(noinline) void centerText(const wchar_t*name,const wchar_t*str,void*button){
 text(name,str);if(!visible||!root||!owns(true)||!readable(button,0x84))return;
 void*p=find(name);if(!readable(p,0x410)||value(p)!=0x4d4fa4c){fault=true;return;}
 int width=((int(__thiscall*)(void*))0x3835890)(p);
 int boxWidth=(int)value(button,0x6c),boxHeight=(int)value(button,0x70);
 if(width<0||width>boxWidth||boxHeight<12)return;
 ((void(__thiscall*)(void*,int,int))0x37ea190)(p,(int)value(button,0x64)+(boxWidth-width)/2,(int)value(button,0x68)+(boxHeight-12)/2);
}
static void show(void*p,bool on){if(p&&readable(p,4)){auto t=*(void***)p;((void(__thiscall*)(void*,bool))t[2])(p,on);}}
static bool owns(bool active){if(!window||!readable(manager,0xc640)||*(void**)0x56a3798!=manager)return false;auto h=(char*)manager+(windowId*5+(active?0x41:0x1879))*4;auto b=value(h,4),e=value(h,8);if(e<b||e-b>64||(e-b)%4||!readable((void*)b,(unsigned)(e-b)))return false;for(;b<e;b+=4)if(*(void**)b==window)return true;return false;}
static void forget(){window=root=nullptr;visible=hasState=windowPlaced=false;pendingNonce=0;nonce++;}
// Native title-bar X closes through the manager rather than our Event callback.
// Keep its cached window and position for reopening; forget only invalid objects.
static __declspec(noinline) void syncWindow(){if(visible&&!owns(true)){if(owns(false))visible=false;else forget();}}
static bool emptySlot(int id){if(!readable(manager,0xc640)||!value(manager,0xc5b4))return false;for(unsigned o: {0x41u,0x1879u}){auto h=(char*)manager+(id*5+o)*4;if(value(h,4)!=value(h,8))return false;}return value((void*)0x3373f58,id*4)==0x3373f41;}
// Native buttons also serve as non-interactive image-state controls. All frames
// contain only decoration/pictograms; all visible labels use native game text.
// Native CNButton::Draw (0x38585c0) passes fields 0x484/0x488 to
// DrawImageEx for normal/hover/down/disabled states. No global UI scale changes.
// SetFrames restores full image dimensions, so reset the logical hit box after it.
static __declspec(noinline) void compact(void*p,unsigned n){
 if(n>=433||!readable(p,0x504)){fault=true;return;}
 *(unsigned*)((char*)p+0x484)=0x3f000000;*(unsigned*)((char*)p+0x488)=0x3f000000; // IEEE-754 0.5: 2x decoration pixels, text remains native
 ((void(__thiscall*)(void*,int,int))0x38271c0)(p,kFrameSizes[n][0],kFrameSizes[n][1]);
}
static void frame(void*p,unsigned up,unsigned over,unsigned down){if(!readable(p,0x504)){fault=true;return;}((void(__thiscall*)(void*,int,int,int,int,int))0x3855fc0)(p,up,over,down,up,-1);compact(p,up);}
static void frame(void*p,unsigned n){frame(p,n,n,n);}
static bool skinMatches(){
 if(!hasState||get16(state,12)>220||get16(state,14)!=100)return false;
 const unsigned thresholds[]={30,50,100},coins[]={500,500,5500};
 for(unsigned i=0;i<3;i++)if(get16(state,114+i*24)!=thresholds[i]||get32(state,116+i*24)!=coins[i]||get32(state,120+i*24)!=990027401+i||get32(state,124+i*24)!=1||get32(state,128+i*24)!=3330||get32(state,132+i*24)!=500)return false;
 const unsigned points[]={10,10,50,100,10,20,10,10};
 for(unsigned i=0;i<8;i++)if(get16(state,50+i*8)!=(i==5?100:1)||get16(state,52+i*8)!=points[i])return false;
 return true;
}
static bool claimable(){if(!skinMatches())return false;for(unsigned i=0;i<3;i++)if(!(state[16]&(1<<i))&&get16(state,12)>=get16(state,114+i*24))return true;return false;}
static void render(){if(!visible||!root||!owns(true))return;bool mapped=skinMatches();unsigned points=get16(state,12);
 frame(find(L"AP_SCORE"),mapped?kScoreBase+points:kScoreUnknown);
 wchar_t label[128],name[64];if(mapped)AP_wsprintfW(label,L"今日活跃值 %u 点",points);else AP_wsprintfW(label,L"今日活跃值 -- 点");text(L"AP_POINTS_TEXT",label);
 frame(find(L"AP_GAUGE"),kGaugeBase+(mapped?(points>100?100:points):0));
 for(unsigned i=0;i<8;i++){
  bool done=mapped&&get16(state,54+i*8)==get16(state,50+i*8);unsigned off=done?3:0;bool sel=selectedTask==i;
  frame(rows[i],kRowFrames[i][off+(sel?2:0)],kRowFrames[i][off+(sel?2:1)],kRowFrames[i][off+2]);
  AP_wsprintfW(name,L"AP_ROW_PROGRESS_%u",i);if(mapped&&!done)AP_wsprintfW(label,L"+%u",get16(state,52+i*8));text(name,!mapped?L"--":done?L"完成":label);
 }
 for(unsigned i=0;i<3;i++){
  bool claimed=mapped&&(state[16]&(1<<i));bool ready=mapped&&!claimed&&points>=get16(state,114+i*24);
  unsigned card=!mapped?4:claimed?2:ready?((pendingNonce&&pendingClaim==i+1)?3:1):0;
  frame(prizes[i],kCardFrames[i][card]);show(claims[i],ready&&!pendingNonce);
  AP_wsprintfW(name,L"AP_PRIZE_STATE_%u",i);centerText(name,!mapped?L"--":claimed?L"已领取":card==3?L"领取中":ready?L"领取奖励":L"未达成",claims[i]);
 }
 frame(find(L"AP_DETAIL"),kDetailBase+selectedTask);
 wchar_t detail[128];if(mapped&&selectedTask==5){AP_wsprintfW(detail,L"今日已消耗 %u / 100 点疲劳值",get16(state,54+5*8));text(L"AP_DETAIL_TEXT",detail);}else text(L"AP_DETAIL_TEXT",taskDetails[selectedTask]);
 text(L"AP_PROGRESS",L"");
 centerText(L"AP_LABEL_refresh",L"刷新",refreshButton);centerText(L"AP_LABEL_close",L"关闭",closeButton);
 centerText(L"AP_LABEL_festival",L"礼包赠礼",find(L"AP_FESTIVAL"));
}
static void select(unsigned i){if(i<8){selectedTask=i;render();}}
static void query(unsigned stage){if(!ui()||fault||pendingNonce)return;if(stage&&(!visible||!owns(true)||!skinMatches()||stage>3||(state[16]&(1<<(stage-1)))||get16(state,12)<get16(state,114+(stage-1)*24)))return;
 lastQuery=AP_GetTickCount();
 void*net=((void*(__cdecl*)())0x374ea10)();if(!readable(net,0x2bd06c)||!*((unsigned char*)net+8)||value(net,0x2bcc34)||value(net,0x2bcc2c)){hasState=false;render();text(L"AP_STATUS",L"暂时无法加载，请稍后重试。");return;}
 unsigned char b[48]{};b[0]='A';b[1]='P';b[2]='Q';b[3]='2';put16(b,4,2);put16(b,6,stage);if(stage){put16(b,8,get16(state,10));put32(b,12,get32(state,20));memcpy(b+16,state+24,16);}if(++nonce==0)++nonce;put32(b,32,nonce);
 if(((int(__thiscall*)(void*,unsigned))0x374f280)(net,0x7f01)!=1){text(L"AP_STATUS",L"暂时无法加载，请稍后重试。");return;}
 for(unsigned i=0;i<48;i+=4)((int(__thiscall*)(void*,unsigned))0x374f3f0)(net,get32(b,i));pendingNonce=nonce;pendingClaim=stage;pendingSince=lastQuery=AP_GetTickCount();render();text(L"AP_STATUS",stage?L"领取中……":L"");((int(__cdecl*)())0x3750ac0)();
}
extern "C" __declspec(dllexport) void __cdecl Receive(){unsigned remaining=*(unsigned*)0x57f9ad4;void*body=*(void**)0x57f9ad0;if(remaining!=184||!readable(body,184))return;unsigned char b[184];((void(__cdecl*)(void*,unsigned))0x37e8430)(b,184);if(_InterlockedCompareExchange(&responseLock,1,0))return;memcpy(response,b,184);responseReady=true;_InterlockedExchange(&responseLock,0);}
static void invalidResponse(){hasState=false;render();text(L"AP_STATUS",L"暂时无法加载，请稍后重试。");}
static void applyResponse(){unsigned char b[184];if(_InterlockedCompareExchange(&responseLock,1,0))return;bool ready=responseReady;if(ready){memcpy(b,response,184);responseReady=false;}_InterlockedExchange(&responseLock,0);if(!ready||!ui()||!pendingNonce||get32(b,40)!=pendingNonce)return;pendingNonce=0;hasState=false;
 if(!equal(b,"APS2",4)||get16(b,4)!=2||get16(b,6)!=184){invalidResponse();return;}
 if(get16(b,8)){render();text(L"AP_STATUS",get16(b,8)==2?L"活跃值不足。":L"暂时无法领取，请稍后重试。");return;}
 if(!get16(b,10)||!get16(b,14)||b[16]>7||!get32(b,20)||!(get32(b,24)|get32(b,28))||!(get32(b,32)|get32(b,36))){invalidResponse();return;}
 for(unsigned i=0;i<8;i++)if(get16(b,48+i*8)!=i+1||!get16(b,50+i*8)||get16(b,54+i*8)>get16(b,50+i*8)){invalidResponse();return;}
 for(unsigned i=0;i<3;i++)if(get16(b,112+i*24)!=i+1||!get16(b,114+i*24)||get32(b,120+i*24)!=990027401+i||get32(b,124+i*24)!=1||(get32(b,128+i*24)&&get32(b,128+i*24)!=3330)){invalidResponse();return;}
 memcpy(state,b,184);hasState=true;render();if(!skinMatches()){text(L"AP_STATUS",L"奖励配置已更新，请更新客户端。");return;}text(L"AP_STATUS",pendingClaim?(get32(b,44)?L"已领取。":L"领取成功。"):L"");
}
#include "festival_ui.h"
static void close(){if(window&&owns(true))((void(__thiscall*)(void*,int,int,int))0x32b94a0)(manager,windowId,-1,0);visible=false;}
static int __fastcall Event(void*,void*,int sender,int kind,int,int){if(kind!=13||!ui()||fault||!owns(true))return 0;auto p=(void*)sender;if(p==closeButton||p==closeTopButton){close();return 1;}if(p==find(L"AP_FESTIVAL")){close();festivalui::open();return 1;}if(p==refreshButton){if(!pendingNonce)query(0);return 1;}for(unsigned i=0;i<8;i++)if(p==rows[i]){select(i);return 1;}for(unsigned i=0;i<3;i++)if(p==claims[i]){query(i+1);return 1;}return 0;}
static void* eventTable[]={(void*)&Event};
static bool bind(){
 if(!readable(window,0x1dc)||value(window)!=0x49f03f4||value(window,0x1b0)!=0x49f03ec)return false;
 root=(void*)value(window,0x30);if(!readable(root,0x100))return false;
 closeButton=find(L"AP_CLOSE");closeTopButton=(void*)value(window,0x50);refreshButton=find(L"AP_REFRESH");
 if(!closeButton||!closeTopButton||!refreshButton||!find(L"AP_SCORE")||!find(L"AP_GAUGE")||!find(L"AP_DETAIL"))return false;
 for(unsigned i=0;i<8;i++){rows[i]=indexed(L"AP_ROW_",i);if(!readable(rows[i],0x504))return false;}
 for(unsigned i=0;i<3;i++){prizes[i]=indexed(L"AP_PRIZE_",i);claims[i]=indexed(L"AP_CLAIM_",i);if(!readable(prizes[i],0x504)||!readable(claims[i],0x504))return false;show(claims[i],false);}
 // Use the original popup title and close controls (loader at 0x29bb672).
 void*title=(void*)value(window,8);if(!readable(title,0x60))return false;
 *((unsigned char*)title+0x44)=1;*((unsigned char*)title+0x45)=1;
 for(const auto&c:kSkinControls){void*p=find(c.name);if(!readable(p,0x504))return false;compact(p,c.frame);}
 compact(find(L"AP_FESTIVAL"),424);
 for(const auto&c:kTextControls){void*p=find(c.name);if(!readable(p,0x410)||value(p)!=0x4d4fa4c)return false;((void(__thiscall*)(void*,unsigned))0x3835710)(p,c.color);}
 *(void***)((char*)window+0x1b0)=eventTable;
 ((void(__thiscall*)(void*,int,int))0x38271c0)(root,kSkinWidth,kSkinHeight);return true;
}
extern "C" __declspec(dllexport) void* __cdecl Factory(int id,void*m){if(festivalui::factoryRequest(id,m))return festivalui::factory(id,m);if(!constructing||id!=windowId||m!=manager||!ui())return ((void*(__cdecl*)(int,void*))0x3365ba0)(id,m);void*memory=((void*(__cdecl*)(unsigned))0x37f2170)(0x1dc);if(!memory)return nullptr;void*answer;const wchar_t*path=layoutPath;
 __asm {
  sub esp,20h
  mov ecx,esp
  push path
  mov eax,0045fea0h
  call eax
  push id
  push m
  mov ecx,memory
  mov eax,029bb9c0h
  call eax
  mov answer,eax
 }
 window=answer;return answer;}
extern "C" __declspec(dllexport) bool __cdecl Layout(const wchar_t*path,void*doc){if(festivalui::layoutRequest(path))return festivalui::layout(doc);if(!constructing||!ui()||!path||!wequal(path,layoutPath))return ((bool(__cdecl*)(const wchar_t*,void*))0x382ab90)(path,doc);if(!readable(doc,0x80)||value(doc)!=0x4627bb4)return false;((void(__thiscall*)(void*,const char*,void*,int))0x3e43eb0)(doc,(const char*)kLayoutXmlBytes,nullptr,0);return !*((unsigned char*)doc+0x48)&&((void*(__thiscall*)(void*))0x3e3f700)(doc);}
// Original TileType 0 uses WindowCommon 94..105, plus its native title and X.
// Native dimensions are TILE COUNTS, not pixels: 34*12 by 30*13 content.
// The original title's parent-drag mode moves the whole control tree.
static __declspec(noinline) bool fitWindow(){
 if(!readable(window,0x1dc)||!readable(root,0x100))return false;
 void*props=(void*)value(window,4);if(!readable(props,0x4c))return false;
 void*title=(void*)value(window,0x48);void*x=(void*)value(window,0x50);
 if(!readable(title,0x140)||!readable(x,0x504))return false;
 *(unsigned*)((char*)props+0x24)=kNativeColumns;*(unsigned*)((char*)props+0x28)=kNativeRows;
 *(unsigned*)((char*)props+0x44)=12;*(unsigned*)((char*)props+0x48)=13;
 *((unsigned char*)props+0x3b)=1;
 ((void(__thiscall*)(void*,int))0x335f0d0)(window,0);
 ((void(__thiscall*)(void*,bool))0x3826f40)(title,true);
 show(title,true);show(x,true);void*accessory=(void*)value(window,0x58);if(accessory)show(accessory,false);
 if(!windowPlaced){((void(__thiscall*)(void*,int,int,int,int))0x335e970)(window,1,1,0,7);windowPlaced=true;}
 return true;
}
static void open(){if(!ui()||fault||!contracts())return;if(window&&!owns(false))forget();manager=*(void**)0x56a3798;if(!window){bool free=false;for(int id:{1065,1068,1213,1214,1216,992})if(emptySlot(id)){windowId=id;free=true;break;}if(!free)return;constructing=true;void*r=((void*(__thiscall*)(void*,int,int,int))0x32bea50)(manager,windowId,0,0);constructing=false;if(!r||r!=window||!bind()){fault=true;return;}}if(((void*(__thiscall*)(void*,int,int,int))0x32bea50)(manager,windowId,0,0)!=window)return;if(!fitWindow()){fault=true;return;}visible=true;select(0);render();if(!pendingNonce)query(0);}
static void __cdecl IconClick(unsigned type,int){if(type==kActivityHudId)open();}
static bool mapValue(void*m,unsigned key,uintptr_t&out){out=0;if(!readable(m,20))return false;auto head=value(m,12),count=value(m,16);if(count>4096||!readable((void*)head,16))return false;auto node=value((void*)head),last=head;for(unsigned i=0;i<count;i++){if(node==head||!readable((void*)node,16)||value((void*)node,4)!=last)return false;if(*(uint16_t*)(node+8)==key)out=value((void*)node,12);last=node;node=value((void*)node);}return node==head&&value((void*)head,4)==last;}
// Native button sprites preserve their 20x20 logical hit rectangle. The ready
// sprite's original game balloon/gift extend above it through IMG offsets.
// No second HUD slot, extra window, or independent mouse hook is created.
static void updateBadge(){void*m=(void*)0x5689bb0;uintptr_t p=0;if(!property||!mapValue((char*)m+0x3c,kActivityHudId,p)||p!=(uintptr_t)property)return;void*icon=((void*(__thiscall*)(void*,unsigned))0x226d8c0)(m,kActivityHudId);if(!readable(icon,0x1c))return;void*button=(void*)value(icon);if(!readable(button,0x504))return;static void*boundButton;static bool boundReady;static uintptr_t boundFrame;bool ready=claimable();
 // Native HUD factory 0x2270265 passes an IMG resource directly. Its button
 // constructor leaves the filename at +0x4e4 empty, even with valid sprites.
 // Resolve the same native resource constant as that factory, and rebind
 // after a HUD rebuild, including reuse of the same button address.
 if(button==boundButton&&ready==boundReady&&value(button,0x4f8)&&boundFrame&&value(button,0x3bc)==boundFrame)return;
 const wchar_t*originalPack=((const wchar_t*(__cdecl*)(const void*))0x37de240)((const void*)0x4987cd8);
 if(!readable(originalPack,16)||!originalPack[0])return;
 bool spritePrefix=(originalPack[0]==L'S'||originalPack[0]==L's')&&(originalPack[6]==L'/'||originalPack[6]==L'\\');
 ((void(__thiscall*)(void*,const wchar_t*))0x3855f80)(button,ready?(spritePrefix?L"Sprite/Interface/PrivateActivity010/Activity.img":L"Interface/PrivateActivity010/Activity.img"):originalPack);unsigned frame=ready?kClaimFrame010:13;
 ((void(__thiscall*)(void*,int,int,int,int,int))0x3855fc0)(button,frame,frame+1,frame+2,-1,-1);
 boundButton=button;boundReady=ready;boundFrame=value(button,0x3bc);
}
static void install(){void*m=(void*)0x5689bb0;void*callbacks=(void*)0x5689b54;uintptr_t p=0,c=0;if(!readable(m,0xc8)||!*(unsigned char*)m||!mapValue((char*)m+0x3c,kActivityHudId,p)||!mapValue(callbacks,kActivityHudId,c))return;if((p&&p!=(uintptr_t)property)||(c&&c!=(uintptr_t)&IconClick))return;
 if(!p){void*mem=((void*(__cdecl*)(unsigned))0x8a9480)(0x13c);if(!mem)return;memset(mem,0,0x13c);property=((void*(__thiscall*)(void*))0x20484b0)(mem);*(unsigned*)property=kActivityHudId;*(unsigned*)((char*)property+4)=13;*(unsigned*)((char*)property+0xfc)=0xffffffff;struct{unsigned key;void*value;}pair{kActivityHudId,property};unsigned char out[32]{};((void*(__thiscall*)(void*,void*,const void*))0x226f240)((char*)m+0x3c,out,&pair);}
 if(!c){struct{unsigned key;void*value;}pair{kActivityHudId,(void*)&IconClick};unsigned char out[32]{};((void*(__thiscall*)(void*,void*,const void*))0x224df00)(callbacks,out,&pair);}
 ((void(__thiscall*)(void*,unsigned))0x2270560)(m,kActivityHudId);
 // The native manager owns both the button and its tooltip. Mode 1 maps to
 // native state 2: draw only when this visible button is the hovered control.
 void*icon=((void*(__thiscall*)(void*,unsigned))0x226d8c0)(m,kActivityHudId);
 if(readable(icon,0x1c)){
  void*tip=(void*)value(icon,8);static void*boundTooltip;
  if(readable(tip,0x3c0)){
   void*data=(void*)value(tip,0x3bc);
   if(readable(data,0x108)&&(tip!=boundTooltip||value(data,4)!=2)){
    ((void(__thiscall*)(void*,unsigned,const wchar_t*,unsigned))0x2270620)(m,kActivityHudId,L"每日活跃度",1);
    boundTooltip=tip;
   }
  }
 }
}
extern "C" __declspec(dllexport) void __cdecl Tick(){if(busy||fault)return;busy=true;if(!uiThread){auto enumeration=(decltype(&EnumWindows))AP_GetProcAddress(AP_GetModuleHandleW(L"user32.dll"),"EnumWindows");if(enumeration)enumeration(findWindow,(LPARAM)&uiThread);}if(!ui()){busy=false;return;}static DWORD lastTick;DWORD tickTime=AP_GetTickCount();if(tickTime-lastTick<100){busy=false;return;}lastTick=tickTime;if(contracts()){if(!registered){void*m=((void*(__cdecl*)())0x1fa2790)();((void(__thiscall*)(void*,unsigned,void*,unsigned))0x251e280)(m,0x7f01,(void*)&Receive,0);((void(__thiscall*)(void*,unsigned,void*,unsigned))0x251e280)(m,0x7f02,(void*)&festivalui::ReceiveFestival,0);registered=true;}install();syncWindow();applyResponse();unsigned now=AP_GetTickCount();if(pendingNonce&&now-pendingSince>8000){pendingNonce=0;lastQuery=now;invalidResponse();}if(property&&!pendingNonce&&now-lastQuery>5000)query(0);updateBadge();festivalui::tick();}busy=false;}
extern "C" __declspec(dllexport) __declspec(naked) void GetterHook(){__asm{
 pushfd
 pushad
 call Tick
 popad
 popfd
 mov eax,05689bb0h
 ret
}}
extern "C" __declspec(dllexport) __declspec(naked) void ReceiveGate(){__asm{
 cmp byte ptr[edi],0
 jne oldPath
 cmp word ptr[esp+4],7f01h
 je privatePath
 cmp word ptr[esp+4],7f02h
 jne oldPath
privatePath:
 mov eax,1
 ret 4
oldPath:
 movzx eax,word ptr[esp+4]
 push 06ea8005h
 ret
}}

// Entered from the original calendar click body, after its SEH frame is set.
// Unknown events retain the upstream PCRoom228 override at 0x6ea4000.
extern "C" int __cdecl CalendarClick(void*);
extern "C" int __cdecl CalendarFromCalendar(void*,void*,unsigned);
extern "C" __declspec(dllexport) __declspec(naked) void CalendarGate(){__asm{
 pushfd
 pushad
 push edi
 call CalendarClick
 add esp,4
 test eax,eax
 jz nativeCalendar
 mov dword ptr [ebp+8],eax
 popad
 popfd
 push 023ee326h
 ret
nativeCalendar:
 popad
 popfd
 push 06ea4000h
 ret
}}

// EventCalendar.xui uses a distinct native click function from the event list.
// This gate is after its SEH prologue and before the original window dispatch.
extern "C" __declspec(dllexport) __declspec(naked) void EventCalendarGate(){__asm{
 pushfd
 pushad
 push dword ptr [ebp+4]
 push ebx
 push edi
 call CalendarFromCalendar
 add esp,12
 test eax,eax
 jz nativeEventCalendar
 mov dword ptr [ebp-014h],eax
 popad
 popfd
 push 017c3c55h
 ret
nativeEventCalendar:
 popad
 popfd
 mov eax,dword ptr [edi+0140h]
 push 017c39d2h
 ret
}}
