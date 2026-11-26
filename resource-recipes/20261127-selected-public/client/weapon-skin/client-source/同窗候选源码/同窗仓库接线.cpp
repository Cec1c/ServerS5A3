#include <windows.h>
#include <stdint.h>
#include <intrin.h>
#include <initializer_list>
#include "同窗布局.h"
#include "仓库入口悬停.h"

#define WINAPI_AT(name, address) (*reinterpret_cast<decltype(&::name)*>(address))

namespace {
constexpr uintptr_t stateAddress = 0x7089cd0;
constexpr uintptr_t managerAddress = 0x56a3798;
constexpr unsigned opcode = 0x7f04;
constexpr int windowID = 202;
constexpr unsigned kWeaponRows = 7, searchLimit = 112;
constexpr unsigned kSummaryPage = 0, kFontPage = 2, kEmoticonPage = 4, kWeaponPage = 5;
constexpr UINT_PTR kWarehouseRefreshTimer = 0x57530001;
constexpr wchar_t fontPath[] = L"UI/DamageFontSkin/skinselectwindow.xui";
constexpr wchar_t kWarehouseEntryTitlePath[] = L"UI/DamageFontSkin/damagefonttitle.xui";
enum : WPARAM {
    WeaponReplyMessage = 2, ApplyMessage, ClearMessage, ResetMessage, SearchMessage, InitMessage,
    RefreshFontPageMessage, RefreshWeaponStateMessage, PageMessageBase = 16,
    ForgeOpenMessage = 32, ForgeCloseMessage, ForgeSelectMessage, ForgeAskMessage,
    ForgeConfirmMessage, ForgeDismissMessage, ForgeReplyMessage, ForgeRegisteredMessage, RefreshWeaponPageMessage,
    SummaryReplyMessage, SummaryPagerMessage
};

struct ForgeState;

struct WeaponLook {
    unsigned source, id, products[2];
    const wchar_t *name, *icon;
    unsigned frame, jobs;
};
static_assert(sizeof(WeaponLook)==32, "外部目录行尺寸变化");
#include "武器外观目录.h"
constexpr unsigned kWeaponFlagsSize = (ARRAYSIZE(kWeaponCatalog)+7)/8;

struct WeaponRow {
    void *card, *icon, *title, *apply, *clear, *applied;
    const WeaponLook* look;
};

constexpr unsigned kSummaryRows = 5;
constexpr unsigned kSummaryReplyBytes = 24+16*kSummaryRows;
enum : unsigned char { SummaryWeapon=1, SummaryFont=2 };
struct SummaryEntry {
    uint32_t id, acquired, expires;
    unsigned char kind;
};
struct SummaryPage {
    uint32_t id, epoch;
    unsigned short page, maximum, appliedFont;
    unsigned char status, count;
    SummaryEntry entries[kSummaryRows];
};
struct SummaryRow {
    void *card, *icon, *title, *source, *period, *apply, *clear, *applied;
};
struct SummaryState {
    SummaryRow rows[kSummaryRows];
    SummaryPage page, reply;
    void* pager;
    const wchar_t* note;
    uint32_t awaiting;
    DWORD sentAt;
    unsigned wanted, requested, shownFont;
    bool queued, settingPager;
    unsigned char renderedRows;
};

struct WeaponPageState {
    WeaponRow rows[kWeaponRows];
    unsigned selected, preview, clicked, clickedRevision, maximum;
    unsigned char flags[kWeaponFlagsSize], received[kWeaponFlagsSize];
    wchar_t query[searchLimit+1];
    SummaryState summary;
};

struct Response {
    uint32_t id, epoch, selected, revision;
    unsigned char status, action, owned;
};

struct State {
    HWND hwnd;
    WNDPROC previous;
    DWORD thread;
    UINT message;
    void* manager;
    void* window;
    void* apply;
    void* clear;
    void* applied;
    void* statusText;
    UINT_PTR timer;
    SRWLOCK lock;
    Response reply;
    uint32_t epoch, serial, awaiting, revision;
    DWORD sentAt;
    unsigned char expectedAction;
    // 保留原仓库状态偏移，界面与网络回包沿用同一状态区。
    bool weaponListRefreshPending, visible, ready, owned, selected, queued, bound;
    void* root;
    void* tabs[ARRAYSIZE(kPages)];
    unsigned page;
    void* searchEdit;
    void* searchButton;
    void* initButton;
    void* itemIcon;
    void* itemTitle;
    const wchar_t* weaponStatus;
    bool filteredOut;
    unsigned fontPage, fontCount, fontApplied;
    unsigned fontIds[5];
    unsigned pendingOpacity, savedOpacity;
    bool opacitySampled;
    bool previewSuspended;
    void* fontCards[5];
    void* fontPreviewWindow;
    unsigned previewFont;
    void* characterPreviewAnchor;
    void* weaponPreviewCard;
    void* characterPreview;
    void* previewRole;
    void* previewWeapon;
    unsigned previewWeaponSlot;
    bool previewWantsWeapon;
    void* damageNumberAnchor;
    void* damageNumberButton;
    void* damageNumberHint;
    void* damageNumber;
    unsigned damageNumberFont;
    bool damageNumberAnimated;
    ForgeState* forge;
    uint32_t registerEpoch;
    uint32_t registerAwaiting;
    DWORD registerSentAt;
    unsigned char registerStatus;
    bool registerQueued;
    WeaponPageState* weapons;
};
static_assert(offsetof(State,damageNumberAnchor) == 0x100, "仓库原状态偏移变化");
static_assert(offsetof(State,forge) == 0x118 && offsetof(State,registerEpoch)==0x11c && sizeof(State)==0x130, "制作窗与场景状态区边界变化");

State& Data() { return *reinterpret_cast<State*>(stateAddress); }
uintptr_t Value(const void* pointer, unsigned offset = 0) {
    return *reinterpret_cast<const uintptr_t*>(static_cast<const char*>(pointer) + offset);
}
uint32_t U32(const unsigned char* p) {
    return uint32_t(p[0]) | uint32_t(p[1]) << 8 | uint32_t(p[2]) << 16 | uint32_t(p[3]) << 24;
}
void Put32(unsigned char* p, uint32_t v) {
    p[0] = static_cast<unsigned char>(v);
    p[1] = static_cast<unsigned char>(v >> 8);
    p[2] = static_cast<unsigned char>(v >> 16);
    p[3] = static_cast<unsigned char>(v >> 24);
}
bool Readable(const void* p, size_t size) {
    uintptr_t cursor = reinterpret_cast<uintptr_t>(p);
    if (!cursor || size > UINTPTR_MAX - cursor) return false;
    const uintptr_t end = cursor + size;
    for (;;) {
        MEMORY_BASIC_INFORMATION info;
        if (!WINAPI_AT(VirtualQuery, 0x6ee5038)(reinterpret_cast<void*>(cursor), &info, sizeof(info)) ||
            info.State != MEM_COMMIT || (info.Protect & (PAGE_GUARD | PAGE_NOACCESS))) return false;
        const uintptr_t next = reinterpret_cast<uintptr_t>(info.BaseAddress) + info.RegionSize;
        if (next <= cursor) return false;
        if (next >= end) return true;
        cursor = next;
    }
}
bool Same(const wchar_t* path, const wchar_t* want, size_t count) {
    // 通用载入器也处理其他类型的窗口，先排除常见路径。
    if (!Readable(path, 2 * count) || path[0] != want[0]) return false;
    for (size_t i = 0; i < count; ++i) if (path[i] != want[i]) return false;
    return true;
}
void Lock() { WINAPI_AT(AcquireSRWLockExclusive, 0x6ee501c)(&Data().lock); }
void Unlock() { WINAPI_AT(ReleaseSRWLockExclusive, 0x6ee5018)(&Data().lock); }
DWORD Tick() { return WINAPI_AT(GetTickCount, 0x6ee5030)(); }
bool UIThread() { return Data().thread == WINAPI_AT(GetCurrentThreadId, 0x6ee5028)(); }
bool Post(WPARAM msg) {
    return Data().hwnd && Data().message && WINAPI_AT(PostMessageW, 0x6ee504c)(Data().hwnd, Data().message, msg, 0);
}

struct NativeRef { void* value; void* counter; };
void* Find(void* root, const wchar_t* id) {
    if (!Readable(root, 0x100)) return nullptr;
    NativeRef ref = {};
    reinterpret_cast<NativeRef* (__thiscall*)(void*, NativeRef*, const wchar_t*)>(0x37eb120)(root, &ref, id);
    void* value = ref.value;
    reinterpret_cast<void (__thiscall*)(NativeRef*)>(0xf11f90)(&ref);
    return value;
}
bool InWindowVector(unsigned offset) {
    const auto& s = Data();
    if (!Readable(s.manager, 0xc640)) return false;
    auto* vec = static_cast<unsigned char*>(s.manager) + (windowID * 5 + offset) * 4;
    uintptr_t begin = Value(vec, 4), end = Value(vec, 8);
    if (end <= begin || (end - begin) % 4 || end - begin > 64 || !Readable(reinterpret_cast<const void*>(begin), end - begin)) return false;
    for (uintptr_t at = begin; at < end; at += 4) if (*reinterpret_cast<void**>(at) == s.window) return true;
    return false;
}
bool Own(bool active) {
    return Data().window && Value(reinterpret_cast<void*>(managerAddress)) == reinterpret_cast<uintptr_t>(Data().manager) &&
        (InWindowVector(0x41) || (!active && InWindowVector(0x1879)));
}
void Show(void* control, bool visible) {
    if (!Readable(control, 4)) return;
    const auto* table = reinterpret_cast<void*>(Value(control));
    if (Readable(table, 12)) reinterpret_cast<void (__thiscall*)(void*, bool)>(Value(table, 8))(control, visible);
}
#include "字体页接线.h"
#include "原生字体预览.h"
#include "人物武器预览.h"
#include "左栏伤害数字.h"
#include "武器仓库页.h"
#include "总览页接线.h"

void Render(const wchar_t* text) {
    auto& s = Data();
    if (!Own(true)) return;
    if (text) s.weaponStatus = text;
    const bool weapon = s.page == kWeaponPage;
    if (weapon && s.weapons) RenderWeaponRows(false);
    if (s.page == kSummaryPage && s.weapons) RefreshSummaryActions();
    Show(s.statusText,weapon);
    const wchar_t* status = s.filteredOut ? L"没有匹配的武器外观。" : s.weaponStatus;
    if (weapon && status && Readable(s.statusText,0x410) && Value(s.statusText) == 0x4d4fa4c)
        reinterpret_cast<void (__thiscall*)(void*, const wchar_t*)>(0x3835940)(s.statusText,status);
}

void CancelQuery() {
    auto& s = Data();
    Lock();
    s.awaiting = 0;
    s.queued = false;
    Unlock();
}
void Close(bool nativeClose) {
    auto& s = Data();
    CloseFontPreview();
    DestroyDamageNumber();
    if (Own(true)) Show(s.damageNumberHint,false);
    DestroyCharacterPreview();
    CancelQuery();
    CancelSummary();
    if (s.timer) WINAPI_AT(KillTimer, 0x6ee505c)(s.hwnd, s.timer);
    s.timer = 0;
    if (nativeClose && s.visible && Own(true))
        reinterpret_cast<void (__thiscall*)(void*, int, int, int)>(0x32b94a0)(s.manager, windowID, -1, 0);
    s.visible = s.ready = s.owned = s.selected = false;
    s.weaponListRefreshPending = false;
    if (!Own(false)) {
        s.window = nullptr;
        s.bound = false;
    }
}

bool SenderIs(void* control, int sender) {
    return sender != 0 && Readable(control,0xc0) &&
        (reinterpret_cast<uintptr_t>(control) == static_cast<uintptr_t>(sender) ||
         Value(control,0xbc) == static_cast<uintptr_t>(sender));
}

constexpr uintptr_t editBoxVtable = 0x4d183bc;
constexpr uintptr_t readEditText = 0x3859310;
constexpr uintptr_t writeEditText = 0x385b5a0;

bool NativeEdit(void* control) {
    return Readable(control,0x498) && Value(control) == editBoxVtable &&
        Value(reinterpret_cast<void*>(editBoxVtable),0xe4) == readEditText &&
        Value(reinterpret_cast<void*>(editBoxVtable),0xec) == writeEditText;
}

bool ClearSearch(void* control) {
    return NativeEdit(control) &&
        reinterpret_cast<int (__cdecl*)(void*, const wchar_t*)>(writeEditText)(control,L"") == 1;
}

void FilterWeapons(bool reset) {
    auto& s = Data();
    if (s.page != kWeaponPage || !s.bound || !s.visible || !s.weapons || !Own(true) || !UIThread()) return;
    if (!NativeEdit(s.searchEdit)) { Render(L"无法读取武器搜索框。"); return; }
    if (reset) {
        if (!ClearSearch(s.searchEdit)) { Render(L"无法清空搜索条件。"); return; }
        s.weapons->query[0] = 0;
        RenderWeaponRows(true);
        Render(nullptr);
        return;
    }
    const auto* query = reinterpret_cast<const wchar_t* (__thiscall*)(void*)>(readEditText)(s.searchEdit);
    if (query) {
        for (unsigned length = 0; length <= searchLimit && Readable(query+length,sizeof(wchar_t)); ++length) {
            if (query[length]) continue;
            for (unsigned i=0;i<=length;++i) s.weapons->query[i]=query[i];
            RenderWeaponRows(true);
            Render(nullptr);
            return;
        }
    }
    Render(L"无法读取武器搜索内容。");
}

void* RootControl(void* owner) {
    if (!Readable(owner,0x1b4) || Value(owner) != 0x471b534 || Value(owner,0x1b0) != 0x471b528) return nullptr;
    return reinterpret_cast<void*>(Value(owner,0x30));
}

void* WarehouseRoot(void* owner) {
    auto* root = RootControl(owner);
    if (!root) return nullptr;
    const auto& s = Data();
    if (s.bound && s.window == owner && s.root == root) return root;
    return Find(root,L"WS_PAGE_TITLE") ? root : nullptr;
}

bool Bind(void* owner) {
    auto& s = Data();
    if (!UIThread()) return false;
    auto* root = WarehouseRoot(owner);
    if (!root) return false;
    if (s.window == owner && s.bound && s.visible && Own(true)) return true;
    const bool cachedLayout = s.window == owner && s.bound && s.root == root;
    if (s.visible || s.timer) Close(false);
    s.manager = *reinterpret_cast<void**>(managerAddress);
    s.window = owner;
    s.root = root;
    for (unsigned page = 0; page < ARRAYSIZE(kPages); ++page) {
        s.tabs[page] = Find(root,kPages[page].button);
        if (!s.tabs[page]) { s.bound = false; return false; }
    }
    s.apply = Find(root,L"btnApply");
    s.clear = Find(root,L"BtnCancel");
    s.applied = Find(root,L"imgApply");
    s.statusText = Find(root,L"WS_STATUS");
    s.searchEdit = Find(root,L"textSearch");
    s.searchButton = Find(root,L"btnSearch");
    s.initButton = Find(root,L"btnInit");
    s.itemIcon = Find(root,L"ItemIcon");
    s.itemTitle = Find(root,L"title");
    s.characterPreviewAnchor = Find(root,L"WS_PREVIEW_ctrlCharacter");
    s.weaponPreviewCard = Find(root,L"WS_WEAPONSKIN_ROW0");
    s.damageNumberAnchor = Find(root,L"WS_PREVIEW_ctrlDamageFont");
    s.damageNumberButton = Find(root,L"WS_PREVIEW_ctrlDamageFontBtn");
    s.damageNumberHint = Find(root,L"WS_DAMAGE_NUMBER_HINT");
    Show(s.damageNumberHint,false);
    s.bound = s.apply && s.clear && s.applied && s.statusText && s.searchEdit &&
        s.searchButton && s.initButton && s.itemIcon && s.itemTitle && Own(true) && BindWeaponRows();
    if (!s.bound) return false;
    BindSummaryRows();
    s.visible = true;
    if (!cachedLayout) s.page = 0;
    s.ready = s.selected = s.owned = false;
    s.revision = 0;
    s.filteredOut = false;
    s.weaponStatus = L"正在读取武器外观……";
    s.fontPage = s.fontCount = 0;
    s.fontApplied = kInvalidFont;
    s.opacitySampled = false;
    s.savedOpacity = kInvalidFont;
    s.previewFont = kInvalidFont;
    s.damageNumberFont = kInvalidFont;
    for (auto& id : s.fontIds) id = kInvalidFont;
    RenderSummaryRows();
    if (!cachedLayout) {
        for (const auto& layout:kPages) {
            if (layout.combo) AddCategory(Find(root,layout.combo),L"全部",0);
            SetPages(Find(root,layout.pager),1,1);
        }
    }
    if (cachedLayout) ClearSearch(s.searchEdit);
    CancelQuery();
    Lock();
    if (++s.epoch == 0) ++s.epoch;
    Unlock();
    // 有窗口句柄时以申请的编号接收 WM_TIMER，并用同一编号取消。
    if (WINAPI_AT(SetTimer,0x6ee5058)(s.hwnd,kWarehouseRefreshTimer,250,nullptr))
        s.timer = kWarehouseRefreshTimer;
    return true;
}

bool Connected(void*& connection) {
    connection = *reinterpret_cast<void**>(0x57ed8d4);
    return Readable(connection, 0x2bd069) && static_cast<const unsigned char*>(connection)[8] != 0 &&
        static_cast<const unsigned char*>(connection)[0x2bd068] == 0;
}

void SendDefaultFont() {
    void* connection=nullptr;
    if (!Connected(connection) || Value(connection,0x2bcc34) || Value(connection,0x2bcc2c)) return;
    using Begin = LONG (__thiscall*)(void*,unsigned);
    using Append = LONG (__thiscall*)(void*,const unsigned char*,unsigned);
    using Finish = LONG (__cdecl*)();
    const unsigned char value[2]={};
    if (reinterpret_cast<Begin>(0x374f280)(connection,0x508)<0) return;
    reinterpret_cast<Append>(0x374eeb0)(connection,value,sizeof(value));
    reinterpret_cast<Finish>(0x3750ac0)();
}
void Fetch(unsigned char action, unsigned id=0) {
    auto& s = Data();
    void* connection = nullptr;
    if (!UIThread() || !s.bound || !Own(true)) return;
    if (!Connected(connection) || Value(connection, 0x2bcc34) || Value(connection, 0x2bcc2c)) {
        s.ready = false;
        Render(L"请进入角色后再打开武器外观。");
        return;
    }
    unsigned char body[28] = {'W','S','Q','2'};
    Lock();
    if (s.awaiting) { Unlock(); return; }
    if (++s.serial == 0) ++s.serial;
    if (s.epoch == 0) s.epoch = 1;
    s.awaiting = s.serial;
    s.expectedAction = action;
    Put32(body + 4, s.awaiting);
    Put32(body + 8, s.epoch);
    Unlock();
    body[12] = action;
    Put32(body + 16, action ? s.revision : 0);
    Put32(body + 20, id);
    Put32(body + 24, kWeaponCatalogChecksum);
    s.sentAt = Tick();
    Render(L"正在同步武器外观……");
    using Begin = LONG (__thiscall*)(void*, unsigned);
    using Append = LONG (__thiscall*)(void*, const void*, unsigned);
    using Finish = LONG (__cdecl*)();
    if (reinterpret_cast<Begin>(0x374f280)(connection, opcode) >= 0) {
        const LONG appended = reinterpret_cast<Append>(0x374eeb0)(connection, body, sizeof(body));
        const LONG finished = reinterpret_cast<Finish>(0x3750ac0)();
        if (appended >= 0 && finished >= 0) {
            if (action == 0) s.weaponListRefreshPending = false;
            return;
        }
    }
    CancelQuery();
    Render(L"请求未发出，请重新打开。");
}
void SelectPage(unsigned page) {
    auto& s = Data();
    if (page >= ARRAYSIZE(kPages) || !UIThread() || !s.bound || !s.visible || !Own(true)) return;
    if (page!=kFontPage || s.page==kSummaryPage) CloseFontPreview();
    for (unsigned index = 0; index < ARRAYSIZE(kPages); ++index) {
        Show(s.tabs[index],index!=page);
        Show(Find(s.root,kPages[index].active),index==page);
        Show(Find(s.root,kPages[index].control),index==page);
    }
    Show(Find(s.root,L"WS_EMPTY_EMOTICON"),page==kEmoticonPage);
    s.page = page;
    if (page==kFontPage) {
        s.opacitySampled=false;
        s.savedOpacity=kInvalidFont;
        RefreshFontPage(true);
    }
    Render(nullptr);
    if (page==kSummaryPage) ScheduleSummary(1);
    if (page!=kWeaponPage || s.ready) return;
    if (s.timer) Fetch(0);
    else Render(L"无法等待服务器回复，请重新打开。");
}

void* Open(void* manager) {
    auto* window = reinterpret_cast<void* (__thiscall*)(void*, int, int, int)>(0x32bea50)(manager,windowID,0,0);
    if (Bind(window)) {
        SelectPage(0);
        RefreshAppliedDamageNumber();
        if (Data().timer && !Data().ready) Post(RefreshWeaponStateMessage);
    }
    return window;
}

void FinishQuery() {
    auto& s = Data();
    Lock();
    const Response r = s.reply;
    const bool current = s.queued && r.id == s.awaiting && r.epoch == s.epoch;
    const bool hasState = r.status == 0 || r.status == 2 || r.status == 3;
    if (current && s.weapons && hasState) {
        for (unsigned i=0;i<kWeaponFlagsSize;++i) s.weapons->flags[i]=s.weapons->received[i];
    }
    s.queued = false;
    if (current) s.awaiting = 0;
    Unlock();
    void* connection = nullptr;
    if (!current || !s.visible || !s.weapons || !Own(true) || !Connected(connection)) return;
    if (r.status == 4) {
        if (r.action == 0) s.weaponListRefreshPending = true;
        Render(L"操作过快，请稍后再试。");
        return;
    }
    s.ready = hasState;
    if (s.ready) {
        s.owned = r.owned != 0;
        s.selected = r.selected != 0;
        s.weapons->selected = r.selected;
        s.revision = r.revision;
    }
    const wchar_t* message = s.selected ? L"已应用武器外观。" : L"可应用已登记的武器外观。";
    if (!s.owned && r.status == 0) message = L"当前角色暂无可用的武器外观。";
    if (r.status == 1) message = L"当前场景或配套暂不可用。";
    if (r.status == 2) message = L"当前角色尚未登记该武器外观。";
    if (r.status == 3) message = L"状态已更新，请再操作。";
    Render(message);
}

#include "林纳斯制作窗.h"
#include "武器入库确认.h"

LRESULT CALLBACK WindowProc(HWND hwnd, UINT message, WPARAM wparam, LPARAM lparam) {
    auto& s = Data();
    __try {
        if (message==WM_ACTIVATEAPP && hwnd==s.hwnd) {
            s.previewSuspended = !wparam;
            if (s.previewSuspended) {
                CloseFontPreview();
                if (Own(true)) Show(s.damageNumberHint,false);
            }
        }
        if (message == s.message && !lparam) {
            if (wparam >= PageMessageBase && wparam < PageMessageBase+ARRAYSIZE(kPages)) {
                SelectPage(static_cast<unsigned>(wparam-PageMessageBase));
                return 1;
            }
            switch (wparam) {
            case WeaponReplyMessage: FinishQuery(); return 1;
            case SummaryReplyMessage: FinishSummary(); return 1;
            case SummaryPagerMessage: ChangeSummaryPage(); return 1;
            case ApplyMessage: case ClearMessage:
                if (s.weapons) {
                    const unsigned id=s.weapons->clicked;
                    s.weapons->clicked=0;
                    if ((s.page==kWeaponPage || s.page==kSummaryPage) && s.ready &&
                        s.weapons->clickedRevision==s.revision && id) {
                        if (wparam==ApplyMessage && id!=s.weapons->selected) Fetch(1,id);
                        if (wparam==ClearMessage && id==s.weapons->selected) Fetch(2);
                    }
                }
                return 1;
            case RefreshWeaponPageMessage: Render(nullptr); return 1;
            case ResetMessage: ForgeClose(); Close(true); return 1;
            case SearchMessage: FilterWeapons(false); return 1;
            case InitMessage: FilterWeapons(true); return 1;
            case RefreshFontPageMessage: RefreshFontPage(false); return 1;
            case RefreshWeaponStateMessage: if (s.visible && !s.ready) Fetch(0); return 1;
            case ForgeOpenMessage: ForgeOpen(); return 1;
            case ForgeCloseMessage: ForgeClose(); return 1;
            case ForgeSelectMessage: ForgeSelectWeapon(); return 1;
            case ForgeAskMessage: ForgeAsk(); return 1;
            case ForgeConfirmMessage: ForgeConfirm(); return 1;
            case ForgeDismissMessage: if (s.forge) ForgeCloseView(s.forge->confirm); return 1;
            case ForgeReplyMessage: ForgeFinishReply(); return 1;
            case ForgeRegisteredMessage: FinishWeaponRegistration(); return 1;
            }
        }
        if (message==WM_TIMER && wparam==kForgeTimer) { ForgeTick(); return 0; }
        if (message == WM_TIMER && s.timer && wparam == s.timer) {
            void* connection = nullptr;
            if (!Own(true) || !Connected(connection)) { Close(false); return 0; }
            RefreshAppliedDamageNumber();
            if (s.page==kSummaryPage) TickSummary();
            if (s.page==kFontPage) {
                reinterpret_cast<void (__thiscall*)(void*)>(0x113ba50)(s.window);
                RefreshFontPage(false);
                RefreshFontOpacity();
            }
            const auto* hovered=HoveredSummaryEntry();
            const unsigned weapon=hovered && hovered->kind==SummaryWeapon ? hovered->id : 0;
            const unsigned font=hovered && hovered->kind==SummaryFont ? hovered->id : kInvalidFont;
            if (s.page==kSummaryPage || s.page==kFontPage)
                RefreshFontPreview(s.page==kFontPage ? FontUnderMouse() : font);
            RefreshCharacterPreview(weapon);
            Lock();
            const bool timedOut = s.awaiting && Tick() - s.sentAt > 6000;
            const bool refreshWeaponList = s.weaponListRefreshPending && !s.awaiting && Tick() - s.sentAt >= 300;
            Unlock();
            if (timedOut) { CancelQuery(); Render(L"读取超时，请重新打开。"); }
            if (refreshWeaponList) { s.weaponListRefreshPending = false; Fetch(0); }
            return 0;
        }
    } __except(EXCEPTION_EXECUTE_HANDLER) {
        CancelQuery();
        CancelSummary();
        s.ready = false;
        return 0;
    }
    return WINAPI_AT(CallWindowProcW, 0x6ee5050)(s.previous, hwnd, message, wparam, lparam);
}
struct WindowSearch { HWND hwnd; unsigned count; DWORD pid; };
BOOL CALLBACK FindGameWindow(HWND hwnd, LPARAM context) {
    auto& found = *reinterpret_cast<WindowSearch*>(context);
    DWORD pid = 0;
    WINAPI_AT(GetWindowThreadProcessId, 0x6ee5054)(hwnd, &pid);
    RECT area;
    if (pid == found.pid && !WINAPI_AT(GetWindow, 0x6ee5040)(hwnd, GW_OWNER) &&
        WINAPI_AT(IsWindowVisible, 0x6ee5044)(hwnd) && WINAPI_AT(GetClientRect, 0x6ee5060)(hwnd, &area) &&
        area.right >= 400 && area.bottom >= 300) { found.hwnd = hwnd; ++found.count; }
    return TRUE;
}
bool Initialize() {
    auto& s = Data();
    if (s.previous) return UIThread();
    WindowSearch found = {nullptr, 0, WINAPI_AT(GetCurrentProcessId, 0x6ee5024)()};
    WINAPI_AT(EnumWindows, 0x6ee5068)(FindGameWindow, reinterpret_cast<LPARAM>(&found));
    DWORD thread = WINAPI_AT(GetCurrentThreadId, 0x6ee5028)();
    if (found.count != 1 || WINAPI_AT(GetWindowThreadProcessId, 0x6ee5054)(found.hwnd, nullptr) != thread) return false;
    UINT message = WINAPI_AT(RegisterWindowMessageW, 0x6ee5048)(L"DNF90.WeaponSkinDemo.20261121");
    if (!message) return false;
    s.previous = reinterpret_cast<WNDPROC>(WINAPI_AT(SetWindowLongW, 0x6ee5064)(
        found.hwnd, GWLP_WNDPROC, reinterpret_cast<LONG>(&WindowProc)));
    if (!s.previous) return false;
    s.hwnd = found.hwnd;
    s.thread = thread;
    s.message = message;
    return true;
}
} // namespace

