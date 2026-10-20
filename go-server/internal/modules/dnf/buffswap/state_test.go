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
	} {
		raw, _ := hex.DecodeString(tc.raw)
		got, err := Parse(raw)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %+v/%v want %+v", tc.raw, got, err, tc.want)
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
