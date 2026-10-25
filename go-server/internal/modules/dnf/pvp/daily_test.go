package pvp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type textSource map[string]string

func (s textSource) ReadText(p string) (string, error) {
	if v, ok := s[p]; ok {
		return v, nil
	}
	return "", fmt.Errorf("missing %s", p)
}

func dailyTestSource() textSource {
	s := textSource{
		"etc/missionsystem.etc":   "[casual daily mission]\n0 113 10\n[/casual daily mission]\n[casual daily mission]\n1 118 10\n[/casual daily mission]\n[casual daily mission]\n2 131 10\n[/casual daily mission]\n",
		"pvp_mission/mission.lst": "113 `daily/a.msn`\n118 `daily/b.msn`\n131 `daily/c.msn`\n",
		"etc/pvp_ref.etc":         "[pvp experience]\n1 12000 2 27000\n[/pvp experience]\n[pvp grade point]\n0 1000 0 1 1000 -429\n[/pvp grade point]\n[pvp match exp set]\n100 -1 -1 50 -1 50\n[/pvp match exp set]\n[pvp manual match etc set]\n45 -1 1 45 -1 0\n[/pvp manual match etc set]\n",
		"etc/matchingmode.etc":    "[initial rating point]\n1000\n[pvp initial rating point]\n0\n[warm up games]\n10\n[warm up default weight]\n120\n[warm up weight]\n0 100\n[/warm up weight]\n[rating]\n2000 8 1700 10 1500 14 1400 16 1100 20 700 22 0 24\n[/rating]\n[streak win]\n8 180 6 140 4 120\n[/streak win]\n[streak lose]\n6 200 4 140 2 120\n[/streak lose]\n[addtional weight]\n2000 0 -1 1700 0 0 800 0 0 500 0 1 0 0 2\n[addtional weight]\n[first after warm up bonus]\n40\n[/first after warm up bonus]\n",
	}
	for i, condition := range []string{"hereafter join", "first aerial attack", "winning streak"} {
		parameter := 1
		if condition == "winning streak" {
			parameter = 3
		}
		s[fmt.Sprintf("pvp_mission/daily/%c.msn", 'a'+i)] = fmt.Sprintf("[grade] `[daily]` %d\n[rank range] 0 30\n[condition] `[%s]` `[all]` 3 %d\n[/condition]\n[reward victory emblem] 40\n[/reward victory emblem]\n", i, condition, parameter)
	}
	return s
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	s := dailyTestSource()
	c, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pools[0]) != 1 || c.Pools[2][0].ID != 131 || !reflect.DeepEqual(c.ExperienceBounds, []uint32{0, 12000, 27000}) {
		t.Fatalf("catalog=%+v", c)
	}
	delete(s, "pvp_mission/daily/b.msn")
	if _, err := Load(s); err == nil {
		t.Fatal("missing mission was silently substituted")
	}
	return c
}

