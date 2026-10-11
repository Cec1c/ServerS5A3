package buffswap

import (
	"context"
	"encoding/json"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

// Candidate-only API/field tests; never compiled or counted as baseline RED.
func TestReviewBuffOwnedBindingJSONCompatibilityAndExclusiveIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		b     Binding
		valid bool
	}{
		{"old_position", Binding{List: 0, Index: 54, ItemID: 123}, true},
		{"old_instance", Binding{List: 0, Index: 54, ItemID: 123, Instance: "id"}, true},
		{"current_instance", Binding{List: 0, Index: 54, ItemID: 123, Instance: "id", SourceKind: SourceCurrentInstance}, true},
		{"legacy_uid", Binding{List: 0, Index: 54, ItemID: 123, SourceKind: SourceLegacyUID, LegacyItemUID: 7000}, true},
		{"legacy_without_uid", Binding{List: 0, Index: 54, ItemID: 123, SourceKind: SourceLegacyUID}, false},
		{"uid_without_provenance", Binding{List: 0, Index: 54, ItemID: 123, LegacyItemUID: 7000}, false},
		{"mixed_legacy_instance", Binding{List: 0, Index: 54, ItemID: 123, Instance: "id", SourceKind: SourceLegacyUID, LegacyItemUID: 7000}, false},
		{"mixed_current_uid", Binding{List: 0, Index: 54, ItemID: 123, Instance: "id", SourceKind: SourceCurrentInstance, LegacyItemUID: 7000}, false},
		{"legacy_equipment_list_unproven", Binding{List: 3, Index: 12, ItemID: 123, SourceKind: SourceLegacyUID, LegacyItemUID: 7000}, false},
		{"unknown_source", Binding{List: 0, Index: 54, ItemID: 123, SourceKind: "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t)
			state := State{WireProfile: CurrentWireProfile, Slots: map[byte]Binding{12: tc.b}}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Settings.Save(context.Background(), repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw)}}); err != nil {
				t.Fatal(err)
			}
			got, found, err := Load(context.Background(), r, "6", "account-6")
			if tc.valid {
				if err != nil || !found || got.Slots[12] != tc.b {
					t.Fatalf("old/new JSON compatibility failed: %+v %v", got, err)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatalf("mixed/unproved identity accepted: %+v %v", got, err)
			}
		})
	}
}

type reviewOwnedRangeFailure struct {
	repo.OwnedLegacyInventoryRepository
	calls int
}

func (r *reviewOwnedRangeFailure) SelectOwnedBuffItems(ctx context.Context, id string, lists []byte) ([]repo.OwnedLegacyItem, error) {
	r.calls++
	for _, list := range lists {
		if list == 1 {
			return nil, errors.New("owned unrelated requested list1 range failure")
		}
	}
	return r.OwnedLegacyInventoryRepository.SelectOwnedBuffItems(ctx, id, lists)
}

type reviewOwnedRangeUOW struct {
	repo.CeraShopAssetUnitOfWork
	failure *reviewOwnedRangeFailure
}

func (u reviewOwnedRangeUOW) WithinBuffOwnedSnapshot(ctx context.Context, a, c, s string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository, repo.BuffPetMetadataReader, repo.OwnedLegacyInventoryRepository) error) error {
	return u.CeraShopAssetUnitOfWork.(repo.BuffOwnedSnapshotUnitOfWork).WithinBuffOwnedSnapshot(ctx, a, c, s, func(ch repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository, st repo.SettingsRepository, p repo.BuffPetMetadataReader, l repo.OwnedLegacyInventoryRepository) error {
		u.failure.OwnedLegacyInventoryRepository = l
		return apply(ch, i, e, st, p, u.failure)
	})
}
func TestReviewBuffOwnedLegacyColdWholeRangeFailsClosed(t *testing.T) {
	row := repo.OwnedLegacyItem{ItemUID: 7000, OwnerScope: "character", OwnerID: "6", CharacterID: "6", ItemKind: "equipment", Item: repo.LegacyInventoryItem{ListType: 0, SlotIndex: 54, ItemTemplateID: 123, StackCount: 1}}
	r := memory.NewMemoryGroupWithBuffLegacyItems([]repo.OwnedLegacyItem{row})
	ctx := context.Background()
	for _, err := range []error{r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "6", AccountID: "account-6"}), r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6"}), r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6"})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyCurrent(ctx, r, "6", "account-6", Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.Settings.Load(ctx, Scope("6"))
	failure := &reviewOwnedRangeFailure{}
	r.CeraShopAssets = reviewOwnedRangeUOW{r.CeraShopAssets, failure}
	projection, err := LoadResolvedOwned(ctx, r, "6", "account-6")
	if err == nil || failure.calls != 1 || len(projection.Items) != 0 || len(projection.State.Slots) != 0 {
		t.Fatalf("whole-range failure returned partial legacy projection: %+v %v calls%d", projection, err, failure.calls)
	}
	after, _, _ := r.Settings.Load(ctx, Scope("6"))
	if after.Values["state"] != before.Values["state"] {
		t.Fatal("failed projection altered persisted registration")
	}
}
