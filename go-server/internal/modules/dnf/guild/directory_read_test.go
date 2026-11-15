package guild

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildDirectoryReadMatchesRosterAndMembershipWithoutFullReload(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	if _, err := reg.JoinDirect(1, r.CharacterRecord{CharacterID: "2", AccountID: "b", Name: "peer", Level: 90, Stats: map[string]int64{"server_group_id": 1}}, now); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		view, err := ReadDirectoryView(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range SortedMembers(reg.Guilds[1]) {
			got, found := view.Member(" " + member.CharacterID + " ")
			if !found || got != (DirectoryMember{GuildID: 1, CharacterID: member.CharacterID, AccountID: member.AccountID, JoinedAt: member.JoinedAt, Grade: member.Grade}) {
				t.Fatal(got, found)
			}
		}
		members := view.Members(1)
		if len(members) != 2 || members[0].CharacterID != "1" || members[1].CharacterID != "2" {
			t.Fatal(members)
		}
		members[0].AccountID = "poison"
		if got, _ := view.OwnedMember(owner); got.AccountID != owner.AccountID {
			t.Fatal(got)
		}
		g, err := view.OwnedRoster(owner)
		if err != nil || g == nil || g.ID != 1 || len(g.Members) != 2 || !reflect.DeepEqual(g.Benefits, reg.Guilds[1].Benefits) {
			t.Fatal(g, err)
		}
		g.Notice = "poison"
		delete(g.Members, "1")
		delete(g.Benefits, 1)
		again, err := view.OwnedRoster(owner)
		if err != nil || again.Notice != reg.Guilds[1].Notice || len(again.Members) != 2 || len(again.Benefits) != 1 {
			t.Fatal("mutable directory escaped", again, err)
		}
		foreign := r.GuildOwner{CharacterID: owner.CharacterID, AccountID: "foreign"}
		if _, found := view.OwnedMember(foreign); found {
			t.Fatal("foreign owner admitted")
		}
		if g, err := view.OwnedRoster(foreign); err != nil || g != nil {
			t.Fatal(g, err)
		}
	}
	if store.loads != 1 {
		t.Fatal("unchanged full source loads", store.loads)
	}
}

func TestGuildDirectoryReadSameTimestampLeaveRejoinAndCorruption(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	old, err := ReadDirectoryView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	member := reg.Guilds[1].Members[owner.CharacterID]
	member.JoinedAt = now.Add(-1)
	reg.Guilds[1].Members[owner.CharacterID] = member
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err := ReadDirectoryView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := old.Member(owner.CharacterID)
	after, _ := latest.Member(owner.CharacterID)
	if before.JoinedAt.Equal(after.JoinedAt) || !after.JoinedAt.Equal(member.JoinedAt) {
		t.Fatal("same timestamp hid rejoin", before, after)
	}
	delete(reg.Guilds, 1)
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err = ReadDirectoryView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := latest.Member(owner.CharacterID); found {
		t.Fatal("departed member retained")
	}
	if len(latest.Members(1)) != 0 {
		t.Fatal("deleted guild fanout retained")
	}
	if _, found := old.Member(owner.CharacterID); !found {
		t.Fatal("bounded source version changed")
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, err := ReadDirectoryView(ctx, store); err == nil {
		t.Fatal("corrupt source used stale cache")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadDirectoryView(cancelled, store); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGuildDirectoryReadChurnFallbackAndNil(t *testing.T) {
	store, _, owner, _ := newSnapshotSettingsFixture(t)
	view, err := ReadDirectoryView(context.Background(), hallChurningSettings{store})
	if err != nil {
		t.Fatal(err)
	}
	if got, found := view.OwnedMember(owner); !found || got.GuildID != 1 {
		t.Fatal(got, found)
	}
	if store.loads != 4 {
		t.Fatal(store.loads)
	}
	var empty *DirectoryReadView
	if _, found := empty.Member(owner.CharacterID); found {
		t.Fatal("nil member")
	}
	if len(empty.Members(1)) != 0 {
		t.Fatal("nil fanout")
	}
	if g, err := empty.OwnedRoster(owner); g != nil || err != nil {
		t.Fatal(g, err)
	}
}