extern "C" void __fastcall ForgeDrawPreview(void* window, void*, NativeRef control) {
    const auto* s = Data().forge;
    auto* anchor = s && s->active && window == s->main.window ? s->previewAnchor : nullptr;
    reinterpret_cast<void (__thiscall*)(void*, NativeRef)>(0x33618d0)(window,control);
    if (!anchor || control.value != anchor || !UIThread() || !ForgeOwn(s->main,true) ||
        !ForgeSelectionCurrent(*s) ||
        s->previewSource != s->selectedItem || s->previewRole != CurrentPreviewRole() ||
        !ForgePreviewMatches(s->previewModel,s->previewRole,s->previewItem,s->previewLook)) return;
    const RECT position = ForgeControlRect(anchor);
    DrawPreviewModel(s->previewModel,position.left,position.top);
}

extern "C" void __fastcall WarehouseDrawCharacter(void* window, void*, NativeRef control) {
    const auto& s = Data();
    const bool drawCharacter = s.window == window && s.characterPreviewAnchor && control.value == s.characterPreviewAnchor;
    const bool drawDamage = s.window == window && s.damageNumberAnchor && control.value == s.damageNumberAnchor;
    // 先完成原生控件绘制和引用释放，再在各自的仓库锚点绘制。
    reinterpret_cast<void (__thiscall*)(void*, NativeRef)>(0x109a730)(window,control);
    if ((drawCharacter || drawDamage) && UIThread() && Own(true)) {
        if (drawCharacter) RenderCharacterPreview();
        if (drawDamage) RenderDamageNumber();
    }
}

