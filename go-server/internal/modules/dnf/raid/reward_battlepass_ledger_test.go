package raid

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRewardBattlePassLedgerCountsActualGrantOnce(t *testing.T) {
	for _, tc := range []struct {
		name         string
		earned, want uint32
	}{
		{"new", 0, 100}, {"partial_cap", 2470, 30}, {"full_cap", 2500, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			a := repo.AccountRecord{AccountID: "fate-weekly"}
			if tc.earned != 0 {
				if _, _, err := raidcurrency.Credit(&a, tc.earned, fateRewardTestTime()); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.Account.Save(ctx, a); err != nil {
				t.Fatal(err)
			}
			seedFateRewardCharacter(t, g, a.AccountID, "31")
			o, _ := NewRewardOwner(g)
			cmd := fateRewardCommand(a.AccountID, "31", "00112233445566778899aabbccddeeff", func(a *RewardAssets) (RewardChanges, error) {
				granted, _, err := raidcurrency.Credit(a.Ledger, 100, fateRewardTestTime())
				a.FateGranted = granted
				return RewardChanges{Ledger: true}, err
			})
			for i := 0; i < 2; i++ {
				result, err := o.Grant(ctx, cmd)
				if err != nil || result.Replayed != (i == 1) || result.FateGranted != tc.want {
					t.Fatalf("attempt %d: %+v %v", i, result, err)
				}
				ch, _, _ := g.Character.Load(ctx, "31")
				key := fmt.Sprintf("%s%s:43:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.CompletedAt))
				if ch.Stats[key] != int64(tc.want) {
					t.Fatalf("weekly Fate=%d want=%d", ch.Stats[key], tc.want)
				}
				bag, _, _ := g.Inventory.Load(ctx, "31")
				if len(bag.Slots) != 0 {
					t.Fatal("ledger reward created a bag mirror")
				}
			}
		})
	}
}

func TestRewardBattlePassLedgerFailureDoesNotCreditProgress(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "fate-weekly"}); err != nil {
		t.Fatal(err)
	}
	seedFateRewardCharacter(t, g, "fate-weekly", "31")
	o, _ := NewRewardOwner(g)
	cmd := fateRewardCommand("fate-weekly", "31", "00112233445566778899aabbccddeeff", func(a *RewardAssets) (RewardChanges, error) {
		_, _, err := raidcurrency.Credit(a.Ledger, 100, fateRewardTestTime())
		if err != nil {
			return RewardChanges{}, err
		}
		return RewardChanges{}, errors.New("reward projection failed after currency calculation")
	})
	if _, err := o.Grant(ctx, cmd); err == nil {
		t.Fatal("failed grant accepted")
	}
	ch, _, _ := g.Character.Load(ctx, "31")
	if len(ch.Stats) != 0 {
		t.Fatal("failed grant persisted progress or receipt", ch.Stats)
	}
	a, _, _ := g.Account.Load(ctx, "fate-weekly")
	w, err := raidcurrency.Read(a, cmd.CompletedAt)
	if err != nil || w.Earned != 0 || w.Balance != 0 {
		t.Fatal("failed grant credited currency", w, err)
	}
}
