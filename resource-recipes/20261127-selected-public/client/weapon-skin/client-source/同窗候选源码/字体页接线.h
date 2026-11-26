#pragma once

// 原生 202 持有的字体编号、应用位置和字体定义。可见行由仓库分页管理。
constexpr unsigned kFontRows = 5;
constexpr unsigned kInvalidFont = ~0u;

struct FontList {
    const unsigned* ids;
    unsigned count;
};

FontList NativeFonts(void* owner) {
    if (!Readable(owner,0x294)) return {};
    const auto begin = Value(owner,0x288);
    const auto end = Value(owner,0x28c);
    const auto cap = Value(owner,0x290);
    constexpr unsigned width = sizeof(unsigned);
    if (!begin || end<=begin || cap<end || (end-begin)%width || (end-begin)/width>65536) return {};
    const auto* ids = reinterpret_cast<const unsigned*>(begin);
    if (!Readable(ids,end-begin)) return {};
    return {ids,(end-begin)/width};
}

void SetLabel(void* control, const wchar_t* text) {
    if (Readable(control,0x410) && Value(control)==0x4d4fa4c)
        reinterpret_cast<void (__thiscall*)(void*, const wchar_t*)>(0x3835940)(control,text);
}

unsigned PageNumber(void* pager) {
    return Readable(pager,0x3d8) ? reinterpret_cast<unsigned (__thiscall*)(void*)>(0x35f8760)(pager) : 1;
}

void SetPages(void* pager, unsigned maximum, unsigned page) {
    if (!Readable(pager,0x3d8)) return;
    using Set = void (__thiscall*)(void*,unsigned);
    reinterpret_cast<Set>(0x35f86a0)(pager,maximum);
    reinterpret_cast<Set>(0x35f8740)(pager,page);
}

void AddCategory(void* combo, const wchar_t* label, unsigned key) {
    if (!Readable(combo,0x410)) return;
    using Add = uintptr_t (__thiscall*)(void*,const wchar_t*,unsigned,unsigned,unsigned,unsigned,unsigned,unsigned,NativeRef,unsigned);
    const auto normal=Value(reinterpret_cast<void*>(0x4d4ce88));
    const auto rollover=Value(reinterpret_cast<void*>(0x4d4ce58));
    const auto disabled=Value(reinterpret_cast<void*>(0x4d4cdb0));
    reinterpret_cast<Add>(0x38440d0)(combo,label,key,0,normal,rollover,rollover,disabled,{},1);
    using Select=void (__thiscall*)(void*,unsigned);
    reinterpret_cast<Select>(0x38435f0)(combo,key);
    reinterpret_cast<Select>(0x3843770)(combo,key);
}

const wchar_t* DefinitionText(const void* entry, unsigned start) {
    if (!Readable(entry,start+28)) return L"";
    const auto length = Value(entry,start+16);
    const auto capacity = Value(entry,start+20);
    if (length>capacity || length>512) return L"";
    const auto* text = capacity<8 ? reinterpret_cast<const wchar_t*>(static_cast<const char*>(entry)+start)
                                  : reinterpret_cast<const wchar_t*>(Value(entry,start));
    return Readable(text,(length+1)*sizeof(wchar_t)) && !text[length] ? text : L"";
}

void* FontDefinition(unsigned id) {
    if (id>0xffff) return nullptr;
    void* manager = reinterpret_cast<void* (__cdecl*)()>(0x1691c00)();
    if (!manager) return nullptr;
    return reinterpret_cast<void* (__thiscall*)(void*,unsigned)>(0x168d330)(manager,id);
}

void SetFontIcon(void* icon, const void* entry) {
    if (!Readable(icon,0x410) || Value(icon)!=0x4d48fac) return;
    const auto* pack = DefinitionText(entry,0x68);
    if (!*pack || !Readable(entry,0x80)) return;
    reinterpret_cast<void (__thiscall*)(void*,const wchar_t*)>(0x37f0260)(icon,pack);
    reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x37ed900)(icon,Value(entry,0x60));
}

constexpr const wchar_t* kFontCardIds[kFontRows] = {
    L"WS_DAMAGEFONT_ROW0",L"WS_DAMAGEFONT_ROW1",L"WS_DAMAGEFONT_ROW2",L"WS_DAMAGEFONT_ROW3",L"WS_DAMAGEFONT_ROW4"
};

void* FontCardPart(unsigned row, const wchar_t* suffix) {
    if (row>=kFontRows) return nullptr;
    wchar_t id[64];
    unsigned at=0;
    for (const wchar_t* p=kFontCardIds[row]; *p; ++p) id[at++]=*p;
    id[at++]=L'_';
    for (; *suffix && at+1<ARRAYSIZE(id); ++suffix) id[at++]=*suffix;
    id[at]=0;
    return Find(Data().root,id);
}

