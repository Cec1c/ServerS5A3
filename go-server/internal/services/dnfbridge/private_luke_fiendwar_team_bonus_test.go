package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func privateLukeFiendwarCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "990027102 `luke.stk`\n990027103 `fiend.stk`\n2681917 `happy.stk`\n600 `a.stk`\n601 `b.stk`\n",
		"stackable/happy.stk":     "[stackable type]\n`[material]` 4\n[stack limit]\n999999\n",
		"stackable/a.stk":         "[stackable type]\n`[material]` 0\n[stack limit]\n999\n",
		"stackable/b.stk":         "[stackable type]\n`[material]` 0\n[stack limit]\n999\n",
	}
	for path, count := range map[string]int{"luke": 4000, "fiend": 5000} {
		source["stackable/"+path+".stk"] = fmt.Sprintf("[stackable type]\n`[booster]` 0\n[stack limit]\n999\n[booster info]\n[etc]\n1 2681917 10000 %d\n[/etc]\n[/booster info]\n", count)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func privateLukeFiendwarFlow(mode byte) (*currentAntonRewardFlow, currentAntonRewardRecipient) {
	phase, id := byte(1), uint32(990027102)
	if mode == 3 {
		phase, id = 0, 990027103
	}
	owner := currentAntonRewardRecipient{CharacterID: 31, AccountID: "account", Group: 1, Clears: 1}
	f := &currentAntonRewardFlow{
		Mode: mode, Phase: phase, InstanceID: "00112233445566778899aabbccddeeff",
		CompletedAt: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		Party:       map[uint16]*currentAntonRewardCard{}, Squad: map[uint16]*currentAntonRewardCard{},
		Recipients: []currentAntonRewardRecipient{owner},
		Rewards: raid.AntonPhaseRewards{MinimumClears: 1, Choices: []raid.AntonRewardChoice{
			{Type: "squad_item", Rank: -1, Weight: 1, ContainerID: id, Flags: 1},
			{Type: "squad_item", Rank: -1, Weight: 3, ContainerID: 600},
			{Type: "squad_item", Rank: -1, Weight: 6, ContainerID: 601},
			{Type: "party_card", Rank: -1, Weight: 1, ContainerID: 600},
		}},
	}
	return f, owner
}

func TestPrivateLukeFiendwarRankWeightsAndFrozenReplay(t *testing.T) {
	catalog := privateLukeFiendwarCatalog(t)
	for _, mode := range []byte{2, 3} {
		for rank := byte(0); rank < 4; rank++ {
			for _, exact := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode%d/rank%d/exact%t", mode, rank, exact), func(t *testing.T) {
					hits := map[uint32]int{}
					for draw := int64(0); draw < 9; draw++ {
						f, owner := privateLukeFiendwarFlow(mode)
						f.Rank = rank
						if exact {
							for i := range f.Rewards.Choices {
								f.Rewards.Choices[i].Rank = int(rank)
							}
							// A common row must not leak into an exact rank pool.
							f.Rewards.Choices = append(f.Rewards.Choices, raid.AntonRewardChoice{Type: "squad_item", Rank: -1, Weight: 100, ContainerID: 999999})
						}
						before := append([]raid.AntonRewardChoice(nil), f.Rewards.Choices...)
						supplementDraws := 0
						roll := func(n int64) (int64, error) {
							if n == 9 {
								supplementDraws++
								return draw, nil
							}
							return 0, nil
						}
						card, cmd, err := freezeCurrentAntonAutomaticCard(f, owner, 3, catalog, roll)
						if err != nil || supplementDraws != 1 {
							t.Fatalf("freeze: draws=%d err=%v", supplementDraws, err)
						}
						id := uint32(600)
						if draw >= 3 {
							id = 601
						}
						amount := uint32(4000)
						if mode == 3 {
							amount = 5000
						}
						privateRaidAssertContents(t, card.Plan.Rewards, map[uint32]uint32{2681917: amount, id: 1})
						hits[id]++
						if card.Plan.Choice.Flags != 1 || cmd.Fingerprint != card.Plan.Fingerprint || !reflect.DeepEqual(before, f.Rewards.Choices) {
							t.Fatal("descriptor, fingerprint or frozen pool changed")
						}
						for _, committed := range []bool{false, true} {
							card.Committed = committed
							again, againCmd, err := freezeCurrentAntonAutomaticCard(f, owner, 3, nil, func(int64) (int64, error) { t.Fatal("frozen plan rerolled"); return 0, nil })
							if err != nil || again != card || againCmd.Fingerprint != cmd.Fingerprint {
								t.Fatal("frozen replay changed", err)
							}
						}
					}
					if hits[600] != 3 || hits[601] != 6 {
						t.Fatal("native relative weights changed", hits)
					}
				})
			}
		}
	}
}

