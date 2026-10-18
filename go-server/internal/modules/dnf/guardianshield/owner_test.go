package guardianshield

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerEquipMoveAndRemovePersistFiveSlotDeckAndMainProjection(t *testing.T) {
	ctx := context.Background()
	repositories := newGuardianShieldTestRepositories(t, map[string]int64{
		"grow_type":              1,
		"guardian_shield_deck_0": 1001,
		"guardian_shield_deck_1": 1002,
	})
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"24": testGuardianShieldProjection(1001),
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories, guardianShieldTestResolver)
	if err != nil {
		t.Fatal(err)
	}

	swapped, err := owner.Move(ctx, MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListEquipped,
		SourceSlotIndex:      1,
		DestinationListType:  WireListEquipped,
		DestinationSlotIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Deck != (Deck{1002, 1001}) || !swapped.Changed || !swapped.MainChanged {
		t.Fatalf("swap result=%+v", swapped)
	}
	assertGuardianShieldState(t, repositories, Deck{1002, 1001}, 1002)

	removed, err := owner.Move(ctx, MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListEquipped,
		SourceSlotIndex:      0,
		DestinationListType:  WireListCatalog,
		DestinationSlotIndex: DeckSlotCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Deck != (Deck{0, 1001}) || !removed.Changed || !removed.MainChanged {
		t.Fatalf("remove result=%+v", removed)
	}
	assertGuardianShieldState(t, repositories, Deck{0, 1001}, 0)

	equipped, err := owner.Move(ctx, MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListCatalog,
		SourceSlotIndex:      0,
		SourceShieldItemID:   1003,
		DestinationListType:  WireListEquipped,
		DestinationSlotIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if equipped.Deck != (Deck{1003, 1001}) || !equipped.MainChanged {
		t.Fatalf("equip result=%+v", equipped)
	}
	assertGuardianShieldState(t, repositories, Deck{1003, 1001}, 1003)
}

func TestMainProjectionItemIDRejectsUnownedSlot24(t *testing.T) {
	record := dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"24": {
				SlotIndex: ActorSlot,
				ItemID:    1001,
				Extra: map[string]string{
					"current_exe_equipment_type": "24",
					"current_exe_runtime_move":   "1",
				},
			},
		},
	}
	if itemID, ok := MainProjectionItemID(record); ok || itemID != 0 {
		t.Fatalf("foreign slot-24 row recognized as guardian shield item=%d ok=%t", itemID, ok)
	}
	entry := record.Entries["24"]
	entry.Extra[projectionMarker] = "1"
	record.Entries["24"] = entry
	if itemID, ok := MainProjectionItemID(record); !ok || itemID != 1001 {
		t.Fatalf("guardian slot-24 row item=%d ok=%t", itemID, ok)
	}
}

func TestOwnerSaveDeckRejectsMainMutationAndDuplicateWithoutWrites(t *testing.T) {
	ctx := context.Background()
	repositories := newGuardianShieldTestRepositories(t, map[string]int64{
		"grow_type":              2,
		"guardian_shield_deck_0": 2001,
		"guardian_shield_deck_1": 2002,
	})
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{"24": testGuardianShieldProjection(2001)},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories, guardianShieldTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.SaveDeck(ctx, 19, "dnf:1", Deck{2003, 2002}); !errors.Is(err, ErrMainDeckMutationRequiresMove) {
		t.Fatalf("main mutation error=%v", err)
	}
	assertGuardianShieldState(t, repositories, Deck{2001, 2002}, 2001)

	if _, err := owner.SaveDeck(ctx, 19, "dnf:1", Deck{2001, 2002, 2002}); !errors.Is(err, ErrDeckDuplicate) {
		t.Fatalf("duplicate error=%v", err)
	}
	assertGuardianShieldState(t, repositories, Deck{2001, 2002}, 2001)
}

func TestOwnerRollsBackDeckWhenMainProjectionSaveFails(t *testing.T) {
	ctx := context.Background()
	repositories := newGuardianShieldTestRepositories(t, map[string]int64{
		"grow_type":              1,
		"guardian_shield_deck_0": 3001,
	})
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{"24": testGuardianShieldProjection(3001)},
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("equipment commit failed")
	repositories.CharacterAssets = failingGuardianShieldAssets{
		base: repositories.CharacterAssets,
		err:  wantErr,
	}
	owner, err := NewOwner(repositories, guardianShieldTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Move(ctx, MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListCatalog,
		SourceSlotIndex:      0,
		SourceShieldItemID:   3002,
		DestinationListType:  WireListEquipped,
		DestinationSlotIndex: 0,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Move error=%v want=%v", err, wantErr)
	}
	assertGuardianShieldState(t, repositories, Deck{3001}, 3001)
}

func TestOwnerRejectsShieldWhosePVFClearQuestIsIncomplete(t *testing.T) {
	ctx := context.Background()
	repositories := newGuardianShieldTestRepositories(t, map[string]int64{"grow_type": 1})
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			9001: {Status: "active"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories, guardianShieldTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Move(ctx, MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListCatalog,
		SourceSlotIndex:      0,
		SourceShieldItemID:   1001,
		DestinationListType:  WireListEquipped,
		DestinationSlotIndex: 0,
	})
	if !errors.Is(err, ErrClearQuestIncomplete) {
		t.Fatalf("locked shield error=%v", err)
	}
	assertGuardianShieldState(t, repositories, Deck{}, 0)
}

func TestValidateUnlockAcceptsLegacyProgressQuestCompletion(t *testing.T) {
	resolution := alignedcmd.GuardianShieldResolution{
		ItemID:       1001,
		UnlockKind:   alignedcmd.GuardianShieldUnlockQuest,
		ClearQuestID: 9001,
	}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		Progress: map[int64]dnfrepo.QuestState{
			9001: {Status: "completed"},
		},
	}
	if err := ValidateUnlock(dnfrepo.CharacterRecord{Level: 90}, record, resolution); err != nil {
		t.Fatalf("progress-only completion rejected: %v", err)
	}
}

