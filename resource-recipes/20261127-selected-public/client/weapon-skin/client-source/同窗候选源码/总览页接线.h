#pragma once

#pragma optimize("s", on)

bool Connected(void*& connection);
bool SenderIs(void* control, int sender);

void* SummaryPart(unsigned row, const wchar_t* suffix) {
    wchar_t id[64];
    constexpr wchar_t prefix[]=L"WS_SUMMARY_ROW";
    unsigned at=0;
    for (const wchar_t* p=prefix; *p; ++p) id[at++]=*p;
    id[at++]=static_cast<wchar_t>(L'0'+row);
    if (*suffix) id[at++]=L'_';
    for (; *suffix && at+1<ARRAYSIZE(id); ++suffix) id[at++]=*suffix;
    id[at]=0;
    return Find(Data().root,id);
}

void BindSummaryRows() {
    auto& s=Data();
    auto& summary=s.weapons->summary;
    Lock();
    __stosb(reinterpret_cast<unsigned char*>(&summary),0,sizeof(summary));
    Unlock();
    summary.note=L"正在读取最近获得……";
    summary.pager=Find(s.root,kPages[kSummaryPage].pager);
    for (unsigned i=0;i<kSummaryRows;++i) {
        summary.rows[i]={SummaryPart(i,L""),SummaryPart(i,L"ItemIcon"),SummaryPart(i,L"title"),
                         SummaryPart(i,L"txtSource"),SummaryPart(i,L"txtPeriod"),SummaryPart(i,L"btnApply"),
                         SummaryPart(i,L"BtnCancel"),SummaryPart(i,L"imgApply")};
    }
}

void CancelSummary() {
    auto& s=Data();
    if (!s.weapons) return;
    Lock();
    auto& summary=s.weapons->summary;
    summary.awaiting=0;
    summary.queued=false;
    Unlock();
    summary.wanted=0;
}

void FormatSummaryDate(wchar_t* out, const wchar_t* prefix, unsigned date) {
    for (;*prefix;++prefix) *out++=*prefix;
    const unsigned divisors[]={10000000,1000000,100000,10000,1000,100,10,1};
    for (unsigned i=0;i<ARRAYSIZE(divisors);++i) {
        if (i==4 || i==6) *out++=L'/';
        *out++=static_cast<wchar_t>(L'0'+(date/divisors[i])%10);
    }
    *out=0;
}

unsigned SummaryAppliedFont() {
    auto& s=Data();
    return s.damageNumberFont==kInvalidFont ? s.weapons->summary.page.appliedFont : s.damageNumberFont;
}

void RefreshSummaryActions() {
    auto& s=Data();
    auto& summary=s.weapons->summary;
    const unsigned font=SummaryAppliedFont();
    summary.shownFont=font;
    for (unsigned i=0;i<kSummaryRows;++i) {
        const auto& row=summary.rows[i];
        const auto& entry=summary.page.entries[i];
        const bool visible=(summary.renderedRows & (1u<<i))!=0;
        const bool weapon=entry.kind==SummaryWeapon;
        const bool applied=visible && entry.id==(weapon ? s.weapons->selected : font);
        const bool actionable=visible && (!weapon || (s.ready && !s.awaiting));
        Show(row.apply,actionable && !applied);
        Show(row.clear,actionable && applied);
        Show(row.applied,applied);
    }
}

void RenderSummaryRows() {
    auto& summary=Data().weapons->summary;
    summary.renderedRows=0;
    for (unsigned i=0;i<kSummaryRows;++i) {
        const auto& row=summary.rows[i];
        const auto& entry=summary.page.entries[i];
        const bool used=i<summary.page.count;
        const bool weapon=used && entry.kind==SummaryWeapon;
        const unsigned index=weapon ? WeaponLookPosition(entry.id) : kWeaponCatalogCount;
        const auto* look=index<kWeaponCatalogCount ? &kWeaponCatalog[index] : nullptr;
        auto* fontInfo=used && entry.kind==SummaryFont ? FontDefinition(entry.id) : nullptr;
        const bool visible=look || fontInfo;
        const bool showNote=i==0 && summary.page.count==0 && summary.note;
        Show(row.card,visible || showNote);
        Show(row.title,visible || showNote);
        Show(row.icon,visible);
        Show(row.source,visible);
        Show(row.period,visible && entry.expires);
        if (showNote) SetLabel(row.title,summary.note);
        if (!visible) continue;
        summary.renderedRows|=static_cast<unsigned char>(1u<<i);
        if (look) {
            SetLabel(row.title,look->name);
            if (Readable(row.icon,0x410) && Value(row.icon)==0x4d48fac) {
                reinterpret_cast<void (__thiscall*)(void*,const wchar_t*)>(0x37f0260)(row.icon,look->icon);
                reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x37ed900)(row.icon,look->frame);
            }
        } else {
            SetLabel(row.title,DefinitionText(fontInfo,4));
            SetFontIcon(row.icon,fontInfo);
        }
        wchar_t date[32];
        FormatSummaryDate(date,weapon ? L"武器外观 " : L"伤害字体 ",entry.acquired);
        SetLabel(row.source,date);
        if (entry.expires) {
            FormatSummaryDate(date,L"~",entry.expires);
            SetLabel(row.period,date);
        }
    }
    RefreshSummaryActions();
}

