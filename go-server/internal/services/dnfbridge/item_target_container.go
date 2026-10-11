package dnfbridge

import (
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是**"这次发放的道具应该落到哪个容器 / 哪一段"的唯一判定**。
//
// 为什么必须只有一处：2026-09-24/25 现场两条报障都属于"每条路径各写一套目标段判定"——
//  1. 商城新买的宠物更名卡（item 25，`[stackable type] [creature]`）落在主背包消耗品页
//     （65..120），而邮件领取的同名卡在宠物消耗品段（list 7 189..238）：因为商城路径用的
//     `isCurrentCeraShopPetConsumable` 只认 `[feed]`，邮件路径自己写了一套认 `[feed]`+`[creature]`；
//  2. 材料类道具入包时进了非材料段：目录的 `dungeonDropStackableSlotRange` 只认精确的
//     `[material]` 前缀，`[material instant item]` 这类家族掉进默认的消耗品页；
//     教程奖励则把整个发放的槽位硬写成 65..120。
//
// 判定真源 = 运行时 PVF 的**道具家族**（`[stackable type]` / `[equipment type]`）+ 既有投影
// （`item_kind` / `pvf_path`）。全量取证（本地 `runtime\data\dnf\Script.pvf`，54789 个
// stackable、140862 个 equipment）得到的家族 → 段表：
//
//	[feed] / [creature] (stackable)  → list 7  189..238  宠物消耗品段（宠物饲料 24 / 更名卡 25）
//	[material]                       → list 0  121..176  材料页（物品 id 3277 / 10300011 …）
//	[material instant item]          → list 0  121..176  材料页（id 6515 fp/fpcubepiece_clear.stk）
//	[material expert job]            → list 0  233..288  副职业材料页
//	[quest]                          → list 0  177..232  任务品页
//	[avatar emblem]                  → list 0  289..344  徽章页
//	其它 stackable（[waste]/[etc]/[booster]/…）→ list 0 65..120 消耗品页
//	[flag gem] (stackable)           → list 38 49..97    公会守护珠页
//	[creature] (equipment)           → list 7  0..139    宠物本体
//	artifact * (equipment)           → list 7  140..188  宠物装备
//	[flag] (equipment)               → list 38 0..48     公会勋章页
//	avatar（装备部位规则 class=avatar）→ list 1  0..500   装扮栏
//	其它 equipment                   → list 0  9..64     装备页
//
// list 38（公会勋章/守护珠）是**同一个容器的两个 49 格分页**，wire 容器 id 就是 38
// （`currentGuildMedalInventoryListType` / `currentGuildMedalActorSlot` 已有证据）。
// 之前缺这两个家族，于是「公会勋章获得时直接进装备栏(9..64)、守护珠直接进消耗品栏
// (65..120)」，而拖进勋章栏又被拒（现场报障 2026-09-26）。真源与 A21 参考实现一致：
// `ItemSlotBoundService.TryGetSlotRange` 把 `KindGuildMedal` 映射到
// `GuildMedal 0..48`、`KindGuardianGem` 映射到 `GuildMedal 49..97`；
// `ItemMetadataResolver` 把 `[equipment type] flag` 判成 KindGuildMedal、
// `[stackable type] flag gem` 判成 KindGuardianGem。
//
// list 7 的 189..238 是既有已验证结论（`inventory.sortSegment` 注释引当前客户端
// `sub_232A9F0` 接受 189..238 含端点；现场库里宠物饲料/更名卡也一直落在这段）。
const (
	currentPVFPetConsumableSlotStart int16 = currentCeraShopPetConsumableSlotStart
	currentPVFPetConsumableSlotEnd   int16 = currentCeraShopPetConsumableSlotEnd
	currentPVFPetBodySlotStart       int16 = 0
	currentPVFPetBodySlotEnd         int16 = 139
	currentPVFPetArtifactSlotStart   int16 = currentBoosterPetArtifactStart
	currentPVFPetArtifactSlotEnd     int16 = currentBoosterPetArtifactEnd
	currentPVFAvatarSlotStart        int16 = 0
	currentPVFAvatarSlotEnd          int16 = 500
	currentPVFMainEquipmentSlotStart int16 = 9
	currentPVFMainEquipmentSlotEnd   int16 = 64
	currentPVFMainConsumableStart    int16 = 65
	currentPVFMainConsumableEnd      int16 = 120
	currentPVFMainMaterialStart      int16 = 121
	currentPVFMainMaterialEnd        int16 = 176
	currentPVFMainMaterialExtraStart int16 = 345
	currentPVFMainMaterialExtraEnd   int16 = 359
	currentPVFMainQuestStart         int16 = 177
	currentPVFMainQuestEnd           int16 = 232
	currentPVFMainExpertJobStart     int16 = 233
	currentPVFMainExpertJobEnd       int16 = 288
	currentPVFMainEmblemStart        int16 = 289
	currentPVFMainEmblemEnd          int16 = 344
)

// currentGuildMedalPVFType 是运行时 PVF 里公会勋章的唯一装备类型标记。
// `currentEquipmentPlacementRuleForPVFType` 已把它映射到 actor 装备槽 32
// （穿戴端点），这里只用于**发放容器**判定：同一枚勋章必须先落在勋章栏
// list 38 的勋章页 0..48，再由玩家（或右键）从 38 穿到 3/32。
const currentGuildMedalPVFType = "[flag]"

// currentPVFItemContainer 是"这次发放落到哪里"的判定结果。ListType 为
// dnfrepo.MainInventoryListType 时 SlotStart..SlotEnd 就是主背包里的分段。
type currentPVFItemContainer struct {
	ListType  byte
	SlotStart int16
	SlotEnd   int16
	// 下面六个标记让调用方不必再各自重算一遍家族判定。
	PetConsumable bool // list 7 189..238（[feed]/[creature] 堆叠品）
	PetBody       bool // list 7 0..139（[creature] 装备）
	PetArtifact   bool // list 7 140..188（artifact *）
	Avatar        bool // list 1（装扮）
	GuildMedal    bool // list 38 0..48（[equipment type] `[flag]` 公会勋章）
	GuardianGem   bool // list 38 49..97（[stackable type] `[flag gem]` 公会守护珠）
}

// currentPVFItemTargetContainer 是唯一的容器/分段判定。所有发放路径
// （邮件领取、商城购买、点券礼包、booster 礼盒、魔盒/随机奖励、NPC 商店、
// 副本掉落与翻牌、任务/活动奖励、教程奖励、GM 面板邮件）都必须问它，
// 不允许再写第二套 switch。
func currentPVFItemTargetContainer(definition dungeonDropItemDefinition) currentPVFItemContainer {
	switch definition.Kind {
	case dungeonDropItemStackable:
		family := normalizeDungeonDropStackableType(definition.StackableType)
		if currentPVFPetConsumableFamily(family) {
			return currentPVFItemContainer{
				ListType:      currentPetInventoryListType,
				SlotStart:     currentPVFPetConsumableSlotStart,
				SlotEnd:       currentPVFPetConsumableSlotEnd,
				PetConsumable: true,
			}
		}
		if currentGuardianGemStackableType(family) {
			// 守护珠（[stackable type] `[flag gem]`）与勋章共用 wire 容器 38，
			// 但分页不同：勋章 0..48、守护珠 49..97（客户端 sub_E9AC00/sub_E9AC30
			// 把 98 格拆成两个 49 格页）。缺这条判定时守护珠落进默认消耗品页
			// （list 0 65..120）——现场报障「守护珠获得时直接进入消耗品栏」。
			return currentPVFItemContainer{
				ListType:    currentGuardianGemInventoryListType,
				SlotStart:   currentGuardianGemPageSlotStart,
				SlotEnd:     currentGuardianGemPageSlotEnd,
				GuardianGem: true,
			}
		}
		start, end := currentPVFMainStackableSegment(family)
		return currentPVFItemContainer{
			ListType:  dnfrepo.MainInventoryListType,
			SlotStart: start,
			SlotEnd:   end,
		}
	case dungeonDropItemEquipment:
		if isCurrentCeraShopCreatureItem(definition) {
			return currentPVFItemContainer{
				ListType:  currentPetInventoryListType,
				SlotStart: currentPVFPetBodySlotStart,
				SlotEnd:   currentPVFPetBodySlotEnd,
				PetBody:   true,
			}
		}
		if currentPVFEquipmentLooksLikePetArtifact(definition) {
			return currentPVFItemContainer{
				ListType:    currentPetInventoryListType,
				SlotStart:   currentPVFPetArtifactSlotStart,
				SlotEnd:     currentPVFPetArtifactSlotEnd,
				PetArtifact: true,
			}
		}
		if rule, supported := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType); supported {
			switch {
			case rule.class == currentEquipmentPlacementClassAvatar:
				return currentPVFItemContainer{
					ListType:  1,
					SlotStart: currentPVFAvatarSlotStart,
					SlotEnd:   currentPVFAvatarSlotEnd,
					Avatar:    true,
				}
			case rule.pvfType == currentGuildMedalPVFType:
				// 公会勋章（[equipment type] `[flag]`）必须发到勋章栏 list 38 的
				// 勋章页 0..48，而不是装备页 9..64——现场报障「勋章获得时直接进入
				// 装备栏、无法被拖拽进入勋章栏-勋章（右键却可直接装备）」。
				return currentPVFItemContainer{
					ListType:   currentGuildMedalInventoryListType,
					SlotStart:  currentGuildMedalPageSlotStart,
					SlotEnd:    currentGuildMedalPageSlotEnd,
					GuildMedal: true,
				}
			}
		}
		return currentPVFItemContainer{
			ListType:  dnfrepo.MainInventoryListType,
			SlotStart: currentPVFMainEquipmentSlotStart,
			SlotEnd:   currentPVFMainEquipmentSlotEnd,
		}
	default:
		// 未知 kind（解析失败的家族）：按消耗品页落，和改动前的默认行为一致。
		return currentPVFItemContainer{
			ListType:  dnfrepo.MainInventoryListType,
			SlotStart: currentPVFMainConsumableStart,
			SlotEnd:   currentPVFMainConsumableEnd,
		}
	}
}

