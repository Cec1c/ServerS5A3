#define NOMINMAX
#include "../S5A3Bridge.h"
#include <windows.h>
#include <gdiplus.h>

#include <algorithm>
#include <array>
#include <cstdarg>
#include <cstdint>
#include <cstdio>
#include <cwctype>
#include <limits>
#include <memory>
#include <string>
#include <utility>
#include <vector>

#include "resource.h"
#include "../GameNative/GameNativeApi.h"

#pragma comment(lib, "gdiplus.lib")
#pragma comment(lib, "ole32.lib")

namespace
{
constexpr wchar_t kOverlayClass[] = L"ClientPatchDpsMeterOverlay";
constexpr wchar_t kConfigFileName[] = L"DpsMeter.ini";
constexpr wchar_t kLogFileName[] = L"DpsMeter.log";

constexpr uintptr_t kPreferredImageBase = 0x00400000;
// A21 伤害聚合：thiscall(record, name, int64 damage, int timestamp)，ret 0x10。
constexpr uintptr_t kDamageAggregatorAddress = 0x01054030;
constexpr uintptr_t kDamageAggregatorHandlerAddress = 0x02EC1F18;
constexpr uintptr_t kSecurityCookieAddress = 0x0399CEE0;
constexpr uintptr_t kCurrentCharacterGlobal = 0x03B3DFB8;
constexpr uintptr_t kGetCharacterName = 0x018A8030;
// 当前角色职业：19E3290(*03A5C980) → 27C6F20(user)=user+0x14 → [info+4]。
constexpr uintptr_t kUserManagerGlobal = 0x03A5C980;
// 组队头像栏：*03A5C990 + 0x30 为 8 个 user*；user+0x14/0x18 为姓名/基础职业。
constexpr uintptr_t kPartyFaceBarGlobal = 0x03A5C990;
constexpr uintptr_t kGetLocalUser = 0x019E3290;
constexpr uintptr_t kGetUserInfo = 0x027C6F20;
constexpr size_t kUserInfoJobOffset = 4;
constexpr size_t kPartyFaceBarUserOffset = 0x30;
constexpr size_t kPartyUserNameOffset = 0x14;
constexpr size_t kPartyUserJobOffset = 0x18;
constexpr int kOfficialJobMax = 0x0D;
constexpr uintptr_t kDamageAggregatorCallAddress = 0x0105454B;
constexpr uintptr_t kDamageAggregatorTailAddress = 0x01054180;
constexpr size_t kHookLength = 5;

constexpr UINT kWindowHotkeyId = 811;
constexpr UINT_PTR kOverlayTimerId = 1;
constexpr UINT_PTR kClickTimerId = 2;
constexpr UINT kOverlayRefreshMessage = WM_APP + 0x31;
constexpr UINT kOverlayRefreshMilliseconds = 100;
constexpr UINT kClickPollMilliseconds = 16;
constexpr size_t kMaximumPlayers = 8;
constexpr size_t kVisiblePlayers = 8;
constexpr size_t kMaximumNameLength = 63;

// Official Style3 assets are authored at 2x. Layout is 60% of the old
// 50%-asset measurements so 1600x900 matches the smaller default size.
constexpr int kBasePanelWidth = 157;
constexpr int kFrameCapHeight = 12;
constexpr int kRowLeft = 7;
constexpr int kRowTop = 18;
constexpr int kRowWidth = 144;
constexpr int kRowHeight = 14;
constexpr int kRowGap = 5;
constexpr int kBottomPadding = 7;
constexpr int kBarStartWidth = 7;
constexpr int kBarEndWidth = 6;
constexpr int kBarBodyWidth = 131;
constexpr float kOfficialAssetScale = 0.30f;
constexpr int kDesignWidth = 1600;
constexpr int kDesignHeight = 900;
constexpr int kSixteenNineRefW = kDesignWidth;
constexpr int kSixteenNineRefH = kDesignHeight;
constexpr int kSixteenTenRefW = 1440;
constexpr int kSixteenTenRefH = kDesignHeight;
constexpr int kFourThreeRefW = 1333;
constexpr int kFourThreeRefH = 1000;
constexpr size_t kThemeCount = 18;
constexpr uint32_t kUniformThemeIndex = 0;

uint32_t ProfessionToTheme(int profession)
{
    // Style3 themes follow base-job IDs. Grow/awakening stages keep the
    // base job's color; the 18 themes come from a newer client job roster.
    static constexpr uint32_t kOfficialProfessionThemes[kThemeCount] = {
        0, 3, 5, 1, 9, 2, 10, 4, 8,
        14, 13, 7, 17, 11, 15, 6, 12, 16
    };
    return profession >= 0 && profession < static_cast<int>(kThemeCount)
        ? kOfficialProfessionThemes[profession] : kUniformThemeIndex;
}

using DamageAggregatorFn = int(__thiscall*)(void* record, void* name,
    int64_t damage, int timestamp);

struct HotkeyBinding
{
    UINT modifiers = 0;
    UINT virtualKey = 0;