extern "C" __declspec(naked) void __cdecl _except_handler3() {
    __asm {
        mov eax,06ee5070h
        jmp dword ptr [eax]
    }
}
extern "C" __declspec(naked) bool __cdecl OriginalLoad(const wchar_t*, void*) {
    __asm {
        push ebp
        mov ebp,esp
        push -1
        push 0382ab95h
        ret
    }
}
extern "C" bool __cdecl WeaponLoad(const wchar_t* path, void* doc) {
    const unsigned char* xml = nullptr;
    if (Same(path,fontPath,ARRAYSIZE(fontPath))) xml=kWarehouseXml;
    else if (Same(path,kWarehouseEntryTitlePath,ARRAYSIZE(kWarehouseEntryTitlePath))) xml=kWarehouseEntryTitleXml;
    else if (Data().forge && Data().forge->creating && UIThread()) {
        if (Same(path,kForgeMainPath,ARRAYSIZE(kForgeMainPath))) xml=kForgeMainXml;
        else if (Same(path,kForgeConfirmPath,ARRAYSIZE(kForgeConfirmPath))) xml=kForgeConfirmXml;
    }
    if (!xml || !Readable(doc,0x80) || Value(doc) != 0x4627bb4)
        return OriginalLoad(path,doc);
    reinterpret_cast<const char* (__thiscall*)(void*, const unsigned char*, void*, int)>(0x3e43eb0)(doc,xml,nullptr,0);
    return static_cast<const unsigned char*>(doc)[0x48] == 0 &&
        reinterpret_cast<void* (__thiscall*)(void*)>(0x3e3f700)(doc) != nullptr;
}