func TestNormalTargetMissionsPreservePVFAndMatchFacts(t *testing.T) {
	s := dailyTestSource()
	definitions := []struct {
		id                       int
		category, condition, tag string
		target, parameter, pre   int
	}{
		{499, "battle", "hereafter join", "all", 100, -1, 0},
		{500, "battle", "hereafter join", "all", 333, -1, 499},
		{522, "victory", "hereafter winning count", "all", 100, -1, 0},
		{545, "rating", "rank", "all", 2, -1, 0},
		{565, "training", "combo clear", "swordman", 1, 0, 0},
		{566, "training", "combo clear", "fighter", 1, 0, 0},
		{604, "training", "arcade mode", "all", -1, 1, 0},
	}
	for _, d := range definitions {
		p := fmt.Sprintf("hidden/%d.msn", d.id)
		s["pvp_mission/mission.lst"] += fmt.Sprintf("%d `%s`\n", d.id, p)
		s["pvp_mission/"+p] = fmt.Sprintf("[grade] `[normal]`\n[normal category] `[%s]`\n[rank range] 0 30\n[condition] `[%s]` `[%s]` %d %d\n[/condition]\n", d.category, d.condition, d.tag, d.target, d.parameter)
		if d.pre != 0 {
			s["pvp_mission/"+p] += fmt.Sprintf("[pre required mission] %d\n", d.pre)
		}
	}
	s["pvp_mission/mission.lst"] += "800 `hidden/800.msn`\n"
	s["pvp_mission/hidden/800.msn"] = "[grade] `[total match normal]`\n"
	c, err := Load(s)
	if err != nil || len(c.Normal) != len(definitions) || c.Normal[1].PreRequired != 499 {
		t.Fatalf("catalog=%+v err=%v", c, err)
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a", Job: "0", Stats: map[string]int64{CertificationKey: 98, WinsKey: 40}}); err != nil {
		t.Fatal(err)
	}
	before, err := Prepare(ctx, repos, c, "a", "29", now)
	want := []Row{{499, 98, 100}, {500, 98, 333}, {522, 40, 100}, {545, 0, 2}, {565, 0, 1}, {604, 0, math.MaxUint32}}
	if err != nil || len(before.Rows) != 3 || !reflect.DeepEqual(before.NormalRows, want) {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	match := Match{Key: "target-progress", StartedAt: now, CompletedAt: now.Add(time.Minute), Won: true}
	failed := repos
	failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
	if _, err := Complete(ctx, failed, c, "a", "29", match); err == nil {
		t.Fatal("commit failure ignored")
	}
	restored, err := Prepare(ctx, repos, c, "a", "29", now)
	if err != nil || !reflect.DeepEqual(restored.NormalRows, before.NormalRows) {
		t.Fatal("failed transaction changed target progress")
	}
	want[0].Progress, want[1].Progress, want[2].Progress = 99, 99, 41
	for retry := 0; retry < 2; retry++ {
		after, err := Complete(ctx, repos, c, "a", "29", match)
		if err != nil || !reflect.DeepEqual(after.NormalRows, want) {
			t.Fatalf("retry=%d after=%+v err=%v", retry, after, err)
		}
		if retry == 0 && (!reflect.DeepEqual(after.BeforeNormalRows, before.NormalRows) || len(after.ChangedRows) != 4) {
			t.Fatal("daily and normal result deltas were not both retained")
		}
		if retry == 1 && len(after.ChangedRows) != 0 {
			t.Fatal("target replay emitted completion again")
		}
	}
	reset, err := Prepare(ctx, repos, c, "a", "29", now.Add(24*time.Hour))
	if err != nil || !reflect.DeepEqual(reset.NormalRows, want) || reset.Rows[0].Progress != 0 {
		t.Fatal("daily reset discarded normal progress")
	}
	reset.Character.Job = "1"
	rows, err := c.normalRows(reset.Character)
	if err != nil || rows[4].ID != 566 {
		t.Fatal("another job's combo cards leaked")
	}
	for _, bad := range []string{
		"[grade] `[normal]`\n[normal category] `[battle]`\n[rank range] 0 30\n[condition] `[hereafter join]` `[all]` 100\n[/condition]",
		s["pvp_mission/hidden/499.msn"] + "[pre required mission] 500\n",
		s["pvp_mission/hidden/499.msn"] + "[pre required mission] 999\n",
	} {
		s["pvp_mission/hidden/499.msn"] = bad
		if _, err := Load(s); err == nil {
			t.Fatal("malformed target or prerequisite accepted")
		}
	}
}

func TestRankedRecordHistoryCommitReplayAndWindow(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c := testCatalog(t)
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a", Job: "0",
		Stats: map[string]int64{WinsKey: 16, LossesKey: 7, "pvp_win_streak": 4}}); err != nil {
		t.Fatal(err)
	}
	outcomes := []int64{2, 1, 1, 3, 2, 1, 2, 3, 1, 1, 1, 2}
	var recent int64
	wins, losses, draws := int64(16), int64(7), int64(0)
	for i, outcome := range outcomes {
		match := Match{Key: fmt.Sprintf("history-%d", i), StartedAt: now.Add(time.Duration(i) * time.Minute), CompletedAt: now.Add(time.Duration(i+1) * time.Minute), Won: outcome == 1, Draw: outcome == 3}
		before, _, _ := repos.Character.Load(ctx, "29")
		failed := repos
		failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
		if _, err := Complete(ctx, failed, c, "a", "29", match); err == nil {
			t.Fatal("injected commit failure ignored")
		}
		unchanged, _, _ := repos.Character.Load(ctx, "29")
		if !reflect.DeepEqual(before.Stats, unchanged.Stats) {
			t.Fatal("failed transaction leaked record")
		}
		recent = (recent<<2 | outcome) & ((1 << 20) - 1)
		switch outcome {
		case 1:
			wins++
		case 2:
			losses++
		case 3:
			draws++
		}
		for retry := 0; retry < 2; retry++ {
			state, err := Complete(ctx, repos, c, "a", "29", match)
			if err != nil {
				t.Fatal(err)
			}
			s := state.Character.Stats
			if s[WinsKey] != wins || s[LossesKey] != losses || s[DrawsKey] != draws || s[RecentResultsKey] != recent || s[MaxWinStreakKey] != 4 {
				t.Fatalf("record=%v", s)
			}
		}
	}
	manual := Match{Key: "manual-history-exclusion", StartedAt: now.Add(time.Hour), CompletedAt: now.Add(2 * time.Hour), Won: true, Manual: true}
	state, err := Complete(ctx, repos, c, "a", "29", manual)
	if err != nil || state.Character.Stats[RecentResultsKey] != recent || state.Character.Stats[WinsKey] != wins {
		t.Fatalf("manual record=%+v err=%v", state, err)
	}
}

