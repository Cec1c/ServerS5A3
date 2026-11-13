// Additive native integration for the exact a28601e3 cumulative client.
// Existing growth/activity/ranking dispatch and native wear/pickup validation
// remain in their original call chains. No external process or OS input.
#include "native_ui.h"
#include "repeat_core.h"
#include "layouts.h"
#include "binding.h"
#include <initializer_list>
#include "plugin_bridge.h"
#include "native_coupon.h"

extern "C" void* __cdecl memset(void*p,int c,size_t n){auto b=(Byte*)p;while(n--)*b++=(Byte)c;return p;}
extern "C" void* __cdecl memcpy(void*d,const void*s,size_t n){auto a=(Byte*)d;auto b=(const Byte*)s;while(n--)*a++=*b++;return d;}
static Panel panels[6]={
    {0,636,286,53,22,L"UI/PrivateFeatures20261112/Keyboard.xui",kKeyboardXml},
    {0,510,290,42,22,L"UI/PrivateFeatures20261112/Swap.xui",kSwapXml},
    {0,276,260,23,20,L"UI/PrivateFeatures20261112/Dps.xui",kDpsXml},
    {0,120,338,10,26,L"UI/PrivateFeatures20261112/Power.xui",kPowerXml},
    {0,320,238,27,19,L"UI/PrivateFeatures20261112/PowerGuide.xui",kPowerGuideXml},
    {0,220,242,19,19,L"UI/PrivateFeatures20261112/PowerHelp.xui",kPowerHelpXml}
};
static Panel*creating;
static DWORD uiThread;
static wchar_t configPath[512];
static bool loaded,busy,swapMenuSelected=true,dpsMenuSelected=true;
static bool tickStarted;
static unsigned lastFeatureTick;
static fullrepeat::Config saved,draft;
static fullrepeat::Run runs[256];
static Byte latestPhysical[256];
static void*keyOff[104],*keyOn[104];
static const Byte*scanMap(){return (const Byte*)0x6e8c000;}
bool ui(){return uiThread&&Thread()==uiThread;}
static bool foreground(){return value((void*)0x584678c)&&value((void*)0x584678c)==value((void*)0x5846770);}
static bool modal(){return panels[0].shown||panels[1].shown;}
static BOOL CALLBACK findWindow(HWND w,LPARAM out){
    DWORD pid=0;DWORD t=API(GetWindowThreadProcessId,0x6e8672c)(w,&pid);RECT r{};
    if(pid==API(GetCurrentProcessId,0x6e85a68)()&&!API(GetWindow,0x6e86728)(w,GW_OWNER)&&API(IsWindowVisible,0x6e866f0)(w)&&API(GetClientRect,0x6e866d8)(w,&r)&&r.right-r.left>=400&&r.bottom-r.top>=300){*(DWORD*)out=t;return FALSE;}return TRUE;
}
static void load(){
    if(loaded)return;loaded=true;fullrepeat::defaults(saved);
    auto env=(decltype(&GetEnvironmentVariableW))Proc(Module(L"kernel32.dll"),"GetEnvironmentVariableW");
    if(!env)return;DWORD n=env(L"LOCALAPPDATA",configPath,470);if(!n||n>=470){configPath[0]=0;return;}
    const wchar_t folder[]=L"\\DNF90";for(unsigned i=0;i<sizeof(folder)/2;i++)configPath[n+i]=folder[i];Mkdir(configPath,nullptr);n+=6;
    const wchar_t file[]=L"\\KeyRepeat.ini";for(unsigned i=0;i<sizeof(file)/2;i++)configPath[n+i]=file[i];
    fullrepeat::Config c{};
    if(ProfileRead(L"KeyRepeat",L"FullKeyboardV2",&c,sizeof(c),configPath)&&fullrepeat::valid(c,scanMap()))saved=c;
    else {fullrepeat::Legacy old{};if(ProfileRead(L"KeyRepeat",L"SlotsV1",&old,sizeof(old),configPath))fullrepeat::migrate(old,saved,scanMap());}
    unsigned state=1;if(ProfileRead(L"KeyRepeat",L"SwapMenuVisibleV1",&state,sizeof(state),configPath)&&state<=1)swapMenuSelected=state!=0;
    state=1;if(ProfileRead(L"KeyRepeat",L"DpsMenuVisibleV1",&state,sizeof(state),configPath)&&state<=1)dpsMenuSelected=state!=0;
}
bool owned(const Panel&p,bool active){
    if(!p.win||!readable(p.manager,0xc640)||*(void**)0x56a3798!=p.manager||p.id<=0||p.id>=0x4d8)return false;
    auto h=(char*)p.manager+(p.id*5+(active?0x41:0x1879))*4;auto b=value(h,4),e=value(h,8);
    if(e<b||e-b>64||(e-b)%4||(b!=e&&!readable((void*)b,(unsigned)(e-b))))return false;
    for(;b<e;b+=4)if(*(void**)b==p.win)return true;return false;
}
static void resetRun(){fullrepeat::step(saved,runs,Time(),false,latestPhysical,nullptr);}
void closePanel(Panel&p){if(owned(p,true))((void(__thiscall*)(void*,int,int,int))0x32b94a0)(p.manager,p.id,-1,0);p.shown=false;resetRun();}
static void place(Panel&p){
    if(&p==&panels[3])return;
    if(!p.shown||!owned(p,true)||!readable(p.root,0x84))return;
    int sw=*(int*)0x55b0318,sh=*(int*)0x55b031c;if(sw<p.width+24||sh<p.height+36)return;
    if(p.placed&&sw==p.screenWidth&&sh==p.screenHeight)return;
    int xy[2];((void*(__thiscall*)(void*,int*))0x37eb710)(p.root,xy);
    int x=p.placed?xy[0]:(sw-p.width)/2,y=p.placed?xy[1]:(sh-p.height-36)/2+25;
    ((void(__thiscall*)(void*,int,int))0x37ecc40)(p.root,clamp(x,12,sw-p.width-12),clamp(y,25,sh-p.height-11));
    p.screenWidth=sw;p.screenHeight=sh;p.placed=true;
}
static void renderKeyboard(){
    Panel&p=panels[0];if(!p.shown||!owned(p,true))return;unsigned count=0;
    for(unsigned i=0;i<104;i++){bool on=draft.enabled[kKeyScan[i]]!=0;visible(keyOn[i],on);visible(keyOff[i],!on);if(on)count++;}
    wchar_t s[48];Format(s,L"已启用 %u 个键",count);text(control(p,L"KR_COUNT"),s);
}
static bool saveKeyboard(){
    fullrepeat::Config back{};Panel&p=panels[0];
    if(!fullrepeat::valid(draft,scanMap())){status(p,L"按键配置无效");return false;}
    if(!configPath[0]||!ProfileWrite(L"KeyRepeat",L"FullKeyboardV2",&draft,sizeof(draft),configPath)||!ProfileRead(L"KeyRepeat",L"FullKeyboardV2",&back,sizeof(back),configPath)||!equal(&draft,&back,sizeof(back))){status(p,L"保存失败，请重试");return false;}
    saved=draft;resetRun();closePanel(p);return true;
}
void swapEvent(unsigned,unsigned);
void swapControl(int,int);
void swapKeys(const Byte*,bool);
void swapTick();
void renderSwap();
void featureTick();
static void clearDamage();
static bool dpsDismissed;
void powerEvent(Panel&,void*);
static int __fastcall PanelEvent(void*self,void*,int sender,int kind,int argument,int){
    if(!ui()||kind!=13)return 0;Panel*p=nullptr;
    for(auto& v:panels)if((char*)v.win+0x1b0==self&&v.shown&&owned(v,true)){p=&v;break;}
    if(!p)return 0;void*src=(void*)sender;
    if(p==&panels[3]){powerEvent(*p,src);return 1;}
    if(src==(void*)value(p->win,0x50)){if(p==&panels[2])dpsDismissed=true;closePanel(*p);return 1;}
    if(p==&panels[2]&&src==control(*p,L"DP_RESET")){clearDamage();return 1;}
    if(p==&panels[0]){
        if(src==control(*p,L"KR_SAVE")){saveKeyboard();return 1;}
        if(src==control(*p,L"KR_CANCEL")){closePanel(*p);return 1;}
        if(src==control(*p,L"KR_CLEAR")){fullrepeat::defaults(draft);renderKeyboard();return 1;}
        for(unsigned i=0;i<104;i++)if(src==keyOff[i]||src==keyOn[i]){
            unsigned scan=kKeyScan[i];if(!fullrepeat::permitted(scan,scanMap())){status(*p,L"此键保留为系统控制");return 1;}
            draft.enabled[scan]^=1;renderKeyboard();status(*p,draft.enabled[scan]?L"已启用此键连发，保存后生效":L"已关闭此键连发，保存后生效");return 1;
        }
    }else if(p==&panels[1]){
        // Numeric XUI IDs arrive as numbers in native notification kind 13
        // (as in CharacterInfo's 03345D50), not control addresses. String-ID
        // controls retain the pointer route used by the keyboard panel.
        int id=((sender>=100&&sender<=106)||(sender>=202&&sender<=207))?sender:-1;
        for(int i=100;i<=106;i++){wchar_t n[16];Format(n,L"%d",i);if(src==control(*p,n))id=i;}
        for(int i=202;i<=207;i++){wchar_t n[16];Format(n,L"%d",i);if(src==control(*p,n))id=i;}
        if(id>=0)swapControl(id,argument);return 1;
    }

    return 0;
}
static void fitPowerChrome(Panel&p){
    if(&p!=&panels[3]||!readable(p.win,0x60))return;
    void*props=(void*)value(p.win,4);void*skin=(void*)value(p.win,0xc);
    void*title=(void*)value(p.win,0x48);void*x=(void*)value(p.win,0x50);
    if(!readable(props,0x4c)||!readable(skin,0x18)||!readable(title,0x140)||!readable(x,0x504))return;
    // 33605A0's type-10 route honors the XUI custom skin. Type 0 ignores
    // TileOffset and always adds eleven pixels on each side.
    *(unsigned*)((Byte*)props+0x40)=10;
    *(unsigned*)((Byte*)skin+0xc)=0;*(unsigned*)((Byte*)skin+0x10)=0;
    // WindowCommon frames 94..96 have 2px horizontal image offsets and
    // 3px at the top. Trim their logical canvas to the content's 120px.
    if(value(title,0x6c)!=(unsigned)(p.width+4)||value(title,0x70)!=26)
        ((void(__thiscall*)(void*,int,int))value((void*)value(title),0x5c))(title,p.width+4,26);
    ((void(__thiscall*)(void*,int,int))0x37ea560)(title,-2,-25);
    ((void(__thiscall*)(void*,int,int))0x37ea560)(x,p.width-12,-15);
}
static void __fastcall PanelUpdate(void*self,void*){
    ((void(__thiscall*)(void*))0x3361970)(self);
    if(!ui())return;for(auto&p:panels)if(p.win==self&&p.shown){if(!owned(p,true))p.shown=false;else {place(p);fitPowerChrome(p);}}
}
static bool bind(Panel&p){
    if(!readable(p.win,0x1dc)||value(p.win)!=0x49f03f4||value(p.win,0x1b0)!=0x49f03ec)return false;
    p.root=(void*)value(p.win,0x30);if(!readable(p.root,0x100))return false;
    p.events[0]=(void*)&PanelEvent;*(void***)((char*)p.win+0x1b0)=p.events;
    void*t=(void*)value(p.win,8);if(!readable(t,0x60))return false;*((Byte*)t+0x44)=1;*((Byte*)t+0x45)=1;
    void*props=(void*)value(p.win,4);if(!readable(props,0x4c))return false;
    *(unsigned*)((char*)props+0x24)=p.tilesX;*(unsigned*)((char*)props+0x28)=p.tilesY;
    *(unsigned*)((char*)props+0x44)=12;*(unsigned*)((char*)props+0x48)=13;*((Byte*)props+0x3b)=1;
    ((void(__thiscall*)(void*,int))0x335f0d0)(p.win,0);
    void*title=(void*)value(p.win,0x48);void*x=(void*)value(p.win,0x50);
    if(!readable(title,0x140)||!readable(x,0x504))return false;
    ((void(__thiscall*)(void*,bool))0x3826f40)(title,true);visible(title,true);visible(x,true);visible((void*)value(p.win,0x58),false);
    fitPowerChrome(p);
    memcpy(p.vtable,(void*)(0x49f03f4-4),sizeof(p.vtable));p.vtable[5]=(uintptr_t)&PanelUpdate;*(uintptr_t**)p.win=p.vtable+1;
    if(&p==&panels[0])for(unsigned i=0;i<104;i++){
        wchar_t n[32];Format(n,L"KR_OFF_%u",i);keyOff[i]=control(p,n);Format(n,L"KR_ON_%u",i);keyOn[i]=control(p,n);
        if(!readable(keyOff[i],0x504)||!readable(keyOn[i],0x504))return false;
    }
    return true;
}
bool openPanel(Panel&p){
    if(p.failed)return false;load();if(!ui())return false;
    if(p.win&&!owned(p,true)&&!owned(p,false)){p.win=p.root=nullptr;p.shown=p.placed=false;}
    p.manager=*(void**)0x56a3798;if(!readable(p.manager,0xc640))return false;
    if(p.shown&&owned(p,true))return true;
    if(!p.win){
        const int ids[]={1214,1216,1213,992,1068,1065,1217,1218};p.id=0;
        for(auto id:ids){bool free=true;for(auto& other:panels)if(other.id==id&&other.win)free=false;
            for(unsigned off:{0x41u,0x1879u}){auto h=(char*)p.manager+(id*5+off)*4;if(value(h,4)!=value(h,8))free=false;}
            if(value((void*)0x3373f58,id*4)!=0x3373f41)free=false;if(free){p.id=id;break;}}
        if(!p.id)return false;creating=&p;void*r=((void*(__thiscall*)(void*,int,int,int))0x32bea50)(p.manager,p.id,0,0);creating=nullptr;
        if(!r||r!=p.win||!bind(p)){closePanel(p);p.failed=true;return false;}
    }else if(((void*(__thiscall*)(void*,int,int,int))0x32bea50)(p.manager,p.id,0,0)!=p.win)return false;
    p.shown=true;if(&p==&panels[0]){draft=saved;renderKeyboard();}else if(&p==&panels[1])renderSwap();place(p);resetRun();return true;
}
extern "C" __declspec(dllexport) void* __cdecl Factory(int id,void*m){
    if(!creating||!ui()||id!=creating->id||m!=creating->manager)return ((void*(__cdecl*)(int,void*))OLD_FACTORY)(id,m);
    void*memory=((void*(__cdecl*)(unsigned))0x37f2170)(0x1dc);if(!memory)return nullptr;void*answer;const wchar_t*path=creating->path;
    __asm{sub esp,20h
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
    creating->win=answer;return answer;
}
extern "C" __declspec(dllexport) bool __cdecl Layout(const wchar_t*path,void*doc){
    const Byte*xml=nullptr;if(pathEqual(path,L"UI/HUD/MainMenuWindowVer2.xui"))xml=kMenuXml;
    else if(creating&&ui()&&pathEqual(path,creating->path))xml=creating->xml;
    if(!xml)return ((bool(__cdecl*)(const wchar_t*,void*))OLD_LAYOUT)(path,doc);
    if(!readable(doc,0x80)||value(doc)!=0x4627bb4)return false;
    ((void(__thiscall*)(void*,const char*,void*,int))0x3e43eb0)(doc,(const char*)xml,nullptr,0);
    return !*((Byte*)doc+0x48)&&((void*(__thiscall*)(void*))0x3e3f700)(doc);
}
static void*menuNode(void*map,int key){if(!readable(map,16))return nullptr;void*head=(void*)value(map,8);if(!readable(head,16))return nullptr;void*n=(void*)value(head,4);for(unsigned i=0;n&&n!=head&&i<128;i++){if(!readable(n,24))return nullptr;int k=(int)value(n,12);if(k==key)return n;n=(void*)value(n,key<k?0:8);}return nullptr;}
extern "C" __declspec(dllexport) void* __fastcall MenuRows(void*data,void*){
    ((void*(__thiscall*)(void*))OLD_MENU_ROWS)(data);load();if(!readable(data,16)||menuNode(data,7201))return data;
    unsigned pair[13]{},result[4]{};pair[0]=7201;((void*(__thiscall*)(void*,const wchar_t*))0x45fea0)(pair+1,L"一键换装");
    pair[9]=2;pair[10]=3;pair[11]=2;pair[12]=49;((void*(__thiscall*)(void*,void*,const void*))0x2287990)(data,result,pair);((void(__thiscall*)(void*))0x1c5f110)(pair);
    if(!menuNode(data,7202)){memset(pair,0,sizeof(pair));pair[0]=7202;((void*(__thiscall*)(void*,const wchar_t*))0x45fea0)(pair+1,L"伤害统计");pair[9]=28;pair[10]=108;pair[11]=28;pair[12]=50;((void*(__thiscall*)(void*,void*,const void*))0x2287990)(data,result,pair);((void(__thiscall*)(void*))0x1c5f110)(pair);}return data;
}
extern "C" __declspec(dllexport) bool __cdecl MenuEnabled(int type){return type==49||type==50?true:((bool(__cdecl*)(int))OLD_MENU_ENABLED)(type);}
extern "C" __declspec(dllexport) bool __fastcall MenuSelected(void*data,void*,int type){if(type==49||type==50){load();return type==49?swapMenuSelected:dpsMenuSelected;}return ((bool(__thiscall*)(void*,int))OLD_MENU_SELECTED)(data,type);}
extern "C" __declspec(dllexport) void __fastcall MenuSave(void*self,void*){
    load();void*n=menuNode((char*)self+0x254,7201);if(n){void*c=(void*)value(n,16);if(readable(c,0x579)){unsigned selected=((bool(__thiscall*)(void*))0x3840b50)(c)?1:0,back=2;
        if(configPath[0]&&ProfileWrite(L"KeyRepeat",L"SwapMenuVisibleV1",&selected,4,configPath)&&ProfileRead(L"KeyRepeat",L"SwapMenuVisibleV1",&back,4,configPath)&&back==selected)swapMenuSelected=selected!=0;}}
    n=menuNode((char*)self+0x254,7202);if(n){void*c=(void*)value(n,16);if(readable(c,0x579)){unsigned selected=((bool(__thiscall*)(void*))0x3840b50)(c)?1:0,back=2;if(configPath[0]&&ProfileWrite(L"KeyRepeat",L"DpsMenuVisibleV1",&selected,4,configPath)&&ProfileRead(L"KeyRepeat",L"DpsMenuVisibleV1",&back,4,configPath)&&back==selected)dpsMenuSelected=selected!=0;}}
    ((void(__thiscall*)(void*))OLD_MENU_SAVE)(self);
}
extern "C" __declspec(dllexport) int __fastcall MenuEvent(void*self,void*,int id,int event,int v,int extra){
    if((id==7200||id==7201||id==7202)&&event==13){void*menu=(char*)self-0x1b0;void*m=*(void**)0x56a3798;
        Panel fake{};fake.win=menu;fake.id=0x453;fake.manager=m;
        if(owned(fake,true)&&!*((Byte*)menu+0x1e0)){((void(__thiscall*)(void*,int,int,int))0x32b94a0)(m,0x453,-1,0);if(id==7202){loadPlugins();if(dpsToggle)dpsToggle();}else openPanel(panels[id==7200?0:1]);}return 1;}
    return ((int(__thiscall*)(void*,int,int,int,int))OLD_MENU_EVENT)(self,id,event,v,extra);
}
extern "C" __declspec(dllexport) __declspec(naked) void MenuCheckGate(){__asm{
    cmp dword ptr[ebx+3ch],31h
    je accepted
    cmp dword ptr[ebx+3ch],32h
    je accepted
    push OLD_MENU_CHECK
    ret
accepted:
    push 01227cefh
    ret}}
extern "C" __declspec(dllexport) __declspec(naked) void MenuReflowGate(){__asm{
    cmp dword ptr[ebx+3ch],31h
    je accepted
    cmp dword ptr[ebx+3ch],32h
    je accepted
    push OLD_MENU_REFLOW
    ret
accepted:
    push 01228c13h
    ret}}
extern "C" __declspec(dllexport) void __cdecl Input(void*keyboard,int result){
    load();Byte physical[256];memcpy(physical,(Byte*)keyboard+0x10d,256);memcpy(latestPhysical,physical,256);
    bool active=result>=0&&*((Byte*)keyboard+4)&&*((Byte*)keyboard+0xc)&&!value(keyboard,0x1310)&&foreground()&&!modal();
    if(active&&!((void*(__cdecl*)())0x29610e0)())active=false;
    if(active){void*main=*(void**)0x57e2698;if(main&&readable(main,8)&&!((bool(__thiscall*)(void*))0x335aa00)(main))active=false;}
    swapKeys(physical,result>=0&&foreground()&&!value(keyboard,0x1310));
    fullrepeat::step(saved,runs,Time(),active,physical,(Byte*)keyboard+0x10d);
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
    jmp edx}}
extern "C" __declspec(dllexport) void __cdecl Tick(){
    if(busy)return;if(!uiThread){auto enumerate=(decltype(&EnumWindows))Proc(Module(L"user32.dll"),"EnumWindows");if(enumerate)enumerate(findWindow,(LPARAM)&uiThread);}if(!ui())return;
    // 022731E0 is a widely used native singleton getter, including inside
    // model/resource construction. Do feature work at most once per 16ms;
    // TickGetter still forwards every original getter call without delay.
    unsigned now=Time();if(tickStarted&&now-lastFeatureTick<16)return;
    tickStarted=true;lastFeatureTick=now;
    busy=true;load();for(auto&p:panels)if(p.shown&&!owned(p,true)){p.shown=false;resetRun();}
    swapTick();featureTick();busy=false;
}
// A local-test timing probe around the existing same-area visual creator.
// It owns no actor/cache, changes no argument, and logs at most 32 calls via
// the paired DLL; no process memory sampler or automatic gameplay is used.

extern "C" __declspec(dllexport) __declspec(naked) void TickGetter(){__asm{
    pushfd
    pushad
    call Tick
    popad
    popfd
    push OLD_GETTER
    ret}}

#include "equipment_swap.h"
#include "features.h"