// 原生202仍先维护字体向量和当前选择。六页仓库单独绘制自己的卡片。
extern "C" void __fastcall WarehouseRefreshLegacyFontRows(void* owner, void*, int first) {
    if (!WarehouseRoot(owner))
        reinterpret_cast<void (__thiscall*)(void*, int)>(0x113bd90)(owner, first);
}

extern "C" void __fastcall WarehouseLegacyFontHover(void* owner, void*) {
    if (!WarehouseRoot(owner))
        reinterpret_cast<void (__thiscall*)(void*)>(0x113bec0)(owner);
}

extern "C" __declspec(noinline) const wchar_t* __cdecl WarehouseCaption(void* owner, unsigned stringId) {
    return stringId == 0x4cd2 && WarehouseRoot(owner) ? L"皮肤仓库" : nullptr;
}

// 原生开窗取标题时 ESI 为窗口实例，原调用由调用者清理参数。
extern "C" __declspec(naked) void __cdecl WarehouseCaptionHook() {
    __asm {
        push ecx
        push edx
        push dword ptr [esp+0ch]
        push esi
        call WarehouseCaption
        add esp,8
        pop edx
        pop ecx
        test eax,eax
        jnz caption
        push 039a6620h
        ret
    caption:
        ret
    }
}

extern "C" int __fastcall FontEvent(void* self, void*, int sender, int event, int a, int b) {
    if (event == 13 || event == 24 || event == 18 || event == 19) {
        __try {
            auto* owner = static_cast<char*>(self)-0x1b0;
            if (auto* root = WarehouseRoot(owner)) {
                auto& s = Data();
                if (event==13) for (unsigned page = 0; page < ARRAYSIZE(kPages); ++page) {
                    auto* tab = s.window == owner && s.bound && s.root == root ? s.tabs[page] : Find(root,kPages[page].button);
                    if (SenderIs(tab,sender)) {
                        if (Initialize() && Bind(owner)) Post(PageMessageBase+page);
                        return 1;
                    }
                }
                if (event==13 && s.window==owner && s.bound && s.visible &&
                    SenderIs(s.damageNumberButton,sender)) {
                    if (s.page!=kFontPage) Post(PageMessageBase+kFontPage);
                    return 1;
                }
                if (s.window==owner && s.bound && s.page==kFontPage && Own(true)) {
                    if (event==24 && SenderIs(Find(root,kPages[kFontPage].pager),sender)) {
                        // 原生分页控件同步通知。等这次分页更新完成后再处理通知。
                        Post(RefreshFontPageMessage);
                        return 1;
                    }
                    if (event==13) for (unsigned row=0;row<kFontRows;++row) {
                        if (SenderIs(FontCardPart(row,L"btnApply"),sender)) { ApplyFontPageRow(row,false); return 1; }
                        if (SenderIs(FontCardPart(row,L"BtnCancel"),sender)) { ApplyFontPageRow(row,true); return 1; }
                    }
                }
                if ((event==18 || event==19) && SenderIs(Find(root,L"WS_HELP"),sender)) {
                    Show(Find(root,L"WS_HELP_CONTENT"), event==18);
                    return 1;
                }
                if (s.window==owner && s.bound && s.page==kSummaryPage && SummaryEvent(sender,event)) return 1;
                if (s.window == owner && s.bound && s.page == kWeaponPage && s.weapons) {
                    if (event==24 && SenderIs(Find(root,kPages[kWeaponPage].pager),sender))
                        return Post(RefreshWeaponPageMessage) ? 1 : 0;
                    if (event==13) {
                        if (SenderIs(s.searchButton,sender)) return Post(SearchMessage) ? 1 : 0;
                        if (SenderIs(s.initButton,sender)) return Post(InitMessage) ? 1 : 0;
                        for (const auto& row:s.weapons->rows) {
                            const bool clear=SenderIs(row.clear,sender);
                            if (!clear && !SenderIs(row.apply,sender)) continue;
                            if (row.look) QueueWeaponSelection(row.look->id,clear);
                            return 1;
                        }
                    }
                }
            }
        } __except(EXCEPTION_EXECUTE_HANDLER) {}
    }
    return reinterpret_cast<int (__thiscall*)(void*, int, int, int, int)>(0x113bfe0)(self,sender,event,a,b);
}

