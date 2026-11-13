#define NOMINMAX
#include "../S5A3Bridge.h"
#include <windows.h>

#include <algorithm>
#include <array>
#include <cstdarg>
#include <cstdint>
#include <cstdio>
#include <limits>
#include <cstring>
#include <string>
#include <vector>

#include "resource.h"
#include "CombatPowerFormula.h"
#include "../GameNative/GameNativeApi.h"

using combat_power::ApplyAffixesToSnapshot;
using combat_power::CollectScriptAffixes;
using combat_power::CombineEquippedAffixes;
using combat_power::CombatSnapshot;
using combat_power::CombatSlotEquipmentScore;
using combat_power::ComputeBaseScore;
using combat_power::FinalizeCombatScores;
using combat_power::IsCombatSlot;
using combat_power::ItemDamageAffixes;
using combat_power::RankName;
using combat_power::TryComputeEquipmentBonusHundredths;

namespace
{
constexpr wchar_t kOverlayClass[] = L"ClientPatchCombatPowerOverlay";
constexpr wchar_t kConfigFileName[] = L"CombatPower.ini";
constexpr wchar_t kLogFileName[] = L"CombatPower.log";

constexpr uintptr_t kPreferredImageBase = 0x00400000;
constexpr uintptr_t kUserManagerGlobal = 0x03A5C980;
constexpr uintptr_t kGetLocalUser = 0x019E3290;
constexpr uintptr_t kGetUserInfo = 0x027C6F20;
constexpr uintptr_t kGetItemAt = 0x01648BA0;
constexpr uintptr_t kDecryptTableGlobal = 0x03B89618;
constexpr uintptr_t kWindowManagerGlobal = 0x03A5C9B8;
constexpr uintptr_t kHasWindowAddress = 0x02301820;
constexpr uintptr_t kCurrentCharacterGlobal = 0x03B3DFB8;
constexpr uintptr_t kGetCharacterName = 0x018A8030;
constexpr int kMaxOfficialWindowId = 0x38B;
constexpr int kPersonalInfoWindowId = 167;
constexpr UINT_PTR kDnfPollTimer = 0x90C2;
constexpr UINT_PTR kDnfClickTimer = 0x90C3;
constexpr size_t kWindowVectorOffset = 0xFC;
constexpr size_t kWindowVectorStride = 20;
constexpr size_t kWindowRootWidgetOffset = 0x30;
constexpr size_t kWidgetXOffset = 0x34;
constexpr size_t kWidgetYOffset = 0x38;
constexpr size_t kWidgetWidthOffset = 0x3C;
constexpr size_t kWidgetHeightOffset = 0x40;
constexpr size_t kWidgetScreenXOffset = 76;
constexpr size_t kWidgetScreenYOffset = 80;
constexpr int kSidecarGap = 13;
constexpr int kPersonalPanelFrameTop = 13;
constexpr int kUiHeight = 600;
constexpr int kCombatRankIconShiftX = -2;
constexpr int kCombatRankIconShiftY = 0;
constexpr int kPanelGoneTicks = 2;
constexpr int kUpgradeButtonLeft = 26;
constexpr int kUpgradeButtonTop = 177;
constexpr int kUpgradeButtonRight = 93;
constexpr int kUpgradeButtonBottom = 204;
constexpr int kCloseButtonLeft = 88;
constexpr int kCloseButtonTop = 4;
constexpr int kCloseButtonRight = 116;
constexpr int kCloseButtonBottom = 24;
constexpr int kTooltipWidth = 230;
constexpr int kTooltipHeight = 260;
constexpr int kGuideWidth = 292;
constexpr int kGuideHeight = 238;
constexpr int kGuideCloseLeft = 264;
constexpr int kGuideCloseTop = 4;
constexpr int kGuideCloseRight = 288;
constexpr int kGuideCloseBottom = 32;
constexpr int kRankHoverTop = 24;
constexpr int kRankHoverBottom = 158;
constexpr int kCombatRankIconSize = 80;
constexpr int kCombatRankIconDraw = 80;
constexpr int kCombatRankIconX = 19;
constexpr int kCombatRankIconY = 37;
constexpr size_t kUserInfoJobOffset = 4;
constexpr size_t kUserInfoGrowLowOffset = 8;
constexpr size_t kUserInfoGrowHighOffset = 12;
constexpr size_t kUserInfoLevelKeyOffset = 0x14;
constexpr size_t kUserInfoLevelValueOffset = 0x18;
constexpr size_t kUserUniqueIdOffset = 8;
constexpr int kOfficialJobMax = 0x0D;
constexpr int kItemInfoVtableSlot = 10;
constexpr size_t kItemInfoIdOffset = 24;
constexpr size_t kItemInfoRepeatIdOffset = 0x2C8;
constexpr size_t kItemInfoQualityOffset = 0x2CC;
constexpr size_t kItemInfoAttrOffset = 0x2D0;
constexpr size_t kItemInfoAmplifyTypeOffset = 0x2D9;
constexpr size_t kItemInfoAmplifyValueOffset = 0x2DA;
constexpr size_t kCharNamePtrOffset = 0x374;
constexpr size_t kCharStatHpOffset = 0x31B8;
constexpr size_t kCharStatMpOffset = 0x31BC;
constexpr size_t kCharStatPhysicalAttackOffset = 0x34F0;
constexpr size_t kCharStatMagicalAttackOffset = 0x3500;
constexpr size_t kCharStatAttackBonusOffset = 0x2428;
constexpr size_t kCharStatStrengthOffset = 0x2140;
constexpr size_t kCharStatVitalityOffset = 0x2150;
constexpr size_t kCharStatIntelligenceOffset = 0x2160;
constexpr size_t kCharStatSpiritOffset = 0x2170;
constexpr size_t kCharProfessionOffset = 0x389C;
// 穿戴物上客户端已结算的 [separate attack]（强化/品质后的当前值，不是下限 0xE60）。
// 时装 0–11、装备 12–23、宠物/宠物装备 25+ 都走同一字段。
constexpr size_t kItemIndependentAttackOffset = 0xE58;
constexpr int kMaxEquipmentSlot = 32;
constexpr size_t kItemScriptTreeOffset = 0xB5C;
// 与 EquipmentSwap kSlotDefinitions.destinationSlot 相同，再补光环皮肤/
// 副武器/神器等换装不换、但 GetItemAt(3) 仍能拿到的占用槽。
constexpr std::array<int, 33> kActorEquipmentSlots = {{
    0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
    12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23,
    25,
    11, 24, 26, 27, 28, 29, 30, 31, 32
}};
static_assert(kActorEquipmentSlots.size() == 33);

constexpr int kPanelWidth = 118;
constexpr int kPanelHeight = 386;
constexpr int kCollapsedWidth = 28;
constexpr int kCollapsedHeight = 96;
constexpr int kAffixArrowLeft = 4;
constexpr int kAffixArrowTop = 330;
constexpr int kAffixArrowRight = 22;
constexpr int kAffixArrowBottom = 358;
constexpr int kAffixArrowNextLeft = 96;
constexpr int kAffixArrowNextRight = 116;
constexpr int kAffixPageCount = 2;
constexpr UINT_PTR kOverlayTimerId = 1;
constexpr UINT kOverlayRefreshMilliseconds = 100;
constexpr UINT kClickPollMilliseconds = 16;
constexpr UINT kLaunchQuietMilliseconds = 3000;
constexpr UINT kIdentityPollMilliseconds = 1000;
constexpr UINT kIdentityQuietMilliseconds = 2000;

HMODULE g_module = nullptr;
HWND g_overlayWindow = nullptr;
HWND g_tooltipWindow = nullptr;
HWND g_guideWindow = nullptr;
HWND g_gameWindow = nullptr;
WNDPROC g_originalDnfWindowProc = nullptr;
void* g_lastPersonalWindow = nullptr;
HANDLE g_overlayReadyEvent = nullptr;
LONG g_started = 0;
LONG g_overlayStartupResult = 0;
bool g_nativeBound = false;
bool g_nativeMissLogged = false;
bool g_loadNoticeSent = false;
bool g_personalInfoOpen = false;
int g_gameClientWidth = 0;
int g_gameClientHeight = 0;
int g_lastWindowX = std::numeric_limits<int>::min();
int g_lastWindowY = std::numeric_limits<int>::min();
int g_lastNativeX = 0;
int g_lastNativeY = 0;
int g_lastNativeW = 0;
int g_lastNativeH = 0;
bool g_overlayOnDnfThread = false;
bool g_guideVisible = false;
bool g_userDismissed = false;
int g_affixPage = 0;
bool g_leftButtonWasDown = false;
int g_pressHit = 0;
int g_lastCursorX = std::numeric_limits<int>::min();
int g_lastCursorY = std::numeric_limits<int>::min();
int g_lastOverlayWidth = 0;
int g_lastOverlayHeight = 0;
DWORD g_lastGuideToggleTick = 0;
DWORD g_lastAffixPageTick = 0;
std::wstring g_moduleDirectory;
std::wstring g_configPath;
std::wstring g_logPath;
ClientPatchGameNativeApi g_native = {};
SRWLOCK g_logLock = SRWLOCK_INIT;

struct PluginConfig
{
    bool enabled = false;
    bool debug = false;
};

PluginConfig g_config;
CombatSnapshot g_snapshot;
SRWLOCK g_s5Lock=SRWLOCK_INIT;
S5PowerFrame g_s5Frame={};
int g_uiWidth=800,g_uiHeight=600;
bool g_panelDragging=false,g_panelOffsetSet=false;
POINT g_panelDragStart={};
int g_panelOffsetX=0,g_panelOffsetY=0,g_panelDragX=0,g_panelDragY=0;


using GetLocalUserFn = void* (__thiscall*)(void* manager);
using GetUserInfoFn = void* (__thiscall*)(void* user);
using GetItemAtFn = void* (__cdecl*)(int, unsigned int);
using GetItemInfoFn = void* (__thiscall*)(void*);
using GetCharacterNameFn = const wchar_t* (__thiscall*)(void*);

int MapUiToClient(int ui);
int ScaleX(int design);
int ScaleY(int design);
int ScalePx(int design);
int ScaleFont(int design);
int ScaleRankIcon(int design);
RECT ScaleRect(int left, int top, int right, int bottom);
POINT ToDesignPoint(HWND window, POINT client, int designWidth, int designHeight);

uintptr_t ClientAddress(uintptr_t preferredAddress)
{
    const auto base = reinterpret_cast<uintptr_t>(GetModuleHandleW(nullptr));
    return base + (preferredAddress - kPreferredImageBase);
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
        fwprintf(file, L"[%02u:%02u:%02u.%03u] %s\n", now.wHour, now.wMinute,
            now.wSecond, now.wMilliseconds, message);
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
    return g_native.chatRgb ? g_native.chatRgb(72, 196, 220) : 0;
}

void QueueOfficialNotice(const wchar_t* text)
{
    (void)text;
}

void RequestLoadNotice()
{

}

bool ValidateClient()
{
    return true; // admitted only by the exact-image S5A3 loader
}

void* CurrentLocalUser()
{
    return nullptr;
}

void* OfficialWindowManager()
{
    return nullptr;
}

enum class WindowPresence
{
    Present,
    Absent,
    Unreadable,
};

bool TryReadWidgetRect(void* window, int& x, int& y, int& width, int& height)
{
    return false;
}

WindowPresence ReadOfficialWindowPresence(int windowId, int& x, int& y,
    int& width, int& height, void** windowOut)
{
    if (windowOut)
        *windowOut = nullptr;
    if (windowId < 0 || windowId > kMaxOfficialWindowId)
        return WindowPresence::Absent;
    void* manager = OfficialWindowManager();
    if (!manager)
        return WindowPresence::Unreadable;
    const size_t slot = kWindowVectorOffset +
        static_cast<size_t>(windowId) * kWindowVectorStride;
    if (!IsReadableRange(static_cast<unsigned char*>(manager) + slot, 12))
        return WindowPresence::Unreadable;
    __try
    {
        const auto* vector = static_cast<const unsigned char*>(manager) + slot;
        const uintptr_t begin = *reinterpret_cast<const uintptr_t*>(
            vector + 4);
        const uintptr_t end = *reinterpret_cast<const uintptr_t*>(
            vector + 8);
        if (!begin || end <= begin)
            return WindowPresence::Absent;
        if ((end - begin) % sizeof(void*) != 0)
            return WindowPresence::Unreadable;
        const size_t bytes = static_cast<size_t>(end - begin);
        if (bytes > 32 * sizeof(void*) ||
            !IsReadableRange(reinterpret_cast<const void*>(begin), bytes))
            return WindowPresence::Unreadable;
        const auto* items = reinterpret_cast<void* const*>(begin);
        const size_t count = bytes / sizeof(void*);
        for (size_t index = 0; index < count; ++index)
        {
            if (TryReadWidgetRect(items[index], x, y, width, height))
            {
                if (windowOut)
                    *windowOut = items[index];
                return WindowPresence::Present;
            }
        }
        return WindowPresence::Unreadable;
    }
    __except (EXCEPTION_EXECUTE_HANDLER)
    {
        return WindowPresence::Unreadable;
    }
}

bool IsPersonalInfoRect(int width, int height)
{
    return width >= 220 && width <= 520 && height >= 340 && height <= 650;
}

bool IsFollowableRect(int width, int height)
{
    return width >= 160 && width <= 700 && height >= 200 && height <= 760;
}

bool TryGetPersonalInfoRect(int& x, int& y, int& width, int& height,
    int& windowId, bool allowClose)
{

    AcquireSRWLockShared(&g_s5Lock);auto f=g_s5Frame;ReleaseSRWLockShared(&g_s5Lock);
    if(!f.visible || GetTickCount()-f.tick>1500) return false;
    if(f.uiWidth<400 || f.uiWidth>8192 || f.uiHeight<300 || f.uiHeight>8192) return false;
    g_uiWidth=f.uiWidth;g_uiHeight=f.uiHeight;
    x=f.x;y=f.y;width=f.width;height=f.height;windowId=240;return true;

}

unsigned int DecryptOfficialValue(unsigned int key, unsigned int value)
{
    return 0;
}

bool CopyWideName(const wchar_t* source, wchar_t* output, size_t capacity)
{
    if (!source || !output || capacity < 2 ||
        !IsReadableRange(source, sizeof(wchar_t)))
        return false;
    size_t length = 0;
    while (length + 1 < capacity && source[length])
    {
        const wchar_t value = source[length];
        if (value < 0x20 && value != L' ')
            return false;
        output[length++] = value;
    }
    if (!length)
        return false;
    output[length] = L'\0';
    return true;
}

bool LooksLikeProfession(const wchar_t* text)
{
    if (!text || !text[0])
        return false;
    size_t length = 0;
    while (text[length])
    {
        const wchar_t value = text[length];
        if (value < 0x4E00 || value > 0x9FFF)
            return false;
        if (++length > 8)
            return false;
    }
    return length >= 2;
}

const wchar_t* BaseJobName(int job)
{
    static constexpr const wchar_t* kNames[] = {
        L"鬼剑士", L"女格斗家", L"男神枪手", L"女魔法师", L"男圣职者",
        L"女神枪手", L"暗夜使者", L"男格斗家", L"男魔法师", L"女圣职者",
        L"暗刃", L"骑士", L"魔枪士", L"枪剑士"
    };
    return job >= 0 && job <= kOfficialJobMax ? kNames[job] : L"--";
}

const wchar_t* GrowJobName(int job, int firstGrow)
{
    if (firstGrow <= 0)
        return BaseJobName(job);
    static constexpr const wchar_t* kGrows[][4] = {
        { L"剑魂", L"鬼泣", L"狂战士", L"阿修罗" },
        { L"气功师", L"散打", L"街霸", L"柔道家" },
        { L"漫游枪手", L"枪炮师", L"机械师", L"弹药专家" },
        { L"元素师", L"召唤师", L"战斗法师", L"魔道学者" },
        { L"圣骑士", L"蓝拳圣使", L"驱魔师", L"复仇者" },
        { L"女漫游", L"女枪炮", L"女机械", L"女弹药" },
        { L"刺客", L"死灵术士", L"忍者", L"影舞者" },
        { L"男气功", L"男散打", L"男街霸", L"男柔道" },
        { L"元素爆破师", L"冰结师", L"血法师", L"逐风者" },
        { L"女圣骑士", L"异端审判者", L"巫女", L"诱魔者" },
    };
    if (job < 0 || job > 9 || firstGrow > 4)
        return BaseJobName(job);
    return kGrows[job][firstGrow - 1];
}

const wchar_t* ProfessionName(int job, int firstGrow, int awakening)
{
    // 90CN DisplayName：觉醒名优先于转职名。官方人物窗已经按这个优先级
    // 写到角色对象；这里只在读不到宽字符串时兜底。
    if (job == 5 && firstGrow == 2 && awakening >= 2)
        return L"风暴骑兵";
    if (job == 4 && firstGrow == 4 && awakening >= 2)
        return L"永生者";
    return GrowJobName(job, firstGrow);
}

void* CurrentCharacter()
{
    return nullptr;
}

unsigned int ReadEncryptedU32(const unsigned char* object, size_t offset)
{
    if (!object || !IsReadableRange(object + offset, 8))
        return 0;
    const unsigned int key = *reinterpret_cast<const unsigned int*>(
        object + offset);
    const unsigned int value = *reinterpret_cast<const unsigned int*>(
        object + offset + 4);
    if ((key >> 16) == 0)
        return 0;
    return DecryptOfficialValue(key, value);
}

bool ReadStdWString(const unsigned char* object, size_t offset,
    wchar_t* output, size_t capacity)
{
    if (!output || capacity < 2 ||
        !IsReadableRange(object + offset, 24))
        return false;
    const unsigned int size = *reinterpret_cast<const unsigned int*>(
        object + offset + 16);
    const unsigned int cap = *reinterpret_cast<const unsigned int*>(
        object + offset + 20);
    if (size == 0 || size >= capacity || size > 16 || cap < size)
        return false;
    const wchar_t* source = nullptr;
    if (cap < 8)
        source = reinterpret_cast<const wchar_t*>(object + offset);
    else
    {
        const unsigned int pointer = *reinterpret_cast<const unsigned int*>(
            object + offset);
        if (!pointer)
            return false;
        source = reinterpret_cast<const wchar_t*>(pointer);
        if (!IsReadableRange(source, (size + 1) * sizeof(wchar_t)))
            return false;
    }
    return CopyWideName(source, output, capacity) &&
        LooksLikeProfession(output);
}

void* ItemInfo(void* item)
{
    return nullptr;
}

bool ReadEquippedUpgrade(int slot, unsigned int& upgrade,
    unsigned int& amplifyType, unsigned int& amplifyValue)
{
    return false;
}

int ReadEquippedItemId(int slot)
{
    return 0;
}

unsigned int ReadSlotEncrypted(int slot, size_t offset)
{
    return 0;
}

unsigned int ReadSlotIndependentAttack(int slot)
{
    const unsigned int independent = ReadSlotEncrypted(slot,
        kItemIndependentAttackOffset);
    if (independent > 20000)
        return 0;
    return independent;
}

ItemDamageAffixes ReadSlotDamageAffixes(int slot)
{
    return {};
}

CombatSnapshot ReadOfficialSnapshot(bool includeEquipment)
{

    AcquireSRWLockShared(&g_s5Lock);auto result=g_s5Frame.stats;
    ReleaseSRWLockShared(&g_s5Lock);return result;

}

void RefreshSnapshot(bool includeEquipment)
{
    static CombatSnapshot lastLogged = {};
    CombatSnapshot snapshot = ReadOfficialSnapshot(includeEquipment);
    g_snapshot = snapshot;
    if (snapshot.characterId == lastLogged.characterId &&
        snapshot.job == lastLogged.job &&
        snapshot.level == lastLogged.level &&
        snapshot.equippedItems == lastLogged.equippedItems &&
        snapshot.whiteDamageTenths == lastLogged.whiteDamageTenths &&
        snapshot.yellowDamageTenths == lastLogged.yellowDamageTenths &&
        snapshot.criticalDamageTenths == lastLogged.criticalDamageTenths &&
        snapshot.totalScore == lastLogged.totalScore &&
        wcscmp(snapshot.profession, lastLogged.profession) == 0)
        return;
    lastLogged = snapshot;
    Log(L"[state] id=%u name=%s job=%d grow=%d/%d lv=%u items=%u "
        L"combat=%u hp=%u mp=%u str=%d vit=%d int=%d spi=%d pa=%u ma=%u ia=%u "
        L"white=%u yellow=%u crit=%u yadd=%u cadd=%u all=%u "
        L"base=%u equip=%u total=%u profession=%s",
        snapshot.characterId, snapshot.name, snapshot.job,
        snapshot.firstGrow, snapshot.awakening, snapshot.level,
        snapshot.equippedItems, snapshot.combatSlots, snapshot.hpMax,
        snapshot.mpMax, snapshot.strength, snapshot.vitality,
        snapshot.intelligence, snapshot.spirit, snapshot.physicalAttack,
        snapshot.magicalAttack, snapshot.independentAttack,
        snapshot.whiteDamageTenths, snapshot.yellowDamageTenths,
        snapshot.criticalDamageTenths, snapshot.yellowAdditionalTenths,
        snapshot.criticalAdditionalTenths, snapshot.allAttackTenths,
        snapshot.baseScore, snapshot.equipmentScore, snapshot.totalScore,
        snapshot.profession);
    if (includeEquipment && snapshot.equipmentValid)
    {
        static unsigned int lastAffixDumpId = 0;
        static unsigned int lastAffixDumpItems = 0;
        if (lastAffixDumpId != snapshot.characterId ||
            lastAffixDumpItems != snapshot.equippedItems)
        {
            lastAffixDumpId = snapshot.characterId;
            lastAffixDumpItems = snapshot.equippedItems;
            for (int slot : kActorEquipmentSlots)
            {
                const int itemId = ReadEquippedItemId(slot);
                if (itemId <= 0)
                    continue;
                const ItemDamageAffixes part = ReadSlotDamageAffixes(slot);
                Log(L"[affix] slot=%d id=%d ia=%u white=%.0f yellow=%.0f crit=%.0f yadd=%.0f cadd=%.0f all=%.0f",
                    slot, itemId, ReadSlotIndependentAttack(slot),
                    part.white, part.yellow, part.critical,
                    part.yellowAdditional, part.criticalAdditional,
                    part.allAttack);
            }
        }
    }
}

HFONT CreatePanelFont(int pixelHeight, int weight)
{
    HFONT font = CreateFontW(-pixelHeight, 0, 0, 0, weight, FALSE, FALSE,
        FALSE, DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
        CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_DONTCARE, L"Microsoft YaHei");
    return font ? font : reinterpret_cast<HFONT>(GetStockObject(DEFAULT_GUI_FONT));
}

void DrawPanelText(HDC dc, const wchar_t* text, RECT bounds, int height,
    int weight, COLORREF color, UINT format)
{
    HFONT font = CreatePanelFont(height, weight);
    HGDIOBJ old = SelectObject(dc, font);
    SetBkMode(dc, TRANSPARENT);
    SetTextColor(dc, color);
    DrawTextW(dc, text, -1, &bounds, format | DT_NOPREFIX);
    SelectObject(dc, old);
    if (font != GetStockObject(DEFAULT_GUI_FONT))
        DeleteObject(font);
}

void DrawPanelTextShadowed(HDC dc, const wchar_t* text, RECT bounds,
    int height, int weight, COLORREF color, UINT format)
{
    RECT shadow = bounds;
    OffsetRect(&shadow, 1, 1);
    DrawPanelText(dc, text, shadow, height, weight, RGB(0, 0, 0), format);
    DrawPanelText(dc, text, bounds, height, weight, color, format);
}

void DrawOverlayCursor(HDC dc, HWND window)
{
    if (!dc || !window)
        return;
    POINT screen = {};
    RECT rect = {};
    if (!GetCursorPos(&screen) || !GetWindowRect(window, &rect) ||
        !PtInRect(&rect, screen))
        return;
    POINT client = screen;
    if (!ScreenToClient(window, &client))
        return;
    DrawIconEx(dc, client.x, client.y, LoadCursorW(nullptr, IDC_ARROW),
        0, 0, 0, nullptr, DI_NORMAL);
}

void FillRoundish(HDC dc, RECT rect, COLORREF color)
{
    HBRUSH brush = CreateSolidBrush(color);
    FillRect(dc, &rect, brush);
    DeleteObject(brush);
}

const unsigned char* LoadEmbeddedPixels(int resourceId, size_t expectedBytes)
{
    const HRSRC resource = FindResourceW(g_module,
        MAKEINTRESOURCEW(resourceId), RT_RCDATA);
    if (!resource)
        return nullptr;
    const HGLOBAL handle = LoadResource(g_module, resource);
    if (!handle)
        return nullptr;
    const DWORD size = SizeofResource(g_module, resource);
    if (size != expectedBytes)
        return nullptr;
    return static_cast<const unsigned char*>(LockResource(handle));
}

int RankIconResource(unsigned int score)
{
    if (score < 3100) return IDR_COMBAT_RANK_EXPLORE;
    if (score < 8100) return IDR_COMBAT_RANK_PIONEER;
    if (score < 23000) return IDR_COMBAT_RANK_FEARLESS;
    if (score < 52000) return IDR_COMBAT_RANK_CONQUER;
    if (score < 65000) return IDR_COMBAT_RANK_BATTLE;
    if (score < 130000) return IDR_COMBAT_RANK_HEROIC;
    return IDR_COMBAT_RANK_MASTERY;
}

bool BlitBgra(HDC dc, const unsigned char* pixels, int width, int height)
{
    if (!pixels)
        return false;
    BITMAPINFO info = {};
    info.bmiHeader.biSize = sizeof(info.bmiHeader);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;
    return StretchDIBits(dc, 0, 0, width, height, 0, 0, width, height,
        pixels, &info, DIB_RGB_COLORS, SRCCOPY) != GDI_ERROR;
}

void BlendIcon(unsigned char* dest, int destWidth, int destHeight,
    const unsigned char* icon, int iconWidth, int iconHeight, int destX,
    int destY)
{
    if (!dest || !icon || iconWidth <= 0 || iconHeight <= 0)
        return;
    for (int row = 0; row < iconHeight; ++row)
    {
        for (int column = 0; column < iconWidth; ++column)
        {
            const int dx = destX + column;
            const int dy = destY + row;
            if (dx < 0 || dy < 0 || dx >= destWidth || dy >= destHeight)
                continue;
            const unsigned char* src = icon +
                (row * iconWidth + column) * 4;
            const unsigned int alpha = src[3];
            if (!alpha)
                continue;
            unsigned char* pixel = dest + (dy * destWidth + dx) * 4;
            const unsigned int inverse = 255 - alpha;
            for (int channel = 0; channel < 3; ++channel)
            {
                const unsigned int value = src[channel] +
                    (pixel[channel] * inverse + 127) / 255;
                pixel[channel] = static_cast<unsigned char>(
                    value > 255 ? 255 : value);
            }
            pixel[3] = 255;
        }
    }
}

void ScaleBgraNearest(const unsigned char* src, int srcWidth, int srcHeight,
    unsigned char* dest, int destWidth, int destHeight)
{
    if (!src || !dest || srcWidth <= 0 || srcHeight <= 0 || destWidth <= 0 ||
        destHeight <= 0)
        return;
    for (int row = 0; row < destHeight; ++row)
    {
        const int srcRow = row * srcHeight / destHeight;
        for (int column = 0; column < destWidth; ++column)
        {
            const int srcColumn = column * srcWidth / destWidth;
            std::memcpy(dest + (row * destWidth + column) * 4,
                src + (srcRow * srcWidth + srcColumn) * 4, 4);
        }
    }
}

bool DrawPanelSkin(HDC dc, unsigned int score, int destWidth, int destHeight)
{
    const size_t panelBytes = static_cast<size_t>(kPanelWidth) *
        kPanelHeight * 4;
    const size_t iconBytes = static_cast<size_t>(kCombatRankIconSize) *
        kCombatRankIconSize * 4;
    const unsigned char* panel = LoadEmbeddedPixels(
        IDR_COMBAT_POWER_PANEL_SKIN, panelBytes);
    const unsigned char* icon = LoadEmbeddedPixels(
        RankIconResource(score), iconBytes);
    static bool loggedSkin = false;
    if (!loggedSkin)
    {
        loggedSkin = true;
        Log(L"[panel] skin=%d rank=%d dest=%dx%d", panel ? 1 : 0, icon ? 1 : 0,
            destWidth, destHeight);
    }
    if (!panel || destWidth <= 0 || destHeight <= 0)
        return false;
    std::vector<unsigned char> composed(
        static_cast<size_t>(destWidth) * destHeight * 4);
    ScaleBgraNearest(panel, kPanelWidth, kPanelHeight, composed.data(),
        destWidth, destHeight);
    if (icon && destWidth > 0 && destHeight > 0)
    {
        const int wellX = (kCombatRankIconX * destWidth + kPanelWidth - 1) /
            kPanelWidth;
        const int wellY = (kCombatRankIconY * destHeight + kPanelHeight - 1) /
            kPanelHeight;
        const int wellW = ((kCombatRankIconX + kCombatRankIconDraw) * destWidth +
            kPanelWidth - 1) / kPanelWidth - wellX;
        const int wellH = ((kCombatRankIconY + kCombatRankIconDraw) *
            destHeight + kPanelHeight - 1) / kPanelHeight - wellY;
        const int destIconW = (std::max)(1, ScaleRankIcon(kCombatRankIconSize));
        const int destIconH = (std::max)(1, ScaleRankIcon(kCombatRankIconSize));
        const int iconX = wellX + (wellW - destIconW) / 2 +
            kCombatRankIconShiftX;
        const int iconY = wellY + (wellH - destIconH) / 2 +
            kCombatRankIconShiftY;
        std::vector<unsigned char> scaled(
            static_cast<size_t>(destIconW) * destIconH * 4);
        ScaleBgraNearest(icon, kCombatRankIconSize, kCombatRankIconSize,
            scaled.data(), destIconW, destIconH);
        BlendIcon(composed.data(), destWidth, destHeight, scaled.data(),
            destIconW, destIconH, iconX, iconY);
    }
    return BlitBgra(dc, composed.data(), destWidth, destHeight);
}

void DrawAffixPercentLine(HDC dc, RECT bounds, const wchar_t* label,
    unsigned int tenths, bool valid, COLORREF color)
{
    wchar_t line[64] = {};
    if (valid)
        swprintf_s(line, L"%s  %u.%u0%%", label, tenths / 10, tenths % 10);
    else
        swprintf_s(line, L"%s  --", label);
    DrawPanelTextShadowed(dc, line, bounds, ScaleFont(10), FW_BOLD,
        valid ? color : RGB(143, 193, 215),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
}

void DrawEquipmentBonusLine(HDC dc, RECT bounds, const CombatSnapshot& state)
{
    wchar_t line[64] = {};
    unsigned int equipmentBonusHundredths = 0;
    if (TryComputeEquipmentBonusHundredths(state, &equipmentBonusHundredths))
        swprintf_s(line, L"装备  %u.%02u%%",
            equipmentBonusHundredths / 100, equipmentBonusHundredths % 100);
    else
        wcscpy_s(line, L"装备  --");
    DrawPanelTextShadowed(dc, line, bounds, ScaleFont(10), FW_BOLD,
        RGB(255, 174, 104), DT_CENTER | DT_VCENTER | DT_SINGLELINE);
}

void DrawCollapsedTab(HDC dc)
{
    const int width = ScaleX(kCollapsedWidth);
    const int height = ScaleY(kCollapsedHeight);
    RECT panel = { 0, 0, width, height };
    FillRoundish(dc, panel, RGB(8, 28, 44));
    HPEN border = CreatePen(PS_SOLID, 1, RGB(212, 176, 98));
    HGDIOBJ oldPen = SelectObject(dc, border);
    HGDIOBJ oldBrush = SelectObject(dc, GetStockObject(NULL_BRUSH));
    Rectangle(dc, 0, 0, width, height);
    SelectObject(dc, oldBrush);
    SelectObject(dc, oldPen);
    DeleteObject(border);
    DrawPanelTextShadowed(dc, L"战", { 0, ScalePx(10), width, ScalePx(38) },
        ScaleFont(14), FW_BOLD, RGB(245, 226, 171),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawPanelTextShadowed(dc, L"力", { 0, ScalePx(34), width, ScalePx(62) },
        ScaleFont(14), FW_BOLD, RGB(245, 226, 171),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawPanelTextShadowed(dc, L">", { 0, ScalePx(64), width, ScalePx(90) },
        ScaleFont(16), FW_BOLD, RGB(255, 174, 104),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawOverlayCursor(dc, g_overlayWindow);
}

void DrawPanel(HDC dc, const CombatSnapshot& state)
{
    const int destWidth = ScaleX(kPanelWidth);
    const int destHeight = ScaleY(kPanelHeight);
    const unsigned int score = state.identityValid ? state.totalScore : 0;
    if (!DrawPanelSkin(dc, score, destWidth, destHeight))
    {
        RECT panel = { 0, 0, destWidth, destHeight };
        FillRoundish(dc, panel, RGB(5, 17, 28));
    }

    wchar_t line[64] = {};
    if (state.identityValid)
        swprintf_s(line, L"%u", state.totalScore);
    else
        wcscpy_s(line, L"--");
    DrawPanelTextShadowed(dc, line, ScaleRect(5, 128, 113, 153), ScaleFont(18),
        FW_BOLD, RGB(255, 225, 101), DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    FillRoundish(dc, ScaleRect(35, 102, 83, 117), RGB(78, 52, 25));
    DrawPanelTextShadowed(dc,
        state.identityValid ? RankName(state.totalScore) : L"--",
        ScaleRect(32, 100, 88, 120), ScaleFont(10), FW_BOLD, RGB(245, 226, 171),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    if (state.identityValid)
        swprintf_s(line, L"%s  Lv.%u", state.profession, state.level);
    else
        wcscpy_s(line, L"--  Lv.--");
    DrawPanelTextShadowed(dc, line, ScaleRect(5, 153, 113, 177), ScaleFont(10),
        FW_BOLD, RGB(220, 229, 232), DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    if (state.baseValid)
        swprintf_s(line, L"%u", state.baseScore);
    else
        wcscpy_s(line, L"--");
    DrawPanelTextShadowed(dc, line, ScaleRect(5, 224, 113, 251), ScaleFont(12),
        FW_BOLD, RGB(103, 218, 255), DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    const bool affixesValid = state.affixesValid;
    if (g_affixPage == 0)
    {
        DrawAffixPercentLine(dc, ScaleRect(7, 274, 111, 302), L"白字",
            state.whiteDamageTenths, affixesValid, RGB(225, 236, 242));
        DrawAffixPercentLine(dc, ScaleRect(7, 302, 111, 330), L"黄字",
            state.yellowDamageTenths + state.yellowAdditionalTenths,
            affixesValid, RGB(255, 216, 95));
        DrawEquipmentBonusLine(dc, ScaleRect(7, 330, 111, 358), state);
        DrawAffixPercentLine(dc, ScaleRect(7, 358, 111, 386), L"爆伤",
            state.criticalDamageTenths + state.criticalAdditionalTenths,
            affixesValid, RGB(139, 218, 244));
    }
    else
    {
        DrawAffixPercentLine(dc, ScaleRect(7, 274, 111, 302), L"黄追",
            state.yellowAdditionalTenths, affixesValid, RGB(249, 232, 145));
        DrawAffixPercentLine(dc, ScaleRect(7, 302, 111, 330), L"爆追",
            state.criticalAdditionalTenths, affixesValid, RGB(246, 190, 132));
        DrawAffixPercentLine(dc, ScaleRect(7, 330, 111, 358), L"全攻",
            state.allAttackTenths, affixesValid, RGB(161, 216, 241));
        DrawEquipmentBonusLine(dc, ScaleRect(7, 358, 111, 386), state);
    }
    DrawOverlayCursor(dc, g_overlayWindow);
}

void HideTooltipWindow()
{
    if (g_tooltipWindow)
        ShowWindow(g_tooltipWindow, SW_HIDE);
}

void HideGuideWindow()
{
    if (g_guideWindow)
        ShowWindow(g_guideWindow, SW_HIDE);
    g_guideVisible = false;
}

void HideOverlayWindow()
{
    HideTooltipWindow();
    HideGuideWindow();
    if (g_overlayWindow)
        ShowWindow(g_overlayWindow, SW_HIDE);
}

void SetGuideVisible(bool visible)
{
    if (!visible || !g_gameWindow || !g_overlayWindow || !g_guideWindow ||
        !IsWindowVisible(g_overlayWindow))
    {
        HideGuideWindow();
        return;
    }

    RECT panel = {};
    RECT parentRect = {};
    if (!GetWindowRect(g_overlayWindow, &panel) ||
        !GetWindowRect(g_gameWindow, &parentRect))
        return;
    int x = panel.right + 4;
    int y = panel.top + ScaleY(kUpgradeButtonTop) - 4;
    const int guideWidth = ScaleX(kGuideWidth);
    const int guideHeight = ScaleY(kGuideHeight);
    if (x + guideWidth > parentRect.right - 4)
        x = panel.left - guideWidth - 4;
    if (x < parentRect.left + 4)
        x = parentRect.left + 4;
    if (y + guideHeight > parentRect.bottom - 4)
        y = parentRect.bottom - guideHeight - 4;
    if (y < parentRect.top + 4)
        y = parentRect.top + 4;

    HideTooltipWindow();
    const bool wasVisible = IsWindowVisible(g_guideWindow) != FALSE;
    UINT flags = SWP_NOACTIVATE | SWP_SHOWWINDOW;
    HWND insertAfter = HWND_TOP;
    if (wasVisible)
    {
        flags |= SWP_NOZORDER;
        insertAfter = nullptr;
    }
    SetWindowPos(g_guideWindow, insertAfter, x, y, guideWidth, guideHeight,
        flags);
    if (!wasVisible)
    {
        InvalidateRect(g_guideWindow, nullptr, FALSE);
        UpdateWindow(g_guideWindow);
        Log(L"[panel] guide opened");
    }
    g_guideVisible = true;
}

void DrawTooltip(HDC dc, const CombatSnapshot& state)
{
    const int destWidth = ScaleX(kTooltipWidth);
    const int destHeight = ScaleY(kTooltipHeight);
    const size_t tooltipBytes = static_cast<size_t>(kTooltipWidth) *
        kTooltipHeight * 4;
    const unsigned char* skin = LoadEmbeddedPixels(
        IDR_COMBAT_RANK_TOOLTIP, tooltipBytes);
    if (skin)
    {
        std::vector<unsigned char> composed(
            static_cast<size_t>(destWidth) * destHeight * 4);
        ScaleBgraNearest(skin, kTooltipWidth, kTooltipHeight, composed.data(),
            destWidth, destHeight);
        if (!BlitBgra(dc, composed.data(), destWidth, destHeight))
            FillRoundish(dc, { 0, 0, destWidth, destHeight }, RGB(5, 17, 28));
    }
    else
        FillRoundish(dc, { 0, 0, destWidth, destHeight }, RGB(5, 17, 28));

    DrawPanelTextShadowed(dc, L"战斗力区间", ScaleRect(19, 17, 112, 43),
        ScaleFont(12), FW_BOLD, RGB(211, 229, 239),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawPanelTextShadowed(dc, L"段位等级", ScaleRect(116, 17, 212, 43),
        ScaleFont(12), FW_BOLD, RGB(211, 229, 239),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    struct RankRow
    {
        unsigned int lower;
        unsigned int upper;
        const wchar_t* range;
        const wchar_t* name;
    };
    const RankRow rows[] = {
        { 0, 3099, L"0 - 3100", L"探索" },
        { 3100, 8099, L"3100 - 8100", L"开拓" },
        { 8100, 22999, L"8100 - 23000", L"无畏" },
        { 23000, 51999, L"23000 - 52000", L"征服" },
        { 52000, 64999, L"52000 - 65000", L"战绝" },
        { 65000, 129999, L"65000 - 130000", L"英杰" },
        { 130000, 0xFFFFFFFFu, L"130000 以上", L"武炼" },
    };
    for (size_t index = 0; index < _countof(rows); ++index)
    {
        const int top = ScalePx(43 + static_cast<int>(index) * 29);
        const int bottom = top + ScalePx(29);
        if (state.identityValid && state.totalScore >= rows[index].lower &&
            state.totalScore <= rows[index].upper)
        {
            HPEN highlight = CreatePen(PS_SOLID, 1, RGB(95, 200, 240));
            HGDIOBJ oldPen = SelectObject(dc, highlight);
            HGDIOBJ oldBrush = SelectObject(dc, GetStockObject(NULL_BRUSH));
            Rectangle(dc, ScaleX(17), top + 1, ScaleX(213), bottom);
            SelectObject(dc, oldBrush);
            SelectObject(dc, oldPen);
            DeleteObject(highlight);
        }
        DrawPanelTextShadowed(dc, rows[index].range,
            { ScaleX(20), top, ScaleX(112), bottom },
            ScaleFont(11), FW_BOLD, RGB(190, 211, 224),
            DT_CENTER | DT_VCENTER | DT_SINGLELINE);
        DrawPanelTextShadowed(dc, rows[index].name,
            { ScaleX(116), top, ScaleX(211), bottom },
            ScaleFont(11), FW_BOLD, RGB(236, 218, 170),
            DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    }
    DrawOverlayCursor(dc, g_tooltipWindow);
}

void DrawGuide(HDC dc, const CombatSnapshot& state)
{
    const int destWidth = ScaleX(kGuideWidth);
    const int destHeight = ScaleY(kGuideHeight);
    RECT client = { 0, 0, destWidth, destHeight };
    HBRUSH background = CreateSolidBrush(RGB(5, 16, 27));
    FillRect(dc, &client, background);
    DeleteObject(background);

    HBRUSH inner = CreateSolidBrush(RGB(8, 29, 45));
    HPEN outerPen = CreatePen(PS_SOLID, 1, RGB(123, 177, 202));
    HGDIOBJ oldBrush = SelectObject(dc, inner);
    HGDIOBJ oldPen = SelectObject(dc, outerPen);
    Rectangle(dc, 1, 1, client.right - 1, client.bottom - 1);
    SelectObject(dc, oldPen);
    SelectObject(dc, oldBrush);
    DeleteObject(outerPen);
    DeleteObject(inner);

    HBRUSH titleBrush = CreateSolidBrush(RGB(12, 55, 80));
    RECT titleBackground = { ScaleX(4), ScaleY(4), client.right - ScaleX(4),
        ScaleY(32) };
    FillRect(dc, &titleBackground, titleBrush);
    DeleteObject(titleBrush);
    DrawPanelTextShadowed(dc, L"战力提升攻略",
        { ScaleX(8), ScaleY(4), client.right - ScaleX(32), ScaleY(32) },
        ScaleFont(14), FW_BOLD, RGB(229, 242, 247),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawPanelTextShadowed(dc, L"×",
        ScaleRect(kGuideCloseLeft, kGuideCloseTop, kGuideCloseRight,
            kGuideCloseBottom),
        ScaleFont(16), FW_BOLD, RGB(241, 173, 97),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    wchar_t summary[96] = {};
    unsigned int equipmentBonusHundredths = 0;
    const bool equipmentBonusValid =
        TryComputeEquipmentBonusHundredths(state, &equipmentBonusHundredths);
    if (state.baseValid && equipmentBonusValid)
        swprintf_s(summary, L"当前：%u（%s）    装备：%u.%02u%%",
            state.totalScore, RankName(state.totalScore),
            equipmentBonusHundredths / 100,
            equipmentBonusHundredths % 100);
    else if (state.baseValid)
        swprintf_s(summary, L"当前：%u（%s）",
            state.totalScore, RankName(state.totalScore));
    else
        wcscpy_s(summary, L"正在读取当前角色战力...");
    DrawPanelTextShadowed(dc, summary,
        { ScaleX(12), ScaleY(38), client.right - ScaleX(12), ScaleY(64) },
        ScaleFont(11), FW_BOLD, RGB(255, 219, 105),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);

    HPEN separator = CreatePen(PS_SOLID, 1, RGB(38, 85, 110));
    oldPen = SelectObject(dc, separator);
    MoveToEx(dc, ScaleX(10), ScaleY(67), nullptr);
    LineTo(dc, client.right - ScaleX(10), ScaleY(67));
    SelectObject(dc, oldPen);
    DeleteObject(separator);

    const wchar_t* guideLines[] = {
        L"1. 优先补齐全部装备、称号、时装与光环",
        L"2. 宠物和所有宠物装备都会计入评分",
        L"3. 强化、增幅、套装和高品级装备可提分",
        L"4. 白字可叠加；普通黄字、爆伤只取最高",
        L"5. 黄追、爆追可叠加；所有攻击归入三攻",
    };
    for (size_t index = 0; index < _countof(guideLines); ++index)
    {
        RECT line = { ScaleX(15), ScaleY(73 + static_cast<int>(index) * 28),
            client.right - ScaleX(12),
            ScaleY(99 + static_cast<int>(index) * 28) };
        DrawPanelTextShadowed(dc, guideLines[index], line, ScaleFont(11),
            FW_BOLD, RGB(205, 226, 236),
            DT_LEFT | DT_VCENTER | DT_SINGLELINE);
    }

    DrawPanelTextShadowed(dc, L"再次点击“战力提升”关闭",
        { ScaleX(10), client.bottom - ScaleY(27), client.right - ScaleX(10),
            client.bottom - ScaleY(7) },
        ScaleFont(10), FW_BOLD, RGB(105, 172, 202),
        DT_CENTER | DT_VCENTER | DT_SINGLELINE);
    DrawOverlayCursor(dc, g_guideWindow);
}

BOOL CALLBACK FindGameWindowCallback(HWND window, LPARAM parameter)
{
    if (window == g_overlayWindow || window == g_tooltipWindow ||
        window == g_guideWindow ||
        !IsWindowVisible(window) || IsIconic(window) || GetParent(window) ||
        GetWindow(window, GW_OWNER))
        return TRUE;
    DWORD processId = 0;
    GetWindowThreadProcessId(window, &processId);
    if (processId != GetCurrentProcessId())
        return TRUE;
    wchar_t name[64] = {};
    GetClassNameW(window, name, _countof(name));
    if (wcscmp(name, L"地下城与勇士") != 0)
        return TRUE;
    RECT client = {};
    if (!GetClientRect(window, &client) || client.right < 640 ||
        client.bottom < 400)
        return TRUE;
    *reinterpret_cast<HWND*>(parameter) = window;
    return FALSE;
}

HWND FindGameWindow()
{
    HWND window = nullptr;
    EnumWindows(FindGameWindowCallback, reinterpret_cast<LPARAM>(&window));
    return window;
}

bool TryGetGameClientOrigin(POINT& origin)
{
    const HWND selected = FindGameWindow();
    if (selected != g_gameWindow)
    {
        g_gameWindow = selected;
        if (g_gameWindow)
            Log(L"[window] selected game hwnd=%p", g_gameWindow);
        else
            Log(L"[window] no DNF main window");
    }
    if (!g_gameWindow)
        return false;
    RECT client = {};
    origin = { 0, 0 };
    if (!IsWindowVisible(g_gameWindow) || IsIconic(g_gameWindow) ||
        !GetClientRect(g_gameWindow, &client) ||
        !ClientToScreen(g_gameWindow, &origin))
    {
        g_gameClientWidth = 0;
        g_gameClientHeight = 0;
        return false;
    }
    g_gameClientWidth = client.right - client.left;
    g_gameClientHeight = client.bottom - client.top;
    return g_gameClientWidth >= 640 && g_gameClientHeight >= 400;
}

bool GameIsForeground()
{
    return g_gameWindow && GetForegroundWindow() == g_gameWindow;
}

int MapUiToClient(int ui)
{
    if (g_gameClientHeight <= 0)
        return ui;
    return static_cast<int>(
        (static_cast<long long>(ui) * g_gameClientHeight) / g_uiHeight);
}

int ScaleBy(int design, int numerator, int denominator)
{
    if (design <= 0 || denominator <= 0)
        return design;
    return (design * numerator + denominator / 2) / denominator;
}

int ScaleRender(int design)
{
    const int height = g_gameClientHeight > 0 ? g_gameClientHeight : g_uiHeight;
    return ScaleBy(design, height, g_uiHeight);
}

int ScaleRankIcon(int design)
{
    return (std::max)(1, ScaleRender(design) - ScaleBy(design, 2, 5));
}

int ScaleY(int design)
{
    return ScaleRender(design);
}

int ScaleX(int design)
{
    return ScaleBy(design,g_gameClientWidth>0?g_gameClientWidth:g_uiWidth,g_uiWidth);
}

int ScalePx(int design)
{
    return ScaleRender(design);
}

int ScaleFont(int design)
{
    return ScaleRender(design);
}

RECT ScaleRect(int left, int top, int right, int bottom)
{
    RECT rect = { ScaleX(left), ScaleY(top), ScaleX(right), ScaleY(bottom) };
    return rect;
}

POINT ToDesignPoint(HWND window, POINT client, int designWidth, int designHeight)
{
    RECT area = {};
    if (!window || !GetClientRect(window, &area) || area.right <= 0 ||
        area.bottom <= 0 || designWidth <= 0 || designHeight <= 0)
        return client;
    POINT design = client;
    design.x = client.x * designWidth / area.right;
    design.y = client.y * designHeight / area.bottom;
    return design;
}

void UpdateOverlayWindow();
void PositionOverlay(int nativeX, int nativeY, int nativeWidth,
    int nativeHeight, bool restack);

enum OverlayHit
{
    OverlayHitNone = 0,
    OverlayHitClose,
    OverlayHitUpgrade,
    OverlayHitGuideClose,
    OverlayHitExpand,
    OverlayHitAffixPrev,
    OverlayHitAffixNext,
    OverlayHitPanel,
    OverlayHitGuide,
    OverlayHitTooltip,
    OverlayHitTitle,
};

bool PointInWindow(HWND window, POINT screen)
{
    RECT rect = {};
    return window && IsWindowVisible(window) &&
        GetWindowRect(window, &rect) && PtInRect(&rect, screen);
}

bool IsUpgradeButtonPoint(POINT point)
{
    RECT button = {
        kUpgradeButtonLeft, kUpgradeButtonTop,
        kUpgradeButtonRight, kUpgradeButtonBottom
    };
    return PtInRect(&button, point) != FALSE;
}

bool IsAffixPrevPoint(POINT point)
{
    RECT button = {
        kAffixArrowLeft, kAffixArrowTop,
        kAffixArrowRight, kAffixArrowBottom
    };
    return PtInRect(&button, point) != FALSE;
}

bool IsAffixNextPoint(POINT point)
{
    RECT button = {
        kAffixArrowNextLeft, kAffixArrowTop,
        kAffixArrowNextRight, kAffixArrowBottom
    };
    return PtInRect(&button, point) != FALSE;
}

OverlayHit HitTestOverlay(POINT screen)
{
    POINT client = screen;
    if (PointInWindow(g_overlayWindow, screen) &&
        ScreenToClient(g_overlayWindow, &client))
    {
        if (g_userDismissed)
            return OverlayHitExpand;
        client = ToDesignPoint(g_overlayWindow, client, kPanelWidth,
            kPanelHeight);
        RECT closeButton = {
            kCloseButtonLeft, kCloseButtonTop,
            kCloseButtonRight, kCloseButtonBottom
        };
        if (PtInRect(&closeButton, client))
            return OverlayHitClose;
        if(client.y>=0&&client.y<23)return OverlayHitTitle;
        if (IsUpgradeButtonPoint(client))
            return OverlayHitUpgrade;
        if (IsAffixPrevPoint(client))
            return OverlayHitAffixPrev;
        if (IsAffixNextPoint(client))
            return OverlayHitAffixNext;
        return OverlayHitPanel;
    }
    client = screen;
    if (g_guideVisible && PointInWindow(g_guideWindow, screen) &&
        ScreenToClient(g_guideWindow, &client))
    {
        client = ToDesignPoint(g_guideWindow, client, kGuideWidth,
            kGuideHeight);
        RECT closeButton = {
            kGuideCloseLeft, kGuideCloseTop,
            kGuideCloseRight, kGuideCloseBottom
        };
        if (PtInRect(&closeButton, client))
            return OverlayHitGuideClose;
        return OverlayHitGuide;
    }
    if (PointInWindow(g_tooltipWindow, screen))
        return OverlayHitTooltip;
    return OverlayHitNone;
}

bool IsCapturedMouseMessage(UINT message)
{
    return message == WM_LBUTTONDOWN || message == WM_LBUTTONUP ||
        message == WM_LBUTTONDBLCLK || message == WM_RBUTTONDOWN ||
        message == WM_RBUTTONUP || message == WM_RBUTTONDBLCLK ||
        message == WM_MBUTTONDOWN || message == WM_MBUTTONUP ||
        message == WM_MBUTTONDBLCLK || message == WM_MOUSEWHEEL ||
        message == WM_MOUSEHWHEEL || message == WM_XBUTTONDOWN ||
        message == WM_XBUTTONUP || message == WM_XBUTTONDBLCLK;
}

void HandleOverlayClick(OverlayHit hit)
{
    if (hit == OverlayHitExpand)
    {
        g_userDismissed = false;
        Log(L"[panel] expand tab restored sidecar");
        PositionOverlay(g_lastNativeX, g_lastNativeY, g_lastNativeW,
            g_lastNativeH, true);
        if (g_overlayWindow)
            InvalidateRect(g_overlayWindow, nullptr, FALSE);
        return;
    }
    if (hit == OverlayHitClose)
    {
        g_userDismissed = true;
        SetGuideVisible(false);
        HideTooltipWindow();
        Log(L"[panel] close button dismissed sidecar");
        PositionOverlay(g_lastNativeX, g_lastNativeY, g_lastNativeW,
            g_lastNativeH, true);
        if (g_overlayWindow)
            InvalidateRect(g_overlayWindow, nullptr, FALSE);
        return;
    }
    if (hit == OverlayHitGuideClose)
    {
        Log(L"[panel] guide closed");
        SetGuideVisible(false);
        return;
    }
    if (hit == OverlayHitAffixPrev || hit == OverlayHitAffixNext)
    {
        const DWORD now = GetTickCount();
        if (now - g_lastAffixPageTick < 200)
            return;
        g_lastAffixPageTick = now;
        if (hit == OverlayHitAffixNext)
            g_affixPage = (g_affixPage + 1) % kAffixPageCount;
        else
            g_affixPage = (g_affixPage + kAffixPageCount - 1) % kAffixPageCount;
        Log(L"[panel] affix page=%d", g_affixPage);
        if (g_overlayWindow)
            InvalidateRect(g_overlayWindow, nullptr, FALSE);
        return;
    }
    if (hit != OverlayHitUpgrade)
        return;
    const DWORD now = GetTickCount();
    if (now - g_lastGuideToggleTick < 400)
        return;
    g_lastGuideToggleTick = now;
    Log(L"[panel] guide toggle click");
    SetGuideVisible(!g_guideVisible);
}

void PollOverlayClicks()
{
    if (!g_overlayWindow || !IsWindowVisible(g_overlayWindow) ||
        !GameIsForeground())
    {
        g_leftButtonWasDown = false;
        g_pressHit = OverlayHitNone;
        g_panelDragging=false;
        return;
    }
    POINT cursor = {};
    if (!GetCursorPos(&cursor))
        return;
    const OverlayHit hit = HitTestOverlay(cursor);
    const bool down = (GetAsyncKeyState(VK_LBUTTON) & 0x8000) != 0;
    if (down && !g_leftButtonWasDown) {
        g_pressHit = hit;
        if(hit==OverlayHitTitle){g_panelDragging=true;g_panelOffsetSet=true;g_panelDragStart=cursor;g_panelDragX=g_panelOffsetX;g_panelDragY=g_panelOffsetY;}
    }
    if(g_panelDragging&&down){
        g_panelOffsetX=g_panelDragX+static_cast<int>((static_cast<long long>(cursor.x-g_panelDragStart.x)*g_uiWidth)/(std::max)(1,g_gameClientWidth));
        g_panelOffsetY=g_panelDragY+static_cast<int>((static_cast<long long>(cursor.y-g_panelDragStart.y)*g_uiHeight)/(std::max)(1,g_gameClientHeight));
        PositionOverlay(g_lastNativeX,g_lastNativeY,g_lastNativeW,g_lastNativeH,false);
    }
    if (!down && g_leftButtonWasDown && g_pressHit != OverlayHitNone &&
        g_pressHit == hit)
        HandleOverlayClick(static_cast<OverlayHit>(g_pressHit));
    if (!down)
    {
        g_panelDragging=false;
        g_pressHit = OverlayHitNone;
    }
    g_leftButtonWasDown = down;
    if (hit != OverlayHitNone &&
        (cursor.x != g_lastCursorX || cursor.y != g_lastCursorY))
    {
        g_lastCursorX = cursor.x;
        g_lastCursorY = cursor.y;
        if (g_overlayWindow)
            InvalidateRect(g_overlayWindow, nullptr, FALSE);
        if (g_guideVisible && g_guideWindow)
            InvalidateRect(g_guideWindow, nullptr, FALSE);
        if (g_tooltipWindow && IsWindowVisible(g_tooltipWindow))
            InvalidateRect(g_tooltipWindow, nullptr, FALSE);
    }
}

void UnhookDnfWindow()
{
    if (!g_gameWindow || !g_originalDnfWindowProc)
        return;
    KillTimer(g_gameWindow, kDnfPollTimer);
    KillTimer(g_gameWindow, kDnfClickTimer);
    SetWindowLongPtrW(g_gameWindow, GWLP_WNDPROC,
        reinterpret_cast<LONG_PTR>(g_originalDnfWindowProc));
    g_originalDnfWindowProc = nullptr;
}

LRESULT CALLBACK ProxyDnfWindowProc(HWND window, UINT message, WPARAM wParam,
    LPARAM lParam)
{
    WNDPROC original = g_originalDnfWindowProc;
    if (!original)
        return DefWindowProcW(window, message, wParam, lParam);
    if (message == WM_DESTROY || message == WM_NCDESTROY)
    {
        KillTimer(window, kDnfPollTimer);
        KillTimer(window, kDnfClickTimer);
        if (g_guideWindow && IsWindow(g_guideWindow))
            DestroyWindow(g_guideWindow);
        g_guideWindow = nullptr;
        g_guideVisible = false;
        if (g_tooltipWindow && IsWindow(g_tooltipWindow))
            DestroyWindow(g_tooltipWindow);
        g_tooltipWindow = nullptr;
        if (g_overlayWindow && IsWindow(g_overlayWindow))
            DestroyWindow(g_overlayWindow);
        g_overlayWindow = nullptr;
        if (message == WM_DESTROY && g_originalDnfWindowProc)
        {
            SetWindowLongPtrW(window, GWLP_WNDPROC,
                reinterpret_cast<LONG_PTR>(g_originalDnfWindowProc));
            g_originalDnfWindowProc = nullptr;
        }
        if (message == WM_NCDESTROY)
            g_gameWindow = nullptr;
    }
    if (message != WM_DESTROY && message != WM_NCDESTROY)
        PollOverlayClicks();
    if (message == WM_TIMER && wParam == kDnfPollTimer)
    {
        UpdateOverlayWindow();
        return 0;
    }
    if (message == WM_TIMER && wParam == kDnfClickTimer)
    {
        PollOverlayClicks();
        return 0;
    }
    if (IsCapturedMouseMessage(message))
    {
        const bool dragging=g_panelDragging;
        PollOverlayClicks();
        POINT cursor = {};
        if (dragging || (GetCursorPos(&cursor) && HitTestOverlay(cursor) != OverlayHitNone))
            return 0;
    }
    else if (message == WM_MOUSEMOVE)
        PollOverlayClicks();
    if ((message == WM_MOVE || message == WM_SIZE) &&
        g_overlayWindow && IsWindowVisible(g_overlayWindow))
        UpdateOverlayWindow();
    return CallWindowProcW(original, window, message, wParam, lParam);
}

bool InstallDnfBridge()
{
    HWND window = FindGameWindow();
    if (!window)
    {
        Log(L"[window] dnf hwnd missing for 90CN-style bridge");
        return false;
    }
    g_gameWindow = window;
    WNDPROC original = reinterpret_cast<WNDPROC>(
        GetWindowLongPtrW(window, GWLP_WNDPROC));
    if (!original)
        return false;
    g_originalDnfWindowProc = original;
    SetLastError(ERROR_SUCCESS);
    const LONG_PTR previous = SetWindowLongPtrW(window, GWLP_WNDPROC,
        reinterpret_cast<LONG_PTR>(&ProxyDnfWindowProc));
    if (!previous && GetLastError() != ERROR_SUCCESS)
    {
        g_originalDnfWindowProc = nullptr;
        Log(L"[window] subclass failed error=%lu",
            static_cast<unsigned long>(GetLastError()));
        return false;
    }
    g_originalDnfWindowProc = reinterpret_cast<WNDPROC>(previous);
    if (!SetTimer(window, kDnfPollTimer, kOverlayRefreshMilliseconds, nullptr))
        Log(L"[window] dnf timer failed error=%lu",
            static_cast<unsigned long>(GetLastError()));
    if (!SetTimer(window, kDnfClickTimer, kClickPollMilliseconds, nullptr))
        Log(L"[window] click timer failed error=%lu",
            static_cast<unsigned long>(GetLastError()));
    Log(L"[window] 90CN-style bridge hwnd=%p timer=%u", window,
        kOverlayRefreshMilliseconds);
    return true;
}

LRESULT CALLBACK OverlayProc(HWND window, UINT message, WPARAM wParam,
    LPARAM lParam)
{
    switch (message)
    {
    case WM_TIMER:
        if (window == g_overlayWindow && wParam == kOverlayTimerId)
            UpdateOverlayWindow();
        return 0;
    case WM_ERASEBKGND:
        return 1;
    case WM_NCHITTEST:
        return HTTRANSPARENT;
    case WM_MOUSEACTIVATE:
        return MA_NOACTIVATE;
    case WM_PAINT:
    {
        PAINTSTRUCT paint = {};
        HDC target = BeginPaint(window, &paint);
        RECT area={};GetClientRect(window,&area);
        HDC dc=CreateCompatibleDC(target);
        HBITMAP bitmap=CreateCompatibleBitmap(target,area.right,area.bottom);
        HGDIOBJ old=nullptr;
        const bool buffered=dc && bitmap;
        if(buffered) old=SelectObject(dc,bitmap);
        HDC memory=dc;
        if(!buffered) dc=target;
        if (window == g_tooltipWindow)
            DrawTooltip(dc, g_snapshot);
        else if (window == g_guideWindow)
            DrawGuide(dc, g_snapshot);
        else if (g_userDismissed)
            DrawCollapsedTab(dc);
        else
            DrawPanel(dc, g_snapshot);
        if(buffered){BitBlt(target,0,0,area.right,area.bottom,dc,0,0,SRCCOPY);SelectObject(dc,old);}
        if(bitmap) DeleteObject(bitmap);
        if(memory) DeleteDC(memory);
        EndPaint(window, &paint);
        return 0;
    }
    case WM_DESTROY:
        if (window == g_overlayWindow)
        {
            KillTimer(window, kOverlayTimerId);
            if (g_guideWindow && IsWindow(g_guideWindow))
                DestroyWindow(g_guideWindow);
            g_guideWindow = nullptr;
            g_guideVisible = false;
            if (g_tooltipWindow && IsWindow(g_tooltipWindow))
                DestroyWindow(g_tooltipWindow);
            g_tooltipWindow = nullptr;
            g_overlayWindow = nullptr;
            if (!g_overlayOnDnfThread)
                PostQuitMessage(0);
        }
        return 0;
    case WM_NCDESTROY:
        if (g_overlayWindow == window)
            g_overlayWindow = nullptr;
        if (g_tooltipWindow == window)
            g_tooltipWindow = nullptr;
        if (g_guideWindow == window)
        {
            g_guideWindow = nullptr;
            g_guideVisible = false;
        }
        break;
    default:
        break;
    }
    return DefWindowProcW(window, message, wParam, lParam);
}

bool RegisterLayeredClass(const wchar_t* className, WNDPROC procedure)
{
    WNDCLASSEXW windowClass = {};
    windowClass.cbSize = sizeof(windowClass);
    windowClass.style = CS_HREDRAW | CS_VREDRAW;
    windowClass.hInstance = g_module;
    windowClass.lpfnWndProc = procedure;
    windowClass.lpszClassName = className;
    if (RegisterClassExW(&windowClass) ||
        GetLastError() == ERROR_CLASS_ALREADY_EXISTS)
        return true;
    Log(L"[window] class registration failed: %lu",
        static_cast<unsigned long>(GetLastError()));
    return false;
}

HWND CreateLayeredPopup(const wchar_t* className, int width, int height,
    bool clickThrough, BYTE alpha)
{
    DWORD exStyle = WS_EX_LAYERED | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;
    if (clickThrough)
        exStyle |= WS_EX_TRANSPARENT;
    HWND window = CreateWindowExW(exStyle, className, L"", WS_POPUP, 0, 0,
        width, height, g_gameWindow, nullptr, g_module, nullptr);
    if (!window)
    {
        Log(L"[window] creation failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        return nullptr;
    }
    if (!SetLayeredWindowAttributes(window, 0, alpha, LWA_ALPHA))
    {
        Log(L"[window] alpha setup failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        DestroyWindow(window);
        return nullptr;
    }
    return window;
}

bool EnsureOverlayWindow()
{
    if (g_overlayWindow && IsWindow(g_overlayWindow) &&
        g_tooltipWindow && IsWindow(g_tooltipWindow) &&
        g_guideWindow && IsWindow(g_guideWindow))
        return true;
    if (!RegisterLayeredClass(kOverlayClass, OverlayProc))
        return false;
    if (!g_overlayWindow || !IsWindow(g_overlayWindow))
    {
        HWND window = CreateLayeredPopup(kOverlayClass, kPanelWidth,
            kPanelHeight, true, 250);
        if (!window)
            return false;
        g_overlayWindow = window;
        g_overlayOnDnfThread = g_gameWindow &&
            GetWindowThreadProcessId(g_gameWindow, nullptr) ==
                GetCurrentThreadId();
        Log(L"[window] overlay owner=%p dnf-thread=%d", g_gameWindow,
            g_overlayOnDnfThread ? 1 : 0);
    }
    if (!g_tooltipWindow || !IsWindow(g_tooltipWindow))
    {
        HWND tooltip = CreateLayeredPopup(kOverlayClass, kTooltipWidth,
            kTooltipHeight, true, 252);
        if (!tooltip)
            return false;
        g_tooltipWindow = tooltip;
    }
    if (!g_guideWindow || !IsWindow(g_guideWindow))
    {
        HWND guide = CreateLayeredPopup(kOverlayClass, kGuideWidth,
            kGuideHeight, true, 252);
        if (!guide)
            return false;
        g_guideWindow = guide;
    }
    return true;
}

RECT OverlayClientRect(int nativeX,int nativeY,int nativeWidth)
{
    const int width = ScaleX(g_userDismissed ? kCollapsedWidth : kPanelWidth);
    const int height = ScaleY(g_userDismissed ? kCollapsedHeight : kPanelHeight);
    int x = ScaleX(nativeX + nativeWidth + 2 + g_panelOffsetX);
    int y = MapUiToClient(nativeY - kPersonalPanelFrameTop + g_panelOffsetY);
    // Default attachment stays at the actual right frame edge. In particular,
    // never flip a right-docked panel over the native character's equipment.
    // A user drag is kept visible and stored in logical UI units, so moving or
    // resizing the game does not turn it back into a desktop-pixel offset.
    if(g_panelOffsetSet&&x+width>g_gameClientWidth-4)x=g_gameClientWidth-width-4;
    if (x < 4)
        x = 4;
    if (y + height > g_gameClientHeight - 4)
        y = g_gameClientHeight - height - 4;
    if (y < 4)
        y = 4;
    return RECT{x,y,x+width,y+height};
}

void PositionOverlay(int nativeX, int nativeY, int nativeWidth,
    int nativeHeight, bool restack)
{
    POINT origin = {};
    if (!TryGetGameClientOrigin(origin)) return;
    const RECT bounds=OverlayClientRect(nativeX,nativeY,nativeWidth);
    const int x=bounds.left,y=bounds.top,width=bounds.right-bounds.left,height=bounds.bottom-bounds.top;
    const int screenX = origin.x + x;
    const int screenY = origin.y + y;
    // 90：hWndInsertAfter=parent 会把 popup 放到 DirectX owner 后面看不见，
    // 所以首次显示用 HWND_TOP，停在进程非 TOPMOST 带顶层。A21 不要每 100ms
    // 再 HWND_TOP，否则会压过游戏内软件鼠标和其它 Win32 分层窗。
    const bool moved = screenX != g_lastWindowX || screenY != g_lastWindowY;
    const bool resized = width != g_lastOverlayWidth ||
        height != g_lastOverlayHeight;
    const bool hidden = !IsWindowVisible(g_overlayWindow);
    UINT flags = SWP_NOACTIVATE | SWP_SHOWWINDOW;
    HWND insertAfter = HWND_TOP;
    if (!restack && !resized)
    {
        flags |= SWP_NOZORDER;
        insertAfter = nullptr;
    }
    if(moved || resized || hidden || restack)
        SetWindowPos(g_overlayWindow, insertAfter, screenX, screenY, width, height, flags);
    if (moved || hidden || resized)
        Log(L"[panel] place native=%d,%d %dx%d sidecar=%d,%d %dx%d client=%d min=%d restack=%d collapsed=%d",
            nativeX, nativeY, nativeWidth, nativeHeight, x, y, width, height,
            g_gameClientHeight, kUiHeight, restack ? 1 : 0,
            g_userDismissed ? 1 : 0);
    g_lastWindowX = screenX;
    g_lastWindowY = screenY;
    g_lastOverlayWidth = width;
    g_lastOverlayHeight = height;
}

void RefreshTooltipWindow(bool gameForeground)
{
    if (g_userDismissed || g_guideVisible || !g_overlayWindow ||
        !g_tooltipWindow || !IsWindowVisible(g_overlayWindow) ||
        !gameForeground)
    {
        HideTooltipWindow();
        return;
    }
    RECT hover = {};
    POINT cursor = {};
    if (!GetWindowRect(g_overlayWindow, &hover) || !GetCursorPos(&cursor))
    {
        HideTooltipWindow();
        return;
    }
    hover.top += ScalePx(kRankHoverTop);
    hover.bottom = hover.top + ScalePx(kRankHoverBottom - kRankHoverTop);
    if (!PtInRect(&hover, cursor))
    {
        HideTooltipWindow();
        return;
    }

    RECT panel = {};
    RECT parent = {};
    if (!GetWindowRect(g_overlayWindow, &panel) || !g_gameWindow ||
        !GetWindowRect(g_gameWindow, &parent))
    {
        HideTooltipWindow();
        return;
    }
    const int tooltipWidth = ScaleX(kTooltipWidth);
    const int tooltipHeight = ScaleY(kTooltipHeight);
    int x = panel.right + 4;
    int y = panel.top + ScalePx(34);
    if (x + tooltipWidth > parent.right - 4)
        x = panel.left - tooltipWidth - 4;
    if (x < parent.left + 4)
        x = parent.left + 4;
    if (y + tooltipHeight > parent.bottom - 4)
        y = parent.bottom - tooltipHeight - 4;
    if (y < parent.top + 4)
        y = parent.top + 4;
    const bool wasVisible = IsWindowVisible(g_tooltipWindow) != FALSE;
    UINT flags = SWP_NOACTIVATE | SWP_SHOWWINDOW;
    HWND insertAfter = HWND_TOP;
    if (wasVisible)
    {
        flags |= SWP_NOZORDER;
        insertAfter = nullptr;
    }
    SetWindowPos(g_tooltipWindow, insertAfter, x, y, tooltipWidth,
        tooltipHeight, flags);
    if (!wasVisible)
        InvalidateRect(g_tooltipWindow, nullptr, FALSE);
}

void UpdateOverlayWindow()
{
    RequestLoadNotice();
    POINT origin = {};
    if (!TryGetGameClientOrigin(origin))
    {
        HideOverlayWindow();
        return;
    }
    const bool foreground = GameIsForeground();
    if (!foreground)
    {
        HideOverlayWindow();
        return;
    }

    __try
    {
        int nativeX = 0;
        int nativeY = 0;
        int nativeWidth = 0;
        int nativeHeight = 0;
        int personalId = -1;
        if (!TryGetPersonalInfoRect(nativeX, nativeY, nativeWidth,
                nativeHeight, personalId, true))
        {
            static bool waitingLogged = false;
            if (g_personalInfoOpen)
                Log(L"[panel] personal info closed");
            else if (!waitingLogged)
            {
                waitingLogged = true;
                Log(L"[panel] waiting for personal info window %d",
                    kPersonalInfoWindowId);
            }
            g_personalInfoOpen = false;
            g_userDismissed = false;
            g_affixPage = 0;
            HideOverlayWindow();
            return;
        }

        g_lastNativeX = nativeX;
        g_lastNativeY = nativeY;
        g_lastNativeW = nativeWidth;
        g_lastNativeH = nativeHeight;
        RefreshSnapshot(true);
        if (!EnsureOverlayWindow())
            return;
        if (g_userDismissed)
        {
            HideGuideWindow();
            HideTooltipWindow();
        }
        const bool wasVisible = IsWindowVisible(g_overlayWindow) != FALSE;
        const bool sizeMatches = (g_userDismissed
            ? (g_lastOverlayWidth == ScaleX(kCollapsedWidth) &&
                g_lastOverlayHeight == ScaleY(kCollapsedHeight))
            : (g_lastOverlayWidth == ScaleX(kPanelWidth) &&
                g_lastOverlayHeight == ScaleY(kPanelHeight)));
        PositionOverlay(nativeX, nativeY, nativeWidth, nativeHeight,
            !wasVisible || !sizeMatches);
        static CombatSnapshot painted = {};
        static int paintedPage = -1;
        static bool paintedCollapsed = false;
        if (!wasVisible || !sizeMatches || painted.totalScore != g_snapshot.totalScore ||
            painted.characterId != g_snapshot.characterId ||
            painted.equippedItems != g_snapshot.equippedItems ||
            painted.level != g_snapshot.level ||
            painted.baseScore != g_snapshot.baseScore ||
            paintedPage != g_affixPage ||
            paintedCollapsed != g_userDismissed ||
            wcscmp(painted.profession, g_snapshot.profession) != 0)
        {
            painted = g_snapshot;
            paintedPage = g_affixPage;
            paintedCollapsed = g_userDismissed;
            InvalidateRect(g_overlayWindow, nullptr, FALSE);
            if (g_guideVisible && g_guideWindow)
                InvalidateRect(g_guideWindow, nullptr, FALSE);
        }
        if (!g_userDismissed && g_guideVisible)
            SetGuideVisible(true);
        RefreshTooltipWindow(foreground);
        PollOverlayClicks();
        if (!g_personalInfoOpen)
            Log(L"[panel] shown id=%d score=%u items=%u", personalId,
                g_snapshot.totalScore, g_snapshot.equippedItems);
        g_personalInfoOpen = true;
    }
    __except (EXCEPTION_EXECUTE_HANDLER)
    {
        Log(L"[panel] refresh exception code=0x%08X", GetExceptionCode());
        HideOverlayWindow();
    }
}

bool WaitForIdentityQuiet()
{
    for (;;)
    {
        RefreshSnapshot(false);
        if (g_snapshot.identityValid)
            break;
        Sleep(kIdentityPollMilliseconds);
    }
    Log(L"[init] identity ready id=%u; quiet %u ms before windows",
        g_snapshot.characterId, kIdentityQuietMilliseconds);
    const DWORD quietUntil = GetTickCount() + kIdentityQuietMilliseconds;
    while (static_cast<LONG>(GetTickCount() - quietUntil) < 0)
        Sleep(100);
    return true;
}

DWORD WINAPI OverlayThreadProc(LPVOID)
{
    RequestLoadNotice();
    InterlockedExchange(&g_overlayStartupResult, 1);
    if (g_overlayReadyEvent)
        SetEvent(g_overlayReadyEvent);

    const DWORD quietUntil = GetTickCount() + kLaunchQuietMilliseconds;
    while (static_cast<LONG>(GetTickCount() - quietUntil) < 0)
        Sleep(100);

    if (!ValidateClient())
    {
        Log(L"[init] client signature mismatch after quiet");
        return 0;
    }
    WaitForIdentityQuiet();

    if (InstallDnfBridge())
    {
        if (!RegisterLayeredClass(kOverlayClass, OverlayProc))
            return 0;
        Log(L"[init] follow armed dnf-thread overlay %ums",
            kOverlayRefreshMilliseconds);
        while (g_gameWindow && IsWindow(g_gameWindow))
            Sleep(500);
        UnhookDnfWindow();
        return 0;
    }

    Log(L"[window] continue without dnf subclass");
    if (!EnsureOverlayWindow())
        return 0;
    if (!SetTimer(g_overlayWindow, kOverlayTimerId,
            kOverlayRefreshMilliseconds, nullptr))
    {
        Log(L"[window] refresh timer failed: %lu",
            static_cast<unsigned long>(GetLastError()));
        DestroyWindow(g_overlayWindow);
        g_overlayWindow = nullptr;
        return 0;
    }
    Log(L"[init] follow armed overlay-thread fallback");

    MSG message = {};
    while (GetMessageW(&message, nullptr, 0, 0) > 0)
    {
        TranslateMessage(&message);
        DispatchMessageW(&message);
    }
    UnhookDnfWindow();
    if (g_guideWindow && IsWindow(g_guideWindow))
        DestroyWindow(g_guideWindow);
    g_guideWindow = nullptr;
    g_guideVisible = false;
    if (g_tooltipWindow && IsWindow(g_tooltipWindow))
        DestroyWindow(g_tooltipWindow);
    g_tooltipWindow = nullptr;
    if (g_overlayWindow && IsWindow(g_overlayWindow))
        DestroyWindow(g_overlayWindow);
    g_overlayWindow = nullptr;
    return 0;
}

bool LoadPluginConfig()
{
    g_config.debug = GetPrivateProfileIntW(L"General", L"Debug", 0,
        g_configPath.c_str()) != 0;
    g_config.enabled = GetPrivateProfileIntW(L"General", L"Enabled", 0,
        g_configPath.c_str()) != 0;
    return true;
}
}

extern "C" __declspec(dllexport) BOOL ClientPatchPluginInit()
{

    // S5A3 owns native UI controls on the game window thread. Keep the
    // upstream formula and assets, but never create an OS overlay worker.
    InterlockedExchange(&g_started,1); return TRUE;

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

extern "C" __declspec(dllexport) void __cdecl S5A3PowerFeed(const S5PowerFrame* f) {
    if(!f || f->size!=sizeof(*f)) return;
    AcquireSRWLockExclusive(&g_s5Lock);g_s5Frame=*f;
    g_s5Frame.tick=GetTickCount();
    if(g_s5Frame.stats.baseValid) {
        ComputeBaseScore(g_s5Frame.stats);
        FinalizeCombatScores(g_s5Frame.stats,g_s5Frame.equipmentV2);
    }
    ReleaseSRWLockExclusive(&g_s5Lock);
}

extern "C" __declspec(dllexport) void __cdecl S5A3PowerCompute(combat_power::CombatSnapshot* state, unsigned equipment, unsigned* bonus, wchar_t* rank) {
    if(!state || !bonus || !rank) return;
    if(state->baseValid){ComputeBaseScore(*state);FinalizeCombatScores(*state,equipment);}
    *bonus=0;TryComputeEquipmentBonusHundredths(*state,bonus);
    wcsncpy_s(rank,16,RankName(state->totalScore),_TRUNCATE);
}
