package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// DOC-067：0x77 物品行的 0x60 字节由持久化的位置锁决定，而且必须**显式**写 0/1。
// 客户端（DNF.exe 8bf1d39d…）把该字节读进 item+0x2aa 作为锁定图标；op13 整表刷新
// 与 op14 增量行共用同一条行解析链，所以重登/重选角色后图标由这一行恢复。
// 旧的 raw 行里若残留 1，未锁时必须被清掉（显式 0 覆盖 raw）。
func TestCurrentItemListEntryProjectsSortLockByte(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	raw[inventory.SortLockRowOffset] = 1 // 历史脏值：未锁也必须被显式清 0
	unlocked := dnfrepo.ItemStack{
		ItemID:   1001,
		Count:    1,
		RawEntry: raw,
		Extra:    map[string]string{"item_kind": "equipment"},
	}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 20, unlocked)
	if got := entry.data[inventory.SortLockRowOffset]; got != inventory.SortLockRowUnlocked {
		t.Fatalf("unlocked row byte = %d, want explicit 0 over the stale raw byte", got)
	}

	locked := dnfrepo.ItemStack{
		ItemID:   1001,
		Count:    1,
		RawEntry: raw,
		Extra:    map[string]string{"item_kind": "equipment", inventory.SortLockExtraKey: "1"},
	}
	entry = currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 20, locked)
	if got := entry.data[inventory.SortLockRowOffset]; got != inventory.SortLockRowLocked {
		t.Fatalf("locked row byte = %d, want 1", got)
	}

	// 时装列表走同一个投影函数：锁定同样必须带出去。
	avatarLocked := dnfrepo.ItemStack{ItemID: 2002, Count: 1, Extra: map[string]string{"item_kind": "avatar", inventory.SortLockExtraKey: "1"}}
	entry = currentItemListEntryFromStack(1, 30, avatarLocked)
	if got := entry.data[inventory.SortLockRowOffset]; got != inventory.SortLockRowLocked {
		t.Fatalf("avatar locked row byte = %d, want 1", got)
	}
}
