// 只读取本机 24.9 的场景来源标记，在原生武器图层优先级计算中投影仓库装扮。
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <cstdint>

using Object = unsigned char;
struct WeaponLook {
    unsigned source, id, products[2];
    const wchar_t *name, *icon;
    unsigned frame, jobs;
};
static_assert(sizeof(WeaponLook)==32, "外部目录行尺寸变化");
#include "武器外观目录.h"
#include "角色原生表.h"
constexpr unsigned visualMarker = 0x31565357;
constexpr unsigned itemVTable = 0x49d0b14;
constexpr unsigned avatarSlot = 10;
constexpr unsigned weaponSlot = 12;
constexpr unsigned nativeBaseLayers = 0xfb0;
constexpr unsigned nativeAvatarLayers = 0xfdc;
constexpr unsigned nativeLayerOverride = 0x1038;
constexpr unsigned nativeRoleLayerCache = 0x38a0;
constexpr unsigned nativeLayerRecordSize = 0x168;
constexpr unsigned bindingBuckets = 32;

struct Binding {
    Object* role;
    Object* scene;
    Object* sample;
    Object* weapon;
    unsigned key;
    bool active;
    Binding* next;
};

struct State {
    const void* appearance;
    unsigned selected;
    bool hasAvatarRow;
    Binding* bindings[bindingBuckets];
};

static_assert(sizeof(State) <= 0x200, "场景武器状态必须放进预留区");

State& Data() {
    return *reinterpret_cast<State*>(0x7089e00);
}

template<class T>
T& At(Object* object, unsigned offset) {
    return *reinterpret_cast<T*>(object + offset);
}

template<class T>
T Native(unsigned address) {
    return reinterpret_cast<T>(address);
}

template<class T>
T Method(Object* object, unsigned offset) {
    return reinterpret_cast<T>(At<unsigned>(At<Object*>(object, 0), offset));
}

bool IsNativeRole(Object* role) {
    return role && OriginalRoleDestructor(At<unsigned>(role, 0));
}

bool AllowsAppearance(Object* role, unsigned id) {
    const unsigned job = At<unsigned>(role, 0x4088);
    const unsigned position = WeaponLookPosition(id);
    return job<16 && position<kWeaponCatalogCount && (kWeaponCatalog[position].jobs & (1u<<job));
}

Object* Worn(Object* role, unsigned slot) {
    return At<Object*>(role, 0x3230 + 4*slot);
}

bool IsItem(Object* item) {
    return item && At<unsigned>(item, 0) == itemVTable;
}

void Release(Object* item) {
    if (item) Method<void (__thiscall*)(Object*, unsigned)>(item, 0x0c)(item, 1);
}

Binding*& BindingLink(Object* role) {
    const auto address = reinterpret_cast<uintptr_t>(role);
    const auto bucket = (address ^ (address >> 8)) >> 4 & (bindingBuckets - 1);
    auto** link = &Data().bindings[bucket];
    while (*link && (*link)->role != role) {
        link = &(*link)->next;
    }
    return *link;
}

Binding* FindRole(Object* role) {
    return BindingLink(role);
}

Binding* Find(Object* role) {
    auto* entry = FindRole(role);
    return entry && role && entry->key == At<unsigned>(role, 0x6864) ? entry : nullptr;
}

void WarmLayers(Object* role, Object* item) {
    unsigned type = Method<unsigned (__thiscall*)(Object*)>(role, 0x680)(role);
    if (!Method<bool (__thiscall*)(Object*, unsigned)>(item, 0x1a8)(item, type)) {
        Method<void (__thiscall*)(Object*, unsigned, unsigned)>(item, 0x1a0)(item, type, 1);
    }
}

bool HasWeaponLayers(Object* sample) {
    if (!IsItem(sample) || At<unsigned>(sample, 0xff4) != weaponSlot) return false;
    unsigned begin = At<unsigned>(sample, 0xfe0);
    unsigned end = At<unsigned>(sample, 0xfe4);
    unsigned capacity = At<unsigned>(sample, 0xfe8);
    return begin && begin < end && end <= capacity &&
        end - begin <= 16*0x168 && (end - begin) % 0x168 == 0;
}

bool KnownAppearance(unsigned id) {
    return WeaponLookPosition(id) < kWeaponCatalogCount;
}

unsigned AppearanceID(Object* item) {
    if (!IsItem(item)) return 0;
    auto* record=Method<Object* (__thiscall*)(Object*)>(item,0x28)(item);
    return record ? Native<unsigned (__thiscall*)(Object*)>(0x994e30)(record+0x14) : 0;
}

