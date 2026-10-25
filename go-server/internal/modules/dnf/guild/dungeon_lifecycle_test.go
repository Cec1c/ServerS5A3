package guild

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func dungeonLifecycleCatalog(t *testing.T) *DungeonCatalog {
	t.Helper()
	text := strings.ReplaceAll(dungeonUnlockFixture(), "[item]\n123 1", "[item]\n10154439 3\n[contribute]\n20")
	c, err := ParseDungeonCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDungeonCatalogMatchedPVFRewards(t *testing.T) {
	path := os.Getenv("GUILD_DUNGEON_EXTRACTED_ETC")
	if path == "" {
		t.Skip("PVF-CLI extracted ETC not configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseDungeonCatalog(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for id, qty := range map[uint32]uint32{8500: 3, 8504: 4, 8507: 5, 8505: 5, 8503: 4, 8508: 7} {
		for _, count := range []uint32{0, 1, 34, 334} {
			row, err := c.Reward(id, count)
			if err != nil || row.Contribution != 20 || !reflect.DeepEqual(row.Items, []DungeonRewardItem{{10154439, qty}}) {
				t.Fatalf("id=%d count=%d row=%+v err=%v", id, count, row, err)
			}
		}
	}
}

func TestDungeonRewardUsesUpperThresholdAndRejectsMalformed(t *testing.T) {
	text := strings.Replace(dungeonUnlockFixture(), "[gauge ratio]\n100\n[item]\n123 1\n[/gauge ratio]", "[gauge ratio]\n99\n[item]\n123 3\n[/gauge ratio]\n[gauge ratio]\n100\n[item]\n123 7\n[/gauge ratio]", 1)
	c, err := ParseDungeonCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ count, quantity uint32 }{{0, 3}, {66, 3}, {67, 7}} {
		r, e := c.Reward(8500, tc.count)
		if e != nil || r.Items[0].Count != tc.quantity {
			t.Fatalf("%+v %v", r, e)
		}
	}
	for _, bad := range []string{strings.Replace(text, "99", "100", 1), strings.Replace(text, "123 3", "123 -3", 1), strings.Replace(text, "[item]\n123 3", "[item]\n123", 1), strings.Replace(text, "[gauge ratio]\n100", "[gauge ratio]\n98", 1)} {
		if _, e := ParseDungeonCatalog(bad); e == nil {
			t.Fatal("malformed reward accepted")
		}
	}
}

func dungeonLifecycleRegistry(t *testing.T, now time.Time) Registry {
	t.Helper()
	reg := NewRegistry()
	_, err := reg.Create(r.CharacterRecord{AccountID: "a", CharacterID: "1", Name: "Member1", Level: 90, Stats: map[string]int64{}}, "DungeonGuild", now)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Members["2"] = Member{AccountID: "b", CharacterID: "2", Name: "Member2", Grade: 4, JoinedAt: now}
	return reg
}

func TestDungeonAdmissionAndCycleLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c := dungeonLifecycleCatalog(t)
	reg := dungeonLifecycleRegistry(t, now)
	owners := []r.GuildOwner{{AccountID: "a", CharacterID: "1"}, {AccountID: "b", CharacterID: "2"}}
	entries, err := PrepareDungeonEntries(reg, c, 8500, owners, "run-one", now)
	if err != nil || len(entries) != 2 || entries[0].CompletionKey() == entries[1].CompletionKey() {
		t.Fatal(entries, err)
	}
	for _, id := range []uint32{8504, 8507, 8505, 8503, 8508, 8510} {
		if _, err = PrepareDungeonEntries(reg, c, id, owners, "run-one", now); err == nil {
			t.Fatalf("locked %d admitted", id)
		}
	}
	for _, bad := range [][]r.GuildOwner{{owners[0], owners[0]}, {{AccountID: "other", CharacterID: "1"}}, {{AccountID: "x", CharacterID: "none"}}} {
		if _, err = PrepareDungeonEntries(reg, c, 8500, bad, "run-one", now); err == nil {
			t.Fatal("bad owner admitted")
		}
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err = Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		reward, result, err := ApplyDungeonClear(ctx, repos.Settings, c, e, now)
		if err != nil || result.Count != uint32(i+1) || reward.Contribution != 20 {
			t.Fatal(result, err)
		}
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := DungeonCounts(loaded.Guilds[1], entries[0].Cycle)
	if err != nil || counts[8500] != 2 {
		t.Fatal(counts, err)
	}
	next := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cycle, err := reg.DungeonPolicy.Cycle(next)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = DungeonCounts(loaded.Guilds[1], cycle)
	if err != nil || len(counts) != 0 {
		t.Fatal("stale cycle exposed", counts, err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if _, _, err = ApplyDungeonClear(ctx, repos.Settings, c, entries[0], next); err == nil {
		t.Fatal("old entry changed new cycle")
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stale attempt wrote")
	}
	fresh, err := PrepareDungeonEntries(loaded, c, 8500, owners, "run-two", next)
	if err != nil {
		t.Fatal(err)
	}
	if _, result, err := ApplyDungeonClear(ctx, repos.Settings, c, fresh[0], next); err != nil || result.Count != 1 {
		t.Fatal(result, err)
	}
}

func TestDungeonPolicyCalendarAndChange(t *testing.T) {
	for _, offset := range []int{-720, 0, 480, 840} {
		for _, day := range []int{1, 28} {
			p := &DungeonPolicy{ResetDay: day, UTCOffsetMinutes: offset, ClearUnitsPerMember: 1}
			now := time.Date(2024, 2, 28, 12, 0, 0, 0, time.UTC)
			cycle, err := p.Cycle(now)
			if err != nil {
				t.Fatal(err)
			}
			for _, delta := range []int64{-1, 0, 1} {
				t.Run(fmt.Sprintf("%d-%d-%d", offset, day, delta), func(t *testing.T) {
					next, err := p.Cycle(time.Unix(int64(cycle.EndUnix)+delta, 0))
					if err != nil {
						t.Fatal(err)
					}
					if (next.Key == cycle.Key) != (delta < 0) {
						t.Fatalf("boundary %+v %+v", cycle, next)
					}
				})
			}
		}
	}
	for _, p := range []*DungeonPolicy{{ResetDay: 0, ClearUnitsPerMember: 1}, {ResetDay: 29, ClearUnitsPerMember: 1}, {ResetDay: 1, ClearUnitsPerMember: 0}, {ResetDay: 1, ClearUnitsPerMember: 5}, {ResetDay: 1, ClearUnitsPerMember: 1, UTCOffsetMinutes: 841}} {
		if _, err := p.Cycle(time.Now()); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
