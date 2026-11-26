#pragma once
#include "林纳斯布局.h"

constexpr wchar_t kForgeURI[] = L"about:blank#dnf90-linus-weapon-skin";
constexpr wchar_t kForgeMainPath[] = L"UI/LocalWeaponSkin/Forge.xui";
constexpr wchar_t kForgeConfirmPath[] = L"UI/LocalWeaponSkin/Confirm.xui";
constexpr wchar_t kForgeConfirmText[] = L"立即幻化";
constexpr wchar_t kForgePreviewAnchor[] = L"forgeWeaponPreview";
constexpr UINT_PTR kForgeTimer = 0x57530002;
constexpr int kForgeBagWindow = 0xdd;
constexpr unsigned short kForgeNoSlot = 0xffff;
constexpr unsigned kForgeLastWeaponSlot = 64;
constexpr unsigned kForgeWeaponGearSlot = 12;
constexpr uintptr_t kForgeRadioVtable = 0x4d4f8c4;
constexpr unsigned kForgeFirstMaterialSlot = 121;
constexpr unsigned kForgeLastMaterialSlot = 176;
// 95 原版 ANI 的帧时长总和，主轨和各子轨一致。
constexpr DWORD kForgeHammerDuration = 1500;
constexpr DWORD kForgeCompleteDuration = 2000;
enum : unsigned char { kForgeConfirm = 1, kForgeCheckBag = 3, kForgePickSlot = 4 };
enum class ForgePhase : unsigned char { Choose, Hammer, Complete };
enum class ForgeRecipe : unsigned char { Magic, Secret, Invalid };

struct ForgeMoldControls {
    unsigned id;
    const wchar_t* item;
    const wchar_t* name;
    const wchar_t* checkbox;
};
constexpr ForgeMoldControls kForgeMoldControls[] = {
    {490704411,L"itemMaterial2",L"textMaterialName2",L"material2"},
    {490704427,L"itemMaterial",L"textMaterialName",L"material1"},
};

unsigned ForgeRecipeIndex(ForgeRecipe recipe) { return static_cast<unsigned>(recipe); }

struct ForgeView {
    int id;
    void* window;
    void* root;
};
struct ForgeReply {
    uint32_t id, epoch;
    uint32_t tokenLow, tokenHigh;
    unsigned short slot, moldSlot;
    unsigned char status, action;
};
struct ForgeState {
    void* manager;
    ForgeView main, confirm;
    ForgeView* creating;
    uint32_t expected, epoch;
    DWORD sentAt, animationStarted, moldCheckedAt;
    unsigned char action;
    ForgePhase phase;
    ForgeRecipe recipe, requestedRecipe, selectedRecipe;
    bool active, queued, picking, hammerReply, refreshMaterials;
    unsigned char hammerStatus;
    unsigned short requestedSlot;
    uint32_t eligible[2];
    void* materials[kForgeLastWeaponSlot];
    void* requestedItem;
    void* selectedItem;
    void* dimmingItem;
    const void* shownMolds[ARRAYSIZE(kForgeMoldControls)];
    void* previewSource;
    void* previewRole;
    void* previewModel;
    void* previewItem;
    ForgeReply reply, selected;
    void* previewAnchor;
    const WeaponLook* previewLook;
    ForgeRecipe materialsRecipe;
    bool materialsReady;
};

bool Initialize();
void RegisterNotice(const wchar_t* text);

bool ForgeViewInVector(const ForgeView& view, unsigned offset) {
    auto* s = Data().forge;
    if (!s || view.id <= 0 || view.id >= 1240 || !Readable(s->manager,0xc640)) return false;
    const auto* vec = static_cast<const unsigned char*>(s->manager)+(view.id*5+offset)*4;
    const uintptr_t begin = Value(vec,4), end = Value(vec,8);
    if (!view.window || end <= begin || (end-begin)%4 || end-begin > 64 ||
        !Readable(reinterpret_cast<const void*>(begin),end-begin)) return false;
    for (uintptr_t at = begin; at < end; at += 4)
        if (*reinterpret_cast<void**>(at) == view.window) return true;
    return false;
}

bool ForgeOwn(const ForgeView& view, bool active) {
    return Data().forge && *reinterpret_cast<void**>(managerAddress) == Data().forge->manager &&
        (ForgeViewInVector(view,0x41) || (!active && ForgeViewInVector(view,0x1879)));
}

bool ForgeSlotFree(int id) {
    auto* s = Data().forge;
    if (!s || !Readable(s->manager,0xc640) || id==s->main.id || id==s->confirm.id) return false;
    for (const unsigned offset : {0x41u,0x1879u}) {
        const auto* vec = static_cast<const unsigned char*>(s->manager)+(id*5+offset)*4;
        if (Value(vec,4)!=Value(vec,8)) return false;
    }
    return Value(reinterpret_cast<void*>(0x3373f58),id*4)==0x3373f41;
}

__declspec(noinline) void* ForgeConstruct(void* storage,void* owner,int id,const wchar_t* path) {
    void* result;
    __asm {
        sub esp,20h
        mov ecx,esp
        push path
        mov eax,0045fea0h
        call eax
        push id
        push owner
        mov ecx,storage
        mov eax,029bb9c0h
        call eax
        mov result,eax
    }
    return result;
}

void* __cdecl ForgeFactory(int id,void* owner) {
    auto* s = Data().forge;
    if (!s || !s->creating || id!=s->creating->id || owner!=s->manager || !UIThread())
        return reinterpret_cast<void* (__cdecl*)(int,void*)>(0x3365ba0)(id,owner);
    auto* storage = reinterpret_cast<void* (__cdecl*)(unsigned)>(0x37f2170)(0x1dc);
    if (!storage) return nullptr;
    const auto* path = s->creating==&s->main ? kForgeMainPath : kForgeConfirmPath;
    s->creating->window = ForgeConstruct(storage,owner,id,path);
    return s->creating->window;
}

