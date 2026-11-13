#pragma once
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#define API(name,addr) (*reinterpret_cast<decltype(&name)*>(addr))
#define Time API(GetTickCount,0x6e85a24)
#define Query API(VirtualQuery,0x6e85b18)
#define Thread API(GetCurrentThreadId,0x6e85a90)
#define Module API(GetModuleHandleW,0x6e85ab0)
#define Proc API(GetProcAddress,0x6e85a9c)
#define ProfileWrite API(WritePrivateProfileStructW,0x6e85b58)
#define ProfileRead API(GetPrivateProfileStructW,0x6e85b60)
#define Mkdir API(CreateDirectoryW,0x6e85b78)
#define Format API(wsprintfW,0x6e866b8)
typedef unsigned char Byte;
inline uintptr_t value(const void*p,unsigned o=0){return *(const uintptr_t*)((const char*)p+o);}
inline bool readable(const void*p,unsigned n){
    MEMORY_BASIC_INFORMATION m{};if(!p||!Query(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;
    uintptr_t a=(uintptr_t)p,e=(uintptr_t)m.BaseAddress+m.RegionSize;return a<=e&&n<=e-a;
}
inline bool equal(const void*a,const void*b,unsigned n){auto x=(const Byte*)a,y=(const Byte*)b;while(n--)if(*x++!=*y++)return false;return true;}
inline bool pathEqual(const wchar_t*a,const wchar_t*b){if(!a||!b)return false;for(;;a++,b++){wchar_t x=*a,y=*b;if(x>=L'A'&&x<=L'Z')x+=32;if(y>=L'A'&&y<=L'Z')y+=32;if(x==L'\\')x=L'/';if(y==L'\\')y=L'/';if(x!=y)return false;if(!x)return true;}}
struct Ref {void*object;void*count;};
inline void*controlAt(void*root,const wchar_t*name){Ref r{};if(!readable(root,0x100))return nullptr;((Ref*(__thiscall*)(void*,Ref*,const wchar_t*))0x37eb120)(root,&r,name);void*p=r.object;((void(__thiscall*)(Ref*))0xf11f90)(&r);return p;}
inline void text(void*p,const wchar_t*s){if(readable(p,0x410)&&value(p)==0x4d4fa4c)((void(__thiscall*)(void*,const wchar_t*))0x3835940)(p,s);}
inline void visible(void*p,bool show){if(readable(p,8)&&readable((void*)value(p),12))((void(__thiscall*)(void*,bool))value((void*)value(p),8))(p,show);}
inline int clamp(int v,int lo,int hi){if(hi<lo)hi=lo;return v<lo?lo:v>hi?hi:v;}
extern "C" void* __cdecl memset(void*,int,size_t);
extern "C" void* __cdecl memcpy(void*,const void*,size_t);

struct Panel {
    int id,width,height;unsigned tilesX,tilesY;
    const wchar_t*path;const Byte*xml;
    void*win,*manager,*root;
    bool shown,failed,placed;int screenWidth,screenHeight;
    uintptr_t vtable[129];void*events[1];
};
bool ui();
bool owned(const Panel&,bool);
bool openPanel(Panel&);
void closePanel(Panel&);
inline void*control(Panel&p,const wchar_t*n){return controlAt(p.root,n);}
inline void status(Panel&p,const wchar_t*s){if(p.shown&&owned(p,true))text(control(p,p.width==636?L"KR_STATUS":L"50"),s);}
