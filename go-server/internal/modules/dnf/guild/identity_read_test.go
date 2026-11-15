package guild

import (
	"context"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildIDReadMembershipAndExactSourceInvalidation(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		id, found, err := ReadGuildID(ctx, store, owner)
		if err != nil || !found || id != 1 {
			t.Fatal(id, found, err)
		}
	}
	if store.loads != 1 {
		t.Fatalf("unchanged registry loads=%d", store.loads)
	}
	for _, bad := range []r.GuildOwner{{AccountID: "other", CharacterID: owner.CharacterID}, {CharacterID: owner.CharacterID}, {AccountID: owner.AccountID}, {AccountID: owner.AccountID, CharacterID: "missing"}} {
		if id, found, err := ReadGuildID(ctx, store, bad); err != nil || found || id != 0 {
			t.Fatal("foreign owner admitted", id, found, err)
		}
	}
	spaced := owner
	spaced.CharacterID = " " + owner.CharacterID + " "
	if id, found, err := ReadGuildID(ctx, store, spaced); err != nil || !found || id != 1 {
		t.Fatal("normalization changed", id, found, err)
	}
	member := reg.Guilds[1].Members[owner.CharacterID]
	member.AccountID = "new-owner"
	reg.Guilds[1].Members[owner.CharacterID] = member
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	if id, found, err := ReadGuildID(ctx, store, owner); err != nil || found || id != 0 {
		t.Fatal("same-timestamp ownership change missed", id, found, err)
	}
	owner.AccountID = "new-owner"
	if id, found, err := ReadGuildID(ctx, store, owner); err != nil || !found || id != 1 {
		t.Fatal(id, found, err)
	}
	// A valid empty registry models guild disbanding.
	reg = NewRegistry()
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadGuildID(ctx, store, owner); err != nil || found {
		t.Fatal("disbanded guild retained", found, err)
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, _, err := ReadGuildID(ctx, store, owner); err == nil {
		t.Fatal("corruption used prior ID")
	}
	if _, _, err := ReadGuildID(ctx, nil, owner); err != ErrRepositoryUnavailable {
		t.Fatal("missing repository", err)
	}
}