extern "C" __declspec(noinline) void __cdecl WeaponObserveBegin(unsigned command) {
    if (command==36) { Post(ForgeCloseMessage); return; }
    if (command != 1 && command != 3 && command != 4 && command != 8) return;
    auto& s = Data();
    Lock();
    if (++s.epoch == 0) ++s.epoch;
    s.awaiting = 0;
    s.queued = false;
    if (s.weapons) {
        s.weapons->summary.awaiting=0;
        s.weapons->summary.queued=false;
    }
    if (++s.registerEpoch == 0) ++s.registerEpoch;
    s.registerAwaiting = 0;
    s.registerQueued = false;
    Unlock();
    Post(ResetMessage);
}
extern "C" __declspec(naked) void WeaponBeginHook() {
    __asm {
        pushfd
        pushad
        mov eax,[esp+40]
        push eax
        call WeaponObserveBegin
        add esp,4
        popad
        popfd
        push 070007a0h
        ret
    }
}
extern "C" __declspec(noinline) bool __cdecl WeaponReceive(const unsigned char* header) {
    if (!header || header[1] != 4 || header[2] != 0x7f) return false;
    const unsigned size=*reinterpret_cast<const unsigned*>(0x57f9ad4);
    if (header[0] != 1 || header[15] != 0 || U32(header + 3) != 16+size ||
        (size != 28 && size != 28+kWeaponFlagsSize && size != kSummaryReplyBytes)) return true;
    const unsigned char* body = *reinterpret_cast<const unsigned char**>(0x57f9ad0);
    if (!Readable(body,size)) return true;
    if (size==kSummaryReplyBytes) {
        ReceiveSummary(body);
        return true;
    }
    if (size==28) {
        if (!ReceiveWeaponRegistration(body)) ForgeReceive(body);
        return true;
    }
    if (U32(body) != 0x32525357 || body[12] > 4 || body[13] > 2 ||
        body[14] || body[15] || U32(body+16)!=kWeaponCatalogChecksum) return true;
    Response r = {U32(body+4), U32(body+8), U32(body+20), U32(body+24), body[12], body[13], 0};
    constexpr unsigned lastBits = kWeaponCatalogCount%8;
    if (lastBits && body[size-1] >= (1u<<lastBits)) return true;
    if (r.selected) {
        const unsigned index = WeaponLookPosition(r.selected);
        if (index >= kWeaponCatalogCount || !(body[28+index/8] & (1u<<(index%8)))) return true;
    }
    for (unsigned i=0;i<kWeaponFlagsSize;++i) r.owned |= body[28+i]!=0;
    auto& s = Data();
    Lock();
    if (s.weapons && r.id && r.id == s.awaiting && r.epoch == s.epoch && r.action == s.expectedAction && !s.queued) {
        for (unsigned i=0;i<kWeaponFlagsSize;++i) s.weapons->received[i]=body[28+i];
        s.reply = r;
        s.queued = Post(WeaponReplyMessage);
    }
    Unlock();
    return true;
}
extern "C" __declspec(naked) void WeaponReceiveHook() {
    __asm {
        pushfd
        pushad
        push edi
        call WeaponReceive
        add esp,4
        test al,al
        jz previous
        popad
        popfd
        push 02523f50h
        ret
        previous:
        popad
        popfd
        push 07000cb0h
        ret
    }
}
// 主入口复用原生字体窗口 ID，主窗与页签共用一个窗口对象。
extern "C" void* __fastcall WarehouseMenuOpen(void* manager, void*, int id, int a, int b) {
    if (id == windowID && a == 0 && b == 0 &&
        manager == *reinterpret_cast<void**>(managerAddress) && Initialize()) return Open(manager);
    return reinterpret_cast<void* (__thiscall*)(void*, int, int, int)>(0x32bea50)(manager,id,a,b);
}