Object* CreateSample(Object* role,unsigned id) {
    alignas(16) Object descriptor[0xd0];
    Object* prepared = Native<Object* (__thiscall*)(Object*)>(0x2430fb0)(descriptor);
    Object* item = Native<Object* (__cdecl*)(unsigned, Object*, unsigned, unsigned)>(0x2a32b30)(
        id, prepared, 1, 0);
    Native<void (__thiscall*)(Object*)>(0x985650)(descriptor);
    if (IsItem(item)) {
        // 对照 0x27CAB89 的原生默认展示对象构造。只初始化独立对象。
        WarmLayers(role, item);
        if (HasWeaponLayers(item)) return item;
    }
    Release(item);
    return nullptr;
}

Binding* GetOrCreate(Object* role) {
    auto& link = BindingLink(role);
    if (link) return link;
    auto* entry = Native<Binding* (__cdecl*)(unsigned)>(0x8a9480)(sizeof(Binding));
    if (!entry) return nullptr;
    entry->role = role;
    entry->scene = nullptr;
    entry->sample = nullptr;
    entry->weapon = nullptr;
    entry->key = At<unsigned>(role, 0x6864);
    entry->active = false;
    entry->next = nullptr;
    link = entry;
    return entry;
}

Object* RemoveBinding(Object* role) {
    auto& link = BindingLink(role);
    auto* entry = link;
    if (!entry) return nullptr;
    link = entry->next;
    Object* sample = entry->sample;
    Native<void (__cdecl*)(void*)>(0x3e70882)(entry);
    return sample;
}

bool Readable(const void* address, size_t size) {
    auto cursor = reinterpret_cast<uintptr_t>(address);
    if (!cursor || size > UINTPTR_MAX - cursor) return false;
    const auto end = cursor + size;
    const auto query = *reinterpret_cast<decltype(&::VirtualQuery)*>(0x6ee5038);
    for (;;) {
        MEMORY_BASIC_INFORMATION info;
        if (!query(reinterpret_cast<const void*>(cursor), &info, sizeof(info)) ||
            info.State != MEM_COMMIT || (info.Protect & (PAGE_GUARD | PAGE_NOACCESS))) return false;
        const auto next = reinterpret_cast<uintptr_t>(info.BaseAddress) + info.RegionSize;
        if (next <= cursor) return false;
        if (next >= end) return true;
        cursor = next;
    }
}

bool TraversableLayers(Object* layers) {
    // 原生迭代器通过容器代理回查所属列表；仅有可读的起止地址还不够。
    if (!Readable(layers,16)) return false;
    auto* proxy = At<Object*>(layers,0);
    if (!Readable(proxy,4) || At<Object*>(proxy,0) != layers) return false;
    const auto begin = At<unsigned>(layers,4);
    const auto end = At<unsigned>(layers,8);
    const auto capacity = At<unsigned>(layers,12);
    if (begin > end || end > capacity || (capacity - begin) % nativeLayerRecordSize ||
        (end - begin) % nativeLayerRecordSize) return false;
    if (begin == end) return begin || !capacity;
    return Readable(reinterpret_cast<const void*>(begin),end-begin);
}

void SanitizeRoleLayerCache(Object* role) {
    auto& cached = At<Object*>(role, nativeRoleLayerCache);
    if (cached && !TraversableLayers(cached)) cached = nullptr;
}

bool CanTraverseItemLayers(Object* item) {
    if (!IsItem(item)) return false;
    auto* layers = Method<Object* (__thiscall*)(Object*)>(item, 0x1dc)(item);
    return TraversableLayers(layers) &&
        (!At<unsigned char>(item, 0xff1) || TraversableLayers(item + nativeAvatarLayers));
}

void AttachBattleOnly(Object* role, Object* item) {
    Native<void (__thiscall*)(Object*, Object*)>(0x2726790)(role, item);
}

void Attach(Object* role, Object* item) {
    // 原生装配分两步：0x2726790 绑定战斗动作容器(0x2df8/0x2f24)，0x278dd40 绑定待机动作容器(0x4258)。
    AttachBattleOnly(role, item);
    SanitizeRoleLayerCache(role);
    if (CanTraverseItemLayers(item)) {
        Native<void (__thiscall*)(Object*, Object*)>(0x278dd40)(role, item);
    }
}

