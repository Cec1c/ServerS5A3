package pvp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func totalProgressTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	s := dailyTestSource()
	s["etc/missionsystem.etc"] = strings.ReplaceAll(s["etc/missionsystem.etc"], "casual daily mission", "total match daily mission")
	s["etc/pvp_ref.etc"] = strings.ReplaceAll(s["etc/pvp_ref.etc"], "pvp grade point", "pvp grade point total match")
	s["etc/pvp_ref.etc"] = strings.ReplaceAll(s["etc/pvp_ref.etc"], "1000", "1500")
	for i, condition := range []string{"hereafter join", "kill count", "total match win all kill"} {
		s[fmt.Sprintf("pvp_mission/daily/%c.msn", 'a'+i)] = fmt.Sprintf("[grade] `[total match daily]` %d\n[rank range] 0 30\n[condition] `[%s]` `[total match mode]` %d 1\n[/condition]\n[reward victory emblem] 80\n", i, condition, []int{3, 9, 1}[i])
	}
	for _, d := range []struct {
		id                  int
		category, condition string
		target, pre         int
	}{
		{800, "battle", "hereafter join", 100, -1},
		{801, "battle", "hereafter join", 333, 800},
		{824, "victory", "hereafter winning count", 100, -1},
	} {
		p := fmt.Sprintf("hidden/%d.msn", d.id)
		s["pvp_mission/mission.lst"] += fmt.Sprintf("%d `%s`\n", d.id, p)
		s["pvp_mission/"+p] = fmt.Sprintf("[grade] `[total match normal]`\n[normal category] `[%s]`\n[rank range] 0 30\n[condition] `[%s]` `[total match mode]` %d -1\n[/condition]\n[pre required mission] %d\n", d.category, d.condition, d.target, d.pre)
	}
	s["pvp_mission/mission.lst"] += "499 `hidden/ordinary.msn`\n"
	s["pvp_mission/hidden/ordinary.msn"] = "[grade] `[normal]`\n"
	c, err := LoadTotal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Normal) != 3 || c.Normal[1].PreRequired != 800 || c.RatingRules.GradePoints[0][1] != 1500 {
		t.Fatal("mixed ordinary and total catalog")
	}
	return c
}

type totalFailCommit struct{ repo.RentalAssetUnitOfWork }

func (u totalFailCommit) WithinPVPTotal(ctx context.Context, a string, ids [3]string, apply func(repo.AccountRepository, repo.CharacterRepository) error) error {
	return u.RentalAssetUnitOfWork.(repo.PVPTotalUnitOfWork).WithinPVPTotal(ctx, a, ids, func(a repo.AccountRepository, c repo.CharacterRepository) error {
		if err := apply(a, c); err != nil {
			return err
		}
		return errors.New("injected total commit failure")
	})
}

func TestTotalProgressAtomicReplaySeparateRankAndDayReset(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := totalProgressTestCatalog(t)
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "value"}}); err != nil {
		t.Fatal(err)
	}
	ids := [3]string{"1", "2", "3"}
	for _, id := range ids {
		if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "a", Stats: map[string]int64{ExperienceKey: 11000, RankPointKey: 1234, WinsKey: 7, "pvp_daily_day": 991, EmblemsKey: 10}}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := PrepareTotal(ctx, repos, c, "a", "1", ids, now)
	if err != nil {
		t.Fatal(err)
	}
	match := TotalMatchFacts{Match: Match{Key: "battle1", StartedAt: now, CompletedAt: now.Add(time.Minute), Won: true}, Kills: [3]uint32{0, 3, 0}, Remaining: 3}
	bad := repos
	bad.RentalAssets = totalFailCommit{repos.RentalAssets}
	if _, err := CompleteTotal(ctx, bad, c, "a", "1", ids, match); err == nil {
		t.Fatal("failed commit succeeded")
	}
	for _, id := range ids {
		v, _, _ := repos.Character.Load(ctx, id)
		if v.Stats[ExperienceKey] != 11000 || v.Stats[EmblemsKey] != 10 {
			t.Fatal("partial reward escaped rollback")
		}
	}
	var first TotalSnapshot
	for i := 0; i < 2; i++ {
		after, err := CompleteTotal(ctx, repos, c, "a", "1", ids, match)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = after
		} else if !reflect.DeepEqual(first.Stats, after.Stats) || !reflect.DeepEqual(first.ChangedRows, after.ChangedRows) || after.Before[0].Stats[ExperienceKey] != 11000 {
			t.Fatal("receipt did not restore identical result")
		}
		if after.Stats[CertificationKey] != 1 || after.Stats[WinsKey] != 1 || after.Stats[RankPointKey] != 1050 || after.Rows[0].Progress != 1 || after.Rows[1].Progress != 3 || after.Rows[2].Progress != 1 {
			t.Fatal("total facts not persisted", after.Stats, after.Rows)
		}
		if !reflect.DeepEqual(after.NormalRows, []Row{{800, 1, 100}, {801, 1, 333}, {824, 1, 100}}) ||
			!reflect.DeepEqual(after.BeforeNormalRows, []Row{{800, 0, 100}, {801, 0, 333}, {824, 0, 100}}) || len(after.ChangedRows) != 6 {
			t.Fatal("total target counters or frozen result deltas missing", after.NormalRows, after.ChangedRows)
		}
		for j, v := range after.Characters {
			if v.Stats[ExperienceKey] != 12500 || v.Stats[GradeKey] != 1 || v.Stats[RankPointKey] != 1234 || v.Stats[WinsKey] != 7 || v.Stats["pvp_daily_day"] != 991 {
				t.Fatal("actor experience or personal counters corrupted", v.Stats)
			}
			want := int64(10)
			if j == 0 {
				want += 80
			}
			if v.Stats[EmblemsKey] != want {
				t.Fatal("daily reward duplicated or wrong owner", v.Stats)
			}
		}
	}
	if before.Characters[0].Stats[ExperienceKey] != 11000 {
		t.Fatal("input mutated")
	}
	a, _, _ := repos.Account.Load(ctx, "a")
	if a.Metadata["keep"] != "value" {
		t.Fatal("unrelated account metadata lost")
	}
	reset, err := PrepareTotal(ctx, repos, c, "a", "2", ids, now.Add(24*time.Hour))
	if err != nil || reset.Rows[0].Progress != 0 || reset.Stats[RankPointKey] != 1050 || !reflect.DeepEqual(reset.NormalRows, first.NormalRows) {
		t.Fatal("day reset lost rating", err)
	}
	if _, err := CompleteTotal(ctx, repos, c, "a", "1", [3]string{"1", "2", "4"}, match); err == nil {
		t.Fatal("receipt reused for another roster")
	}
}

