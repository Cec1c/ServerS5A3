package progression

import (
	"context"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LevelUpRewardDelivery struct {
	LevelUpReward
	CharacterName, MailID string
}
type LevelUpRewardCommand struct {
	CharacterID string
	Now         time.Time
	// 物品定义、期限由持有现用PVF的桥接层提供，领域不凭物品编号猜属性。
	ResolveAttachment func(LevelUpReward) (dnfrepo.MailAttachment, error)
}

// DeliverLevelUpRewards 只消费已持久化事件，邮件、附件和永久审计在同一事务提交。
// 返回值仅含本次实际提交的邮件；失败、重试和删除邮件都不会触发二次播报或发奖。
func DeliverLevelUpRewards(ctx context.Context, repos dnfrepo.Group, catalog *LevelUpRewardCatalog, cmd LevelUpRewardCommand) ([]LevelUpRewardDelivery, error) {
	if repos.MailboxAssets == nil || catalog == nil || strings.TrimSpace(cmd.CharacterID) == "" || cmd.ResolveAttachment == nil {
		return nil, dnfrepo.ErrRepoMissing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := cmd.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Unix() <= 0 {
		return nil, ErrCommitPlanInvalid
	}
	var result []LevelUpRewardDelivery
	err := repos.MailboxAssets.WithinMailboxAssets(ctx, cmd.CharacterID, cmd.CharacterID, func(characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository) error {
		character, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID || character.Stats["delete_flag"] != 0 {
			return nil
		}
		character = dnfrepo.CloneCharacter(character)
		var levels []int
		for key, value := range character.Stats {
			if level, ok := dnfrepo.LevelUpEventLevel(key); ok && value > 0 {
				levels = append(levels, level)
			}
		}
		sort.Ints(levels)
		for _, level := range levels {
			if reward, configured := catalog.Reward(level); configured {
				attachment, err := cmd.ResolveAttachment(reward)
				if err != nil {
					return err
				}
				if attachment.ItemID != reward.ItemID || attachment.Count != reward.Count {
					return dnfrepo.ErrSystemMailInvalid
				}
				source := "level_up_reward:" + strconv.Itoa(level)
				mailID, err := dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Title: "升级奖励", Body: fmt.Sprintf("恭喜达到%d级，请查收升级奖励附件。", level), Source: source, CreatedAt: now, Attachments: []dnfrepo.MailAttachment{attachment}})
				if err != nil {
					return err
				}
				numericMail, err := strconv.ParseInt(mailID, 10, 64)
				if err != nil || numericMail <= 0 {
					return dnfrepo.ErrSystemMailInvalid
				}
				suffix := strconv.Itoa(level)
				character.Stats["level_up_reward_item_"+suffix] = reward.ItemID
				character.Stats["level_up_reward_count_"+suffix] = reward.Count
				character.Stats["level_up_reward_mail_"+suffix] = numericMail
				character.Stats["level_up_reward_delivered_"+suffix] = now.Unix()
				result = append(result, LevelUpRewardDelivery{LevelUpReward: reward, CharacterName: character.Name, MailID: mailID})
			}
			character.Stats[dnfrepo.LevelUpEventKey(level)] = -character.Stats[dnfrepo.LevelUpEventKey(level)]
		}
		if len(levels) == 0 {
			return nil
		}
		character.UpdatedAt = now
		return dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
