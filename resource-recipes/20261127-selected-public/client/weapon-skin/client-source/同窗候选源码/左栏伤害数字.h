#pragma once

constexpr unsigned kDamageNumberBytes = 0x98;
constexpr uintptr_t kDamageNumberVtable = 0x47c15cc;
constexpr unsigned kDamageNumberFrames[] = {0x2c,0x34,0x3c,0x44};
constexpr int kDamageNumberYOffset = 60;

void DestroyDamageNumber() {
    auto& s = Data();
    auto* model = s.damageNumber;
    s.damageNumber = nullptr;
    if (model)
        reinterpret_cast<void (__thiscall*)(void*, unsigned)>(0x168fc20)(model,1);
}

void RefreshAppliedDamageNumber() {
    auto& s = Data();
    auto* manager = reinterpret_cast<void* (__cdecl*)()>(0x1691c00)();
    const unsigned current = manager
        ? reinterpret_cast<unsigned short (__thiscall*)(void*)>(0x168d1e0)(manager)
        : kInvalidFont;
    if (current != s.damageNumberFont) {
        DestroyDamageNumber();
        s.damageNumberFont = current;
        if (auto* hint = Find(s.damageNumberHint,L"WS_DAMAGE_NUMBER_HINT_TEXT")) {
            SetLabel(hint,DefinitionText(FontDefinition(current),4));
        }
    }
    if (!s.damageNumberHint) return;
    auto* focus = reinterpret_cast<NativeFocusGroupGet>(0x32b50a0)(s.manager);
    const bool hover = !s.previewSuspended && current < 0xffff &&
        Readable(focus,8) && Value(focus,4)==reinterpret_cast<uintptr_t>(s.window) &&
        Readable(s.damageNumberButton,0x100) &&
        reinterpret_cast<bool (__thiscall*)(void*)>(0x37e9fd0)(s.damageNumberButton);
    Show(s.damageNumberHint,hover);
}

void* CreateDamageNumber(unsigned font, bool animate) {
    if (!FontDefinition(font)) return nullptr;
    auto* storage = reinterpret_cast<void* (__cdecl*)(unsigned)>(0x8a9480)(kDamageNumberBytes);
    if (!storage) return nullptr;
    auto* model = reinterpret_cast<void* (__thiscall*)(void*)>(0x168fb20)(storage);
    using Build = void (__thiscall*)(void*,int,int,int,int,unsigned,int,int,void*,unsigned,unsigned short);
    // 90 从倒数第二个参数查 DFK，最后一个参数用于选择动画变体。
    // 原生字体定义会限制这个有符号 32 位样例的显示值。
    constexpr int sampleDamage = 0x7fffffff;
    reinterpret_cast<Build>(0x1691c70)(model,0,0,0,sampleDamage,0x11,100,100,nullptr,font,0);
    if (!animate) {
        // x86 栈上的 float 0.7。直接传位模式，保持现有接线载体的无 CRT 构建方式。
        constexpr uint32_t dimAlpha = 0x3f333333;
        using ApplyAlpha = void (__thiscall*)(void*,uint32_t,bool);
        for (unsigned offset : kDamageNumberFrames)
            if (auto* animation = reinterpret_cast<void*>(Value(model,offset)))
                reinterpret_cast<ApplyAlpha>(0x36142b0)(animation,dimAlpha,false);
    }
    return model;
}

void RenderDamageNumber() {
    auto& s = Data();
    if (!s.visible || s.previewSuspended || s.damageNumberFont >= 0xffff ||
        !Readable(s.damageNumberAnchor,0x100) ||
        !Value(reinterpret_cast<void*>(0x581a298))) return;

    const bool animate = s.page == kFontPage;
    if (s.damageNumber && animate != s.damageNumberAnimated) DestroyDamageNumber();
    if (!s.damageNumber) {
        s.damageNumber = CreateDamageNumber(s.damageNumberFont,animate);
        s.damageNumberAnimated = animate;
    }
    auto* model = s.damageNumber;
    if (!Readable(model,kDamageNumberBytes) || Value(model)!=kDamageNumberVtable ||
        (!Value(model,0x34) && !Value(model,0x3c))) return;

    if (animate) {
        reinterpret_cast<void (__thiscall*)(void*)>(0x168d970)(model);
        if (static_cast<const unsigned char*>(model)[4]) {
            DestroyDamageNumber();
            return;
        }
    }
    const int x = reinterpret_cast<int (__thiscall*)(void*)>(0x209bbe0)(s.damageNumberAnchor);
    const int y = reinterpret_cast<int (__thiscall*)(void*)>(0x37e9f40)(s.damageNumberAnchor);
    reinterpret_cast<void (__thiscall*)(void*,int,int)>(0x1ac2fc0)(model,x,y-kDamageNumberYOffset);
    reinterpret_cast<void (__thiscall*)(void*)>(0x168d7c0)(model);
}
