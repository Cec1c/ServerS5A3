package inventory

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGuardianShieldOp19CommitsMainThenReturnsACKAndOp580(t *testing.T) {
	repositories := newGuardianShieldHandlerRepositories(t)
	result, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(34, 0, 113370003, 0, 33, 0, 0, 0, 19),
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        repositories,
		GuardianShield:      guardianShieldHandlerResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ResponseAllowed || len(result.UpperResponses) != 2 ||
		len(result.PostActions) != 2 ||
		result.PostActions[0] != alignedcmd.PostActionRefreshSelectedActorExpandedEquipment ||
		result.PostActions[1] != alignedcmd.PostActionRefreshSelectedActorAppearance {
		t.Fatalf("result=%+v", result)
	}
	ack := result.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketMoveItemspace) || len(ack.Body) != 11 ||
		ack.Body[0] != 1 || ack.Body[1] != 34 || ack.Body[8] != 33 {
		t.Fatalf("op19 ACK=%+v", ack)
	}
	deckProjection := result.UpperResponses[1]
	if deckProjection.MsgID != currentGuardianShieldDeckNotificationMsgID ||
		deckProjection.Classification != 0 || len(deckProjection.Body) != 20 ||
		binary.LittleEndian.Uint32(deckProjection.Body[0:4]) != 113370003 {
		t.Fatalf("op580=%+v", deckProjection)
	}
	assertGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{113370003})
}

func TestGuardianShieldOp19UnequipClearsMainThenRequestsTownActorRefresh(t *testing.T) {
	repositories := newGuardianShieldHandlerRepositories(t)
	seedGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{113370003})

	result, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(33, 0, 0, 0, 34, dnfguardianshield.DeckSlotCount, 0, 0, 19),
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        repositories,
		GuardianShield:      guardianShieldHandlerResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ResponseAllowed || len(result.UpperResponses) != 2 ||
		len(result.PostActions) != 1 ||
		result.PostActions[0] != alignedcmd.PostActionRefreshSelectedActorExpandedEquipment {
		t.Fatalf("result=%+v", result)
	}
	if !bytes.Equal(result.UpperResponses[1].Body, make([]byte, dnfguardianshield.DeckSlotCount*4)) {
		t.Fatalf("cleared op580=%x", result.UpperResponses[1].Body)
	}
	equipment, found, err := repositories.Equipment.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	if _, exists := equipment.Entries["24"]; exists {
		t.Fatalf("guardian shield slot 24 remained after unequip: %+v", equipment.Entries["24"])
	}
	assertGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{})
}

func TestGuardianShieldOp19RejectsBackupOnlyMoveWithoutMutation(t *testing.T) {
	repositories := newGuardianShieldHandlerRepositories(t)
	seedGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{113370003, 113370004, 113370005})
	result, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                currentMoveItemspaceBody(33, 1, 0, 0, 33, 2, 0, 0, 19),
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        repositories,
		GuardianShield:      guardianShieldHandlerResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UpperResponses) != 1 || !bytes.Equal(result.UpperResponses[0].Body, []byte{0, moveItemspaceRejectedErrorCode, 33, 33}) {
		t.Fatalf("rejection=%+v", result)
	}
	assertGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{113370003, 113370004, 113370005})
}

func TestGuardianShieldOp657SavesBackupsButRejectsMainMutation(t *testing.T) {
	repositories := newGuardianShieldHandlerRepositories(t)
	seedGuardianShieldHandlerDeck(t, repositories, dnfguardianshield.Deck{113370003, 113370004})

	requestDeck := dnfguardianshield.Deck{113370003, 0, 113370004}
	success, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketChangeDeckInfo),
		Body:                buildCurrentGuardianShieldDeckBody(requestDeck),
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        repositories,
		GuardianShield:      guardianShieldHandlerResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(success.UpperResponses) != 1 || len(success.PostActions) != 0 ||
		success.UpperResponses[0].Classification != 1 ||
		!bytes.Equal(success.UpperResponses[0].Body, buildCurrentGuardianShieldDeckSuccessBody(requestDeck)) {
		t.Fatalf("op657 success=%+v", success)
	}
	assertGuardianShieldHandlerDeck(t, repositories, requestDeck)

	rejectedDeck := requestDeck
	rejectedDeck[0] = 113370005
	rejected, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketChangeDeckInfo),
		Body:                buildCurrentGuardianShieldDeckBody(rejectedDeck),
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        repositories,
		GuardianShield:      guardianShieldHandlerResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected.UpperResponses) != 1 || !bytes.Equal(rejected.UpperResponses[0].Body, []byte{0}) {
		t.Fatalf("op657 rejection=%+v", rejected)
	}
	assertGuardianShieldHandlerDeck(t, repositories, requestDeck)
}

func newGuardianShieldHandlerRepositories(t *testing.T) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Job:         "12",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			9001: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func guardianShieldHandlerResolver(_ context.Context, itemID uint32, growType byte) (alignedcmd.GuardianShieldResolution, error) {
	if growType != 1 || itemID < 113370003 || itemID > 113370039 {
		return alignedcmd.GuardianShieldResolution{}, errors.New("not in test catalog")
	}
	return alignedcmd.GuardianShieldResolution{
		ItemID:        itemID,
		PVFPath:       "equipment/character/knight/weapon/shield/test.equ",
		EquipmentType: "[support weapon]",
		UnlockKind:    alignedcmd.GuardianShieldUnlockQuest,
		ClearQuestID:  9001,
	}, nil
}

func seedGuardianShieldHandlerDeck(t *testing.T, repositories dnfrepo.Group, deck dnfguardianshield.Deck) {
	t.Helper()
	owner, err := dnfguardianshield.NewOwner(repositories, guardianShieldHandlerResolver)
	if err != nil {
		t.Fatal(err)
	}
	if deck[0] != 0 {
		if _, err := owner.Move(context.Background(), dnfguardianshield.MoveCommand{
			CharacterID:          19,
			AccountID:            "dnf:1",
			SourceListType:       dnfguardianshield.WireListCatalog,
			SourceSlotIndex:      0,
			SourceShieldItemID:   deck[0],
			DestinationListType:  dnfguardianshield.WireListEquipped,
			DestinationSlotIndex: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.SaveDeck(context.Background(), 19, "dnf:1", deck); err != nil {
		t.Fatal(err)
	}
}

func assertGuardianShieldHandlerDeck(t *testing.T, repositories dnfrepo.Group, want dnfguardianshield.Deck) {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	got, err := dnfguardianshield.LoadDeck(character)
	if err != nil || got != want {
		t.Fatalf("deck=%v want=%v err=%v", got, want, err)
	}
}
