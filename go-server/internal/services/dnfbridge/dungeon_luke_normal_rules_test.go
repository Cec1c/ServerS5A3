package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRealPVFLukeThroneNeedsEveryPersistedPrerequisiteForEachCharacter(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	source, err := f.service.initialEquipmentArchiveLocked()
	if err != nil {
		t.Fatal(err)
	}
	prerequisites, err := loadCurrentLukeThronePrerequisites(source)
	if err != nil || !reflect.DeepEqual(prerequisites, []uint32{5101, 5102, 5103, 5104, 5105}) {
		t.Fatal("actual throne sequence mismatch", prerequisites, err)
	}
	text, err := source.ReadText("dungeon/luke_normal/solium.dgn")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("dungeon/luke_normal/solium.dgn", text)
	if err != nil {
		t.Fatal(err)
	}
	d := worldmap.ParseDungeon(5106, doc.Path, doc)
	beforeInventory, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	beforeCharacter, _, _ := f.repos.Character.Load(context.Background(), "19")
	for n, id := range prerequisites {
		if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), session, d); err == nil {
			t.Fatalf("only %d prerequisites admitted", n)
		}
		if _, _, err := f.repos.DungeonPermission.UpsertMax(context.Background(), "19", id, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), session, d); err != nil {
		t.Fatal(err)
	}
	// Check the follower with an independent session. Copying the leader would
	// duplicate sync.Once/sync.Mutex state and alias the leader's bindings.
	peer := &gameSession{selectedCharacterID: 20, accountID: "account-20"}
	if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), peer, d); err == nil {
		t.Fatal("leader prerequisites authorized unqualified follower")
	}
	inventory, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	character, _, _ := f.repos.Character.Load(context.Background(), "19")
	if !reflect.DeepEqual(beforeInventory, inventory) || !reflect.DeepEqual(beforeCharacter, character) {
		t.Fatal("read-only prerequisite check changed assets")
	}
	for _, id := range []int64{5101, 5061, 5062} {
		if err := (&Service{}).validateCurrentLukeNormalThroneEntry(context.Background(), nil, worldmap.Dungeon{ID: id}); err != nil {
			t.Fatal("non-throne entry affected", err)
		}
	}
}

func TestLukeNormalDualBossDeathsKeepSecondDropOwnerAndDeathAck(t *testing.T) {
	for _, first := range []int{0, 1} {
		s, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true})
		r.Dungeon.ID = 5104
		r.Room.mu.Lock()
		r.Room.monsters[1].Spawn.Rank = "[boss]"
		r.Room.mu.Unlock()
		if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
			t.Fatal(err)
		}
		room := r.Room.Snapshot()
		key, other := room.Monsters[first].ObjectKey, room.Monsters[1-first].ObjectKey
		conn := &bufferConn{}
		session := &gameSession{conn: conn, connID: "luke-dual-boss", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
		if err := s.handleDungeonMonsterDeath(session, storyAIBossDeathBody(key)); err != nil {
			t.Fatal(err)
		}
		if got := storyAIBossDeathNotificationKeys(t, conn.write.Bytes()); !reflect.DeepEqual(got, []uint32{key}) {
			t.Fatal("first op39 lost ack or fabricated second death", got)
		}
		scene := r.Session.Snapshot().Scene
		if scene.Cleared || r.settlementEntrySent || !currentLukeNormalBossesPending(r, scene) {
			t.Fatal("first boss ended room")
		}
		conn.write.Reset()
		if err := s.handleDungeonBossDieCheck(session, bossDieCheckRequestBody(99, uint16(key))); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 || r.bossDieCheckAccepted {
			t.Fatal("op117 cleared surviving boss")
		}
		if err := s.handleDungeonMonsterDeath(session, storyAIBossDeathBody(other)); err != nil {
			t.Fatal(err)
		}
		if got := storyAIBossDeathNotificationKeys(t, conn.write.Bytes()); !reflect.DeepEqual(got, []uint32{other}) {
			t.Fatal("second op39 lost death ack", got)
		}
		snapshot := r.Session.Snapshot()
		if snapshot.Run.Status != worldmap.DungeonRunCompleted || !snapshot.Scene.Cleared || !r.settlementEntrySent {
			t.Fatal("both deaths did not settle")
		}
		if currentLukeNormalBossesPending(r, snapshot.Scene) {
			t.Fatal("completed boss gate retained")
		}
	}
}

func TestRealPVFLukeDailyLightAltarEssenceRequiresTwoSeparateMonsterGrants(t *testing.T) {
	f, _, _ := normalLukeFixture(t)
	ctx := context.Background()
	catalog, err := f.service.loadQuestCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	itemCatalog, _ := actualAntonRewardFixture(t)
	definition, err := itemCatalog.ResolveItem(10159404)
	if err != nil {
		t.Fatal(err)
	}
	inventory := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}
	for _, monster := range []int64{56699, 56698} {
		candidates := currentQuestDropMatchMonster(catalog, []int64{2902}, 5104, 2, monster)
		if len(candidates) != 1 || candidates[0] != (currentQuestDropCandidate{QuestID: 2902, ItemID: 10159404, Count: 1, DropRate: 100, MaxStack: 2}) {
			t.Fatal("actual essence producer differs", candidates)
		}
		c := candidates[0]
		count := currentQuestDropRoll(99, c.Count, c.DropRate, c.MaxStack, currentQuestDropHeldCount(inventory, 10159404))
		if count != 1 {
			t.Fatal("legitimate second essence capped", count)
		}
		if _, err := currentQuestDropGrantStack(&inventory, definition, uint32(count)); err != nil {
			t.Fatal(err)
		}
	}
	if currentQuestDropHeldCount(inventory, 10159404) != 2 {
		t.Fatal("two producers did not grant two essences")
	}
	if count := currentQuestDropRoll(0, 1, 100, 2, 2); count != 0 {
		t.Fatal("quest cap exceeded")
	}
	if got := currentQuestDropMatchMonster(catalog, []int64{2902}, 5059, 2, 56699); len(got) != 0 {
		t.Fatal("ordinary daily quest granted in raid")
	}
}