type failCommit struct {
	repo.CharacterSettlementUnitOfWork
}

func (u failCommit) WithinCharacterSettlement(ctx context.Context, id string, f func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		if err := f(tx); err != nil {
			return err
		}
		return errors.New("injected commit failure")
	})
}

func TestDailyAssignmentSettlementAndReplay(t *testing.T) {
	ctx := context.Background()
	catalog := testCatalog(t)
	repos := memory.NewMemoryGroup()
	character := repo.CharacterRecord{CharacterID: "29", AccountID: "a", Stats: map[string]int64{"exp": 555, "pvp_rank_point": 777}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	state, err := Prepare(ctx, repos, catalog, "a", "29", now)
	if err != nil || len(state.Rows) != 3 || state.Rows[0].Target != 3 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	match := Match{Key: "match1", StartedAt: now, CompletedAt: now.Add(time.Minute), Won: true, Reports: map[uint16]bool{118: true}}
	failed := repos
	failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
	if _, err := Complete(ctx, failed, catalog, "a", "29", match); err == nil {
		t.Fatal("commit failure ignored")
	}
	after, _, _ := repos.Character.Load(ctx, "29")
	if !reflect.DeepEqual(after.Stats, state.Character.Stats) {
		t.Fatal("failed settlement committed")
	}
	for i := 0; i < 2; i++ {
		state, err = Complete(ctx, repos, catalog, "a", "29", match)
		if err != nil || state.Character.Stats[CertificationKey] != 1 || state.Character.Stats[WinsKey] != 1 || state.Character.Stats[LossesKey] != 0 || state.Rows[0].Progress != 1 || state.Rows[1].Progress != 1 {
			t.Fatalf("replay=%d state=%+v err=%v", i, state, err)
		}
	}
	if state.Rows[2].Progress != 0 || state.Character.Stats["exp"] != 555 || state.Character.Stats[RankPointKey] != 855 || state.Character.Stats[ExperienceKey] != 1000 {
		t.Fatal("ranked rewards or independent normal experience are incorrect")
	}
	restored, err := Prepare(ctx, repos, catalog, "a", "29", now.Add(2*time.Minute))
	if err != nil || !reflect.DeepEqual(restored.Rows, state.Rows) {
		t.Fatal("reconnect lost progress")
	}
	if _, err = Complete(ctx, repos, catalog, "b", "29", match); err == nil {
		t.Fatal("wrong account accepted")
	}
	reset, err := Prepare(ctx, repos, catalog, "a", "29", now.Add(24*time.Hour))
	if err != nil || reset.Rows[0].Progress != 0 || reset.Character.Stats[CertificationKey] != 1 {
		t.Fatal("daily reset changed certification or retained progress")
	}
	match.Key = "spanning-reset"
	match.CompletedAt = now.Add(24 * time.Hour)
	state, err = Complete(ctx, repos, catalog, "a", "29", match)
	if err != nil || state.Rows[0].Progress != 0 || state.Character.Stats[CertificationKey] != 2 {
		t.Fatal("yesterday's match progressed today's missions")
	}
}

func TestDailyEmblemsCommitReplayRepairAndReset(t *testing.T) {
	ctx := context.Background()
	for _, reward := range []int64{0, 40} {
		t.Run(fmt.Sprint(reward), func(t *testing.T) {
			catalog := testCatalog(t)
			catalog.Pools[0][0].Emblems = reward
			repos := memory.NewMemoryGroup()
			now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			state, err := Prepare(ctx, repos, catalog, "a", "29", now)
			if err != nil {
				t.Fatal(err)
			}
			// A completed row from the old server has no paid marker.
			state.Character.Stats[dailyKey(0, "progress")] = 3
			state.Character.Stats[EmblemsKey] = 7
			if err := repos.Character.Save(ctx, state.Character); err != nil {
				t.Fatal(err)
			}
			failed := repos
			failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
			if _, err := Prepare(ctx, failed, catalog, "a", "29", now); err == nil {
				t.Fatal("reward commit failure ignored")
			}
			after, _, _ := repos.Character.Load(ctx, "29")
			if !reflect.DeepEqual(after.Stats, state.Character.Stats) {
				t.Fatal("failed reward mutated saved state")
			}
			for attempt := 0; attempt < 2; attempt++ {
				repaired, err := Prepare(ctx, repos, catalog, "a", "29", now)
				if err != nil || repaired.Character.Stats[EmblemsKey] != 7+reward || repaired.Character.Stats[dailyKey(0, "paid")] != 1 {
					t.Fatalf("repair=%+v err=%v", repaired, err)
				}
				want := int64(0)
				if attempt == 0 {
					want = reward
				}
				if repaired.EmblemsGranted != want {
					t.Fatal("reward replayed")
				}
			}
			reset, err := Prepare(ctx, repos, catalog, "a", "29", now.Add(24*time.Hour))
			if err != nil || reset.Rows[0].Progress != 0 || reset.Character.Stats[EmblemsKey] != 7+reward || reset.Character.Stats[dailyKey(0, "paid")] != 0 {
				t.Fatalf("reset=%+v err=%v", reset, err)
			}
			for i := 0; i < 3; i++ {
				match := Match{Key: fmt.Sprintf("match%d", i), StartedAt: now.Add(24 * time.Hour), CompletedAt: now.Add(24*time.Hour + time.Duration(i)*time.Minute), Won: i == 0, Draw: i == 2}
				for retry := 0; retry < 2; retry++ {
					state, err = Complete(ctx, repos, catalog, "a", "29", match)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if state.Character.Stats[EmblemsKey] != 7+reward*2 || state.Character.Stats[WinsKey] != 1 || state.Character.Stats[LossesKey] != 1 || state.Character.Stats[CertificationKey] != 3 {
				t.Fatalf("final=%+v", state)
			}
		})
	}
}

func TestDailyRewardOverflowRollsBackProgressAndWins(t *testing.T) {
	ctx := context.Background()
	catalog := testCatalog(t)
	repos := memory.NewMemoryGroup()
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	state, err := Prepare(ctx, repos, catalog, "a", "29", now)
	if err != nil {
		t.Fatal(err)
	}
	state.Character.Stats[EmblemsKey] = math.MaxInt32 - 39
	state.Character.Stats[dailyKey(0, "progress")] = 2
	if err := repos.Character.Save(ctx, state.Character); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(ctx, repos, catalog, "a", "29", Match{Key: "overflow", StartedAt: now, CompletedAt: now, Won: true}); err == nil {
		t.Fatal("overflow accepted")
	}
	after, _, _ := repos.Character.Load(ctx, "29")
	if !reflect.DeepEqual(after.Stats, state.Character.Stats) {
		t.Fatal("overflow partially committed")
	}
}