void RefreshFontPage(bool force) {
    auto& s=Data();
    if (s.page!=kFontPage || !Own(true)) return;
    auto list=NativeFonts(s.window);
    auto* pager=Find(s.root,kPages[kFontPage].pager);
    const unsigned maximum=list.count ? (list.count+kFontRows-1)/kFontRows : 1;
    const unsigned current=static_cast<unsigned>(Value(s.window,0x260));
    unsigned page=PageNumber(pager);
    if (!page || page>maximum) page=1;
    const unsigned first=(page-1)*kFontRows;
    bool change=force || page!=s.fontPage || s.fontCount!=list.count || s.fontApplied!=current;
    for (unsigned row=0;row<kFontRows;++row) {
        const unsigned index=first+row;
        const unsigned id=index<list.count ? list.ids[index] : kInvalidFont;
        change=change || id!=s.fontIds[row];
        s.fontIds[row]=id;
    }
    if (!change) return;
    if (force || s.fontCount!=list.count) SetPages(pager,maximum,page);
    s.fontPage=page;
    s.fontCount=list.count;
    s.fontApplied=current;
    for (unsigned row=0;row<kFontRows;++row) {
        auto* card=Find(s.root,kFontCardIds[row]);
        s.fontCards[row]=card;
        const unsigned id=s.fontIds[row];
        auto* info=id==kInvalidFont ? nullptr : FontDefinition(id);
        Show(card,info!=nullptr);
        if (!info) continue;
        const bool applied=first+row==current;
        auto* icon=FontCardPart(row,L"ItemIcon");
        auto* title=FontCardPart(row,L"title");
        auto* detail=FontCardPart(row,L"txtSource");
        SetFontIcon(icon,info);
        SetLabel(title,DefinitionText(info,4));
        SetLabel(detail,DefinitionText(info,0x24));
        Show(icon,true);
        Show(title,true);
        Show(detail,true);
        Show(FontCardPart(row,L"btnApply"),!applied);
        Show(FontCardPart(row,L"BtnCancel"),applied && id!=0);
        Show(FontCardPart(row,L"imgApply"),applied);
    }
    Show(Find(s.root,L"WS_DAMAGEFONT_empty_panel01"),list.count==0);
}

void SendDefaultFont();

void ApplyNativeFont(const FontList& list, unsigned index, bool clear) {
    auto& s=Data();
    if (index>=list.count || !FontDefinition(list.ids[index])) return;
    if (clear) {
        if (Value(s.window,0x260)!=index) return;
        for (index=0;index<list.count && list.ids[index]!=0;++index) {}
        if (index==list.count) { SendDefaultFont(); return; }
    }
    auto* selected=reinterpret_cast<unsigned*>(static_cast<char*>(s.window)+0x264);
    *selected=index;
    reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x113bbf0)(s.window,list.ids[index]);
}

void ApplyFontPageRow(unsigned row, bool clear) {
    auto& s=Data();
    if (s.page!=kFontPage || !Own(true) || !UIThread() || !s.fontPage || row>=kFontRows) return;
    const auto list=NativeFonts(s.window);
    const unsigned index=(s.fontPage-1)*kFontRows+row;
    if (index<list.count && list.ids[index]==s.fontIds[row]) ApplyNativeFont(list,index,clear);
}

void RefreshFontOpacity() {
    auto& s=Data();
    auto* slider=reinterpret_cast<void*>(Value(s.window,0x2b0));
    if (slider!=Find(s.root,L"alpha_slider") || !Readable(slider,0x3c0)) return;
    const void* value=reinterpret_cast<const void*>(Value(slider,0x3bc));
    if (!Readable(value,8)) return;
    const unsigned opacity=static_cast<unsigned>(Value(value,4));
    if (opacity>255) return;
    if (opacity!=s.pendingOpacity) s.savedOpacity=kInvalidFont;
    if (s.opacitySampled && opacity==s.pendingOpacity && opacity!=s.savedOpacity && opacity!=Value(s.window,0x2c0)) {
        auto* selected=reinterpret_cast<unsigned*>(static_cast<char*>(s.window)+0x264);
        *selected=static_cast<unsigned>(Value(s.window,0x260));
        // 只有透明度变化时走原生保存分支，不提交不同的字体编号。
        reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x113bbf0)(s.window,0);
        s.savedOpacity=opacity;
    }
    s.opacitySampled=true;
    s.pendingOpacity=opacity;
}
