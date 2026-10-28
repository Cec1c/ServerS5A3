package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

// grantCurrentAntonRewardRow keeps the established account-shared placement
// first. Only an ordinary page-full error is converted to transaction-scoped
// mail. A failed partial character placement is restored before the complete
// remaining row is mailed; the account placement is retained in the same UOW.
// Fate rows are account-ledger mutations and never enter bag/overflow-mail
// placement.
func grantCurrentAntonRewardRow(ctx context.Context, a *raid.RewardAssets, catalog *pvfDungeonDropCatalog, reward currentBoosterReward, cmd raid.RewardCommand) ([]string, bool, bool, error) {
	if a == nil || reward.Count == 0 {
		return nil, false, false, raid.ErrRewardReceipt
	}
	if reward.Definition.ItemID == raidcurrency.FateItemID {
		if a.Ledger == nil {
			return nil, false, false, raid.ErrRewardOwnerUnavailable
		}
		granted, _, err := raidcurrency.Credit(a.Ledger, reward.Count, cmd.CompletedAt)
		if err != nil {
			return nil, false, false, err
		}
		a.FateGranted += granted
		// Fate is account ledger data. The bridge projects list44 after commit;
		// it never creates a list-0 mirror or a bag-overflow mail attachment.
		a.LedgerChanged = true
		return nil, false, false, nil
	}
	if a.Inventory == nil || a.AccountInventory == nil || catalog == nil || catalog.source == nil {
		return nil, false, false, raid.ErrRewardReceipt
	}
	remaining := int64(reward.Count)
	accountChanged := false
	if slot, shared := currentDisjointWarehouseFixedSlots[reward.Definition.ItemID]; shared {
		left, err := grantCurrentDisjointWarehouseStack(a.AccountInventory, reward.Definition, slot, remaining)
		if err != nil {
			return nil, false, false, err
		}
		accountChanged = left < remaining
		remaining = left
	}
	if remaining == 0 {
		return nil, accountChanged, false, nil
	}
	row := reward
	row.Count = uint32(remaining)
	before := repo.CloneInventory(*a.Inventory)
	keys, err := grantCurrentBoosterReward(a.Inventory, catalog.source, row)
	if err == nil {
		return keys, accountChanged, false, nil
	}
	if !errors.Is(err, errDungeonPickupInventoryFull) || a.Mailbox == nil {
		return nil, false, false, err
	}
	*a.Inventory = before
	attachments, err := buildCurrentAntonOverflowAttachments(catalog, row, cmd)
	if err != nil {
		return nil, false, false, err
	}
	_, err = repo.AppendSystemMail(ctx, a.Mailbox, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Title: "攻坚奖励（背包已满）", Body: "对应道具分页没有空位，攻坚奖励已随本邮件发送。请清理该分页后领取。", Source: "raid_reward_overflow", CreatedAt: cmd.CompletedAt, Attachments: attachments})
	if err != nil {
		return nil, false, false, err
	}
	return nil, accountChanged, true, nil
}

// Build attachments through the same existing reward instance projector on an
// isolated empty inventory. This preserves its raw entry, bind, expiry, item
// family and equipment quality/durability rather than recreating those rules.
func buildCurrentAntonOverflowAttachments(catalog *pvfDungeonDropCatalog, reward currentBoosterReward, cmd raid.RewardCommand) ([]repo.MailAttachment, error) {
	scratch := repo.InventoryRecord{CharacterID: cmd.CharacterID, Slots: map[string]repo.ItemStack{}}
	keys, err := grantCurrentBoosterReward(&scratch, catalog.source, reward)
	if err != nil {
		return nil, fmt.Errorf("build raid overflow attachment: %w", err)
	}
	sort.Strings(keys)
	out := make([]repo.MailAttachment, 0, len(keys))
	var total int64
	for _, key := range keys {
		stack, ok := scratch.Slots[key]
		if !ok || stack.ItemID != int64(reward.Definition.ItemID) || stack.Count <= 0 {
			return nil, raid.ErrRewardReceipt
		}
		stack = tagCurrentAntonRewardStack(stack, cmd)
		out = append(out, repo.MailAttachment{ItemID: stack.ItemID, Count: stack.Count, Bind: stack.Bind, ExpireAt: reward.Definition.ExpirationDate, RawEntry: append([]byte(nil), stack.RawEntry...), Extra: stack.Extra})
		total += stack.Count
	}
	if total != int64(reward.Count) || len(out) == 0 {
		return nil, raid.ErrRewardReceipt
	}
	return out, nil
}
