package itemgrade

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/itemtrade"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type waxCatalogStub map[uint32]WaxDefinition

func (c waxCatalogStub) ResolveWaxItem(id uint32) (WaxDefinition, error) {
	d, ok := c[id]
	if !ok {
		return d, ErrWaxReseal
	}
	return d, nil
}
func waxRuleFixture() WaxRules {
	return WaxRules{Grades: []WaxRangeWeight{{1, 30, 1}, {31, 50, 2}, {51, 70, 3}, {71, 85, 4}, {86, 90, 5}}, Limits: []WaxRangeWeight{{1, 85, 7}, {86, 90, 4}}, Rarity: [7]float64{1, 2, 1, 2, 8, 8, 6}, Extended: [7]float64{0, 0, 1, 1, 1, 1, 1}, Growth: [7]float64{0, 0, 1.1, 1.1, 0, 0, 1.11}}
}
func waxFixture(t *testing.T) (dnfrepo.Group, waxCatalogStub, WaxCommand) {
	t.Helper()
	ctx := context.Background()
	g := dnfrepomemory.NewMemoryGroup()
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "47", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 0x77)
	for i := range raw {
		raw[i] = byte(i * 3)
	}
	binary.LittleEndian.PutUint16(raw, 10)
	binary.LittleEndian.PutUint32(raw[2:], 28309)
	binary.LittleEndian.PutUint32(raw[6:], 462346698)
	raw[10] = 12
	raw[13] = 0
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "47", Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 28309, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "462346698", "enchant_card": "777", "seal_removed_by_first_equip": "1", "trade_locked_by_first_equip": "1"}}, "0:65": {ItemID: 14, Count: 100, Extra: map[string]string{"item_kind": "stackable"}}}}); err != nil {
		t.Fatal(err)
	}
	c := waxCatalogStub{28309: {Equipment: true, Resealable: true, MinimumLevel: 65, Rarity: 3, Placement: itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64}}, 14: {GoldenWax: true, Placement: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120}}}
	cmd := WaxCommand{AccountID: "account-1", CharacterID: "47", TargetSlot: 10, TargetItemID: 28309, MaterialSlot: 65, UpdatedAt: time.Date(2026, 10, 17, 2, 6, 0, 0, time.UTC), Project: func(_ int16, s dnfrepo.ItemStack) ([]byte, bool, error) {
		return append([]byte(nil), s.RawEntry...), s.Extra["seal_flag"] == "1", nil
	}}
	return g, c, cmd
}

func TestGoldenWaxResealPreservesInstanceAndRejectsRepeat(t *testing.T) {
	g, c, cmd := waxFixture(t)
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "47")
	o, err := NewWaxOwner(g, c, waxRuleFixture())
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.Reseal(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cost != 6 || r.MaterialRemaining != 94 || r.ResealCount != 1 {
		t.Fatalf("result=%+v", r)
	}
	after, _, _ := g.Inventory.Load(ctx, "47")
	target := after.Slots["0:10"]
	for i, b := range before.Slots["0:10"].RawEntry {
		if i != 10 && i != 13 && target.RawEntry[i] != b {
			t.Fatalf("changed unrelated byte %x", i)
		}
	}
	if target.Bind || target.RawEntry[10] != 44 || target.RawEntry[13] != 1 || target.Extra["quality_seed"] != "462346698" || target.Extra["enchant_card"] != "777" || target.Extra["trade_locked_by_first_equip"] != "" || target.Extra["seal_removed_by_first_equip"] != "" {
		t.Fatalf("target=%+v", target)
	}
	if _, err = o.Reseal(ctx, cmd); err == nil {
		t.Fatal("repeat charged again")
	}
	again, _, _ := g.Inventory.Load(ctx, "47")
	if !reflect.DeepEqual(after, again) {
		t.Fatal("repeat changed inventory")
	}
}

