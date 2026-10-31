// Source component for statically integrated, matched x86 clients.
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#include <initializer_list>
#include <intrin.h>
#pragma intrinsic(_InterlockedCompareExchange, _InterlockedExchange)
static DWORD uiThread;
#define AP_GetCurrentThreadId (*reinterpret_cast<decltype(&GetCurrentThreadId)*>(0x6e85a90))
#define AP_GetTickCount (*reinterpret_cast<decltype(&GetTickCount)*>(0x6e85a24))
#define AP_VirtualQuery (*reinterpret_cast<decltype(&VirtualQuery)*>(0x6e85b18))
#define AP_wsprintfW (*reinterpret_cast<decltype(&wsprintfW)*>(0x6e866b8))
extern "C" void* __cdecl memset(void* p,int v,size_t n){auto b=(unsigned char*)p;while(n--)*b++=(unsigned char)v;return p;}
extern "C" void* __cdecl memcpy(void* d,const void* s,size_t n){auto a=(unsigned char*)d;auto b=(const unsigned char*)s;while(n--)*a++=*b++;return d;}
static bool equal(const void* a,const void* b,unsigned n){auto x=(const unsigned char*)a,y=(const unsigned char*)b;while(n--)if(*x++!=*y++)return false;return true;}
static bool wequal(const wchar_t* a,const wchar_t* b){while(*a&&*a==*b){a++;b++;}return *a==*b;}
static uintptr_t value(const void*p,unsigned o=0){return *(const uintptr_t*)((const char*)p+o);}
static bool readable(const void*p,unsigned n){MEMORY_BASIC_INFORMATION m{};if(!p||!AP_VirtualQuery(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;uintptr_t a=(uintptr_t)p;uintptr_t end=(uintptr_t)m.BaseAddress+m.RegionSize;return a<=end&&n<=end-a;}
static unsigned get16(const unsigned char*b,unsigned o){return b[o]|(unsigned)b[o+1]<<8;}
static unsigned get32(const unsigned char*b,unsigned o){return get16(b,o)|get16(b,o+2)<<16;}
static void put16(unsigned char*b,unsigned o,unsigned v){b[o]=(unsigned char)v;b[o+1]=(unsigned char)(v>>8);}
static void put32(unsigned char*b,unsigned o,unsigned v){put16(b,o,v);put16(b,o+2,v>>16);}
static bool ui(){return uiThread&&AP_GetCurrentThreadId()==uiThread;}
static void show(void*p,bool on){if(p&&readable(p,4)){auto t=*(void***)p;((void(__thiscall*)(void*,bool))t[2])(p,on);}}
struct Ref {void* value; void* counter;};
#include "native_contract.h"
static bool contracts(){
 for(const auto& c:kFestivalCodeChecks)
  if(!readable((void*)c.address,c.length)||!equal((void*)c.address,c.bytes,c.length))return false;
 return true;
}
// Paging buttons use only the three approved 112x46 decoration frames.
static void compact(void* p,unsigned frame){
 if(frame!=0||!readable(p,0x504))return;
 *(unsigned*)((char*)p+0x484)=0x3f000000;
 *(unsigned*)((char*)p+0x488)=0x3f000000;
 ((void(__thiscall*)(void*,int,int))0x38271c0)(p,56,23);
}
#include "festival_ui.h"
extern "C" __declspec(dllexport) bool __cdecl FestivalBindUIThread(){
 if(uiThread&&uiThread!=AP_GetCurrentThreadId())return false;
 if(!contracts())return false;
 uiThread=AP_GetCurrentThreadId();return true;
}
extern "C" __declspec(dllexport) void __cdecl FestivalTick(){if(ui())festivalui::tick();}
extern "C" __declspec(dllexport) void* __cdecl FestivalFactory(int id,void* mgr){
 if(festivalui::factoryRequest(id,mgr))return festivalui::factory(id,mgr);
 return ((void*(__cdecl*)(int,void*))0x3365ba0)(id,mgr);
}
extern "C" __declspec(dllexport) bool __cdecl FestivalLayout(const wchar_t* path,void* doc){
 if(festivalui::layoutRequest(path))return festivalui::layout(doc);
 return ((bool(__cdecl*)(const wchar_t*,void*))0x382ab90)(path,doc);
}
