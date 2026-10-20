package characterfriend

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func fixture(t *testing.T) dnfrepo.Group {
	t.Helper()
	g := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, a := range []dnfrepo.AccountRecord{{AccountID: "dnf:1", Metadata: map[string]string{"other_setting": "keep"}}, {AccountID: "dnf:2"}} {
		if err := g.Account.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []dnfrepo.CharacterRecord{{CharacterID: "1", AccountID: "dnf:1", Name: "source", Level: 90}, {CharacterID: "10", AccountID: "dnf:1", Name: "alt", Level: 90}, {CharacterID: "2", AccountID: "dnf:2", Name: "target", Level: 80}, {CharacterID: "11", AccountID: "dnf:2", Name: "other", Level: 70}} {
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func TestRelationSurvivesOwnerRecreationAndUsesCurrentCharacter(t *testing.T) {
	g := fixture(t)
	ctx := context.Background()
	o := NewOwner(g)
	if err := o.Add(ctx, "dnf:1", "1", "2"); err != nil {
		t.Fatal(err)
	}
	if err := o.Add(ctx, "dnf:1", "1", "2"); err != nil {
		t.Fatal(err)
	}
	row, _, _ := g.Character.Load(ctx, "2")
	row.Name = "renamed"
	row.Level = 91
	if err := g.Character.Save(ctx, row); err != nil {
		t.Fatal(err)
	}
	rows, err := NewOwner(g).List(ctx, "dnf:1", "1")
	if err != nil || len(rows) != 1 || rows[0].Name != "renamed" || rows[0].Level != 91 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	alt, err := o.List(ctx, "dnf:1", "10")
	if err != nil || len(alt) != 0 {
		t.Fatalf("source identity leaked to alt: %v %v", alt, err)
	}
	reverse, err := o.List(ctx, "dnf:2", "2")
	if err != nil || len(reverse) != 0 {
		t.Fatalf("invented reciprocal relation: %v %v", reverse, err)
	}
	if err := o.Remove(ctx, "dnf:1", "1", "2"); err != nil {
		t.Fatal(err)
	}
	rows, err = NewOwner(g).List(ctx, "dnf:1", "1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("delete did not survive reload: %v %v", rows, err)
	}
	a, _, _ := g.Account.Load(ctx, "dnf:1")
	if a.Metadata["other_setting"] != "keep" {
		t.Fatal("unrelated metadata changed")
	}
	if err := o.Add(ctx, "dnf:1", "1", "2"); err != nil {
		t.Fatal(err)
	}
	rows, _ = o.List(ctx, "dnf:1", "1")
	if len(rows) != 1 {
		t.Fatal("readd failed")
	}
}

func TestRejectedRequestsDoNotCreateRelations(t *testing.T) {
	cases := []struct {
		name, account, source, target string
		want                          error
	}{{"ownership", "dnf:2", "1", "2", ErrSourceNotOwned}, {"missing account", "absent", "1", "2", ErrAccountMissing}, {"self", "dnf:1", "1", "1", ErrInvalidCharacter}, {"source overflow", "dnf:1", "65536", "2", ErrInvalidCharacter}, {"target overflow", "dnf:1", "1", "65536", ErrInvalidCharacter}, {"zero", "dnf:1", "1", "0", ErrInvalidCharacter}, {"alias", "dnf:1", "1", "02", ErrInvalidCharacter}, {"missing target", "dnf:1", "1", "3", ErrTargetMissing}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := fixture(t)
			err := NewOwner(g).Add(nil, tc.account, tc.source, tc.target)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			a, _, _ := g.Account.Load(context.Background(), "dnf:1")
			if len(a.Metadata) != 1 {
				t.Fatalf("failed request mutated metadata: %v", a.Metadata)
			}
		})
	}
	g := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewOwner(g).Add(ctx, "dnf:1", "1", "2"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Deliberately hides the optional single-key writer. No whole-map Save fallback
// is acceptable: two simultaneous friends could erase each other's updates.
type readWriteOnlyAccount struct{ dnfrepo.AccountRepository }

func TestMutationsRequireAtomicSingleKeyWriter(t *testing.T) {
	g := fixture(t)
	g.Account = readWriteOnlyAccount{g.Account}
	o := NewOwner(g)
	for _, err := range []error{o.Add(nil, "dnf:1", "1", "2"), o.Remove(nil, "dnf:1", "1", "2")} {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	a, _, _ := g.Account.Load(context.Background(), "dnf:1")
	if len(a.Metadata) != 1 {
		t.Fatal(a.Metadata)
	}
}

func TestConcurrentFriendsAndUnrelatedMetadataDoNotLoseUpdates(t *testing.T) {
	g := fixture(t)
	ctx := context.Background()
	const count = 50
	for i := 100; i < 100+count; i++ {
		if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: "dnf:2", Name: fmt.Sprintf("peer%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, count+1)
	for i := 100; i < 100+count; i++ {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errs <- NewOwner(g).Add(ctx, "dnf:1", "1", id) }(fmt.Sprint(i))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- g.Account.(dnfrepo.AccountMetadataEntryWriter).SaveMetadataEntry(ctx, "dnf:1", "concurrent_setting", "retained", time.Now().UTC())
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := NewOwner(g).List(ctx, "dnf:1", "1")
	if err != nil || len(rows) != count {
		t.Fatalf("lost updates: rows=%d err=%v", len(rows), err)
	}
	for i, row := range rows {
		if row.CharacterID != fmt.Sprint(100+i) {
			t.Fatal("unstable numeric ordering")
		}
	}
	a, _, _ := g.Account.Load(ctx, "dnf:1")
	if a.Metadata["other_setting"] != "keep" || a.Metadata["concurrent_setting"] != "retained" {
		t.Fatal(a.Metadata)
	}
}

func TestMissingTargetsAndMalformedRowsAreNotFabricated(t *testing.T) {
	g := fixture(t)
	ctx := context.Background()
	writer := g.Account.(dnfrepo.AccountMetadataEntryWriter)
	for key, value := range map[string]string{metadataKey("1", "2"): "1", metadataKey("1", "3"): "1", metadataKey("1", "11"): "0", metadataKey("1", "02"): "1", metadataKey("10", "11"): "1", metadataKey("1", "65536"): "1", metadataKey("1", "1"): "1", metadataKey("1", "11:other"): "1"} {
		if err := writer.SaveMetadataEntry(ctx, "dnf:1", key, value, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := NewOwner(g).List(ctx, "dnf:1", "1")
	if err != nil || len(rows) != 1 || rows[0].CharacterID != "2" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err := NewOwner(g).Remove(ctx, "dnf:1", "1", "3"); err != nil {
		t.Fatal("cannot remove missing target", err)
	}
}
