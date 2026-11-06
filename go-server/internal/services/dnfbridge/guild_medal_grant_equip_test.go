package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exercise the production PVF placement validator, equipment owner, gem asset
// transaction and a newly created owner on reload. No stored raw row is seeded.
func TestGrantedMedalEquipThenGuardianGemSurvivesReload(t *testing.T) {
	s, repos := newGuardianGemUniquenessService(t)
	validator := &currentEquipmentPlacementValidator{selectedAccountID: "fixture", selectedCharacterID: 77,
		characters: repos.Character, pvfSource: equipmentPlacementPVFSource{"equipment/flag/test_medal.equ": equipmentPlacementPVFDocument("[flag]")},
		equipmentPaths: map[int64]string{100380017: "flag/test_medal.equ"}}
	runGrantedMedalEquipThenGuardianGemSurvivesReload(t, s, repos, validator)
}

func TestRealPVFGrantedMedalEquipThenGuardianGemSurvivesReload(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to a complete Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ref, found := catalog.itemRefs[100380017]
	if !found {
		t.Fatal("real PVF is missing the original test medal")
	}
	repos := testRepositoryGroup()
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, pvfItemCatalog: catalog}
	validator := &currentEquipmentPlacementValidator{selectedAccountID: "fixture", selectedCharacterID: 77,
		characters: repos.Character, pvfSource: archive, equipmentPaths: map[int64]string{100380017: ref.path}}
	runGrantedMedalEquipThenGuardianGemSurvivesReload(t, s, repos, validator)
}

func runGrantedMedalEquipThenGuardianGemSurvivesReload(t *testing.T, s *Service, repos dnfrepo.Group, validator *currentEquipmentPlacementValidator) {
	t.Helper()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "fixture", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
		"38:0": {ItemID: 100380017, Count: 1, Extra: map[string]string{
			"item_kind": "equipment", "equipment_type": "[flag]", "pvf_path": "equipment/" + validator.equipmentPaths[100380017], "quality_seed": "999999998",
		}},
		"38:49": {ItemID: 90002, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfequip.NewOwnerWithPlacementValidator(repos, validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Move(ctx, dnfequip.MoveCommand{SelectedCharacterID: 77, SourceListType: 38, SourceSlotIndex: 0,
		SourceInstanceValue: 12345, MoveCount: 1, DestinationListType: 3, DestinationSlotIndex: 32}); err != nil {
		t.Fatalf("granted medal cannot equip: %v", err)
	}
	request := currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90002, SocketIndex: 2}
	if _, err := s.commitCurrentGuardianGemUse(&gameSession{selectedCharacterID: 77}, request); err != nil {
		t.Fatal(err)
	}
	worn := loadGuardianGemUniquenessMedal(t, repos)
	if got := binary.LittleEndian.Uint16(worn[105:107]); got != 3 {
		t.Fatalf("gem socket=%d, want 3", got)
	}
	if _, exists := mustLoadCurrentSocketInventory(t, repos, "77").Slots["38:49"]; exists {
		t.Fatal("gem source was not consumed")
	}
	if _, err := s.commitCurrentGuardianGemUse(&gameSession{selectedCharacterID: 77}, request); err == nil {
		t.Fatal("consumed gem was applied twice")
	}
	owner, err = dnfequip.NewOwnerWithPlacementValidator(repos, validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Move(ctx, dnfequip.MoveCommand{SelectedCharacterID: 77, SourceListType: 3, SourceSlotIndex: 32,
		MoveCount: 1, DestinationListType: 38, DestinationSlotIndex: 0}); err != nil {
		t.Fatal(err)
	}
	owner, err = dnfequip.NewOwnerWithPlacementValidator(repos, validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Move(ctx, dnfequip.MoveCommand{SelectedCharacterID: 77, SourceListType: 38, SourceSlotIndex: 0,
		SourceInstanceValue: 12345, MoveCount: 1, DestinationListType: 3, DestinationSlotIndex: 32}); err != nil {
		t.Fatal(err)
	}
	if after := loadGuardianGemUniquenessMedal(t, repos); !bytes.Equal(worn[101:109], after[101:109]) {
		t.Fatal("gem sockets lost after unequip/reload/re-equip")
	}
}