bool ForgeSwapFactory(bool install) {
    constexpr uintptr_t va = 0x32becc0;
    constexpr unsigned char original[] = {0xe8,0xdb,0x6e,0x0a,0x00};
    unsigned char replacement[5] = {0xe8};
    Put32(replacement+1,reinterpret_cast<uintptr_t>(&ForgeFactory)-va-5);
    auto* dest = reinterpret_cast<unsigned char*>(va);
    const auto* expected = install ? original : replacement;
    const auto* next = install ? replacement : original;
    for (unsigned i=0;i<5;++i) if (dest[i]!=expected[i]) return false;
    DWORD protect, ignored;
    auto protectPages = WINAPI_AT(VirtualProtect,0x6fa402c);
    if (!protectPages(dest,5,PAGE_EXECUTE_READWRITE,&protect)) return false;
    for (unsigned i=0;i<5;++i) dest[i]=next[i];
    const BOOL restored = protectPages(dest,5,protect,&ignored);
    const BOOL flushed = WINAPI_AT(FlushInstructionCache,0x6fa4024)(WINAPI_AT(GetCurrentProcess,0x6fa4018)(),dest,5);
    if (!restored || !flushed) {
        if (install && protectPages(dest,5,PAGE_EXECUTE_READWRITE,&ignored)) {
            for (unsigned i=0;i<5;++i) dest[i]=original[i];
            protectPages(dest,5,protect,&ignored);
            WINAPI_AT(FlushInstructionCache,0x6fa4024)(WINAPI_AT(GetCurrentProcess,0x6fa4018)(),dest,5);
        }
        return false;
    }
    return true;
}

void ForgeCloseView(ForgeView& view) {
    if (ForgeOwn(view,true))
        reinterpret_cast<void (__thiscall*)(void*,int,int,int)>(0x32b94a0)(Data().forge->manager,view.id,-1,0);
}

void ForgeClearPreview(ForgeState& s) {
    auto* model = s.previewModel;
    auto* item = s.previewItem;
    s.previewModel = s.previewItem = s.previewSource = s.previewRole = nullptr;
    s.previewLook = nullptr;
    ReleasePreview(model,item);
}

void ForgeClose() {
    auto* s = Data().forge;
    if (!s || !s->active) return;
    s->active = false;
    s->picking = false;
    s->phase = ForgePhase::Choose;
    WINAPI_AT(KillTimer,0x6ee505c)(Data().hwnd,kForgeTimer);
    Lock();
    s->expected = 0;
    s->queued = false;
    Unlock();
    s->selected = {};
    ForgeClearPreview(*s);
    ForgeCloseView(s->confirm);
    ForgeCloseView(s->main);
}

void ForgeLabel(const ForgeView& view,const wchar_t* id,const wchar_t* text) {
    auto* control = Find(view.root,id);
    if (Readable(control,0x410) && Value(control)==0x4d4fa4c)
        reinterpret_cast<void (__thiscall*)(void*,const wchar_t*)>(0x3835940)(control,text);
}

void ForgeEnable(const ForgeView& view,const wchar_t* id,bool value) {
    auto* control = Find(view.root,id);
    if (Readable(control,4) && Readable(reinterpret_cast<void*>(Value(control)),16))
        reinterpret_cast<void (__thiscall*)(void*,bool)>(Value(reinterpret_cast<void*>(Value(control)),12))(control,value);
}

void* ForgeBag() { return *reinterpret_cast<void**>(0x56a379c); }

void* ForgeBagItem(unsigned slot) {
    auto* bag = ForgeBag();
    if (!Readable(bag, 0x60)) return nullptr;
    const auto begin = Value(bag, 0x58), end = Value(bag, 0x5c);
    if (end < begin || slot >= (end-begin)/sizeof(void*)) return nullptr;
    const auto* entry = reinterpret_cast<void* const*>(begin) + slot;
    return Readable(entry, sizeof(*entry)) ? *entry : nullptr;
}

const void* ForgeItemRecord(void* item) {
    if (!Readable(item, 4)) return nullptr;
    const auto* table = reinterpret_cast<const void*>(Value(item));
    if (!Readable(table, 0x2c)) return nullptr;
    return reinterpret_cast<const void* (__thiscall*)(void*)>(Value(table, 0x28))(item);
}

unsigned ForgeItemID(const void* record) {
    if (!Readable(record,0x1c)) return 0;
    return reinterpret_cast<unsigned (__thiscall*)(const void*)>(0x994e30)(
        static_cast<const char*>(record)+0x14);
}

const wchar_t* ForgeItemName(const void* record) {
    if (!Readable(record, 0xb0)) return L"";
    const auto* name = Value(record, 0xac) < 8
        ? reinterpret_cast<const wchar_t*>(static_cast<const char*>(record) + 0x98)
        : reinterpret_cast<const wchar_t*>(Value(record, 0x98));
    const auto length = Value(record, 0xa8);
    return length < 256 && Readable(name, (length + 1) * sizeof(wchar_t)) && !name[length] ? name : L"";
}

void ForgeItemControl(const ForgeView& view, const wchar_t* id, const void* record) {
    auto* ctrl = Find(view.root, id);
    if (Readable(ctrl, 0x518) && Value(ctrl) == 0x48da95c)
        reinterpret_cast<void (__thiscall*)(void*, uint32_t)>(0x1d53410)(
            ctrl, Readable(record, 0x1c) ? Value(record, 0x18) : UINT32_MAX);
}

