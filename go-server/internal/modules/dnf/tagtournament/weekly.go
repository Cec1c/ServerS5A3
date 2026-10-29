package tagtournament

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const weeklyClaimKey = "tag_tournament_weekly_claim_v1"

// WeeklyReward的道具、相对有效期及字体效果均经当前PVF交叉校验。
// 一次领取发放一件该字体；time attack reward后三项不是附件数量。
type WeeklyReward struct {
	Resource
	UsablePeriodDays int64
	FontPeriodDays   int64
}

func loadWeeklyReward(source dnfpvf.Source, resource Resource) (WeeklyReward, error) {
	doc, err := readDocument(source, resource.Path)
	if err != nil {
		return WeeklyReward{}, err
	}
	action, _ := doc.Text("action type")
	bind, _ := doc.Text("attach type")
	period, _ := doc.Int("usable period")
	expiry, _ := doc.Section("expiration info")
	if action != "[add damage font skin]" || bind != "[account]" || period <= 0 || period > math.MaxInt32/86400 ||
		len(expiry) != 2 || expiry[0].Value != "[period]" || expiry[1].Kind != dnfpvf.TokenInt || expiry[1].Int <= 0 || expiry[1].Int > math.MaxInt32/86400 {
		return WeeklyReward{}, fmt.Errorf("帝国竞技场周挑战字体或有效期配置无效")
	}
	return WeeklyReward{Resource: resource, UsablePeriodDays: period, FontPeriodDays: expiry[1].Int}, nil
}

func (c *Catalog) WeeklyChallengeTimeMS() uint32 {
	if c == nil {
		return 0
	}
	return c.config.HardChallengeTimeMS
}

func (c *Catalog) WeeklyReward() WeeklyReward {
	if c == nil {
		return WeeklyReward{}
	}
	return c.weeklyReward
}

type WeeklySnapshot struct {
	TargetTimeMS uint32
	Best         *ClearRecord
	Claimed      bool
	CanClaim     bool
}

type weeklyClaimReceipt struct {
	Week        int64     `json:"week"`
	CharacterID string    `json:"character_id"`
	MailID      string    `json:"mail_id"`
	ItemID      uint32    `json:"item_id"`
	ClaimedAt   time.Time `json:"claimed_at"`
}

func (o *Owner) weeklySnapshot(account repo.AccountRecord, now time.Time) (WeeklySnapshot, error) {
	l, err := loadRecordLedger(account)
	if err != nil {
		return WeeklySnapshot{}, err
	}
	week := RecordWeek(now)
	state := WeeklySnapshot{TargetTimeMS: o.catalog.WeeklyChallengeTimeMS(), Best: l.Weeks[week]}
	if raw, exists := account.Metadata[weeklyClaimKey]; exists {
		var claim weeklyClaimReceipt
		if err = decodeMetadata(raw, &claim); err != nil {
			return WeeklySnapshot{}, err
		}
		if claim.Week <= 0 || claim.ClaimedAt.IsZero() || claim.ClaimedAt.After(now) || RecordWeek(claim.ClaimedAt) != claim.Week ||
			claim.Week > week || strings.TrimSpace(claim.CharacterID) == "" || claim.MailID == "" || claim.ItemID == 0 {
			return WeeklySnapshot{}, fmt.Errorf("帝国竞技场周挑战领奖回执损坏")
		}
		state.Claimed = claim.Week == week
	}
	state.CanClaim = !state.Claimed && o.catalog.weeklyReward.ID != 0 && state.TargetTimeMS != 0 &&
		state.Best != nil && !state.Best.FinishedAt.After(now) && state.Best.ElapsedMS <= state.TargetTimeMS
	return state, nil
}

// Weekly按一次账号读取投影本周成绩与领取状态，不依赖当前出战阵容。
func (o *Owner) Weekly(ctx context.Context, accountID string) (WeeklySnapshot, error) {
	if o == nil || strings.TrimSpace(accountID) == "" {
		return WeeklySnapshot{}, fmt.Errorf("帝国竞技场周挑战缺少账号")
	}
	a, found, err := o.repositories.Account.Load(ctx, accountID)
	if err != nil {
		return WeeklySnapshot{}, err
	}
	if !found || a.AccountID != accountID {
		return WeeklySnapshot{}, fmt.Errorf("帝国竞技场周挑战账号不存在")
	}
	return o.weeklySnapshot(a, o.now())
}

// ClaimWeekly在账号、当前收件角色和邮箱同一事务内复核本周真实完整通关成绩。
// 先落邮件再落账号回执，任一步失败均回滚；改角色、换日、换配置不重开周额度。
func (o *Owner) ClaimWeekly(ctx context.Context, accountID, characterID string) (string, error) {
	if o == nil || accountID == "" || characterID == "" || accountID != strings.TrimSpace(accountID) || characterID != strings.TrimSpace(characterID) {
		return "", fmt.Errorf("帝国竞技场周挑战领取身份无效")
	}
	var mailID string
	err := o.tx.WithinAccountTradeMailbox(ctx, accountID, []string{characterID}, characterID, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, mailboxes repo.MailboxRepository) error {
		now := o.now()
		if now.IsZero() || now.Unix() <= 0 || now.Unix() > math.MaxUint32 {
			return fmt.Errorf("帝国竞技场周挑战领取时间无效")
		}
		a, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || a.AccountID != accountID {
			return fmt.Errorf("帝国竞技场周挑战账号不存在")
		}
		ch, found, err := chars.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || ch.CharacterID != characterID || ch.AccountID != accountID || ch.Slot < 0 || ch.Stats["delete_flag"] != 0 || ch.Stats["delete_time"] != 0 {
			return fmt.Errorf("帝国竞技场周挑战收件角色不存在或不属于账号")
		}
		state, err := o.weeklySnapshot(a, now)
		if err != nil {
			return err
		}
		if state.Claimed {
			return nil
		}
		if !state.CanClaim {
			return fmt.Errorf("帝国竞技场本周尚未在规定时间内通关困难模式")
		}
		reward := o.catalog.weeklyReward
		deadline := now.UTC().Add(time.Duration(reward.UsablePeriodDays) * 24 * time.Hour)
		if deadline.Unix() > math.MaxUint32 {
			return fmt.Errorf("帝国竞技场周挑战道具有效期溢出")
		}
		week := RecordWeek(now)
		mailID, err = repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{
			RecipientCharacterID: characterID, RecipientName: ch.Name,
			Title:  "帝国竞技场每周挑战奖励",
			Body:   fmt.Sprintf("您已在本周规定时间内通关帝国竞技场困难模式。\n奖励：%s。道具有效期%d天，使用后字体有效期%d天。\n请查收附件。", reward.Name, reward.UsablePeriodDays, reward.FontPeriodDays),
			Source: "tag_tournament_weekly", CompletionKey: fmt.Sprintf("tag/weekly/%s/%d", accountID, week), CreatedAt: now,
			Attachments: []repo.MailAttachment{{ItemID: int64(reward.ID), Count: 1, Bind: true, ExpireAt: deadline}},
		})
		if err != nil {
			return err
		}
		return saveMetadata(ctx, accounts, a, weeklyClaimKey, weeklyClaimReceipt{Week: week, CharacterID: characterID, MailID: mailID, ItemID: reward.ID, ClaimedAt: now.UTC()}, now)
	})
	if err != nil {
		return "", err
	}
	return mailID, nil
}