func TestOwnerLevelUnlockDoesNotRequireQuestRepository(t *testing.T) {
	ctx := context.Background()
	repositories := newGuardianShieldTestRepositories(t, map[string]int64{"grow_type": 1})
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 89
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	repositories.Quest = nil
	resolver := func(_ context.Context, itemID uint32, growType byte) (alignedcmd.GuardianShieldResolution, error) {
		if itemID != 4001 || (growType != 1 && growType != 2) {
			return alignedcmd.GuardianShieldResolution{}, errors.New("invalid test level shield")
		}
		return alignedcmd.GuardianShieldResolution{
			ItemID:        itemID,
			PVFPath:       "equipment/character/knight/weapon/shield/level.equ",
			EquipmentType: "[support weapon]",
			UnlockKind:    alignedcmd.GuardianShieldUnlockLevel,
			MinimumLevel:  90,
		}, nil
	}
	owner, err := NewOwner(repositories, resolver)
	if err != nil {
		t.Fatal(err)
	}
	command := MoveCommand{
		CharacterID:          19,
		AccountID:            "dnf:1",
		SourceListType:       WireListCatalog,
		SourceSlotIndex:      0,
		SourceShieldItemID:   4001,
		DestinationListType:  WireListEquipped,
		DestinationSlotIndex: 0,
	}
	if _, err := owner.Move(ctx, command); !errors.Is(err, ErrLevelInsufficient) {
		t.Fatalf("below-level error=%v", err)
	}
	character, _, _ = repositories.Character.Load(ctx, "19")
	character.Level = 90
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Move(ctx, command); err != nil {
		t.Fatalf("level unlock with nil quest repository: %v", err)
	}
	assertGuardianShieldState(t, repositories, Deck{4001}, 4001)
}

func newGuardianShieldTestRepositories(t *testing.T, stats map[string]int64) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Job:         "12",
		Level:       90,
		Stats:       stats,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			9001: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func guardianShieldTestResolver(_ context.Context, itemID uint32, growType byte) (alignedcmd.GuardianShieldResolution, error) {
	if itemID == 0 || (growType != 1 && growType != 2) {
		return alignedcmd.GuardianShieldResolution{}, errors.New("invalid test shield")
	}
	return alignedcmd.GuardianShieldResolution{
		ItemID:        itemID,
		PVFPath:       "equipment/character/knight/weapon/shield/test.equ",
		EquipmentType: "[support weapon]",
		UnlockKind:    alignedcmd.GuardianShieldUnlockQuest,
		ClearQuestID:  9001,
	}, nil
}

func testGuardianShieldProjection(itemID int64) dnfrepo.EquipmentEntry {
	raw := make([]byte, currentRawItemEntrySize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(ActorSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	return dnfrepo.EquipmentEntry{
		SlotIndex: ActorSlot,
		ItemID:    itemID,
		RawEntry:  raw,
		Extra:     map[string]string{projectionMarker: "1"},
	}
}

func assertGuardianShieldState(t *testing.T, repositories dnfrepo.Group, wantDeck Deck, wantMain int64) {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	deck, err := LoadDeck(character)
	if err != nil || deck != wantDeck {
		t.Fatalf("deck=%v want=%v err=%v", deck, wantDeck, err)
	}
	equipment, found, err := repositories.Equipment.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	entry, occupied := equipment.Entries["24"]
	if wantMain == 0 {
		if occupied {
			t.Fatalf("unexpected main projection=%+v", entry)
		}
		return
	}
	if !occupied || entry.ItemID != wantMain || entry.SlotIndex != ActorSlot ||
		len(entry.RawEntry) != currentRawItemEntrySize || entry.Extra[projectionMarker] != "1" {
		t.Fatalf("main projection=%+v raw_len=%d want item=%d", entry, len(entry.RawEntry), wantMain)
	}
}

type failingGuardianShieldAssets struct {
	base dnfrepo.CharacterAssetUnitOfWork
	err  error
}

func (u failingGuardianShieldAssets) WithinCharacterAssets(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return u.base.WithinCharacterAssets(ctx, characterID, func(
		characters dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		return apply(characters, inventory, failingGuardianShieldEquipment{EquipmentRepository: equipment, err: u.err})
	})
}

type failingGuardianShieldEquipment struct {
	dnfrepo.EquipmentRepository
	err error
}

func (r failingGuardianShieldEquipment) Save(context.Context, dnfrepo.EquipmentRecord) error {
	return r.err
}
