// 本文件是 equip 侧对**共用**增量写口径（O-B）的薄适配层：
//
//	DNFBRIDGE_INVENTORY_SAVE_SCOPE = collection | slot      （默认 collection）
//	DNFBRIDGE_EQUIPMENT_SAVE_SCOPE = collection | entry     （默认 collection）
//
// 背景（见 _dist/op19-commit-span-decomposition.md §1/§3/§5/§7-O-B）：一次穿脱的提交
// 跨度里 99% 是两条"整容器 DELETE + 逐行 INSERT"—— 每穿脱一件就把整个主背包
// （角色 1：195 行 + 1774 extra 行）与整个装备表（25 行 + 318 extra 行）全量重写。
// 厂商参考实现（ServerS4A21-master\...\InventoryPersistenceService.cs:58/67/323/396）
// 的落库口径是**只写脏槽**（ClearDirtyState + CollectDirtyItemSlots + SaveDirtyItems），
// 所以本文件是**回归参考口径**，不是发明新机制。
//
// 实现自本次起**只有一份**，放在 repository/inventory_write_scope.go：参考实现里
// "穿/脱"与"背包内移动"本来就是同一条链路（0x0013 → InventoryMoveCommitService →
// OnlineInventoryMutationCommitCoordinator → InventoryMoveService.TryMove），只是脏槽
// 落在哪两个 list_type 的区别。旧实现（只接受恰好一个容器字段）无法表达跨容器移动，
// 已被仓库层的 InventoryCollectionNames 取代；本文件只保留 equip 的调用形状与既有
// 导出名（MoveSaveFallbackEvent / SetMoveSaveFallbackObserver），行为逐字节不变：
//
//  1. 把 op19 真正改动的 key 集合（穿/脱各一个背包端点 + 一个装备端点）传下去；
//  2. 用**类型断言**取增量写能力，memory / trade-scoped / rental-scoped /
//     settlement-scoped 等实现都不满足它 ⇒ 一律留在今天的整容器路径上；
//  3. 拿不到 key 集合时**回退整容器写并记一条事件** —— 绝不静默不写。
//
// 为什么开关直接 os.Getenv 自读、不经 dnfbridge/options.go：与本仓既有先例一致
// （本目录 owner.go:876 的 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP、
// repository/mysql/mysql_char.go:222、services/gmtool/online_guard.go:67 的
// "开关自读环境、不动改动最密集的 options.go"约定）。读取与缓存同样在仓库层。
package equip

import (
	"context"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 开关名与字面量由仓库层的共用实现持有（equip 与 inventory 两个消费方共用同一份）。
const (
	inventorySaveScopeEnv = dnfrepo.InventorySaveScopeEnvName
	equipmentSaveScopeEnv = dnfrepo.EquipmentSaveScopeEnvName
)

const (
	inventorySaveScopeCollection = dnfrepo.SaveScopeCollection
	inventorySaveScopeSlot       = dnfrepo.SaveScopeSlot
	equipmentSaveScopeCollection = dnfrepo.SaveScopeCollection
	equipmentSaveScopeEntry      = dnfrepo.SaveScopeEntry
)

// MoveSaveFallbackEvent 是"想增量写、但退回了整容器写"的事件（仓库层共用类型的别名，
// 保持 equip 既有导出名与字段不变）。
type MoveSaveFallbackEvent = dnfrepo.InventoryWriteFallbackEvent

// SetMoveSaveFallbackObserver 替换回退事件观察者；传 nil = 恢复默认（slog 默认日志）。
// 默认观察者永远非 nil，所以回退分支不会静默。
func SetMoveSaveFallbackObserver(observer func(MoveSaveFallbackEvent)) {
	dnfrepo.SetInventoryWriteFallbackObserver(observer)
}

// moveWriteKeys 是一次 op19 真正改动的 key 集合。
//
//   - inventory：record.Slots / record.Warehouse 里发生增/改/删的 slot key（`<list>:<slot>`）；
//   - equipment：record.Entries 里发生增/改/删的 entry key（槽位十进制串）。
//
// **nil / 空切片 = "取不到"**：落库退回整容器写并记一条回退事件。所有调用点都必须
// 显式给出本次改动的 key；给出空集合与"取不到"同义（宁可慢，不可漏写）。
type moveWriteKeys struct {
	inventory []string
	equipment []string
}

// saveMoveInventoryFields 写背包。默认（含一切未知取值）= 今天的整容器写；
// scope=slot 且 keys 是本次改动的完整 key 集合时，只写这些 key。
//
// 穿/脱只动一个容器，因此把扁平 key 集合折成"恰好一个 collection"；字段集不满足
// 这个形状时 InventorySlotDeltaKeysForFields 返回 nil，共用口径会按
// collection_unmapped / keys_unavailable 回退整容器写并记事件。
func (o *Owner) saveMoveInventoryFields(
	ctx context.Context,
	record dnfrepo.InventoryRecord,
	fields []dnfrepo.InventoryField,
	keys []string,
) error {
	return dnfrepo.SaveInventoryFieldsScoped(
		ctx, o.inventory, record, fields,
		dnfrepo.InventorySlotDeltaKeysForFields(fields, keys),
	)
}

// saveMoveEquipmentEntries 写穿戴表。默认（含一切未知取值）= 今天的整容器写；
// scope=entry 且 keys 是本次改动的完整 key 集合时，只写这些 entry。
func (o *Owner) saveMoveEquipmentEntries(
	ctx context.Context,
	record dnfrepo.EquipmentRecord,
	keys []string,
) error {
	return dnfrepo.SaveEquipmentFieldsScoped(ctx, o.equipment, record, keys)
}
