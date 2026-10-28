package guild

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestHongbaoContributionProjectionExcludesOnlyLegacyDungeon(t *testing.T) {
	testHongbaoContributionProjection(t, memory.NewMemoryGroup())
}

func testHongbaoContributionProjection(t *testing.T, repos r.Group) {
	t.Helper()
	ctx := context.Background()
	settings := repos.Settings
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "29", AccountID: "a", Name: "projection-reader", Level: 90}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	g := &Guild{ID: 1, Members: map[string]Member{
		"29": {AccountID: "a", CharacterID: "29", ContributionPoints: 74, JoinedAt: now},
		"30": {AccountID: "b", CharacterID: "30", ContributionPoints: 276, JoinedAt: now},
	}}
	original := *g
	original.Members = map[string]Member{"29": g.Members["29"], "30": g.Members["30"]}
	add := func(key, account, character string, guildID, legacy, earned uint32, at time.Time) {
		t.Helper()
		owner := r.GuildOwner{AccountID: account, CharacterID: character}
		value, err := json.Marshal(map[string]any{"Version": 1, "Owner": owner, "GuildDungeon": DungeonClearResult{GuildID: guildID, Contribution: legacy}, "GuildContribution": ContributionIncome{GuildID: guildID, Points: earned}})
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.Save(ctx, r.SettingsRecord{Scope: r.GuildSettlementReceiptScope(owner, key), UpdatedAt: at, Values: map[string]string{"receipt": string(value)}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		add(fmt.Sprint(i), "a", "29", 1, 20, 0, now.Add(time.Hour))
	}
	add("party-activity", "a", "29", 1, 0, 1, now.Add(time.Hour))
	add("other-member", "b", "30", 1, 260, 10, now.Add(time.Hour))
	add("old-membership", "a", "29", 1, 20, 0, now.Add(-time.Hour))
	add("old-guild", "a", "29", 2, 20, 0, now.Add(time.Hour))
	add("old-account", "old-a", "29", 1, 20, 0, now.Add(time.Hour))
	add("departed-member", "c", "31", 1, 20, 0, now.Add(time.Hour))
	for range 3 {
		var members []Member
		err := repos.Guild.WithinGuild(ctx, []r.GuildOwner{{AccountID: "a", CharacterID: "29"}}, func(tx r.Group) error {
			var err error
			members, err = HongbaoContributionMembers(ctx, tx.Settings, g)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]uint32{}
		for _, m := range members {
			got[m.CharacterID] = m.ContributionPoints
		}
		if !reflect.DeepEqual(got, map[string]uint32{"29": 14, "30": 16}) || !reflect.DeepEqual(*g, original) {
			t.Fatal("projection or mutation", got, g)
		}
	}
	// A contradictory historical total must not wrap into a huge score.
	add("corrupt-total", "a", "29", 1, 100, 0, now.Add(time.Hour))
	if _, err := HongbaoContributionMembers(ctx, settings, g); err == nil {
		t.Fatal("inconsistent historical total accepted")
	}
}
