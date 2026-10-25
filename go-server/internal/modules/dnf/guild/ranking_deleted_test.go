package guild

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type rankingMissingCharacter struct {
	r.CharacterRepository
	missingID     string
	loadRecord    r.CharacterRecord
	found         bool
	err           error
	loads         int
	listedDeleted *r.CharacterRecord
}

func (c *rankingMissingCharacter) ListByAccount(ctx context.Context, account string, limit int) ([]r.CharacterRecord, error) {
	rows, err := c.CharacterRepository.ListByAccount(ctx, account, limit)
	if err == nil && c.listedDeleted != nil && c.listedDeleted.AccountID == account {
		rows = append(rows, r.CloneCharacter(*c.listedDeleted))
	}
	return rows, err
}

func (c *rankingMissingCharacter) Load(ctx context.Context, id string) (r.CharacterRecord, bool, error) {
	if id == c.missingID {
		c.loads++
		return c.loadRecord, c.found, c.err
	}
	return c.CharacterRepository.Load(ctx, id)
}

func rankingDeletedFixture(t *testing.T) (r.Group, Registry, *adventuregroup.Tables, r.GuildOwner) {
	t.Helper()
	repos := memory.NewMemoryGroup()
	reg, _, master, member := textRegistry(t)
	for _, owner := range []r.GuildOwner{master, member} {
		c := r.CharacterRecord{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Level: 17, Stats: map[string]int64{"pvp_grade": 8}}
		if owner == member {
			c.Stats["delete_flag"] = 1
		}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	table, err := adventuregroup.Load(context.Background(), rankingAdventureSource{adventuregroup.CharacterManagementPath: "[point bonus]\n1 90 10\n[/point bonus]\n[manage level point]\n10 1000 2000\n[/manage level point]\n[manage level max]\n3\n[/manage level max]\n[exp bonus]\n1 0\n[/exp bonus]\n[gold bonus]\n1 0\n[/gold bonus]\n[manage option]\n1 0\n[/manage option]"})
	if err != nil {
		t.Fatal(err)
	}
	return repos, reg, table, member
}

func TestRankingDeletedMemberRequiresAuthoritativeRecord(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"deleted", "deleted_in_roster", "restored_after_deleted_roster", "missing", "wrong_account", "wrong_character", "active_missing", "load_failure"} {
		t.Run(mode, func(t *testing.T) {
			repos, reg, table, member := rankingDeletedFixture(t)
			c, _, _ := repos.Character.Load(ctx, member.CharacterID)
			probe := &rankingMissingCharacter{CharacterRepository: repos.Character, missingID: member.CharacterID, loadRecord: c, found: true}
			injected := errors.New("character storage failure")
			switch mode {
			case "deleted_in_roster", "restored_after_deleted_roster":
				listed := r.CloneCharacter(c)
				probe.listedDeleted = &listed
				if mode == "restored_after_deleted_roster" {
					probe.loadRecord.Stats["delete_flag"] = 0
				}
			case "missing":
				probe.found = false
			case "wrong_account":
				probe.loadRecord.AccountID = "foreign"
			case "wrong_character":
				probe.loadRecord.CharacterID = "foreign"
			case "active_missing":
				probe.loadRecord.Stats["delete_flag"] = 0
			case "load_failure":
				probe.err = injected
			}
			before, _ := RankingSourceDigest(reg)
			facts, err := SampleRankingMembers(ctx, probe, reg, table, 32, map[r.GuildOwner]bool{member: true}, map[r.GuildOwner]bool{member: true})
			if mode == "deleted" || mode == "deleted_in_roster" {
				if err != nil {
					t.Fatal(err)
				}
				f, ok := facts[member.CharacterID]
				if !ok || f.Owner != member || f.Character.Stats["delete_flag"] != 1 || f.Online || f.Newbie || f.PVPGrade != 0 || f.AdventureLevel != 0 {
					t.Fatal("incorrect deleted fact")
				}
			} else if mode == "load_failure" {
				if !errors.Is(err, injected) {
					t.Fatalf("storage error lost: %v", err)
				}
			} else if !errors.Is(err, ErrCharacterNotFound) {
				t.Fatalf("invalid identity accepted: %v", err)
			}
			if probe.loads != 1 {
				t.Fatalf("missing member loads=%d", probe.loads)
			}
			after, _ := RankingSourceDigest(reg)
			if before != after {
				t.Fatal("sampling changed membership")
			}
		})
	}
}

func TestRankingDeletedMemberExcludedFromAllMemberFactorsAndOnline(t *testing.T) {
	repos, reg, table, member := rankingDeletedFixture(t)
	ctx := context.Background()
	facts, err := SampleRankingMembers(ctx, repos.Character, reg, table, 32, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ParseRankingRules(rankingRulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	g, _, _ := reg.GuildForOwner(member)
	f := facts[member.CharacterID]
	f.AdventureLevel, f.PVPGrade, f.Newbie, f.Online = 30, 21, true, true
	f.Character.Stats["raid_clear_v1_1_01234567890123456789012345678901"] = now.Unix()
	facts[member.CharacterID] = f
	got, err := RankingMemberFactors(g, facts, rules, now)
	if err != nil {
		t.Fatal(err)
	}
	onlyActive := *g
	onlyActive.Members = make(map[string]Member)
	for id, m := range g.Members {
		if id != member.CharacterID {
			onlyActive.Members[id] = m
		}
	}
	want, err := RankingMemberFactors(&onlyActive, facts, rules, now)
	if err != nil || got != want || got.Members != 0.1 {
		t.Fatalf("got=%+v want=%+v err=%v", got, want, err)
	}
	unlock := &DungeonUnlockRules{byID: map[uint32]dungeonUnlockRule{}}
	for _, id := range []uint32{8500, 8504, 8507, 8505, 8503, 8508} {
		unlock.byID[id] = dungeonUnlockRule{limit: 10000, gain: 50, threshold: 50}
	}
	period, err := CalculateRankingPeriod(reg, facts, rules, &DungeonCatalog{Rules: unlock}, now)
	if err != nil || len(period.Online) != 0 {
		t.Fatal("deleted member became online observation", err)
	}
	// Even an excluded fact must prove the original account ownership.
	f.Owner.AccountID = "foreign"
	facts[member.CharacterID] = f
	if _, err := RankingMemberFactors(g, facts, rules, now); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatal("deleted foreign owner accepted", err)
	}
}

func TestRankingRestoredMemberReturnsWithoutChangingMembership(t *testing.T) {
	repos, reg, table, member := rankingDeletedFixture(t)
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, member.CharacterID)
	c.Stats["delete_flag"] = 0
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	facts, err := SampleRankingMembers(ctx, repos.Character, reg, table, 32, map[r.GuildOwner]bool{member: true}, nil)
	if err != nil || facts[member.CharacterID].PVPGrade != 8 || !facts[member.CharacterID].Online {
		t.Fatal("restored member not sampled", err)
	}
	after, _, _ := repos.Character.Load(ctx, member.CharacterID)
	if !reflect.DeepEqual(c, after) {
		t.Fatal("sample changed character")
	}
}
