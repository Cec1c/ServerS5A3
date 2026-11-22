package dungeon

import (
	"context"
	"longheng.io/server/internal/modules/dnf/mileage"
	"testing"
)

func TestRecommendedClearMileageUsesDurableSettlementReceipt(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	o, _ := NewOwner(g)
	cmd.MileageEnabled = true
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := o.CommitSettlement(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	a, _, _ := g.Account.Load(ctx, cmd.AccountID)
	s, err := mileage.Load(a)
	if err != nil || s.Balance != 1 {
		t.Fatal(s, err)
	}
	old := cmd
	cmd.CompletionKey += "-next"
	cmd.MileageEnabled = false
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.MileageEnabled = true
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CommitSettlement(ctx, old); err != nil {
		t.Fatal(err)
	}
	a, _, _ = g.Account.Load(ctx, cmd.AccountID)
	s, err = mileage.Load(a)
	if err != nil || s.Balance != 1 {
		t.Fatal("old/closed clear gained points", s, err)
	}
}