const SummaryEntry* HoveredSummaryEntry() {
    const auto& s=Data();
    if (s.page!=kSummaryPage || !s.visible || !s.weapons || s.previewSuspended || !WarehousePreviewFocused()) return nullptr;
    const auto& summary=s.weapons->summary;
    for (unsigned i=0;i<summary.page.count;++i) {
        const auto& row=summary.rows[i];
        if (!(summary.renderedRows & (1u<<i)) || !PreviewControlHit(row.card,true)) continue;
        return PreviewControlHit(row.apply) || PreviewControlHit(row.clear) ? nullptr : &summary.page.entries[i];
    }
    return nullptr;
}

void PumpSummary() {
    auto& s=Data();
    if (!s.weapons || s.page!=kSummaryPage || !s.bound || !s.visible || !Own(true)) return;
    auto& summary=s.weapons->summary;
    void* connection=nullptr;
    if (!Connected(connection) || Value(connection,0x2bcc34) || Value(connection,0x2bcc2c)) return;
    const DWORD tick=Tick();
    Lock();
    if (summary.awaiting && tick-summary.sentAt>6000 && !summary.queued) {
        summary.awaiting=0;
        summary.note=L"读取最近获得超时，请重新打开。";
        Unlock();
        RenderSummaryRows();
        return;
    }
    if (!summary.wanted || summary.awaiting || (summary.sentAt && tick-summary.sentAt<200)) {
        Unlock();
        return;
    }
    if (++s.serial==0) ++s.serial;
    summary.awaiting=s.serial;
    summary.sentAt=tick;
    summary.requested=summary.wanted;
    summary.wanted=0;
    const uint32_t id=summary.awaiting, epoch=s.epoch;
    Unlock();
    unsigned char body[20]={'W','S','O','1'};
    Put32(body+4,id);
    Put32(body+8,epoch);
    Put32(body+12,summary.requested);
    Put32(body+16,kWeaponCatalogChecksum);
    if (reinterpret_cast<LONG (__thiscall*)(void*,unsigned)>(0x374f280)(connection,opcode)>=0) {
        const auto append=reinterpret_cast<LONG (__thiscall*)(void*,const void*,unsigned)>(0x374eeb0)(connection,body,sizeof(body));
        const auto finish=reinterpret_cast<LONG (__cdecl*)()>(0x3750ac0)();
        if (append>=0 && finish>=0) return;
    }
    CancelSummary();
    summary.note=L"最近获得请求未发出，请重新打开。";
    RenderSummaryRows();
}

void ScheduleSummary(unsigned page) {
    auto& s=Data();
    if (!s.weapons || !s.visible || s.page!=kSummaryPage || !page || page>0xffff) return;
    s.weapons->summary.wanted=page;
    PumpSummary();
}

void ChangeSummaryPage() {
    auto& s=Data();
    if (!s.weapons || s.page!=kSummaryPage || !Own(true)) return;
    auto& summary=s.weapons->summary;
    if (summary.settingPager) return;
    const unsigned page=PageNumber(summary.pager);
    if (!page || page>summary.page.maximum) return;
    if (page==summary.requested && summary.awaiting) {
        summary.wanted=0;
    } else if (page!=summary.page.page || summary.awaiting) {
        ScheduleSummary(page);
    } else {
        summary.wanted=0;
    }
}

bool SummaryDateValid(unsigned day) {
    return day>=1000101 && day<=99991231 && day/100%100>=1 && day/100%100<=12 && day%100>=1 && day%100<=31;
}

