constexpr int kRegisterConfirmWindow = 0x332;
constexpr int kRegisterNoticeWindow = 0x2d6;
constexpr uintptr_t kMainBagAddress = 0x56a379c;

struct RegisterCallback {
    unsigned char native[0x30];
    void* bag;
    void* item;
    uint32_t slot;
    uint32_t epoch;
    uint32_t productID;
    ForgeRecipe recipe;
};

void* RegisterMainBag() { return *reinterpret_cast<void**>(kMainBagAddress); }
void* RegisterManager() { return *reinterpret_cast<void**>(managerAddress); }

using RegisterOpenWindow = void* (__thiscall*)(void*, int, const wchar_t*, void*);

void RegisterNotice(const wchar_t* text) {
    void* manager = RegisterManager();
    if (manager)
        reinterpret_cast<RegisterOpenWindow>(0x32bea50)(manager, kRegisterNoticeWindow, text, nullptr);
}

void* RegisterItemAt(void* bag, uint32_t slot) {
    if (bag != RegisterMainBag() || !Readable(bag, 0x60)) return nullptr;
    const uintptr_t begin = Value(bag, 0x58), end = Value(bag, 0x5c);
    if (end < begin || slot >= (end - begin) / sizeof(void*)) return nullptr;
    const auto* entry = reinterpret_cast<void* const*>(begin) + slot;
    return Readable(entry, sizeof(*entry)) ? *entry : nullptr;
}

const WeaponLook* WeaponProductLook(void* item, ForgeRecipe& recipe) {
    const auto* record = ForgeItemRecord(item);
    if (!Readable(record,0x1c)) return nullptr;
    const auto id = Value(record,0x18);
    if (ForgeItemID(record) != id) return nullptr;
    if (id-kNewMagicBase<kNewWeaponCount) {
        recipe = ForgeRecipe::Magic;
        return &kWeaponCatalog[id-kNewMagicBase+3];
    }
    if (id-kNewSecretBase<kNewWeaponCount) {
        recipe = ForgeRecipe::Secret;
        return &kWeaponCatalog[id-kNewSecretBase+3];
    }
    for (unsigned i=0;i<3;++i) {
        const auto& look = kWeaponCatalog[i];
        if (id!=look.products[0] && id!=look.products[1]) continue;
        recipe = id==look.products[0] ? ForgeRecipe::Magic : ForgeRecipe::Secret;
        return &look;
    }
    return nullptr;
}

bool SendWeaponRegistration(const RegisterCallback& click) {
    auto& s = Data();
    void* connection = nullptr;
    auto recipe = ForgeRecipe::Invalid;
    if (!UIThread() || !Connected(connection) || Value(connection,0x2bcc34) || Value(connection,0x2bcc2c) ||
        RegisterItemAt(click.bag, click.slot) != click.item || !WeaponProductLook(click.item,recipe) ||
        recipe != click.recipe || ForgeItemID(ForgeItemRecord(click.item))!=click.productID) return false;
    unsigned char body[24] = {'W','S','F','2'};
    Lock();
    if (s.registerEpoch != click.epoch || (s.registerAwaiting && Tick() - s.registerSentAt < 6000)) {
        Unlock();
        return false;
    }
    if (++s.serial == 0) ++s.serial;
    s.registerAwaiting = s.serial;
    s.registerSentAt = Tick();
    s.registerQueued = false;
    Put32(body+4, s.registerAwaiting);
    Put32(body+8, s.registerEpoch);
    Unlock();
    body[12] = 2;
    body[13] = static_cast<unsigned char>(click.recipe);
    body[14] = static_cast<unsigned char>(click.slot);
    body[15] = static_cast<unsigned char>(click.slot >> 8);
    Put32(body+16,click.productID);
    if (reinterpret_cast<LONG (__thiscall*)(void*,unsigned)>(0x374f280)(connection,opcode)>=0) {
        const LONG appended = reinterpret_cast<LONG (__thiscall*)(void*,const void*,unsigned)>(0x374eeb0)(connection,body,sizeof(body));
        const LONG finished = reinterpret_cast<LONG (__cdecl*)()>(0x3750ac0)();
        if (appended>=0 && finished>=0) return true;
    }
    Lock();
    s.registerAwaiting = 0;
    s.registerQueued = false;
    Unlock();
    return false;
}

