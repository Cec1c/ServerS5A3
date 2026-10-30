package vending

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const vendingEntitlementBaselineMailID = "vending-entitlement-baseline"

type vendingEntitlementSnapshot struct {
	account        repo.AccountRecord
	accountFound   bool
	character      repo.CharacterRecord
	characterFound bool
	inventory      repo.InventoryRecord
	inventoryFound bool
	mailbox        repo.MailboxRecord
	mailboxFound   bool
}

func readVendingEntitlementSnapshot(t *testing.T, repos repo.Group, accountID, characterID string) vendingEntitlementSnapshot {
	t.Helper()
	ctx := context.Background()
	var snapshot vendingEntitlementSnapshot
	var err error

	snapshot.account, snapshot.accountFound, err = repos.Account.Load(ctx, accountID)
	if err != nil {
		t.Fatalf("load account snapshot: %v", err)
	}
	snapshot.character, snapshot.characterFound, err = repos.Character.Load(ctx, characterID)
	if err != nil {
		t.Fatalf("load character snapshot: %v", err)
	}
	snapshot.inventory, snapshot.inventoryFound, err = repos.Inventory.Load(ctx, characterID)
	if err != nil {
		t.Fatalf("load inventory snapshot: %v", err)
	}
	snapshot.mailbox, snapshot.mailboxFound, err = repos.Mailbox.Load(ctx, characterID)
	if err != nil {
		t.Fatalf("load mailbox snapshot: %v", err)
	}
	return snapshot
}

func prepareVendingEntitlementFixture(
	t *testing.T,
	metadata func(string, time.Time) map[string]string,
) (repo.Group, Command) {
	t.Helper()
	ctx := context.Background()
	repos, cmd, now := fullConsumableVendingFixture(t)
	cmd.Now = now
	cmd.OverflowToMail = false

	account, found, err := repos.Account.Load(ctx, cmd.AccountID)
	if err != nil || !found {
		t.Fatalf("load fixture account found=%t err=%v", found, err)
	}
	account.Metadata = metadata(premium.MetadataKey(premium.TypeBlackDiamond), now)
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatalf("save entitlement boundary account: %v", err)
	}

	mailbox := repo.MailboxRecord{
		CharacterID: cmd.CharacterID,
		Mails: map[string]repo.MailRecord{
			vendingEntitlementBaselineMailID: {
				MailID:               vendingEntitlementBaselineMailID,
				RecipientCharacterID: cmd.CharacterID,
				Title:                "existing entitlement boundary mail",
				Attachments: []repo.MailAttachment{{
					ItemID:   7001,
					Count:    2,
					Bind:     true,
					ExpireAt: now.Add(24 * time.Hour),
					RawEntry: []byte{1, 2, 3},
					Extra:    map[string]string{"fixture": "preserve"},
				}},
				Metadata:  map[string]string{"fixture": "preserve"},
				CreatedAt: now.Add(-time.Hour),
			},
		},
		UpdatedAt: now.Add(-time.Minute),
	}
	if err := repos.Mailbox.Save(ctx, mailbox); err != nil {
		t.Fatalf("save entitlement boundary mailbox: %v", err)
	}
	return repos, cmd
}

