package characterfriend

import (
	"context"
	"errors"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
)

func TestCurrentFriendCapacityIsAtomicAcrossOwnersAndIdempotent(t *testing.T) {
	g := fixture(t)
	ctx := context.Background()
	for i := 100; i < 180; i++ {
		if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: "dnf:2", Name: fmt.Sprintf("friend%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 80)
	for i := 100; i < 180; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs <- NewOwner(g).Add(ctx, "dnf:1", "1", fmt.Sprint(i)) }(i)
	}
	wg.Wait()
	close(errs)
	added, full := 0, 0
	for err := range errs {
		if err == nil {
			added++
		} else if errors.Is(err, dnfrepo.ErrAccountMetadataLimitReached) {
			full++
		} else {
			t.Fatal(err)
		}
	}
	rows, err := NewOwner(g).List(ctx, "dnf:1", "1")
	if err != nil || added != MaximumFriends || full != 80-MaximumFriends || len(rows) != MaximumFriends {
		t.Fatalf("added=%d full=%d rows=%d err=%v", added, full, len(rows), err)
	}
	if err := NewOwner(g).Add(ctx, "dnf:1", "1", rows[0].CharacterID); err != nil {
		t.Fatal("idempotent retry at capacity", err)
	}
	if err := NewOwner(g).Add(ctx, "dnf:1", "10", "179"); err != nil {
		t.Fatal("alt has independent capacity", err)
	}
	if err := NewOwner(g).Remove(ctx, "dnf:1", "1", rows[0].CharacterID); err != nil {
		t.Fatal(err)
	}
	if err := NewOwner(g).Add(ctx, "dnf:1", "1", rows[0].CharacterID); err != nil {
		t.Fatal("tombstone must free capacity", err)
	}
	a, _, _ := g.Account.Load(ctx, "dnf:1")
	if a.Metadata["other_setting"] != "keep" {
		t.Fatal("unrelated state lost")
	}
}
