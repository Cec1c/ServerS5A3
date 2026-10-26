package guild

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type rankingAdventureSource map[string]string

func (s rankingAdventureSource) ReadText(path string) (string, error) {
	v, ok := s[path]
	if !ok {
		return "", errors.New("missing test PVF entry")
	}
	return v, nil
}

type countedRankingCharacters struct {
	r.CharacterRepository
	calls int
	fail  bool
}

func (c *countedRankingCharacters) ListByAccount(ctx context.Context, account string, limit int) ([]r.CharacterRecord, error) {
	c.calls++
	if c.fail {
		return nil, errors.New("roster failure")
	}
	return c.CharacterRepository.ListByAccount(ctx, account, limit)
}

func TestRankingSamplesDurableRosterOncePerAccount(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	reg := NewRegistry()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	table, err := adventuregroup.Load(ctx, rankingAdventureSource{adventuregroup.CharacterManagementPath: "[point bonus]\n1 90 10\n[/point bonus]\n[manage level point]\n10 1000 2000\n[/manage level point]\n[manage level max]\n3\n[/manage level max]\n[exp bonus]\n1 0\n[/exp bonus]\n[gold bonus]\n1 0\n[/gold bonus]\n[manage option]\n1 0\n[/manage option]"})
	if err != nil {
		t.Fatal(err)
	}
	var g *Guild
	for i := 1; i <= 3; i++ {
		c := r.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: "shared-account", Name: fmt.Sprint("rank", i), Level: 90, Stats: map[string]int64{"server_group_id": 1, "pvp_grade": 8}}
		if i == 3 {
			c.Stats["delete_flag"] = 1
		}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			g, err = reg.Create(c, "Ranking", now)
		} else if i == 2 {
			_, err = reg.JoinDirect(g.ID, c, now)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedRankingCharacters{CharacterRepository: repos.Character}
	owner := r.GuildOwner{AccountID: "shared-account", CharacterID: "1"}
	facts, err := SampleRankingMembers(ctx, counted, reg, table, 24, map[r.GuildOwner]bool{owner: true}, nil)
	if err != nil || len(facts) != 2 || counted.calls != 1 {
		t.Fatal(facts, counted.calls, err)
	}
	want, err := table.Calculate([]adventuregroup.Character{{Level: 90}, {Level: 90}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if fact.AdventureLevel != want.ManageLevel || fact.PVPGrade != 8 || fact.Newbie {
			t.Fatal("wrong durable source", fact)
		}
	}
	if !facts["1"].Online || facts["2"].Online {
		t.Fatal("online identity changed")
	}
	counted.fail = true
	if _, err := SampleRankingMembers(ctx, counted, reg, table, 24, nil, nil); err == nil {
		t.Fatal("failed roster became zero scores")
	}
	counted.fail = false
	c, _, _ := repos.Character.Load(ctx, "2")
	c.AccountID = "different-account"
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := SampleRankingMembers(ctx, counted, reg, table, 24, nil, nil); err == nil {
		t.Fatal("stale ownership accepted")
	}
}
