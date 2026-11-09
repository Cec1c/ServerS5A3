#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <string.h>

// One command, one game-owned message-thread callback. No arbitrary addresses,
// inventory edits, persistent EXE patches, or asset transactions are exposed.
#define MAGIC 0x31554841u
#define MARKER 0x4b50554eu
#define UI_ID 198u
struct Command {
    uint32_t magic, version, bytes, pid;
    uint64_t created;
    uint32_t window, nonce, action;
    volatile LONG state;
    uint32_t error, ui_id, deadline, reserved[3];
};
_Static_assert(sizeof(struct Command)==64,"command ABI");
static BOOL read_bytes(uintptr_t at,void *out,SIZE_T bytes) {
    SIZE_T got=0;
    return ReadProcessMemory(GetCurrentProcess(),(void*)at,out,bytes,&got) && got==bytes;
}
static BOOL code(uintptr_t at,const unsigned char *bytes,SIZE_T size) {
    unsigned char actual[32];
    return size<=sizeof(actual) && read_bytes(at,actual,size) && !memcmp(actual,bytes,size);
}
static DWORD open_warehouse(void) {
    DWORD foreground=0;GetWindowThreadProcessId(GetForegroundWindow(),&foreground);if(foreground!=GetCurrentProcessId())return 14;
    static const unsigned char opener[]={0x55,0x8b,0xec,0x6a,0xff,0x68,0x48,0x9f,0x25,0x04,0x64,0xa1,0,0,0,0};
    static const unsigned char factory[]={0x68,0xdc,0x02,0,0,0xe8,0xbc,0xb1,0x48,0,0x83,0xc4,0x04,0x89,0x45,0x08};
    static const unsigned char ctor[]={0x55,0x8b,0xec,0x6a,0xff,0x68,0x84,0xab,0x28,0x04,0x64,0xa1,0,0,0,0};
    if((uintptr_t)GetModuleHandleW(NULL)!=0x400000u ||
       !code(0x32bea50u,opener,sizeof(opener)) ||
       !code(0x3366faau,factory,sizeof(factory)) ||
       !code(0x3504b50u,ctor,sizeof(ctor)))return 10;
    uintptr_t manager=0;
    if(!read_bytes(0x56a3798u,&manager,4) || manager<0x10000u)return 11;
    uint32_t ready=0;
    if(!read_bytes(manager+0xc5b4u,&ready,4) || !ready)return 11;
    typedef void *(__attribute__((thiscall)) *OpenUI)(void*,int,int,int);
    void *window=((OpenUI)0x32bea50u)((void*)manager,UI_ID,0,0);
    if(!window)return 12;
    uintptr_t begin=0,end=0;
    if(!read_bytes(manager+(UI_ID*5u+0x41u)*4u+4u,&begin,4) ||
       !read_bytes(manager+(UI_ID*5u+0x41u)*4u+8u,&end,4) ||
       !begin || end<begin || end-begin>64 || (end-begin)%4)return 13;
    for(uintptr_t p=begin;p<end;p+=4){uintptr_t item=0;if(read_bytes(p,&item,4)&&item==(uintptr_t)window)return 0;}
    return 13;
}
__declspec(dllexport) LRESULT CALLBACK AradWarehouseHook(int n,WPARAM kind,LPARAM value) {
    if(n==HC_ACTION && kind==PM_REMOVE && value) {
        const MSG *msg=(const MSG*)value;
        if(msg->message==WM_NULL && msg->lParam==MARKER && msg->wParam) {
            wchar_t name[100];
            wsprintfW(name,L"Local\\Arad.Helper.NativeUI.%lu.%08lX",GetCurrentProcessId(),(DWORD)msg->wParam);
            HANDLE mapping=OpenFileMappingW(FILE_MAP_READ|FILE_MAP_WRITE,FALSE,name);
            if(mapping){
                struct Command *q=(struct Command*)MapViewOfFile(mapping,FILE_MAP_READ|FILE_MAP_WRITE,0,0,sizeof(struct Command));
                if(q){
                    FILETIME created={0},exit={0},kernel={0},user={0};DWORD pid=0;
                    DWORD thread=GetWindowThreadProcessId(msg->hwnd,&pid);
                    BOOL identity=GetProcessTimes(GetCurrentProcess(),&created,&exit,&kernel,&user);
                    uint64_t born=((uint64_t)created.dwHighDateTime<<32)|created.dwLowDateTime;
                    if(q->magic==MAGIC && q->version==1 && q->bytes==sizeof(*q) &&
                       q->pid==GetCurrentProcessId() && identity && q->created==born &&
                       q->window==(uint32_t)(uintptr_t)msg->hwnd && q->nonce==(DWORD)msg->wParam &&
                       q->action==1 && q->ui_id==UI_ID && thread==GetCurrentThreadId() && pid==q->pid &&
                       (int32_t)(q->deadline-GetTickCount())>=0 &&
                       (uint32_t)(q->deadline-GetTickCount())<=5000u &&
                       InterlockedCompareExchange(&q->state,1,0)==0) {
                        DWORD error=open_warehouse();q->error=error;InterlockedExchange(&q->state,error?3:2);
                        wchar_t event_name[110];wsprintfW(event_name,L"%s.done",name);
                        HANDLE event=OpenEventW(EVENT_MODIFY_STATE,FALSE,event_name);
                        if(event){SetEvent(event);CloseHandle(event);}
                    }
                    UnmapViewOfFile(q);
                }
                CloseHandle(mapping);
            }
        }
    }
    return CallNextHookEx(NULL,n,kind,value);
}
BOOL WINAPI DllMain(HINSTANCE module,DWORD why,LPVOID reserved) {
    (void)reserved;if(why==DLL_PROCESS_ATTACH)DisableThreadLibraryCalls(module);return TRUE;
}