bool DecodeSummary(const unsigned char* body, SummaryPage& page) {
    if (U32(body)!=0x324f5357 || U32(body+20)!=kWeaponCatalogChecksum || !U32(body+4) ||
        !U32(body+8) || body[12]>2 || body[13]>kSummaryRows) return false;
    page.id=U32(body+4);
    page.epoch=U32(body+8);
    page.status=body[12];
    page.count=body[13];
    page.page=static_cast<unsigned short>(U32(body+12)>>16);
    page.maximum=static_cast<unsigned short>(U32(body+16));
    page.appliedFont=static_cast<unsigned short>(U32(body+16)>>16);
    if (page.status) return !page.count && !page.page && !page.maximum && !page.appliedFont;
    if (!page.page || page.page>page.maximum || (page.page<page.maximum && page.count!=kSummaryRows)) return false;
    for (unsigned i=0;i<page.count;++i) {
        const auto* wire=body+24+i*16;
        const auto kind=U32(wire);
        const auto id=U32(wire+4);
        const auto acquired=U32(wire+8);
        const auto expires=U32(wire+12);
        if (!id || !SummaryDateValid(acquired)) return false;
        if (kind==SummaryWeapon) {
            if (expires || WeaponLookPosition(id)>=kWeaponCatalogCount) return false;
        } else if (kind!=SummaryFont || id>0xffff || (expires && !SummaryDateValid(expires))) return false;
        page.entries[i]={id,acquired,expires,static_cast<unsigned char>(kind)};
    }
    return true;
}

void ReceiveSummary(const unsigned char* body) {
    SummaryPage page;
    __stosb(reinterpret_cast<unsigned char*>(&page),0,sizeof(page));
    if (!DecodeSummary(body,page)) return;
    auto& s=Data();
    Lock();
    if (s.weapons && page.epoch==s.epoch) {
        auto& summary=s.weapons->summary;
        if (summary.awaiting==page.id && !summary.queued) {
            summary.reply=page;
            summary.queued=Post(SummaryReplyMessage);
        }
    }
    Unlock();
}

void FinishSummary() {
    auto& s=Data();
    if (!s.weapons) return;
    auto& summary=s.weapons->summary;
    Lock();
    if (!summary.queued || summary.reply.id!=summary.awaiting || summary.reply.epoch!=s.epoch) {
        Unlock(); return;
    }
    const auto page=summary.reply;
    summary.awaiting=0;
    summary.queued=false;
    Unlock();
    if (s.page!=kSummaryPage || !Own(true)) return;
    if (page.status==2) {
        if (!summary.wanted) summary.wanted=summary.requested;
        return;
    }
    if (page.status) {
        summary.note=L"当前无法读取最近获得。";
    } else {
        summary.page=page;
        summary.requested=page.page;
        summary.note=page.count ? nullptr : L"暂无最近获得记录。";
        summary.settingPager=true;
        SetPages(summary.pager,page.maximum,page.page);
        summary.settingPager=false;
    }
    RenderSummaryRows();
    PumpSummary();
}

void TickSummary() {
    auto& s=Data();
    if (!s.weapons || !s.bound) return;
    auto& summary=s.weapons->summary;
    if (SummaryAppliedFont()!=summary.shownFont) RefreshSummaryActions();
    PumpSummary();
}

bool SummaryEvent(int sender, int event) {
    auto& s=Data();
    if (!s.weapons || !UIThread() || !Own(true)) return false;
    auto& summary=s.weapons->summary;
    if (event==24 && SenderIs(summary.pager,sender)) {
        if (!summary.settingPager) Post(SummaryPagerMessage);
        return true;
    }
    if (event!=13) return false;
    for (unsigned i=0;i<summary.page.count;++i) {
        const auto& row=summary.rows[i];
        const bool clear=SenderIs(row.clear,sender);
        if (!clear && !SenderIs(row.apply,sender)) continue;
        const auto& entry=summary.page.entries[i];
        if (entry.kind==SummaryWeapon) {
            QueueWeaponSelection(entry.id,clear);
        } else {
            const auto list=NativeFonts(s.window);
            for (unsigned index=0;index<list.count;++index) {
                if (list.ids[index]!=entry.id) continue;
                ApplyNativeFont(list,index,clear);
                break;
            }
        }
        return true;
    }
    return false;
}

#pragma optimize("", on)
