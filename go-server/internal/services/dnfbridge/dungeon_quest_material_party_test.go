package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/questmaterial"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func partyMaterialFixture(t *testing.T, n, leader int, boss, enemyTable bool, rate int) (*Service, []*gameSession, r.Group, uint32) {
	t.Helper()
	ctx := context.Background()
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[limit party count]\n1", "[limit party count]\n4", 1)
	if boss {
		source["map/dungeon/test/start.map"] = strings.Replace(source["map/dungeon/test/start.map"], "`[normal]`", "`[boss]`", 1)
	}
	source[quest.DefaultList] = "84001 `party_material.qst`\n"
	block := fmt.Sprintf("[monster reward item]\n3001 700 -1 600001 1 %d 100\n[/monster reward item]\n", rate)
	if enemyTable {
		block = fmt.Sprintf("[enemy reward item]\n3001 1 700 -1 600001 1 %d 100\n3001 2 700 -1 600002 1 100 100\n[/enemy reward item]\n", rate)
	}
	source["n_quest/party_material.qst"] = "[grade]\n`[normal]`\n[type]\n`[seeking]`\n[int data]\n600001 100\n" + block
	index, err := pvf.Build(ctx, source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repos := memory.NewMemoryGroup()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repos.Account.Save(ctx, r.AccountRecord{AccountID: "party-material-test"}))
	s := &Service{options: options{currentClientVersion: "official-24.9", accountID: "party-material-test", gameUpperBodyCodec: gameUpperBodyCodecPlain}, onlinePlayers: newOnlinePlayerManager(), worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, questCatalog: catalog, pvfItemCatalog: &pvfDungeonDropCatalog{source: source, itemCache: map[uint32]dungeonDropItemDefinition{600001: {ItemID: 600001, Name: "synthetic quest material", Kind: dungeonDropItemStackable, PVFPath: "stackable/quest/test.stk", StackableType: "[quest]", StackLimit: 999, SlotStart: 177, SlotEnd: 232}}}, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 0x12345678, nil }, repositoryProvider: func() (r.Group, bool) { return repos, true }}
	ch := channelcatalog.Channel{ServerID: 1, ID: 121, Type: 11, Name: "synthetic", Port: 10121}
	party := alignedcmd.PartyState{PartyID: 303, UserID: uint16(99 + leader), MaxMembers: 4}
	sessions := []*gameSession{}
	for i := 0; i < n; i++ {
		id := uint16(99 + i)
		text := strconv.Itoa(int(id))
		must(repos.Character.Save(ctx, r.CharacterRecord{CharacterID: text, AccountID: "party-material-test", Level: 90, Stats: map[string]int64{"fatigue": 100}}))
		must(repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: text, Slots: map[string]r.ItemStack{}}))
		must(repos.Quest.Save(ctx, r.QuestRecord{CharacterID: text, States: map[int64]r.QuestState{84001: {Status: "active"}}}))
		p := &gameSession{conn: &bufferConn{}, channel: ch, residentChannel: ch, selectedCharacterID: id}
		sessions = append(sessions, p)
		s.bindGameSessionCharacter(p, id)
		party.Members = append(party.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
	}
	if n > 1 {
		for _, p := range sessions {
			storeRuntimePartyState(p, party)
		}
	}
	for _, p := range sessions {
		rt, _, err := s.prepareDungeonRuntime(ctx, p, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
		must(err)
		p.dungeon.runtime = rt
		_, err = rt.Room.AnnounceAllActors(rt.Session)
		must(err)
		scene, _ := rt.Session.Scene()
		if n > 1 {
			if s.adoptPartyDungeonInstance(p, rt, scene) == nil {
				t.Fatal("shared instance missing")
			}
			s.recordDungeonPartyRunKey(p, rt, scene)
		}
	}
	key := sessions[0].dungeon.runtime.Room.Snapshot().Monsters[0].ObjectKey
	if boss {
		yes, _, _ := currentDungeonRuntimeObjectBossActor(sessions[0].dungeon.runtime, key, runtimeDungeonMonsterAnnounced)
		if !yes {
			t.Fatal("boss fixture not classified")
		}
	}
	return s, sessions, repos, key
}
func partyMaterialDeath(s *Service, p *gameSession, key uint32) error {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
	body[30] = 1
	binary.LittleEndian.PutUint32(body, key)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(p.selectedCharacterID))
	return s.handleDungeonMonsterDeath(p, body)
}
func TestPartyQuestMaterialAuthoritativeDeathMatrix(t *testing.T) {
	for n := 1; n <= 4; n++ {
		for _, leader := range []int{0, n - 1} {
			for _, first := range []int{0, n - 1} {
				for _, boss := range []bool{false, true} {
					for _, enemy := range []bool{false, true} {
						t.Run(fmt.Sprintf("n%d/leader%d/first%d/boss%t/enemy%t", n, leader, first, boss, enemy), func(t *testing.T) {
							s, ps, g, key := partyMaterialFixture(t, n, leader, boss, enemy, 100)
							if e := partyMaterialDeath(s, ps[first], key); e != nil {
								t.Fatal(e)
							}
							for _, p := range ps {
								if e := partyMaterialDeath(s, p, key); e != nil {
									t.Fatal(e)
								}
							}
							for _, p := range ps {
								inv, _, e := g.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
								if e != nil {
									t.Fatal(e)
								}
								if got := currentQuestDropHeldCount(inv, 600001); got != 1 {
									t.Fatalf("role %d held %d want1", p.selectedCharacterID, got)
								}
								if got := currentQuestDropHeldCount(inv, 600002); got != 0 {
									t.Fatal("type2 was expanded")
								}
							}
						})
					}
				}
			}
		}
	}
}
func TestPartyQuestMaterialEligibilityAndDisconnect(t *testing.T) {
	for _, mode := range []string{"reporter-ineligible", "member-completed", "member-cap", "disconnected", "unannounced", "full"} {
		t.Run(mode, func(t *testing.T) {
			s, ps, g, key := partyMaterialFixture(t, 3, 2, false, false, 100)
			ctx := context.Background()
			who := ps[1]
			expected := []int64{1, 1, 1}
			if mode == "reporter-ineligible" {
				who = ps[0]
				expected[0] = 0
			}
			if mode == "reporter-ineligible" || mode == "member-completed" {
				q, _, _ := g.Quest.Load(ctx, strconv.Itoa(int(who.selectedCharacterID)))
				x := q.States[84001]
				x.Status = "completed"
				q.States[84001] = x
				if e := g.Quest.Save(ctx, q); e != nil {
					t.Fatal(e)
				}
				expected[int(who.selectedCharacterID)-99] = 0
			}
			if mode == "member-cap" {
				id := strconv.Itoa(int(who.selectedCharacterID))
				inv, _, _ := g.Inventory.Load(ctx, id)
				inv.Slots = map[string]r.ItemStack{"0:177": {ItemID: 600001, Count: 100}}
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
				expected[1] = 100
			}
			if mode == "unannounced" {
				instance := ps[0].dungeon.runtime.partyInstance
				instance.mu.Lock()
				instance.scenes[ps[1].selectedCharacterID] = nil
				instance.mu.Unlock()
				expected[1] = 0
			}
			if mode == "full" {
				id := strconv.Itoa(int(who.selectedCharacterID))
				inv, _, _ := g.Inventory.Load(ctx, id)
				inv.Slots = map[string]r.ItemStack{}
				for slot := 177; slot <= 232; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 700000 + int64(slot), Count: 999}
				}
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "disconnected" {
				if e := s.startGameSessionEvents(context.Background(), who); e != nil {
					t.Fatal(e)
				}
				s.beginGameSessionClose(who)
				defer s.stopGameSessionEvents(context.Background(), who)
			}
			if e := partyMaterialDeath(s, ps[0], key); e != nil {
				t.Fatal(e)
			}
			if mode == "full" {
				id := strconv.Itoa(int(who.selectedCharacterID))
				inv, _, _ := g.Inventory.Load(ctx, id)
				delete(inv.Slots, "0:177")
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
			}
			// Recover with a new session identity, not the disconnected socket.
			if mode == "disconnected" || mode == "full" {
				reconnected := &gameSession{conn: &bufferConn{}, selectedCharacterID: who.selectedCharacterID}
				if e := s.recoverQuestMaterials(reconnected); e != nil {
					t.Fatal(e)
				}
			}
			for i, p := range ps {
				inv, _, _ := g.Inventory.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
				if got := currentQuestDropHeldCount(inv, 600001); got != expected[i] {
					t.Fatalf("role %d=%d want %d", i, got, expected[i])
				}
			}
		})
	}
}