func TestPrivateLukeFiendwarOriginalBranchesAndIsolation(t *testing.T) {
	catalog := privateLukeFiendwarCatalog(t)
	for _, mode := range []byte{2, 3} {
		hits := 0
		for draw := int64(0); draw < 10; draw++ {
			f, owner := privateLukeFiendwarFlow(mode)
			card, _, err := freezeCurrentAntonAutomaticCard(f, owner, 3, catalog, func(n int64) (int64, error) {
				if n == 10 {
					return draw, nil
				}
				return 0, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !currentRaidRewardVouchers(card.Plan).IsZero() {
				hits++
			} else {
				id := uint32(600)
				if draw >= 4 {
					id = 601
				}
				privateRaidAssertContents(t, card.Plan.Rewards, map[uint32]uint32{id: 1})
			}
		}
		if hits != 1 {
			t.Fatal("10% branch changed", mode, hits)
		}
	}
	baseFlow, _ := privateLukeFiendwarFlow(2)
	base, err := resolveCurrentAntonConfiguredReward(catalog, baseFlow.Rewards.Choices[0], zeroAntonRoll, baseFlow.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		mode, phase, kind byte
		single            bool
	}{
		{"anton", 1, 1, 3, false}, {"luke_first", 2, 0, 3, false},
		{"fiend_nonfinal", 3, 1, 3, false}, {"party", 2, 1, 1, false},
		{"pcroom", 2, 1, 2, false}, {"retry", 3, 0, 4, false},
		{"single_luke", 2, 1, 3, true}, {"single_fiend", 3, 0, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := *baseFlow
			f.Mode, f.Phase, f.Single = tc.mode, tc.phase, tc.single
			got, err := addCurrentPrivateLukeFiendwarNativeReward(&f, tc.kind, nil, base, func(int64) (int64, error) { t.Fatal("unrelated flow drew"); return 0, nil })
			if err != nil || !reflect.DeepEqual(got, base) {
				t.Fatal("unrelated flow changed", err)
			}
		})
	}
}

func TestPrivateLukeFiendwarFailClosedBeforeFreeze(t *testing.T) {
	catalog := privateLukeFiendwarCatalog(t)
	for _, name := range []string{"token_only", "exact_token_no_common_fallback", "missing_native_resource", "native_voucher", "foreign_token", "bad_rank", "negative_roll", "upper_roll", "roll_error", "nil_roll"} {
		t.Run(name, func(t *testing.T) {
			f, owner := privateLukeFiendwarFlow(2)
			roll := zeroAntonRoll
			switch name {
			case "token_only":
				f.Rewards.Choices = f.Rewards.Choices[:1]
			case "exact_token_no_common_fallback":
				f.Rewards.Choices[0].Rank = 0
			case "missing_native_resource":
				f.Rewards.Choices[1].ContainerID = 999999
			case "native_voucher":
				f.Rewards.Choices[1].ContainerID = 2681917
			case "foreign_token":
				f.Rewards.Choices[1].ContainerID = 990027103
			case "bad_rank":
				f.Rank = 4
			case "nil_roll":
				roll = nil
			default:
				roll = func(n int64) (int64, error) {
					if n != 9 {
						return 0, nil
					}
					if name == "negative_roll" {
						return -1, nil
					}
					if name == "upper_roll" {
						return n, nil
					}
					return 0, errors.New("injected RNG failure")
				}
			}
			card, _, err := freezeCurrentAntonAutomaticCard(f, owner, 3, catalog, roll)
			if err == nil || card != nil || len(f.Squad) != 0 || len(f.Party) != 0 {
				t.Fatal("failed plan was frozen", err)
			}
		})
	}
	for _, name := range []string{"amount", "leaf", "extra_row", "missing_token_choice"} {
		t.Run(name, func(t *testing.T) {
			f, _ := privateLukeFiendwarFlow(2)
			base, err := resolveCurrentAntonConfiguredReward(catalog, f.Rewards.Choices[0], zeroAntonRoll, f.CompletedAt)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "amount":
				base.Rewards[0].Count++
			case "leaf":
				base.Rewards[0].Definition.ItemID = 600
			case "extra_row":
				base.Rewards = append(base.Rewards, base.Rewards[0])
			case "missing_token_choice":
				f.Rewards.Choices = f.Rewards.Choices[1:]
			}
			if _, err = addCurrentPrivateLukeFiendwarNativeReward(f, 3, catalog, base, zeroAntonRoll); err == nil {
				t.Fatal("malformed private reward accepted")
			}
		})
	}
}

// Inject the failure AFTER ledger, inventory and receipt saves in the existing
// memory transaction, so a pre-projection rejection cannot satisfy this test.
type privateLukeFiendwarLateFailure struct {
	repo.AccountCharacterAssetUnitOfWork
	ledger  repo.AccountCharacterLedgerMailboxAssetUnitOfWork
	inspect func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository) error
}

