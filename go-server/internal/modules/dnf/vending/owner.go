package vending

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

var ErrBlackDiamondRequired = errors.New("black diamond required")

type Command struct {
	AccountID, CharacterID string
	CoinID, CoinCount      int64
	CoinSlot               int16
	Reward                 dungeon.CardItemReward
	Project                dungeon.StackProjector
	// ValidateCoin checks PVF and instance deadlines on the locked source,
	// rather than trusting the bridge's earlier inventory snapshot.
	ValidateCoin func(repo.ItemStack, time.Time) error
	Now          time.Time
	// OverflowToMail keeps a paid draw when the PVF item segment is full. It is
	// intentionally not enabled by CMD215: that client resolves the settlement
	// slot as a real inventory key, so a mail-only reward cannot reuse slot zero.
	OverflowToMail bool
}
type Result struct {
	CoinRemaining int64
	RewardSlot    int16
	// RewardStack is the committed slot total, including an existing stack.
	// For an overflow delivery it is the reward snapshot represented by the
	// settlement row, while RewardSlot is -1 because no inventory slot exists.
	RewardStack repo.ItemStack
	// OverflowMailID is set only when the complete reward was delivered by
	// system mail because AddResolvedRewardToInventory could not place it.
	OverflowMailID string
}

// A failed grant rolls back the coin debit. Entitlement, character ownership,
// source quantity and inventory capacity are checked against real records.
func Use(ctx context.Context, repos repo.Group, cmd Command) (Result, error) {
	if (repos.CharacterAssets == nil && repos.MailboxAssets == nil) || repos.Account == nil || cmd.CoinID <= 0 || cmd.CoinCount <= 0 || cmd.CoinSlot < 0 || cmd.Now.IsZero() {
		return Result{}, fmt.Errorf("vending dependencies or command invalid")
	}
	var result Result
	apply := func(characters repo.CharacterRepository, items repo.InventoryRepository, mailboxes repo.MailboxRepository) error {
		account, found, e := repos.Account.Load(ctx, cmd.AccountID)
		if e != nil {
			return e
		}
		if !found || !premium.Active(account, premium.TypeBlackDiamond, cmd.Now) {
			return ErrBlackDiamondRequired
		}
		character, found, e := characters.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found || character.AccountID != cmd.AccountID {
			return fmt.Errorf("vending character ownership invalid")
		}
		inventory, found, e := items.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("vending inventory missing")
		}
		if err := repo.BindInventoryContainerState(ctx, repos.Settings, &inventory); err != nil {
			return err
		}
		inventory = repo.CloneInventory(inventory)
		key := "0:" + strconv.Itoa(int(cmd.CoinSlot))
		coin, found := inventory.Slots[key]
		if !found || coin.ItemID != cmd.CoinID || coin.Count < cmd.CoinCount {
			return fmt.Errorf("vending coin unavailable")
		}
		if cmd.ValidateCoin != nil {
			if e := cmd.ValidateCoin(coin, cmd.Now); e != nil {
				return e
			}
		} else if !coin.ExpireAt.IsZero() && !cmd.Now.Before(coin.ExpireAt) {
			return fmt.Errorf("vending coin expired")
		}
		coin.Count -= cmd.CoinCount
		result.CoinRemaining = coin.Count
		if coin.Count == 0 {
			delete(inventory.Slots, key)
		} else {
			inventory.Slots[key] = coin
		}
		slot, e := dungeon.AddResolvedRewardToInventory(&inventory, cmd.Reward, cmd.Project)
		if e != nil {
			if !errors.Is(e, dungeon.ErrInventoryFull) || !cmd.OverflowToMail || mailboxes == nil {
				return e
			}
			stack, stackErr := projectVendingReward(cmd.Reward, cmd.Project)
			if stackErr != nil {
				return stackErr
			}
			mailID, mailErr := repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{
				RecipientCharacterID: cmd.CharacterID,
				Title:                "背包已满：黑钻售货机奖励",
				Body:                 "对应道具分页已满，本次售货机奖励已通过邮件发送。请清理空间后领取。",
				Source:               "black_diamond_vending_inventory_full",
				CreatedAt:            cmd.Now,
				Attachments: []repo.MailAttachment{{
					ItemID: stack.ItemID, Count: stack.Count, Bind: stack.Bind,
					ExpireAt: stack.ExpireAt, RawEntry: stack.RawEntry, Extra: stack.Extra,
				}},
			})
			if mailErr != nil {
				return mailErr
			}
			result.RewardSlot = -1
			result.RewardStack = stack
			result.OverflowMailID = mailID
		} else {
			if slot < 0 {
				return fmt.Errorf("vending reward slot invalid")
			}
			result.RewardSlot = slot
			result.RewardStack = inventory.Slots["0:"+strconv.Itoa(int(slot))]
		}
		inventory.UpdatedAt = cmd.Now
		return repo.SaveInventoryFields(ctx, items, inventory, repo.InventoryFieldSlots)
	}
	var err error
	if cmd.OverflowToMail && repos.MailboxAssets != nil {
		err = repos.MailboxAssets.WithinMailboxAssets(ctx, cmd.CharacterID, cmd.CharacterID, apply)
	} else if repos.CharacterAssets != nil {
		err = repos.CharacterAssets.WithinCharacterAssets(ctx, cmd.CharacterID, func(characters repo.CharacterRepository, items repo.InventoryRepository, _ repo.EquipmentRepository) error {
			return apply(characters, items, nil)
		})
	} else {
		err = fmt.Errorf("vending mailbox transaction unavailable")
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func projectVendingReward(reward dungeon.CardItemReward, project dungeon.StackProjector) (repo.ItemStack, error) {
	stack := reward.Stack
	stack.RawEntry = append([]byte(nil), stack.RawEntry...)
	if len(stack.Extra) > 0 {
		extra := make(map[string]string, len(stack.Extra))
		for key, value := range stack.Extra {
			extra[key] = value
		}
		stack.Extra = extra
	}
	if reward.ExpireAt.IsZero() {
		return stack, nil
	}
	if project == nil {
		return repo.ItemStack{}, fmt.Errorf("vending reward projector required")
	}
	return project(stack, reward.ExpireAt)
}
