#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <string.h>
#include <stdio.h>
static volatile unsigned char native_space[0x05400000];
static unsigned char *manager;
static uintptr_t entry;
static int object,calls;
static HWND main_window,child;
static DWORD ui_thread;
static wchar_t report[1024],test_case[80];
static void write_report(void){
    if(!report[0])return;
    HANDLE f=CreateFileW(report,GENERIC_WRITE,FILE_SHARE_READ,NULL,CREATE_ALWAYS,FILE_ATTRIBUTE_NORMAL,NULL);
    if(f!=INVALID_HANDLE_VALUE){char text[180];int n=sprintf(text,"{\"calls\":%d,\"warehouseWindows\":%d,\"correctThread\":true,\"gameAssetsTouched\":false}",calls,child?1:0);DWORD got;WriteFile(f,text,n,&got,NULL);CloseHandle(f);}
}
static void* __attribute__((thiscall)) fixture_open(void *self,int id,int a,int b){
    if(self!=manager || id!=198 || a || b || GetCurrentThreadId()!=ui_thread)return NULL;
    calls++;
    if(!child)child=CreateWindowExW(0,L"STATIC",L"Native warehouse fixture",WS_CHILD|WS_VISIBLE|WS_BORDER,30,40,430,230,main_window,NULL,GetModuleHandleW(NULL),NULL);
    entry=(uintptr_t)&object;*(uintptr_t*)(manager+(198*5+0x41)*4+4)=(uintptr_t)&entry;*(uintptr_t*)(manager+(198*5+0x41)*4+8)=(uintptr_t)&entry+4;
    write_report();return &object;
}
static BOOL region(uintptr_t address){DWORD old=0;if(address<(uintptr_t)native_space || address+0x10000>(uintptr_t)native_space+sizeof(native_space))return FALSE;return VirtualProtect((void*)address,0x10000,PAGE_EXECUTE_READWRITE,&old);}
static LRESULT CALLBACK window_proc(HWND hwnd,UINT msg,WPARAM wp,LPARAM lp){if(msg==WM_DESTROY){PostQuitMessage(0);return 0;}return DefWindowProcW(hwnd,msg,wp,lp);}
int main(void){
    HINSTANCE instance=GetModuleHandleW(NULL);
    GetEnvironmentVariableW(L"ARAD_NATIVE_FIXTURE_RESULT",report,1024);GetEnvironmentVariableW(L"ARAD_NATIVE_FIXTURE_CASE",test_case,80);
    native_space[0]=1;
    if((uintptr_t)GetModuleHandleW(NULL)!=0x400000){fprintf(stderr,"base %p\n",GetModuleHandleW(NULL));return 3;}
    if(!region(0x032b0000)||!region(0x03360000)||!region(0x03500000)||!region(0x056a0000))return 3;
    manager=(unsigned char*)VirtualAlloc(NULL,0x10000,MEM_RESERVE|MEM_COMMIT,PAGE_READWRITE);if(!manager)return 3;*(uintptr_t*)0x056a3798=(uintptr_t)manager;*(DWORD*)(manager+0xc5b4)=1;
    const unsigned char prefix[]={0x55,0x8b,0xec,0x6a,0xff,0x68,0x48,0x9f,0x25,0x04,0x64,0xa1,0,0,0,0};
    unsigned char *at=(unsigned char*)0x032bea50;memcpy(at,prefix,sizeof(prefix));at+=sizeof(prefix);
    // Restore the original incoming frame, then tail-call the typed fixture.
    const unsigned char unwind[]={0x8b,0xe5,0x5d,0xb8};memcpy(at,unwind,sizeof(unwind));at+=sizeof(unwind);*(uintptr_t*)at=(uintptr_t)&fixture_open;at+=4;*at++=0xff;*at++=0xe0;
    const unsigned char factory[]={0x68,0xdc,0x02,0,0,0xe8,0xbc,0xb1,0x48,0,0x83,0xc4,0x04,0x89,0x45,0x08};memcpy((void*)0x03366faa,factory,sizeof(factory));
    const unsigned char ctor[]={0x55,0x8b,0xec,0x6a,0xff,0x68,0x84,0xab,0x28,0x04,0x64,0xa1,0,0,0,0};memcpy((void*)0x03504b50,ctor,sizeof(ctor));
    if(!lstrcmpW(test_case,L"fingerprint"))*(unsigned char*)0x032bea50=0x90;
    if(!lstrcmpW(test_case,L"manager"))*(uintptr_t*)0x056a3798=0;
    if(!lstrcmpW(test_case,L"not-ready"))*(DWORD*)(manager+0xc5b4)=0;
    WNDCLASSW wc={0};wc.lpfnWndProc=window_proc;wc.hInstance=instance;wc.lpszClassName=L"AradNativeFixture";if(!RegisterClassW(&wc))return 4;
    ui_thread=GetCurrentThreadId();main_window=CreateWindowExW(0,wc.lpszClassName,L"Arad native UI fixture",WS_OVERLAPPEDWINDOW|WS_VISIBLE,80,80,640,400,NULL,NULL,instance,NULL);if(!main_window)return 5;
    ShowWindow(main_window,SW_SHOW);SetForegroundWindow(main_window);write_report();
    if(!lstrcmpW(test_case,L"timeout"))Sleep(12000);
    MSG msg;while(GetMessageW(&msg,NULL,0,0)>0){TranslateMessage(&msg);DispatchMessageW(&msg);}return 0;
}
