package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是「强化/增幅成功率 buff 药水」的真源：把 op44 使用的那一行 stackable 翻成
// 模式 + PVF `[upgrade prob increase]` 加成 + `[stat change duration]` 持续时间。
//
// 判据 = `[stackable type]` 首列记号（正是 op44 契约里已经认的那两族，
// 见 inventory/use_upgrad_buff.go）：
//
//	[item upgrad buff usual upgrade]    → "reinforce"（强化的奥秘 / 强化秘药 / 凯丽的幸福强化药剂 …）
//	[item upgrad buff amplify upgrade]  → "amplify"（永恒的增幅的秘密 / 壕罐的增幅秘笈 …）
//
// 当前104有20个已登记模板（16强化、4增幅）。原生类型确认其用途，
// 缺省按已批准口径乘1.1；显式PVF [upgrade prob increase] 仍优先，0关闭。
// 不向原生stackable插入新标签，不把类型后的堆叠参数当作概率。
//
// 为什么由 op44 解析、op50 读账：op50 请求体里没有任何 buff 字段（线上 62/62 条都是
// 16+nameLen 字节、无尾随数据），服务端只能在自己扣药水的时候记账。
func (s *Service) alignedUpgradeRateBuffResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.UpgradeRateBuffResolver, error) {
	if opcode != dnfenum.CmdPacketUseStackable && opcode != dnfenum.CmdPacketUseSharedEffectItem {
		return nil, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errDungeonDropSourceRequired
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(itemID int64) (alignedcmd.UpgradeRateBuffResolution, error) {
		return resolveCurrentUpgradeRateBuff(catalog, source, itemID)
	}, nil
}

// resolveCurrentUpgradeRateBuff 解析一件 stackable 的成功率 buff 身份。
// 非该族 / 解析不到 / PVF 没写持续时间 —— 都返回零值 + nil error（"这一行不记账"）。
// 只有目录或文档读失败才返回 error，让调用方如实记日志。
func resolveCurrentUpgradeRateBuff(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, itemID int64) (alignedcmd.UpgradeRateBuffResolution, error) {
	resolution := alignedcmd.UpgradeRateBuffResolution{}
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return resolution, nil
	}
	if catalog == nil || source == nil {
		return resolution, errDungeonDropSourceRequired
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil || definition.Kind != dungeonDropItemStackable {
		return resolution, nil
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return resolution, nil
	}
	mode := currentUpgradeRateBuffMode(document, definition.StackableType)
	if mode == "" {
		return resolution, nil
	}
	resolution.Mode = mode
	resolution.BonusWeight = 10000 // approved default for these two proven native families
	resolution.PVFPath = definition.PVFPath
	if values, present := document.Section("upgrade prob increase"); present {
		if len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 || values[0].Int > 100000 {
			return resolution, fmt.Errorf("invalid upgrade buff probability field")
		}
		resolution.BonusWeight = int(values[0].Int)
	}
	if duration, found := document.Int("stat change duration"); found && duration > 0 {
		resolution.DurationMillis = duration
	}
	return resolution, nil
}

// currentUpgradeRateBuffStackableToken 取 `[stackable type]` 原文的首列记号
// （与 inventory/use_upgrad_buff.go 的 stackableTypeToken 同口径：PVF 原文是
// "记号 + 堆叠上限"两列，例如 `[item upgrad buff usual upgrade] 1000`）。
func currentUpgradeRateBuffStackableToken(stackableType string) string {
	normalized := strings.ReplaceAll(stackableType, "`", "")
	start := strings.Index(normalized, "[")
	if start < 0 {
		return ""
	}
	if strings.TrimSpace(strings.Trim(normalized[:start], "\"'")) != "" {
		return ""
	}
	end := strings.Index(normalized[start:], "]")
	if end < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(normalized[start : start+end+1]))
}

// currentUpgradeRateBuffMode 从 `[stackable type]`（文档优先）判定模式。
// 逐字比对记号，不做前缀匹配：`[item upgrad buff usual upgrade]` 与
// `[item upgrad buff amplify upgrade]` 是仅有的两族。
func currentUpgradeRateBuffMode(document *dnfpvf.Document, fallbackStackableType string) string {
	stackableType := fallbackStackableType
	if document != nil {
		if value, found := document.Text("stackable type"); found {
			stackableType = value
		}
	}
	token := currentUpgradeRateBuffStackableToken(stackableType)
	switch token {
	case "[item upgrad buff usual upgrade]":
		return "reinforce"
	case "[item upgrad buff amplify upgrade]":
		return "amplify"
	default:
		return ""
	}
}
