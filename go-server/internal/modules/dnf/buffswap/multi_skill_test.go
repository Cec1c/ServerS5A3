package buffswap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func multiSkillApplyCurrent(t *testing.T, r repo.Group, c Command) {
	t.Helper()
	if err := ApplyCurrent(context.Background(), r, "6", "account-6", c); err != nil {
		t.Fatal(err)
	}
}

func multiSkillApplyGroup(t *testing.T, r repo.Group, group byte, c Command) {
	t.Helper()
	if err := ApplyCurrentGroup(context.Background(), r, "6", "account-6", group, c); err != nil {
		t.Fatal(err)
	}
}

func multiSkillLoad(t *testing.T, r repo.Group) State {
	t.Helper()
	state, found, err := Load(context.Background(), r, "6", "account-6")
	if err != nil || !found {
		t.Fatalf("load state: found=%v err=%v", found, err)
	}
	return state
}

func multiSkillSave(t *testing.T, r repo.Group, state State) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Settings.Save(context.Background(), repo.SettingsRecord{
		Scope:  Scope("6"),
		Values: map[string]string{"state": string(raw), "source": "test"},
	}); err != nil {
		t.Fatal(err)
	}
}

func multiSkillRequireBinding(t *testing.T, state State, skill uint16, slot byte, itemID int64, index uint16) Binding {
	t.Helper()
	if state.Skill != skill {
		t.Fatalf("active skill=%d want=%d", state.Skill, skill)
	}
	b, ok := state.Slots[slot]
	if !ok || b.ItemID != itemID || b.Index != index {
		t.Fatalf("active skill %d slot %d=%+v want item=%d index=%d", skill, slot, b, itemID, index)
	}
	return b
}

func TestCurrentSkillGroupsKeepSameSlotInstancesIndependentAndColdReload(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	inv, found, err := r.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load fixture inventory: found=%v err=%v", found, err)
	}
	inv.Slots["0:56"] = repo.ItemStack{ItemID: 101040495, Count: 1}
	if err := r.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		skill  uint16
		index  uint16
		itemID int64
	}{
		{38, 54, 101040493},
		{39, 55, 101040494},
		{40, 56, 101040495},
	}
	instances := map[uint16]string{}
	for _, tc := range tests {
		multiSkillApplyCurrent(t, r, Command{Skill: tc.skill, Slot: 12, List: 0, Index: tc.index})
		b := multiSkillRequireBinding(t, multiSkillLoad(t, r), tc.skill, 12, tc.itemID, tc.index)
		if b.Instance == "" {
			t.Fatalf("skill %d registration has no physical instance", tc.skill)
		}
		instances[tc.skill] = b.Instance
	}

	state := multiSkillLoad(t, r)
	if len(state.SkillSlots) != len(tests) {
		t.Fatalf("groups=%d want=%d state=%+v", len(state.SkillSlots), len(tests), state)
	}
	for _, tc := range tests {
		b, ok := state.SkillSlots[tc.skill][12]
		if !ok || b.ItemID != tc.itemID || b.Index != tc.index || b.Instance != instances[tc.skill] {
			t.Fatalf("skill %d group slot=%+v", tc.skill, b)
		}
	}
	if instances[38] == instances[39] || instances[39] == instances[40] || instances[38] == instances[40] {
		t.Fatalf("three skills share a physical instance: %+v", instances)
	}

	// Every switch is followed by a fresh Load, so active selection and cold
	// persistence are both exercised without sharing the caller's maps.
	for _, skill := range []uint16{38, 40, 39} {
		multiSkillApplyCurrent(t, r, Command{Skill: skill, Slot: SlotCount, List: 44, Index: 0xffff})
		loaded := multiSkillLoad(t, r)
		for _, tc := range tests {
			if tc.skill == skill {
				b := multiSkillRequireBinding(t, loaded, skill, 12, tc.itemID, tc.index)
				if b.Instance != instances[skill] {
					t.Fatalf("skill %d switched to wrong instance: %+v", skill, b)
				}
			}
		}
	}

	// The active map and active group must be independent aliases. The next
	// block also emulates instance.Migrate writing the top-level active view while
	// its persisted SkillSlots copy is still the pre-migration value.
	loaded := multiSkillLoad(t, r)
	active := loaded.Slots[12]
	active.Instance = "in-memory-only"
	loaded.Slots[12] = active
	if loaded.SkillSlots[loaded.Skill][12].Instance == active.Instance {
		t.Fatal("active Slots aliases the active SkillSlots group")
	}

	writeback := multiSkillLoad(t, r)
	active = writeback.Slots[12]
	active.Instance = "migration-writeback-instance"
	writeback.Slots[12] = active
	multiSkillSave(t, r, writeback)
	reloaded := multiSkillLoad(t, r)
	if got := reloaded.SkillSlots[reloaded.Skill][12].Instance; got != active.Instance {
		t.Fatalf("active write-back was not authoritative: got=%q want=%q", got, active.Instance)
	}
	if reloaded.SkillSlots[38][12].Instance != instances[38] {
		t.Fatal("active write-back changed an inactive skill group")
	}
}

