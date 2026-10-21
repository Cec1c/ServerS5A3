package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var reviewAntonHatcheryPolicyIDs = []uint32{222, 223, 224}

var reviewAntonHatcheryCorners = map[uint32]worldmap.RoomCoordinate{
	221: {X: 3, Y: 0},
	222: {X: 3, Y: 4},
	223: {X: 0, Y: 4},
	224: {X: 0, Y: 0},
}

func reviewAntonHatcheryStates(progress *raid.AntonProgression) map[uint32]byte {
	states := map[uint32]byte{}
	if progress == nil {
		return states
	}
	for _, dungeon := range progress.Dungeons() {
		states[dungeon.ID] = dungeon.State
	}
	return states
}

func TestReviewR180ActualAntonHatcheryPolicyOpens234(t *testing.T) {
	_, _, _, progress := actualAntonHatcheryEnergyOwnerForCorner(t, 222)
	states := reviewAntonHatcheryStates(progress)
	if states[221] == 0 {
		t.Fatal("actual Anton progression left disabled first hatchery 221 open")
	}
	for _, id := range reviewAntonHatcheryPolicyIDs {
		state, found := states[id]
		if !found || state != 0 {
			t.Fatalf("actual Anton progression hatchery %d state=%d found=%t, want open", id, state, found)
		}
	}
}

func TestReviewR180ActualAntonHatcheryEntryRejects221WithoutRuntimeSwap(t *testing.T) {
	s, session, old, progress := actualAntonHatcheryEnergyOwnerForCorner(t, 221)
	states := reviewAntonHatcheryStates(progress)
	if states[221] == 0 {
		t.Fatal("fixture did not observe the actual closed state of hatchery 221")
	}
	if old == nil || old.Dungeon.ID != 218 || old.Request.DungeonID != 218 || session.dungeon.runtime != old {
		t.Fatalf("fixture is not the actual active 218 energy owner: old=%p current=%p", old, session.dungeon.runtime)
	}

	connection, ok := session.conn.(*bufferConn)
	if !ok {
		t.Fatalf("fixture connection type %T is not a buffer connection", session.conn)
	}
	connection.write.Reset()
	before := session.dungeon.runtime
	beforeToken := session.dungeon.runToken
	beforeRunID := old.raidEntry.RunID
	err := s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:  221,
		Difficulty: 2,
	}))
	if err != nil {
		t.Fatalf("production entry rejected hatchery 221: %v", err)
	}
	if session.dungeon.runtime != old || session.dungeon.runtime != before {
		t.Fatalf("disabled hatchery 221 changed runtime owner: before=%p after=%p old=%p", before, session.dungeon.runtime, old)
	}
	if session.dungeon.runToken != beforeToken {
		t.Fatalf("disabled hatchery 221 changed lifecycle token: %d -> %d", beforeToken, session.dungeon.runToken)
	}
	if old.raidEntry == nil || old.raidEntry.RunID != beforeRunID || old.antonHatcheryTransfer != nil {
		t.Fatal("disabled hatchery 221 mutated the active energy entry")
	}

	var refusals, dungeonEntries int
	for _, packet := range splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes()) {
		if packet.Header.Classification == 0 {
			switch packet.Header.MsgID {
			case currentDungeonInfoNotification, currentDungeonStartNotification:
				dungeonEntries++
			}
			continue
		}
		if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.GameTypeSelectDungeon) {
			continue
		}
		want := []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}
		if !bytes.Equal(packet.Body, want) {
			t.Fatalf("unexpected op16 refusal body=%x, want %x", packet.Body, want)
		}
		refusals++
	}
	if refusals != 1 {
		t.Fatalf("disabled hatchery 221 produced %d op16 refusals, want exactly one", refusals)
	}
	if dungeonEntries != 0 {
		t.Fatalf("disabled hatchery 221 emitted %d dungeon entry packets", dungeonEntries)
	}
	if s.currentRaidDungeonSelectionAllowed(session, 221) {
		t.Fatal("production selection gate still accepts disabled hatchery 221")
	}
}
