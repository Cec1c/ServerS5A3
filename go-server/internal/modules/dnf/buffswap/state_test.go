package buffswap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func fixture(t *testing.T) repo.Group {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "6", AccountID: "account-6"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}, "0:55": {ItemID: 101040494, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCurrentEXECommands(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want Command
	}{
		{"26000c003600", Command{38, 12, 0, 54}},
		{"2600162cffff", Command{38, 22, 44, 0xffff}},
		{"0000212cffff", Command{0, 33, 44, 0xffff}},
		{"2600212cffff", Command{38, 33, 44, 0xffff}},
		{"00000c030c00", Command{0, 12, 3, 12}},
		{"26000c020700", Command{38, 12, 2, 7}},
	} {
		raw, _ := hex.DecodeString(tc.raw)
		got, err := Parse(raw)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %+v/%v want %+v", tc.raw, got, err, tc.want)
		}
		if tc.want.List == 2 {
			got, err = ParseCurrent(raw)
			if err != nil || got != tc.want {
				t.Fatalf("%s current: got %+v/%v want %+v", tc.raw, got, err, tc.want)
			}
		}
	}
	for _, raw := range []string{"0c003600", "2600222cffff", "260021003600", "26000c043600", "26000c2c3600", "26000c03ffff"} {
		body, _ := hex.DecodeString(raw)
		if _, err := Parse(body); err == nil {
			t.Fatalf("accepted invalid %s", raw)
		}
	}
}

func TestRegistrationRemovalDisableAndOwnerIsolation(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	apply := func(c Command) {
		t.Helper()
		if err := Apply(ctx, r, "6", "account-6", c); err != nil {
			t.Fatal(err)
		}
	}
	apply(Command{38, 12, 0, 54})
	apply(Command{38, 23, 0, 55})
	apply(Command{0, 33, 44, 0xffff})
	s, found, err := Load(ctx, r, "6", "account-6")
	if err != nil || !found || s.Skill != 0 || len(s.Slots) != 2 {
		t.Fatalf("disable lost equipment: %+v %v", s, err)
	}
	apply(Command{38, 33, 44, 0xffff})
	apply(Command{38, 23, 44, 0xffff})
	s, _, _ = Load(ctx, r, "6", "account-6")
	if s.Skill != 38 || len(s.Slots) != 1 || s.Slots[12].ItemID != 101040493 {
		t.Fatalf("removal/selection: %+v", s)
	}
	if err := Apply(ctx, r, "6", "other-account", Command{0, 33, 44, 0xffff}); err == nil {
		t.Fatal("cross-account write accepted")
	}
	if _, _, err := Load(ctx, r, "6", "other-account"); err == nil {
		t.Fatal("cross-account read accepted")
	}
	if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 999}); err == nil {
		t.Fatal("missing source accepted")
	}
	after, _, _ := Load(ctx, r, "6", "account-6")
	if !reflect.DeepEqual(s, after) {
		t.Fatal("rejected command changed persisted state")
	}
}

func TestRegistrationFromWarehousePersistsInstanceAndFollowsMoves(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	inventory, found, err := r.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load fixture inventory found=%t err=%v", found, err)
	}
	inventory.Warehouse = map[string]repo.ItemStack{
		"2:7": {ItemID: 101040495, Count: 1},
	}
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCurrent(ctx, r, "6", "account-6", Command{Skill: 38, Slot: 12, List: 2, Index: 7}); err != nil {
		t.Fatalf("warehouse registration: %v", err)
	}
	state, found, err := Load(ctx, r, "6", "account-6")
	if err != nil || !found {
		t.Fatalf("load registration found=%t err=%v", found, err)
	}
	binding := state.Slots[12]
	if binding.List != 2 || binding.Index != 7 || binding.ItemID != 101040495 ||
		binding.Instance == "" || binding.SourceKind != SourceCurrentInstance {
		t.Fatalf("warehouse binding = %+v", binding)
	}
	inventory, _, _ = r.Inventory.Load(ctx, "6")
	if got := inventory.Warehouse["2:7"].Extra[instanceKey]; got != binding.Instance {
		t.Fatalf("warehouse instance=%q want %q", got, binding.Instance)
	}
	projection, err := LoadResolvedOwned(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	if item := projection.Items[12]; !item.HasCurrentStack || item.Binding.List != 2 || item.Binding.Index != 7 {
		t.Fatalf("warehouse projection = %+v", item)
	}

	stack := inventory.Warehouse["2:7"]
	delete(inventory.Warehouse, "2:7")
	inventory.Slots["0:60"] = stack
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	resolved, omitted, err := Resolve(ctx, r, "6", state)
	if err != nil || omitted != 0 {
		t.Fatalf("resolve after warehouse-to-bag move omitted=%d err=%v", omitted, err)
	}
	if got := resolved.Slots[12]; got.List != 0 || got.Index != 60 || got.Instance != binding.Instance {
		t.Fatalf("resolved after move = %+v", got)
	}
}

