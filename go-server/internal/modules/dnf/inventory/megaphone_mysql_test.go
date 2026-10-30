package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMegaphoneMySQLRollbackConcurrencyAndReopen(t *testing.T) {
	_, _, newGroup := itemTransactionMySQLFixture(t)
	seed, cmd := megaphoneAssets(t)
	ctx := context.Background()
	repos := newGroup()
	c, _, _ := seed.Character.Load(ctx, "19")
	inv, _, _ := seed.Inventory.Load(ctx, "19")
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	beforeC, _, _ := repos.Character.Load(ctx, "19")
	beforeI, _, _ := repos.Inventory.Load(ctx, "19")
	broken := repos
	broken.CharacterAssets = reviveCoinUseFailUOW{base: repos.CharacterAssets, late: true}
	owner, _ := NewOwner(broken)
	if _, err := owner.ConsumeMegaphone(ctx, cmd); err == nil {
		t.Fatal("late rollback accepted")
	}
	afterC, _, _ := repos.Character.Load(ctx, "19")
	afterI, _, _ := repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
		t.Fatal("SQL rollback left receipt/debit")
	}
	start := make(chan struct{})
	done := make(chan error, 8)
	for range 8 {
		go func() { o, _ := NewOwner(newGroup()); <-start; _, err := o.ConsumeMegaphone(ctx, cmd); done <- err }()
	}
	close(start)
	successes := 0
	for range 8 {
		err := <-done
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrMegaphoneReplay) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("commits=%d", successes)
	}
	reopened := newGroup()
	o, _ := NewOwner(reopened)
	if _, err := o.ConsumeMegaphone(ctx, cmd); !errors.Is(err, ErrMegaphoneReplay) {
		t.Fatal("receipt lost across repository reopen", err)
	}
	afterI, _, _ = reopened.Inventory.Load(ctx, "19")
	if afterI.Slots["0:68"].Count != 2 {
		t.Fatal("wrong committed count")
	}
}
