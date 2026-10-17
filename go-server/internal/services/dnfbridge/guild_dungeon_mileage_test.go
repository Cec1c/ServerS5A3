package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGuildSettlementOrdinaryModeAndEntryLevel(t *testing.T) {
	for _, mode := range []string{"ordinary", "over-level", "under-level", "unaccepted", "hell", "special", "tournament", "guild", "raid", "tutorial", "unknown-type", "training", "missing-range", "hell-fog", "missing-entry-level"} {
		t.Run(mode, func(t *testing.T) {
			runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 6, Metadata: worldmap.DungeonMetadata{RecommendedLevels: []int64{5, 8}}}, Character: r.CharacterRecord{Level: 9}, entryCharacterLevel: 8, ordinaryFinalRoomClearAccepted: true}
			switch mode {
			case "over-level":
				runtime.entryCharacterLevel = 9
			case "under-level":
				runtime.entryCharacterLevel = 4
			case "unaccepted":
				runtime.ordinaryFinalRoomClearAccepted = false
			case "hell-fog":
				runtime.StartMapHellFogFlag = 1
			case "hell":
				runtime.Request.HellPartyRequestFlag = 1
			case "special":
				runtime.Request.SpecialMode = 1
			case "tournament":
				runtime.Tournament = &tournamentDungeonRuntime{}
			case "guild":
				runtime.Dungeon.SourceSections = []worldmap.RawSection{{Name: "guild dungeon"}}
			case "raid":
				runtime.raidEntry = &currentRaidDungeonEntry{}
			case "tutorial":
				runtime.Dungeon.Metadata.TutorialDungeon = worldmap.OptionalInt{Set: true, Value: 1}
			case "unknown-type":
				runtime.Dungeon.Metadata.DungeonType = "future-special-mode"
			case "training":
				runtime.Dungeon.ID = 5000
			case "missing-entry-level":
				runtime.entryCharacterLevel = 0
			case "missing-range":
				runtime.Dungeon.Metadata.RecommendedLevels = nil
			}
			if got := currentDungeonEarnsGuildMileage(runtime); got != (mode == "ordinary") {
				t.Fatalf("eligible=%v", got)
			}
		})
	}
}

func TestGuildSettlementExcludesTowerMetadata(t *testing.T) {
	for _, tower := range []string{"tower of despair", "tower of grief"} {
		for _, form := range []string{"flag", "integer", "source"} {
			t.Run(tower+"/"+form, func(t *testing.T) {
				runtime := &runtimeDungeonState{
					Dungeon:             worldmap.Dungeon{ID: 6, Metadata: worldmap.DungeonMetadata{RecommendedLevels: []int64{5, 8}}},
					Character:           r.CharacterRecord{Level: 8},
					entryCharacterLevel: 8, ordinaryFinalRoomClearAccepted: true,
				}
				if !currentDungeonEarnsGuildMileage(runtime) {
					t.Fatal("ordinary control must be eligible before adding the tower marker")
				}
				switch form {
				case "flag":
					runtime.Dungeon.Metadata.Flags = map[string]bool{tower: true}
				case "integer":
					runtime.Dungeon.Metadata.IntegerValues = map[string]int64{tower: 1}
				case "source":
					runtime.Dungeon.SourceSections = []worldmap.RawSection{{Name: tower}}
				}
				if currentDungeonEarnsGuildMileage(runtime) {
					t.Fatal("tower clear was treated as an ordinary dungeon for guild mileage")
				}
			})
		}
	}
}

