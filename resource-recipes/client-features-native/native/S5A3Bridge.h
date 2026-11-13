#pragma once
#include <stdint.h>
#include "CombatPower/CombatPowerFormula.h"
struct S5DamageRow { wchar_t name[64]; uint64_t damage; uint32_t hits; int32_t job; };
struct S5DpsFrame { uint32_t size,count,elapsed,reserved; uint64_t generation; S5DamageRow rows[8]; };
struct S5PowerFrame { uint32_t size,tick; int32_t visible,x,y,width,height; combat_power::CombatSnapshot stats; uint32_t equipmentV2; int32_t uiWidth,uiHeight; };
