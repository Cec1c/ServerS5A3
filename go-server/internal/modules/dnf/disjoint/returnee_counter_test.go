package disjoint

import (
	"context"
	"errors"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPortedDisjointCounterUsesConsumedQuantity(t *testing.T) {
	ctx := context.Background()
	r := newOwnerTestRepositories(t, "a")
	ch, _, _ := r.Character.Load(ctx, "77")
	ch.Stats = map[string]int64{"is_return_user": 1, "seria_returnee_active_until": time.Now().Add(time.Hour).Unix(), "keep": 5}
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := r.Inventory.Load(ctx, "77")
	inventory.Slots = map[string]repo.ItemStack{"0:9": {ItemID: 1, Count: 1}, "0:10": {ItemID: 2, Count: 1}, "0:11": {ItemID: 3, Count: 1}}
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	project := func(a *Assets) (Changes, error) {
		for _, k := range []string{"0:9", "0:10", "0:11"} {
			delete(a.Inventory.Slots, k)
		}
		a.Inventory.Slots["0:65"] = repo.ItemStack{ItemID: 100, Count: 9}
		return Changes{Inventory: true}, nil
	}
	fail := Command{AccountID: "a", CharacterID: "77", Project: func(a *Assets) (Changes, error) {
		_, _ = project(a)
		return Changes{}, errors.New("模拟分解失败")
	}}
	if err := owner.DisjointEquipment(ctx, fail); err == nil {
		t.Fatal("未拒绝失败的分解")
	}
	ch, _, _ = r.Character.Load(ctx, "77")
	if ch.Stats["seria_returnee_disjoint_count"] != 0 {
		t.Fatal("失败分解计数泄漏")
	}
	if err := owner.DisjointEquipment(ctx, Command{AccountID: "a", CharacterID: "77", Project: project}); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = r.Character.Load(ctx, "77")
	if ch.Stats["seria_returnee_disjoint_count"] != 3 || ch.Stats["keep"] != 5 {
		t.Fatalf("数量或无关属性错误%+v", ch.Stats)
	}
	if err := owner.DisjointEquipment(ctx, Command{AccountID: "a", CharacterID: "77", Project: project}); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = r.Character.Load(ctx, "77")
	if ch.Stats["seria_returnee_disjoint_count"] != 3 {
		t.Fatal("未消耗装备的操作重复计数")
	}
}