void ForgeFindMolds(const void* (&molds)[ARRAYSIZE(kForgeMoldControls)], unsigned first, unsigned last) {
    for (unsigned slot = first; slot <= last && (!molds[0] || !molds[1]); ++slot) {
        const auto* record = ForgeItemRecord(ForgeBagItem(slot));
        if (!Readable(record,0x1c)) continue;
        for (unsigned n=0; n<ARRAYSIZE(kForgeMoldControls); ++n) {
            const auto id = kForgeMoldControls[n].id;
            if (!molds[n] && Value(record,0x18)==id && ForgeItemID(record)==id) molds[n] = record;
        }
    }
}

bool ForgeRefreshMolds(ForgeState& s, bool force) {
    s.moldCheckedAt = Tick();
    const void* molds[ARRAYSIZE(kForgeMoldControls)] = {};
    if (s.phase != ForgePhase::Complete) {
        ForgeFindMolds(molds,kForgeFirstMaterialSlot,kForgeLastMaterialSlot);
        ForgeFindMolds(molds,3,8);
    }
    bool changed = false;
    for (unsigned n=0; n<ARRAYSIZE(kForgeMoldControls); ++n) {
        if (molds[n] != s.shownMolds[n]) changed = true;
        else if (!force) continue;
        s.shownMolds[n] = molds[n];
        const auto& controls = kForgeMoldControls[n];
        const bool held = molds[n] != nullptr;
        ForgeItemControl(s.main,controls.item,molds[n]);
        Show(Find(s.main.root,controls.item),held);
        Show(Find(s.main.root,controls.name),held);
        Show(Find(s.main.root,controls.checkbox),held);
        ForgeEnable(s.main,controls.checkbox,held && s.phase == ForgePhase::Choose && !s.expected);
        if (held) ForgeLabel(s.main,controls.name,ForgeItemName(molds[n]));
    }
    return changed;
}

void* ForgeMaterialRadio(const ForgeView& view) {
    auto* radio = Find(view.root,L"selectMaterial");
    return Readable(radio,0x3c4) && Value(radio)==kForgeRadioVtable && Value(radio,0x94)==2 ? radio : nullptr;
}

void ForgeSetRadio(void* radio, unsigned index) {
    if (radio && Value(radio,0x3bc) != index)
        reinterpret_cast<void (__thiscall*)(void*,unsigned)>(0x3834e90)(radio,index);
}

bool ForgeSyncRecipe(ForgeState& s) {
    if (s.phase != ForgePhase::Choose) return false;
    auto* radio = ForgeMaterialRadio(s.main);
    if (!radio) return false;
    auto recipe = Value(radio,0x3bc)==1 ? ForgeRecipe::Magic : ForgeRecipe::Secret;
    const auto other = recipe==ForgeRecipe::Magic ? ForgeRecipe::Secret : ForgeRecipe::Magic;
    if (!s.shownMolds[ForgeRecipeIndex(recipe)] && s.shownMolds[ForgeRecipeIndex(other)]) recipe = other;
    // 95 布局的顺序是左秘传、右神奇，协议中的配方编号顺序相反。
    ForgeSetRadio(radio,recipe == ForgeRecipe::Secret ? 0u : 1u);
    if (recipe == s.recipe) return false;
    s.recipe = recipe;
    ForgeCloseView(s.confirm);
    return true;
}

void ForgeAnimation(const ForgeView& view, const wchar_t* id, bool visible, bool restart = false) {
    auto* ctrl = Find(view.root, id);
    if (!Readable(ctrl, 0x428) || Value(ctrl) != 0x4d18184) return;
    ForgeEnable(view, id, visible);
    Show(ctrl, visible);
    if (restart) reinterpret_cast<void (__thiscall*)(void*)>(0x35d4430)(ctrl);
}

bool ForgeSelectionCurrent(const ForgeState& s) {
    return s.selectedItem && (s.selected.tokenLow || s.selected.tokenHigh) &&
        ForgeBagItem(s.selected.slot) == s.selectedItem;
}

bool ForgeSelectedMoldCurrent(const ForgeState& s) {
    if (s.selectedRecipe != s.recipe) return false;
    const auto* mold = ForgeItemRecord(ForgeBagItem(s.selected.moldSlot));
    return Readable(mold,0x1c) && Value(mold,0x18) == kForgeMoldControls[ForgeRecipeIndex(s.recipe)].id;
}

bool ForgeCanCraft(const ForgeState& s) {
    return ForgeSelectionCurrent(s) && ForgeSelectedMoldCurrent(s);
}

const WeaponLook* ForgeSourceLook(unsigned id) {
    for (unsigned i=0; i<3; ++i)
        if (kWeaponCatalog[i].source == id) return &kWeaponCatalog[i];
    unsigned first = 3, last = kWeaponCatalogCount;
    while (first < last) {
        const unsigned middle = first + (last-first)/2;
        if (kWeaponCatalog[middle].source < id) first = middle+1;
        else last = middle;
    }
    return first < kWeaponCatalogCount && kWeaponCatalog[first].source == id ? &kWeaponCatalog[first] : nullptr;
}

bool ForgePreviewMatches(void* model, void* role, void* weapon, const WeaponLook* look) {
    if (!model || !role || !weapon) return false;
    const unsigned job = Value(model,kPreviewJob);
    if (job >= kJobCount) return false;
    if (job == PreviewRoleJob(role))
        return PreviewGearMatches(model,role,weapon,kForgeWeaponGearSlot,true);
    // 原生装备预览会为异职业武器建立适配职业的基础模型。
    const unsigned equipment = kPreviewGear + job*kGearSlots*4;
    return look && (look->jobs & (1u<<job)) &&
        Value(model,equipment+kForgeWeaponGearSlot*4) == reinterpret_cast<uintptr_t>(weapon) &&
        !Value(model,equipment+kWeaponAvatarSlot*4);
}