// currentPVFPetConsumableFamily 是宠物消耗品家族（list 7 189..238）的**唯一判据**：
// 运行时 PVF 里 `[feed]`（宠物饲料 24）与 `[creature]`（更名卡 25 / 亲密度道具 26 …）
// 两类堆叠品都属于宠物背包的消耗品段。
//
// **现场根因（2026-09-24/25）**：`isCurrentCeraShopPetConsumable` 之前只认 `[feed]`，
// 于是商城购买/booster/魔盒把 `[creature]` 的更名卡放到了主背包 65..120，
// 而邮件路径（自己写了 `[feed]`+`[creature]`）放进了 list 7 —— 同一件道具两条路径两个段。
func currentPVFPetConsumableFamily(family string) bool {
	switch family {
	case "[feed]", "[creature]":
		return true
	default:
		return false
	}
}

// currentPVFMainStackableSegment 返回 stackable 家族在**主背包(list 0)**里的分段。
// 家族一律小写、去反引号与首尾空白（normalizeDungeonDropStackableType）。
func currentPVFMainStackableSegment(family string) (int16, int16) {
	switch {
	case strings.HasPrefix(family, "[material expert job]"):
		return currentPVFMainExpertJobStart, currentPVFMainExpertJobEnd
	case strings.HasPrefix(family, "[material"):
		// [material] / [material instant item] … 全是材料家族（现场 id 3277、
		// 6515、10300011）。旧代码只认带右括号的 `[material]`，于是
		// `[material instant item]` 掉进默认的消耗品页。
		if strings.HasSuffix(family, "4") {
			return currentPVFMainMaterialExtraStart, currentPVFMainMaterialExtraEnd
		}
		return currentPVFMainMaterialStart, currentPVFMainMaterialEnd
	case strings.HasPrefix(family, "[quest"):
		return currentPVFMainQuestStart, currentPVFMainQuestEnd
	case strings.HasPrefix(family, "[avatar emblem]"):
		return currentPVFMainEmblemStart, currentPVFMainEmblemEnd
	default:
		return currentPVFMainConsumableStart, currentPVFMainConsumableEnd
	}
}

// currentPVFEquipmentLooksLikePetArtifact 判定宠物装备（artifact red/blue/green）。
func currentPVFEquipmentLooksLikePetArtifact(definition dungeonDropItemDefinition) bool {
	return strings.HasPrefix(normalizeMagicBoxPVFType(definition.EquipmentType), "artifact ")
}