func TestPartyQuestMaterialIndependentProbability(t *testing.T) {
	for _, sample := range []struct{ rate, trials int }{{50, 32}, {5, 128}, {1, 512}} {
		t.Run(fmt.Sprint(sample.rate), func(t *testing.T) {
			var hits, misses [4]int
			mixed := false
			for trial := 0; trial < sample.trials; trial++ {
				s, ps, g, key := partyMaterialFixture(t, 4, trial%4, false, false, sample.rate)
				ps[0].dungeon.runtime.Room.materialID = fmt.Sprintf("%032x", trial+1)
				if err := partyMaterialDeath(s, ps[trial%4], key); err != nil {
					t.Fatal(err)
				}
				counts := [4]int64{}
				for i, p := range ps {
					inv, _, _ := g.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
					counts[i] = currentQuestDropHeldCount(inv, 600001)
					if counts[i] == 1 {
						hits[i]++
					} else if counts[i] == 0 {
						misses[i]++
					} else {
						t.Fatal("invalid probability quantity")
					}
				}
				for _, p := range ps {
					if err := partyMaterialDeath(s, p, key); err != nil {
						t.Fatal(err)
					}
				}
				for i, p := range ps {
					inv, _, _ := g.Inventory.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
					if currentQuestDropHeldCount(inv, 600001) != counts[i] {
						t.Fatal("miss rerolled or duplicate granted")
					}
				}
				for i := 1; i < 4; i++ {
					mixed = mixed || counts[i] != counts[0]
				}
			}
			for i := 0; i < 4; i++ {
				if hits[i] == 0 || misses[i] == 0 {
					t.Fatalf("role %d did not exercise both outcomes: %v/%v", i, hits, misses)
				}
			}
			if !mixed {
				t.Fatal("party copied one shared probability result")
			}
			t.Logf("rate=%d trials=%d personalHits=%v misses=%v", sample.rate, sample.trials, hits, misses)
		})
	}
}

