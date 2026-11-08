package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestAikenSupportTitleOnlyProjectsInEligibleActiveRun(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"13": {SlotIndex: 13, ItemID: currentAikenSupportTitleOwnedID},
		"14": {SlotIndex: 14, ItemID: 123456},
	}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 19}
	read := func(want uint32) {
		t.Helper()
		body, _, n, ok := service.buildCurrentEquippedItemListBodyForSession(ctx, session)
		if !ok || n != 2 {
			t.Fatalf("list3 unavailable: ok=%t count=%d", ok, n)
		}
		if got := binary.LittleEndian.Uint32(body[3+2 : 3+6]); got != want {
			t.Fatalf("list3 title=%d want=%d", got, want)
		}
		update, _, n, ok := service.buildCurrentEquipmentItemUpdateBodyForSession(ctx, session)
		if !ok || n != 2 {
			t.Fatalf("op14 unavailable: ok=%t count=%d", ok, n)
		}
		if got := binary.LittleEndian.Uint32(update[3+2 : 3+6]); got != want {
			t.Fatalf("op14 title=%d want=%d", got, want)
		}
		stored, found, err := repos.Equipment.Load(ctx, "19")
		if err != nil || !found || stored.Entries["13"].ItemID != currentAikenSupportTitleOwnedID {
			t.Fatalf("durable title changed: found=%t err=%v item=%d", found, err, stored.Entries["13"].ItemID)
		}
	}
	read(uint32(currentAikenSupportTitleOwnedID))
	character := dnfrepo.CharacterRecord{CharacterID: "19", Job: "4", Stats: map[string]int64{"grow_type": 0x21}}
	for _, id := range []int64{5097, 5098, 3570, 3571} {
		setCurrentAikenSupportTitleProjection(session, &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: id}, Character: character})
		read(uint32(currentAikenSupportTitleCombatID))
	}
	setCurrentAikenSupportTitleProjection(session, &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 6062}, Character: character})
	read(uint32(currentAikenSupportTitleOwnedID))
	setCurrentAikenSupportTitleProjection(session, nil)
	read(uint32(currentAikenSupportTitleOwnedID))
}

func TestAikenSupportTitleRejectsOtherClassesAndPrivateCombatDefinition(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	validator := &currentEquipmentPlacementValidator{
		selectedAccountID: "account-1", selectedCharacterID: 19, characters: repos.Character,
		pvfSource: equipmentPlacementPVFSource{
			"equipment/title/owned.equ":  equipmentPlacementPVFDocument("[title name]"),
			"equipment/title/combat.equ": equipmentPlacementPVFDocument("[title name]"),
		},
		equipmentPaths: map[int64]string{
			currentAikenSupportTitleOwnedID: "title/owned.equ", currentAikenSupportTitleCombatID: "title/combat.equ",
		},
	}
	placement := dnfequip.Placement{CharacterID: "19", ItemID: currentAikenSupportTitleOwnedID, SourceListType: 0, SourceSlotIndex: 1, TargetSlotIndex: 13}
	for _, test := range []struct {
		job, grow string
		allow     bool
	}{
		{"4", "33", true}, {"14", "33", true}, {"4", "17", false},
		{"14", "34", false}, {"11", "33", false}, {"4", "256", false},
	} {
		stats := map[string]int64{}
		if test.grow != "" {
			// The packed growth field is database-owned, not a client declaration.
			var grow int64
			for _, digit := range test.grow {
				grow = grow*10 + int64(digit-'0')
			}
			stats["grow_type"] = grow
		}
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Job: test.job, Level: 90, Stats: stats}); err != nil {
			t.Fatal(err)
		}
		err := validator.ValidateEquipmentPlacement(ctx, placement)
		if test.allow && err != nil || !test.allow && !errors.Is(err, errEquipmentPlacementAikenTitleRestricted) {
			t.Fatalf("job=%s grow=%s allow=%t err=%v", test.job, test.grow, test.allow, err)
		}
	}
	placement.ItemID = currentAikenSupportTitleCombatID
	if err := validator.ValidateEquipmentPlacement(ctx, placement); !errors.Is(err, errEquipmentPlacementAikenTitleRestricted) {
		t.Fatalf("private combat title equip allowed: %v", err)
	}
}
