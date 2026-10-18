package itemgrade

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestEquipmentFollowupGradeOwnerRejectsGoldenWaxEvenWithoutCatalog(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if e := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:9": {ItemID: 1001, Count: 1}, "0:65": {ItemID: 14, Count: 30}}}); e != nil {
		t.Fatal(e)
	}
	called := false
	o, e := NewOwner(g, nil, func() (uint32, error) { called = true; return 1, nil })
	if e != nil {
		t.Fatal(e)
	}
	_, e = o.Adjust(ctx, Command{SelectedCharacterID: 19, TargetSlot: 9, TargetItemID: 1001, MaterialSlot: 65})
	if e == nil || called {
		t.Fatal("黄金蜜蜡缺少目录证据时落入品级随机路径", e, called)
	}
	i, _, _ := g.Inventory.Load(ctx, "19")
	if i.Slots["0:65"].Count != 30 {
		t.Fatal("错误消耗了蜜蜡")
	}
}