extern "C" void __fastcall WarehouseMenuClose(void* manager, void*, int id, int a, int b) {
    if (id == windowID && a == -1 && b == 0 && manager == Data().manager && UIThread() && Data().visible && Own(true)) {
        Close(true);
        return;
    }
    reinterpret_cast<void (__thiscall*)(void*, int, int, int)>(0x32b94a0)(manager,id,a,b);
}

extern "C" bool __fastcall WarehouseMenuIsOpen(void* manager, void*, int id) {
    return reinterpret_cast<bool (__thiscall*)(void*, int)>(0x32b55e0)(manager,id);
}

extern "C" __declspec(noinline) bool __stdcall ForgeOpenFromNpc(void* source,void* manager,int id,const wchar_t* url,int flag) {
    __try {
        if (id!=911 || flag!=37 || manager!=*reinterpret_cast<void**>(managerAddress) ||
            !Readable(source,4) || Value(source)!=1 || !Same(url,kForgeURI,ARRAYSIZE(kForgeURI))) return false;
        if (Initialize()) Post(ForgeOpenMessage);
        return true;
    } __except(EXCEPTION_EXECUTE_HANDLER) { return false; }
}

extern "C" __declspec(naked) void ForgeNpcHook() {
    __asm {
        pushfd
        pushad
        push dword ptr [esp+48]
        push dword ptr [esp+48]
        push dword ptr [esp+48]
        push ecx
        push esi
        call ForgeOpenFromNpc
        test al,al
        jz previous
        popad
        popfd
        xor eax,eax
        ret 12
        previous:
        popad
        popfd
        push 070009c0h
        ret
    }
}

