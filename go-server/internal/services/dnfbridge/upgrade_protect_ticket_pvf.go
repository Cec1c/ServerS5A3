package dnfbridge

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是 op50「失败即摧毁」档的**保护券真源**：把背包里一行 stackable 的
// template id 翻成运行时 PVF 的保护券身份，交给 inventory 域层判定"能不能拿它挡掉
// 这次摧毁"。
//
// 判据只有 PVF 的 `[action type]`，与厂商参考一致（C#
// `ItemUpgradeConsumableResolver.FromActionType`，ItemUpgradeModels.cs:239-243）：
//
//	`[protect equipment]`         / `[new protect equipment]`         → reinforce
//	`[amplify protect equipment]` / `[new amplify protect equipment]` → amplify
//
// `[action type]` 段里的整数参数 = 装备失败后回落到的等级（参考的
// `FailureRetainLevel`，ItemUpgradeModels.cs:295）。线上 9 张真实保护券这一节**没有**
// 整数参数，所以回落等级 = 0（回到未强化 / 非增幅状态），与它们自己的 `[explain]`
// 「使装备回到未强化状态 / 使装备回到非增幅状态 / 装备的强化等级归零」逐字一致。
// 取证（`pvfscriptedit.exe -dump-text`，Script.pvf
// sha256=afa00e752189a0e14326f92b3264ec59afb7fd1d2c12ae33b7a831c4adb505fe）：
//
//	stackable/10303001/10303749.stk  [action type] `[amplify protect equipment]`
//	stackable/10093001/10093372.stk  [action type] `[amplify protect equipment]`
//	stackable/10098001/10098989.stk  [action type] `[amplify protect equipment]`
//	stackable/10164001/10164047.stk  [action type] `[amplify protect equipment]`
//	stackable/10304001/10304363.stk  [action type] `[amplify protect equipment]`
//	stackable/10093001/10093371.stk  [action type] `[protect equipment]`
//	stackable/10147001/10147773.stk  [action type] `[protect equipment]`（另有 [stack limit] 1）
//	stackable/10164001/10164034.stk  [action type] `[protect equipment]`
//	stackable/10304001/10304354.stk  [action type] `[protect equipment]`
//
// 不硬编码任何 id：判据是 PVF 字节，因此新发的活动保护券只要写了这两节就自动生效，
// 而 [action type] 是别的东西（`[portable amplify]`、`[limited cube]`、
// `[add damage font skin]`…）的 stackable 永远不会被误当成券。
const (
	currentUpgradeProtectReinforceAction = "[protect equipment]"
	currentUpgradeProtectAmplifyAction   = "[amplify protect equipment]"
	currentUpgradeProtectNewPrefix       = "[new "
)

// currentUpgradeProtectTicketCacheKey 把缓存钉在**这一个运行时 PVF 目录**上：
// 同一个 itemID 在不同目录（不同 Service / 不同 PVF）下可能不是同一件东西，
// 只用 itemID 做键会在换 PVF 后读到上一个版本的判定。
type currentUpgradeProtectTicketCacheKey struct {
	catalog *pvfDungeonDropCatalog
	itemID  int64
}

// currentUpgradeProtectTicketCache 是 (目录, itemID) → 解析结果的进程级缓存。
// 扫描要对背包里每一行 stackable 问一次 PVF，而 op50 是玩家会连点的命令；
// 保护券身份是纯道具定义（不随角色/会话变化），与 currentUpgradeTableCache 同口径
// 缓存即可。
var (
	currentUpgradeProtectTicketMu    sync.Mutex
	currentUpgradeProtectTicketCache = make(map[currentUpgradeProtectTicketCacheKey]alignedcmd.UpgradeProtectTicketResolution)
)

