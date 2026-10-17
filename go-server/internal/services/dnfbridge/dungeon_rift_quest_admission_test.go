package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Uses the shipped PVF, but isolated repositories. The actual scene roles have
// the same completed/missing quest distinction; no player data is changed.
func TestRealPVFRiftRejectsLockedCharacterBeforePreparationOrDebit(t *testing.T) {
	f := riftUnlockFixture(t)
	ctx := context.Background()
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountID: "test-account"}
	before, _, _ := f.repos.Character.Load(ctx, "19")
	bagBefore, _, _ := f.repos.Inventory.Load(ctx, "19")
	runtime, _, err := f.service.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 316, Difficulty: 3})
	if err == nil || runtime != nil {
		t.Fatalf("locked character prepared: runtime=%v err=%v", runtime != nil, err)
	}
	if reason, class := currentSelectDungeonEntryFailureReason(err); reason != currentSelectDungeonMissingPermissionReason || class != "entry_map_quest_required" {
		t.Fatalf("wrong refusal %x %s: %v", reason, class, err)
	}
	after, _, _ := f.repos.Character.Load(ctx, "19")
	bagAfter, _, _ := f.repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bagBefore, bagAfter) {
		t.Fatal("locked admission changed character or tickets")
	}
	if sessionHasActiveDungeonRuntime(session) {
		t.Fatal("locked admission committed runtime")
	}
}

func TestRealPVFRiftChecksEachCharactersOwnQuestRecord(t *testing.T) {
	f := riftUnlockFixture(t)
	ctx := context.Background()
	if err := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{2888: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	leader := &gameSession{selectedCharacterID: 6}
	follower := &gameSession{selectedCharacterID: 72}
	if err := f.service.validateCurrentRiftUnlock(ctx, leader, f.dungeon); err != nil {
		t.Fatal("unlocked leader refused", err)
	}
	if err := f.service.validateCurrentRiftUnlock(ctx, follower, f.dungeon); err == nil {
		t.Fatal("follower borrowed leader unlock")
	}
	if runtime, _, err := f.service.prepareDungeonRuntimePlanned(ctx, follower, dungeoncmd.SelectDungeonRequest{DungeonID: 316, Difficulty: 3}, &runtimePartyDungeonEntryPlan{}); runtime != nil || err == nil {
		t.Fatal("leader-planned prepare bypassed follower quest")
	} else if reason, _ := currentSelectDungeonEntryFailureReason(err); reason != currentSelectDungeonMissingPermissionReason {
		t.Fatalf("planned prepare failed at wrong gate: %v", err)
	}
	for _, status := range []string{"active", "accepted", "inprogress", "available", ""} {
		if err := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "72", States: map[int64]dnfrepo.QuestState{2888: {Status: status}}}); err != nil {
			t.Fatal(err)
		}
		if err := f.service.validateCurrentRiftUnlock(ctx, follower, f.dungeon); err == nil {
			t.Fatalf("status %q authorized locked follower", status)
		}
	}
	if err := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "72", Progress: map[int64]dnfrepo.QuestState{2888: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.validateCurrentRiftUnlock(ctx, follower, f.dungeon); err != nil {
		t.Fatal("persisted completed progress refused", err)
	}
	// A new session reloads the saved completion, rather than a leader/session latch.
	if err := f.service.validateCurrentRiftUnlock(ctx, &gameSession{selectedCharacterID: 72}, f.dungeon); err != nil {
		t.Fatal(err)
	}
	f.service.options.currentClientVersion = "legacy"
	if err := f.service.validateCurrentRiftUnlock(ctx, &gameSession{selectedCharacterID: 73}, f.dungeon); err != nil {
		t.Fatal("legacy profile changed", err)
	}
	f.service.options.currentClientVersion = "official-24.9"
	if err := f.service.validateCurrentRiftUnlock(ctx, &gameSession{selectedCharacterID: 73}, worldmap.Dungeon{ID: 310}); err != nil {
		t.Fatal("unrelated dungeon changed", err)
	}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return dnfrepo.Group{}, false }
	if err := f.service.validateCurrentRiftUnlock(ctx, follower, f.dungeon); err == nil {
		t.Fatal("unavailable quest repository authorized entry")
	}
}