extern "C" void __cdecl ForgeNpcCaption(void* out,void* owner,unsigned command) {
    reinterpret_cast<void (__cdecl*)(void*,void*,unsigned)>(0x7000ff0)(out,owner,command);
    if (command!=0x41b) return;
    __try {
        const auto ptr=reinterpret_cast<uintptr_t>(out);
        const auto low=__readfsdword(8), high=__readfsdword(4);
        if (ptr<low || ptr>high || high-ptr<32 || !Readable(out,32) || !Readable(owner,0x18)) return;
        const unsigned length=Value(out,0x14), capacity=Value(out,0x18);
        if (length>255 || capacity<length || capacity<7 || capacity>0x100000) return;
        const auto* data=capacity>=8 ? reinterpret_cast<const wchar_t*>(Value(out,4)) :
                                     reinterpret_cast<const wchar_t*>(static_cast<unsigned char*>(out)+4);
        if (!Readable(data,(length+1)*2) || data[length]) return;
        auto* actor=reinterpret_cast<void*>(Value(owner,0x14));
        if (!Readable(actor,4) || !Readable(reinterpret_cast<void*>(Value(actor)),0x40)) return;
        const auto isActor=reinterpret_cast<bool (__thiscall*)(void*,unsigned)>(Value(reinterpret_cast<void*>(Value(actor)),0x3c));
        if (!isActor || !isActor(actor,0x22)) return;
        auto* npc=reinterpret_cast<void* (__cdecl*)(void*)>(0x1e36040)(actor);
        if (!Readable(npc,4) || !Readable(reinterpret_cast<void*>(Value(npc)),0x348)) return;
        const auto get=reinterpret_cast<void* (__thiscall*)(void*)>(Value(reinterpret_cast<void*>(Value(npc)),0x344));
        auto* record=get ? get(npc) : nullptr;
        if (!Readable(record,4) || Value(record)!=1) return;
        constexpr wchar_t label[]=L"武器幻化";
        reinterpret_cast<void* (__thiscall*)(void*,const wchar_t*,unsigned)>(0x45fda0)(out,label,ARRAYSIZE(label)-1);
    } __except(EXCEPTION_EXECUTE_HANDLER) {}
}