func TestGuildSettlementBridgeCreditsOnceAndRefreshes(t *testing.T) {
	s, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	ctx := context.Background()
	repos, _ := s.repositoryGroup()
	ch, _, _ := repos.Character.Load(ctx, "99")
	ch.Level = 1
	ch.Stats["exp"] = 90
	ch.Name = "GuildClear"
	for _, err := range []error{repos.Character.Save(ctx, ch), repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "99", Points: r.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}), repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "99", Slots: map[string]r.ItemStack{}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg := guild.NewRegistry()
	if _, err := reg.Create(ch, "ClearGuild", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := guild.Save(ctx, repos.Settings, reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtime.Character = ch
	runtime.entryCharacterLevel = 1
	runtime.Dungeon.Metadata.TutorialDungeon = worldmap.OptionalInt{}
	runtime.Dungeon.Metadata.RecommendedLevels = []int64{1, 2}
	runtime.Dungeon.Metadata.DungeonType = ""
	runtime.tutorialFinalRoomClearAccepted = false
	runtime.ordinaryFinalRoomClearAccepted = true
	runtime.clearMapCompletionKey = "guild-test-clear"
	if !currentDungeonEarnsGuildMileage(runtime) {
		t.Fatalf("recommended=%v tutorial=%v raid=%v semester=%v epic=%+v hell=%d special=%d started=%v startmode=%d level=%d entry=%d", currentDungeonIsRecommendedClear(runtime), runtime.tutorialFinalRoomClearAccepted, runtime.raidEntry != nil, runtime.semesterRun != nil, runtime.epicEntry, runtime.Request.HellPartyRequestFlag, runtime.Request.SpecialMode, runtime.HellPartyStarted, runtime.StartMapHellPartyMode, runtime.Character.Level, runtime.entryCharacterLevel)
	}
	s.gameSessions = map[uint16]*gameSession{99: session}
	resources := currentDungeonSettlementTestResources(t)
	beforeBytes := conn.write.Len()
	for i := 0; i < 2; i++ {
		session.dungeon.mu.Lock()
		res, err := s.commitCurrentDungeonSettlement(ctx, session, runtime, resources.Progression, 20)
		session.dungeon.mu.Unlock()
		if err != nil || res.Replayed != (i != 0) {
			t.Fatalf("replay=%v err=%v", res.Replayed, err)
		}
	}
	reg, _ = guild.Load(ctx, repos.Settings)
	if reg.Guilds[1].Mileage != 10 {
		t.Fatalf("balance=%d", reg.Guilds[1].Mileage)
	}
	history := reg.Guilds[1].MileageHistory
	if len(history) != 1 || history[0].Kind != 3 || history[0].Amount != 10 || history[0].CharacterName != ch.Name {
		t.Fatalf("committed clear/replay history=%+v", history)
	}
	body := guildReply(t, session, 70)
	if len(body) < 35 || binary.LittleEndian.Uint32(body[len(body)-35:]) != 10 {
		t.Fatalf("NOTI70 balance not refreshed: %x", body)
	}
	if conn.write.Len() <= beforeBytes {
		t.Fatal("no guild balance refresh")
	}
}

func TestGuildSettlementRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText("etc/(r)guild.etc")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pvf.Parse("etc/(r)guild.etc", text)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := doc.Section("Guild Mileage for Dungeon Clear")
	if !ok || len(value) != 1 || value[0].Int != int64(guild.OrdinaryDungeonMileage) {
		t.Fatalf("mileage contract=%v", value)
	}
	text, err = archive.ReadText(worldmap.DefaultDungeonList)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = pvf.Parse(worldmap.DefaultDungeonList, text)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[int64]bool{6: true, 11: true, 191: true, 8500: false, 11000: false, 11008: false, 5101: false, 5115: false, 5214: false, 210: false, 5000: false}
	for _, entry := range pvf.ParseList(doc) {
		want, found := wants[entry.ID]
		if !found {
			continue
		}
		path := "dungeon/" + strings.ReplaceAll(entry.Path, "\\", "/")
		text, err := archive.ReadText(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := worldmap.ParseDocument(path, text)
		if err != nil {
			t.Fatal(err)
		}
		dungeon := worldmap.ParseDungeon(entry.ID, path, doc)
		minimum, _, hasRange := currentDungeonRecommendedLevelRange(dungeon.Metadata)
		if !hasRange {
			minimum = 1
			dungeon.Metadata.RecommendedLevels = []int64{1, 100}
		}
		runtime := &runtimeDungeonState{Dungeon: dungeon, Character: r.CharacterRecord{Level: int(minimum)}, entryCharacterLevel: int(minimum), ordinaryFinalRoomClearAccepted: true}
		if got := currentDungeonEarnsGuildMileage(runtime); got != want {
			t.Errorf("%s eligible=%v want=%v type=%s flags=%v", path, got, want, dungeon.Metadata.DungeonType, dungeon.Metadata.Flags)
		}
		t.Logf("%d %s ordinary=%v actualRange=%v", entry.ID, path, want, hasRange)
		delete(wants, entry.ID)
	}
	if len(wants) != 0 {
		t.Fatalf("missing real dungeon fixtures=%v", wants)
	}
}