func TestCurrentSkillGroupDeleteAndSkillZeroRestoreOnlyTarget(t *testing.T) {
	r := fixture(t)
	multiSkillApplyCurrent(t, r, Command{Skill: 38, Slot: 12, List: 0, Index: 54})
	multiSkillApplyCurrent(t, r, Command{Skill: 39, Slot: 12, List: 0, Index: 55})

	multiSkillApplyCurrent(t, r, Command{Skill: 38, Slot: 12, List: 44, Index: 0xffff})
	state := multiSkillLoad(t, r)
	if state.Skill != 38 || len(state.Slots) != 0 {
		t.Fatalf("deleted active group still visible: %+v", state)
	}
	if _, found := state.SkillSlots[38]; found {
		t.Fatal("empty group remained after deleting its final slot")
	}
	if state.SkillSlots[39][12].ItemID != 101040494 {
		t.Fatalf("single-group delete damaged skill39: %+v", state.SkillSlots)
	}

	multiSkillApplyCurrent(t, r, Command{Skill: 0, Slot: SlotCount, List: 44, Index: 0xffff})
	state = multiSkillLoad(t, r)
	if state.Skill != 0 || len(state.Slots) != 0 {
		t.Fatalf("skill zero was not an empty disable view: %+v", state)
	}
	if _, found := state.SkillSlots[0]; found {
		t.Fatal("skill zero was persisted as a real group")
	}
	if state.SkillSlots[39][12].ItemID != 101040494 {
		t.Fatalf("skill zero discarded an existing group: %+v", state.SkillSlots)
	}

	multiSkillApplyCurrent(t, r, Command{Skill: 38, Slot: SlotCount, List: 44, Index: 0xffff})
	state = multiSkillLoad(t, r)
	if state.Skill != 38 || len(state.Slots) != 0 || len(state.SkillSlots) != 1 {
		t.Fatalf("unregistered skill inherited another group: %+v", state)
	}
	multiSkillApplyCurrent(t, r, Command{Skill: 39, Slot: SlotCount, List: 44, Index: 0xffff})
	multiSkillRequireBinding(t, multiSkillLoad(t, r), 39, 12, 101040494, 55)
}