extern "C" void __cdecl PayloadEntry() {}

extern "C" __declspec(noinline) bool __cdecl ForgeBackpackPick(unsigned slot, void* item) {
    __try { return UIThread() && ForgePickWeapon(slot, item, false); }
    __except(EXCEPTION_EXECUTE_HANDLER) { return false; }
}

extern "C" __declspec(naked) void ForgeBackpackPickHook() {
    __asm {
        pushfd
        pushad
        push esi
        push ebx
        call ForgeBackpackPick
        add esp, 8
        test al, al
        jz native_use
        popad
        popfd
        mov dword ptr [esp], 0249DFDEh
        ret
    native_use:
        popad
        popfd
        mov edx, [ebp-18h]
        lea eax, [edx-5]
        ret
    }
}

extern "C" __declspec(noinline) void __cdecl ForgeDrawBackpackItem(
    void* draw, void* item, int x, int y, const void* grid, unsigned index) {
    auto* s = Data().forge;
    using NativeDraw = void (__thiscall*)(void*, int, int, bool, bool, bool);
    if (!s || !s->active || !s->picking || !UIThread() ||
        Value(grid, 8) != reinterpret_cast<uintptr_t>(ForgeBag())) {
        reinterpret_cast<NativeDraw>(draw)(item, x, y, false, true, true);
        return;
    }
    const auto slot = Value(grid, 0xc) + index;
    auto* previous = s->dimmingItem;
    s->dimmingItem = ForgeSlotAllowed(*s, slot, item) ? nullptr : item;
    __try {
        reinterpret_cast<NativeDraw>(draw)(item, x, y, false, true, true);
    } __finally {
        s->dimmingItem = previous;
    }
}

extern "C" __declspec(naked) void ForgeBackpackDrawHook() {
    __asm {
        inc edi
        inc ebx
        push dword ptr [ebp+8]
        push dword ptr [ebp-1ch]
        push edi
        push ebx
        push esi
        push edx
        call ForgeDrawBackpackItem
        add esp, 24
        ret
    }
}

extern "C" __declspec(noinline) bool __cdecl ForgeDimmingItem(const void* item) {
    const auto* s = Data().forge;
    return s && s->dimmingItem && s->dimmingItem == item && UIThread();
}

extern "C" __declspec(naked) void ForgeBackpackShadeHook() {
    __asm {
        cmp byte ptr [ebp-0dh], 0
        jne shade
        pushad
        push esi
        call ForgeDimmingItem
        add esp, 4
        test al, al
        jnz shade_saved
        popad
        mov dword ptr [esp], 01D4C6D9h
        ret
    shade_saved:
        popad
    shade:
        mov dword ptr [esp], 01D4C63Fh
        ret
    }
}

extern "C" __declspec(noinline) bool __cdecl WeaponRegisterRightClick(void* bag, void* item, const void* selection) {
    __try {
        if (bag != RegisterMainBag() || !item) return false;
        // 使用原生背包刚校验的选中槽位，再核对它仍指向当前道具。
        const uint32_t slot = Value(selection, 4);
        if (slot > 0x7fff || RegisterItemAt(bag, slot) != item) return false;
        auto recipe = ForgeRecipe::Invalid;
        const auto* look = WeaponProductLook(item,recipe);
        return look && OpenWeaponRegistration(bag,item,slot,recipe,*look);
    } __except(EXCEPTION_EXECUTE_HANDLER) { return false; }
}

extern "C" __declspec(naked) void WeaponRegisterRightClickHook() {
    __asm {
        pushfd
        pushad
        push dword ptr [ebp+8]
        push esi
        push edi
        call WeaponRegisterRightClick
        add esp, 12
        test al, al
        jz native_use
        popad
        popfd
        mov al, 1
        mov dword ptr [esp], 02605A2Ch
        ret
    native_use:
        popad
        popfd
        test ebx, ebx
        jnz normal_action
        mov dword ptr [esp], 02603A84h
    normal_action:
        ret
    }
}
