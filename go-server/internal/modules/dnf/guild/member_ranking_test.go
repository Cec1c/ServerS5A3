package guild

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func memberRankingFixture(t *testing.T, repos r.Group, n int) (r.Group, raid.WeeklyClearCommand) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, rankingLocation)
	reg := NewRegistry()
	cmd := raid.WeeklyClearCommand{Mode: 1, Limit: 2, InstanceID: strings.Repeat("ab", 16), CompletedAt: now, Policy: raid.WeeklyPolicy{ResetWeekday: time.Thursday, ResetHour: 6}}
	var group *Guild
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		account := "a" + id
		c := r.CharacterRecord{AccountID: account, CharacterID: id, Name: "角色" + id, Level: 90, Stats: map[string]int64{"unrelated": 77}}
		if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: account}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		var err error
		if i == 1 {
			group, err = reg.Create(c, "排名测试", now.Add(-time.Hour))
		} else {
			_, err = reg.JoinDirect(group.ID, c, now.Add(-time.Hour))
		}
		if err != nil {
			t.Fatal(err)
		}
		cmd.Members = append(cmd.Members, raid.ClearMember{CharacterID: id, AccountID: account})
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	return repos, cmd
}

func TestMemberRankingRulesUseIndependentCatalog(t *testing.T) {
	rules, err := ParseRankingRules(rankingRulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	member := rules.MemberRanking
	if member == nil || member.RaidClears != [3]float64{1, 2, 2} || member.SameGuildRaidBonus != 3 || member.MileageRate != 1 || member.PartyClears[247].Points != 6 || rules.PartyClears[247].Points != 5 {
		t.Fatal(rules, member)
	}
	for _, bad := range []string{
		strings.Replace(rankingRulesFixture, "[mileage count point rate] 1", "[mileage count point rate] -1", 1),
		strings.Replace(rankingRulesFixture, "`anton` 1 `luke` 2 `fiendwar` 2", "`anton` 1 `anton` 2 `fiendwar` 2", 1),
		strings.Replace(rankingRulesFixture, "[with guild member bonus point] 3", "", 1),
		strings.Replace(rankingRulesFixture, "247 6 9 5106 6 9", "247 6 9 247 6 9", 1),
	} {
		if _, err := ParseMemberRankingRules(bad); err == nil {
			t.Fatal("invalid member rules accepted")
		}
	}
}

func TestMemberRankingActualIncomePartyMonthAndMembership(t *testing.T) {
	repos, cmd := memberRankingFixture(t, memory.NewMemoryGroup(), 2)
	ctx := context.Background()
	rules, _ := ParseRankingRules(rankingRulesFixture)
	owner := r.GuildOwner{AccountID: "a1", CharacterID: "1"}
	err := repos.Guild.WithinGuild(ctx, []r.GuildOwner{owner}, func(tx r.Group) error {
		if _, err := GrantDungeonMileage(ctx, tx.Settings, owner, cmd.CompletedAt); err != nil {
			return err
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		if _, err = reg.GrantItemMileage(owner, 100, "角色1", cmd.CompletedAt); err != nil {
			return err
		}
		if err = Save(ctx, tx.Settings, reg, cmd.CompletedAt); err != nil {
			return err
		}
		_, err = ApplyRankingPartyClear(ctx, tx.Settings, owner, RankingPartyClear{DungeonID: 247, StartedAt: cmd.CompletedAt.Add(-time.Minute), Members: []r.GuildOwner{owner, {AccountID: "a2", CharacterID: "2"}}, Rules: rules, Revalidate: func() bool { return true }}, cmd.CompletedAt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	g, m, _ := reg.GuildForOwner(owner)
	got, err := MemberRankingPoints(m, rules.MemberRanking, cmd.CompletedAt)
	if err != nil || got != [3]float32{0, 110, 6} || g.RankingActivity.PartyClear != 5 || m.Mileage != 0 || m.ContributionPoints != 0 {
		t.Fatal(got, err, m)
	}
	// Spending the shared wallet and truncating recent history cannot erase income.
	g.Mileage = 0
	g.MileageHistory = nil
	if err := Save(ctx, repos.Settings, reg, cmd.CompletedAt); err != nil {
		t.Fatal(err)
	}
	reg, _ = Load(ctx, repos.Settings)
	_, m, _ = reg.GuildForOwner(owner)
	if got, _ := MemberRankingPoints(m, rules.MemberRanking, cmd.CompletedAt); got != [3]float32{0, 110, 6} {
		t.Fatal(got)
	}
	next := RankingMonthStart(cmd.CompletedAt).AddDate(0, 1, 0)
	if got, _ := MemberRankingPoints(m, rules.MemberRanking, next); got != [3]float32{} {
		t.Fatal(got)
	}
	if got, _ := MemberRankingPoints(m, rules.MemberRanking, next.Add(-time.Nanosecond)); got != [3]float32{0, 110, 6} {
		t.Fatal(got)
	}
	// A new membership contains no previous membership's optional event state.
	fresh := Member{AccountID: m.AccountID, CharacterID: m.CharacterID, JoinedAt: next}
	if got, _ := MemberRankingPoints(fresh, rules.MemberRanking, next); got != [3]float32{} {
		t.Fatal(got)
	}
}

func testMemberRankingRaid(t *testing.T, repos r.Group) {
	t.Helper()
	repos, cmd := memberRankingFixture(t, repos, 20)
	ctx := context.Background()
	rules, _ := ParseRankingRules(rankingRulesFixture)
	// The final participant belongs to another guild: no same-guild bonus.
	reg, _ := Load(ctx, repos.Settings)
	delete(reg.Guilds[1].Members, "20")
	last, _, _ := repos.Character.Load(ctx, "20")
	if _, err := reg.Create(last, "另一公会", cmd.CompletedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, repos.Settings, reg, cmd.CompletedAt); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := CommitRankedRaidClear(ctx, repos, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range cmd.Members {
		_, m, ok := reg.GuildForOwner(r.GuildOwner{AccountID: member.AccountID, CharacterID: member.CharacterID})
		if !ok {
			t.Fatal(member)
		}
		want := float32(4)
		if member.CharacterID == "20" {
			want = 1
		}
		got, err := MemberRankingPoints(m, rules.MemberRanking, cmd.CompletedAt)
		ch, _, _ := repos.Character.Load(ctx, member.CharacterID)
		if err != nil || got != [3]float32{want, 0, 0} || raid.RecordedLifetimeClears(ch, 1) != 1 || ch.Stats["unrelated"] != 77 {
			t.Fatal(member, got, err)
		}
	}
	// Inject failure after all character and registry writes in the transaction.
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	cmd.InstanceID = strings.Repeat("cd", 16)
	broken := repos
	broken.Guild = memberRankingLateFailure{repos.Guild}
	if _, err := CommitRankedRaidClear(ctx, broken, cmd); err == nil {
		t.Fatal("failure accepted")
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("registry did not roll back")
	}
	for _, member := range cmd.Members {
		ch, _, _ := repos.Character.Load(ctx, member.CharacterID)
		if raid.RecordedLifetimeClears(ch, 1) != 1 {
			t.Fatal("partial character commit")
		}
	}
	// Existing two-owner operations retain their boundary.
	if err := repos.Guild.WithinGuild(ctx, []r.GuildOwner{{"a1", "1"}, {"a2", "2"}, {"a3", "3"}}, func(r.Group) error { return nil }); err == nil {
		t.Fatal("ordinary scope widened")
	}
}

func TestMemberRankingRaidAtomicReplayAndIsolation(t *testing.T) {
	testMemberRankingRaid(t, memory.NewMemoryGroup())
}

type memberRankingLateFailure struct{ r.GuildUnitOfWork }

func (u memberRankingLateFailure) WithinGuildRaidClear(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	return u.GuildUnitOfWork.(r.GuildRaidClearUnitOfWork).WithinGuildRaidClear(ctx, owners, func(tx r.Group) error {
		if tx.Account != nil || tx.Inventory != nil {
			return errors.New("extra scope")
		}
		if _, _, err := tx.Character.Load(ctx, "foreign"); err == nil {
			return errors.New("unscoped character")
		}
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("injected late failure")
	})
}

func TestMemberRankingRejectsCorruptAndDelayedEvents(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, rankingLocation)
	rules, _ := ParseMemberRankingRules(rankingRulesFixture)
	for _, a := range []MemberRankingActivity{
		{Month: now}, {Month: RankingMonthStart(now), PartyClear: math.NaN()},
		{Month: RankingMonthStart(now), SameGuildRaids: 1}, {Month: RankingMonthStart(now), Mileage: math.MaxInt32 + 1},
	} {
		if _, err := MemberRankingPoints(Member{RankingActivity: &a}, rules, now); err == nil {
			t.Fatal(a)
		}
	}
	m := Member{JoinedAt: now.AddDate(0, -2, 0), RankingActivity: &MemberRankingActivity{Month: RankingMonthStart(now), Mileage: 42}}
	a, apply, err := memberRankingAt(m, now.AddDate(0, -1, 0))
	if err != nil || apply || a != (MemberRankingActivity{}) {
		t.Fatal(a, apply, err)
	}
}
