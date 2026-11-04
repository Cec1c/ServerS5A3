package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type GuildDungeonKillCommand struct {
	Clear     *GuildDungeonClear
	Target    string // coordinate/map/original hostile index, never client supplied
	Total     uint32 // frozen number of eligible preplaced monsters
	UpdatedAt time.Time
}

type GuildDungeonKillResult struct {
	Revision int64
	Fraction uint32
	Reward   guild.DungeonRepeatRewardResult
	Slots    []int16
	MailID   string
	Replayed bool
}

type guildDungeonKillReceipt struct {
	Version int
	Entry   guild.DungeonEntry
	Total   uint32
	Kills   map[string]GuildDungeonKillResult
}

func (o *Owner) CommitGuildDungeonKill(ctx context.Context, cmd GuildDungeonKillCommand) (GuildDungeonKillResult, error) {
	var result GuildDungeonKillResult
	if o == nil || cmd.Clear == nil || cmd.Clear.Catalog == nil {
		return result, ErrOwnerUnavailable
	}
	clear := cmd.Clear
	if !clear.Catalog.HasRepeatReward() {
		return result, nil
	}
	if err := clear.Catalog.ValidateRepeatRewardEntry(clear.Entry); err != nil {
		return result, err
	}
	if cmd.Target == "" || len(cmd.Target) > 128 || cmd.Total == 0 || cmd.Total > 10000 {
		return result, ErrRewardInvalid
	}
	u, ok := o.settlements.(r.GuildDungeonKillUnitOfWork)
	if !ok {
		return result, ErrOwnerUnavailable
	}
	ctx, now := contextOrBackground(ctx), updatedAtOrNow(cmd.UpdatedAt)
	entry := clear.Entry
	err := u.WithinGuildDungeonKill(ctx, entry.Owner, entry.CompletionKey(), func(tx r.Group) error {
		if tx.Character == nil || tx.Inventory == nil || tx.Settings == nil {
			return ErrOwnerUnavailable
		}
		character, found, err := tx.Character.Load(ctx, entry.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != entry.Owner.AccountID || character.CharacterID != entry.Owner.CharacterID {
			return ErrSettlementStateInvalid
		}
		scope := r.GuildDungeonKillReceiptScope(entry.Owner, entry.CompletionKey())
		record, found, err := tx.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		receipt := guildDungeonKillReceipt{Version: 1, Entry: entry, Total: cmd.Total, Kills: map[string]GuildDungeonKillResult{}}
		if found {
			if json.Unmarshal([]byte(record.Values["receipt"]), &receipt) != nil || receipt.Version != 1 || receipt.Total != cmd.Total || receipt.Entry.Owner != entry.Owner || receipt.Entry.CompletionKey() != entry.CompletionKey() || receipt.Entry.DungeonID != entry.DungeonID || receipt.Entry.Cycle != entry.Cycle || !receipt.Entry.JoinedAt.Equal(entry.JoinedAt) || receipt.Entry.CatalogDigest != entry.CatalogDigest || receipt.Entry.RepeatRewardPercent != entry.RepeatRewardPercent || receipt.Kills == nil || len(receipt.Kills) > int(cmd.Total) {
				return ErrSettlementStateInvalid
			}
			if previous, ok := receipt.Kills[cmd.Target]; ok {
				result = previous
				result.Replayed = true
				progress, err := guild.DungeonRepeatRewardProgress(character)
				if err != nil {
					return err
				}
				result.Reward = guild.DungeonRepeatRewardResult{Enabled: true, Progress: progress, Display: progress}
				result.Fraction = uint32(character.Stats[guild.DungeonRepeatRewardFractionStat])
				result.Revision = character.Stats[guild.DungeonRepeatRewardRevisionStat]
				return nil
			}
		}
		if len(receipt.Kills) >= int(cmd.Total) {
			return ErrRewardInvalid
		}
		budget, before := uint64(entry.RepeatRewardPercent*guild.DungeonRepeatRewardScale), uint64(len(receipt.Kills))
		units := uint32(budget*(before+1)/uint64(cmd.Total) - budget*before/uint64(cmd.Total))
		character = r.CloneCharacter(character)
		items, reward, err := guild.ApplyDungeonRepeatReward(&character, clear.Catalog, units)
		if err != nil {
			return err
		}
		result.Reward = reward
		revision := character.Stats[guild.DungeonRepeatRewardRevisionStat]
		if revision < 0 || revision == math.MaxInt64 {
			return ErrSettlementStateInvalid
		}
		result.Revision, result.Fraction = revision+1, uint32(character.Stats[guild.DungeonRepeatRewardFractionStat])
		character.Stats[guild.DungeonRepeatRewardRevisionStat] = result.Revision
		if len(items) > 0 {
			inventory, found, err := tx.Inventory.Load(ctx, entry.Owner.CharacterID)
			if err != nil {
				return err
			}
			if !found {
				inventory = r.InventoryRecord{CharacterID: entry.Owner.CharacterID}
			}
			inventory = r.CloneInventory(inventory)
			if err := r.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
				return err
			}
			if inventory.Slots == nil {
				inventory.Slots = map[string]r.ItemStack{}
			}
			for _, item := range items {
				definition, ok := clear.Items[item.ItemID]
				if !ok || !definition.Stackable || definition.Stack.ItemID != int64(item.ItemID) || definition.Stack.Extra["item_kind"] == "avatar" {
					return ErrRewardInvalid
				}
				definition.Stack.Count = int64(item.Count)
				if err := validateCardReward(CardRewardCommand{CharacterID: character.CharacterID, Bundle: CardRewardBundle{Items: []CardItemReward{definition}}, Project: clear.Project}); err != nil {
					return err
				}
				slot, err := AddResolvedRewardToInventory(&inventory, definition, clear.Project)
				if errors.Is(err, ErrInventoryFull) {
					attachment, e := cardRewardMailAttachment(definition, clear.Project)
					if e != nil {
						return e
					}
					result.MailID, err = r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{RecipientCharacterID: character.CharacterID, Title: "公会地下城奖励礼盒", Body: "奖励条已满，礼盒通过邮件补发。", Source: "guild_dungeon_kill_reward", Attachments: []r.MailAttachment{attachment}, CreatedAt: now})
				} else if err == nil {
					result.Slots = append(result.Slots, slot)
				}
				if err != nil {
					return err
				}
			}
			if len(result.Slots) > 0 {
				inventory.UpdatedAt = now
				if err := r.SaveInventoryFields(ctx, tx.Inventory, inventory, r.InventoryFieldSlots); err != nil {
					return err
				}
			}
		}
		character.UpdatedAt = now
		if err := r.SaveCharacterFields(ctx, tx.Character, character, r.CharacterFieldStats); err != nil {
			return err
		}
		receipt.Kills[cmd.Target] = result
		encoded, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": string(encoded)}, UpdatedAt: now})
	})
	if err != nil {
		return GuildDungeonKillResult{}, err
	}
	return result, nil
}
