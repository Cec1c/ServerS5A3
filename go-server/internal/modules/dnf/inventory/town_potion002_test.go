package inventory

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func townPotion002Fixture(t *testing.T) (repo.Group, Command) {
	t.Helper()
	t.Setenv(UpgradeRateBuffBoostEnv, "")
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	path := "stackable/cash/chn_20141111_SAO_Package/crystallight_10007343.stk"
	saveTestCharacter(t, ctx, g, repo.CharacterRecord{CharacterID: "77", AccountID: "a", Level: 90, Stats: map[string]int64{"gold": 100}})
	saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 10007343, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": path, "instance_value": "123"}}}})
	r := alignedcmd.StackableUseProvenanceResolution{ItemID: 10007343, Found: true, PVFPath: path, StackableType: "[expert town potion]",
		TownPotion002: &alignedcmd.TownPotion002Definition{ItemID: 10007343, PVFPath: path, MinimumLevel: 1, CooldownMillis: 60000,
			Buff: alignedcmd.UpgradeRateBuffResolution{PVFPath: path, Mode: "reinforce", DurationMillis: 30000, BonusWeight: 10000}}}
	return g, Command{Operation: "use_stackable", SelectedCharacterID: 77, AccountID: "a", SourceSlotIndex: 87, SourceInstanceValue: 123, ItemTemplateID: 10007343,
		ItemUsageNow: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), StackableUseProvenanceResolver: func(int64) (alignedcmd.StackableUseProvenanceResolution, error) { return r, nil }}
}

func TestTownPotion002CrystalEffectAndCooldown(t *testing.T) {
	g, cmd := townPotion002Fixture(t)
	o, _ := NewOwner(g)
	ctx := context.Background()
	r, err := o.UseStackable(ctx, cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.RemainingCount != 1 || r.UpgradeRateBuffBonus != 10000 || r.UpgradeRateBuffUntil != cmd.ItemUsageNow.Unix()+30 {
		t.Fatalf("result %+v", r)
	}
	c, _, _ := g.Character.Load(ctx, "77")
	bonus, _ := upgradeRateBuffActiveBonus(c, "reinforce", cmd.ItemUsageNow.Add(time.Second))
	if bonus != 10000 {
		t.Fatalf("actual upgrade bonus %d", bonus)
	}
	// Feed the actually persisted potion state through the existing op50 owner,
	// rather than merely asserting that the ledger contains a bonus number.
	active := map[string]int64{}
	for k, v := range c.Stats {
		active[k] = v
	}
	active["upgrade_rate_buff_reinforce_until_unix"] = time.Now().Unix() + 30
	_, _, upgrade := runRateBuffUpgrade(t, rateBuffUpgradeCase{mode: "reinforce", baseWeight: 50000, stats: active})
	if upgrade.UpgradeSuccessWeight != 55000 {
		t.Fatalf("op50 weight %d", upgrade.UpgradeSuccessWeight)
	}
	if bonus, _ = upgradeRateBuffActiveBonus(c, "amplify", cmd.ItemUsageNow.Add(time.Second)); bonus != 0 {
		t.Fatal("cross-mode effect")
	}
	if bonus, _ = upgradeRateBuffActiveBonus(c, "reinforce", cmd.ItemUsageNow.Add(30*time.Second)); bonus != 0 {
		t.Fatal("expired effect")
	}
	before := loadTestInventory(t, ctx, g, "77")
	_, err = o.UseStackable(ctx, cmd, nil)
	if err == nil {
		t.Fatal("replay accepted")
	}
	after := loadTestInventory(t, ctx, g, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay debited")
	}
	cmd.ItemUsageNow = cmd.ItemUsageNow.Add(time.Minute)
	if _, err = o.UseStackable(ctx, cmd, nil); err != nil {
		t.Fatal(err)
	}
	cmd.ItemUsageNow = cmd.ItemUsageNow.Add(time.Minute)
	if _, err = o.UseStackable(ctx, cmd, nil); err == nil {
		t.Fatal("empty slot reused")
	}
}

func TestTownPotion002RejectionsPreserveAssets(t *testing.T) {
	for _, name := range []string{"locked", "expired", "expiry_metadata", "definition_expired", "wrong_instance", "wrong_item", "wrong_account", "zero_level", "closed_slot", "wrong_list", "shared_effect", "missing_resolver", "resolver_error", "missing_effect", "invalid_effect", "wrong_path", "action", "count_zero", "boost_disabled", "malformed_reserved"} {
		t.Run(name, func(t *testing.T) {
			g, cmd := townPotion002Fixture(t)
			ctx := context.Background()
			inv := loadTestInventory(t, ctx, g, "77")
			s := inv.Slots["0:87"]
			c, _, _ := g.Character.Load(ctx, "77")
			switch name {
			case "locked":
				s.Extra["equipment_lock_state"] = "1"
			case "expired":
				s.ExpireAt = cmd.ItemUsageNow
			case "expiry_metadata":
				s.Extra["expire_time"] = "1"
			case "wrong_instance":
				cmd.SourceInstanceValue++
			case "wrong_item":
				s.ItemID++
			case "wrong_account":
				cmd.AccountID = "other"
			case "zero_level":
				c.Level = 0
			case "closed_slot":
				cmd.SourceSlotIndex = 300
			case "wrong_list":
				cmd.SourceListType = 1
			case "shared_effect":
				cmd.Operation = "use_shared_effect_item"
			case "missing_resolver":
				cmd.StackableUseProvenanceResolver = nil
				s.Extra["stackable_type"] = "[waste]"
			case "resolver_error":
				cmd.StackableUseProvenanceResolver = func(int64) (alignedcmd.StackableUseProvenanceResolution, error) {
					return alignedcmd.StackableUseProvenanceResolution{}, errors.New("unavailable")
				}
			case "missing_effect", "invalid_effect", "action", "definition_expired":
				r, _ := cmd.StackableUseProvenanceResolver(10007343)
				if name == "missing_effect" {
					r.TownPotion002 = nil
				} else if name == "definition_expired" {
					r.TownPotion002.ExpirationDate = cmd.ItemUsageNow
				} else if name == "action" {
					r.ActionType = "[add fatigue]"
				} else {
					r.TownPotion002.Buff.BonusWeight = 0
				}
				cmd.StackableUseProvenanceResolver = func(int64) (alignedcmd.StackableUseProvenanceResolution, error) { return r, nil }
			case "wrong_path":
				s.Extra["pvf_path"] = "other.stk"
			case "count_zero":
				s.Count = 0
			case "boost_disabled":
				t.Setenv(UpgradeRateBuffBoostEnv, "off")
			case "malformed_reserved":
				cmd.ReservedValue = 1
			}
			inv.Slots["0:87"] = s
			saveTestInventory(t, ctx, g, inv)
			saveTestCharacter(t, ctx, g, c)
			before := loadTestInventory(t, ctx, g, "77")
			bc, _, _ := g.Character.Load(ctx, "77")
			o, _ := NewOwner(g)
			r, err := o.UseStackable(ctx, cmd, nil)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if r.Changed {
				t.Fatal("error exposed success")
			}
			after := loadTestInventory(t, ctx, g, "77")
			ac, _, _ := g.Character.Load(ctx, "77")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bc, ac) {
				t.Fatal("rejection mutated assets")
			}
		})
	}
}