func TestPartyQuestMaterialConcurrentSessionQueues(t *testing.T) {
	s, ps, g, key := partyMaterialFixture(t, 4, 3, false, false, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range ps {
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		defer s.stopGameSessionEvents(context.Background(), p)
	}
	var wg sync.WaitGroup
	for _, p := range ps {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.callGameSession(ctx, p, "synthetic-death", func() error { return partyMaterialDeath(s, p, key) }); err != nil {
				t.Error(err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("cross-session deadlock")
	}
	for _, p := range ps {
		if err := s.callGameSession(ctx, p, "synthetic-recovery-barrier", func() error { return s.recoverQuestMaterials(p) }); err != nil {
			t.Fatal(err)
		}
		inv, _, err := g.Inventory.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil {
			t.Fatal(err)
		}
		if currentQuestDropHeldCount(inv, 600001) != 1 {
			t.Fatal("concurrent delivery missing/duplicate")
		}
	}
}

func TestPartyQuestMaterialPersonalAccounts(t *testing.T) {
	s, ps, g, key := partyMaterialFixture(t, 2, 0, false, false, 100)
	ctx := context.Background()
	p := ps[1]
	p.accountID = "second-owner"
	if err := g.Account.Save(ctx, r.AccountRecord{AccountID: p.accountID}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := g.Character.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
	c.AccountID = p.accountID
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := partyMaterialDeath(s, ps[0], key); err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		inv, _, _ := g.Inventory.Load(ctx, strconv.Itoa(int(p.selectedCharacterID)))
		if currentQuestDropHeldCount(inv, 600001) != 1 {
			t.Fatal("used reporter account for member")
		}
	}
}

func TestPartyQuestMaterialStackSplitIsComplete(t *testing.T) {
	s, _, _, _ := partyMaterialFixture(t, 1, 0, false, false, 100)
	def := s.pvfItemCatalog.itemCache[600001]
	def.StackLimit = 2
	s.pvfItemCatalog.itemCache[600001] = def
	inv := r.InventoryRecord{CharacterID: "99", Slots: map[string]r.ItemStack{}}
	if _, err := s.grantQuestMaterialInventory(&inv, questmaterial.Attempt{ItemID: 600001, Count: 1}); err != nil {
		t.Fatal(err)
	}
	slots, err := s.grantQuestMaterialInventory(&inv, questmaterial.Attempt{ItemID: 600001, Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if currentQuestDropHeldCount(inv, 600001) != 4 || len(slots) != 2 {
		t.Fatalf("partial stack silently truncated: slots=%v", slots)
	}
}

func TestPartyQuestMaterialPreparationLeavesRoomReadable(t *testing.T) {
	_, ps, _, key := partyMaterialFixture(t, 1, 0, false, false, 100)
	rt := ps[0].dungeon.runtime
	done := make(chan error, 1)
	go func() {
		_, _, err := rt.Room.commitActorDeathReportPrepared(key, rt.Session, true, func() error { rt.Room.Snapshot(); return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SQL preparation held room state lock")
	}
}
