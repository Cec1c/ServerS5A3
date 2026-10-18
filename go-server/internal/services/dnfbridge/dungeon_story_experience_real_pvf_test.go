package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realStoryExperienceArchive(t *testing.T) (*platformpvf.Archive, *worldmap.Table) {
	t.Helper()
	source, table, _ := loadRealStoryPVF(t)
	archive, ok := source.(*platformpvf.Archive)
	if !ok {
		t.Fatalf("real story PVF source type=%T, want *platformpvf.Archive", source)
	}
	return archive, table
}

func realStoryExperienceService(archive *platformpvf.Archive) *Service {
	return &Service{
		initialEquipmentArchive: archive,
		options:                 options{currentClientVersion: "official-24.9"},
	}
}

func realStoryExperienceRecord(questID int64, status string) dnfrepo.QuestRecord {
	return dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		questID: {Status: status, ProgressValue: 1},
	}}
}

func realStoryExperienceIntro(t *testing.T, block, name string) string {
	t.Helper()
	lower := strings.ToLower(block)
	needle := "[" + strings.ToLower(name) + "]"
	start := strings.Index(lower, needle)
	if start < 0 {
		t.Fatalf("real DGN story block has no %s", name)
	}
	rest := block[start+len(needle):]
	if end := strings.Index(rest, "["); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func realStoryExperienceRate(t *testing.T, archive *platformpvf.Archive, dungeon worldmap.Dungeon) int64 {
	t.Helper()
	path := "dungeon/" + strings.TrimPrefix(strings.TrimSpace(dungeon.Path), "dungeon/")
	text, err := archive.ReadText(strings.ToLower(path))
	if err != nil {
		t.Fatalf("read real DGN %s: %v", path, err)
	}
	lower := strings.ToLower(text)
	start := strings.Index(lower, "[story mode]")
	if start < 0 {
		t.Fatalf("real DGN %s has no story mode", dungeon.Path)
	}
	end := strings.Index(lower[start:], "[/story mode]")
	if end < 0 {
		t.Fatalf("real DGN %s has unclosed story mode", dungeon.Path)
	}
	block := text[start : start+end]
	fields := strings.Fields(realStoryExperienceIntro(t, block, "increase exp rate"))
	if len(fields) == 0 {
		t.Fatalf("real DGN %s has no increase exp rate", dungeon.Path)
	}
	value, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		t.Fatalf("real DGN %s increase exp rate fields=%v: %v", dungeon.Path, fields, err)
	}
	return value
}

func realStoryExperienceQuestList(t *testing.T, archive *platformpvf.Archive, dungeon worldmap.Dungeon) []int64 {
	t.Helper()
	path := "dungeon/" + strings.TrimPrefix(strings.TrimSpace(dungeon.Path), "dungeon/")
	text, err := archive.ReadText(strings.ToLower(path))
	if err != nil {
		t.Fatalf("read real DGN %s: %v", path, err)
	}
	lower := strings.ToLower(text)
	start := strings.Index(lower, "[story mode]")
	end := strings.Index(lower[start:], "[/story mode]")
	if start < 0 || end < 0 {
		t.Fatalf("real DGN %s story mode block missing", dungeon.Path)
	}
	block := text[start : start+end]
	body := realStoryExperienceIntro(t, block, "quest list")
	var ids []int64
	depth := 0
	var digits strings.Builder
	flush := func() {
		if digits.Len() == 0 {
			return
		}
		value, err := strconv.ParseInt(digits.String(), 10, 64)
		if err != nil {
			t.Fatalf("real DGN %s quest list token %q: %v", dungeon.Path, digits.String(), err)
		}
		ids = append(ids, value)
		digits.Reset()
	}
	for _, r := range body {
		switch {
		case r == '{':
			flush()
			depth++
		case r == '}':
			flush()
			if depth > 0 {
				depth--
			}
		case depth == 0 && r >= '0' && r <= '9':
			digits.WriteRune(r)
		case depth == 0:
			flush()
		}
	}
	flush()
	return ids
}

func realStoryExperienceSelection(t *testing.T, dungeon worldmap.Dungeon, questID int64) (dungeonMazeSelection, dnfrepo.QuestRecord) {
	t.Helper()
	record := realStoryExperienceRecord(questID, "active")
	selection, err := selectDungeonMaze(dungeon.Mazes, 0, record, func(int) (int, error) {
		return 0, nil
	})
	if err != nil {
		t.Fatalf("select real DGN %d maze for quest %d: %v", dungeon.ID, questID, err)
	}
	if selection.Reason != "active_quest_connection" || selection.QuestID != questID {
		t.Fatalf("real DGN %d selection=%+v, want active quest %d", dungeon.ID, selection, questID)
	}
	if selection.Index < 0 || selection.Index >= len(dungeon.Mazes) {
		t.Fatalf("real DGN %d selected index=%d mazes=%d", dungeon.ID, selection.Index, len(dungeon.Mazes))
	}
	connection := dungeon.Mazes[selection.Index].QuestConnection
	if len(connection) < 2 || connection[0] != 0 || connection[1] != questID {
		t.Fatalf("real DGN %d selected maze %d connection=%v, want [0 %d ...]", dungeon.ID, selection.Index, connection, questID)
	}
	return selection, record
}

