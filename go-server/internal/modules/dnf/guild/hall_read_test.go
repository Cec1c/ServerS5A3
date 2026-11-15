package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildHallReadProjectionIsDetachedAndScoped(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	g := reg.Guilds[1]
	g.Essence = EssenceBalance{AP: 55, GP: 77}
	g.Guardians = &GuardianState{EverAcquired: true, ActiveID: 2, Owned: []Guardian{{ID: 2, Level: 12, Skills: [5]uint8{1, 2, 3, 4, 5}, APProgress: 18, GPProgress: 19}}}
	g.Merchants = &MerchantState{Levels: [2]uint8{3, 4}}
	g.Placements = &PlacementState{NextID: 2, Rows: []Placement{{ID: 1, ItemID: 1, ObjectID: 2, CreatorID: 1, Maintenance: 3, Cells: []PlacementCell{{X: 1, Y: 2}}, CreatedAt: now, ReadyAt: now}}}
	if err := Save(context.Background(), store, reg, now); err != nil {
		t.Fatal(err)
	}
	view, err := ReadHallView(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	projection, found := view.Projection(owner)
	if !found || projection.GuildID != g.ID || projection.Essence != g.Essence || !reflect.DeepEqual(projection.Guardians, g.Guardians) || !reflect.DeepEqual(projection.Merchants, g.Merchants) || !reflect.DeepEqual(projection.Placements, g.Placements) {
		t.Fatal("hall fields differ", projection)
	}
	projection.Guardians.Owned[0].Skills[0] = 0
	projection.Guardians.ActiveID = 1
	projection.Merchants.Levels[0] = 10
	projection.Placements.NextID = 99
	projection.Placements.Rows[0].Cells[0].X = 99
	again, _ := view.Projection(owner)
	if !reflect.DeepEqual(again.Guardians, g.Guardians) || !reflect.DeepEqual(again.Merchants, g.Merchants) || !reflect.DeepEqual(again.Placements, g.Placements) {
		t.Fatal("projection mutated immutable registry")
	}
	for _, invalid := range []r.GuildOwner{{CharacterID: owner.CharacterID, AccountID: "foreign"}, {CharacterID: owner.CharacterID}, {CharacterID: "missing", AccountID: owner.AccountID}} {
		if id, found := view.GuildID(invalid); found || id != 0 {
			t.Fatal("invalid membership", id, found)
		}
		if _, found := view.Projection(invalid); found {
			t.Fatal("invalid projection")
		}
	}
	if _, err := view.MerchantLevel(owner, 2, 0); !errors.Is(err, ErrNotMember) {
		t.Fatal("wrong committed guild", err)
	}
	if _, err := view.MerchantLevel(owner, 1, 2); !errors.Is(err, ErrMerchantState) {
		t.Fatal("invalid merchant", err)
	}
	var empty *HallReadView
	if _, found := empty.Projection(owner); found {
		t.Fatal("nil view admitted")
	}
}

func TestGuildHallReadFreshViewsAndFrozenPartyVersion(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	peer := r.GuildOwner{CharacterID: "2", AccountID: "b"}
	if _, err := reg.JoinDirect(1, r.CharacterRecord{CharacterID: peer.CharacterID, AccountID: peer.AccountID, Name: "peer", Level: 90, Stats: map[string]int64{"server_group_id": 1}}, now); err != nil {
		t.Fatal(err)
	}
	if err := Save(context.Background(), store, reg, now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old, err := ReadHallView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		view, err := ReadHallView(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		if id, found := view.GuildID(peer); !found || id != 1 {
			t.Fatal(id, found)
		}
		if level, err := view.MerchantLevel(owner, 1, 0); err != nil || level != MerchantMinLevel {
			t.Fatal(level, err)
		}
	}
	if store.loads != 1 {
		t.Fatalf("unchanged source loads=%d", store.loads)
	}
	reg.Guilds[1].Merchants = &MerchantState{Levels: [2]uint8{4, 5}}
	delete(reg.Guilds[1].Members, peer.CharacterID)
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err := ReadHallView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := latest.GuildID(peer); found {
		t.Fatal("removed party member admitted")
	}
	if _, found := old.GuildID(peer); !found {
		t.Fatal("bounded party version changed")
	}
	if level, err := latest.MerchantLevel(owner, 1, 0); err != nil || level != 4 {
		t.Fatal("postcommit version stale", level, err)
	}
	if level, err := old.MerchantLevel(owner, 1, 0); err != nil || level != MerchantMinLevel {
		t.Fatal("frozen version changed", level, err)
	}
	if store.loads != 2 {
		t.Fatalf("changed source loads=%d", store.loads)
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, err := ReadHallView(ctx, store); err == nil {
		t.Fatal("corrupt source used cached hall")
	}
}

type hallChurningSettings struct{ *snapshotSettingsFixture }

func (store hallChurningSettings) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	store.mu.Lock()
	store.record.Values["state"] += " "
	store.mu.Unlock()
	return store.snapshotSettingsFixture.LoadGuildSnapshotRevision(ctx)
}

func TestGuildHallReadChurnRetainsAtomicSourceFallback(t *testing.T) {
	store, _, owner, _ := newSnapshotSettingsFixture(t)
	view, err := ReadHallView(context.Background(), hallChurningSettings{store})
	if err != nil {
		t.Fatal(err)
	}
	if id, found := view.GuildID(owner); !found || id != 1 {
		t.Fatal(id, found)
	}
	if store.loads != 4 {
		t.Fatalf("three attempts plus atomic fallback loads=%d", store.loads)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadHallView(ctx, store); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation hidden", err)
	}
	if _, err := ReadHallView(context.Background(), nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatal(err)
	}
}

func BenchmarkGuildHallDetachedProjection(b *testing.B) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	owner := r.GuildOwner{CharacterID: "1", AccountID: "a"}
	g, err := reg.Create(r.CharacterRecord{CharacterID: "1", AccountID: "a", Name: "member", Level: 90, Stats: map[string]int64{"server_group_id": 1}}, "Hall", now)
	if err != nil {
		b.Fatal(err)
	}
	g.MemberLimit = 1000
	for i := 2; i <= 500; i++ {
		id := fmt.Sprint(i)
		g.Members[id] = Member{CharacterID: id, AccountID: "a" + id, Name: "member" + id, Level: 90, Grade: 4, ServerGroup: 1, JoinedAt: now}
	}
	g.Guardians = &GuardianState{EverAcquired: true, ActiveID: 1, Owned: []Guardian{{ID: 1, Level: 1}}}
	g.Merchants = &MerchantState{Levels: [2]uint8{1, 1}}
	g.Placements = &PlacementState{NextID: 1}
	if err := validateRegistry(reg); err != nil {
		b.Fatal(err)
	}
	snapshot, err := newGuildReadSnapshot(reg)
	if err != nil {
		b.Fatal(err)
	}
	view := &HallReadView{snapshot: snapshot}
	b.Run("full_guild_decode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if g, err := snapshot.display(owner); err != nil || g == nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("hall_fields", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if p, found := view.Projection(owner); !found || p.GuildID != 1 {
				b.Fatal("missing")
			}
		}
	})
}
