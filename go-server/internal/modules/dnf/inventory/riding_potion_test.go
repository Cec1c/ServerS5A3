package inventory

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func ridingPotionFixture(t *testing.T) (repo.Group, RidingPotionRequest, time.Time) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "2", AccountID: "test", Level: 90, Stats: map[string]int64{"fixture_marker": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "2", Slots: map[string]repo.ItemStack{
		"0:4": {ItemID: 490005150, Count: 2, Extra: map[string]string{"item_kind": "stackable"}},
		"0:5": {ItemID: 490005151, Count: 1}, "0:6": {ItemID: 490005151, Count: 4}, "1:5": {ItemID: 490005151, Count: 100},
	}}); err != nil {
		t.Fatal(err)
	}
	req := RidingPotionRequest{CharacterID: "2", AccountID: "test", SlotIndex: 4, DungeonID: 80, Revalidate: func() bool { return true },
		Definition: RidingPotionDefinition{ItemID: 490005150, PVFPath: "stackable/test.stk", FormID: 107000902, DurationMS: 300000, MinimumLevel: 1, CoolTimeMS: 10000, DungeonIDs: []int64{80, 81}, Materials: map[int64]int64{490005151: 2}, DisallowHardcore: true}}
	return g, req, now
}

func TestRidingPotionCommitsPotionMaterialsAndDurableCooldown(t *testing.T) {
	g, req, now := ridingPotionFixture(t)
	ctx := context.Background()
	owner, _ := NewOwner(g)
	result, err := owner.UseRidingPotion(ctx, req, now)
	if err != nil || result.Remaining != 1 || !reflect.DeepEqual(result.ChangedSlots, []int16{4, 5, 6}) {
		t.Fatalf("变身事务结果=%+v 错误=%v", result, err)
	}
	bag, _, _ := g.Inventory.Load(ctx, "2")
	c, _, _ := g.Character.Load(ctx, "2")
	if bag.Slots["0:4"].Count != 1 || bag.Slots["0:5"].Count != 0 || bag.Slots["0:6"].Count != 3 || bag.Slots["1:5"].Count != 100 || c.Stats["riding_potion_next_use_ms_490005150"] != now.UnixMilli()+10000 {
		t.Fatalf("扣数或冷却错误：%+v %+v", bag, c.Stats)
	}
	fresh, _ := NewOwner(g)
	if _, err := fresh.UseRidingPotion(ctx, req, now.Add(9999*time.Millisecond)); err == nil {
		t.Fatal("重建owner后绕过了持久冷却")
	}
	if _, err := fresh.UseRidingPotion(ctx, req, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	bag, _, _ = g.Inventory.Load(ctx, "2")
	if _, ok := bag.Slots["0:4"]; ok {
		t.Fatal("最后一瓶未删除")
	}
}

func TestRidingPotionRejectionsAndLateRollbackPreserveAssets(t *testing.T) {
	for _, kind := range []string{"map", "level", "max_level", "owner", "hardcore", "expired_source", "expired_material", "insufficient_material", "changed_item", "late_scene", "early_scene", "invalid_definition", "missing_transaction"} {
		t.Run(kind, func(t *testing.T) {
			g, req, now := ridingPotionFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "2")
			if c.Stats == nil {
				c.Stats = map[string]int64{}
			}
			bag, _, _ := g.Inventory.Load(ctx, "2")
			switch kind {
			case "map":
				req.DungeonID = 999
			case "level":
				req.Definition.MinimumLevel = 91
			case "max_level":
				req.Definition.MaximumLevel = 89
			case "owner":
				req.AccountID = "other"
			case "hardcore":
				c.Stats["is_hardcore_mode"] = 1
			case "expired_source":
				s := bag.Slots["0:4"]
				s.ExpireAt = now
				bag.Slots["0:4"] = s
			case "expired_material":
				s := bag.Slots["0:6"]
				s.ExpireAt = now
				bag.Slots["0:6"] = s
			case "insufficient_material":
				req.Definition.Materials[490005151] = 6
			case "changed_item":
				s := bag.Slots["0:4"]
				s.ItemID++
				bag.Slots["0:4"] = s
			case "late_scene":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls < 2 }
			case "early_scene":
				req.Revalidate = func() bool { return false }
			case "invalid_definition":
				req.Definition.DurationMS = 0
			case "missing_transaction":
				g.CharacterAssets = nil
			}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			owner, _ := NewOwner(g)
			if _, err := owner.UseRidingPotion(ctx, req, now); err == nil {
				t.Fatal("非法使用被接受")
			}
			afterC, _, _ := g.Character.Load(ctx, "2")
			afterBag, _, _ := g.Inventory.Load(ctx, "2")
			if !reflect.DeepEqual(c, afterC) || !reflect.DeepEqual(bag, afterBag) {
				t.Fatal("拒绝或回滚后资产发生变化")
			}
		})
	}
}

func TestRidingPotionConcurrentClicksConsumeOnceDuringCooldown(t *testing.T) {
	g, req, now := ridingPotionFixture(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner, _ := NewOwner(g)
			if _, err := owner.UseRidingPotion(context.Background(), req, now); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("并发点击成功次数=%d", successes.Load())
	}
	bag, _, _ := g.Inventory.Load(context.Background(), "2")
	if bag.Slots["0:4"].Count != 1 || bag.Slots["0:6"].Count != 3 {
		t.Fatal("并发点击额外消耗物品")
	}
}