func realStoryExperienceContains(values []int64, wanted int64) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func realStoryExperienceServiceWithGrade(t *testing.T, archive *platformpvf.Archive, questID int64, grade string) *Service {
	t.Helper()
	source := bridgePVFSource{
		dnfquest.DefaultList: fmt.Sprintf("%d \x60grade.qst\x60\n", questID),
		"n_quest/grade.qst":  fmt.Sprintf("[grade] \x60[%s]\x60\n[level] 1 99\n[type] \x60[clear map]\x60\n[int data] 3\n", grade),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	service := realStoryExperienceService(archive)
	service.questCatalog = catalog
	return service
}

func TestRealStoryExperienceDGN369CapturesSelectedEpicQuestAndFreezesRate(t *testing.T) {
	ctx := context.Background()
	archive, table := realStoryExperienceArchive(t)
	service := realStoryExperienceService(archive)
	for _, sample := range []struct {
		dungeonID int64
		questID   int64
	}{
		{dungeonID: 3, questID: 3146},
		{dungeonID: 6, questID: 3147},
		{dungeonID: 9, questID: 3148},
	} {
		t.Run(fmt.Sprintf("dgn%d-quest%d", sample.dungeonID, sample.questID), func(t *testing.T) {
			dungeon, found := table.FindDungeon(sample.dungeonID)
			if !found {
				t.Fatalf("real dungeon %d is missing", sample.dungeonID)
			}
			mode := dungeon.Metadata.StoryMode
			if mode == nil || mode.Malformed {
				t.Fatalf("real dungeon %d story mode=%+v", sample.dungeonID, mode)
			}
			if mode.DifficultySize != 1 {
				t.Fatalf("real dungeon %d difficulty size=%d, want 1", sample.dungeonID, mode.DifficultySize)
			}
			if !reflect.DeepEqual(mode.IncreaseExperienceRates, []int64{100, 30}) {
				t.Fatalf("real dungeon %d increase rates=%v, want [100 30]", sample.dungeonID, mode.IncreaseExperienceRates)
			}
			rawRate := realStoryExperienceRate(t, archive, dungeon)
			rawQuests := realStoryExperienceQuestList(t, archive, dungeon)
			if rawRate != 100 {
				t.Fatalf("real dungeon %d raw increase rate=%d, want 100", sample.dungeonID, rawRate)
			}
			if !reflect.DeepEqual(mode.QuestIDs, rawQuests) {
				t.Fatalf("real dungeon %d parsed quest list=%v raw=%v", sample.dungeonID, mode.QuestIDs, rawQuests)
			}
			if !realStoryExperienceContains(rawQuests, sample.questID) {
				t.Fatalf("real dungeon %d raw quest list=%v lacks %d", sample.dungeonID, rawQuests, sample.questID)
			}

			selection, active := realStoryExperienceSelection(t, dungeon, sample.questID)
			profile := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, active)
			if profile.QuestID != sample.questID || profile.RatePercent != rawRate {
				t.Fatalf("real dungeon %d capture=%+v, want quest=%d rate=%d", sample.dungeonID, profile, sample.questID, rawRate)
			}
			runtime := &runtimeDungeonState{
				Dungeon:         dungeon,
				Request:         dungeoncmd.SelectDungeonRequest{DungeonID: uint32(sample.dungeonID), Difficulty: 0},
				storyExperience: profile,
			}
			wantMultiplier := 1 + float64(rawRate)/100
			if got := currentDungeonStoryExperienceMultiplier(runtime); math.Abs(got-wantMultiplier) > 1e-12 {
				t.Fatalf("real dungeon %d frozen multiplier=%v, want %v", sample.dungeonID, got, wantMultiplier)
			}

			completed := realStoryExperienceRecord(sample.questID, "completed")
			if after := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, completed); after != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d completed quest still granted %+v", sample.dungeonID, after)
			}
			if got := currentDungeonStoryExperienceMultiplier(runtime); math.Abs(got-wantMultiplier) > 1e-12 {
				t.Fatalf("real dungeon %d frozen rate changed after quest completion: %v", sample.dungeonID, got)
			}

			clearedSelection := selection
			clearedSelection.Reason = "cleared_quest_connection"
			if got := service.captureCurrentDungeonStoryExperience(ctx, dungeon, clearedSelection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d non-active reason granted %+v", sample.dungeonID, got)
			}

			wrongIndex := -1
			for index := range dungeon.Mazes {
				if index == selection.Index {
					continue
				}
				connection := dungeon.Mazes[index].QuestConnection
				if len(connection) < 2 || connection[0] != 0 || connection[1] != sample.questID {
					wrongIndex = index
					break
				}
			}
			if wrongIndex < 0 {
				t.Fatalf("real dungeon %d has no non-matching maze for index guard", sample.dungeonID)
			}
			wrongSelection := selection
			wrongSelection.Index = wrongIndex
			if got := service.captureCurrentDungeonStoryExperience(ctx, dungeon, wrongSelection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d wrong maze index %d granted %+v", sample.dungeonID, wrongIndex, got)
			}

			malformedDungeon := dungeon
			malformedMode := *mode
			malformedMode.Malformed = true
			malformedDungeon.Metadata.StoryMode = &malformedMode
			if got := service.captureCurrentDungeonStoryExperience(ctx, malformedDungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d malformed story mode granted %+v", sample.dungeonID, got)
			}

			legacy := realStoryExperienceService(archive)
			legacy.options.currentClientVersion = "legacy"
			if got := legacy.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d legacy profile granted %+v", sample.dungeonID, got)
			}

			otherQuest := sample.questID + 1
			if got := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, realStoryExperienceRecord(otherQuest, "active")); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d shared party candidate granted to member active on %d: %+v", sample.dungeonID, otherQuest, got)
			}

			nonEpic := realStoryExperienceServiceWithGrade(t, archive, sample.questID, "normal")
			if got := nonEpic.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d non-epic quest %d granted %+v", sample.dungeonID, sample.questID, got)
			}

			for _, difficulty := range []byte{byte(mode.DifficultySize), 255} {
				if got := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, difficulty, active); got != (currentDungeonStoryExperienceProfile{}) {
					t.Fatalf("real dungeon %d difficulty %d granted %+v", sample.dungeonID, difficulty, got)
				}
			}

			noDeclaredMode := *mode
			noDeclaredMode.QuestIDs = nil
			noDeclaredDungeon := dungeon
			noDeclaredDungeon.Metadata.StoryMode = &noDeclaredMode
			if got := service.captureCurrentDungeonStoryExperience(ctx, noDeclaredDungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d quest outside QuestIDs granted %+v", sample.dungeonID, got)
			}

			noModeDungeon := dungeon
			noModeDungeon.Metadata.StoryMode = nil
			if got := service.captureCurrentDungeonStoryExperience(ctx, noModeDungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d nil story mode granted %+v", sample.dungeonID, got)
			}

			missingCatalog := realStoryExperienceServiceWithGrade(t, archive, otherQuest, "epic")
			if got := missingCatalog.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, active); got != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("real dungeon %d unknown quest %d granted %+v", sample.dungeonID, sample.questID, got)
			}

			partyMemberActive := realStoryExperienceRecord(sample.questID, "active")
			if got := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, partyMemberActive); got != profile {
				t.Fatalf("real dungeon %d party member on same frozen quest/maze captured %+v, want %+v", sample.dungeonID, got, profile)
			}
		})
	}
}

