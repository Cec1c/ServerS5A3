package dnfbridge

import (
	"strconv"
	"strings"
)

// 邮件附件的耐久证据（装备/时装）。
//
// 现场问题：GM 面板发放的道具是 0 耐久，领到角色背包后也无法修理（用户与操作者在
// 2026-09-24 确认"只发现了装备"）。原因是发放路径只写了 {ItemID, Count}：
//
//   - `gmtool.handleItemGrant`（api_items.go）用 dnfrepo.AppendSystemMail 发邮件，
//     附件没有 Extra、也没有 0x77 raw 行；
//   - 领取端 mailboxAttachmentStack 的耐久只能来自 attachment.Extra 或
//     resolution.DefaultExtra，两者都是空的，于是落地堆栈没有 durability /
//     max_durability；
//   - 客户端把缺这两个键的条目显示成 0 耐久（场景物品行与邮件附件行读的
//     都是这两个键：current_item_list_stack_projection 的 0x0B）；
//   - 服务端修理路径要求这两条证据，缺了就失败关闭 ——
//     inventory.repairDurabilityOf → ErrRepairDurabilityMissing，
//     equip.repairBagDurabilityOf → ErrRepairCostMissing。
//     这正是"领到的装备修不了"，而**不是**要放宽校验去绕过。
//
// 真源只能是运行时 PVF：`dungeon_drop_catalog.ResolveItem` 已按
// [durability] / [maximum durability] / [max durability] 的顺序解析出
// dungeonDropItemDefinition.Durability（见 dungeon_drop_catalog.go:241-251），
// 与掉落拾取、翻牌奖励、点券套餐发放写进堆栈的耐久同源。
//
// 堆叠物没有耐久语义（ResolveItem 只在装备分支解析 [durability]），所以这里
// 用 Kind == dungeonDropItemEquipment 硬门禁，绝不给堆叠物写耐久。

// currentMailAttachmentDurabilityExtraMissing 报告附件是否还缺耐久证据。
func currentMailAttachmentDurabilityExtraMissing(extra map[string]string) bool {
	if extra == nil {
		return true
	}
	return strings.TrimSpace(extra["durability"]) == "" ||
		strings.TrimSpace(extra["max_durability"]) == ""
}

// currentMailAttachmentQualitySeedExtra 保证装备/时装附件在**领取**时带上实例身份
// quality_seed（0x06 行的品质种子）。
//
// 为什么必须有：装备行的 quality_seed 就是这只装备的实例身份。
// `equip` owner 的 op19 move 对"没有 0x77 行的装备行"必须用耐久 + 品质种子重建 raw 行
// （equip/owner.go 的 buildCurrentEquipmentRawEntryFromMoveEvidence 要求
// itemquality.Valid(quality_seed) 且客户端回传非零 source identity）。GM 面板发的
// 附件既没有 raw 行、也没有任何 Extra，重建必然失败 → 事务内返回
// ErrMoveRawEntryMissing → 服务端静默不回包，客户端本地乐观穿上，重登又掉下来
// —— 操作者看到的"武器自动脱下"和这里同源。
//
// 种子由与掉落拾取/翻牌奖励同一套 itemquality 语义生成（crypto/rand，[1, TopSeed]）；
// 附件自己已经带种子时（玩家邮寄的实例装备）绝不覆盖。
func currentMailAttachmentQualitySeedExtra(
	base map[string]string,
	definition dungeonDropItemDefinition,
) (map[string]string, error) {
	if definition.Kind != dungeonDropItemEquipment {
		return base, nil
	}
	if strings.TrimSpace(base["quality_seed"]) != "" {
		return base, nil
	}
	seed, err := newCurrentEquipmentQualitySeed()
	if err != nil {
		return base, err
	}
	if base == nil {
		base = make(map[string]string, 3)
	}
	base["quality_seed"] = strconv.FormatUint(uint64(seed), 10)
	return base, nil
}

// mergeCurrentMailAttachmentDurabilityExtra 把 PVF 声明的基础耐久并进附件 Extra。
// 缺键才补，绝不覆盖附件（或玩家邮寄时随行下发）已有的实例耐久；新造的装备实例
// 按 PVF 语义是满耐久，所以两个键写同一个值。
func mergeCurrentMailAttachmentDurabilityExtra(
	base map[string]string,
	definition dungeonDropItemDefinition,
) map[string]string {
	if definition.Kind != dungeonDropItemEquipment || definition.Durability == 0 {
		return base
	}
	value := strconv.FormatUint(uint64(definition.Durability), 10)
	if base == nil {
		base = make(map[string]string, 2)
	}
	if strings.TrimSpace(base["durability"]) == "" {
		base["durability"] = value
	}
	if strings.TrimSpace(base["max_durability"]) == "" {
		base["max_durability"] = value
	}
	return base
}