void ForgeRefreshPreview(ForgeState& s, void* source, const void* record) {
    auto* role = source ? CurrentPreviewRole() : nullptr;
    if (source == s.previewSource && role == s.previewRole &&
        (!s.previewModel || ForgePreviewMatches(s.previewModel,role,s.previewItem,s.previewLook))) return;
    ForgeClearPreview(s);
    const auto id = ForgeItemID(record);
    if (!Readable(role,4) || !id) return;
    const auto* look = ForgeSourceLook(id);
    s.previewRole = role;
    s.previewSource = source;
    auto* item = CreatePreviewEquipment(id);
    if (!item) return;
    s.previewItem = item;
    if (!Readable(item,0x1000) || Value(item) != kEquipmentTable) return;
    const auto* table = reinterpret_cast<const void*>(Value(item));
    const auto* slot = reinterpret_cast<const unsigned* (__thiscall*)(void*)>(Value(table,0x154))(item);
    if (!Readable(slot,4) || *slot != kForgeWeaponGearSlot) return;
    auto* model = CreatePreviewModel();
    if (!model) return;
    using Equip = void (__thiscall*)(void*, unsigned, void*, int);
    const auto equip = reinterpret_cast<Equip>(0x35274f0);
    // 只在独立模型里清空覆盖普通武器的装扮，再装入选中的源武器。
    equip(model,kWeaponAvatarSlot,nullptr,0);
    equip(model,kForgeWeaponGearSlot,item,0);
    if (!ForgePreviewMatches(model,role,item,look)) {
        s.previewItem = nullptr;
        ReleasePreview(model,item);
        return;
    }
    s.previewModel = model;
    s.previewLook = look;
}

bool ForgeBagOpen(const ForgeState& s) {
    return ForgeOwn(s.main, true) &&
        reinterpret_cast<bool (__thiscall*)(void*, int)>(0x32b54b0)(s.manager, kForgeBagWindow);
}

RECT ForgeControlRect(void* control) {
    if (!Readable(control, 0x84)) return {};
    const auto x = reinterpret_cast<int (__thiscall*)(void*)>(0x209bbe0)(control);
    const auto y = reinterpret_cast<int (__thiscall*)(void*)>(0x37e9f40)(control);
    return {x, y, x + static_cast<int>(Value(control, 0x6c)),
                  y + static_cast<int>(Value(control, 0x70))};
}

RECT ForgeWindowBounds(void* window, RECT content) {
    auto* title = Readable(window,0x4c) ? reinterpret_cast<void*>(Value(window,0x48)) : nullptr;
    const RECT caption = ForgeControlRect(title);
    if (caption.right > caption.left && caption.bottom > caption.top) {
        content.left = min(content.left,caption.left);
        content.top = min(content.top,caption.top);
        content.right = max(content.right,caption.right);
        content.bottom = max(content.bottom,caption.bottom);
    }
    return content;
}

void ForgeConfigureMain(const ForgeView& view) {
    // 装饰仍参与绘制和动画，但把标题处的鼠标命中留给原生标题控件。
    using IgnoreMouse = void (__thiscall*)(void*, bool);
    const auto ignoreMouse = reinterpret_cast<IgnoreMouse>(0x3826fa0);
    for (const auto* id : {L"imgBack", L"imgBarrel", L"aniIdle", L"aniTitleOpen",
                           L"aniTitleCloseMain", L"aniComplete", kForgePreviewAnchor}) {
        auto* art = Find(view.root, id);
        if (Readable(art, 0x139)) ignoreMouse(art, true);
    }

    auto* title = reinterpret_cast<void*>(Value(view.window, 0x48));
    if (Readable(title, 0x139) && Readable(reinterpret_cast<void*>(Value(view.window, 4)), 0x3c)) {
        // 与 90 原生标题一样，由游戏的拖动处理移动父窗体。
        reinterpret_cast<void (__thiscall*)(void*, bool)>(0x335a970)(view.window, true);
        reinterpret_cast<void (__thiscall*)(void*, bool)>(0x3826f40)(title, true);
        ignoreMouse(title, false);
    }

    auto* barrel = Find(view.root, L"imgBarrel");
    if (!Readable(barrel, 0x378)) return;
    const RECT window = ForgeControlRect(view.root);
    const RECT image = ForgeControlRect(barrel);
    if (image.right <= image.left || image.bottom <= image.top ||
        window.right <= window.left || window.bottom <= window.top) return;
    // 原生裁切矩形使用控件局部坐标，窗口移动时会跟随木桶一起移动。
    const RECT clip = {window.left - image.left, window.top - image.top,
                       window.right - image.left, window.bottom - image.top};
    reinterpret_cast<void (__thiscall*)(void*, const RECT*)>(0x3827240)(barrel, &clip);
}

