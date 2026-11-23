#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <strsafe.h>
#include <algorithm>
#include <cstring>
#include "zlib/zlib.h"

struct Entry { const wchar_t* pack; DWORD offset, encoded, raw, tail; unsigned short group; bool observed; };
struct Range { unsigned short first, count; };
#include "atlas_index.inc"

struct Stats {
    DWORD version, count;
    volatile LONG calls, hit, hitBytes, jobs[2], failed, finished, peak, active;
    volatile LONG samples;
    struct Sample { DWORD length, tail; } misses[128];
    volatile LONG scheduled, groupsActive, bytesReady, observedHits, demandHits, demandHitBytes;
    volatile LONG extraJobs[4], workerLimit, repeatHits, repeatEntries, repeatBytes, repeatHitBytes;
};
static Stats localStats{};
static Stats* stats = &localStats;
static HANDLE statsMapping; // Keep the mapping name available to independent readers.
static INIT_ONCE once = INIT_ONCE_STATIC_INIT;
static void* volatile buffers[_countof(entries)]{};
static volatile LONG nextJob=-1;
static unsigned workerLimit;
#ifdef ATLAS_TEST
static unsigned testWorkerLimit;
static HANDLE testComplete;
#endif

static unsigned chooseWorkerCount() {
    DWORD_PTR usable=0,system=0;
    if (!GetProcessAffinityMask(GetCurrentProcess(),&usable,&system)) return 2;
    unsigned count=0;
    while (usable) {usable&=usable-1;++count;}
    if (count>=12) return 6;
    if (count>=8) return 4;
    return count>=4 ? 2 : 1;
}
static wchar_t folder[MAX_PATH];
#ifdef ATLAS_TEST
static const wchar_t* testFolder;
#endif

static bool prepare(const Entry& item, HANDLE file, BYTE* memory) {
    DWORD bytesRead;
    if (file == INVALID_HANDLE_VALUE ||
        SetFilePointer(file,item.offset,nullptr,FILE_BEGIN)==INVALID_SET_FILE_POINTER ||
        !ReadFile(file,memory,item.encoded,&bytesRead,nullptr) || bytesRead!=item.encoded)
        return false;
    DWORD tail;
    memcpy(&tail,memory+item.encoded-4,4);
    uLongf length=item.raw;
    return tail==item.tail &&
        uncompress(memory+item.encoded,&length,memory,item.encoded)==Z_OK && length==item.raw;
}

static DWORD WINAPI worker(void* arg) {
    const size_t slot=reinterpret_cast<size_t>(arg);
    SetThreadPriority(GetCurrentThread(),THREAD_PRIORITY_BELOW_NORMAL);
    HANDLE file=INVALID_HANDLE_VALUE;
    const wchar_t* last=nullptr;
    for (;;) {
        LONG id=InterlockedIncrement(&nextJob);
        if (static_cast<unsigned>(id)>=_countof(entries)) break;
        const Entry& item=entries[id];
        if (!last || wcscmp(last,item.pack)!=0) {
            if (file!=INVALID_HANDLE_VALUE) CloseHandle(file);
            wchar_t path[MAX_PATH];
            file=INVALID_HANDLE_VALUE;
            if (SUCCEEDED(StringCchPrintfW(path,_countof(path),L"%s%s",folder,item.pack)))
                file=CreateFileW(path,GENERIC_READ,FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE,
                                 nullptr,OPEN_EXISTING,FILE_ATTRIBUTE_NORMAL,nullptr);
            last=item.pack;
        }
        BYTE* block=static_cast<BYTE*>(HeapAlloc(GetProcessHeap(),0,item.encoded+item.raw));
        LONG active=InterlockedIncrement(&stats->active);
        LONG prev=stats->peak;
        while (prev<active) {
            LONG observed=InterlockedCompareExchange(&stats->peak,active,prev);
            if (observed==prev) break;
            prev=observed;
        }
        if (block && prepare(item,file,block)) {
            InterlockedExchangePointer(&buffers[id],block);
            if (slot<2) InterlockedIncrement(&stats->jobs[slot]);
            else InterlockedIncrement(&stats->extraJobs[slot-2]);
            InterlockedExchangeAdd(&stats->bytesReady,item.encoded+item.raw);
        } else {
            if (block) HeapFree(GetProcessHeap(),0,block);
            InterlockedIncrement(&stats->failed);
        }
        InterlockedDecrement(&stats->active);
    }
    if (file!=INVALID_HANDLE_VALUE) CloseHandle(file);
    LONG finished=InterlockedIncrement(&stats->finished);
#ifdef ATLAS_TEST
    if (finished==static_cast<LONG>(workerLimit)) SetEvent(testComplete);
#else
    (void)finished;
#endif
    return 0;
}