func TestCurrentLegacyStateMigratesOnlyToStoredSkill(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	if err := Apply(ctx, r, "6", "account-6", Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	legacy := multiSkillLoad(t, r)
	if legacy.SkillSlots != nil || legacy.Skill != 38 || len(legacy.Slots) != 1 {
		t.Fatalf("fixture is not ungrouped legacy state: %+v", legacy)
	}

	multiSkillApplyCurrent(t, r, Command{Skill: 39, Slot: SlotCount, List: 44, Index: 0xffff})
	upgraded := multiSkillLoad(t, r)
	if upgraded.Skill != 39 || len(upgraded.Slots) != 0 {
		t.Fatalf("new skill inherited legacy 38 equipment: %+v", upgraded)
	}
	if len(upgraded.SkillSlots) != 1 || upgraded.SkillSlots[38][12].ItemID != 101040493 {
		t.Fatalf("legacy skill was not migrated only to group38: %+v", upgraded.SkillSlots)
	}
	if _, copied := upgraded.SkillSlots[39]; copied {
		t.Fatal("legacy equipment was copied into the newly selected skill")
	}

	multiSkillApplyCurrent(t, r, Command{Skill: 38, Slot: SlotCount, List: 44, Index: 0xffff})
	multiSkillRequireBinding(t, multiSkillLoad(t, r), 38, 12, 101040493, 54)
}

func TestCurrentSelectedGroupsKeepIndependentSkillTreesAndColdProjection(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	multiSkillApplyGroup(t, r, 0, Command{Skill: 38, Slot: 12, List: 0, Index: 54})
	multiSkillApplyGroup(t, r, 1, Command{Skill: 39, Slot: 12, List: 0, Index: 55})

	state := multiSkillLoad(t, r)
	if state.SelectedSkills[0] != 38 || state.SelectedSkills[1] != 39 || state.CurrentGroup != 1 {
		t.Fatalf("selected groups=%+v current=%d", state.SelectedSkills, state.CurrentGroup)
	}
	if len(state.SkillSlots) != 2 {
		t.Fatalf("skill trees=%+v", state.SkillSlots)
	}
	group0Binding := state.SkillSlots[38][12]
	group1Binding := state.SkillSlots[39][12]
	if group0Binding.ItemID != 101040493 || group1Binding.ItemID != 101040494 {
		t.Fatalf("same-slot trees were mixed: g0=%+v g1=%+v", group0Binding, group1Binding)
	}
	if group0Binding.Instance == "" || group1Binding.Instance == "" || group0Binding.Instance == group1Binding.Instance {
		t.Fatalf("same-slot physical identities are not distinct: g0=%+v g1=%+v", group0Binding, group1Binding)
	}

	before := multiSkillLoad(t, r)
	projection0, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 0)
	if err != nil {
		t.Fatal(err)
	}
	projection1, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 1)
	if err != nil {
		t.Fatal(err)
	}
	if projection0.Owner.CharacterID != "6" || projection1.Owner.AccountID != "account-6" {
		t.Fatalf("group projection owner mismatch: g0=%+v g1=%+v", projection0.Owner, projection1.Owner)
	}
	if projection0.State.Skill != 38 || projection0.State.Slots[12].ItemID != 101040493 || projection0.State.Slots[12].Instance != group0Binding.Instance {
		t.Fatalf("cold group0 projection=%+v items=%+v", projection0.State, projection0.Items)
	}
	if projection1.State.Skill != 39 || projection1.State.Slots[12].ItemID != 101040494 || projection1.State.Slots[12].Instance != group1Binding.Instance {
		t.Fatalf("cold group1 projection=%+v items=%+v", projection1.State, projection1.Items)
	}
	if projection0.State.Slots[12].ItemID == projection1.State.Slots[12].ItemID {
		t.Fatal("group projections selected the same equipment tree")
	}
	after := multiSkillLoad(t, r)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("group projection changed active persisted state: before=%+v after=%+v", before, after)
	}
}

func TestCurrentGroupCancelLeavesOtherGroupIntact(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	multiSkillApplyGroup(t, r, 0, Command{Skill: 38, Slot: 12, List: 0, Index: 54})
	multiSkillApplyGroup(t, r, 1, Command{Skill: 39, Slot: 12, List: 0, Index: 55})
	before := multiSkillLoad(t, r)
	group0Before := before.SkillSlots[38][12]

	multiSkillApplyGroup(t, r, 1, Command{Skill: 0, Slot: SlotCount, List: 44, Index: 0xffff})
	state := multiSkillLoad(t, r)
	if state.SelectedSkills[0] != 38 {
		t.Fatalf("group0 selection changed: %+v", state.SelectedSkills)
	}
	if _, selected := state.SelectedSkills[1]; selected {
		t.Fatalf("group1 cancellation remained selected: %+v", state.SelectedSkills)
	}
	if state.CurrentGroup != 1 || state.Skill != 0 || len(state.Slots) != 0 {
		t.Fatalf("group1 cancellation active view=%+v", state)
	}
	if len(state.SkillSlots) != 2 || state.SkillSlots[38][12] != group0Before || state.SkillSlots[39][12].ItemID != 101040494 {
		t.Fatalf("group1 cancellation damaged saved skill trees: %+v", state.SkillSlots)
	}

	projection0, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 0)
	if err != nil || projection0.State.Skill != 38 || projection0.State.Slots[12].ItemID != 101040493 {
		t.Fatalf("group0 projection after cancelling g1: projection=%+v err=%v", projection0, err)
	}
	projection1, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 1)
	if err != nil || projection1.State.Skill != 0 || len(projection1.State.Slots) != 0 {
		t.Fatalf("cancelled group1 projection: projection=%+v err=%v", projection1, err)
	}
}