bool Detach(Object* role, Object* item) {
    if (!CanTraverseItemLayers(item)) return false;
    const bool restoredBaseWeapon = At<unsigned char>(item, 0xff1) != 0;
    SanitizeRoleLayerCache(role);
    // 0x2726e60 卸载战斗动作图层并写入 role+0x38a4/0x38a5；0x278e1e0 消费 0x38a5 卸载待机动作图层。
    Native<void (__thiscall*)(Object*, Object*, unsigned)>(0x2726e60)(role, item, 0);
    SanitizeRoleLayerCache(role);
    if (CanTraverseItemLayers(item)) {
        Native<void (__thiscall*)(Object*, Object*)>(0x278e1e0)(role, item);
    }
    return restoredBaseWeapon;
}

void ClearRetiredLayerAliases(Object* role, Object* retired) {
    if (!retired) return;
    const auto pointsToRetired = [retired](Object* layers) {
        return layers == retired + nativeBaseLayers || layers == retired + nativeAvatarLayers;
    };
    auto& cached = At<Object*>(role,nativeRoleLayerCache);
    if (pointsToRetired(cached)) cached = nullptr;
    auto* weapon = Worn(role,weaponSlot);
    if (IsItem(weapon)) {
        auto& effective = At<Object*>(weapon,nativeLayerOverride);
        if (pointsToRetired(effective)) effective = nullptr;
    }
}

extern "C" __declspec(noinline) void __cdecl SelectAppearance(Object* role,Object* scene,unsigned id) {
    if (!IsNativeRole(role)) return;
    if (id && !AllowsAppearance(role,id)) id=0;
    Binding* entry = id ? GetOrCreate(role) : FindRole(role);
    if (!entry) return;
    Object* weapon = Worn(role, weaponSlot);
    Object* avatar = Worn(role, avatarSlot);
    unsigned key = At<unsigned>(role, 0x6864);
    const bool sameItem=id && AppearanceID(entry->sample)==id && key==entry->key;
    if (entry->active && sameItem && scene == entry->scene && weapon == entry->weapon &&
            (!IsItem(weapon) || At<Object*>(weapon, 0x1038) == entry->sample+0xfdc)) return;
    Object* appearance=sameItem ? entry->sample : id ? CreateSample(role,id) : nullptr;
    Object* previous=entry->sample;
    if (!entry->active && !appearance) {
        RemoveBinding(role);
        ClearRetiredLayerAliases(role,previous);
        Release(previous);
        return;
    }
    bool baseWeaponRestored = false;
    if (entry->active) {
        // 先关闭激活态，使 0x2726e60 内部重算优先级时退回实穿武器/装扮，再卸载战斗与待机图层。
        entry->active = false;
        baseWeaponRestored = Detach(role, previous);
    } else if (IsItem(avatar) && At<unsigned char>(avatar, 0xff1)) {
        baseWeaponRestored = Detach(role, avatar);
    } else {
        Detach(role, weapon);
    }
    // 原生绑定先读取实穿的图层列表，再重算优先级。先移除指向待释放对象的旧别名。
    if (appearance!=previous) ClearRetiredLayerAliases(role,previous);
    entry->sample=appearance;
    entry->active=appearance!=nullptr;
    entry->scene = scene;
    entry->weapon = weapon;
    entry->key = key;
    if (appearance) {
        Attach(role, entry->sample);
    } else if (avatar) {
        Attach(role, avatar);
    } else if (weapon) {
        if (!baseWeaponRestored) Attach(role, weapon);
    } else {
        Native<void (__thiscall*)(Object*)>(0x26f9740)(role);
    }
    if (!appearance) RemoveBinding(role);
    // 原武器可能仍指向旧外观的图层集合，等原生优先级完成重绑后再释放。
    if (appearance!=previous) {
        ClearRetiredLayerAliases(role,previous);
        Release(previous);
    }
}

extern "C" __declspec(noinline) void __cdecl BeginSceneAppearance(const void* appearance) {
    auto& state = Data();
    state.appearance = appearance;
    state.hasAvatarRow = false;
    state.selected = 0;
}

extern "C" __declspec(naked) void ReadSceneAppearance() {
    __asm {
        pushfd
        pushad
        push [esp+40]
        call BeginSceneAppearance
        add esp,4
        popad
        popfd
        push 022bde30h
        ret
    }
}

extern "C" __declspec(noinline) void __cdecl ObserveAvatarRow(
    const void* appearance, unsigned caller, unsigned slot, unsigned id, unsigned auxiliary) {
    auto& state = Data();
    if (caller != 0x22c5349 || appearance != state.appearance || slot != avatarSlot) return;
    state.hasAvatarRow = true;
    state.selected = auxiliary == visualMarker && KnownAppearance(id) ? id : 0;
}