func TestRealStoryExperienceClearUsesDifficultyZeroGlobalRateAndGrowthFollows(t *testing.T) {
	ctx := context.Background()
	archive, table := realStoryExperienceArchive(t)
	dungeon, found := table.FindDungeon(9)
	if !found {
		t.Fatal("real dungeon 9 is missing")
	}
	selection, active := realStoryExperienceSelection(t, dungeon, 3148)
	service := realStoryExperienceService(archive)
	profile := service.captureCurrentDungeonStoryExperience(ctx, dungeon, selection, 0, active)
	if profile.QuestID != 3148 || profile.RatePercent != 100 {
		t.Fatalf("real dungeon 9 story profile=%+v, want quest3148 rate100", profile)
	}

	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := progression.LoadMonsterExperienceSources(archive)
	if err != nil {
		t.Fatal(err)
	}
	clearRanks, err := loadCurrentDungeonClearRankCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resources := currentDungeonSettlementRewardResources{
		Progression:       tables,
		ExperienceSources: sources,
		ClearRankCatalog:  clearRanks,
	}
	// DGN9 declares basis level 11 in the current PVF. Use its real metadata
	// rather than substituting the request difficulty or a synthetic base.
	clearDungeon := dungeon
	if !clearDungeon.Metadata.BasisLevel.Set || clearDungeon.Metadata.BasisLevel.Value != 11 {
		t.Fatalf("real dungeon 9 basis level=%+v, want set 11", clearDungeon.Metadata.BasisLevel)
	}
	baseRuntime := &runtimeDungeonState{
		Dungeon: clearDungeon,
		Request: dungeoncmd.SelectDungeonRequest{DungeonID: 9, Difficulty: 0},
	}
	storyRuntime := &runtimeDungeonState{
		Dungeon:         clearDungeon,
		Request:         dungeoncmd.SelectDungeonRequest{DungeonID: 9, Difficulty: 0},
		storyExperience: profile,
	}
	baseClear, err := currentDungeonClearExperience(baseRuntime, resources)
	if err != nil {
		t.Fatal(err)
	}
	storyClear, err := currentDungeonClearExperience(storyRuntime, resources)
	if err != nil {
		t.Fatal(err)
	}
	clearBase, err := resources.Progression.DungeonClearBaseExperience(int(clearDungeon.Metadata.BasisLevel.Value))
	if err != nil {
		t.Fatal(err)
	}
	difficultyRate := resources.ExperienceSources.RawModifiers().DifficultyRates[0]
	if clearBase != 2403 || math.Abs(difficultyRate-1.3) > 1e-12 {
		t.Fatalf("real dungeon 9 clear base=%d difficulty_rate=%v, want 2403/1.3", clearBase, difficultyRate)
	}
	if baseClear != 3123 || storyClear != 6247 {
		t.Fatalf("real dungeon 9 clear base/story=%d/%d, want 3123/6247", baseClear, storyClear)
	}
	wantBase := uint32(math.Floor(float64(clearBase) * difficultyRate))
	wantStory := uint32(math.Floor(float64(clearBase) * (1 + float64(profile.RatePercent)/100) * difficultyRate))
	if baseClear != wantBase || storyClear != wantStory {
		t.Fatalf("real dungeon 9 exact float clear=%d/%d want=%d/%d", baseClear, storyClear, wantBase, wantStory)
	}

	monsterService, monsterRuntime, monsterRepositories := prepareCurrentDungeonMonsterExperienceTest(t)
	monsterService.premiumCatalog = &currentPremiumCatalog{
		effectsByType: map[int64]currentPremiumEffectInfo{
			premium.TypeBonusExp: {BonusExperiencePercent: 20},
		},
	}
	future := time.Now().Add(24 * time.Hour).Unix()
	if err := monsterRepositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{"premium_expire_84": strconv.FormatInt(future, 10)},
	}); err != nil {
		t.Fatal(err)
	}
	baseGrowth := monsterService.currentGrowthContractBonusExp(ctx, "account-1", baseClear)
	storyGrowth := monsterService.currentGrowthContractBonusExp(ctx, "account-1", storyClear)
	if baseGrowth != 624 || storyGrowth != 1249 {
		t.Fatalf("clear growth base/story=%d/%d, want 624/1249", baseGrowth, storyGrowth)
	}

	catalog, err := monsterService.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	monsterResources, err := currentDungeonMonsterExperienceResourcesForCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	monster := monsterRuntime.Room.Snapshot().Monsters[0]
	monsterRuntime.Dungeon.Metadata.ExperienceIncreasingPoint = worldmap.OptionalNumber{Value: 1, Set: true}
	monsterRuntime.Character.Level = 20
	monsterRuntime.Request.Difficulty = 0
	monsterRuntime.Dungeon.Metadata.NamedMonsters = nil
	monsterRuntime.storyExperience = currentDungeonStoryExperienceProfile{}
	baseAward, err := currentDungeonMonsterExperienceAwardFor(monsterRuntime, monster, monsterResources.Sources)
	if err != nil {
		t.Fatal(err)
	}
	monsterRuntime.storyExperience = profile
	storyAward, err := currentDungeonMonsterExperienceAwardFor(monsterRuntime, monster, monsterResources.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if baseAward.PrePenaltyGain == 0 || storyAward.PrePenaltyGain != baseAward.PrePenaltyGain*2 ||
		storyAward.Gain != baseAward.Gain*2 {
		t.Fatalf("monster story award base=%+v story=%+v, want doubled base", baseAward, storyAward)
	}
	t.Logf("real DGN9 clear base=%d old=%d story=%d growth=%d/%d; monster base=%d story=%d",
		clearBase, baseClear, storyClear, baseGrowth, storyGrowth,
		baseAward.Gain, storyAward.Gain)
}
