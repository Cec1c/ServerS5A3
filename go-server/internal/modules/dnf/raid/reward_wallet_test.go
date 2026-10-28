package raid

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRewardOwnerWalletDoesNotCreateFateReceipt(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "wallet"}); err != nil {
		t.Fatal(err)
	}
	seedFateRewardCharacter(t, g, "wallet", "31")
	owner, err := NewRewardOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	cmd := fateRewardCommand("wallet", "31", "00112233445566778899aabbccddeeff", func(a *RewardAssets) (RewardChanges, error) {
		calls++
		_, err := cerashop.CreditVoucherCounts(a.Ledger, cerashop.VoucherCounts{TokenCera: 2000, HappyTokenCera: 4000})
		return RewardChanges{Ledger: true}, err
	})
	cmd.RequireLedger, cmd.RequireWallet = false, true
	for attempt := 0; attempt < 2; attempt++ {
		got, err := owner.Grant(ctx, cmd)
		if err != nil || got.Replayed != (attempt == 1) || got.FateRecorded || got.FateGranted != 0 || calls != 1 {
			t.Fatal("pure wallet created Fate receipt or duplicated credit", got, calls, err)
		}
	}
	a, _, _ := g.Account.Load(ctx, "wallet")
	w := cerashop.WalletBalance(a)
	if w.TokenCera != 2000 || w.HappyTokenCera != 4000 {
		t.Fatal(w)
	}
}