func TestVendingBlackDiamondEntitlementBoundaries(t *testing.T) {
	ctx := context.Background()
	const coinKey = "0:141"
	cases := []struct {
		name     string
		metadata func(string, time.Time) map[string]string
	}{
		{
			name: "metadata table missing",
			metadata: func(string, time.Time) map[string]string {
				return nil
			},
		},
		{
			name: "metadata table empty",
			metadata: func(string, time.Time) map[string]string {
				return map[string]string{}
			},
		},
		{
			name: "empty expiry",
			metadata: func(key string, _ time.Time) map[string]string {
				return map[string]string{key: ""}
			},
		},
		{
			name: "malformed expiry",
			metadata: func(key string, _ time.Time) map[string]string {
				return map[string]string{key: "not-a-unix-timestamp"}
			},
		},
		{
			name: "negative expiry",
			metadata: func(key string, _ time.Time) map[string]string {
				return map[string]string{key: "-1"}
			},
		},
		{
			name: "expired before now",
			metadata: func(key string, now time.Time) map[string]string {
				return map[string]string{key: strconv.FormatInt(now.Add(-time.Second).Unix(), 10)}
			},
		},
		{
			name: "expires exactly now",
			metadata: func(key string, now time.Time) map[string]string {
				return map[string]string{key: strconv.FormatInt(now.Unix(), 10)}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos, cmd := prepareVendingEntitlementFixture(t, tc.metadata)
			before := readVendingEntitlementSnapshot(t, repos, cmd.AccountID, cmd.CharacterID)
			beforeCoin, found := before.inventory.Slots[coinKey]
			if !before.inventoryFound || !found || beforeCoin.Count <= cmd.CoinCount {
				t.Fatalf("invalid entitlement fixture inventory: found=%t coinFound=%t coin=%+v", before.inventoryFound, found, beforeCoin)
			}

			result, err := Use(ctx, repos, cmd)
			if err == nil || err.Error() != "black diamond required" {
				t.Fatalf("Use error=%v, want black diamond required", err)
			}
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("rejected entitlement exposed a settlement result: %+v", result)
			}

			after := readVendingEntitlementSnapshot(t, repos, cmd.AccountID, cmd.CharacterID)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected entitlement changed persisted state: before=%+v after=%+v", before, after)
			}
			afterCoin, found := after.inventory.Slots[coinKey]
			if !found || !reflect.DeepEqual(afterCoin, beforeCoin) {
				t.Fatalf("rejected entitlement changed coin slot: found=%t before=%+v after=%+v", found, beforeCoin, afterCoin)
			}
			for slot, stack := range after.inventory.Slots {
				if stack.ItemID == cmd.Reward.Stack.ItemID {
					t.Fatalf("rejected entitlement placed reward in inventory slot %s: %+v", slot, stack)
				}
			}
			if _, found := after.mailbox.Mails[vendingEntitlementBaselineMailID]; !found || len(after.mailbox.Mails) != len(before.mailbox.Mails) {
				t.Fatalf("rejected entitlement changed mailbox: found=%t before=%+v after=%+v", found, before.mailbox.Mails, after.mailbox.Mails)
			}
		})
	}

	t.Run("future expiry by one second debits and rewards once", func(t *testing.T) {
		repos, cmd := prepareVendingEntitlementFixture(t, func(key string, now time.Time) map[string]string {
			return map[string]string{key: strconv.FormatInt(now.Add(time.Second).Unix(), 10)}
		})
		inventory, found, err := repos.Inventory.Load(ctx, cmd.CharacterID)
		if err != nil || !found {
			t.Fatalf("load fixture inventory found=%t err=%v", found, err)
		}
		rewardSlotKey := "0:" + strconv.Itoa(int(cmd.Reward.SlotStart))
		if _, found := inventory.Slots[rewardSlotKey]; !found {
			t.Fatalf("fixture reward page slot %s is not occupied", rewardSlotKey)
		}
		delete(inventory.Slots, rewardSlotKey)
		if err := repos.Inventory.Save(ctx, inventory); err != nil {
			t.Fatalf("open fixture reward slot: %v", err)
		}
		before := readVendingEntitlementSnapshot(t, repos, cmd.AccountID, cmd.CharacterID)

		result, err := Use(ctx, repos, cmd)
		if err != nil {
			t.Fatalf("Use future entitlement: %v", err)
		}
		if result.CoinRemaining != 3 || result.RewardSlot < 0 || result.OverflowMailID != "" {
			t.Fatalf("future entitlement settlement=%+v", result)
		}
		if result.RewardStack.ItemID != cmd.Reward.Stack.ItemID || result.RewardStack.Count != cmd.Reward.Stack.Count {
			t.Fatalf("future entitlement reward=%+v, want %+v", result.RewardStack, cmd.Reward.Stack)
		}

		after := readVendingEntitlementSnapshot(t, repos, cmd.AccountID, cmd.CharacterID)
		if before.accountFound != after.accountFound || !reflect.DeepEqual(before.account, after.account) {
			t.Fatalf("accepted entitlement changed account: before=%+v after=%+v", before.account, after.account)
		}
		if before.characterFound != after.characterFound || !reflect.DeepEqual(before.character, after.character) {
			t.Fatalf("accepted entitlement changed character: before=%+v after=%+v", before.character, after.character)
		}
		beforeCoin := before.inventory.Slots[coinKey]
		afterCoin, found := after.inventory.Slots[coinKey]
		if !found || afterCoin.Count != beforeCoin.Count-cmd.CoinCount || result.CoinRemaining != afterCoin.Count {
			t.Fatalf("accepted entitlement coin before=%+v after=%+v found=%t result=%+v", beforeCoin, afterCoin, found, result)
		}
		if before.mailboxFound != after.mailboxFound || !reflect.DeepEqual(before.mailbox, after.mailbox) {
			t.Fatalf("accepted entitlement changed mailbox: before=%+v after=%+v", before.mailbox, after.mailbox)
		}
		committedReward, found := after.inventory.Slots["0:"+strconv.Itoa(int(result.RewardSlot))]
		if !found || committedReward.ItemID != cmd.Reward.Stack.ItemID || committedReward.Count != cmd.Reward.Stack.Count {
			t.Fatalf("accepted entitlement committed reward found=%t stack=%+v", found, committedReward)
		}
		var inventoryRewardCount int64
		for _, stack := range after.inventory.Slots {
			if stack.ItemID == cmd.Reward.Stack.ItemID {
				inventoryRewardCount += stack.Count
			}
		}
		if inventoryRewardCount != cmd.Reward.Stack.Count {
			t.Fatalf("accepted entitlement inventory reward count=%d, want %d", inventoryRewardCount, cmd.Reward.Stack.Count)
		}
	})
}
