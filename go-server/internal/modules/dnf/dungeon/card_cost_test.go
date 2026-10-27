package dungeon

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerCardCostUsesBalanceBeforeReward(t *testing.T) {
	for _, test := range []struct {
		name                      string
		balance, cost, gold, want int64
		wantErr                   error
	}{
		{name: "exact_balance", balance: 30, cost: 30, gold: 7, want: 7},
		{name: "net_reward", balance: 100, cost: 30, gold: 25, want: 95},
		{name: "free_card", balance: 100, gold: 25, want: 125},
		{name: "one_short_even_with_large_reward", balance: 29, cost: 30, gold: 100000, wantErr: ErrGoldInsufficient},
		{name: "negative_cost", balance: 100, cost: -1, wantErr: ErrRewardInvalid},
		{name: "cost_creates_room_before_reward", balance: math.MaxInt64, cost: 10, gold: 10, want: math.MaxInt64},
		{name: "overflow_after_cost", balance: math.MaxInt64, cost: 10, gold: 11, wantErr: ErrGoldOverflow},
		{name: "maximum_cost", balance: math.MaxInt64, cost: math.MaxInt64, gold: math.MaxInt64, want: math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedDungeonOwnerCharacter(t, ctx, repos, test.balance)
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:0": {ItemID: 7001, Count: 1}},
			}); err != nil {
				t.Fatal(err)
			}
			beforeCharacter, _, _ := repos.Character.Load(ctx, "19")
			beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.GrantCardReward(ctx, CardRewardCommand{
				CharacterID: "19", MainSlots: 2, Cost: test.cost,
				Bundle: CardRewardBundle{Gold: test.gold, Items: []CardItemReward{{
					Stack: dnfrepo.ItemStack{ItemID: 7002, Count: 1},
				}}},
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("GrantCardReward err=%v, want %v", err, test.wantErr)
			}
			character, _, loadErr := repos.Character.Load(ctx, "19")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			inventory, _, loadErr := repos.Inventory.Load(ctx, "19")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if test.wantErr != nil {
				if !reflect.DeepEqual(result, CardRewardResult{}) ||
					!reflect.DeepEqual(character, beforeCharacter) || !reflect.DeepEqual(inventory, beforeInventory) {
					t.Fatalf("rejected charge changed assets: result=%+v character=%+v inventory=%+v", result, character, inventory)
				}
				if test.cost < 0 && !strings.Contains(err.Error(), "rule=card_reward_cost_negative cost=-1") {
					t.Fatalf("negative cost lost rule detail: %v", err)
				}
				return
			}
			if result.GoldBefore != test.balance || result.GoldAfter != test.want || character.Stats["gold"] != test.want ||
				len(result.ItemSlots) != 1 || result.ItemSlots[0] != 1 || inventory.Slots["0:1"].ItemID != 7002 {
				t.Fatalf("wrong charged reward: result=%+v character=%+v inventory=%+v", result, character, inventory)
			}
		})
	}
}

var errCardCostTestSave = errors.New("翻牌费用测试：资产保存失败")

type cardCostSaveProbe struct {
	called bool
	gold   int64
}

type cardCostFailInventory struct {
	dnfrepo.InventoryRepository
	characters dnfrepo.CharacterRepository
	probe      *cardCostSaveProbe
}

func (store cardCostFailInventory) Save(ctx context.Context, _ dnfrepo.InventoryRecord) error {
	character, _, err := store.characters.Load(ctx, "19")
	if err != nil {
		return err
	}
	store.probe.called, store.probe.gold = true, character.Stats["gold"]
	return errCardCostTestSave
}

func (store cardCostFailInventory) SaveFields(ctx context.Context, record dnfrepo.InventoryRecord, _ ...dnfrepo.InventoryField) error {
	return store.Save(ctx, record)
}

type cardCostFailMailbox struct {
	dnfrepo.MailboxRepository
	characters dnfrepo.CharacterRepository
	probe      *cardCostSaveProbe
}

func (store cardCostFailMailbox) Save(ctx context.Context, _ dnfrepo.MailboxRecord) error {
	character, _, err := store.characters.Load(ctx, "19")
	if err != nil {
		return err
	}
	store.probe.called, store.probe.gold = true, character.Stats["gold"]
	return errCardCostTestSave
}

type cardCostFailMailboxAssets struct {
	dnfrepo.MailboxAssetUnitOfWork
	mailbox bool
	probe   *cardCostSaveProbe
}

func (assets cardCostFailMailboxAssets) WithinMailboxAssets(ctx context.Context, accountID, characterID string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error,
) error {
	return assets.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, accountID, characterID,
		func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository) error {
			if assets.mailbox {
				mailboxes = cardCostFailMailbox{mailboxes, characters, assets.probe}
			} else {
				inventories = cardCostFailInventory{inventories, characters, assets.probe}
			}
			return apply(characters, inventories, mailboxes)
		})
}

func TestOwnerCardCostRollsBackWithRewardFailure(t *testing.T) {
	for _, failure := range []string{"projection", "inventory_save", "mailbox_save"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedDungeonOwnerCharacter(t, ctx, repos, 100)
			initial := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
				"0:0": {ItemID: 7001, Count: 1},
			}}
			if failure == "mailbox_save" {
				initial.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 7003, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, initial); err != nil {
				t.Fatal(err)
			}
			beforeCharacter, _, _ := repos.Character.Load(ctx, "19")
			beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
			beforeMailbox, beforeMailboxFound, _ := repos.Mailbox.Load(ctx, "19")
			probe := &cardCostSaveProbe{}
			if failure != "projection" {
				repos.MailboxAssets = cardCostFailMailboxAssets{repos.MailboxAssets, failure == "mailbox_save", probe}
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			command := CardRewardCommand{
				CharacterID: "19", MainSlots: 2, Cost: 30,
				Bundle: CardRewardBundle{Gold: 25, Items: []CardItemReward{{
					Stack: dnfrepo.ItemStack{ItemID: 7002, Count: 1},
				}}},
			}
			if failure == "projection" {
				command.Bundle.Items[0].Stackable = true
				command.Bundle.Items[0].ExpireAt = time.Unix(2000, 0).UTC()
				command.Project = func(dnfrepo.ItemStack, time.Time) (dnfrepo.ItemStack, error) {
					return dnfrepo.ItemStack{}, errCardCostTestSave
				}
			}
			if _, err := owner.GrantCardReward(ctx, command); !errors.Is(err, errCardCostTestSave) {
				t.Fatalf("reward failure err=%v", err)
			}
			if failure != "projection" && (!probe.called || probe.gold != 95) {
				t.Fatalf("failure was not after the charged wallet save: %+v", probe)
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			mailbox, mailboxFound, _ := repos.Mailbox.Load(ctx, "19")
			if !reflect.DeepEqual(character, beforeCharacter) || !reflect.DeepEqual(inventory, beforeInventory) ||
				mailboxFound != beforeMailboxFound || !reflect.DeepEqual(mailbox, beforeMailbox) {
				t.Fatalf("failed reward retained a charge or award: character=%+v inventory=%+v mailbox=%+v", character, inventory, mailbox)
			}
		})
	}
}
