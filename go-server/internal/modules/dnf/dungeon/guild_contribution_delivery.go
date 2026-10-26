package dungeon

import (
	"context"
	"errors"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// The definition and slot range come from the matched PVF before the asset
// transaction. Only ErrInventoryFull selects mail; projection/storage errors
// roll the complete contribution event back.
func GuildContributionDelivery(settings r.SettingsRepository, definition CardItemReward, project StackProjector) guild.ContributionDeliver {
	return func(ctx context.Context, tx r.Group, actor *r.CharacterRecord, inventory *r.InventoryRecord, amount uint32, completion string, now time.Time) (guild.ContributionDelivery, error) {
		if definition.Stack.ItemID != guild.ContributionCoinID || !definition.Stackable || amount == 0 || actor == nil || inventory == nil || inventory.CharacterID != actor.CharacterID || completion == "" || now.IsZero() {
			return guild.ContributionDelivery{}, ErrRewardInvalid
		}
		candidate := r.CloneInventory(*inventory)
		if err := r.BindInventoryContainerState(ctx, settings, &candidate); err != nil {
			return guild.ContributionDelivery{}, err
		}
		if candidate.Slots == nil {
			candidate.Slots = map[string]r.ItemStack{}
		}
		reward := definition
		reward.Stack.Count = int64(amount)
		slot, err := AddResolvedRewardToInventory(&candidate, reward, project)
		if err == nil {
			candidate.UpdatedAt = now
			if err := r.SaveInventoryFields(ctx, tx.Inventory, candidate, r.InventoryFieldSlots); err != nil {
				return guild.ContributionDelivery{}, err
			}
			*inventory = candidate
			return guild.ContributionDelivery{Slot: slot}, nil
		}
		if !errors.Is(err, ErrInventoryFull) {
			return guild.ContributionDelivery{}, err
		}
		id, err := r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{
			RecipientCharacterID: actor.CharacterID, RecipientName: actor.Name,
			Source: "guild_contribution", Title: "公会贡献币", Body: "任务物品栏已满，本次公会贡献币通过邮件发放，请在15天内领取。",
			CompletionKey: completion, CreatedAt: now, ExpireAt: now.Add(15 * 24 * time.Hour),
			Attachments: []r.MailAttachment{{ItemID: guild.ContributionCoinID, Count: int64(amount), Bind: definition.Stack.Bind, Extra: definition.Stack.Extra}},
		})
		if err != nil {
			return guild.ContributionDelivery{}, err
		}
		return guild.ContributionDelivery{Slot: -1, MailID: id}, nil
	}
}