static BOOL CALLBACK initialize(PINIT_ONCE,void*,void**) {
    wchar_t name[80];
    StringCchPrintfW(name,_countof(name),L"Local\\DNF101Atlas-%lu",GetCurrentProcessId());
    HANDLE mapping=CreateFileMappingW(INVALID_HANDLE_VALUE,nullptr,PAGE_READWRITE,0,sizeof(Stats),name);
    if (mapping) {
        if (GetLastError()!=ERROR_ALREADY_EXISTS) {
            auto view=static_cast<Stats*>(MapViewOfFile(mapping,FILE_MAP_WRITE,0,0,sizeof(Stats)));
            if (view) {
                stats=view;
                statsMapping=mapping;
            }
        }
        if (!statsMapping) CloseHandle(mapping);
    }
    stats->version=3;
    stats->count=_countof(entries);
#ifdef ATLAS_TEST
    if (FAILED(StringCchCopyW(folder,_countof(folder),testFolder))) return TRUE;
#else
    DWORD len=GetModuleFileNameW(nullptr,folder,_countof(folder));
    if (!len || len>=_countof(folder)) return TRUE;
    wchar_t* end=wcsrchr(folder,L'\\');
    if (!end) return TRUE;
    end[1]=0;
    if (FAILED(StringCchCatW(folder,_countof(folder),L"ImagePacks2\\"))) return TRUE;
#endif
    workerLimit=chooseWorkerCount();
#ifdef ATLAS_TEST
    workerLimit=testWorkerLimit;
    testComplete=CreateEventW(nullptr,TRUE,FALSE,nullptr);
#endif
    stats->workerLimit=workerLimit;
    stats->scheduled=_countof(entries);
    stats->groupsActive=_countof(groups);
    for (unsigned slot=0;slot<workerLimit;++slot) {
        HANDLE thread=CreateThread(nullptr,0,worker,reinterpret_cast<void*>(static_cast<size_t>(slot)),0,nullptr);
        if (thread) CloseHandle(thread);
    }
    return TRUE;
}

#include "repeat_cache.h"

extern "C" int __cdecl TryInflateAtlas(BYTE* dest,uLongf* size,const BYTE* src,uLong n) {
    InitOnceExecuteOnce(&once,initialize,nullptr,nullptr);
    InterlockedIncrement(&stats->calls);
    bool indexed=false;
    DWORD tail=0;
    if (src && dest && size && n>=6) {
        memcpy(&tail,src+n-4,4);
        auto it=std::lower_bound(std::begin(byLength),std::end(byLength),n,
                [](unsigned short index,DWORD length){return entries[index].encoded<length;});
        for (;it!=std::end(byLength) && entries[*it].encoded==n;++it) {
            const Entry& item=entries[*it];
            if (item.tail!=tail || *size<item.raw) continue;
            indexed=true;
            auto payload=static_cast<const BYTE*>(InterlockedCompareExchangePointer(&buffers[*it],nullptr,nullptr));
            if (!payload || memcmp(src,payload,n)!=0) continue;
            memcpy(dest,payload+n,item.raw);
            *size=item.raw;
            InterlockedIncrement(&stats->hit);
            if (item.observed) InterlockedIncrement(&stats->observedHits);
            if (item.group) {
                InterlockedIncrement(&stats->demandHits);
                InterlockedExchangeAdd(&stats->demandHitBytes,item.raw);
            }
            InterlockedExchangeAdd(&stats->hitBytes,item.raw);
            return Z_OK;
        }
        if (!indexed && repeatSizeAllowed(n) && copyRepeated(dest,size,src,n,tail)) return Z_OK;
        if (n>=1024 && stats->samples<128) {
            LONG i=InterlockedIncrement(&stats->samples)-1;
            if (i<128) stats->misses[i]={n,tail};
        }
    }
#ifdef ATLAS_TEST
    int result=uncompress(dest,size,src,n);
#else
    using Native=int(__cdecl*)(BYTE*,uLongf*,const BYTE*,uLong);
    int result=reinterpret_cast<Native>(0x03e448b0)(dest,size,src,n);
#endif
    if (result==Z_OK && !indexed && src && dest && size && repeatSizeAllowed(n) &&
        *size>=4096 && *size<=1024*1024) rememberRepeated(dest,*size,src,n,tail);
    return result;
}
