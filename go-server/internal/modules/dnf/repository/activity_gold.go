package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"time"
)

const (
	ActivityGoldReinforce = "gold_reinforce_spent"
	ActivityGoldAmplify   = "gold_amplify_spent"
	ActivityGoldForge     = "gold_forge_spent"
	ActivityGoldRepair    = "gold_repair_spent"
	ActivityGoldNPC       = "gold_npc_spent"
	ActivityGoldEnchant   = "gold_enchant_spent"
	ActivityGoldTradeOut  = "gold_trade_out"
	ActivityGoldTradeIn   = "gold_trade_in"
	ActivityGoldTradeFee  = "gold_trade_fee"
	ActivityGoldMailOut   = "gold_mail_out"
	ActivityGoldPostage   = "gold_mail_postage"
)

func GoldActivitySource(metric string) string {
	switch metric {
	case ActivityGoldReinforce:
		return "reinforce"
	case ActivityGoldAmplify:
		return "amplify"
	case ActivityGoldForge:
		return "forge"
	case ActivityGoldRepair:
		return "repair"
	case ActivityGoldNPC:
		return "npc_purchase"
	case ActivityGoldEnchant:
		return "enchant"
	case ActivityGoldTradeOut, ActivityGoldTradeIn, ActivityGoldTradeFee:
		return "player_trade"
	case ActivityGoldMailOut, ActivityGoldPostage:
		return "player_mail"
	default:
		return ""
	}
}

// 消耗与转移分别记账；一笔事件只更新原有累计和服务日两条汇总。
// 这里必须传入原资产事务的记录器，后续写入失败时连同金币一起回滚。
func RecordGoldActivity(ctx context.Context, activity ActivityRepository, ch CharacterRecord, metric string, amount int64, at time.Time) error {
	if amount == 0 {
		return nil
	}
	if activity == nil {
		return ErrActivityTransaction
	}
	e, err := newGoldActivity(ch, metric, amount, at)
	if err != nil {
		return err
	}
	_, err = activity.Record(ctx, e)
	return err
}

func RecordCharacterGold(ctx context.Context, characters CharacterRepository, ch CharacterRecord, metric string, amount int64, at time.Time) error {
	if amount == 0 {
		return nil
	}
	// 旧资产接口允许无账号的独立角色；不能把它们归到公共/默认账号参与排名。
	// 有真实账号的正常角色则必须成功写入同事务账本。
	if ch.AccountID == "" && amount > 0 && GoldActivitySource(metric) != "" {
		return nil
	}
	e, err := newGoldActivity(ch, metric, amount, at)
	if err != nil {
		return err
	}
	_, err = RecordCharacterActivity(ctx, characters, e)
	return err
}

func newGoldActivity(ch CharacterRecord, metric string, amount int64, at time.Time) (ActivityEvent, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ActivityEvent{}, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	e := ActivityEvent{ID: "gold:" + hex.EncodeToString(nonce[:]), AccountID: ch.AccountID, CharacterID: ch.CharacterID, CharacterName: ch.Name, Metric: metric, Amount: amount, OccurredAt: at, Source: GoldActivitySource(metric)}
	return e, e.Validate()
}

type ActivityGoldSummary struct {
	Spent      int64            `json:"spent"`
	Categories map[string]int64 `json:"categories"`
	Transfers  map[string]int64 `json:"transfers"`
	Fees       map[string]int64 `json:"fees"`
}

// 手续费与转移单列，后续金币榜不能误把玩家相互转账当作培养消耗。
func SummarizeActivityGold(metrics map[string]int64) (ActivityGoldSummary, error) {
	out := ActivityGoldSummary{Categories: map[string]int64{}, Transfers: map[string]int64{}, Fees: map[string]int64{}}
	for _, m := range []string{ActivityGoldReinforce, ActivityGoldAmplify, ActivityGoldForge, ActivityGoldRepair, ActivityGoldNPC, ActivityGoldEnchant, ActivityGoldTradeOut, ActivityGoldTradeIn, ActivityGoldTradeFee, ActivityGoldMailOut, ActivityGoldPostage} {
		n := metrics[m]
		if n < 0 {
			return out, ErrActivityInvalid
		}
		switch m {
		case ActivityGoldTradeOut, ActivityGoldTradeIn, ActivityGoldMailOut:
			out.Transfers[m] = n
		case ActivityGoldTradeFee, ActivityGoldPostage:
			out.Fees[m] = n
		default:
			if out.Spent > math.MaxInt64-n {
				return out, ErrActivityInvalid
			}
			out.Spent += n
			out.Categories[GoldActivitySource(m)] = n
		}
	}
	return out, nil
}