void ForgePlaceBag(const ForgeView& forge, void* bagWindow, void* bagRoot) {
    const RECT forgeContent = ForgeControlRect(forge.root);
    const RECT bagContent = ForgeControlRect(bagRoot);
    if (forgeContent.right <= forgeContent.left || bagContent.right <= bagContent.left) return;
    const RECT forgeFrame = ForgeWindowBounds(forge.window,forgeContent);
    const RECT bagFrame = ForgeWindowBounds(bagWindow,bagContent);
    const int forgeWidth = forgeFrame.right - forgeFrame.left;
    const int bagWidth = bagFrame.right - bagFrame.left;
    const int screenWidth = *reinterpret_cast<const int*>(0x55b0318);
    if (forgeWidth <= 0 || bagWidth <= 0 || screenWidth < bagWidth || screenWidth < forgeWidth) return;

    using MoveWindow = void (__thiscall*)(void*, int, int, bool);
    const auto moveWindow = reinterpret_cast<MoveWindow>(0x335b140);
    // 标题栏与内容区的原点不同。排列外框后把坐标换回原生移动函数所用的内容区原点。
    const int spareWidth = screenWidth - forgeWidth - bagWidth;
    const int gap = min(4,max(0,spareWidth));
    const int rightMargin = max(0, min(36, spareWidth - gap));
    const int lastBagLeft = screenWidth - bagWidth - rightMargin;
    const int forgeLeft = max(0,min(forgeFrame.left,lastBagLeft-forgeWidth-gap));
    const int forgeX = forgeLeft + forgeContent.left - forgeFrame.left;
    if (forgeX != forgeContent.left) moveWindow(forge.window,forgeX,forgeContent.top,true);
    const int bagLeft = min(lastBagLeft,forgeLeft+forgeWidth+gap);
    const int bagX = bagLeft + bagContent.left - bagFrame.left;
    moveWindow(bagWindow,bagX,bagContent.top,true);
}

bool ForgeSlotAllowed(const ForgeState& s, unsigned slot, void* item) {
    if (!s.materialsReady || s.materialsRecipe != s.recipe || !item ||
        slot < 1 || slot > kForgeLastWeaponSlot) return false;
    const auto index = slot - 1;
    return (s.eligible[index / 32] & (1u << (index % 32))) && s.materials[index] == item;
}

void ForgeRender() {
    auto& s = *Data().forge;
    if (!ForgeOwn(s.main,true)) return;
    const bool selected = ForgeSelectionCurrent(s);
    const bool complete = s.phase == ForgePhase::Complete;
    const bool choose = s.phase == ForgePhase::Choose;
    const auto* weapon = selected ? ForgeItemRecord(s.selectedItem) : nullptr;
    ForgeItemControl(s.main, L"srcItem", weapon);
    ForgeRefreshPreview(s,selected ? s.selectedItem : nullptr,weapon);
    ForgeRefreshMolds(s,true);
    ForgeSyncRecipe(s);
    Show(Find(s.main.root,L"btnOk"), !complete);
    Show(Find(s.main.root,L"btnCancel"), !complete);
    Show(Find(s.main.root,L"btnComplete"), complete);
    ForgeEnable(s.main,L"btnComplete", complete && Tick()-s.animationStarted >= kForgeCompleteDuration);
    ForgeEnable(s.main,L"btnOk",choose && !s.picking && !s.expected && ForgeCanCraft(s));
    ForgeEnable(s.main,L"btnCancel",choose);
    ForgeEnable(s.main,L"btnSelectWeapon",choose && !s.expected);
}

bool ForgeDispatch(unsigned char action, unsigned short slot) {
    auto& w = Data();
    auto* s = w.forge;
    void* connection = nullptr;
    if (!s || !s->active || !UIThread() || !Connected(connection) ||
        Value(connection,0x2bcc34) || Value(connection,0x2bcc2c)) return false;
    unsigned char body[24] = {'W','S','F','2'};
    Lock();
    if (s->expected) { Unlock(); return false; }
    if (++w.serial == 0) ++w.serial;
    if (w.epoch == 0) w.epoch = 1;
    s->expected = w.serial;
    s->epoch = w.epoch;
    s->action = action;
    s->requestedRecipe = s->recipe;
    s->sentAt = Tick();
    Put32(body+4,s->expected);
    Put32(body+8,s->epoch);
    Unlock();
    body[12]=action;
    body[13]=static_cast<unsigned char>(s->requestedRecipe);
    body[14]=static_cast<unsigned char>(slot);
    body[15]=static_cast<unsigned char>(slot>>8);
    if (action == kForgeConfirm) {
        Put32(body+16,s->selected.tokenLow);
        Put32(body+20,s->selected.tokenHigh);
    }
    if (reinterpret_cast<LONG (__thiscall*)(void*,unsigned)>(0x374f280)(connection,opcode)>=0) {
        const LONG app = reinterpret_cast<LONG (__thiscall*)(void*,const void*,unsigned)>(0x374eeb0)(connection,body,sizeof(body));
        const LONG finish = reinterpret_cast<LONG (__cdecl*)()>(0x3750ac0)();
        if (app>=0 && finish>=0) return true;
    }
    Lock();
    s->expected=0;
    s->queued=false;
    Unlock();
    return false;
}

bool ForgeOpenView(ForgeView& view);

void ForgeReadMaterials(ForgeState& s) {
    s.materialsRecipe = s.recipe;
    s.materialsReady = false;
    s.refreshMaterials = false;
    s.eligible[0] = s.eligible[1] = 0;
    for (unsigned slot = 1; slot <= kForgeLastWeaponSlot; ++slot)
        s.materials[slot-1] = ForgeBagItem(slot);
}

bool ForgeMaterialsMoved(const ForgeState& s) {
    for (unsigned slot = 1; slot <= kForgeLastWeaponSlot; ++slot)
        if (s.materials[slot-1] != ForgeBagItem(slot)) return true;
    return false;
}

