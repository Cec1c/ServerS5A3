package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func guildDungeonBridgeCatalog(t *testing.T, s *Service) {
	t.Helper()
	text := "[dungeon]\n[dungeon index]\n8500\n[gauge limit]\n10000\n[gauge on clear]\n150\n[conquest gauge ratio]\n50\n[reward]\n[gauge ratio]\n100\n[item]\n10154439 3\n[contribute]\n20\n[/gauge ratio]\n[/reward]\n[/dungeon]"
	if path := os.Getenv("GUILD_DUNGEON_EXTRACTED_ETC"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		guild.DungeonCatalogPath:  text,
		"stackable/stackable.lst": "10154439 `guild.stk`",
		"stackable/guild.stk":     "[name] `Guild Reward Fixture`\n[stackable type] `[material]` 0\n[stack limit] 10000\n[minimum level] 1",
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
}

func TestGuildDungeonGroupAdmissionRejectsOtherGuildBeforeBinding(t *testing.T) {
	for _, id := range []uint16{30, 31, 32} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			guildDungeonBridgeCatalog(t, s)
			ctx := context.Background()
			makeRuntime := func(p *gameSession) *runtimeDungeonState {
				c, _, _ := repos.Character.Load(ctx, fmt.Sprint(p.selectedCharacterID))
				return &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 8500}, Character: c}
			}
			leader, other := makeRuntime(peers[29]), makeRuntime(peers[id])
			follower := &runtimePartyPreparedDungeonEntry{session: peers[id], runtime: other}
			err := s.freezeCurrentGuildDungeonGroup(ctx, peers[29], leader, []*runtimePartyPreparedDungeonEntry{follower})
			if id == 30 {
				if err != nil || leader.guildDungeonClear == nil || other.guildDungeonClear == nil || leader.guildDungeonClear.Entry.RunID != other.guildDungeonClear.Entry.RunID || leader.guildDungeonClear.Entry.CompletionKey() == other.guildDungeonClear.Entry.CompletionKey() {
					t.Fatal("same guild", err)
				}
			} else if err == nil || leader.guildDungeonClear != nil || other.guildDungeonClear != nil {
				t.Fatal("cross-guild/absent member partially admitted", err)
			}
		})
	}
}

