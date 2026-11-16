package guild

import (
	"context"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildDungeonReadMatchesFullLoadAndIsolatesCounts(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	reg.DungeonPolicy = &DungeonPolicy{ResetDay: 2, ResetHour: 5, UTCOffsetMinutes: 480, ClearUnitsPerMember: 2}
	cycle, err := reg.DungeonPolicy.Cycle(now)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Dungeon = &DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{8500: 3}}
	ctx := context.Background()
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		view, err := ReadDungeonView(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		got, counts, member, err := view.Counts(owner, now)
		want, err2 := DungeonCounts(reg.Guilds[1], cycle)
		if err != nil || err2 != nil || !member || got != cycle || !reflect.DeepEqual(counts, want) {
			t.Fatalf("counts=%v cycle=%+v member=%t err=%v", counts, got, member, err)
		}
		counts[8500] = 999
		if _, _, member, err := view.Counts(r.GuildOwner{AccountID: "foreign", CharacterID: owner.CharacterID}, now); err != nil || member {
			t.Fatal("foreign account", member, err)
		}
	}
	if store.loads != 1 {
		t.Fatalf("cache hits loaded full registry %d times", store.loads)
	}
	// A legacy writer changing content at the same timestamp must invalidate.
	reg.DungeonPolicy.ResetHour = 6
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	view, err := ReadDungeonView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := view.Counts(owner, now)
	if err != nil || got.Key == cycle.Key || store.loads != 2 {
		t.Fatal("changed policy was stale", got, err, store.loads)
	}
}

func TestGuildDungeonReadEntryRulesMatchRegistry(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	catalog := dungeonLifecycleCatalog(t)
	ctx := context.Background()
	view, err := ReadDungeonView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, owners := range [][]r.GuildOwner{{owner}, {owner, owner}, {{AccountID: "foreign", CharacterID: owner.CharacterID}}, nil} {
		want, wantErr := PrepareDungeonEntries(reg, catalog, 8500, owners, "same-run", now)
		got, gotErr := view.PrepareEntries(catalog, 8500, owners, "same-run", now)
		if !reflect.DeepEqual(got, want) || gotErr != wantErr {
			t.Fatalf("entries differ: got=%+v/%v want=%+v/%v", got, gotErr, want, wantErr)
		}
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, err := ReadDungeonView(ctx, store); err == nil {
		t.Fatal("corruption accepted cached entries")
	}
}
