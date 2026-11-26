#pragma once

constexpr unsigned kPreviewBytes = 0x9e4;
constexpr unsigned kPreviewJob = 0x988;
constexpr unsigned kPreviewGear = 0x40;
constexpr unsigned kJobCount = 16;
constexpr unsigned kGearSlots = 33;
constexpr unsigned kUnknownSlot = kGearSlots;
constexpr unsigned kWeaponAvatarSlot = 10;
constexpr unsigned kWeaponLayerCategory = 12;
constexpr unsigned kLayerRecordBytes = 0x168;
constexpr uintptr_t kEquipmentTable = 0x49d0b14;

void* CurrentPreviewRole() {
    return reinterpret_cast<void* (__cdecl*)()>(0x29610e0)();
}

unsigned PreviewRoleJob(void* role) {
    if (!Readable(role,4)) return kJobCount;
    const auto* table = reinterpret_cast<const void*>(Value(role));
    if (!Readable(table,0xac8)) return kJobCount;
    using JobInfo = const unsigned* (__thiscall*)(void*);
    const auto* job = reinterpret_cast<JobInfo>(Value(table,0xac4))(role);
    return Readable(job,4) ? *job : kJobCount;
}

void* CreatePreviewModel() {
    auto* storage = reinterpret_cast<void* (__cdecl*)(unsigned)>(0x8a9480)(kPreviewBytes);
    if (!storage) return nullptr;
    auto* model = reinterpret_cast<void* (__thiscall*)(void*)>(0x3526860)(storage);
    reinterpret_cast<void (__thiscall*)(void*)>(0x3526f70)(model);
    return model;
}

void* CreatePreviewEquipment(unsigned id) {
    // 礼包试穿使用同一个道具描述和道具构造接口。
    alignas(16) unsigned char descriptor[0xd0];
    auto* prepared = reinterpret_cast<void* (__thiscall*)(void*)>(0x2430fb0)(descriptor);
    auto* item = reinterpret_cast<void* (__cdecl*)(unsigned, void*, unsigned, unsigned)>(0x2a32b30)(
        id,prepared,1,0);
    reinterpret_cast<void (__thiscall*)(void*)>(0x985650)(descriptor);
    return item;
}

void ReleasePreview(void* model, void* item) {
    if (model) {
        // 先销毁模型内部持有的各职业绘制对象，再释放模型。
        reinterpret_cast<void (__thiscall*)(void*)>(0x3525890)(model);
        reinterpret_cast<void (__thiscall*)(void*)>(0x3525e80)(model);
        reinterpret_cast<void (__cdecl*)(void*)>(0x3e70882)(model);
    }
    if (item) {
        const auto* table = reinterpret_cast<const void*>(Value(item));
        reinterpret_cast<void (__thiscall*)(void*, unsigned)>(Value(table,0x0c))(item,1);
    }
}

void DrawPreviewModel(void* model, int x, int y) {
    reinterpret_cast<void (__thiscall*)(void*, int, int)>(0x3526400)(model,x,y);
}

void DestroyCharacterPreview() {
    auto& s = Data();
    auto* model = s.characterPreview;
    auto* item = s.previewWeapon;
    s.characterPreview = s.previewRole = s.previewWeapon = nullptr;
    s.previewWeaponSlot = kUnknownSlot;
    s.previewWantsWeapon = false;
    if (s.weapons) s.weapons->preview=0;
    ReleasePreview(model,item);
}

bool PreviewGearMatches(void* model, void* role, void* weapon, unsigned weaponSlot, bool hideWeaponAvatar) {
    if (!model || !Readable(role,4)) return false;
    const auto* table = reinterpret_cast<const void*>(Value(role));
    if (!Readable(table,0xb58)) return false;
    const auto job = Value(model,kPreviewJob);
    if (job >= kJobCount || PreviewRoleJob(role) != job) return false;
    const auto getGear = reinterpret_cast<void* (__thiscall*)(void*, unsigned)>(Value(table,0xb54));
    const unsigned start = kPreviewGear + job*kGearSlots*4;
    for (unsigned slot = 0; slot < kGearSlots; ++slot) {
        auto* expected = slot == weaponSlot ? weapon : getGear(role,slot);
        if (hideWeaponAvatar && slot == kWeaponAvatarSlot) expected = nullptr;
        if (Value(model,start+slot*4) != reinterpret_cast<uintptr_t>(expected)) return false;
    }
    return true;
}