func TestRealPVFRiftPartyRefusesBeforeAnyMemberRuntimeOrDebit(t *testing.T) {
	f := riftUnlockFixture(t)
	ctx := context.Background()
	if e := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{2888: {Status: "completed"}}}); e != nil {
		t.Fatal(e)
	}
	leader := &gameSession{conn: &bufferConn{}, selectedCharacterID: 6, accountID: "test-account"}
	state := alignedcmd.PartyState{PartyID: 3, UserID: 6, Members: []alignedcmd.PartyMemberState{{UserID: 6}, {UserID: 72}}}
	storeRuntimePartyState(leader, state)
	bags := map[string]dnfrepo.InventoryRecord{}
	chars := map[string]dnfrepo.CharacterRecord{}
	for _, id := range []string{"6", "72"} {
		bags[id], _, _ = f.repos.Inventory.Load(ctx, id)
		chars[id], _, _ = f.repos.Character.Load(ctx, id)
	}
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, 316)
	body[4] = 3
	if e := f.service.handleDungeonSelectUpper(leader, body); e != nil {
		t.Fatal(e)
	}
	packet, rest := splitGameServerUpperPacket(t, leader.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 16 || !bytes.Equal(packet.Body, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}) {
		t.Fatalf("not one explicit permission refusal: msg=%d body=%x rest=%d", packet.Header.MsgID, packet.Body, len(rest))
	}
	assertRiftUnlockSystemText(t, rest, "队伍中有角色尚未完成魔界裂缝的入场前置任务，无法进入。")
	for _, id := range []string{"6", "72"} {
		bag, _, _ := f.repos.Inventory.Load(ctx, id)
		ch, _, _ := f.repos.Character.Load(ctx, id)
		if !reflect.DeepEqual(bags[id], bag) || !reflect.DeepEqual(chars[id], ch) {
			t.Fatalf("refused party changed %s assets", id)
		}
	}
	if sessionHasActiveDungeonRuntime(leader) {
		t.Fatal("refused party committed leader")
	}
	if e := f.repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "72", States: map[int64]dnfrepo.QuestState{2888: {Status: "completed"}}}); e != nil {
		t.Fatal(e)
	}
	if e := f.service.validateCurrentRiftPartyUnlock(ctx, leader, state, 316); e != nil {
		t.Fatal("qualified party rejected", e)
	}
	for _, session := range []*gameSession{leader, {conn: &bufferConn{}, selectedCharacterID: 72, accountID: "test-account"}} {
		if e := f.service.consumeCurrentRiftEntry(session, &runtimeDungeonState{Dungeon: f.dungeon}); e != nil {
			t.Fatal("qualified entry debit", e)
		}
	}
	for _, id := range []string{"6", "72"} {
		bag, _, _ := f.repos.Inventory.Load(ctx, id)
		if bag.Slots["0:20"].Count != 18 {
			t.Fatalf("qualified %s ticket count %d", id, bag.Slots["0:20"].Count)
		}
	}
}

type riftUnlockTestFixture struct {
	service *Service
	repos   dnfrepo.Group
	dungeon worldmap.Dungeon
}

func riftUnlockFixture(t *testing.T) riftUnlockTestFixture {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, e := platformpvf.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	table, e := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	d, ok := table.FindDungeon(316)
	if !ok {
		t.Fatal("real rift missing")
	}
	ar, ok := table.FindAreaByDungeon(316)
	if !ok || ar.ID != 39 {
		t.Fatal("real area mismatch")
	}
	found := false
	for _, entry := range ar.Dungeons {
		if entry.DungeonID == 316 && entry.QuestID == 2888 && !entry.InProgressOnly {
			found = true
		}
	}
	if !found {
		t.Fatal("real worldmap unlock pair changed")
	}
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, id := range []string{"19", "6", "72"} {
		if e := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "test-account", Level: 90, Stats: map[string]int64{"fatigue": 100}}); e != nil {
			t.Fatal(e)
		}
		if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 10158964, Count: 20}}}); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", pvfPath: p, gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, initialEquipmentArchive: a, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	return riftUnlockTestFixture{s, repos, d}
}