func TestGoldenWaxCostUsesPVFWeightsCountGrowthAndLevelLimits(t *testing.T) {
	rules := waxRuleFixture()
	for _, tc := range []struct {
		level, rarity int64
		count         byte
		cost          int64
	}{{65, 3, 0, 6}, {65, 3, 2, 7}, {85, 3, 0, 8}, {86, 3, 3, 13}, {65, 6, 0, 18}} {
		cost, _, err := rules.WaxCost(tc.level, tc.rarity, tc.count)
		if err != nil || cost != tc.cost {
			t.Fatalf("%+v cost=%d err=%v", tc, cost, err)
		}
	}
	for _, tc := range []struct {
		level, rarity int64
		count         byte
	}{{65, 3, 7}, {86, 3, 4}, {0, 3, 0}, {91, 3, 0}, {65, 0, 0}, {65, 7, 0}} {
		if _, _, err := rules.WaxCost(tc.level, tc.rarity, tc.count); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestGoldenWaxResealGuardsLeaveAllAssetsUntouched(t *testing.T) {
	for _, mode := range []string{"foreign_owner", "target_id", "target_missing", "target_locked", "material_locked", "target_period", "material_expired", "already_sealed", "count_limit", "short_wax", "same_slot", "wrong_material", "target_not_resealable", "locked_slot", "no_project", "count_metadata_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			g, c, cmd := waxFixture(t)
			ctx := context.Background()
			i, _, _ := g.Inventory.Load(ctx, "47")
			target, material := i.Slots["0:10"], i.Slots["0:65"]
			switch mode {
			case "foreign_owner":
				cmd.AccountID = "other"
			case "target_id":
				cmd.TargetItemID = 999
			case "target_missing":
				delete(i.Slots, "0:10")
			case "target_locked":
				target.Extra["equipment_lock_state"] = "2"
			case "material_locked":
				material.Extra["equipment_lock_state"] = "1"
			case "target_period":
				target.ExpireAt = cmd.UpdatedAt.Add(time.Hour)
			case "material_expired":
				material.ExpireAt = cmd.UpdatedAt
			case "already_sealed":
				target.Extra["seal_flag"] = "1"
			case "count_limit":
				target.RawEntry[10] = 7<<5 | 12
			case "short_wax":
				material.Count = 5
			case "same_slot":
				cmd.MaterialSlot = 10
			case "wrong_material":
				material.ItemID = 15
			case "target_not_resealable":
				d := c[28309]
				d.Resealable = false
				c[28309] = d
			case "locked_slot":
				cmd.TargetSlot = 50
				i.Slots["0:50"] = target
			case "no_project":
				cmd.Project = nil
			case "count_metadata_mismatch":
				target.Extra["reseal_count"] = "7"
			}
			if mode != "target_missing" {
				i.Slots["0:10"] = target
			}
			i.Slots["0:65"] = material
			if err := g.Inventory.Save(ctx, i); err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Inventory.Load(ctx, "47")
			o, _ := NewWaxOwner(g, c, waxRuleFixture())
			if _, err := o.Reseal(ctx, cmd); err == nil {
				t.Fatal("guard accepted")
			}
			after, _, _ := g.Inventory.Load(ctx, "47")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected action mutated assets")
			}
		})
	}
}

type waxFailUOW struct {
	base dnfrepo.CharacterAssetUnitOfWork
}

func (w waxFailUOW) WithinCharacterAssets(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return w.base.WithinCharacterAssets(ctx, id, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		return apply(c, waxFailInventory{i}, e)
	})
}

type waxFailInventory struct{ dnfrepo.InventoryRepository }

func (w waxFailInventory) SaveFields(ctx context.Context, r dnfrepo.InventoryRecord, f ...dnfrepo.InventoryField) error {
	if err := dnfrepo.SaveInventoryFields(ctx, w.InventoryRepository, r, f...); err != nil {
		return err
	}
	return errors.New("wax save failed after writes")
}
func TestGoldenWaxResealSaveFailureRollsBackTargetAndMaterial(t *testing.T) {
	g, c, cmd := waxFixture(t)
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "47")
	g.CharacterAssets = waxFailUOW{g.CharacterAssets}
	o, _ := NewWaxOwner(g, c, waxRuleFixture())
	if _, err := o.Reseal(ctx, cmd); err == nil {
		t.Fatal("failed save succeeded")
	}
	after, _, _ := g.Inventory.Load(ctx, "47")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed save leaked target/wax mutation")
	}
}

func TestGradeAdjustmentRejectsResealMaterialBeforeRandomSeed(t *testing.T) {
	g, _, _ := waxFixture(t)
	before, _, _ := g.Inventory.Load(context.Background(), "47")
	o, _ := NewOwner(g, catalogStub{14: {StackableType: "[etc]", ResealMaterial: true}}, func() (uint32, error) { t.Fatal("wax reached quality randomizer"); return 0, nil })
	if _, err := o.Adjust(context.Background(), Command{SelectedCharacterID: 47, TargetSlot: 10, TargetItemID: 28309, MaterialSlot: 65}); !errors.Is(err, ErrMaterialTypeInvalid) {
		t.Fatal(err)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "47")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("grade fallback consumed wax")
	}
}

func TestGoldenWaxReleasesCharacterBindingForRealTradeOwner(t *testing.T) {
	g, c, cmd := waxFixture(t)
	ctx := context.Background()
	i, _, _ := g.Inventory.Load(ctx, "47")
	target := i.Slots["0:10"]
	target.Bind = true
	i.Slots["0:10"] = target
	if err := g.Inventory.Save(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "48", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	trade, err := itemtrade.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	first := itemtrade.Participant{CharacterID: "47", Offers: []itemtrade.Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 10, Count: 1, ExpectedItem: 28309}}}
	second := itemtrade.Participant{CharacterID: "48"}
	if _, err = trade.Exchange(ctx, first, second); !errors.Is(err, itemtrade.ErrItemNotTradeable) {
		t.Fatalf("unsealed bound gear trade err=%v", err)
	}
	o, _ := NewWaxOwner(g, c, waxRuleFixture())
	if _, err = o.Reseal(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	result, err := trade.Exchange(ctx, first, second)
	if err != nil {
		t.Fatalf("resealed item still untradeable: %v", err)
	}
	if len(result.Received["48"]) != 1 || result.Received["48"][0].Stack.Bind || result.Received["48"][0].Stack.Extra["seal_flag"] != "1" {
		t.Fatal("trade lost sealed state")
	}
}
