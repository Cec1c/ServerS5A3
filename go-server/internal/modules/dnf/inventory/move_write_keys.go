// 本文件是 op19"背包内移动"侧的脏槽 key 推导（O-B 的 inventory 半边）。
//
// 参考实现里"穿/脱"与"背包内移动"是同一条链路（opcode 0x0013 → InventoryMoveCommitService
// → OnlineInventoryMutationCommitCoordinator → InventoryMoveService.TryMove）：域侧只改
// 内存，改哪一格就 MarkDirty 哪一格（InventoryService.cs:515-536），提交时
// 写入集合 = 脏位集合（InventoryPersistenceService.cs:323-331/396-417）。我们没有常驻
// 内存与持久脏位，等价物是"逐 op 显式给出本次真正改动的槽"，本类型就是这个集合在背包
// 内移动路径上的**唯一**推导点（见 _dist/reference-inventory-persistence-study.md §5.2）。
//
// 为什么按 collection 分组：一次移动可能同时改两个容器——`itemMapForList` 把主背包
// （list 0/1/7/38）映射到 slots、把个人仓库（list 2）映射到 warehouse，"主背包 ↔ 仓库"
// 的一次移动必须对两个 collection 各写一次；只写其中一个就是静默数据丢失
// （study §5.2-2 与风险 R3，正是旧 equip 侧 inventoryCollectionName 只接受单个字段的坑）。
package inventory

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// bagMoveWriteKeys 收集本次移动真正发生增/改/删的槽 key，按落库 collection 分组。
//
// 语义：nil / 空 = "取不到脏槽集合" ⇒ 落库退回整容器写并记一条回退事件（宁可慢，
// 不可漏写）。它只做加法，去重与排序交给仓储层（mysql 的 sortedUniqueStrings），
// 因此整容器口径与增量口径的行内容、行顺序都不受这里影响。
type bagMoveWriteKeys struct {
	keys map[dnfrepo.InventoryField][]string
}

// newBagMoveWriteKeys 建一个覆盖给定容器的 key 集合收集器；重复字段只留一个。
func newBagMoveWriteKeys(fields ...dnfrepo.InventoryField) *bagMoveWriteKeys {
	keys := make(map[dnfrepo.InventoryField][]string, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		if _, ok := keys[field]; !ok {
			keys[field] = nil
		}
	}
	return &bagMoveWriteKeys{keys: keys}
}

// add 记下一个被改动的槽 key（形如 `<list>:<slot>`）。
func (k *bagMoveWriteKeys) add(field dnfrepo.InventoryField, key string) {
	if k == nil || field == "" || key == "" {
		return
	}
	if k.keys == nil {
		k.keys = make(map[dnfrepo.InventoryField][]string, 2)
	}
	k.keys[field] = append(k.keys[field], key)
}

// deltaKeys 折成仓储层的 collection → keys 形态。
// 认不得的字段 ⇒ 返回 nil（整体视为"取不到"），由共用口径回退整容器写并记事件。
func (k *bagMoveWriteKeys) deltaKeys() dnfrepo.InventorySlotDeltaKeys {
	if k == nil || len(k.keys) == 0 {
		return nil
	}
	out := make(dnfrepo.InventorySlotDeltaKeys, len(k.keys))
	for field, keys := range k.keys {
		collection, ok := dnfrepo.InventoryCollectionName(field)
		if !ok {
			return nil
		}
		out[collection] = append([]string(nil), keys...)
	}
	return out
}