void ForgeSelectWeapon() {
    auto* s = Data().forge;
    if (!s || !s->active || !ForgeOwn(s->main, true) || s->expected || s->phase != ForgePhase::Choose) return;
    // 原生打开入口对已经打开的装备栏也会执行前置。
    reinterpret_cast<void* (__thiscall*)(void*, int, int, int)>(0x32bea50)(s->manager, kForgeBagWindow, 0, 0);
    if (!ForgeBagOpen(*s)) return;
    auto* bagWindow = reinterpret_cast<void* (__thiscall*)(void*, int)>(0x32ba120)(s->manager, kForgeBagWindow);
    if (!Readable(bagWindow, 0x1fc) || Value(bagWindow) != 0x49bd76c) return;
    if (Value(bagWindow, 0x1f8) != 0)
        reinterpret_cast<void (__thiscall*)(void*, int, int)>(0x24a7cb0)(bagWindow, 0, -1);
    if (!ForgeOwn(s->main, true) || !ForgeBagOpen(*s)) return;
    ForgeSyncRecipe(*s);
    ForgePlaceBag(s->main, bagWindow, reinterpret_cast<void*>(Value(bagWindow, 0x30)));
    s->picking = true;
    s->selected = {};
    s->selectedItem = nullptr;
    if (!s->materialsReady || s->materialsRecipe != s->recipe ||
        s->refreshMaterials || ForgeMaterialsMoved(*s)) {
        ForgeReadMaterials(*s);
        if (!ForgeDispatch(kForgeCheckBag, kForgeNoSlot)) s->picking = false;
    }
    ForgeRender();
}

bool ForgeRequestWeapon(ForgeState& s, unsigned short slot, void* item) {
    s.requestedSlot = slot;
    s.requestedItem = item;
    if (ForgeDispatch(kForgePickSlot,slot)) return true;
    s.requestedItem = nullptr;
    return false;
}

bool ForgePickWeapon(unsigned slot, void* item, bool dragging) {
    auto* s = Data().forge;
    if (!s || !s->active || (!s->picking && !dragging) ||
        s->phase != ForgePhase::Choose || !ForgeBagOpen(*s) ||
        !item || slot == 0 || slot > kForgeLastWeaponSlot || ForgeBagItem(slot) != item) return false;
    if (s->expected) return true;
    ForgeSyncRecipe(*s);
    if (!ForgeSlotAllowed(*s, slot, item)) return true;
    if (ForgeRequestWeapon(*s,static_cast<unsigned short>(slot),item)) ForgeRender();
    return true;
}

bool ForgeDropWeapon() {
    auto* s = Data().forge;
    if (!s || !s->active || !ForgeOwn(s->main, true)) return false;
    const auto* drag = reinterpret_cast<const void* (__thiscall*)(void*)>(0x32b4220)(s->manager);
    if (!Readable(drag, 12)) return false;
    auto* item = reinterpret_cast<void* (__cdecl*)()>(0x25ed700)();
    const auto* record = ForgeItemRecord(item);
    if (!Readable(record, 0x1c) || reinterpret_cast<uint32_t (__thiscall*)(const void*)>(0x994e30)(
        static_cast<const char*>(record) + 0x14) != Value(drag, 4)) return false;
    return ForgePickWeapon(Value(drag, 8), item, true);
}

int __fastcall ForgeEvent(void* self,void*,int sender,int event,int,int) {
    auto* s = Data().forge;
    if (!s || !s->active || !UIThread()) return 0;
    auto* owner = static_cast<unsigned char*>(self)-0x1b0;
    if (owner==s->main.window && ForgeOwn(s->main,true)) {
        if (event==10 && (SenderIs(Find(s->main.root,L"srcItem"),sender) ||
                         SenderIs(Find(s->main.root,L"srcItemArea"),sender))) return ForgeDropWeapon();
        if (event==14 && SenderIs(Find(s->main.root,L"srcItem"),sender) && s->phase == ForgePhase::Choose) {
            s->selected = {};
            s->selectedItem = nullptr;
            ForgeRender();
            return 1;
        }
        if (event != 13) return 0;
        if (SenderIs(Find(s->main.root,L"btnCancel"),sender) && s->phase == ForgePhase::Choose) {
            Post(ForgeCloseMessage); return 1;
        }
        if (SenderIs(Find(s->main.root,L"btnComplete"),sender) && s->phase == ForgePhase::Complete &&
            Tick()-s->animationStarted >= kForgeCompleteDuration) { Post(ForgeCloseMessage); return 1; }
        if (SenderIs(Find(s->main.root,L"btnSelectWeapon"),sender)) { Post(ForgeSelectMessage); return 1; }
        if (SenderIs(Find(s->main.root,L"btnOk"),sender)) { Post(ForgeAskMessage); return 1; }
    }
    if (event==13 && owner==s->confirm.window && ForgeOwn(s->confirm,true) && s->phase == ForgePhase::Choose) {
        if (SenderIs(Find(s->confirm.root,L"btnCancel"),sender)) { Post(ForgeDismissMessage); return 1; }
        if (SenderIs(Find(s->confirm.root,L"btnOk"),sender)) { Post(ForgeConfirmMessage); return 1; }
    }
    return 0;
}

void* const kForgeEvents[] = {reinterpret_cast<void*>(&ForgeEvent)};

