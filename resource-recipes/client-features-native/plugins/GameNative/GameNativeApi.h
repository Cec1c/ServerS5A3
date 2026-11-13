#pragma once

#include <windows.h>

#if defined(CLIENT_PATCH_GAME_NATIVE_EXPORTS)
#define CLIENT_PATCH_GAME_NATIVE_API __declspec(dllexport)
#else
#define CLIENT_PATCH_GAME_NATIVE_API
#endif

#ifdef __cplusplus
extern "C"
{
#endif

enum
{
    CLIENT_PATCH_GAME_NATIVE_API_VERSION = 1,
    CLIENT_PATCH_GAME_NATIVE_MOUNT_STRICT = 0x00000001,
    CLIENT_PATCH_WINDOW_ID_EQUIPMENT = 119,
    CLIENT_PATCH_WINDOW_ID_MID_POPUP = 364,
    CLIENT_PATCH_WINDOW_ID_GAME_MENU = 724,
    CLIENT_PATCH_WINDOW_ID_ABOVE_MENU = 834,
    CLIENT_PATCH_WINDOW_ID_PLUGIN_POPUP_TOP_ANCHOR = 837,
};

typedef BOOL (*ClientPatchGameNativeMountFn)(
    LPCWSTR virtualPath, LPCWSTR diskPath, DWORD flags);
typedef BOOL (*ClientPatchGameNativeUnmountFn)(LPCWSTR virtualPath);
typedef BOOL (*ClientPatchReserveWindowIdsFn)(LPCWSTR owner, DWORD count,
    INT afterWindowId, INT* windowIds);
typedef BOOL (*ClientPatchReleaseWindowIdsFn)(LPCWSTR owner);
typedef void* (__cdecl *ClientPatchWindowFactoryFn)(INT windowId,
    void* manager, void* context);
typedef BOOL (*ClientPatchRegisterWindowFactoryFn)(LPCWSTR owner,
    ClientPatchWindowFactoryFn factory, void* context);
typedef BOOL (*ClientPatchUnregisterWindowFactoryFn)(LPCWSTR owner);
typedef BOOL (*ClientPatchChatReadyFn)();
typedef BOOL (*ClientPatchPostChatNoticeFn)(LPCWSTR text, INT color);
typedef BOOL (*ClientPatchPostLoadNoticeFn)(LPCWSTR text, INT color);
typedef INT (*ClientPatchChatRgbFn)(INT red, INT green, INT blue);
typedef void (*ClientPatchRefreshPluginClipsFn)();

typedef struct ClientPatchGameNativeApi
{
    DWORD cbSize;
    DWORD version;
    ClientPatchGameNativeMountFn mount;
    ClientPatchGameNativeUnmountFn unmount;
    ClientPatchReserveWindowIdsFn reserveWindowIds;
    ClientPatchReleaseWindowIdsFn releaseWindowIds;
    ClientPatchRegisterWindowFactoryFn registerWindowFactory;
    ClientPatchUnregisterWindowFactoryFn unregisterWindowFactory;
    ClientPatchChatReadyFn chatReady;
    ClientPatchPostChatNoticeFn postChatNotice;
    ClientPatchChatRgbFn chatRgb;
    ClientPatchRefreshPluginClipsFn refreshPluginClips;
    ClientPatchPostLoadNoticeFn postLoadNotice;
} ClientPatchGameNativeApi;

CLIENT_PATCH_GAME_NATIVE_API BOOL ClientPatchGetGameNativeApi(
    DWORD requestedVersion, ClientPatchGameNativeApi* api);

#ifdef __cplusplus
}

inline bool ClientPatchBindGameNative(ClientPatchGameNativeApi* api)
{
    if (!api)
        return false;

    const HMODULE host = GetModuleHandleW(L"GameNative.dll");
    if (!host)
        return false;

    using GetApiFn = BOOL(*)(DWORD, ClientPatchGameNativeApi*);
    const auto getApi = reinterpret_cast<GetApiFn>(
        GetProcAddress(host, "ClientPatchGetGameNativeApi"));
    if (!getApi)
        return false;

    api->cbSize = sizeof(*api);
    return getApi(CLIENT_PATCH_GAME_NATIVE_API_VERSION, api) != FALSE;
}
#endif

#undef CLIENT_PATCH_GAME_NATIVE_API
