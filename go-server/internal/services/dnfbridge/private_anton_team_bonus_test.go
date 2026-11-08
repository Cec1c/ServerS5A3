package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestRealPVFPrivateAntonBonusProbabilityIsolationAndReplay(t *testing.T) {
	catalog, config := actualAntonRewardFixture(t)
	pool, total, err := config.Phases[1].Pool("squad_item", 0)
	if err != nil || total != 9000 || len(pool) != 5 {
		t.Fatalf("team pool %v total=%d err=%v", pool, total, err)
	}
	for _, choice := range pool {
		if choice.ContainerID == 990027101 {
			t.Fatal("standalone token card remains")
		}
	}
	now := time.Now().UTC()
	for _, id := range []uint32{10304480, 10157783} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			choice := raid.AntonRewardChoice{Type: "squad_item", Rank: -1, Weight: 1, ContainerID: id}
			phase := raid.AntonPhaseRewards{MinimumClears: 1, Choices: []raid.AntonRewardChoice{choice}}
			hits := 0
			for draw := int64(0); draw < 100; draw++ {
				f := &currentAntonRewardFlow{Mode: 1, Phase: 1, InstanceID: "00112233445566778899aabbccddeeff", CompletedAt: now, Rewards: phase, Squad: map[uint16]*currentAntonRewardCard{}}
				recipient := currentAntonRewardRecipient{CharacterID: 31, AccountID: "account", Clears: 1}
				calls := 0
				roll := func(n int64) (int64, error) {
					calls++
					if n == 100 {
						return draw, nil
					}
					return 0, nil
				}
				card, cmd, err := freezeCurrentAntonAutomaticCard(f, recipient, 3, catalog, roll)
				if err != nil {
					t.Fatal(err)
				}
				want := privateRaidExpectedZero(id, 1)
				if draw < 30 {
					want[2681917] = 2000
					hits++
				}
				privateRaidAssertContents(t, card.Plan.Rewards, want)
				before := calls
				again, _, err := freezeCurrentAntonAutomaticCard(f, recipient, 3, catalog, func(int64) (int64, error) { t.Fatal("frozen card rerolled"); return 0, nil })
				if err != nil || again != card || calls != before {
					t.Fatal("card not stable")
				}
				if draw == 0 || draw == 30 {
					g := memory.NewMemoryGroup()
					ctx := context.Background()
					if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "account"}); err != nil {
						t.Fatal(err)
					}
					if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account"}); err != nil {
						t.Fatal(err)
					}
					if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}}); err != nil {
						t.Fatal(err)
					}
					for attempt := 0; attempt < 2; attempt++ {
						r, err := grantCurrentAntonRewardPlan(ctx, g, catalog, card.Plan, cmd)
						if err != nil || r.Replayed != (attempt == 1) {
							t.Fatal("grant/replay", err)
						}
					}
					a, _, _ := g.Account.Load(ctx, "account")
					expected := int64(0)
					if draw == 0 {
						expected = 2000
					}
					if cerashop.WalletBalance(a).HappyTokenCera != expected {
						t.Fatal("incorrect wallet/replayed bonus")
					}
				}
			}
			if hits != 30 {
				t.Fatal("not 30 percent", hits)
			}
			base, err := resolveCurrentAntonConfiguredReward(catalog, choice, zeroAntonRoll, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range []*currentAntonRewardFlow{{Mode: 1, Phase: 1, Single: true}, {Mode: 1, Phase: 0}, {Mode: 2, Phase: 1}, {Mode: 3, Phase: 1}} {
				p, err := addCurrentAntonTeamTokenBonus(f, 3, catalog, base, func(int64) (int64, error) { t.Fatal("unrelated mode drew bonus"); return 0, nil })
				if err != nil || p.Fingerprint != base.Fingerprint {
					t.Fatal("unrelated mode changed")
				}
			}
			for _, bad := range []int64{-1, 100} {
				_, err := addCurrentAntonTeamTokenBonus(&currentAntonRewardFlow{Mode: 1, Phase: 1, CompletedAt: now}, 3, catalog, base, func(int64) (int64, error) { return bad, nil })
				if err == nil {
					t.Fatal("invalid RNG accepted")
				}
			}
		})
	}
}
