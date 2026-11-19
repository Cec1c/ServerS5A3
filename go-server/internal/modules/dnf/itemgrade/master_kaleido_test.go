package itemgrade

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMasterKaleidoMaximumQualityPersistsWithoutRandomizing(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint32(raw[2:6], 101010856)
	binary.LittleEndian.PutUint32(raw[6:10], 123456)
	raw[30] = 12 // An unrelated enhancement byte must survive.
	before := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:42": {ItemID: 101010856, Count: 1, RawEntry: raw, Extra: map[string]string{"quality_seed": "123456", "custom": "preserve"}},
		"0:75": {ItemID: 10150863, Count: 2},
	}}
	if err := repos.Inventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, catalogStub{10150863: {StackableType: "[etc]", MaximumQuality: true}}, func() (uint32, error) {
		t.Fatal("master box must not call random seed generator")
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Adjust(ctx, Command{SelectedCharacterID: 19, TargetSlot: 42, TargetItemID: 101010856, MaterialSlot: 75})
	if err != nil {
		t.Fatal(err)
	}
	if !result.MaximumQuality || result.GoldKaleido || result.NewSeed != itemquality.TopSeed || result.MaterialRemaining != 1 {
		t.Fatalf("result=%+v", result)
	}
	stored, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load found=%t err=%v", found, err)
	}
	target := stored.Slots["0:42"]
	if itemquality.FromStack(target) != itemquality.TopSeed || binary.LittleEndian.Uint32(target.RawEntry[6:10]) != itemquality.TopSeed {
		t.Fatalf("quality was not persisted: %+v", target)
	}
	if target.Count != 1 || target.RawEntry[30] != 12 || target.Extra["custom"] != "preserve" || target.Extra["grade_adjust_type"] != "master_kaleido" {
		t.Fatalf("unrelated attributes changed: %+v", target)
	}
	if stored.Slots["0:75"].Count != 1 {
		t.Fatal("master box must consume exactly one")
	}
}

func TestOrdinaryEtcBoxStillUsesRandomQuality(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:42": {ItemID: 1001, Count: 1}, "0:75": {ItemID: 15, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, catalogStub{15: {StackableType: "[etc]"}}, func() (uint32, error) { return 123456, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Adjust(ctx, Command{SelectedCharacterID: 19, TargetSlot: 42, TargetItemID: 1001, MaterialSlot: 75})
	if err != nil {
		t.Fatal(err)
	}
	if result.MaximumQuality || result.NewSeed != 123456 || !result.MaterialRemoved {
		t.Fatalf("ordinary result=%+v", result)
	}
}