func TestGuildDungeonAdmissionDesignatedDifficulty(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	guildDungeonBridgeCatalog(t, s)
	c, _, _ := repos.Character.Load(context.Background(), "29")
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 8500}, Character: c}
	runtime.Dungeon.Metadata.DesignatedDifficulties = []int64{2}
	if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, runtime, nil); err == nil {
		t.Fatal("normal difficulty accepted for designated difficulty 2")
	}
	runtime.Request.Difficulty = 2
	if err := s.freezeCurrentGuildDungeonGroup(context.Background(), p, runtime, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGuildDungeonBridgeSettlementRestartAndNativeStages(t *testing.T) {
	s, runtime, p, conn, _ := prepareCompletedSettlementRuntime(t)
	ctx := context.Background()
	repos, _ := s.repositoryGroup()
	guildDungeonBridgeCatalog(t, s)
	c, _, _ := repos.Character.Load(ctx, "99")
	c.Name = "DungeonClear"
	c.Level = 1
	c.Stats["exp"] = 90
	for _, err := range []error{repos.Character.Save(ctx, c), repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "99", Points: r.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}), repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "99"})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg := guild.NewRegistry()
	if _, err := reg.Create(c, "DungeonGuild", s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	// Seed the pre-boundary state explicitly; the 34th unit below comes from
	// the production bridge -> owner -> durable transaction, not packet edits.
	reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 33}}
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	runtime.Dungeon.ID = 8500
	runtime.Character = c
	runtime.AccountID = c.AccountID
	runtime.Request.DungeonID = 8500
	runtime.Request.Difficulty = 2
	runtime.Dungeon.Metadata.IntegerValues = map[string]int64{"guild dungeon": 1}
	runtime.ordinaryFinalRoomClearAccepted = true
	runtime.tutorialFinalRoomClearAccepted = false
	runtime.clearMapCompletionKey = "guild-native-stage"
	runtime.entryCharacterLevel = 1
	if err := s.freezeCurrentGuildDungeonGroup(ctx, p, runtime, nil); err != nil {
		t.Fatal(err)
	}
	packets := map[string][]byte{}
	capture := func(name string) {
		t.Helper()
		conn.write.Reset()
		if err := s.writeCurrentGuildDungeonSnapshot(p); err != nil {
			t.Fatal(err)
		}
		packets[name] = append([]byte(nil), guildReply(t, p, 741)...)
	}
	capture("before-33")
	resources := currentDungeonSettlementTestResources(t)
	first, err := s.commitCurrentDungeonSettlement(ctx, p, runtime, resources.Progression, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GuildDungeonItems) != 1 || first.GuildDungeonItems[0].ItemID != 10154439 || first.GuildDungeonItems[0].Quantity != 3 {
		t.Fatalf("items=%+v", first.GuildDungeonItems)
	}
	capture("committed-34")
	second, err := s.commitCurrentDungeonSettlement(ctx, p, runtime, resources.Progression, 20)
	if err != nil || !second.Replayed || !reflect.DeepEqual(second.Inventory, first.Inventory) {
		t.Fatal("replay", err)
	}
	reg, err = guild.Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Dungeon.Counts[8500] != 34 || reg.Guilds[1].Members["99"].ContributionPoints != 20 || reg.Guilds[1].Mileage != 0 {
		t.Fatal("commit state", err)
	}
	// New service and registry load, same durable stores: reconnect projection.
	reconnected := &Service{repositoryProvider: s.repositoryProvider, options: s.options, gameplayTimers: s.gameplayTimers}
	conn.write.Reset()
	if err := reconnected.writeCurrentGuildDungeonSnapshot(p); err != nil {
		t.Fatal(err)
	}
	packets["reconnect-34"] = append([]byte(nil), guildReply(t, p, 741)...)
	if !reflect.DeepEqual(packets["committed-34"], packets["reconnect-34"]) {
		t.Fatal("reconnect differs")
	}
	// Clearing another character must replace native count state with empty.
	original := p.selectedCharacterID
	p.selectedCharacterID = 100
	capture("nonmember-empty")
	p.selectedCharacterID = original
	if out := os.Getenv("GUILD_DUNGEON_NATIVE_STAGE_DIR"); out != "" {
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := map[string]any{"source": "production bridge settlement and persistent registry snapshot", "seed": "8500 count33", "pvf_item_definition": "structural material fixture; catalog ETC optionally real", "packets": []string{}}
		for name, body := range packets {
			if err := os.WriteFile(filepath.Join(out, name+".bin"), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.MarshalIndent(manifest, "", "  ")
		if err := os.WriteFile(filepath.Join(out, "source.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildDungeonSnapshotExpiredCycleIsReadOnly(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := reg.DungeonPolicy.Cycle(s.gameplayNow().AddDate(0, -1, 0))
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Dungeon = &guild.DungeonProgress{CycleKey: previous.Key, StartUnix: previous.StartUnix, EndUnix: previous.EndUnix, Counts: map[uint32]uint32{8500: 67}}
	if err = guild.Save(ctx, repos.Settings, reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if err = s.writeCurrentGuildDungeonSnapshot(p); err != nil {
		t.Fatal(err)
	}
	body := guildReply(t, p, 741)
	if len(body) != 17 || binary.LittleEndian.Uint32(body[13:]) != 0 {
		t.Fatalf("stale counts sent: %x", body)
	}
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only reset projection wrote registry")
	}
}

func TestGuildDungeonRefreshUsesPeerEventLoopAndGuildBoundary(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}
	s.queueCurrentGuildDungeonRefresh(1, 29)
	for _, id := range []uint16{30, 31, 32, 33} {
		p := peers[id]
		if err := s.callGameSession(ctx, p, "drain-guild-refresh", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if id == 30 || id == 33 {
			if body := guildReply(t, p, 741); len(body) != 17 {
				t.Fatalf("peer %d snapshot=%x", id, body)
			}
		} else if p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("cross-guild snapshot sent to %d", id)
		}
	}
}

func TestGuildDungeonExtractedSixDGNAdmissionMetadata(t *testing.T) {
	path := os.Getenv("GUILD_DUNGEON_EXTRACTED_ETC")
	if path == "" {
		t.Skip("PVF-CLI extracted catalog not configured")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(path), "../../.."))
	raw, err := os.ReadFile(filepath.Join(root, "dungeon/dungeon.lst"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pvf.Parse("dungeon/dungeon.lst", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{8500: true, 8504: true, 8507: true, 8505: true, 8503: true, 8508: true}
	for _, entry := range pvf.ParseList(doc) {
		if !want[entry.ID] {
			continue
		}
		t.Run(fmt.Sprint(entry.ID), func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join(root, "dungeon", entry.Path))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := pvf.Parse(entry.Path, string(text))
			if err != nil {
				t.Fatal(err)
			}
			dgn := worldmap.ParseDungeon(entry.ID, entry.Path, doc)
			fatigueCost, startOnly, fatigueErr := currentDungeonStartFatigueRule(dgn)
			if fatigueErr != nil || !startOnly || fatigueCost != 8 {
				t.Fatalf("fatigue rule cost=%d start_only=%t err=%v", fatigueCost, startOnly, fatigueErr)
			}
			if !currentGuildDungeon(&runtimeDungeonState{Dungeon: dgn}) || !reflect.DeepEqual(dgn.Metadata.DesignatedDifficulties, []int64{2}) || dgn.Metadata.MinimumRequiredLevel.Value != 50 {
				t.Fatalf("metadata=%+v", dgn.Metadata)
			}
			for _, level := range []int{49, 50, 90} {
				err := validateDungeonEntry(r.CharacterRecord{Level: level, Stats: map[string]int64{"fatigue": 100}}, dgn, nil)
				if (err != nil) != (level < 50) {
					t.Fatalf("level %d err=%v", level, err)
				}
			}
			if err := validateDungeonEntry(r.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 0}}, dgn, nil); err == nil {
				t.Fatal("zero fatigue admitted")
			}
		})
		delete(want, entry.ID)
	}
	if len(want) != 0 {
		t.Fatal("missing six-map definitions", want)
	}
}

func TestGuildDungeonLoginPublishesAuthoritative741(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	g, err := s.currentGuildForSelectedCharacter(p)
	if err != nil || g == nil {
		t.Fatalf("guild=%v err=%v", g, err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.writeCurrentGuildManagementState(p, g); err != nil {
		t.Fatal(err)
	}
	found := false
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 741 {
			found = true
			if len(packet.Body) < 17 || binary.LittleEndian.Uint32(packet.Body[5:]) <= uint32(s.gameplayNow().Unix()) {
				t.Fatalf("invalid 741: %x", packet.Body)
			}
		}
	}
	if !found {
		t.Fatal("guild login emitted no authoritative NOTI741; native catalog stays unloaded and all six maps locked")
	}
}