bool ForgeOpenView(ForgeView& view) {
    auto& s = *Data().forge;
    using OpenWindow = void* (__thiscall*)(void*,int,int,int);
    const auto open = reinterpret_cast<OpenWindow>(0x32bea50);
    if (ForgeOwn(view,false)) return view.root && open(s.manager,view.id,0,0)==view.window;
    view = {};
    for (const int id : {821,819,517,518}) if (ForgeSlotFree(id)) { view.id=id; break; }
    if (!view.id || !Value(s.manager,0xc5b4)) return false;
    s.creating=&view;
    bool installed=false, restored=false;
    void* result=nullptr;
    __try {
        installed=ForgeSwapFactory(true);
        if (installed) result=open(s.manager,view.id,0,0);
    } __finally {
        restored=!installed || ForgeSwapFactory(false);
        s.creating=nullptr;
    }
    if (!installed || !restored || result!=view.window || !Readable(view.window,0x1dc) ||
        Value(view.window)!=0x49f03f4 || Value(view.window,0x1b0)!=0x49f03ec) return false;
    view.root=reinterpret_cast<void*>(Value(view.window,0x30));
    if (!Readable(view.root,0x100)) return false;
    const bool main = &view==&s.main;
    if (!Value(view.root,0x6c) || !Value(view.root,0x70))
        reinterpret_cast<void (__thiscall*)(void*,int,int)>(0x38271c0)(view.root,main?485:356,main?368:280);
    if (!Find(view.root,L"btnOk") || !Find(view.root,L"btnCancel") ||
        (main && (!Find(view.root,L"srcItem") || !Find(view.root,L"btnComplete") || !ForgeMaterialRadio(view))) ||
        (!main && (!NativeEdit(Find(view.root,L"editBoxConfirm")) || !Find(view.root,L"aniHammer")))) return false;
    if (main) {
        s.previewAnchor = Find(view.root,kForgePreviewAnchor);
        if (!Readable(s.previewAnchor,0x139)) return false;
        ForgeConfigureMain(view);
    }
    *reinterpret_cast<void* const**>(static_cast<unsigned char*>(view.window)+0x1b0)=kForgeEvents;
    return true;
}

void ForgeOpen() {
    auto& w = Data();
    if (!w.forge) {
        auto* storage=reinterpret_cast<ForgeState* (__cdecl*)(unsigned)>(0x8a9480)(sizeof(ForgeState));
        if (!storage) return;
        auto* bytes=reinterpret_cast<volatile unsigned char*>(storage);
        for (unsigned i=0;i<sizeof(ForgeState);++i) bytes[i]=0;
        Lock();
        w.forge=storage;
        Unlock();
    }
    auto& s=*w.forge;
    ForgeClose();
    auto* manager=*reinterpret_cast<void**>(managerAddress);
    if (s.manager!=manager) { s.manager=manager; s.main={}; s.confirm={}; s.previewAnchor=nullptr; }
    s.active=true;
    s.recipe = ForgeRecipe::Secret;
    s.selectedItem = nullptr;
    if (!Readable(manager,0xc640) || !ForgeOpenView(s.main) ||
        !WINAPI_AT(SetTimer,0x6ee5058)(w.hwnd,kForgeTimer,100,nullptr)) { ForgeClose(); return; }
    ForgeAnimation(s.main,L"aniComplete",false);
    ForgeAnimation(s.main,L"aniIdle",true,true);
    ForgeAnimation(s.main,L"aniTitleOpen",true,true);
    ForgeSetRadio(ForgeMaterialRadio(s.main),0);
    ForgeRender();
    ForgeReadMaterials(s);
    if (ForgeDispatch(kForgeCheckBag,kForgeNoSlot)) ForgeRender();
}

void ForgeAsk() {
    auto* s=Data().forge;
    if (!s || !s->active || s->expected || s->phase != ForgePhase::Choose ||
        s->picking) return;
    ForgeSyncRecipe(*s);
    if (!ForgeCanCraft(*s)) return;
    if (!ForgeOpenView(s->confirm)) { ForgeCloseView(s->confirm); return; }
    const auto* item = ForgeItemRecord(s->selectedItem);
    ForgeItemControl(s->confirm,L"itemSrc",item);
    ForgeLabel(s->confirm,L"textItemName",ForgeItemName(item));
    ForgeEnable(s->confirm,L"btnOk",true);
    ForgeEnable(s->confirm,L"btnCancel",true);
    ForgeEnable(s->confirm,L"editBoxConfirm",true);
    ForgeAnimation(s->confirm,L"aniHammer",false);
    ForgeAnimation(s->confirm,L"aniMain",true,true);
    ClearSearch(Find(s->confirm.root,L"editBoxConfirm"));
}

void ForgeConfirm() {
    auto* s=Data().forge;
    if (!s || !s->active || s->expected || !ForgeOwn(s->confirm,true) ||
        s->phase != ForgePhase::Choose) return;
    if (ForgeSyncRecipe(*s) || !ForgeCanCraft(*s)) return;
    auto* edit=Find(s->confirm.root,L"editBoxConfirm");
    if (!NativeEdit(edit)) return;
    const auto* value=reinterpret_cast<const wchar_t* (__thiscall*)(void*)>(readEditText)(edit);
    if (!Same(value,kForgeConfirmText,ARRAYSIZE(kForgeConfirmText))) return;
    if (!ForgeDispatch(kForgeConfirm, s->selected.slot)) return;
    s->phase = ForgePhase::Hammer;
    s->animationStarted = Tick();
    s->hammerReply = false;
    ForgeEnable(s->confirm,L"btnOk",false);
    ForgeEnable(s->confirm,L"btnCancel",false);
    ForgeEnable(s->confirm,L"editBoxConfirm",false);
    ForgeAnimation(s->confirm,L"aniMain",false);
    ForgeAnimation(s->confirm,L"aniHammer",true,true);
    ForgeRender();
}

const wchar_t* ForgeError(unsigned status, ForgeRecipe recipe) {
    switch (status) {
    case 2: return L"这把武器无法用于幻化，或背包位置已发生变化。";
    case 3: return recipe==ForgeRecipe::Secret ? L"缺少林纳斯的秘传模具。" : L"缺少林纳斯的神奇模具。";
    case 4: return L"背包已满，请腾出消耗品格。";
    case 7: return L"当前角色已登记该武器外观。";
    case 5: return L"确认已失效，请重新选择武器。";
    case 8: return L"操作太快，请稍候。";
    default: return L"请在艾尔文防线林纳斯处重新打开。";
    }
}

