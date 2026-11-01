package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

const ActivityColorless = "colorless_spent"
const ActivityGoldSpent = "gold_spent"
const ColorlessCubeItemID int64 = 3037

// 金币榜只合计不可往返买卖的养成费用；NPC购买仍在原账本单列。
func ActivityRankSourceMetrics(metric string) []string {
	if metric == ActivityGoldSpent {
		return []string{ActivityGoldReinforce, ActivityGoldAmplify, ActivityGoldForge, ActivityGoldRepair, ActivityGoldEnchant}
	}
	return []string{metric}
}

func ActivityRankUsesMetric(board, actual string) bool {
	if board != ActivityGoldSpent {
		return board == actual
	}
	switch actual {
	case ActivityGoldReinforce, ActivityGoldAmplify, ActivityGoldForge, ActivityGoldRepair, ActivityGoldEnchant:
		return true
	}
	return false
}

func NewColorlessActivity(ch CharacterRecord, id string, amount int64, source string, at time.Time, dungeon int64) (ActivityEvent, error) {
	if id == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return ActivityEvent{}, err
		}
		id = "colorless:" + hex.EncodeToString(nonce[:])
	}
	e := ActivityEvent{ID: id, AccountID: ch.AccountID, CharacterID: ch.CharacterID, CharacterName: ch.Name, Metric: ActivityColorless, Amount: amount, OccurredAt: at, DungeonID: dungeon, ItemID: uint32(ColorlessCubeItemID), Source: source}
	return e, e.Validate()
}

// 必须在原扣除事务内调用；移仓、邮寄、交易和出售都不调用此入口。
func RecordCharacterColorless(ctx context.Context, characters CharacterRepository, ch CharacterRecord, amount int64, source string, at time.Time) error {
	if amount == 0 || ch.AccountID == "" {
		return nil
	}
	e, err := NewColorlessActivity(ch, "", amount, source, at, 0)
	if err != nil {
		return err
	}
	_, err = RecordCharacterActivity(ctx, characters, e)
	return err
}

// 结算事务直接持有账本时使用此入口，不能换用事务外的全局记录器。
func RecordColorlessActivity(ctx context.Context, activity ActivityRepository, ch CharacterRecord, amount int64, source string, at time.Time) error {
	if amount == 0 {
		return nil
	}
	if activity == nil {
		return ErrActivityTransaction
	}
	e, err := NewColorlessActivity(ch, "", amount, source, at, 0)
	if err != nil {
		return err
	}
	_, err = activity.Record(ctx, e)
	return err
}

// 技能扣量与战令计数共用账号晶块、角色及已学技能事务，不加载角色背包和装备。
type AccountCharacterSkillAssetUnitOfWork interface {
	WithinAccountCharacterSkillAssets(context.Context, string, string, func(AccountInventoryRepository, CharacterRepository, SkillRepository) error) error
}