void __fastcall RegisterConfirmed(RegisterCallback* click, void*, int result) {
    __try {
        if (result == 0 && !SendWeaponRegistration(*click))
            RegisterNotice(L"外观道具所在的背包位置已变化，或请求暂时无法发送。请重新右键使用。");
    } __except(EXCEPTION_EXECUTE_HANDLER) {}
    // 使用原生回调的取消分支回收对象。该分支仅析构，不执行字体动作。
    reinterpret_cast<void (__thiscall*)(RegisterCallback*,int)>(0x24f8870)(click,1);
}

void* const kRegisterCallbacks[] = {
    reinterpret_cast<void*>(&RegisterConfirmed),
    reinterpret_cast<void*>(0x769ad0),
};

bool Initialize();

bool OpenWeaponRegistration(void* bag, void* item, uint32_t slot, ForgeRecipe recipe, const WeaponLook& look) {
    if (!Initialize()) return false;
    void* manager = RegisterManager();
    if (!manager) return false;
    if (reinterpret_cast<bool (__thiscall*)(void*,int)>(0x32b55e0)(manager,kRegisterConfirmWindow)) return true;
    void* connection = nullptr;
    if (!Connected(connection) || Value(connection,0x2bcc34) || Value(connection,0x2bcc2c)) return false;
    const unsigned job = PreviewRoleJob(CurrentPreviewRole());
    if (job < kJobCount && !(look.jobs & (1u<<job))) {
        RegisterNotice(L"当前角色的职业无法登记此武器外观。");
        return true;
    }
    auto& s=Data();
    Lock();
    if (s.registerEpoch==0) s.registerEpoch=1;
    const uint32_t epoch=s.registerEpoch;
    const bool awaiting=s.registerAwaiting && Tick()-s.registerSentAt<6000;
    Unlock();
    if (awaiting) return true;
    auto* callback = reinterpret_cast<RegisterCallback* (__cdecl*)(unsigned)>(0x8a9480)(sizeof(RegisterCallback));
    if (!callback) return false;
    reinterpret_cast<void (__thiscall*)(RegisterCallback*)>(0x3388220)(callback);
    *reinterpret_cast<void* const**>(callback) = kRegisterCallbacks;
    callback->bag = bag;
    callback->item = item;
    callback->slot = slot;
    callback->epoch = epoch;
    callback->productID=ForgeItemID(ForgeItemRecord(item));
    callback->recipe = recipe;
    reinterpret_cast<RegisterOpenWindow>(0x32bea50)(manager, kRegisterConfirmWindow,
        L"使用后，该武器外观将登记到当前角色的皮肤仓库。确定使用吗？", callback);
    return true;
}

bool ReceiveWeaponRegistration(const unsigned char* body) {
    if (U32(body)!=0x31524657 || body[13]!=2 || !U32(body+4)) return false;
    if (body[12]>8 || body[26] || body[27] || U32(body+16) || U32(body+20)) return true;
    auto& s=Data();
    Lock();
    if (U32(body+4)==s.registerAwaiting && U32(body+8)==s.registerEpoch && !s.registerQueued) {
        s.registerStatus=body[12];
        s.registerQueued=Post(ForgeRegisteredMessage);
    }
    Unlock();
    return true;
}

void FinishWeaponRegistration() {
    auto& s=Data();
    Lock();
    if (!s.registerQueued) { Unlock(); return; }
    const auto status=s.registerStatus;
    s.registerAwaiting=0;
    s.registerQueued=false;
    Unlock();
    if (!status) {
        s.weaponListRefreshPending = s.visible;
        if (s.page==kSummaryPage) ScheduleSummary(1);
        RegisterNotice(L"武器外观已登记到当前角色的皮肤仓库。");
    } else if (status==7) {
        RegisterNotice(L"当前角色已登记该武器外观。");
    } else if (status==6) {
        RegisterNotice(L"这个位置的武器外观道具已变化，请重新选择。");
    } else if (status==8) {
        RegisterNotice(L"操作太频繁，请稍后再试。");
    } else {
        RegisterNotice(L"当前无法登记武器外观，请在城镇重试。");
    }
}
