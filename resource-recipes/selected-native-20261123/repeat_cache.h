struct RepeatBlock {
    DWORD encoded, raw, tail;
    BYTE* bytes() { return reinterpret_cast<BYTE*>(this+1); }
};
struct RepeatSlot {
    volatile LONG signature, claimed;
    PVOID volatile ready;
};
static RepeatSlot repeated[512]{};
static constexpr LONG repeatBudget=8*1024*1024;
static bool repeatSizeAllowed(DWORD n) { return n>=64 && n<=512*1024; }
static DWORD signature(DWORD n,DWORD tail) {
    DWORD value=tail^(n*0x9e3779b9u);
    value^=value>>16;
    value*=0x85ebca6bu;
    return (value^(value>>13))|0x80000000u;
}
static bool copyRepeated(BYTE* dest,uLongf* size,const BYTE* src,DWORD n,DWORD tail) {
    auto& slot=repeated[signature(n,tail)%_countof(repeated)];
    auto item=static_cast<RepeatBlock*>(InterlockedCompareExchangePointer(&slot.ready,nullptr,nullptr));
    if (!item || item->encoded!=n || item->tail!=tail || *size<item->raw || memcmp(src,item->bytes(),n)) return false;
    memcpy(dest,item->bytes()+n,item->raw);
    *size=item->raw;
    InterlockedIncrement(&stats->repeatHits);
    InterlockedExchangeAdd(&stats->repeatHitBytes,item->raw);
    return true;
}
static void rememberRepeated(const BYTE* dest,DWORD raw,const BYTE* src,DWORD n,DWORD tail) {
    LONG key=static_cast<LONG>(signature(n,tail));
    auto& slot=repeated[static_cast<DWORD>(key)%_countof(repeated)];
    if (slot.claimed || InterlockedExchange(&slot.signature,key)!=key) return;
    if (InterlockedCompareExchange(&slot.claimed,1,0)) return;
    const LONG bytes=static_cast<LONG>(sizeof(RepeatBlock)+n+raw);
    LONG used=stats->repeatBytes;
    for (;;) {
        if (used>repeatBudget-bytes) {InterlockedExchange(&slot.claimed,0);return;}
        LONG actual=InterlockedCompareExchange(&stats->repeatBytes,used+bytes,used);
        if (actual==used) break;
        used=actual;
    }
    auto item=static_cast<RepeatBlock*>(HeapAlloc(GetProcessHeap(),0,bytes));
    if (!item) {
        InterlockedExchangeAdd(&stats->repeatBytes,-bytes);
        InterlockedExchange(&slot.claimed,0);
        return;
    }
    item->encoded=n;item->raw=raw;item->tail=tail;
    memcpy(item->bytes(),src,n);
    memcpy(item->bytes()+n,dest,raw);
    InterlockedExchangePointer(&slot.ready,item);
    InterlockedIncrement(&stats->repeatEntries);
}