func (u privateLukeFiendwarLateFailure) WithinAccountCharacterLedgerMailboxAssets(ctx context.Context, accountID, characterID string, apply func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error) error {
	return u.ledger.WithinAccountCharacterLedgerMailboxAssets(ctx, accountID, characterID, func(a repo.AccountRepository, ai repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository, m repo.MailboxRepository) error {
		if err := apply(a, ai, c, i, e, m); err != nil {
			return err
		}
		return u.inspect(a, c, i)
	})
}

func privateLukeFiendwarMemory(t *testing.T) repo.Group {
	t.Helper()
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
	return g
}

func TestPrivateLukeFiendwarWalletInventoryReceiptRollback(t *testing.T) {
	catalog := privateLukeFiendwarCatalog(t)
	ctx := context.Background()
	for _, mode := range []byte{2, 3} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f, owner := privateLukeFiendwarFlow(mode)
			card, cmd, err := freezeCurrentAntonAutomaticCard(f, owner, 3, catalog, zeroAntonRoll)
			if err != nil {
				t.Fatal(err)
			}
			g := privateLukeFiendwarMemory(t)
			beforeAccount, _, _ := g.Account.Load(ctx, "account")
			beforeCharacter, _, _ := g.Character.Load(ctx, "31")
			beforeInventory, _, _ := g.Inventory.Load(ctx, "31")
			original := g.AccountAssets
			amount := int64(4000)
			if mode == 3 {
				amount = 5000
			}
			injected := errors.New("late failure after combined reward projection")
			inspected := false
			g.AccountAssets = privateLukeFiendwarLateFailure{AccountCharacterAssetUnitOfWork: original, ledger: original.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork), inspect: func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository) error {
				account, _, e := a.Load(ctx, "account")
				if e != nil {
					return e
				}
				character, _, e := c.Load(ctx, "31")
				if e != nil {
					return e
				}
				inventory, _, e := i.Load(ctx, "31")
				if e != nil {
					return e
				}
				marker := fmt.Sprintf("raid_reward_v1_%d_%s_%d_3", mode, f.InstanceID, f.Phase)
				if cerashop.WalletBalance(account).HappyTokenCera != amount || privateLukeFiendwarItemCount(inventory, 600) != 1 || character.Stats[marker] == 0 {
					t.Fatal("wallet, item and receipt were not visible inside one transaction")
				}
				inspected = true
				return injected
			}}
			if _, err := grantCurrentAntonRewardPlan(ctx, g, catalog, card.Plan, cmd); !errors.Is(err, injected) || !inspected {
				t.Fatal("late rollback not exercised", err)
			}
			a, _, _ := g.Account.Load(ctx, "account")
			c, _, _ := g.Character.Load(ctx, "31")
			i, _, _ := g.Inventory.Load(ctx, "31")
			if !reflect.DeepEqual(a, beforeAccount) || !reflect.DeepEqual(c, beforeCharacter) || !reflect.DeepEqual(i, beforeInventory) {
				t.Fatal("rollback leaked wallet/item/receipt")
			}
			g.AccountAssets = original
			again, againCmd, err := freezeCurrentAntonAutomaticCard(f, owner, 3, nil, func(int64) (int64, error) { t.Fatal("rollback rerolled"); return 0, nil })
			if err != nil || again != card || againCmd.Fingerprint != cmd.Fingerprint {
				t.Fatal("rollback changed frozen entitlement", err)
			}
			for n := 0; n < 2; n++ {
				result, err := grantCurrentAntonRewardPlan(ctx, g, catalog, card.Plan, cmd)
				if err != nil || result.Replayed != (n == 1) || result.WalletChanged != (n == 0) {
					t.Fatal("grant/replay", n, err)
				}
			}
			a, _, _ = g.Account.Load(ctx, "account")
			i, _, _ = g.Inventory.Load(ctx, "31")
			if cerashop.WalletBalance(a).HappyTokenCera != amount || privateLukeFiendwarItemCount(i, 600) != 1 || privateLukeFiendwarItemCount(i, 2681917) != 0 {
				t.Fatal("wallet/item pair duplicated or voucher placed in inventory")
			}
			// Party and squad have independent receipts; neither suppresses the other.
			party, partyCmd, err := freezeCurrentAntonAutomaticCard(f, owner, 1, catalog, zeroAntonRoll)
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				result, err := grantCurrentAntonRewardPlan(ctx, g, catalog, party.Plan, partyCmd)
				if err != nil || result.Replayed != (n == 1) {
					t.Fatal("party receipt", err)
				}
			}
			i, _, _ = g.Inventory.Load(ctx, "31")
			if privateLukeFiendwarItemCount(i, 600) != 2 {
				t.Fatal("two pools suppressed or duplicated each other")
			}
		})
	}
}

func privateLukeFiendwarItemCount(inventory repo.InventoryRecord, itemID int64) int64 {
	var count int64
	for _, item := range inventory.Slots {
		if item.ItemID == itemID {
			count += item.Count
		}
	}
	return count
}
