package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type transitionInventoryReadCounter struct {
	dnfrepo.InventoryRepository
	loads int
}

func (r *transitionInventoryReadCounter) Load(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, bool, error) {
	r.loads++
	return r.InventoryRepository.Load(ctx, characterID)
}

func requireTransitionCharacterReads(t *testing.T, counter *partyFrameBatchCharacterRepository, ids ...string) {
	t.Helper()
	want := make(map[string]int, len(ids))
	for _, id := range ids {
		want[id]++
	}
	got := make(map[string]int, len(counter.loadIDs))
	for _, id := range counter.loadIDs {
		got[id]++
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("character reads=%v want exactly one per projected member=%v", got, want)
	}
}

func TestDungeonMemberReturnRosterReadsEachSurvivorOnce(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	addRetirementPeer(t, f, 101)
	addRetirementPeer(t, f, 102)
	before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	round69AlignInstancePartyID(t, f, before.ID)
	repositories, _ := f.service.repositoryProvider()
	counter := &partyFrameBatchCharacterRepository{CharacterRepository: repositories.Character}
	repositories.Character = counter
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	f.member.dungeon.runtime = nil
	if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
		t.Fatal(err)
	}
	requireTransitionCharacterReads(t, counter, "99", "101", "102")
	current, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found || len(current.Members) != 3 || f.leader.dungeon.runtime != f.leaderRuntime {
		t.Fatal("return changed the retained party or survivor dungeon")
	}
}

func TestDungeonRoomSlotTableReadsOnlyCharacterRows(t *testing.T) {
	f := newPartyFrameBatchRosterFixture(t, 4)
	f.service.options.currentClientVersion = "official-24.9"
	repositories, _ := f.service.repositoryProvider()
	inventory := &transitionInventoryReadCounter{InventoryRepository: repositories.Inventory}
	repositories.Inventory = inventory
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	for round := 0; round < 2; round++ {
		f.counter.reset()
		f.resetConns()
		if round == 1 {
			row := f.store.records["2001"]
			row.Name = "fresh_room_slot_name"
			f.store.records["2001"] = row
		}
		if err := f.service.sendDungeonPartyRoomSlotTable(f.sessions[0], nil, worldmap.DungeonRoomScene{}, dungeonPartyRoomSlotTableTriggerMove, "read_budget_test"); err != nil {
			t.Fatal(err)
		}
		requireTransitionCharacterReads(t, f.counter, "2001", "2002", "2003", "2004")
		if inventory.loads != 0 {
			t.Fatalf("identity-only room table loaded inventory %d times", inventory.loads)
		}
		frames := splitUpperFrames(t, f.conns[0].write.Bytes())
		if len(frames) != 1 || frames[0].msgID != 9 {
			t.Fatalf("room table frames=%d, want exactly one op9", len(frames))
		}
		if round == 1 && !bytes.Contains(frames[0].body, []byte("fresh_room_slot_name")) {
			t.Fatal("room transition reused a previous message's character data")
		}
	}
}