    bool IsValid() const
    {
        return virtualKey != 0;
    }
};

struct PluginConfig
{
    bool enabled = false;
    bool debug = false;
    bool clickThrough = false;
    bool autoPosition = true;
    int offsetX = 24;
    int offsetY = 120;
    int scale = 100;
    HotkeyBinding windowHotkey;
};

struct PlayerStat
{
    wchar_t name[kMaximumNameLength + 1] = {};
    uint64_t damage = 0;
    uint32_t hits = 0;
    uint32_t theme = 0;
    int profession = -1;
};

struct RoundStats
{
    void* record = nullptr;
    int lastTimestamp = -1;
    uint32_t elapsedMilliseconds = 0;
    uint64_t totalDamage = 0;
    uint64_t generation = 0;
    size_t playerCount = 0;
    PlayerStat players[kMaximumPlayers] = {};
};

struct StatsSnapshot
{
    uint32_t elapsedMilliseconds = 0;
    uint64_t totalDamage = 0;
    uint64_t generation = 0;
    size_t playerCount = 0;
    PlayerStat players[kMaximumPlayers] = {};
};

struct OverlayFrame
{
    std::vector<unsigned char> pixels;
    int width = 0;
    int height = 0;
    int x = 0;
    int y = 0;
    uint64_t generation = 0;
    bool ready = false;
};

struct RoleIdentity
{
    wchar_t name[kMaximumNameLength + 1] = {};
    int profession = -1;
};

struct EmbeddedPng
{
    IStream* stream = nullptr;
    Gdiplus::Bitmap* bitmap = nullptr;
};

struct OfficialTheme
{
    EmbeddedPng row;
    EmbeddedPng start;
    EmbeddedPng middle;
    EmbeddedPng end;
    EmbeddedPng fullEnd;
    EmbeddedPng shortParts[4];
};

struct OfficialAssets
{
    EmbeddedPng top;
    EmbeddedPng body;
    EmbeddedPng bottom;
    EmbeddedPng gloss;
    OfficialTheme themes[kThemeCount];
};

bool TryReadRoleName(void* character,
    wchar_t (&name)[kMaximumNameLength + 1]);

HMODULE g_module = nullptr;
std::wstring g_moduleDirectory;
std::wstring g_configPath;
std::wstring g_logPath;
PluginConfig g_config;
HWND g_overlayWindow = nullptr;
HWND g_gameWindow = nullptr;
LONG g_started = 0;
LONG g_overlayStartupResult = 0;
HANDLE g_overlayReadyEvent = nullptr;
bool g_userVisible = true;
volatile LONG g_s5DungeonRun=0,g_s5ToggleRequests=0;
LONG g_s5VisibleRun=0;
ClientPatchGameNativeApi g_native = {};
bool g_nativeBound = false;
bool g_nativeMissLogged = false;
bool g_loadNoticeSent = false;
bool g_dragging = false;
bool g_leftButtonWasDown = false;
POINT g_dragStartCursor = {};
RECT g_dragStartWindow = {};
int g_lastCursorX = std::numeric_limits<int>::min();
int g_lastCursorY = std::numeric_limits<int>::min();
bool g_overlayWasVisible = false;
int g_gameClientWidth = 0;
int g_gameClientHeight = 0;
float g_renderScale = 1.0f;
uint64_t g_lastRenderedGeneration = std::numeric_limits<uint64_t>::max();
size_t g_lastRenderedPlayerCount = 0;
int g_lastRenderedWidth = 0;
int g_lastRenderedHeight = 0;
float g_lastRenderedScale = 0.0f;
int g_lastWindowX = 0;
int g_lastWindowY = 0;
int g_lastWindowWidth = 0;
int g_lastWindowHeight = 0;
enum class OverlayState
{
    Starting,
    UserHidden,
    NoData,
    GameWindowMissing,
    GameNotForeground,
    Visible
};
OverlayState g_overlayState = OverlayState::Starting;
SRWLOCK g_logLock = SRWLOCK_INIT;
SRWLOCK g_statsLock = SRWLOCK_INIT;
SRWLOCK g_frameLock = SRWLOCK_INIT;
SRWLOCK g_roleLock = SRWLOCK_INIT;
SRWLOCK g_characterLock = SRWLOCK_INIT;
RoundStats g_stats;
OverlayFrame g_frame;
RoleIdentity g_roles[kMaximumPlayers] = {};
size_t g_roleCount = 0;
void* g_currentCharacter = nullptr;
bool g_haveCurrentCharacter = false;
wchar_t g_currentCharacterName[kMaximumNameLength + 1] = {};
void* g_observedCharacter = nullptr;
bool g_observedCharacterInitialized = false;
DamageAggregatorFn g_originalDamageAggregator = nullptr;
void* g_damageTrampoline = nullptr;
std::array<unsigned char, kHookLength> g_originalHookBytes = {};
ULONG_PTR g_gdiplusToken = 0;
OfficialAssets g_assets;

uintptr_t ClientAddress(uintptr_t preferredAddress)
{
    const uintptr_t base = reinterpret_cast<uintptr_t>(
        GetModuleHandleW(nullptr));
    return base + (preferredAddress - kPreferredImageBase);
}

int Scale(int value)
{
    return (std::max)(1, static_cast<int>(
        static_cast<float>(value) * g_renderScale + 0.5f));
}

int ScaleX(int value)
{
    return Scale(value);
}

std::wstring ModuleDirectory(HMODULE module)
{
    wchar_t path[MAX_PATH] = {};
    const DWORD length = GetModuleFileNameW(module, path, _countof(path));
    if (!length || length >= _countof(path))
        return L".";
    std::wstring result(path, length);
    const size_t slash = result.find_last_of(L"\\/");
    return slash == std::wstring::npos ? L"." : result.substr(0, slash);
}

void Log(const wchar_t* format, ...)
{
    if (!g_config.debug)
        return;

    wchar_t message[1024] = {};
    va_list arguments;
    va_start(arguments, format);
    _vsnwprintf_s(message, _countof(message), _TRUNCATE, format, arguments);
    va_end(arguments);

    AcquireSRWLockExclusive(&g_logLock);
    FILE* file = nullptr;
    _wfopen_s(&file, g_logPath.c_str(), L"a, ccs=UTF-8");
    if (file)
    {
        SYSTEMTIME now = {};
        GetLocalTime(&now);
        fwprintf(file, L"[%02u:%02u:%02u.%03u] %s\n", now.wHour,
            now.wMinute, now.wSecond, now.wMilliseconds, message);
        fclose(file);
    }
    ReleaseSRWLockExclusive(&g_logLock);
}

bool EnsureGameNative()
{
    return false;
}

INT NoticeColor()
{
    return g_native.chatRgb ? g_native.chatRgb(255, 168, 72) : 0;
}

void QueueOfficialNotice(const wchar_t* text)
{
    (void)text;
}

void RequestLoadNotice()
{

}

bool IsReadableRange(const void* pointer, size_t length)
{
    if (!pointer || !length)
        return false;

    uintptr_t cursor = reinterpret_cast<uintptr_t>(pointer);
    if (cursor > std::numeric_limits<uintptr_t>::max() - length)
        return false;
    const uintptr_t end = cursor + length;
    while (cursor < end)
    {
        MEMORY_BASIC_INFORMATION memory = {};
        if (!VirtualQuery(reinterpret_cast<const void*>(cursor), &memory,
                sizeof(memory)) || memory.State != MEM_COMMIT ||
            (memory.Protect & (PAGE_GUARD | PAGE_NOACCESS)))
            return false;

        const uintptr_t regionStart =
            reinterpret_cast<uintptr_t>(memory.BaseAddress);
        if (regionStart > std::numeric_limits<uintptr_t>::max() -
                memory.RegionSize)
            return false;
        const uintptr_t regionEnd = regionStart + memory.RegionSize;
        if (regionEnd <= cursor)
            return false;
        cursor = (std::min)(end, regionEnd);
    }
    return true;
}

bool IsClientRange(uintptr_t address, size_t length)
{
    MEMORY_BASIC_INFORMATION memory = {};
    return IsReadableRange(reinterpret_cast<const void*>(address), length) &&
        VirtualQuery(reinterpret_cast<const void*>(address), &memory,
            sizeof(memory)) &&
        memory.AllocationBase == GetModuleHandleW(nullptr);
}

template <size_t Size>
bool ValidateBytes(uintptr_t preferredAddress,
    const std::array<unsigned char, Size>& expected)
{
    const uintptr_t address = ClientAddress(preferredAddress);
    if (!IsClientRange(address, expected.size()))
        return false;
    const auto* bytes = reinterpret_cast<const unsigned char*>(address);
    return std::equal(expected.begin(), expected.end(), bytes);
}

bool ValidateRelocatedOperand(uintptr_t preferredInstructionAddress,
    size_t operandOffset, uintptr_t preferredTargetAddress)
{
    const uintptr_t instruction = ClientAddress(preferredInstructionAddress);
    if (!IsClientRange(instruction + operandOffset, sizeof(uint32_t)))
        return false;
    const uint32_t actual = *reinterpret_cast<const uint32_t*>(
        instruction + operandOffset);
    return actual == static_cast<uint32_t>(
        ClientAddress(preferredTargetAddress));
}

bool ValidateClient()
{
    return true; // admitted only by the exact-image S5A3 loader
}

uint64_t SaturatingAdd(uint64_t left, uint64_t right)
{
    return right > std::numeric_limits<uint64_t>::max() - left
        ? std::numeric_limits<uint64_t>::max()
        : left + right;
}

void ResetRoundLocked(void* record)
{
    const uint64_t nextGeneration = g_stats.generation + 1;
    ZeroMemory(&g_stats, sizeof(g_stats));
    g_stats.record = record;
    g_stats.lastTimestamp = -1;
    g_stats.generation = nextGeneration;
}

void InvalidateOverlayFrame()
{
    AcquireSRWLockExclusive(&g_frameLock);
    g_frame.ready = false;
    g_frame.generation = std::numeric_limits<uint64_t>::max();
    g_frame.pixels.clear();
    ReleaseSRWLockExclusive(&g_frameLock);
}

void ResetForCharacterChange()
{
    AcquireSRWLockExclusive(&g_statsLock);
    ResetRoundLocked(nullptr);
    ReleaseSRWLockExclusive(&g_statsLock);
    InvalidateOverlayFrame();
    if (g_overlayWindow)
        PostMessageW(g_overlayWindow, kOverlayRefreshMessage, 0, 0);
}

void* ReadCurrentCharacterPointer()
{
    const uintptr_t global = ClientAddress(kCurrentCharacterGlobal);
    void* character = nullptr;
    if (IsClientRange(global, sizeof(uint32_t)))
    {
        __try
        {
            character = *reinterpret_cast<void**>(global);
        }
        __except (EXCEPTION_EXECUTE_HANDLER)
        {
            character = nullptr;
        }
    }
    return character;
}

void PollCurrentCharacterPointer()
{

}

void PollCurrentCharacter()
{

}

bool TryReadClientName(void* object,
    wchar_t (&name)[kMaximumNameLength + 1], size_t& length)
{
    return false;
}

using CharacterNameFn = const wchar_t*(__thiscall*)(void* character);
using GetLocalUserFn = void*(__thiscall*)(void* manager);
using GetUserInfoFn = void*(__thiscall*)(void* user);

bool TryCopyRoleName(const wchar_t* source,
    wchar_t (&name)[kMaximumNameLength + 1])
{
    ZeroMemory(name, sizeof(name));
    if (!source || !IsReadableRange(source, sizeof(wchar_t)))
        return false;

    size_t length = 0;
    __try
    {
        while (length < kMaximumNameLength && source[length])
        {
            const wchar_t value = source[length];
            if (value < 0x20 && value != L' ')
                return false;
            name[length++] = value;
        }
        if (!length ||
            (length == kMaximumNameLength && source[length]))
            return false;
    }
    __except (EXCEPTION_EXECUTE_HANDLER)
    {
        name[0] = L'\0';
        return false;
    }
    name[length] = L'\0';
    return true;
}

bool TryReadRoleName(void* character,
    wchar_t (&name)[kMaximumNameLength + 1])
{
    return false;
}

int TryReadRoleProfession(void* character)
{
    return -1;
}

bool TryReadPartyRole(void* user, RoleIdentity& role)
{
    return false;
}

void MergeRoleIdentity(RoleIdentity (&roles)[kMaximumPlayers],
    size_t& roleCount, const RoleIdentity& candidate)
{
    for (size_t index = 0; index < roleCount; ++index)
    {
        if (wcscmp(roles[index].name, candidate.name) != 0)
            continue;
        if (roles[index].profession < 0 && candidate.profession >= 0)
            roles[index].profession = candidate.profession;
        return;
    }
    if (roleCount < kMaximumPlayers)
        roles[roleCount++] = candidate;
}

void CollectPartyRoles(RoleIdentity (&roles)[kMaximumPlayers],
    size_t& roleCount)
{
    const uintptr_t global = ClientAddress(kPartyFaceBarGlobal);
    if (!IsClientRange(global, sizeof(void*)))
        return;

    void* faceBar = nullptr;
    __try
    {
        faceBar = *reinterpret_cast<void**>(global);
    }
    __except (EXCEPTION_EXECUTE_HANDLER)
    {
        return;
    }
    if (!faceBar || !IsReadableRange(
            static_cast<const unsigned char*>(faceBar) +
                kPartyFaceBarUserOffset,
            kMaximumPlayers * sizeof(void*)))
        return;

    for (size_t slot = 0; slot < kMaximumPlayers; ++slot)
    {
        void* user = nullptr;
        __try
        {
            const auto* bytes = static_cast<const unsigned char*>(faceBar);
            user = *reinterpret_cast<void* const*>(
                bytes + kPartyFaceBarUserOffset + slot * sizeof(void*));
        }
        __except (EXCEPTION_EXECUTE_HANDLER)
        {
            continue;
        }

        RoleIdentity role;
        if (TryReadPartyRole(user, role))
            MergeRoleIdentity(roles, roleCount, role);
    }
}

void PublishRoleIdentities(const RoleIdentity (&roles)[kMaximumPlayers],
    size_t roleCount)
{
    static RoleIdentity lastLogged[kMaximumPlayers] = {};
    static size_t lastLoggedCount = 0;
    bool changed = roleCount != lastLoggedCount;
    for (size_t index = 0; !changed && index < roleCount; ++index)
    {
        changed = roles[index].profession != lastLogged[index].profession ||
            wcscmp(roles[index].name, lastLogged[index].name) != 0;
    }
    if (changed)
    {
        for (size_t index = 0; index < roleCount; ++index)
            Log(L"[roles] index=%u name=%s profession=%d theme=%u",
                static_cast<unsigned>(index), roles[index].name,
                roles[index].profession,
                ProfessionToTheme(roles[index].profession));
        memcpy(lastLogged, roles, sizeof(lastLogged));
        lastLoggedCount = roleCount;
    }

    AcquireSRWLockExclusive(&g_roleLock);
    memcpy(g_roles, roles, sizeof(g_roles));
    g_roleCount = roleCount;
    ReleaseSRWLockExclusive(&g_roleLock);

    AcquireSRWLockExclusive(&g_statsLock);
    bool themeChanged = false;
    for (size_t index = 0; index < g_stats.playerCount; ++index)
    {
        for (size_t role = 0; role < roleCount; ++role)
        {
            if (wcscmp(g_stats.players[index].name, roles[role].name) != 0)
                continue;
            if (roles[role].profession < 0 ||
                g_stats.players[index].profession == roles[role].profession)
                break;
            g_stats.players[index].profession = roles[role].profession;
            g_stats.players[index].theme = ProfessionToTheme(
                roles[role].profession);
            themeChanged = true;
            break;
        }
    }
    if (themeChanged)
        ++g_stats.generation;
    ReleaseSRWLockExclusive(&g_statsLock);
}

void RefreshRoleIdentities()
{

}

int ProfessionForName(const wchar_t* name)
{
    int profession = -1;
    AcquireSRWLockShared(&g_roleLock);
    for (size_t index = 0; index < g_roleCount; ++index)
    {
        if (wcscmp(g_roles[index].name, name) == 0)
        {
            profession = g_roles[index].profession;
            break;
        }
    }
    ReleaseSRWLockShared(&g_roleLock);
    return profession;
}

void RecordDamage(void* record, const wchar_t* name, uint64_t damage,
    int timestamp)
{
    if (!record || !name || !*name || !damage)
        return;

    AcquireSRWLockExclusive(&g_statsLock);
    if (g_stats.record != record ||
        (timestamp >= 0 && g_stats.lastTimestamp >= 0 &&
            timestamp < g_stats.lastTimestamp))
    {
        ResetRoundLocked(record);
    }

    PlayerStat* player = nullptr;
    for (size_t index = 0; index < g_stats.playerCount; ++index)
    {
        if (wcscmp(g_stats.players[index].name, name) == 0)
        {
            player = &g_stats.players[index];
            break;
        }
    }
    if (!player && g_stats.playerCount < kMaximumPlayers)
    {
        player = &g_stats.players[g_stats.playerCount++];
        wcsncpy_s(player->name, name, _TRUNCATE);
        player->profession = ProfessionForName(name);
        player->theme = ProfessionToTheme(player->profession);
        Log(L"[stats] player added: name=%s theme=%u", player->name,
            player->theme);
    }

    if (player)
    {
        if (player->profession < 0)
        {
            player->profession = ProfessionForName(player->name);
            if (player->profession >= 0)
                player->theme = ProfessionToTheme(player->profession);
        }
        player->damage = SaturatingAdd(player->damage, damage);
        if (player->hits != std::numeric_limits<uint32_t>::max())
            ++player->hits;
        g_stats.totalDamage = SaturatingAdd(g_stats.totalDamage, damage);
        if (timestamp >= 0)
        {
            g_stats.lastTimestamp = timestamp;
            g_stats.elapsedMilliseconds = (std::max)(
                g_stats.elapsedMilliseconds,
                static_cast<uint32_t>(timestamp));
        }
        ++g_stats.generation;
    }
    ReleaseSRWLockExclusive(&g_statsLock);
}

int __fastcall HookDamageAggregator(void* record, void*, void* nameObject,
    int64_t damage, int timestamp)
{
    const int result = g_originalDamageAggregator(
        record, nameObject, damage, timestamp);
    if (damage <= 0)
        return result;

    wchar_t name[kMaximumNameLength + 1] = {};
    size_t nameLength = 0;
    if (TryReadClientName(nameObject, name, nameLength) && nameLength)
        RecordDamage(record, name, static_cast<uint64_t>(damage), timestamp);
    return result;
}

bool InstallDamageHook()
{
    return true;
}

void RemoveDamageHook()
{

}

StatsSnapshot ReadStatsSnapshot()
{
    StatsSnapshot snapshot;
    AcquireSRWLockShared(&g_statsLock);
    snapshot.elapsedMilliseconds = g_stats.elapsedMilliseconds;
    snapshot.totalDamage = g_stats.totalDamage;
    snapshot.generation = g_stats.generation;
    snapshot.playerCount = g_stats.playerCount;
    for (size_t index = 0; index < g_stats.playerCount; ++index)
        snapshot.players[index] = g_stats.players[index];
    ReleaseSRWLockShared(&g_statsLock);

    std::sort(snapshot.players,
        snapshot.players + snapshot.playerCount,
        [](const PlayerStat& left, const PlayerStat& right)
        {
            return left.damage > right.damage;
        });
    return snapshot;
}

std::wstring Trim(const std::wstring& value)
{
    size_t first = 0;
    while (first < value.size() && iswspace(value[first]))
        ++first;
    size_t last = value.size();
    while (last > first && iswspace(value[last - 1]))
        --last;
    return value.substr(first, last - first);
}

bool EqualsIgnoreCase(const std::wstring& left, const std::wstring& right)
{
    return _wcsicmp(left.c_str(), right.c_str()) == 0;
}

UINT VirtualKeyFromToken(const std::wstring& token)
{
    if (token.size() == 1)
    {
        const SHORT value = VkKeyScanW(token[0]);
        return value == -1 ? 0 : static_cast<UINT>(LOBYTE(value));
    }
    if (token.size() >= 2 && (token[0] == L'f' || token[0] == L'F'))
    {
        const int number = _wtoi(token.c_str() + 1);
        return number >= 1 && number <= 24 ? VK_F1 + number - 1 : 0;
    }
    if (EqualsIgnoreCase(token, L"Space")) return VK_SPACE;
    if (EqualsIgnoreCase(token, L"Enter")) return VK_RETURN;
    if (EqualsIgnoreCase(token, L"Tab")) return VK_TAB;
    if (EqualsIgnoreCase(token, L"Esc") ||
        EqualsIgnoreCase(token, L"Escape")) return VK_ESCAPE;
    if (EqualsIgnoreCase(token, L"Insert")) return VK_INSERT;
    if (EqualsIgnoreCase(token, L"Delete")) return VK_DELETE;
    if (EqualsIgnoreCase(token, L"Home")) return VK_HOME;
    if (EqualsIgnoreCase(token, L"End")) return VK_END;
    if (EqualsIgnoreCase(token, L"PageUp")) return VK_PRIOR;
    if (EqualsIgnoreCase(token, L"PageDown")) return VK_NEXT;
    if (EqualsIgnoreCase(token, L"Up")) return VK_UP;
    if (EqualsIgnoreCase(token, L"Down")) return VK_DOWN;
    if (EqualsIgnoreCase(token, L"Left")) return VK_LEFT;
    if (EqualsIgnoreCase(token, L"Right")) return VK_RIGHT;
    return 0;
}

HotkeyBinding ParseHotkey(const std::wstring& text, bool& valid)
{
    valid = false;
    HotkeyBinding result;
    const std::wstring value = Trim(text);
    if (value.empty() || EqualsIgnoreCase(value, L"None") ||
        EqualsIgnoreCase(value, L"Disabled"))
    {
        valid = true;
        return result;
    }

    size_t start = 0;
    while (start <= value.size())
    {
        const size_t end = value.find(L'+', start);
        const std::wstring token = Trim(value.substr(start,
            end == std::wstring::npos ? std::wstring::npos : end - start));
        if (EqualsIgnoreCase(token, L"Ctrl") ||
            EqualsIgnoreCase(token, L"Control"))
            result.modifiers |= MOD_CONTROL;
        else if (EqualsIgnoreCase(token, L"Alt"))
            result.modifiers |= MOD_ALT;
        else if (EqualsIgnoreCase(token, L"Shift"))
            result.modifiers |= MOD_SHIFT;
        else if (EqualsIgnoreCase(token, L"Win") ||
            EqualsIgnoreCase(token, L"Windows"))
            result.modifiers |= MOD_WIN;
        else if (!result.virtualKey)
            result.virtualKey = VirtualKeyFromToken(token);
        else
            return result;

        if (end == std::wstring::npos)
            break;
        start = end + 1;
    }
    valid = result.virtualKey != 0;
    return result;
}

BOOL CALLBACK FindGameWindowCallback(HWND window, LPARAM parameter)
{
    if (window == g_overlayWindow || !IsWindowVisible(window) ||
        IsIconic(window) || GetParent(window))
        return TRUE;

    DWORD processId = 0;
    GetWindowThreadProcessId(window, &processId);
    if (processId != GetCurrentProcessId())
        return TRUE;

    RECT rect = {};
    if (!GetWindowRect(window, &rect))
        return TRUE;
    const int64_t area = static_cast<int64_t>(rect.right - rect.left) *
        (rect.bottom - rect.top);
    auto* best = reinterpret_cast<std::pair<HWND, int64_t>*>(parameter);
    if (area > best->second)
        *best = { window, area };
    return TRUE;
}

HWND FindGameWindow()
{
    std::pair<HWND, int64_t> best = { nullptr, 0 };
    EnumWindows(FindGameWindowCallback, reinterpret_cast<LPARAM>(&best));
    return best.second >= 10000 ? best.first : nullptr;
}

bool BindOverlayOwner()
{
    if (!g_overlayWindow || !g_gameWindow)
        return false;
    if (GetWindow(g_overlayWindow, GW_OWNER) == g_gameWindow)
        return true;

    SetLastError(ERROR_SUCCESS);
    const LONG_PTR previousOwner = SetWindowLongPtrW(g_overlayWindow,
        GWLP_HWNDPARENT, reinterpret_cast<LONG_PTR>(g_gameWindow));
    const DWORD error = GetLastError();
    if (!previousOwner && error != ERROR_SUCCESS)
    {
        Log(L"[window] failed to bind overlay owner: game=%p error=%lu",
            g_gameWindow, static_cast<unsigned long>(error));
        return false;
    }

    Log(L"[window] overlay owner bound to game window: overlay=%p game=%p",
        g_overlayWindow, g_gameWindow);
    return true;
}

void UpdateRenderScale(int width, int height)
{
    int refW = kSixteenNineRefW;
    int refH = kSixteenNineRefH;
    const int aspect100 = height > 0 ? (width * 100) / height : 177;
    const wchar_t* aspectName = L"16:9";
    if (aspect100 < 147)
    {
        refW = kFourThreeRefW;
        refH = kFourThreeRefH;
        aspectName = L"4:3";
    }
    else if (aspect100 < 170)
    {
        refW = kSixteenTenRefW;
        refH = kSixteenTenRefH;
        aspectName = L"16:10";
    }

    const float byWidth = static_cast<float>(width) / static_cast<float>(refW);
    const float byHeight = static_cast<float>(height) / static_cast<float>(refH);
    const float resolution = (std::min)(byWidth, byHeight);
    const float signedOff = static_cast<float>(kDesignHeight) / 600.0f;
    g_renderScale = signedOff * resolution *
        static_cast<float>(g_config.scale) / 100.0f;

    static int lastWidth = 0;
    static int lastHeight = 0;
    static float lastScale = 0.0f;
    if (g_config.debug && (width != lastWidth || height != lastHeight ||
        g_renderScale != lastScale))
    {
        lastWidth = width;
        lastHeight = height;
        lastScale = g_renderScale;
        Log(L"[scale] %dx%d %s ref=%dx%d resolution=%.3f render=%.3f",
            width, height, aspectName, refW, refH, resolution, g_renderScale);
    }
}

bool TryGetGameClientOrigin(POINT& origin)
{
    const HWND selectedWindow = FindGameWindow();
    if (selectedWindow != g_gameWindow)
    {
        g_gameWindow = selectedWindow;
        if (g_gameWindow)
        {
            wchar_t className[128] = {};
            wchar_t title[256] = {};
            GetClassNameW(g_gameWindow, className, _countof(className));
            GetWindowTextW(g_gameWindow, title, _countof(title));
            RECT rect = {};
            GetWindowRect(g_gameWindow, &rect);
            Log(L"[window] selected game window: hwnd=%p class=%s "
                L"title=%s rect=%ld,%ld,%ld,%ld", g_gameWindow,
                className, title, rect.left, rect.top, rect.right,
                rect.bottom);
        }
        else
        {
            Log(L"[window] no visible top-level game window found");
        }
    }
    if (!g_gameWindow)
        return false;
    if (!BindOverlayOwner())
        return false;

    RECT client = {};
    origin = { 0, 0 };
    if (!IsWindowVisible(g_gameWindow) || IsIconic(g_gameWindow) ||
        !GetClientRect(g_gameWindow, &client) || client.right <= client.left ||
        client.bottom <= client.top || !ClientToScreen(g_gameWindow, &origin))
    {
        g_gameClientWidth = 0;
        g_gameClientHeight = 0;
        return false;
    }

    g_gameClientWidth = client.right - client.left;
    g_gameClientHeight = client.bottom - client.top;
    UpdateRenderScale(g_gameClientWidth, g_gameClientHeight);
    return true;
}

bool GameIsForeground()
{
    if (!g_gameWindow)
        return false;
    HWND foreground = GetForegroundWindow();
    DWORD foregroundProcess = 0;
    DWORD gameProcess = 0;
    GetWindowThreadProcessId(foreground, &foregroundProcess);
    GetWindowThreadProcessId(g_gameWindow, &gameProcess);
    return foreground && foregroundProcess == gameProcess;
}

void SetOverlayState(OverlayState state)
{
    if (g_overlayState == state)
        return;
    g_overlayState = state;
    switch (state)
    {
    case OverlayState::UserHidden:
        Log(L"[window] overlay hidden by user");
        break;
    case OverlayState::NoData:
        Log(L"[window] overlay hidden while DPS data is empty");
        break;
    case OverlayState::GameWindowMissing:
        Log(L"[window] overlay waiting for the game window");
        break;
    case OverlayState::GameNotForeground:
    {
        HWND foreground = GetForegroundWindow();
        DWORD processId = 0;
        GetWindowThreadProcessId(foreground, &processId);
        Log(L"[window] overlay hidden while game is not foreground: "
            L"foreground=%p pid=%lu", foreground,
            static_cast<unsigned long>(processId));
        break;
    }
    case OverlayState::Visible:
        Log(L"[window] overlay visible");
        break;
    default:
        break;
    }
}

void HideOverlayWindow(OverlayState state)
{
    if (g_overlayWindow)
        ShowWindow(g_overlayWindow, SW_HIDE);
    InvalidateOverlayFrame();
    g_overlayWasVisible = false;
    SetOverlayState(state);
}

std::wstring FormatCompact(uint64_t value)
{
    static constexpr const wchar_t* kSuffixes[] = {
        L"", L"K", L"M", L"G", L"T"
    };
    double display = static_cast<double>(value);
    size_t suffix = 0;
    while (display >= 1000.0 && suffix + 1 < _countof(kSuffixes))
    {
        display /= 1000.0;
        ++suffix;
    }

    wchar_t text[64] = {};
    if (!suffix)
        _snwprintf_s(text, _countof(text), _TRUNCATE, L"%.0f", display);
    else
        _snwprintf_s(text, _countof(text), _TRUNCATE, L"%.2f%s",
            display, kSuffixes[suffix]);
    return text;
}

bool LoadEmbeddedPng(int resourceId, EmbeddedPng& result)
{
    HRSRC resource = FindResourceW(g_module, MAKEINTRESOURCEW(resourceId),
        RT_RCDATA);
    HGLOBAL loaded = resource ? LoadResource(g_module, resource) : nullptr;
    const DWORD size = resource ? SizeofResource(g_module, resource) : 0;
    const void* source = loaded ? LockResource(loaded) : nullptr;
    if (!source || !size)
        return false;

    HGLOBAL copy = GlobalAlloc(GMEM_MOVEABLE, size);
    if (!copy)
        return false;
    void* destination = GlobalLock(copy);
    if (!destination)
    {
        GlobalFree(copy);
        return false;
    }
    memcpy(destination, source, size);
    GlobalUnlock(copy);

    IStream* stream = nullptr;
    if (CreateStreamOnHGlobal(copy, TRUE, &stream) != S_OK)
    {
        GlobalFree(copy);
        return false;
    }
    auto* bitmap = Gdiplus::Bitmap::FromStream(stream, FALSE);
    if (!bitmap || bitmap->GetLastStatus() != Gdiplus::Ok)
    {
        delete bitmap;
        stream->Release();
        return false;
    }
    result.stream = stream;
    result.bitmap = bitmap;
    return true;
}

void ReleaseEmbeddedPng(EmbeddedPng& image)
{
    delete image.bitmap;
    image.bitmap = nullptr;
    if (image.stream)
        image.stream->Release();
    image.stream = nullptr;
}

void ShutdownOfficialAssets();

bool InitializeOfficialAssets()
{
    Gdiplus::GdiplusStartupInput startupInput;
    if (Gdiplus::GdiplusStartup(&g_gdiplusToken, &startupInput, nullptr) !=
        Gdiplus::Ok)
        return false;

    static constexpr int kThemeResourceIds[kThemeCount][9] = {
        {201, 205, 209, 213, 217, 301, 302, 303, 304},
        {202, 206, 210, 214, 218, 305, 306, 307, 308},
        {203, 207, 211, 215, 219, 309, 310, 311, 312},
        {204, 208, 212, 216, 220, 313, 314, 315, 316},
        {222, 226, 230, 234, 238, 317, 318, 319, 320},
        {223, 227, 231, 235, 239, 321, 322, 323, 324},
        {224, 228, 232, 236, 240, 325, 326, 327, 328},
        {225, 229, 233, 237, 241, 329, 330, 331, 332},
        {243, 250, 257, 264, 271, 333, 334, 335, 336},
        {244, 251, 258, 265, 272, 337, 338, 339, 340},
        {245, 252, 259, 266, 273, 341, 342, 343, 344},
        {246, 253, 260, 267, 274, 345, 346, 347, 348},
        {247, 254, 261, 268, 275, 349, 350, 351, 352},
        {248, 255, 262, 269, 276, 353, 354, 355, 356},
        {249, 256, 263, 270, 277, 357, 358, 359, 360},
        {280, 283, 286, 289, 292, 361, 362, 363, 364},
        {281, 284, 287, 290, 293, 365, 366, 367, 368},
        {282, 285, 288, 291, 294, 369, 370, 371, 372},
    };
    bool loaded = LoadEmbeddedPng(IDR_DPS_TOP, g_assets.top) &&
        LoadEmbeddedPng(IDR_DPS_BODY, g_assets.body) &&
        LoadEmbeddedPng(IDR_DPS_BOTTOM, g_assets.bottom) &&
        LoadEmbeddedPng(IDR_DPS_GLOSS, g_assets.gloss);
    for (size_t themeIndex = 0; loaded && themeIndex < kThemeCount;
        ++themeIndex)
    {
        OfficialTheme& theme = g_assets.themes[themeIndex];
        const int* ids = kThemeResourceIds[themeIndex];
        loaded = LoadEmbeddedPng(ids[0], theme.row) &&
            LoadEmbeddedPng(ids[1], theme.start) &&
            LoadEmbeddedPng(ids[2], theme.middle) &&
            LoadEmbeddedPng(ids[3], theme.end) &&
            LoadEmbeddedPng(ids[4], theme.fullEnd);
        for (size_t partIndex = 0; loaded && partIndex < 4; ++partIndex)
            loaded = LoadEmbeddedPng(ids[5 + partIndex],
                theme.shortParts[partIndex]);
    }
    if (!loaded)
    {
        ShutdownOfficialAssets();
    }
    return loaded;
}

void ShutdownOfficialAssets()
{
    ReleaseEmbeddedPng(g_assets.top);
    ReleaseEmbeddedPng(g_assets.body);
    ReleaseEmbeddedPng(g_assets.bottom);
    ReleaseEmbeddedPng(g_assets.gloss);
    for (auto& theme : g_assets.themes)
    {
        ReleaseEmbeddedPng(theme.row);
        ReleaseEmbeddedPng(theme.start);
        ReleaseEmbeddedPng(theme.middle);
        ReleaseEmbeddedPng(theme.end);
        ReleaseEmbeddedPng(theme.fullEnd);
        for (auto& shortPart : theme.shortParts)
            ReleaseEmbeddedPng(shortPart);
    }
    if (g_gdiplusToken)
        Gdiplus::GdiplusShutdown(g_gdiplusToken);
    g_gdiplusToken = 0;
}

void DrawBitmap(Gdiplus::Graphics& graphics, const EmbeddedPng& image,
    const Gdiplus::RectF& destination, float sourceX = 0.0f,
    float sourceY = 0.0f, float sourceWidth = -1.0f,
    float sourceHeight = -1.0f)
{
    if (!image.bitmap)
        return;
    if (sourceWidth < 0.0f)
        sourceWidth = static_cast<float>(image.bitmap->GetWidth());
    if (sourceHeight < 0.0f)
        sourceHeight = static_cast<float>(image.bitmap->GetHeight());
    graphics.DrawImage(image.bitmap, destination, sourceX, sourceY,
        sourceWidth, sourceHeight, Gdiplus::UnitPixel);
}

bool DrawWithFont(Gdiplus::Graphics& graphics, const wchar_t* familyName,
    const wchar_t* text,
    float x, float y, float width, float height, float size,
    Gdiplus::StringAlignment alignment, Gdiplus::FontStyle style,
    const Gdiplus::Color& color, Gdiplus::StringAlignment lineAlignment,
    bool ellipsis)
{
    Gdiplus::FontFamily family(familyName);
    if (family.GetLastStatus() != Gdiplus::Ok || !family.IsAvailable())
        return false;
    Gdiplus::Font font(&family, size, style, Gdiplus::UnitPixel);
    if (font.GetLastStatus() != Gdiplus::Ok)
        return false;
    Gdiplus::StringFormat format(Gdiplus::StringFormat::GenericTypographic());
    format.SetAlignment(alignment);
    format.SetLineAlignment(lineAlignment);
    format.SetTrimming(ellipsis ? Gdiplus::StringTrimmingEllipsisCharacter
                                : Gdiplus::StringTrimmingNone);
    format.SetFormatFlags(Gdiplus::StringFormatFlagsNoWrap);
    Gdiplus::SolidBrush brush(color);
    return graphics.DrawString(text, -1, &font,
        Gdiplus::RectF(x, y, width, height), &format, &brush) == Gdiplus::Ok;
}

void DrawOfficialText(Gdiplus::Graphics& graphics, const wchar_t* text,
    float x, float y, float width, float height, float size,
    Gdiplus::StringAlignment alignment, Gdiplus::FontStyle style,
    const Gdiplus::Color& color,
    Gdiplus::StringAlignment lineAlignment = Gdiplus::StringAlignmentCenter,
    bool ellipsis = false)
{
    static constexpr const wchar_t* kFamilies[] = {
        L"Microsoft YaHei UI", L"Microsoft YaHei", L"SimSun"
    };
    for (const wchar_t* family : kFamilies)
    {
        if (DrawWithFont(graphics, family, text, x, y, width, height,
                size, alignment, style, color, lineAlignment, ellipsis))
            return;
    }

    const Gdiplus::FontFamily* family =
        Gdiplus::FontFamily::GenericSansSerif();
    if (!family)
        return;
    Gdiplus::Font font(family, size, style, Gdiplus::UnitPixel);
    if (font.GetLastStatus() != Gdiplus::Ok)
        return;
    Gdiplus::StringFormat format(Gdiplus::StringFormat::GenericTypographic());
    format.SetAlignment(alignment);
    format.SetLineAlignment(lineAlignment);
    format.SetTrimming(ellipsis ? Gdiplus::StringTrimmingEllipsisCharacter
                                : Gdiplus::StringTrimmingNone);
    format.SetFormatFlags(Gdiplus::StringFormatFlagsNoWrap);
    Gdiplus::SolidBrush brush(color);
    graphics.DrawString(text, -1, &font, Gdiplus::RectF(x, y, width, height),
        &format, &brush);
}

float ScaleF(float value)
{
    return value * g_renderScale;
}

float ScaleXF(float value)
{
    return ScaleF(value);
}

int BasePanelHeight(size_t rowCount)
{
    if (!rowCount)
        return 0;
    return kRowTop + static_cast<int>(rowCount) * kRowHeight +
        static_cast<int>(rowCount - 1) * kRowGap + kBottomPadding;
}

std::wstring FormatPercentage(double ratio)
{
    wchar_t text[32] = {};
    if (ratio <= 0.0)
    {
        wcscpy_s(text, L"0%");
    }
    else if (ratio < 0.001)
    {
        wcscpy_s(text, L"0.1%");
    }
    else
    {
        const double percent = ratio * 100.0;
        const double fraction = percent - static_cast<int>(percent);
        if (fraction < 0.05 || fraction > 0.95)
        {
            const int rounded = static_cast<int>(percent) +
                (fraction > 0.5 ? 1 : 0);
            _snwprintf_s(text, _countof(text), _TRUNCATE, L"%d%%", rounded);
        }
        else
        {
            _snwprintf_s(text, _countof(text), _TRUNCATE, L"%.1f%%",
                percent);
        }
    }
    return text;
}

void DrawNaturalHalfSize(Gdiplus::Graphics& graphics,
    const EmbeddedPng& image, float x, float rowY)
{
    if (!image.bitmap)
        return;
    const float width = ScaleXF(image.bitmap->GetWidth() * kOfficialAssetScale);
    const float height = ScaleF(image.bitmap->GetHeight() * kOfficialAssetScale);
    const float y = rowY + (ScaleF(static_cast<float>(kRowHeight)) -
        height) * 0.5f - ScaleF(1.2f);
    DrawBitmap(graphics, image, Gdiplus::RectF(x, y, width, height));
}

void DrawTransitionEnd(Gdiplus::Graphics& graphics,
    const EmbeddedPng& image, float x, float rowY)
{
    if (!image.bitmap)
        return;
    DrawBitmap(graphics, image, Gdiplus::RectF(x, rowY,
        ScaleXF(image.bitmap->GetWidth() * kOfficialAssetScale),
        ScaleF(static_cast<float>(kRowHeight))));
}

void DrawOfficialBar(Gdiplus::Graphics& graphics,
    const OfficialTheme& theme, float rowX, float rowY, double ratio)
{
    const float rowWidth = ScaleXF(static_cast<float>(kRowWidth));
    const float rowHeight = ScaleF(static_cast<float>(kRowHeight));
    DrawBitmap(graphics, theme.row,
        Gdiplus::RectF(rowX, rowY, rowWidth, rowHeight));

    const double clamped = (std::max)(0.0, (std::min)(1.0, ratio));
    if (clamped <= 0.0)
        return;
    if (clamped <= 0.02)
    {
        DrawNaturalHalfSize(graphics,
            theme.shortParts[clamped > 0.01 ? 1 : 0], rowX, rowY);
        return;
    }

    const float startWidth = ScaleXF(static_cast<float>(kBarStartWidth));
    const float endWidth = ScaleXF(static_cast<float>(kBarEndWidth));
    DrawBitmap(graphics, theme.start,
        Gdiplus::RectF(rowX, rowY, startWidth, rowHeight));
    if (clamped <= 0.03)
    {
        DrawBitmap(graphics, theme.end, Gdiplus::RectF(
            rowX + startWidth, rowY, endWidth, rowHeight));
        return;
    }

    double logicalBody = ((clamped - 0.03) / 0.97) * kBarBodyWidth;
    logicalBody = (std::max)(1.0,
        (std::min)(static_cast<double>(kBarBodyWidth), logicalBody));
    const float bodyWidth = ScaleXF(static_cast<float>(logicalBody));
    const float bodyX = rowX + startWidth;
    DrawBitmap(graphics, theme.middle,
        Gdiplus::RectF(bodyX, rowY, bodyWidth, rowHeight));

    const float endX = bodyX + bodyWidth;
    if (clamped > 0.9995)
    {
        DrawBitmap(graphics, theme.fullEnd, Gdiplus::RectF(
            endX, rowY, startWidth, rowHeight));
    }
    else if (clamped >= 0.98)
    {
        DrawTransitionEnd(graphics,
            theme.shortParts[clamped >= 0.99 ? 3 : 2], endX, rowY);
    }
    else
    {
        DrawBitmap(graphics, theme.end,
            Gdiplus::RectF(endX, rowY, endWidth, rowHeight));
    }

    if (g_assets.gloss.bitmap)
    {
        const float sourceWidth = static_cast<float>(
            g_assets.gloss.bitmap->GetWidth()) *
            static_cast<float>(logicalBody / kBarBodyWidth);
        DrawBitmap(graphics, g_assets.gloss,
            Gdiplus::RectF(bodyX, rowY, bodyWidth, rowHeight),
            0.0f, 0.0f, sourceWidth,
            static_cast<float>(g_assets.gloss.bitmap->GetHeight()));
    }
}

bool RenderOverlay(HWND window, int screenX, int screenY,
    const StatsSnapshot& snapshot)
{
    const size_t visibleCount = (std::min)(snapshot.playerCount,
        kVisiblePlayers);
    if (!visibleCount)
        return false;
    const int width = ScaleX(kBasePanelWidth);
    const int height = Scale(BasePanelHeight(visibleCount));

    HDC screen = GetDC(nullptr);
    HDC memory = screen ? CreateCompatibleDC(screen) : nullptr;
    if (!screen || !memory)
    {
        if (memory) DeleteDC(memory);
        if (screen) ReleaseDC(nullptr, screen);
        return false;
    }

    BITMAPINFO bitmapInfo = {};
    bitmapInfo.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bitmapInfo.bmiHeader.biWidth = width;
    bitmapInfo.bmiHeader.biHeight = -height;
    bitmapInfo.bmiHeader.biPlanes = 1;
    bitmapInfo.bmiHeader.biBitCount = 32;
    bitmapInfo.bmiHeader.biCompression = BI_RGB;
    void* pixels = nullptr;
    HBITMAP bitmap = CreateDIBSection(memory, &bitmapInfo, DIB_RGB_COLORS,
        &pixels, nullptr, 0);
    if (!bitmap || !pixels)
    {
        if (bitmap) DeleteObject(bitmap);
        DeleteDC(memory);
        ReleaseDC(nullptr, screen);
        return false;
    }

    HGDIOBJ oldBitmap = SelectObject(memory, bitmap);
    ZeroMemory(pixels, static_cast<size_t>(width) * height * 4);
    bool rendered = false;
    {
        Gdiplus::Bitmap surface(width, height, width * 4,
            PixelFormat32bppPARGB, static_cast<BYTE*>(pixels));
        if (surface.GetLastStatus() == Gdiplus::Ok)
        {
            Gdiplus::Graphics graphics(&surface);
            graphics.SetCompositingMode(Gdiplus::CompositingModeSourceOver);
            graphics.SetCompositingQuality(
                Gdiplus::CompositingQualityGammaCorrected);
            graphics.SetInterpolationMode(
                Gdiplus::InterpolationModeHighQualityBilinear);
            graphics.SetPixelOffsetMode(Gdiplus::PixelOffsetModeHalf);
            graphics.SetSmoothingMode(Gdiplus::SmoothingModeNone);
            graphics.SetTextRenderingHint(
                Gdiplus::TextRenderingHintAntiAliasGridFit);

            const float panelWidth = static_cast<float>(width);
            const float capHeight = ScaleF(
                static_cast<float>(kFrameCapHeight));
            const float rowHeight = ScaleF(static_cast<float>(kRowHeight));
            const float fontSize = (std::max)(5.0f, ScaleF(6.6f));
            const float headerFontSize = (std::max)(6.0f, ScaleF(7.8f));
            DrawBitmap(graphics, g_assets.top,
                Gdiplus::RectF(0.0f, 0.0f, panelWidth, capHeight));
            DrawBitmap(graphics, g_assets.body, Gdiplus::RectF(
                0.0f, capHeight, panelWidth,
                static_cast<float>(height) - capHeight * 2.0f));
            DrawBitmap(graphics, g_assets.bottom, Gdiplus::RectF(
                0.0f, static_cast<float>(height) - capHeight,
                panelWidth, capHeight));

            DrawOfficialText(graphics, L"DPS", ScaleXF(8.4f), 0.0f,
                ScaleXF(33.6f), capHeight, headerFontSize,
                Gdiplus::StringAlignmentNear, Gdiplus::FontStyleBold,
                Gdiplus::Color(255, 255, 255, 255));

            for (size_t index = 0; index < visibleCount; ++index)
            {
                const PlayerStat& player = snapshot.players[index];
                const float rowX = ScaleXF(static_cast<float>(kRowLeft));
                const float rowY = ScaleF(static_cast<float>(kRowTop +
                    static_cast<int>(index) * (kRowHeight + kRowGap)));
                const double ratio = snapshot.totalDamage
                    ? static_cast<double>(player.damage) /
                        static_cast<double>(snapshot.totalDamage)
                    : 0.0;
                const OfficialTheme& theme =
                    g_assets.themes[player.theme % kThemeCount];
                DrawOfficialBar(graphics, theme, rowX, rowY, ratio);

                wchar_t displayName[kMaximumNameLength + 3] = {};
                _snwprintf_s(displayName, _countof(displayName), _TRUNCATE,
                    L" %s", player.name);
                DrawOfficialText(graphics, displayName,
                    rowX + ScaleXF(4.2f), rowY,
                    ScaleXF(62.4f), rowHeight, fontSize,
                    Gdiplus::StringAlignmentNear, Gdiplus::FontStyleRegular,
                    Gdiplus::Color(255, 255, 255, 255),
                    Gdiplus::StringAlignmentCenter, true);

                const std::wstring damageText = FormatCompact(player.damage);
                const std::wstring percentageText = FormatPercentage(ratio);
                wchar_t detail[96] = {};
                _snwprintf_s(detail, _countof(detail), _TRUNCATE, L"%s,%s",
                    damageText.c_str(), percentageText.c_str());
                DrawOfficialText(graphics, detail, rowX + ScaleXF(67.2f),
                    rowY, ScaleXF(72.0f), rowHeight, fontSize,
                    Gdiplus::StringAlignmentFar, Gdiplus::FontStyleRegular,
                    Gdiplus::Color(255, 255, 255, 255),
                    Gdiplus::StringAlignmentCenter, false);
            }

            graphics.Flush(Gdiplus::FlushIntentionSync);
            POINT cursor = {};
            RECT overlay = { screenX, screenY, screenX + width,
                screenY + height };
            if (GetCursorPos(&cursor) && PtInRect(&overlay, cursor))
            {
                DrawIconEx(memory, cursor.x - screenX, cursor.y - screenY,
                    LoadCursorW(nullptr, IDC_ARROW), 0, 0, 0, nullptr,
                    DI_NORMAL);
            }
            rendered = true;
        }
    }
    if (!rendered)
    {
        SelectObject(memory, oldBitmap);
        DeleteObject(bitmap);
        DeleteDC(memory);
        ReleaseDC(nullptr, screen);
        return false;
    }
    AcquireSRWLockExclusive(&g_frameLock);
    g_frame.pixels.resize(static_cast<size_t>(width) * height * 4);
    memcpy(g_frame.pixels.data(), pixels,
        static_cast<size_t>(width) * height * 4);
    g_frame.width = width;
    g_frame.height = height;
    g_frame.x = screenX;
    g_frame.y = screenY;
    g_frame.generation = snapshot.generation;
    g_frame.ready = true;
    ReleaseSRWLockExclusive(&g_frameLock);

    POINT destination = { screenX, screenY };
    SIZE size = { width, height };
    POINT source = { 0, 0 };
    BLENDFUNCTION blend = { AC_SRC_OVER, 0, 255, AC_SRC_ALPHA };
    const BOOL updated = UpdateLayeredWindow(window, screen, &destination,
        &size, memory, &source, 0, &blend, ULW_ALPHA);

    SelectObject(memory, oldBitmap);
    DeleteObject(bitmap);
    DeleteDC(memory);
    ReleaseDC(nullptr, screen);
    if (updated != FALSE)
    {
        g_lastRenderedGeneration = snapshot.generation;
        g_lastRenderedPlayerCount = visibleCount;
        g_lastRenderedWidth = width;
        g_lastRenderedHeight = height;
        g_lastRenderedScale = g_renderScale;
    }
    return updated != FALSE;
}

void UpdateOverlayDragPosition();

void SaveWindowPosition()
{
    POINT gameOrigin = {};
    RECT overlay = {};
    if (!TryGetGameClientOrigin(gameOrigin) ||
        !GetWindowRect(g_overlayWindow, &overlay))
        return;

    g_config.offsetX = overlay.left - gameOrigin.x;
    g_config.offsetY = overlay.top - gameOrigin.y;
    g_config.autoPosition = false;
    WritePrivateProfileStringW(L"General", L"AutoPosition", L"0",
        g_configPath.c_str());
    wchar_t value[32] = {};
    _snwprintf_s(value, _countof(value), _TRUNCATE, L"%d",
        g_config.offsetX);
    WritePrivateProfileStringW(L"General", L"OffsetX", value,
        g_configPath.c_str());
    _snwprintf_s(value, _countof(value), _TRUNCATE, L"%d",
        g_config.offsetY);
    WritePrivateProfileStringW(L"General", L"OffsetY", value,
        g_configPath.c_str());
    Log(L"[window] position saved: %d,%d", g_config.offsetX,
        g_config.offsetY);
}

void PollOverlayDrag()
{
    if (!g_overlayWindow || !IsWindowVisible(g_overlayWindow) ||
        g_config.clickThrough)
        return;

    POINT cursor = {};
    RECT overlay = {};
    if (!GetCursorPos(&cursor) || !GetWindowRect(g_overlayWindow, &overlay))
        return;

    const bool down = (GetAsyncKeyState(VK_LBUTTON) & 0x8000) != 0;
    if (!g_dragging && down && !g_leftButtonWasDown &&
        PtInRect(&overlay, cursor))
    {
        g_dragging = true;
        g_dragStartCursor = cursor;
        g_dragStartWindow = overlay;
    }
    if (g_dragging && down)
        UpdateOverlayDragPosition();
    if (g_dragging && !down)
    {
        UpdateOverlayDragPosition();
        g_dragging = false;
        SaveWindowPosition();
    }
    g_leftButtonWasDown = down;
}

void UpdateOverlayDragPosition()
{
    POINT cursor = {};
    if (!GetCursorPos(&cursor))
        return;
    const int x = g_dragStartWindow.left +
        cursor.x - g_dragStartCursor.x;
    const int y = g_dragStartWindow.top +
        cursor.y - g_dragStartCursor.y;
    SetWindowPos(g_overlayWindow, HWND_TOP, x, y, 0, 0,
        SWP_NOSIZE | SWP_NOACTIVATE | SWP_NOZORDER);
    g_lastWindowX = x;
    g_lastWindowY = y;
    AcquireSRWLockExclusive(&g_frameLock);
    if (g_frame.ready)
    {
        g_frame.x = x;
        g_frame.y = y;
    }
    ReleaseSRWLockExclusive(&g_frameLock);
}

void ApplyDungeonVisibility()
{
    const LONG run=InterlockedCompareExchange(&g_s5DungeonRun,0,0);
    if(run!=g_s5VisibleRun){g_s5VisibleRun=run;if(run)g_userVisible=true;}
    if(InterlockedExchange(&g_s5ToggleRequests,0)&1)g_userVisible=!g_userVisible;
}
void UpdateOverlayWindow()
{
    ApplyDungeonVisibility();
    if(!g_s5VisibleRun){HideOverlayWindow(OverlayState::NoData);return;}
    if (!g_userVisible)
    {
        HideOverlayWindow(OverlayState::UserHidden);
        return;
    }

    const StatsSnapshot snapshot = ReadStatsSnapshot();
    if (!snapshot.playerCount)
    {
        HideOverlayWindow(OverlayState::NoData);
        return;
    }

    POINT gameOrigin = {};
    if (!TryGetGameClientOrigin(gameOrigin))
    {
        HideOverlayWindow(OverlayState::GameWindowMissing);
        return;
    }
    if (!GameIsForeground())
    {
        HideOverlayWindow(OverlayState::GameNotForeground);
        return;
    }

    const size_t visibleCount = (std::min)(snapshot.playerCount,
        kVisiblePlayers);
    const int width = ScaleX(kBasePanelWidth);
    const int height = Scale(BasePanelHeight(visibleCount));
    int x = gameOrigin.x + (g_config.autoPosition
        ? Scale(15) : g_config.offsetX);
    int y = gameOrigin.y + (g_config.autoPosition
        ? static_cast<int>(static_cast<float>(g_gameClientHeight) * 0.22f)
        : g_config.offsetY);
    if (g_dragging)
    {
        RECT current = {};
        if (GetWindowRect(g_overlayWindow, &current))
        {
            x = current.left;
            y = current.top;
        }
    }
    else
    {
        const int maxX = gameOrigin.x + (std::max)(0,
            g_gameClientWidth - width);
        const int maxY = gameOrigin.y + (std::max)(0,
            g_gameClientHeight - height);
        x = (std::max)(static_cast<int>(gameOrigin.x),
            (std::min)(x, maxX));
        y = (std::max)(static_cast<int>(gameOrigin.y),
            (std::min)(y, maxY));
    }

    POINT cursor = {};
    const bool haveCursor = GetCursorPos(&cursor) != FALSE;
    const bool cursorMoved = haveCursor &&
        (cursor.x != g_lastCursorX || cursor.y != g_lastCursorY);
    if (haveCursor)
    {
        g_lastCursorX = cursor.x;
        g_lastCursorY = cursor.y;
    }
    const bool needsRender = !g_overlayWasVisible ||
        g_lastRenderedGeneration != snapshot.generation ||
        g_lastRenderedPlayerCount != visibleCount ||
        g_lastRenderedWidth != width || g_lastRenderedHeight != height ||
        g_lastRenderedScale != g_renderScale ||
        g_dragging || cursorMoved;
    AcquireSRWLockExclusive(&g_frameLock);
    if (g_frame.ready)
    {
        g_frame.x = x;
        g_frame.y = y;
    }
    ReleaseSRWLockExclusive(&g_frameLock);
    if (needsRender && !RenderOverlay(g_overlayWindow, x, y, snapshot))
    {
        Log(L"[window] UpdateLayeredWindow failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        return;
    }
    const bool needsWindowUpdate = !g_overlayWasVisible ||
        x != g_lastWindowX || y != g_lastWindowY ||
        width != g_lastWindowWidth || height != g_lastWindowHeight;
    if (needsWindowUpdate && !SetWindowPos(g_overlayWindow, HWND_TOP, x, y,
        width, height, SWP_NOACTIVATE | SWP_SHOWWINDOW))
    {
        Log(L"[window] SetWindowPos failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        return;
    }
    if (needsWindowUpdate)
    {
        g_lastWindowX = x;
        g_lastWindowY = y;
        g_lastWindowWidth = width;
        g_lastWindowHeight = height;
    }
    g_overlayWasVisible = true;
    SetOverlayState(OverlayState::Visible);
}

LRESULT CALLBACK OverlayProc(HWND window, UINT message,
    WPARAM wParam, LPARAM lParam)
{
    switch (message)
    {
    case WM_TIMER:
        if (wParam == kOverlayTimerId)
        {
            PollCurrentCharacterPointer();
            PollCurrentCharacter();
            RefreshRoleIdentities();
            RequestLoadNotice();
            UpdateOverlayWindow();
        }
        else if (wParam == kClickTimerId)
        {
            PollOverlayDrag();
            if (g_overlayWasVisible)
                UpdateOverlayWindow();
        }
        return 0;
    case kOverlayRefreshMessage:
        g_lastRenderedGeneration = std::numeric_limits<uint64_t>::max();
        UpdateOverlayWindow();
        return 0;
    case WM_HOTKEY:
        if (wParam == kWindowHotkeyId)
        {
            InterlockedIncrement(&g_s5ToggleRequests);
            UpdateOverlayWindow();
            Log(L"[hotkey] visibility toggled: requestedVisible=%d",
                g_userVisible ? 1 : 0);
            QueueOfficialNotice(g_userVisible ? L"DPS已开启" : L"DPS已关闭");
            if (!g_userVisible)
                HideOverlayWindow(OverlayState::UserHidden);
            else
                UpdateOverlayWindow();
        }
        return 0;
    case WM_NCHITTEST:
        return HTTRANSPARENT;
    case WM_MOUSEACTIVATE:
        return MA_NOACTIVATE;
    case WM_ERASEBKGND:
        return 1;
    case WM_DESTROY:
        KillTimer(window, kOverlayTimerId);
        KillTimer(window, kClickTimerId);
        UnregisterHotKey(window, kWindowHotkeyId);
        g_overlayWindow = nullptr;
        PostQuitMessage(0);
        return 0;
    default:
        break;
    }
    return DefWindowProcW(window, message, wParam, lParam);
}

DWORD WINAPI OverlayThreadProc(LPVOID)
{
    RequestLoadNotice();
    if (!InitializeOfficialAssets())
    {
        Log(L"[window] official DPS assets failed to load");
        InterlockedExchange(&g_overlayStartupResult, -1);
        SetEvent(g_overlayReadyEvent);
        return 0;
    }

    WNDCLASSEXW windowClass = {};
    windowClass.cbSize = sizeof(windowClass);
    windowClass.hInstance = g_module;
    windowClass.lpfnWndProc = OverlayProc;
    windowClass.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    windowClass.lpszClassName = kOverlayClass;
    if (!RegisterClassExW(&windowClass) &&
        GetLastError() != ERROR_CLASS_ALREADY_EXISTS)
    {
        Log(L"[window] class registration failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        ShutdownOfficialAssets();
        InterlockedExchange(&g_overlayStartupResult, -1);
        SetEvent(g_overlayReadyEvent);
        return 0;
    }

    DWORD extendedStyle = WS_EX_LAYERED | WS_EX_TOOLWINDOW |
        WS_EX_NOACTIVATE | WS_EX_TRANSPARENT;
    HWND window = CreateWindowExW(extendedStyle, kOverlayClass, L"DPS",
        WS_POPUP, 0, 0, kBasePanelWidth, BasePanelHeight(1),
        nullptr, nullptr, g_module, nullptr);
    if (!window)
    {
        Log(L"[window] creation failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        ShutdownOfficialAssets();
        InterlockedExchange(&g_overlayStartupResult, -1);
        SetEvent(g_overlayReadyEvent);
        return 0;
    }
    g_overlayWindow = window;

    if (g_config.windowHotkey.IsValid() && !RegisterHotKey(window,
        kWindowHotkeyId, g_config.windowHotkey.modifiers | MOD_NOREPEAT,
        g_config.windowHotkey.virtualKey))
    {
        Log(L"[hotkey] registration failed: %lu",
            static_cast<unsigned long>(GetLastError()));
    }
    else if (g_config.windowHotkey.IsValid())
    {
        Log(L"[hotkey] registration succeeded: modifiers=0x%X key=0x%X",
            g_config.windowHotkey.modifiers,
            g_config.windowHotkey.virtualKey);
    }
    if (!SetTimer(window, kOverlayTimerId, kOverlayRefreshMilliseconds,
        nullptr))
    {
        Log(L"[window] refresh timer creation failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        DestroyWindow(window);
        ShutdownOfficialAssets();
        InterlockedExchange(&g_overlayStartupResult, -1);
        SetEvent(g_overlayReadyEvent);
        return 0;
    }
    if (!SetTimer(window, kClickTimerId, kClickPollMilliseconds, nullptr))
    {
        Log(L"[window] click timer creation failed: %lu",
            static_cast<unsigned long>(GetLastError()));
    }
    UpdateOverlayWindow();
    RequestLoadNotice();
    Log(L"[init] overlay thread ready");
    InterlockedExchange(&g_overlayStartupResult, 1);
    SetEvent(g_overlayReadyEvent);

    MSG message = {};
    while (GetMessageW(&message, nullptr, 0, 0) > 0)
    {
        TranslateMessage(&message);
        DispatchMessageW(&message);
    }
    if (IsWindow(window))
        DestroyWindow(window);
    ShutdownOfficialAssets();
    return 0;
}

bool LoadPluginConfig()
{
    g_config.debug = GetPrivateProfileIntW(L"General", L"Debug", 0,
        g_configPath.c_str()) != 0;
    g_config.enabled = GetPrivateProfileIntW(L"General", L"Enabled", 0,
        g_configPath.c_str()) != 0;
    g_config.clickThrough = GetPrivateProfileIntW(L"General",
        L"ClickThrough", 0, g_configPath.c_str()) != 0;
    g_config.autoPosition = GetPrivateProfileIntW(L"General",
        L"AutoPosition", 1, g_configPath.c_str()) != 0;
    const int offsetX = static_cast<int>(GetPrivateProfileIntW(
        L"General", L"OffsetX", 24, g_configPath.c_str()));
    const int offsetY = static_cast<int>(GetPrivateProfileIntW(
        L"General", L"OffsetY", 120, g_configPath.c_str()));
    const int scale = static_cast<int>(GetPrivateProfileIntW(
        L"General", L"Scale", 100, g_configPath.c_str()));
    g_config.offsetX = (std::max)(-4000, (std::min)(4000, offsetX));
    g_config.offsetY = (std::max)(-4000, (std::min)(4000, offsetY));
    g_config.scale = (std::max)(75, (std::min)(150, scale));

    wchar_t hotkeyText[128] = {};
    GetPrivateProfileStringW(L"General", L"WindowHotkey", L"None",
        hotkeyText, _countof(hotkeyText), g_configPath.c_str());
    bool valid = false;
    g_config.windowHotkey = ParseHotkey(hotkeyText, valid);
    if (!valid)
    {
        g_config.windowHotkey = {};
        Log(L"[config] invalid WindowHotkey; hotkey disabled");
    }
    return true;
}
}

extern "C" __declspec(dllexport) BOOL ClientPatchPluginInit()
{

    if (InterlockedCompareExchange(&g_started,1,0)) return TRUE;
    LoadPluginConfig(); g_config.enabled=true;
    g_overlayReadyEvent=CreateEventW(nullptr,TRUE,FALSE,nullptr);
    HANDLE thread=CreateThread(nullptr,0,OverlayThreadProc,nullptr,0,nullptr);
    if(!thread){g_started=0;return FALSE;} CloseHandle(thread); return TRUE;

}

BOOL APIENTRY DllMain(HMODULE module, DWORD reason, LPVOID)
{
    if (reason == DLL_PROCESS_ATTACH)
    {
        DisableThreadLibraryCalls(module);
        g_module = module;
        g_moduleDirectory = ModuleDirectory(module);
        g_configPath = g_moduleDirectory + L"\\" + kConfigFileName;
        g_logPath = g_moduleDirectory + L"\\" + kLogFileName;
    }
    return TRUE;
}

extern "C" __declspec(dllexport) void __cdecl S5A3DpsFeed(const S5DpsFrame* f) {
    if(!f || f->size!=sizeof(*f) || f->count>8) return;
    InterlockedExchange(&g_s5DungeonRun,static_cast<LONG>(f->reserved));
    AcquireSRWLockExclusive(&g_statsLock);
    g_stats={};g_stats.generation=f->generation;g_stats.playerCount=f->count;
    g_stats.elapsedMilliseconds=f->elapsed;
    for(unsigned i=0;i<f->count;i++) {
        auto& p=g_stats.players[i];wcsncpy_s(p.name,f->rows[i].name,_TRUNCATE);
        p.damage=f->rows[i].damage;p.hits=f->rows[i].hits;p.profession=f->rows[i].job;
        p.theme=ProfessionToTheme(p.profession);
        g_stats.totalDamage=SaturatingAdd(g_stats.totalDamage,p.damage);
    }
    ReleaseSRWLockExclusive(&g_statsLock);
}
extern "C" __declspec(dllexport) void __cdecl S5A3DpsToggle() {
    InterlockedIncrement(&g_s5ToggleRequests);
    if(g_overlayWindow) PostMessageW(g_overlayWindow,kOverlayRefreshMessage,0,0);
}
