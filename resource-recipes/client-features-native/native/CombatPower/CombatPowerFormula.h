#pragma once

#include <cstddef>
#include <cwchar>

// 战力分数与词缀汇总。改公式、段位阈值、白/黄/爆合并规则只动这里。
// 客户端读字段和画窗仍在 CombatPower.cpp。
namespace combat_power
{
constexpr int kCombatSlotBegin = 12;
constexpr int kCombatSlotEnd = 23;
constexpr int kCombatSlotPet = 25;
constexpr unsigned int kOccupiedSlotScore = 200;
constexpr unsigned int kFormulaVersion = 6;

struct CombatSnapshot
{
    bool identityValid = false;
    bool equipmentValid = false;
    bool baseValid = false;
    bool affixesValid = false;
    unsigned int characterId = 0;
    int job = -1;
    int firstGrow = 0;
    int awakening = 0;
    unsigned int level = 0;
    unsigned int equippedItems = 0;
    unsigned int combatSlots = 0;
    unsigned int hpMax = 0;
    unsigned int mpMax = 0;
    int strength = 0;
    int vitality = 0;
    int intelligence = 0;
    int spirit = 0;
    unsigned int physicalAttack = 0;
    unsigned int magicalAttack = 0;
    unsigned int independentAttack = 0;
    // 单位 0.1%。白字跨件加算；普通黄字/爆伤跨件取最高；黄追/爆追跨件加算。
    unsigned int whiteDamageTenths = 0;
    unsigned int yellowDamageTenths = 0;
    unsigned int criticalDamageTenths = 0;
    unsigned int yellowAdditionalTenths = 0;
    unsigned int criticalAdditionalTenths = 0;
    unsigned int allAttackTenths = 0;
    unsigned int baseScore = 0;
    unsigned int equipmentScore = 0;
    unsigned int totalScore = 0;
    wchar_t name[64] = {};
    wchar_t profession[32] = {};
};

struct ItemDamageAffixes
{
    double white = 0;
    double yellow = 0;
    double critical = 0;
    double yellowAdditional = 0;
    double criticalAdditional = 0;
    double allAttack = 0;
};

bool IsCombatSlot(int slot);
unsigned int CombatSlotEquipmentScore(unsigned int upgrade,
    unsigned int amplifyType, unsigned int amplifyValue);
bool ComputeBaseScore(CombatSnapshot& snapshot);
void CollectScriptAffixes(unsigned int root, ItemDamageAffixes& part);
void CombineEquippedAffixes(ItemDamageAffixes& total,
    const ItemDamageAffixes& part);
void ApplyAffixesToSnapshot(CombatSnapshot& snapshot,
    const ItemDamageAffixes& affixes);
void FinalizeCombatScores(CombatSnapshot& snapshot, unsigned int equipmentV2);
bool TryComputeEquipmentBonusHundredths(const CombatSnapshot& state,
    unsigned int* output);
const wchar_t* RankName(unsigned int score);
}