// alignedUpgradeProtectTicketResolverForCommand 与 upgrade_ticket_pvf.go 的
// alignedUpgradeTicketResolverForCommand 同形状：只有 op50 需要这个依赖，
// 其它对齐命令一个 PVF 都不开。
func (s *Service) alignedUpgradeProtectTicketResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.UpgradeProtectTicketResolver, error) {
	if opcode != dnfenum.CmdPacketUpgradeItem {
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
	return func(itemID int64) (alignedcmd.UpgradeProtectTicketResolution, error) {
		return resolveCurrentUpgradeProtectTicket(catalog, source, itemID)
	}, nil
}

// resolveCurrentUpgradeProtectTicket 解析一个 template id 的保护券身份。
//
// 非 stackable / 解析不到 / `[action type]` 不是保护券 —— 都返回零值 + nil error
// （"不是券"，调用方继续扫下一行）。只有"目录或文档读失败"才返回 error，让调用方
// 把失败如实记进日志（既不静默，也不因为一次读失败就多发一张保护）。
func resolveCurrentUpgradeProtectTicket(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, itemID int64) (alignedcmd.UpgradeProtectTicketResolution, error) {
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return alignedcmd.UpgradeProtectTicketResolution{}, nil
	}
	currentUpgradeProtectTicketMu.Lock()
	cached, hit := currentUpgradeProtectTicketCache[currentUpgradeProtectTicketCacheKey{catalog: catalog, itemID: itemID}]
	currentUpgradeProtectTicketMu.Unlock()
	if hit {
		return cached, nil
	}
	if catalog == nil || source == nil {
		return alignedcmd.UpgradeProtectTicketResolution{}, errDungeonDropSourceRequired
	}

	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			// 模板不在运行时 PVF 目录里：不是券，且这是**正常**情形
			// （背包里有客户端侧道具/历史遗留 id）。不缓存、不报错。
			return alignedcmd.UpgradeProtectTicketResolution{}, nil
		}
		return alignedcmd.UpgradeProtectTicketResolution{}, fmt.Errorf("resolve protect ticket item=%d: %w", itemID, err)
	}
	if definition.Kind != dungeonDropItemStackable {
		return alignedcmd.UpgradeProtectTicketResolution{}, nil
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return alignedcmd.UpgradeProtectTicketResolution{}, fmt.Errorf("parse protect ticket item=%d path=%s: %w", itemID, definition.PVFPath, err)
	}
	resolution := upgradeProtectTicketResolutionFromDocument(document, definition.PVFPath)
	currentUpgradeProtectTicketMu.Lock()
	currentUpgradeProtectTicketCache[currentUpgradeProtectTicketCacheKey{catalog: catalog, itemID: itemID}] = resolution
	currentUpgradeProtectTicketMu.Unlock()
	return resolution, nil
}

// upgradeProtectTicketResolutionFromDocument 只认 `[action type]`：
// 归一化后等于强化券 tag → reinforce，等于增幅券 tag → amplify，其余一律零值。
func upgradeProtectTicketResolutionFromDocument(document *dnfpvf.Document, pvfPath string) alignedcmd.UpgradeProtectTicketResolution {
	if document == nil {
		return alignedcmd.UpgradeProtectTicketResolution{}
	}
	actionType, found := document.Text("action type")
	if !found {
		return alignedcmd.UpgradeProtectTicketResolution{}
	}
	mode := currentUpgradeProtectTicketModeForAction(actionType)
	if mode == "" {
		return alignedcmd.UpgradeProtectTicketResolution{}
	}
	resolution := alignedcmd.UpgradeProtectTicketResolution{
		Mode:     mode,
		UnitCost: 1, // 参考实现：每挡下一次摧毁消耗 1 张（Cost.MaterialCount = 1）。
		PVFPath:  pvfPath,
	}
	if values := document.Ints("action type"); len(values) > 0 {
		if values[0] > 0 {
			resolution.RetainLevel = int(values[0])
		}
	}
	return resolution
}

// currentUpgradeProtectTicketModeForAction 把 PVF 的 `[action type]` 字面量
// 归一化成 op50 的 mode 字面量；不是保护券时返回 ""。
func currentUpgradeProtectTicketModeForAction(actionType string) string {
	normalized := strings.ToLower(strings.TrimSpace(actionType))
	normalized = strings.Trim(normalized, "`")
	normalized = strings.TrimSpace(normalized)
	switch normalized {
	case currentUpgradeProtectReinforceAction:
		return "reinforce"
	case currentUpgradeProtectAmplifyAction:
		return "amplify"
	}
	// `[new protect equipment]` / `[new amplify protect equipment]`：参考实现对这两族
	// 一视同仁（ItemUpgradeModels.cs:239-243），去掉 `[new ` 前缀再判一次。
	if strings.HasPrefix(normalized, currentUpgradeProtectNewPrefix) && strings.HasSuffix(normalized, "]") {
		inner := "["
		inner += strings.TrimSuffix(strings.TrimPrefix(normalized, currentUpgradeProtectNewPrefix), "]")
		inner += "]"
		switch inner {
		case currentUpgradeProtectReinforceAction:
			return "reinforce"
		case currentUpgradeProtectAmplifyAction:
			return "amplify"
		}
	}
	return ""
}
