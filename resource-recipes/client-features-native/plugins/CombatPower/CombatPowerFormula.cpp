#define NOMINMAX
#include "CombatPowerFormula.h"

#include <windows.h>

#include <algorithm>
#include <cstdint>
#include <cstring>
#include <limits>
#include <vector>

namespace combat_power
{
namespace
{
constexpr unsigned int kScriptIfThen = 0x25;
constexpr unsigned int kScriptAddAbsolute = 0x26;
constexpr unsigned int kScriptStatCond = 0x17;
constexpr unsigned int kFloatOneBits = 0x3F800000u;
constexpr unsigned int kFloatNegOneBits = 0xBF800000u;
constexpr unsigned int kFloatHundredBits = 0x42C80000u;
constexpr unsigned int kFloatTenBits = 0x41200000u;
constexpr int kScriptWalkLimit = 128;
constexpr int kScriptVectorLimit = 64;
constexpr int kThenWalkLimit = 160;

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

} // namespace

bool TryComputeEquipmentBonusHundredths(const CombatSnapshot& state,
    unsigned int* output)
{
    if (!output || !state.baseValid || state.baseScore == 0 ||
        !state.equipmentValid)
        return false;
    const unsigned int yellowTenths = state.yellowDamageTenths +
        state.yellowAdditionalTenths;
    const unsigned int criticalTenths = state.criticalDamageTenths +
        state.criticalAdditionalTenths;
    const long double multiplier =
        (1.0L + static_cast<long double>(state.whiteDamageTenths) / 1000.0L) *
        (1.0L + static_cast<long double>(yellowTenths) / 1000.0L) *
        (1.0L + static_cast<long double>(criticalTenths) / 1000.0L);
    const long double affixValue =
        static_cast<long double>(state.baseScore) * (multiplier - 1.0L);
    unsigned int affixScore = 0;
    if (affixValue >= static_cast<long double>(state.equipmentScore))
        affixScore = state.equipmentScore;
    else if (affixValue > 0.0L)
        affixScore = static_cast<unsigned int>(affixValue);
    const unsigned int equipmentOnly =
        state.equipmentScore - affixScore;
    const unsigned long long hundredths =
        static_cast<unsigned long long>(equipmentOnly) * 10000ull /
        state.baseScore;
    if (hundredths > 999999ull)
        return false;
    *output = static_cast<unsigned int>(hundredths);
    return true;
}


const wchar_t* RankName(unsigned int score)
{
    if (score < 3100) return L"探索";
    if (score < 8100) return L"开拓";
    if (score < 23000) return L"无畏";
    if (score < 52000) return L"征服";
    if (score < 65000) return L"战绝";
    if (score < 130000) return L"英杰";
    return L"武炼";
}

bool IsCombatSlot(int slot)
{
    return (slot >= kCombatSlotBegin && slot <= kCombatSlotEnd) ||
        slot == kCombatSlotPet;
}

unsigned int CombatSlotEquipmentScore(unsigned int upgrade,
    unsigned int amplifyType, unsigned int amplifyValue)
{
    unsigned int score = kOccupiedSlotScore + upgrade * upgrade * 10;
    if (amplifyType >= 1 && amplifyType <= 4 && amplifyValue > 0)
        score += amplifyValue * 20;
    return score;
}

bool ComputeBaseScore(CombatSnapshot& snapshot)
{
    const unsigned int fourDim =
        static_cast<unsigned int>(snapshot.strength + snapshot.vitality +
            snapshot.intelligence + snapshot.spirit);
    if (fourDim == 0 || snapshot.hpMax < 100)
        return false;
    snapshot.baseScore = fourDim * 8 +
        (snapshot.hpMax + snapshot.mpMax) / 20 +
        snapshot.physicalAttack + snapshot.magicalAttack +
        snapshot.independentAttack;
    snapshot.baseValid = true;
    return true;
}

void FinalizeCombatScores(CombatSnapshot& snapshot, unsigned int equipmentV2)
{
    const unsigned int yellowTenths = snapshot.yellowDamageTenths +
        snapshot.yellowAdditionalTenths;
    const unsigned int criticalTenths = snapshot.criticalDamageTenths +
        snapshot.criticalAdditionalTenths;
    const long double multiplier =
        (1.0L + static_cast<long double>(snapshot.whiteDamageTenths) /
            1000.0L) *
        (1.0L + static_cast<long double>(yellowTenths) / 1000.0L) *
        (1.0L + static_cast<long double>(criticalTenths) / 1000.0L);
    unsigned int affixScore = 0;
    if (snapshot.baseValid && multiplier > 1.0L)
        affixScore = static_cast<unsigned int>(
            static_cast<long double>(snapshot.baseScore) * (multiplier - 1.0L));
    snapshot.equipmentScore = equipmentV2 + affixScore;
    snapshot.totalScore = snapshot.baseScore + snapshot.equipmentScore;
}

bool LooksHeapPointer(unsigned int address)
{
    return address >= 0x20000000u && address < 0x7F000000u &&
        address != kFloatOneBits && address != kFloatNegOneBits &&
        address != kFloatHundredBits;
}

bool LooksScriptVtable(unsigned int head)
{
    return head >= 0x02700000u && head <= 0x03800000u;
}

double BitsToWholePercent(unsigned int bits)
{
    if (bits == kFloatOneBits || bits == kFloatNegOneBits ||
        bits == kFloatHundredBits)
        return 0;
    float value = 0;
    std::memcpy(&value, &bits, sizeof(value));
    if (value < 2.f || value > 80.f)
        return 0;
    const int nearest = static_cast<int>(value >= 0.f ? value + 0.5f :
        value - 0.5f);
    if (value - static_cast<float>(nearest) > 0.01f ||
        static_cast<float>(nearest) - value > 0.01f)
        return 0;
    return nearest;
}

double LastPercentBeforeSentinel(const unsigned int* words, int count)
{
    if (!words || count <= 0)
        return 0;
    // 只认 `1.0,1.0,-1.0` 哨兵前的整百分数。不要回退成块里任意 (x,1,y)
    // 的最大值：那会把技能冷却/MP、坐标一类 66 误收成黄字。
    for (int index = 0; index + 2 < count; ++index)
    {
        if (words[index] != kFloatOneBits ||
            words[index + 1] != kFloatOneBits ||
            words[index + 2] != kFloatNegOneBits)
            continue;
        for (int cursor = index - 1; cursor >= 0; --cursor)
        {
            const double percent = BitsToWholePercent(words[cursor]);
            if (percent > 0)
                return percent;
        }
    }
    return 0;
}

// 编译后的百分数在 `1.0` 后面：`(29, 1.0, 30)` 取 30，`(10, 8, 1.0, 8)` 取 8。
// 不要取 `1.0` 前面的数，技能冷却会把 66 放在单位 1.0 之前。
double LastPercentAfterOne(const unsigned int* words, int count)
{
    double best = 0;
    if (!words || count < 2)
        return 0;
    for (int index = 0; index + 1 < count; ++index)
    {
        if (words[index] != kFloatOneBits)
            continue;
        const double percent = BitsToWholePercent(words[index + 1]);
        if (percent > best)
            best = percent;
    }
    return best;
}

// `(30, 1.0, 10)` 和 `(29, 1.0, 30)` 是互斥分支，取较大值。
// `(66, 1.0, 0)` 只有一侧是百分数，不收——那是技能冷却。
double LastPercentPairAroundOne(const unsigned int* words, int count)
{
    double best = 0;
    if (!words || count < 3)
        return 0;
    for (int index = 0; index + 2 < count; ++index)
    {
        if (words[index + 1] != kFloatOneBits)
            continue;
        const double left = BitsToWholePercent(words[index]);
        const double right = BitsToWholePercent(words[index + 2]);
        if (left <= 0 || right <= 0)
            continue;
        const double pair = left > right ? left : right;
        if (pair > best)
            best = pair;
    }
    return best;
}

void ReadDwords(unsigned int address, unsigned int* output, int count)
{
    if (!output || count <= 0)
        return;
    std::memset(output, 0, static_cast<size_t>(count) * 4);
    if (!LooksHeapPointer(address) ||
        !IsReadableRange(reinterpret_cast<const void*>(address),
            static_cast<size_t>(count) * 4))
        return;
    __try
    {
        std::memcpy(output, reinterpret_cast<const void*>(address),
            static_cast<size_t>(count) * 4);
    }
    __except (EXCEPTION_EXECUTE_HANDLER)
    {
        std::memset(output, 0, static_cast<size_t>(count) * 4);
    }
}

bool LooksScriptNodeHead(unsigned int head)
{
    return head < 0x80u || LooksScriptVtable(head);
}

bool LooksPointerVector(unsigned int begin, unsigned int end)
{
    return LooksHeapPointer(begin) && LooksHeapPointer(end) && end > begin &&
        end - begin <= 0x100 && ((end - begin) % 4) == 0;
}

bool LooksFollowablePointer(unsigned int address)
{
    return LooksHeapPointer(address) && BitsToWholePercent(address) == 0;
}

double ReadNearestWhiteTuple(unsigned int address)
{
    // `[add absolute damage] all % N` 编成离 0x26 不远的 `10, 8, 1.0, N`。
    // 不要扫任意 `1.0, percent`：同页上戒指/手镯的 30 会算进白字。
    const int back = 0x1000;
    const int forward = 0x3000;
    if (address < static_cast<unsigned int>(back))
        return 0;
    const unsigned int start = address - back;
    const int count = (back + forward) / 4;
    std::vector<unsigned int> window(static_cast<size_t>(count));
    ReadDwords(start, window.data(), count);
    double best = 0;
    int bestDist = 0x7fffffff;
    const int origin = back / 4;
    for (int index = 0; index + 3 < count; ++index)
    {
        if (window[index] != kFloatTenBits ||
            window[index + 2] != kFloatOneBits)
            continue;
        const double percent = BitsToWholePercent(window[index + 3]);
        if (percent <= 0)
            continue;
        int dist = index - origin;
        if (dist < 0)
            dist = -dist;
        if (dist < bestDist)
        {
            bestDist = dist;
            best = percent;
        }
    }
    return best;
}

double ReadWhitePercentAtNode(unsigned int address)
{
    unsigned int packed[8] = {};
    if (address >= 32)
    {
        ReadDwords(address - 32, packed, 8);
        const double value = LastPercentBeforeSentinel(packed, 8);
        if (value > 0)
            return value;
    }
    const double tuple = ReadNearestWhiteTuple(address);
    if (tuple > 0)
        return tuple;
    if (address >= 16)
    {
        ReadDwords(address - 16, packed, 8);
        return LastPercentBeforeSentinel(packed, 8);
    }
    return 0;
}

double ReadSentinelAround(unsigned int address)
{
    unsigned int packed[16] = {};
    if (address >= 32)
    {
        ReadDwords(address - 32, packed, 16);
        const double value = LastPercentBeforeSentinel(packed, 16);
        if (value > 0)
            return value;
    }
    if (address >= 16)
    {
        ReadDwords(address - 16, packed, 16);
        const double value = LastPercentBeforeSentinel(packed, 16);
        if (value > 0)
            return value;
    }
    ReadDwords(address, packed, 16);
    const double around = LastPercentBeforeSentinel(packed, 16);
    if (around > 0)
        return around;
    return LastPercentAfterOne(packed, 16);
}

void KeepGreaterPercent(double& dest, double value)
{
    if (value > dest)
        dest = value;
}

double CollectThenPercents(unsigned int start, bool* sawStat)
{
    if (!LooksFollowablePointer(start))
        return 0;

    unsigned int nodes[kThenWalkLimit] = {};
    int queued = 0;
    int cursor = 0;
    auto enqueue = [&](unsigned int address) -> void
    {
        if (!LooksFollowablePointer(address) || queued >= kThenWalkLimit)
            return;
        for (int index = 0; index < queued; ++index)
        {
            if (nodes[index] == address)
                return;
        }
        nodes[queued++] = address;
    };
    enqueue(start);

    double best = 0;
    while (cursor < queued)
    {
        const unsigned int address = nodes[cursor++];
        unsigned int words[16] = {};
        ReadDwords(address, words, 16);
        if (words[0] == kScriptAddAbsolute)
            continue;
        if (words[0] == kScriptStatCond && sawStat)
            *sawStat = true;
        KeepGreaterPercent(best, LastPercentAfterOne(words, 16));
        KeepGreaterPercent(best, LastPercentPairAroundOne(words, 16));
        if (LooksPointerVector(words[1], words[2]))
        {
            const int count = static_cast<int>((words[2] - words[1]) / 4);
            const int n = count > kScriptVectorLimit ? kScriptVectorLimit : count;
            unsigned int vector[kScriptVectorLimit] = {};
            ReadDwords(words[1], vector, n);
            for (int index = 0; index < n; ++index)
                enqueue(vector[index]);
        }
        for (int index = 1; index < 12; ++index)
            enqueue(words[index]);
    }
    return best;
}

void CollectScriptAffixes(unsigned int root, ItemDamageAffixes& part)
{
    if (!LooksFollowablePointer(root))
        return;

    unsigned int nodes[kScriptWalkLimit] = {};
    int queued = 0;
    int cursor = 0;
    auto enqueue = [&](unsigned int address) -> void
    {
        if (!LooksFollowablePointer(address) || queued >= kScriptWalkLimit)
            return;
        for (int index = 0; index < queued; ++index)
        {
            if (nodes[index] == address)
                return;
        }
        nodes[queued++] = address;
    };
    enqueue(root);

    bool hasWhiteCmd = false;
    double white = 0;
    double yellow = 0;
    double critical = 0;

    auto considerWhiteLeaf = [&](unsigned int address) -> void
    {
        unsigned int leaf[4] = {};
        ReadDwords(address, leaf, 4);
        if (leaf[0] == kScriptAddAbsolute && leaf[1] == kScriptStatCond)
        {
            hasWhiteCmd = true;
            KeepGreaterPercent(white, ReadWhitePercentAtNode(address));
            return;
        }
        if (address >= 32)
        {
            ReadDwords(address - 32, leaf, 4);
            if (leaf[0] == kScriptAddAbsolute && leaf[1] == kScriptStatCond)
            {
                hasWhiteCmd = true;
                KeepGreaterPercent(white, ReadWhitePercentAtNode(address - 32));
            }
        }
    };

    auto followChild = [&](unsigned int child) -> void
    {
        if (!LooksFollowablePointer(child))
            return;
        unsigned int childWords[3] = {};
        ReadDwords(child, childWords, 3);
        if (LooksScriptNodeHead(childWords[0]) ||
            LooksPointerVector(childWords[1], childWords[2]))
            enqueue(child);
        considerWhiteLeaf(child);
        if (childWords[0] == kScriptAddAbsolute &&
            childWords[1] == kScriptStatCond && child >= 32)
            considerWhiteLeaf(child - 32);
    };

    auto collectThenChildren = [&](unsigned int address, const unsigned int* words,
        double& thenValue, bool& localStat) -> void
    {
        KeepGreaterPercent(thenValue, ReadSentinelAround(address));
        unsigned int children[kScriptVectorLimit + 12] = {};
        int childCount = 0;
        auto addChild = [&](unsigned int child) -> void
        {
            if (!LooksFollowablePointer(child) || childCount >= kScriptVectorLimit + 12)
                return;
            for (int index = 0; index < childCount; ++index)
            {
                if (children[index] == child)
                    return;
            }
            children[childCount++] = child;
        };
        if (LooksPointerVector(words[1], words[2]))
        {
            const int count = static_cast<int>((words[2] - words[1]) / 4);
            const int n = count > kScriptVectorLimit ? kScriptVectorLimit : count;
            unsigned int vector[kScriptVectorLimit] = {};
            ReadDwords(words[1], vector, n);
            for (int index = 0; index < n; ++index)
                addChild(vector[index]);
        }
        for (int index = 1; index < 12; ++index)
            addChild(words[index]);
        for (int index = 0; index < childCount; ++index)
        {
            const unsigned int child = children[index];
            unsigned int childWords[3] = {};
            ReadDwords(child, childWords, 3);
            if (childWords[0] == kScriptStatCond)
                localStat = true;
            else if (childWords[0] != kScriptAddAbsolute)
                KeepGreaterPercent(thenValue, ReadSentinelAround(child));
            followChild(child);
        }
    };

    while (cursor < queued)
    {
        const unsigned int address = nodes[cursor++];
        unsigned int words[16] = {};
        ReadDwords(address, words, 16);
        const unsigned int head = words[0];
        if (head == kScriptAddAbsolute && words[1] == kScriptStatCond)
        {
            hasWhiteCmd = true;
            KeepGreaterPercent(white, ReadWhitePercentAtNode(address));
        }
        else if (head == kScriptIfThen)
        {
            // 0x25 是 if/then。数字在旁路 1,1,-1 哨兵块。
            // 子树有 0x17（戒指 HP 条件）→ [increase critical damage] 取最高；
            // 否则 → [increase damage] 取最高。THEN 里的 [stat by condition]
            // 技能冷却/MP 也是 0x17，但百分数在 0x17 上，这里不收。
            // A21 Script.pvf 没有 add increase damage / all attack bonus rate，
            // 黄追/爆追/全攻保持 0，与 90 parse.go 对这三类 tag 的空目录一致。
            double thenValue = 0;
            bool localStat = false;
            collectThenChildren(address, words, thenValue, localStat);
            KeepGreaterPercent(thenValue, CollectThenPercents(address,
                &localStat));
            if (thenValue > 0)
            {
                if (localStat)
                    KeepGreaterPercent(critical, thenValue);
                else
                    KeepGreaterPercent(yellow, thenValue);
            }
            continue;
        }

        if (LooksPointerVector(words[1], words[2]))
        {
            const int count = static_cast<int>((words[2] - words[1]) / 4);
            const int n = count > kScriptVectorLimit ? kScriptVectorLimit : count;
            unsigned int vector[kScriptVectorLimit] = {};
            ReadDwords(words[1], vector, n);
            for (int index = 0; index < n; ++index)
                followChild(vector[index]);
        }

        for (int index = 1; index < 12; ++index)
            followChild(words[index]);
    }

    if (hasWhiteCmd)
        part.white = white;
    part.yellow = yellow;
    part.critical = critical;
}

void CombineEquippedAffixes(ItemDamageAffixes& total,
    const ItemDamageAffixes& part)
{
    total.white += part.white;
    KeepGreaterPercent(total.yellow, part.yellow);
    KeepGreaterPercent(total.critical, part.critical);
    total.yellowAdditional += part.yellowAdditional;
    total.criticalAdditional += part.criticalAdditional;
    total.allAttack += part.allAttack;
}

unsigned int PercentToTenths(double percent)
{
    if (percent <= 0)
        return 0;
    const double tenths = percent * 10.0;
    const double rounded = tenths >= 0
        ? tenths + 0.5 : tenths - 0.5;
    if (rounded >= 65535.0)
        return 65535;
    if (rounded <= 0)
        return 0;
    return static_cast<unsigned int>(rounded);
}

void ApplyAffixesToSnapshot(CombatSnapshot& snapshot,
    const ItemDamageAffixes& affixes)
{
    snapshot.whiteDamageTenths = PercentToTenths(affixes.white);
    snapshot.yellowDamageTenths = PercentToTenths(affixes.yellow);
    snapshot.criticalDamageTenths = PercentToTenths(affixes.critical);
    snapshot.yellowAdditionalTenths = PercentToTenths(affixes.yellowAdditional);
    snapshot.criticalAdditionalTenths =
        PercentToTenths(affixes.criticalAdditional);
    snapshot.allAttackTenths = PercentToTenths(affixes.allAttack);
    snapshot.affixesValid = snapshot.equipmentValid;
}

}
