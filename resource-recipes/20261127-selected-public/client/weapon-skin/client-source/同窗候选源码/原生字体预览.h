#pragma once

constexpr int kNativeFontPreviewWindow = 203;
constexpr uintptr_t kNativeFontPreviewVtable = 0x471b74c;
constexpr uintptr_t kFontCardVtable = 0x4d4ee3c;
// 原生面板宽 200。左栏右边界为 392，面板右侧留 16 像素。
constexpr int kFontPreviewLeft = 176;
// 原生数字锚点位于面板内 y=49，仓库预览锚点 y=236。
constexpr int kFontPreviewTop = 187;

using NativeWindowOpen = void* (__thiscall*)(void*, int, int, int);
using NativeWindowClose = void (__thiscall*)(void*, int, int, int);
using NativeWindowGet = void* (__thiscall*)(void*, int, int);
using NativeFocusGroupGet = void* (__thiscall*)(void*);

bool WarehousePreviewFocused() {
    const auto& s=Data();
    auto* focus=reinterpret_cast<NativeFocusGroupGet>(0x32b50a0)(s.manager);
    return Readable(focus,8) && Value(focus,4)==reinterpret_cast<uintptr_t>(s.window);
}

bool PreviewControlHit(void* control, bool card=false) {
    return Readable(control,0xec) && (!card || Value(control)==kFontCardVtable) &&
        static_cast<const unsigned char*>(control)[0x60] &&
        reinterpret_cast<bool (__thiscall*)(void*)>(0x37e9fd0)(control);
}

void* ActiveFontPreview(void* manager) {
    return reinterpret_cast<NativeWindowGet>(0x32b5c90)(manager,kNativeFontPreviewWindow,0);
}

void CloseFontPreview() {
    auto& s = Data();
    auto* preview = s.fontPreviewWindow;
    s.fontPreviewWindow = nullptr;
    s.previewFont = kInvalidFont;
    if (preview && s.manager && Value(reinterpret_cast<void*>(managerAddress))==reinterpret_cast<uintptr_t>(s.manager) &&
        ActiveFontPreview(s.manager)==preview) {
        reinterpret_cast<NativeWindowClose>(0x32b94a0)(s.manager,kNativeFontPreviewWindow,-1,0);
    }
}

unsigned FontUnderMouse() {
    const auto& s = Data();
    if (s.previewSuspended || !s.visible || s.page!=kFontPage || !WarehousePreviewFocused()) return kInvalidFont;
    static_assert(ARRAYSIZE(s.fontCards)==kFontRows);
    for (unsigned row=0; row<kFontRows; ++row) {
        if (s.fontIds[row]<=0xffff && PreviewControlHit(s.fontCards[row],true))
            return s.fontIds[row];
    }
    return kInvalidFont;
}

void PositionFontPreview(void* preview) {
    const auto& s = Data();
    const auto* warehouseTable = reinterpret_cast<const void*>(Value(s.window));
    auto* focus = reinterpret_cast<NativeFocusGroupGet>(0x32b50a0)(s.manager);
    // 原版 202 打开预览后把游戏内窗口焦点交回主窗。
    if (Readable(focus,8))
        reinterpret_cast<void (__thiscall*)(void*, int, void*)>(0x32b46e0)(focus,0,s.window);
    const int x = reinterpret_cast<int (__thiscall*)(void*)>(Value(warehouseTable,0xc8))(s.window);
    const int y = reinterpret_cast<int (__thiscall*)(void*, int)>(Value(warehouseTable,0xcc))(s.window,1);
    const auto* previewTable = reinterpret_cast<const void*>(Value(preview));
    reinterpret_cast<void (__thiscall*)(void*, int, int)>(Value(previewTable,0xbc))(
        preview,x+kFontPreviewLeft,y+kFontPreviewTop);
}

void RefreshFontPreview(unsigned font) {
    auto& s = Data();
    if (font==s.previewFont) return;
    CloseFontPreview();
    s.previewFont = font;
    if (font==kInvalidFont || ActiveFontPreview(s.manager) || !FontDefinition(font)) return;

    auto* preview = reinterpret_cast<NativeWindowOpen>(0x32bea50)(
        s.manager,kNativeFontPreviewWindow,0,static_cast<int>(font));
    if (Readable(preview,4) && Value(preview)==kNativeFontPreviewVtable) {
        s.fontPreviewWindow = preview;
        PositionFontPreview(preview);
    }
}