extern "C" __declspec(naked) void ObserveAvatarRowHook() {
    __asm {
        pushfd
        pushad
        push [ebp-38h]
        push [ebp-30h]
        movzx eax, byte ptr [ebp-25h]
        push eax
        push [ebp+4]
        push esi
        call ObserveAvatarRow
        add esp,20
        popad
        popfd
        lea eax,[ebp-28h]
        push 1
        push 022bdf2eh
        ret
    }
}

extern "C" __declspec(noinline) void __cdecl ApplySceneAppearance(
    Object* role, Object* scene, const void* appearance) {
    auto& state = Data();
    if (appearance != state.appearance || !state.hasAvatarRow) return;
    const unsigned selected=state.selected;
    state.appearance = nullptr;
    state.hasAvatarRow = false;
    if (!IsNativeRole(role) || !scene || At<Object*>(scene, 0x4f8) != role) return;
    unsigned key = At<unsigned short>(scene, 0x20);
    if (!key || key != At<unsigned>(role, 0x6864)) return;
    SelectAppearance(role,scene,selected);
}

extern "C" __declspec(naked) void ApplySceneAppearanceHook() {
    __asm {
        push [esp+8]
        push [esp+8]
        mov eax, 022c0d50h
        call eax
        add esp,8
        pushfd
        pushad
        push [esp+44]
        push esi
        push [esp+48]
        call ApplySceneAppearance
        add esp,12
        popad
        popfd
        ret
    }
}

extern "C" __declspec(noinline) Object* __cdecl SelectRenderSlot(Object* role, unsigned caller, unsigned slot) {
    if (slot != avatarSlot) return nullptr;
    // 仅 0x26F9740 的原生图层优先级计算。其它装备读取仍走原访问器。
    switch (caller) {
    case 0x26f975d:
    case 0x26f97f9:
    case 0x26f982d:
    case 0x26f98dc:
    case 0x26f997b:
        break;
    default:
        return nullptr;
    }
    auto* entry = Find(role);
    if (!entry || !entry->active) return nullptr;
    if (caller == 0x26f975d) {
        Object* worn = Worn(role, avatarSlot);
        if (IsItem(worn)) {
            Native<void (__thiscall*)(Object*, unsigned)>(0x255e5e0)(worn, 0);
            Native<void (__thiscall*)(Object*, unsigned)>(0x255e5f0)(worn, 0);
        }
    }
    return entry->sample;
}

extern "C" __declspec(naked) void SelectRenderSlotHook() {
    __asm {
        pushfd
        cmp dword ptr [esp+8], 0ah
        jne original
        cmp dword ptr [esp+4], 026f975dh
        jb original
        cmp dword ptr [esp+4], 026f997bh
        ja original
        pushad
        push [esp+40]
        push [esp+40]
        push ecx
        call SelectRenderSlot
        add esp,12
        test eax,eax
        jz restore
        mov [esp+28],eax
        popad
        popfd
        ret 4
    restore:
        popad
    original:
        popfd
        push ebp
        mov ebp,esp
        push esi
        mov esi,ecx
        push 02724576h
        ret
    }
}

extern "C" void __fastcall AttachRealLayers(Object* role, void*, Object* item) {
    auto* entry = Find(role);
    if (!entry || !entry->active || !item) {
        AttachBattleOnly(role, item);
        return;
    }
    // 配置和业务属性仍由进入本回调以前的原生穿戴逻辑处理。
    Object* weapon = Worn(role, weaponSlot);
    const bool isWornAvatar = item == Worn(role, avatarSlot);
    const bool refresh = isWornAvatar || (item == weapon && (entry->weapon != weapon ||
        (IsItem(weapon) && At<Object*>(weapon, 0x1038) != entry->sample+0xfdc)));
    if (!isWornAvatar) AttachBattleOnly(role, item);
    if (refresh) {
        entry->active = false;
        Detach(role, entry->sample);
        entry->active = true;
        Attach(role, entry->sample);
        entry->weapon = weapon;
    }
}

extern "C" Object* __fastcall DestroySwordman(Object* role, void*, unsigned flags) {
    // 保留该角色类原有的标量析构。待原生图层与穿戴销毁后才释放外观。
    const unsigned destructor = OriginalRoleDestructor(At<unsigned>(role,0));
    if (!destructor) return role;
    Object* item = RemoveBinding(role);
    Object* original = Native<Object* (__thiscall*)(Object*, unsigned)>(destructor)(role, flags);
    Release(item);
    return original;
}

extern "C" void __cdecl PayloadEntry() {}