func TestWarehouseRegistrationWithoutInstanceDoesNotGuessAmongSameTemplates(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	inventory, _, _ := r.Inventory.Load(ctx, "6")
	inventory.Slots["0:60"] = repo.ItemStack{ItemID: 101040495, Count: 1}
	inventory.Warehouse = map[string]repo.ItemStack{
		"2:7": {ItemID: 101040495, Count: 1},
	}
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	state := State{
		WireProfile: CurrentWireProfile,
		Skill:       38,
		Slots: map[byte]Binding{
			12: {List: 2, Index: 7, ItemID: 101040495},
		},
	}
	resolved, omitted, err := Resolve(ctx, r, "6", state)
	if err != nil || omitted != 1 || len(resolved.Slots) != 0 {
		t.Fatalf("ambiguous warehouse resolve=%+v omitted=%d err=%v", resolved, omitted, err)
	}
}

func TestUniqueWarehouseCoordinateMigrationPersistsInstance(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	inventory, _, _ := r.Inventory.Load(ctx, "6")
	inventory.Warehouse = map[string]repo.ItemStack{
		"2:7": {ItemID: 101040495, Count: 1},
	}
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	state := State{
		WireProfile: CurrentWireProfile,
		Skill:       38,
		Slots: map[byte]Binding{
			12: {List: 2, Index: 7, ItemID: 101040495},
		},
	}
	raw, _ := json.Marshal(state)
	if err := r.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	projection, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	binding := projection.State.Slots[12]
	if binding.List != 2 || binding.Index != 7 || binding.Instance == "" || binding.SourceKind != SourceCurrentInstance {
		t.Fatalf("migrated warehouse binding = %+v", binding)
	}
	inventory, _, _ = r.Inventory.Load(ctx, "6")
	if got := inventory.Warehouse["2:7"].Extra[instanceKey]; got != binding.Instance {
		t.Fatalf("migrated warehouse instance=%q want %q", got, binding.Instance)
	}
	persisted, found, err := Load(ctx, r, "6", "account-6")
	if err != nil || !found || persisted.Slots[12].Instance != binding.Instance {
		t.Fatalf("persisted migrated registration found=%t state=%+v err=%v", found, persisted, err)
	}
}

func TestReapplyFollowsWearingAndOmitsMissingOrAmbiguousItems(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err != nil {
		t.Fatal(err)
	}
	s, _, _ := Load(ctx, r, "6", "account-6")
	// This fixture intentionally represents a legacy position/template binding.
	b := s.Slots[12]
	s.Slots[12] = Binding{List: b.List, Index: b.Index, ItemID: b.ItemID}
	raw, _ := json.Marshal(s)
	if err := r.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	persistedBefore, beforeFound, beforeErr := Load(ctx, r, "6", "account-6")
	if beforeErr != nil || !beforeFound || !reflect.DeepEqual(s, persistedBefore) {
		t.Fatalf("historical fixture must load exactly: found=%v err=%v want=%+v got=%+v", beforeFound, beforeErr, s, persistedBefore)
	}
	settingsBefore, settingsFound, settingsErr := r.Settings.Load(ctx, Scope("6"))
	if settingsErr != nil || !settingsFound || settingsBefore.Values["state"] != string(raw) {
		t.Fatalf("historical persisted bytes missing: found=%v err=%v", settingsFound, settingsErr)
	}
	beforeRawState := settingsBefore.Values["state"]

	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 999, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{"12": {SlotIndex: 12, ItemID: 101040493}}}); err != nil {
		t.Fatal(err)
	}
	out, omitted, err := Resolve(ctx, r, "6", s)
	if err != nil || omitted != 0 || out.Slots[12].List != 3 || out.Slots[12].Index != 12 {
		t.Fatalf("wear resolve: %+v %d %v", out, omitted, err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	out, omitted, err = Resolve(ctx, r, "6", s)
	if err != nil || omitted != 1 || len(out.Slots) != 0 {
		t.Fatalf("missing source resolve: %+v %d %v", out, omitted, err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{"0:60": {ItemID: 101040493, Count: 1}, "0:61": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	out, omitted, err = Resolve(ctx, r, "6", s)
	if err != nil || omitted != 1 || len(out.Slots) != 0 {
		t.Fatalf("ambiguous resolve: %+v %d %v", out, omitted, err)
	}
	after, afterFound, afterErr := Load(ctx, r, "6", "account-6")
	if afterErr != nil || !afterFound {
		t.Fatalf("projected registration must remain loadable: found=%v err=%v", afterFound, afterErr)
	}
	if !reflect.DeepEqual(s, after) || !reflect.DeepEqual(persistedBefore, after) {
		t.Fatalf("projection mutated registration: before=%+v after=%+v", persistedBefore, after)
	}
	settingsAfter, afterSettingsFound, afterSettingsErr := r.Settings.Load(ctx, Scope("6"))
	if afterSettingsErr != nil || !afterSettingsFound || settingsAfter.Values["state"] != beforeRawState {
		t.Fatalf("projection mutated persisted registration bytes: found=%v err=%v before=%q after=%q", afterSettingsFound, afterSettingsErr, beforeRawState, settingsAfter.Values["state"])
	}
}
