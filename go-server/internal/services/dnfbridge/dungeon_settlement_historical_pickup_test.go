package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A real pickup transaction followed by a real discard transaction must not
// require the discarded item to be recreated in the result packet.
func TestSettlementPickedThenDiscardedItemKeepsCommittedAssets(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, accountID: "account-1", dungeon: dungeonSessionState{runtime: runtime}}
	key := seedCurrentDungeonPickupDrop(t, s, runtime, 3227, 99)
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
		t.Fatal(err)
	}
	drop := runtime.DropOwner.byObjectKey[key]
	if drop.Status != runtimeDungeonDropConsumed || len(drop.PickupItemUpdateBody) != 3+currentItemListEntryWireSize {
		t.Fatal("pickup did not commit its full receipt")
	}
	slot := drop.DestinationSlot
	if handled, err := s.handleCurrentDungeonDiscard(session, buildCurrentDungeonDiscardTestRequest(248, 276, 0, slot, 1, 0)); err != nil || !handled {
		t.Fatalf("discard=%t %v", handled, err)
	}
	before, _, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := before.Slots[currentDungeonPickupMainSlotKey(int16(slot))]; present {
		t.Fatal("discard did not remove item")
	}
	for i := 0; i < 2; i++ {
		rows, err := s.currentDungeonCommittedDropReceiptsLocked(session, runtime, before)
		if err != nil || len(rows) != 0 {
			t.Fatalf("legally discarded pickup blocked result: rows=%v err=%v", rows, err)
		}
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "99")
	if !reflect.DeepEqual(before, after) || drop.DestinationSlot != slot || drop.Status != runtimeDungeonDropConsumed {
		t.Fatal("presentation changed inventory or pickup ownership")
	}
	verifyHistoricalPickupResultAndExit(t, runtime.DropOwner, before)
}

func verifyHistoricalPickupResultAndExit(t *testing.T, drops *runtimeDungeonDropOwner, inventory repo.InventoryRecord) {
	t.Helper()
	s, runtime, session, conn := prepareOrdinaryCompletedSettlementRuntime(t)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	character, _, _ := repos.Character.Load(ctx, "99")
	character.Level = 1
	character.Stats["exp"] = 90
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	runtime.Character = character
	runtime.DropOwner = drops
	runtime.Dungeon.Metadata.BasisLevel.Set = true
	runtime.Dungeon.Metadata.BasisLevel.Value = 1
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
	runtime.settlementPlayResultReceived = true
	runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
	plan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "99", DungeonID: runtime.Dungeon.ID, MazeIndex: runtime.MazeIndex, RunSeed: runtime.Seed}, "committed_pickup_result_test", dungeonCardRewardBundle{Gold: 33}, dungeonCardRewardBundle{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.settlementCardRewardState, err = newDungeonCardState(plan)
	if err != nil {
		t.Fatal(err)
	}
	resources := currentDungeonSettlementTestResources(t)
	if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
		t.Fatal(err)
	}
	committed, _, _ := repos.Character.Load(ctx, "99")
	runtime.settlementResultPlan = nil
	if err := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
		t.Fatal(err)
	}
	replay, _, _ := repos.Character.Load(ctx, "99")
	if replay.Stats["exp"] != committed.Stats["exp"] {
		t.Fatal("result retry granted experience twice")
	}
	conn.write.Reset()
	if err := s.sendCurrentDungeonSettlementResultsLocked(session, runtime, "historical_pickup_test"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []uint16{34, 37, 35} {
		frame, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if frame.Header.MsgID != op {
			t.Fatalf("result opcode=%d want=%d", frame.Header.MsgID, op)
		}
		conn.write.Reset()
		conn.write.Write(rest)
	}
	if err := s.handleDungeonCharacterStatistic(session, make([]byte, currentDungeonCharacterStatisticBodySize)); err != nil {
		t.Fatal(err)
	}
	if !completedDungeonSettlementResultReady(runtime, 99) {
		t.Fatal("result still blocks exit")
	}
	if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonCardSelectRightState(session, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonSelectCard(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if !runtime.settlementCardRewardCommitted {
		t.Fatal("card reward never committed")
	}
	if err := s.handleDungeonSelectCard(session, []byte{0}); err != nil {
		t.Fatal(err)
	}
	wallet, _, _ := repos.Character.Load(ctx, "99")
	if wallet.Stats["gold"] != committed.Stats["gold"]+33 {
		t.Fatal("card retry changed gold")
	}
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !runtime.townReturnOp24Sent {
		t.Fatal("completed run did not return to town")
	}
	after, _, _ := repos.Inventory.Load(ctx, "99")
	if !reflect.DeepEqual(inventory.Slots, after.Slots) {
		t.Fatal("result resurrected a discarded item")
	}
}

func TestSettlementHistoricalPickupRequiresCompleteOwnedReceipt(t *testing.T) {
	for _, name := range []string{"missing_ack", "missing_update", "wrong_picker", "wrong_item", "wrong_slot", "wrong_list", "zero_count", "unknown_kind", "not_committed"} {
		t.Run(name, func(t *testing.T) {
			s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
			session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, accountID: "account-1", dungeon: dungeonSessionState{runtime: runtime}}
			key := seedCurrentDungeonPickupDrop(t, s, runtime, 3227, 99)
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
				t.Fatal(err)
			}
			drop := runtime.DropOwner.byObjectKey[key]
			switch name {
			case "missing_ack":
				drop.PickupResponseBody = nil
			case "missing_update":
				drop.PickupItemUpdateBody = nil
			case "wrong_picker":
				drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(key, 98, drop.DestinationSlot)
			case "wrong_item":
				drop.PickupItemUpdateBody[5]++
			case "wrong_slot":
				drop.PickupItemUpdateBody[3]++
			case "wrong_list":
				drop.PickupItemUpdateBody[0] = 1
			case "zero_count":
				binary.LittleEndian.PutUint32(drop.PickupItemUpdateBody[9:13], 0)
			case "unknown_kind":
				drop.Item.Kind = "unknown"
			case "not_committed":
				drop.Status = runtimeDungeonDropAvailable
			}
			empty := repo.InventoryRecord{CharacterID: "99"}
			if err := repos.Inventory.Save(context.Background(), empty); err != nil {
				t.Fatal(err)
			}
			rows, err := s.currentDungeonCommittedDropReceiptsLocked(session, runtime, empty)
			if name == "not_committed" {
				if err != nil || len(rows) != 0 {
					t.Fatal("unpicked object became a receipt")
				}
				return
			}
			if !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
				t.Fatalf("incomplete pickup accepted: %v", err)
			}
		})
	}
}