func TestTownPotion002LateFailureRollback(t *testing.T) {
	g, cmd := townPotion002Fixture(t)
	ctx := context.Background()
	before := loadTestInventory(t, ctx, g, "77")
	bc, _, _ := g.Character.Load(ctx, "77")
	forced := errors.New("town-potion forced save failure")
	g.CharacterAssets = failingDamageFontAssetUOW{base: g.CharacterAssets, saveErr: forced}
	o, _ := NewOwner(g)
	r, err := o.UseStackable(ctx, cmd, nil)
	if !errors.Is(err, forced) {
		t.Fatalf("error %v", err)
	}
	if r.Changed {
		t.Fatal("error exposed success")
	}
	after := loadTestInventory(t, ctx, g, "77")
	ac, _, _ := g.Character.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bc, ac) {
		t.Fatal("late failure did not roll back")
	}
}

func TestTownPotion002FamilyStillNeedsEffectOwner(t *testing.T) {
	if stackableTypeIsPVFProvenUseConsumable("[expert town potion]") {
		t.Fatal("must not enable debit-only family")
	}
	g, cmd := townPotion002Fixture(t)
	r, _ := cmd.StackableUseProvenanceResolver(10007343)
	s := loadTestInventory(t, context.Background(), g, "77").Slots["0:87"]
	if _, _, ok := stackableUseProvenance(s, cmd.StackableUseProvenanceResolver); ok {
		t.Fatal("generic provenance would skip effect")
	}
	r.ItemID = 2600479
	r.TownPotion002 = nil
	cmd.ItemTemplateID = 2600479
	cmd.StackableUseProvenanceResolver = func(int64) (alignedcmd.StackableUseProvenanceResolution, error) { return r, nil }
	o, _ := NewOwner(g)
	if _, err := o.UseStackable(context.Background(), cmd, nil); err == nil {
		t.Fatal("card binder accepted as potion")
	}
}

func TestTownPotion002ConcurrentUseCommitsOnce(t *testing.T) {
	g, cmd := townPotion002Fixture(t)
	o, _ := NewOwner(g)
	ready := make(chan struct{})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-ready; _, err := o.UseStackable(context.Background(), cmd, nil); done <- err }()
	}
	close(ready)
	success := 0
	for i := 0; i < 2; i++ {
		if <-done == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("commits %d", success)
	}
	inv := loadTestInventory(t, context.Background(), g, "77")
	if inv.Slots["0:87"].Count != 1 {
		t.Fatal("concurrent double debit")
	}
}

func TestTownPotion002SlotFamilyAndCapacity(t *testing.T) {
	for _, slot := range []int16{3, 65, 96, 97, 120, 9, 0, 300} {
		t.Run(fmt.Sprint(slot), func(t *testing.T) {
			g, cmd := townPotion002Fixture(t)
			ctx := context.Background()
			inv := loadTestInventory(t, ctx, g, "77")
			s := inv.Slots["0:87"]
			delete(inv.Slots, "0:87")
			inv.Slots[slotKey(0, slot)] = s
			saveTestInventory(t, ctx, g, inv)
			cmd.SourceSlotIndex = slot
			o, _ := NewOwner(g)
			_, err := o.UseStackable(ctx, cmd, nil)
			want := slot == 3 || slot == 65 || slot == 96
			if (err == nil) != want {
				t.Fatalf("slot=%d err=%v", slot, err)
			}
			if !want && !reflect.DeepEqual(inv, loadTestInventory(t, ctx, g, "77")) {
				t.Fatal("invalid slot debited")
			}
		})
	}
}