func TestTotalMissionsWithoutRosterShareAccountAndRollBack(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := totalProgressTestCatalog(t)
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "value"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := repos.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Stats: map[string]int64{WinsKey: 99, CertificationKey: 123}}); err != nil {
			t.Fatal(err)
		}
	}
	bad := repos
	bad.RentalAssets = renameFailCommit{repos.RentalAssets}
	if _, err := PrepareTotalMissions(ctx, bad, c, "a", "1", now); err == nil {
		t.Fatal("commit failure ignored")
	}
	a, _, _ := repos.Account.Load(ctx, "a")
	if a.Metadata[TotalProgressMetadataKey] != "" {
		t.Fatal("mission initialization escaped rollback")
	}
	first, err := PrepareTotalMissions(ctx, repos, c, "a", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := PrepareTotalMissions(ctx, repos, c, "a", "2", now)
	if err != nil || !reflect.DeepEqual(first.Rows, other.Rows) || !reflect.DeepEqual(first.NormalRows, other.NormalRows) {
		t.Fatal("missions changed with character", err)
	}
	if first.NormalRows[0].Progress != 0 || first.NormalRows[2].Progress != 0 {
		t.Fatal("ordinary counters became total progress")
	}
	if _, err := PrepareTotalMissions(ctx, repos, c, "foreign", "1", now); err == nil {
		t.Fatal("foreign owner accepted")
	}
	a, _, _ = repos.Account.Load(ctx, "a")
	if a.Metadata[TotalTeamMetadataKey] != "" || a.Metadata["keep"] != "value" {
		t.Fatal("initializing missions changed team or unrelated metadata")
	}
}

func TestTotalFactsConditionsAndTransactionScope(t *testing.T) {
	f := TotalMatchFacts{Match: Match{Won: true}, Kills: [3]uint32{1, 2, 0}, Remaining: 2, ReportsCount: map[uint16]uint32{138: 9}}
	for _, row := range []struct {
		condition string
		param     int64
		streak    int64
		want      uint32
	}{
		{"total match win left character", 2, 0, 1}, {"total match win left character", 3, 0, 0},
		{"total match win all kill", 1, 0, 0}, {"kill count", 1, 0, 3}, {"winning streak", 3, 3, 1}, {"winning streak", 3, 4, 0}, {"reduce hp", 30, 0, 5},
	} {
		if got := totalMissionAdvance(Mission{ID: 138, Condition: row.condition, Parameter: row.param}, f, row.streak); got != row.want {
			t.Fatal(row, got)
		}
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	err := repos.RentalAssets.(repo.PVPTotalUnitOfWork).WithinPVPTotal(ctx, "a", [3]string{"1", "2", "3"}, func(a repo.AccountRepository, c repo.CharacterRepository) error {
		if _, _, err := a.Load(ctx, "b"); err == nil {
			t.Fatal("foreign account admitted")
		}
		if _, _, err := c.Load(ctx, "4"); err == nil {
			t.Fatal("foreign character admitted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type renameFailCommit struct{ repo.RentalAssetUnitOfWork }

func (u renameFailCommit) WithinRentalAssets(ctx context.Context, a, c string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, a, c, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return errors.New("injected rename commit failure")
	})
}
func TestTotalRenameRollbackPreservesNameAndCard(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveTotalTeam(ctx, repos.Account, "a", TotalTeam{Name: "旧队名"}, now); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:100": {ItemID: TotalTeamRenameCardID, Count: 1, Extra: map[string]string{"equipment_lock_state": "0"}}}}); err != nil {
		t.Fatal(err)
	}
	bad := repos
	bad.RentalAssets = renameFailCommit{repos.RentalAssets}
	if err := RenameTotalTeam(ctx, bad, "a", "1", "新队名", 100, now); err == nil {
		t.Fatal("failure not returned")
	}
	team, err := ReadTotalTeam(ctx, repos, "a")
	if err != nil || team.Team.Name != "旧队名" {
		t.Fatal(team, err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	if inv.Slots["0:100"].Count != 1 {
		t.Fatal("card escaped rollback")
	}
}
