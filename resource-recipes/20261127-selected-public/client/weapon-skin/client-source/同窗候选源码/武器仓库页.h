#pragma once

void* WeaponRowPart(void* root, unsigned row, const wchar_t* suffix) {
    constexpr wchar_t prefix[]=L"WS_WEAPONSKIN_ROW";
    wchar_t id[64];
    unsigned at=0;
    for (const wchar_t* p=prefix;*p;++p) id[at++]=*p;
    id[at++]=static_cast<wchar_t>(L'0'+row);
    if (*suffix) id[at++]=L'_';
    for (;*suffix && at+1<ARRAYSIZE(id);++suffix) id[at++]=*suffix;
    id[at]=0;
    return Find(root,id);
}

bool BindWeaponRows() {
    auto& s=Data();
    if (!s.weapons) {
        auto* state=reinterpret_cast<WeaponPageState* (__cdecl*)(unsigned)>(0x8a9480)(sizeof(WeaponPageState));
        if (!state) return false;
        __stosb(reinterpret_cast<unsigned char*>(state),0,sizeof(WeaponPageState));
        Lock();
        s.weapons=state;
        Unlock();
    }
    auto& state=*s.weapons;
    state.clicked=state.selected=state.preview=state.maximum=0;
    state.query[0]=0;
    __stosb(state.flags,0,kWeaponFlagsSize);
    for (unsigned i=0;i<kWeaponRows;++i) {
        auto& row=state.rows[i];
        row.card=WeaponRowPart(s.root,i,L"");
        row.icon=i ? WeaponRowPart(s.root,i,L"ItemIcon") : s.itemIcon;
        row.title=i ? WeaponRowPart(s.root,i,L"title") : s.itemTitle;
        row.apply=i ? WeaponRowPart(s.root,i,L"btnApply") : s.apply;
        row.clear=i ? WeaponRowPart(s.root,i,L"BtnCancel") : s.clear;
        row.applied=i ? WeaponRowPart(s.root,i,L"imgApply") : s.applied;
        row.look=nullptr;
        if (!row.card || !row.icon || !row.title || !row.apply || !row.clear || !row.applied) return false;
    }
    return true;
}

void QueueWeaponSelection(unsigned id, bool clear) {
    auto& s=Data();
    auto& page=*s.weapons;
    if (!s.ready || s.awaiting || !id || page.clicked || clear!=(id==page.selected)) return;
    page.clicked=id;
    page.clickedRevision=s.revision;
    if (!Post(clear ? ClearMessage : ApplyMessage)) page.clicked=0;
}

bool WeaponSearchPadding(wchar_t c) { return c==L' ' || c==L'\t' || c==L'　'; }

bool WeaponMatches(const wchar_t* name,const wchar_t* query) {
    while (WeaponSearchPadding(*query)) ++query;
    unsigned length=0;
    while (query[length]) ++length;
    while (length && WeaponSearchPadding(query[length-1])) --length;
    if (!length) return true;
    for (const wchar_t* start=name;*start;++start) {
        unsigned match=0;
        while (match<length && start[match] && start[match]==query[match]) ++match;
        if (match==length) return true;
    }
    return false;
}

void RenderWeaponRows(bool reset) {
    auto& s=Data();
    auto& state=*s.weapons;
    auto* pager=Find(s.root,kPages[kWeaponPage].pager);
    unsigned page=reset ? 1 : PageNumber(pager);
    if (!page || page>state.maximum) page=1;
    const unsigned first=(page-1)*kWeaponRows;
    const WeaponLook* visible[kWeaponRows]={};
    unsigned matches=0;
    if (s.ready) for (unsigned byte=0;byte<kWeaponFlagsSize;++byte) {
        const unsigned flags=state.flags[byte];
        if (!flags) continue;
        for (unsigned bit=0;bit<8;++bit) {
            const unsigned i=8*byte+bit;
            if (i>=kWeaponCatalogCount || !(flags & (1u<<bit))) continue;
            const auto& look=kWeaponCatalog[i];
            if (!WeaponMatches(look.name,state.query)) continue;
            if (matches>=first && matches-first<kWeaponRows) visible[matches-first]=&look;
            ++matches;
        }
    }
    const unsigned maximum=matches ? (matches+kWeaponRows-1)/kWeaponRows : 1;
    if (page>maximum) {
        state.maximum=maximum;
        SetPages(pager,maximum,1);
        RenderWeaponRows(false);
        return;
    }
    if (reset || maximum!=state.maximum) {
        state.maximum=maximum;
        SetPages(pager,maximum,page);
    }
    s.filteredOut=s.ready && s.owned && matches==0;
    for (unsigned i=0;i<kWeaponRows;++i) {
        auto& row=state.rows[i];
        const auto* look=visible[i];
        if (look && look!=row.look) {
            SetLabel(row.title,look->name);
            if (Readable(row.icon,0x410) && Value(row.icon)==0x4d48fac) {
                reinterpret_cast<void (__thiscall*)(void*,const wchar_t*)>(0x37f0260)(row.icon,look->icon);
                reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x37ed900)(row.icon,look->frame);
            }
        }
        row.look=look;
        const bool isApplied=look && look->id==state.selected;
        Show(row.icon,look!=nullptr);
        Show(row.title,look!=nullptr);
        Show(row.apply,look && !isApplied && !s.awaiting);
        Show(row.clear,isApplied && !s.awaiting);
        Show(row.applied,isApplied);
    }
}