void ForgeFinishHammer(ForgeState& s) {
    if (!s.hammerReply || Tick()-s.animationStarted < kForgeHammerDuration) return;
    ForgeCloseView(s.confirm);
    s.selected = {};
    s.selectedItem = nullptr;
    if (s.hammerStatus) {
        s.phase = ForgePhase::Choose;
        RegisterNotice(ForgeError(s.hammerStatus,s.requestedRecipe));
    } else {
        s.phase = ForgePhase::Complete;
        s.animationStarted = Tick();
        ForgeAnimation(s.main,L"aniIdle",false);
        ForgeAnimation(s.main,L"aniComplete",true,true);
    }
    ForgeRender();
}

void ForgeFinishReply() {
    auto* s=Data().forge;
    if (!s) return;
    Lock();
    const ForgeReply r=s->reply;
    const bool accepted=s->active && s->queued && r.id==s->expected && r.epoch==Data().epoch;
    s->queued=false;
    if (accepted) s->expected=0;
    Unlock();
    if (!accepted) return;
    if (r.action == kForgeConfirm && s->phase == ForgePhase::Hammer) {
        s->hammerReply = true;
        s->hammerStatus = r.status;
        ForgeFinishHammer(*s);
        return;
    }
    if (r.action == kForgeCheckBag) {
        s->materialsReady = !r.status;
        s->eligible[0] = r.status ? 0 : r.tokenLow;
        s->eligible[1] = r.status ? 0 : r.tokenHigh;
        if (r.status == 8) s->refreshMaterials = true;
    } else if (r.action == kForgePickSlot) {
        if (!r.status && r.slot == s->requestedSlot && s->requestedItem && ForgeBagItem(r.slot) == s->requestedItem) {
            s->selected = r;
            s->selectedItem = s->requestedItem;
            s->selectedRecipe = s->requestedRecipe;
            s->picking = false;
        } else {
            s->selected = {};
            s->selectedItem = nullptr;
            if (r.status) RegisterNotice(ForgeError(r.status,s->requestedRecipe));
            s->refreshMaterials = true;
        }
        s->requestedItem = nullptr;
    }
    ForgeRender();
}

void ForgeTick() {
    auto* s=Data().forge;
    void* connection=nullptr;
    if (!s || !s->active) return;
    if (!ForgeOwn(s->main,true) || !Connected(connection)) { ForgeClose(); return; }
    Lock();
    const bool timedOut=s->expected && Tick()-s->sentAt>6000;
    if (timedOut) { s->expected=0; s->queued=false; }
    Unlock();
    if (timedOut && s->phase == ForgePhase::Hammer) {
        s->hammerReply = true;
        s->hammerStatus = 5;
    } else if (timedOut && s->action == kForgeCheckBag) {
        // 下次打开选材时重新查询背包，避免连续超时重发。
        s->picking = false;
    }
    if (s->phase == ForgePhase::Hammer) { ForgeFinishHammer(*s); return; }
    if (s->phase == ForgePhase::Complete) {
        ForgeEnable(s->main,L"btnComplete",Tick()-s->animationStarted >= kForgeCompleteDuration);
        return;
    }
    const bool moldsChanged = Tick()-s->moldCheckedAt >= 250 && ForgeRefreshMolds(*s,false);
    const bool recipeChanged = ForgeSyncRecipe(*s);
    const bool finishedPicking = s->picking && !ForgeBagOpen(*s);
    if (finishedPicking) s->picking = false;
    if (!s->expected && Tick()-s->sentAt >= 200 && !timedOut && ForgeSelectionCurrent(*s) &&
        !ForgeSelectedMoldCurrent(*s) && s->shownMolds[ForgeRecipeIndex(s->recipe)] &&
        ForgeRequestWeapon(*s,s->selected.slot,s->selectedItem)) {
        ForgeCloseView(s->confirm);
        ForgeRender();
        return;
    }
    if (ForgeBagOpen(*s) && !s->expected && Tick()-s->sentAt >= 150 &&
        (s->refreshMaterials || s->materialsRecipe != s->recipe || ForgeMaterialsMoved(*s))) {
        ForgeReadMaterials(*s);
        s->refreshMaterials = !ForgeDispatch(kForgeCheckBag, kForgeNoSlot);
    }
    if (timedOut || (s->selectedItem && !ForgeSelectionCurrent(*s))) {
        s->selected = {};
        s->selectedItem = nullptr;
        ForgeRender();
    } else if (finishedPicking || moldsChanged || recipeChanged) {
        ForgeRender();
    }
}

bool ForgeReceive(const unsigned char* body) {
    if (U32(body)!=0x31524657) return false;
    const ForgeReply r={U32(body+4),U32(body+8),U32(body+16),U32(body+20),
        static_cast<unsigned short>(body[14]|(body[15]<<8)),static_cast<unsigned short>(body[24]|(body[25]<<8)),body[12],body[13]};
    if ((r.action!=kForgeConfirm && r.action!=kForgeCheckBag && r.action!=kForgePickSlot) ||
        r.status>8 || !r.id || !r.epoch || body[26] || body[27]) return true;
    if (r.action != kForgeCheckBag &&
        ((!r.status && r.action==kForgePickSlot)!=bool(r.tokenLow||r.tokenHigh))) return true;
    Lock();
    auto* s=Data().forge;
    if (s && r.id==s->expected && r.epoch==s->epoch && r.epoch==Data().epoch && r.action==s->action && !s->queued) {
        s->reply=r;
        s->queued=Post(ForgeReplyMessage);
    }
    Unlock();
    return true;
}