func TestCurrentGroupMissingItemNeverFallsBackToOtherGroup(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	multiSkillApplyGroup(t, r, 0, Command{Skill: 38, Slot: 12, List: 0, Index: 54})
	multiSkillApplyGroup(t, r, 1, Command{Skill: 39, Slot: 12, List: 0, Index: 55})

	inv, found, err := r.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%v err=%v", found, err)
	}
	delete(inv.Slots, "0:55")
	if err := r.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}

	projection1, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 1)
	if err != nil {
		t.Fatal(err)
	}
	if projection1.State.Skill != 39 || len(projection1.State.Slots) != 0 || len(projection1.Items) != 0 || projection1.Omitted != 1 {
		t.Fatalf("missing g1 item inherited another tree: %+v", projection1)
	}
	projection0, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 0)
	if err != nil || projection0.State.Slots[12].ItemID != 101040493 || projection0.Omitted != 0 {
		t.Fatalf("missing g1 item disturbed g0: projection=%+v err=%v", projection0, err)
	}
}

func TestCurrentLegacySelectionMigratesOnlyToGroupZero(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	if err := Apply(ctx, r, "6", "account-6", Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}

	// Initialising group state while cancelling group1 must migrate the old
	// active tree only to group0.
	multiSkillApplyGroup(t, r, 1, Command{Skill: 0, Slot: SlotCount, List: 44, Index: 0xffff})
	state := multiSkillLoad(t, r)
	if state.SelectedSkills[0] != 38 || len(state.SelectedSkills) != 1 || state.CurrentGroup != 1 {
		t.Fatalf("legacy selection copied outside group0: %+v current=%d", state.SelectedSkills, state.CurrentGroup)
	}
	if len(state.SkillSlots) != 1 || state.SkillSlots[38][12].ItemID != 101040493 {
		t.Fatalf("legacy tree migration=%+v", state.SkillSlots)
	}

	multiSkillApplyGroup(t, r, 1, Command{Skill: 39, Slot: 12, List: 0, Index: 55})
	state = multiSkillLoad(t, r)
	if state.SelectedSkills[0] != 38 || state.SelectedSkills[1] != 39 {
		t.Fatalf("group selections=%+v", state.SelectedSkills)
	}
	if state.SkillSlots[38][12].ItemID != 101040493 || state.SkillSlots[39][12].ItemID != 101040494 {
		t.Fatalf("legacy tree was copied into group1: %+v", state.SkillSlots)
	}
}

