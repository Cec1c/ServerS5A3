package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFTimeRiftBossDeathOrder(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("real PVF not configured")
	}
	for _, tc := range []struct {
		name               string
		lastMap, bossFirst bool
		hit, peer          bool
		bossMap            int64
	}{
		{name: "first_map"},
		{name: "last_map_minions_first", lastMap: true},
		{name: "last_map_boss_first", lastMap: true, bossFirst: true},
		{name: "last_map_boss_first_peer", lastMap: true, bossFirst: true, peer: true},
		{name: "first_map_difficulty2_hit", hit: true},
		{name: "first_map_difficulty2_hit_peer", hit: true, peer: true},
		{name: "normal_map_boss_first", bossFirst: true, bossMap: 80304},
		{name: "normal_map_boss_first_peer", bossFirst: true, peer: true, bossMap: 80304},
		{name: "special_single_boss", bossMap: 80290},
	} {
		t.Run(tc.name, func(t *testing.T) {
			difficulty := byte(1)
			if tc.hit {
				difficulty = 2
			}
			s, leader, member, runtime, _ := timeRiftBossOrderFixture(t, int64(7539), difficulty, func(s *Service, _, _ *gameSession) {
				if tc.lastMap {
					s.dungeonChoice = func(n int) (int, error) { return n - 1, nil }
				}
				if tc.hit {
					s.dungeonSeed = func() (uint32, error) { return 2, nil }
				}
				if tc.bossMap != 0 {
					s.dungeonChoice = func(n int) (int, error) {
						if tc.bossMap == 80304 && n == 2 {
							return 1, nil
						}
						if tc.bossMap == 80290 && n == 7 {
							return 5, nil // The sixth declared default maze owns this boss.
						}
						return 0, nil
					}
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sessions := []*gameSession{leader, member}
			for _, g := range sessions {
				if err := s.startGameSessionEvents(ctx, g); err != nil {
					t.Fatal(err)
				}
				defer s.stopGameSessionEvents(context.Background(), g)
			}
			start := runtime.Session.Snapshot().Run.Current
			rooms := map[worldmap.RoomCoordinate]bool{}
			for _, r := range runtime.Session.Rooms() {
				rooms[r.Coordinate] = true
			}
			queue := [][]worldmap.RoomCoordinate{{start}}
			seen := map[worldmap.RoomCoordinate]bool{start: true}
			var path []worldmap.RoomCoordinate
			for len(queue) > 0 {
				p := queue[0]
				queue = queue[1:]
				last := p[len(p)-1]
				if last == runtime.BossCoordinate {
					path = p
					break
				}
				for _, d := range []worldmap.RoomCoordinate{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
					next := worldmap.RoomCoordinate{X: last.X + d.X, Y: last.Y + d.Y}
					if rooms[next] && !seen[next] {
						seen[next] = true
						queue = append(queue, append(append([]worldmap.RoomCoordinate{}, p...), next))
					}
				}
			}
			if len(path) == 0 {
				t.Fatal("boss path missing")
			}
			for step, coordinate := range path {
				if step > 0 {
					for _, g := range sessions {
						if err := s.callGameSession(ctx, g, "helper-move", func() error {
							body := make([]byte, dungeoncmd.MoveMapRequestSize)
							body[0] = byte(coordinate.X)
							body[1] = byte(coordinate.Y)
							if err := s.handleDungeonMoveMapPlanned(g, body, nil, false); err != nil {
								return err
							}
							if g.dungeon.runtime.Session.Snapshot().Run.Current != coordinate {
								return fmt.Errorf("move blocked")
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}
				}
				scene, _ := runtime.Session.Scene()
				monsters := runtime.Room.Snapshot().Monsters
				if scene.Boss {
					if tc.bossMap != 0 && scene.Map.Map.ID != tc.bossMap {
						t.Fatalf("boss map=%d want%d", scene.Map.Map.ID, tc.bossMap)
					}
					sort.SliceStable(monsters, func(i, j int) bool {
						bi, _, _ := currentDungeonRuntimeObjectBossActor(runtime, monsters[i].ObjectKey, runtimeDungeonMonsterAnnounced)
						bj, _, _ := currentDungeonRuntimeObjectBossActor(runtime, monsters[j].ObjectKey, runtimeDungeonMonsterAnnounced)
						if tc.bossFirst {
							return bi && !bj
						}
						return !bi && bj
					})
					t.Logf("boss map=%d monsters=%d gate=%t", scene.Map.Map.ID, len(monsters), scene.Boss)
				}
				for _, m := range monsters {
					if m.State != runtimeDungeonMonsterAnnounced {
						continue
					}
					reporter := leader
					if tc.peer {
						reporter = member
					}
					if err := s.callGameSession(ctx, reporter, "rift-boss-order-death", func() error { return s.handleDungeonMonsterDeath(reporter, doc94Official249DeathBody(m.ObjectKey)) }); err != nil {
						t.Fatal(err)
					}
					if scene.Boss {
						for _, retry := range sessions {
							if err := s.callGameSession(ctx, retry, "rift-boss-order-replay", func() error { return s.handleDungeonMonsterDeath(retry, doc94Official249DeathBody(m.ObjectKey)) }); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				for _, g := range sessions {
					if err := s.callGameSession(ctx, g, "rift-room-death-barrier", func() error { return nil }); err != nil {
						t.Fatal(err)
					}
				}
			}
			pieces := map[uint32]bool{}
			for _, id := range runtime.riftLoot.Pieces {
				pieces[id] = true
			}
			fragments := map[uint16]int{}
			epics := map[uint16]int{}
			// A peer can be the first drop producer. Inspect the authoritative
			// instance ledger; other sessions adopt that pointer lazily on pickup.
			owner := runtime.partyInstance.dropOwner
			if owner != nil {
				for _, drop := range owner.byObjectKey {
					if pieces[drop.Item.ItemID] {
						fragments[drop.OwnerActorObjectKey] += int(drop.Amount)
					}
					if drop.Item.Kind == dungeonDropItemEquipment && drop.Item.Rarity == 4 {
						epics[drop.OwnerActorObjectKey] += int(drop.Amount)
					}
				}
			}
			t.Logf("final cleared=%t fragments=%v epics=%v", runtime.Session.Snapshot().Scene.Cleared, fragments, epics)
			for _, g := range sessions {
				if fragments[currentSceneActorObjectKey(g.selectedCharacterID)] != runtime.riftLoot.FragmentCount {
					t.Errorf("member %d missing guaranteed fragments", g.selectedCharacterID)
				}
				if !tc.hit {
					continue
				}
				if err := s.callGameSession(ctx, g, "helper-packet-barrier", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				var wireEpics []uint32
				seenEpics := map[uint32]bool{}
				for _, body := range abyssOp38Bodies(t, g.conn.(*bufferConn).write.Bytes()) {
					for _, row := range abyssEpicFragmentDropRows(t, body) {
						if owner == nil {
							t.Fatal("emitted drops without instance ledger")
						}
						drop := owner.byObjectKey[row.ObjectKey]
						if drop != nil && drop.Item.Kind == dungeonDropItemEquipment && drop.Item.Rarity == 4 {
							if drop.OwnerActorObjectKey != currentSceneActorObjectKey(g.selectedCharacterID) {
								t.Fatal("full handler sent foreign epic")
							}
							if !seenEpics[row.ObjectKey] {
								wireEpics = append(wireEpics, row.ItemID)
								seenEpics[row.ObjectKey] = true
							}
						}
					}
				}
				t.Logf("actual handler difficulty=%d reporterPeer=%t member=%d emitted op38 epic IDs=%v", difficulty, tc.peer, g.selectedCharacterID, wireEpics)
				if len(wireEpics) != 1 {
					t.Errorf("member %d expected one emitted epic, got %v", g.selectedCharacterID, wireEpics)
				}
			}
		})
	}
}
func timeRiftBossOrderFixture(t *testing.T, fixtureOptions ...any) (*Service, *gameSession, *gameSession, *runtimeDungeonState, *runtimeDungeonState) {
	t.Helper()
	var realID int64
	difficulty := byte(1)
	var counts []int
	var beforeEntry []func(*Service, *gameSession, *gameSession)
	for _, option := range fixtureOptions {
		switch value := option.(type) {
		case int64:
			realID = value
		case int:
			counts = append(counts, value)
		case byte:
			difficulty = value
		case func(*Service, *gameSession, *gameSession):
			beforeEntry = append(beforeEntry, value)
		default:
			t.Fatalf("unsupported party fixture option %T", option)
		}
	}
	source := bridgeDungeonPVF(false)
	for path, text := range source {
		source[path] = strings.ReplaceAll(text, "700", "316")
	}
	if len(counts) > 0 {
		source["map/dungeon/test/start.map"] = "[map name]\n`start`\n[dungeon]\n316\n[type]\n`[start]`\n[monster]\n" + strings.Repeat("3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n", counts[0])
	}
	// 合成副本默认 `[limit party count] 1`；两人同场必须是 2 人以上。
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"],
		"[limit party count]\n1",
		"[limit party count]\n4",
		1,
	)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	var archive *platformpvf.Archive
	if realID > 0 {
		var err error
		archive, err = platformpvf.Open(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
		if err != nil {
			t.Fatal(err)
		}
		table, err = worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
		if err != nil {
			t.Fatal(err)
		}
		resolver, err = worldmap.NewResolver(table)
		if err != nil {
			t.Fatal(err)
		}
		monsters, err = newPVFDungeonMonsterCatalog(archive)
		if err != nil {
			t.Fatal(err)
		}
	}
	repositories := dnfrepomemory.NewMemoryGroup()

	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	for slot, characterID := range []string{"99", "100"} {
		account := fmt.Sprintf("account-%d", slot+1)
		if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: account}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
			Name:        "fixture-" + characterID, Slot: slot,
			AccountID: account,
			Level:     90,
			Stats:     map[string]int64{"fatigue": 100},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: characterID,
			Slots:       map[string]dnfrepo.ItemStack{"0:20": {ItemID: 10158964, Count: 100}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	if realID > 0 {
		service.initialEquipmentArchive = archive
		service.options.currentClientVersion = "official-24.9"
		service.options.pvfPath = os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
		var err error
		service.dungeonAICharacterTable, err = newPVFDungeonAICharacterCatalog(archive)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"99", "100"} {
			if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: id, States: map[int64]dnfrepo.QuestState{2888: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	channel := channelcatalog.Channel{ServerID: 1, ID: 121, Type: 11, Name: "ch.121", Port: 10121}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 99, accountID: "account-1"}
	follower := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 100, accountID: "account-2"}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	for _, prepareParty := range beforeEntry {
		prepareParty(service, leader, follower)
	}

	runtimes := make([]*runtimeDungeonState, 0, 2)
	for _, session := range []*gameSession{leader, follower} {
		runtime, _, err := service.prepareDungeonRuntime(
			context.Background(),
			session,
			dungeoncmd.SelectDungeonRequest{DungeonID: func() uint32 {
				if realID > 0 {
					return uint32(realID)
				}
				return 316
			}(), Difficulty: func() byte {
				if realID == 316 {
					return 3
				}
				return difficulty
			}()},
		)
		if err != nil {
			t.Fatalf("prepare party member %d dungeon runtime: %v", session.selectedCharacterID, err)
		}
		scene, ok := runtime.Session.Scene()
		if !ok {
			t.Fatalf("party member %d runtime has no scene", session.selectedCharacterID)
		}
		if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
			t.Fatalf("announce party member %d actors: %v", session.selectedCharacterID, err)
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime = runtime
		session.dungeon.mu.Unlock()
		if instance := service.adoptPartyDungeonInstance(session, runtime, scene); instance == nil {
			t.Fatalf("party member %d did not join the shared dungeon instance", session.selectedCharacterID)
		}
		runtimes = append(runtimes, runtime)
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count=%d want 1 (both members must share one run)", got)
	}
	for _, session := range []*gameSession{leader, follower} {
		scene, _ := session.dungeon.runtime.Session.Scene()
		service.recordDungeonPartyRunKey(session, session.dungeon.runtime, scene)
	}
	return service, leader, follower, runtimes[0], runtimes[1]
}
