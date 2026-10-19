package dnfbridge

import (
	"errors"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// alignedStackableUseProvenanceResolverForCommand 把 op44/op251 的"这一格到底是什么
// 道具"判据接到**运行时 PVF**上：item id → `stackable/stackable.lst` → 该道具的 `.stk`
// → `[stackable type]` 原文。服务端因此不再只依赖库存行里的
// `Extra["stackable_type"]` 元数据列（授予链路上有一批行没写这个键，连 `[waste]`
// 药水都会被 fail-closed 拒掉：线上实例 `10007484 她的心意`）。
//
// 与 random_reward_item_pvf.go / premium_pvf.go 同构：解析器只在真正需要它的
// opcode 上装载，PVF 目录走 `currentPVFItemCatalog` 的既有缓存（首次读一次）。
//
// 失败语义（**故意不 fail-closed**）：目录读不到时返回 nil 解析器，并在这里记一条
// 事件日志；领域层拿到 nil 就走改动前的元数据判据。理由是"读不到 PVF"不该把
// 所有既有能用的药水一起变成不可用——元数据本来就是改动前的证据来源。
func (s *Service) alignedStackableUseProvenanceResolverForCommand(opcode dnfenum.CmdPacket) alignedcmd.StackableUseProvenanceResolver {
	if opcode != dnfenum.CmdPacketUseStackable && opcode != dnfenum.CmdPacketUseSharedEffectItem {
		return nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		if err == nil {
			err = errDungeonDropSourceRequired
		}
		s.logPacketEvent("game-aligned-stackable-use-provenance-unavailable",
			"type", opcode,
			"error", err)
		s.logInfo("dnfbridge stackable use provenance resolver unavailable; falling back to inventory metadata",
			"type", opcode,
			"error", err)
		return nil
	}
	return func(itemID int64) (alignedcmd.StackableUseProvenanceResolution, error) {
		return resolveCurrentStackableUseProvenance(catalog, itemID)
	}
}

// resolveCurrentStackableUseProvenance 回读一件道具在运行时 PVF 里的身份。
//
// Found=false（或报错）= "运行时 PVF 没有答案"，调用方必须回退到库存元数据；
// 这里绝不把"查不到"翻译成"拒绝"。
func resolveCurrentStackableUseProvenance(catalog *pvfDungeonDropCatalog, itemID int64) (alignedcmd.StackableUseProvenanceResolution, error) {
	resolution := alignedcmd.StackableUseProvenanceResolution{}
	if catalog == nil {
		return resolution, errDungeonDropSourceRequired
	}
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return resolution, nil
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return resolution, nil
		}
		return resolution, fmt.Errorf("resolve use-stackable provenance item=%d: %w", itemID, err)
	}
	if definition.Kind != dungeonDropItemStackable {
		return resolution, nil
	}
	stackableType := strings.TrimSpace(definition.StackableType)
	if stackableType == "" {
		// PVF 里这一行没有 [stackable type] 段（真档只有 490008569 一件）：没有答案。
		return resolution, nil
	}
	resolution.ItemID = itemID
	resolution.PVFPath = definition.PVFPath
	resolution.StackableType = stackableType
	resolution.ActionType = definition.ActionType
	resolution.Found = true
	return resolution, nil
}