func TestSelectedGroupBoundsAndInvalidSelectionFailClosed(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	if err := ApplyCurrentGroup(ctx, r, "6", "account-6", 2, Command{Skill: 38, Slot: SlotCount, List: 44, Index: 0xffff}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("group2 registration error=%v", err)
	}
	if _, err := LoadResolvedOwnedGroup(ctx, r, "6", "account-6", 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("group2 projection error=%v", err)
	}

	valid := Binding{List: 0, Index: 54, ItemID: 101040493, Instance: "valid-instance", SourceKind: SourceCurrentInstance}
	for _, tc := range []struct {
		name         string
		selected     map[byte]uint16
		currentGroup byte
	}{
		{name: "selected_key2", selected: map[byte]uint16{0: 38, 2: 39}},
		{name: "selected_zero_skill", selected: map[byte]uint16{0: 38, 1: 0}},
		{name: "current_group2", selected: map[byte]uint16{0: 38}, currentGroup: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			multiSkillSave(t, r, State{
				WireProfile:    CurrentWireProfile,
				Skill:          38,
				Slots:          map[byte]Binding{12: valid},
				SkillSlots:     map[uint16]map[byte]Binding{38: {12: valid}, 39: {12: valid}},
				SelectedSkills: tc.selected,
				CurrentGroup:   tc.currentGroup,
			})
			before, found, err := r.Settings.Load(ctx, Scope("6"))
			if err != nil || !found {
				t.Fatalf("load raw state: found=%v err=%v", found, err)
			}
			if _, _, err := Load(ctx, r, "6", "account-6"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid group metadata error=%v", err)
			}
			after, found, err := r.Settings.Load(ctx, Scope("6"))
			if err != nil || !found || after.Values["state"] != before.Values["state"] {
				t.Fatalf("invalid metadata rewrote stored groups: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

type multiSkillRejectingSettings struct {
	repo.SettingsRepository
	err error
}

func (s multiSkillRejectingSettings) Save(context.Context, repo.SettingsRecord) error {
	return s.err
}

type multiSkillFailingBuffUOW struct {
	repo.CeraShopAssetUnitOfWork
	err error
}

func (u multiSkillFailingBuffUOW) WithinBuffOwnedSnapshot(
	ctx context.Context,
	accountID string,
	characterID string,
	scope string,
	apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository, repo.BuffPetMetadataReader, repo.OwnedLegacyInventoryRepository) error,
) error {
	base, ok := u.CeraShopAssetUnitOfWork.(repo.BuffOwnedSnapshotUnitOfWork)
	if !ok {
		return repo.ErrBuffOwnedSnapshotUnavailable
	}
	return base.WithinBuffOwnedSnapshot(ctx, accountID, characterID, scope,
		func(characters repo.CharacterRepository, inventories repo.InventoryRepository, equipment repo.EquipmentRepository, settings repo.SettingsRepository, pet repo.BuffPetMetadataReader, legacy repo.OwnedLegacyInventoryRepository) error {
			return apply(characters, inventories, equipment, multiSkillRejectingSettings{SettingsRepository: settings, err: u.err}, pet, legacy)
		})
}

func TestCurrentSkillGroupSaveFailureDoesNotEraseExistingGroups(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	multiSkillApplyCurrent(t, r, Command{Skill: 38, Slot: 12, List: 0, Index: 54})
	before := multiSkillLoad(t, r)
	invBefore, found, err := r.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%v err=%v", found, err)
	}

	bad := r
	bad.CeraShopAssets = multiSkillFailingBuffUOW{
		CeraShopAssetUnitOfWork: r.CeraShopAssets,
		err:                     errors.New("forced settings save failure"),
	}
	if err := ApplyCurrent(ctx, bad, "6", "account-6", Command{Skill: 39, Slot: 12, List: 0, Index: 55}); err == nil {
		t.Fatal("forced settings failure was not returned")
	}

	after := multiSkillLoad(t, r)
	invAfter, found, err := r.Inventory.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("reload inventory: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed transaction changed registration: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(invBefore, invAfter) {
		t.Fatal("failed transaction retained allocated inventory instance metadata")
	}
}

func TestCurrentSkillGroupLimitRejectsNewGroupWithoutDroppingExisting(t *testing.T) {
	ctx := context.Background()
	r := fixture(t)
	groups := make(map[uint16]map[byte]Binding, MaxSkillGroups)
	binding := Binding{List: 0, Index: 54, ItemID: 101040493, Instance: "limit-instance", SourceKind: SourceCurrentInstance}
	for skill := uint16(1); skill <= MaxSkillGroups; skill++ {
		groups[skill] = map[byte]Binding{12: binding}
	}
	multiSkillSave(t, r, State{
		WireProfile: CurrentWireProfile,
		Skill:       1,
		Slots:       map[byte]Binding{12: binding},
		SkillSlots:  groups,
	})
	before := multiSkillLoad(t, r)
	if len(before.SkillSlots) != MaxSkillGroups {
		t.Fatalf("seed groups=%d", len(before.SkillSlots))
	}

	err := ApplyCurrent(ctx, r, "6", "account-6", Command{Skill: MaxSkillGroups + 1, Slot: 12, List: 0, Index: 55})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("new group at limit error=%v", err)
	}
	after := multiSkillLoad(t, r)
	if len(after.SkillSlots) != MaxSkillGroups {
		t.Fatalf("limit rejection dropped groups: got=%d want=%d", len(after.SkillSlots), MaxSkillGroups)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("limit rejection changed state: before=%+v after=%+v", before, after)
	}
}

func TestLoadRejectsInvalidInactiveSkillGroup(t *testing.T) {
	r := fixture(t)
	valid := Binding{List: 0, Index: 54, ItemID: 101040493, Instance: "valid-instance", SourceKind: SourceCurrentInstance}
	for _, tc := range []struct {
		name  string
		slots map[byte]Binding
	}{
		{name: "slot33", slots: map[byte]Binding{SlotCount: valid}},
		{name: "missing_item", slots: map[byte]Binding{12: {List: 0, Index: 54}}},
		{name: "bad_identity", slots: map[byte]Binding{12: {List: 0, Index: 54, ItemID: 101040493, SourceKind: SourceCurrentInstance}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			multiSkillSave(t, r, State{
				WireProfile: CurrentWireProfile,
				Skill:       38,
				Slots:       map[byte]Binding{12: valid},
				SkillSlots: map[uint16]map[byte]Binding{
					38: {12: valid},
					39: tc.slots,
				},
			})
			if _, _, err := Load(context.Background(), r, "6", "account-6"); err == nil {
				t.Fatal("invalid inactive group loaded")
			}
		})
	}
}
