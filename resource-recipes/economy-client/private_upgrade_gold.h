#pragma once

// Private group pricing, pinned to V200's calculator and native protected-int
// accessors. Called only at its successful epilogue, after every native fee
// multiplier. Only result+0xa0 is changed; material fields remain untouched.
namespace privateupgrade {
typedef void (__thiscall *ProtectedGet)(void*,const void*,void*);
typedef void (__thiscall *ProtectedSet)(void*,const void*,void*);
static unsigned accessor(unsigned cell, bool write) {
    switch(cell & 15u) {
    case 0: return write ? 0x37df130 : 0x37df550;
    case 4: return write ? 0x37df1b0 : 0x37df5c0;
    case 8: return write ? 0x37df230 : 0x37df630;
    case 12:return write ? 0x37df2b0 : 0x37df6a0;
    default:return write ? 0x37df010 : 0x37df7f0;
    }
}
static bool reinforcementMaterialConversion() {
    void* manager=*(void**)0x56a3798;
    if(!manager)return false;
    // Same RTTI-checked window getter used by 331264f and the cost tooltip.
    unsigned window=(unsigned)((void*(__thiscall*)(void*))0x32bac90)(manager);
    if(!window)return false;
    unsigned npc=*(unsigned*)(window+0x1f4);
    return npc && *(int*)(npc+0x6a8)>0;
}
}

extern "C" __declspec(dllexport) void __cdecl PrivateUpgradeGold(unsigned table,unsigned result) {
    if(!result)return;
    if(table==0x56c8b70) {
        // Jonathan's material count derives from the original gold amount.
        if(privateupgrade::reinforcementMaterialConversion())return;
    } else {
        unsigned holder=*(unsigned*)0x56ca6f8;
        if(!holder || table!=*(unsigned*)holder)return;
    }
    unsigned cell=result+0xa0;
    int original=0;
    ((privateupgrade::ProtectedGet)privateupgrade::accessor(cell,false))((void*)cell,(void*)(cell+4),&original);
    if(original<=0)return;
    int reduced=original/7+(original%7!=0);
    ((privateupgrade::ProtectedSet)privateupgrade::accessor(cell,true))((void*)cell,&reduced,(void*)(cell+4));
}

extern "C" __declspec(dllexport) __declspec(naked) void PrivateUpgradeGoldGate(){__asm{
    pushfd
    pushad
    push dword ptr [ebp+0ch]
    push dword ptr [ebp-038h]
    call PrivateUpgradeGold
    add esp,8
    popad
    popfd
    lea ecx,[ebp-0b8h]
    push 03a3daf1h
    ret
}}
