package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestUpgradableLegacyLotteryConsumesJarMaterialAndGrantsOnce(t *testing.T) {
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `cash/anton.stk`\n"
	source["stackable/cash/anton.stk"] = "[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n[lottery use need item] 10007367 4\n[int data] 2600014 4167 1\n[/int data]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, materialCount := range []int64{3, 4} {
		t.Run(string(rune('0'+materialCount)), func(t *testing.T) {
			ctx := context.Background()
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			initial := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
				"0:75": {ItemID: 900, Count: 1}, "0:109": {ItemID: 10007367, Count: materialCount},
			}}
			if err := repositories.Inventory.Save(ctx, initial); err != nil {
				t.Fatal(err)
			}
			service := currentBoosterTestService(catalog, repositories)
			session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
			if err := service.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repositories.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(before.Slots, initial.Slots) {
				t.Fatal("animation phase mutated inventory")
			}
			if err := service.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repositories.Inventory.Load(ctx, "19")
			if materialCount < 4 {
				if !reflect.DeepEqual(after.Slots, initial.Slots) {
					t.Fatal("insufficient material consumed assets")
				}
				return
			}
			if _, ok := after.Slots["0:75"]; ok {
				t.Fatal("jar was not consumed")
			}
			if _, ok := after.Slots["0:109"]; ok {
				t.Fatal("material was not consumed")
			}
			var rewardCount int64
			for _, stack := range after.Slots {
				if stack.ItemID == 2600014 {
					rewardCount += stack.Count
				}
			}
			if rewardCount != 1 {
				t.Fatalf("reward count=%d", rewardCount)
			}
			if err := service.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := repositories.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(replayed.Slots, after.Slots) {
				t.Fatal("duplicate animation confirmation granted again")
			}
		})
	}
}

// TestUpgradableLegacyLotteryIgnoresExchangePriceNeedMaterial 守住
// 「[need material] 是兑换价、不是开启价」这条语义。
//
// 2026-11-07 线上 490700581「异次元碎片礼盒」用 2 个 490700573 国庆邀请函
// 在商店兑换，服务端开启时又扣了一次 —— 同一份材料被收两遍。
// 这里用一个带 [need material] 的罐子、背包里一件材料都没有，
// 断言仍然能开成功、罐子被消耗、不会因为"缺材料"被拒。
func TestUpgradableLegacyLotteryIgnoresExchangePriceNeedMaterial(t *testing.T) {
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `cash/anton.stk`\n"
	source["stackable/cash/anton.stk"] = "[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n[need material] 10007367 4\n[int data] 2600014 4167 1\n[/int data]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	// 背包里只有罐子，没有任何 10007367。
	initial := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:75": {ItemID: 900, Count: 1},
	}}
	if err := repositories.Inventory.Save(ctx, initial); err != nil {
		t.Fatal(err)
	}
	service := currentBoosterTestService(catalog, repositories)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	if err := service.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repositories.Inventory.Load(ctx, "19")
	if _, ok := after.Slots["0:75"]; ok {
		t.Fatal("jar was not consumed")
	}
	var rewardCount int64
	for _, stack := range after.Slots {
		if stack.ItemID == 2600014 {
			rewardCount += stack.Count
		}
	}
	if rewardCount != 1 {
		t.Fatalf("reward count=%d slots=%v", rewardCount, after.Slots)
	}
}

func TestRealPVFAntonUpgradableLegacyLotteryPool(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the deployed PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, 10094738, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	if definition.StackableType != "[upgradable legacy]" || len(definition.Random.Groups) != 1 {
		t.Fatalf("definition=%+v", definition)
	}
	group := definition.Random.Groups[0]
	if group.DrawCount != 1 || len(group.Entries) != 47 || definition.Random.MaterialItemID != 0 || definition.Random.MaterialCountPerUse != 0 {
		t.Fatalf("pool=%+v material=%d/%d", group, definition.Random.MaterialItemID, definition.Random.MaterialCountPerUse)
	}
	for _, row := range group.Entries {
		item, err := catalog.ResolveItem(uint32(row.ItemID))
		if err != nil || item.Kind != dungeonDropItemEquipment || row.Count != 1 {
			t.Fatalf("invalid Anton reward item=%d definition=%+v err=%v", row.ItemID, item, err)
		}
	}
}