bool CharacterPreviewMatches(void* role) {
    const auto& s = Data();
    return role == s.previewRole &&
        PreviewGearMatches(s.characterPreview,role,s.previewWeapon,s.previewWeaponSlot,false);
}

unsigned HoveredWeapon() {
    const auto& s = Data();
    if (s.page!=kWeaponPage || !s.ready || !s.weapons || s.previewSuspended || !WarehousePreviewFocused()) return 0;
    for (const auto& row:s.weapons->rows) {
        if (row.look && PreviewControlHit(row.card,true))
            return PreviewControlHit(row.apply) || PreviewControlHit(row.clear) ? 0 : row.look->id;
    }
    return 0;
}

void PreviewWeapon(void* model, void* role,unsigned id) {
    auto* item = CreatePreviewEquipment(id);
    if (!item) return;
    auto& s = Data();
    s.previewWeapon = item;
    if (!Readable(item,0x1000) || Value(item) != kEquipmentTable) return;
    const auto* table = reinterpret_cast<const void*>(Value(item));
    using GetSlot = const unsigned* (__thiscall*)(void*);
    const auto* slot = reinterpret_cast<GetSlot>(Value(table,0x154))(item);
    if (!Readable(slot,4) || *slot != kWeaponAvatarSlot) return;

    // 复用场景别云剑初始化图层的原生调用，图层类别 12 对应试穿装备槽 10。
    const auto* roleTable = reinterpret_cast<const void*>(Value(role));
    const auto type = reinterpret_cast<unsigned (__thiscall*)(void*)>(Value(roleTable,0x680))(role);
    if (!reinterpret_cast<bool (__thiscall*)(void*,unsigned)>(Value(table,0x1a8))(item,type))
        reinterpret_cast<void (__thiscall*)(void*,unsigned,unsigned)>(Value(table,0x1a0))(item,type,1);
    const auto begin = Value(item,0xfe0), end = Value(item,0xfe4), capacity = Value(item,0xfe8);
    if (Value(item,0xff4) != kWeaponLayerCategory || !begin || end <= begin || end > capacity ||
        end-begin > 16*kLayerRecordBytes || (end-begin)%kLayerRecordBytes) return;

    reinterpret_cast<void (__thiscall*)(void*, unsigned, void*, int)>(0x35274f0)(model,kWeaponAvatarSlot,item,0);
    const auto job = Value(model,kPreviewJob);
    if (job < kJobCount && Value(model,kPreviewGear+(job*kGearSlots+kWeaponAvatarSlot)*4) == reinterpret_cast<uintptr_t>(item))
        s.previewWeaponSlot = kWeaponAvatarSlot;
}

void RefreshCharacterPreview(unsigned summaryWeapon) {
    auto& s = Data();
    if (!s.visible || !s.characterPreviewAnchor || s.previewSuspended) return;
    auto* role = CurrentPreviewRole();
    if (!Readable(role,4)) {
        DestroyCharacterPreview();
        return;
    }
    unsigned wanted=0;
    if (s.weapons) {
        wanted=summaryWeapon ? summaryWeapon : HoveredWeapon();
        if (!wanted) wanted=s.weapons->selected;
        if (wanted) {
            const unsigned job=PreviewRoleJob(role);
            const unsigned index=WeaponLookPosition(wanted);
            if (job>=kJobCount || index>=kWeaponCatalogCount || !(kWeaponCatalog[index].jobs & (1u<<job))) wanted=0;
        }
    }
    if ((!s.weapons || wanted==s.weapons->preview) && CharacterPreviewMatches(role)) return;
    DestroyCharacterPreview();
    auto* model = CreatePreviewModel();
    if (!model) return;
    s.characterPreview = model;
    s.previewRole = role;
    s.previewWantsWeapon = wanted!=0;
    if (s.weapons) s.weapons->preview=wanted;
    if (wanted) PreviewWeapon(model,role,wanted);
}

void RenderCharacterPreview() {
    const auto& s = Data();
    if (s.previewSuspended || !s.visible || !Readable(s.characterPreviewAnchor,0x100) ||
        !CharacterPreviewMatches(CurrentPreviewRole())) return;
    auto* anchor = s.characterPreviewAnchor;
    const int x = reinterpret_cast<int (__thiscall*)(void*)>(0x209bbe0)(anchor);
    const int y = reinterpret_cast<int (__thiscall*)(void*)>(0x37e9f40)(anchor);
    DrawPreviewModel(s.characterPreview,x,y);
}
