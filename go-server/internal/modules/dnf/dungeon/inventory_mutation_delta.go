package dungeon

import (
	"context"
	"reflect"
	"sort"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func saveMutatedInventorySlots(ctx context.Context, inventories dnfrepo.InventoryRepository, before, after dnfrepo.InventoryRecord, scoped bool) error {
	if !scoped {
		return dnfrepo.SaveInventoryFields(ctx, inventories, after, dnfrepo.InventoryFieldSlots)
	}
	// before来自同一事务成功Load的完整记录，Apply只能拿到独立深拷贝。
	// 包括自动叠堆目的槽、删除、RawEntry及Extra，不采用客户端给出的槽号。
	keys := make([]string, 0)
	for key, previous := range before.Slots {
		current, found := after.Slots[key]
		if !found || !reflect.DeepEqual(previous, current) {
			keys = append(keys, key)
		}
	}
	for key := range after.Slots {
		if _, found := before.Slots[key]; !found {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	fields := []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots}
	return dnfrepo.SaveInventoryFieldsScoped(ctx, inventories, after, fields, dnfrepo.InventorySlotDeltaKeysForFields(fields, keys))
}
