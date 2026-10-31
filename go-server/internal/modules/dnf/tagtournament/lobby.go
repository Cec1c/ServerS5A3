package tagtournament

import (
	"context"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
)

// LobbySchedule 同时提供两种难度的今日名单，切换预览不重抽、不支付。
// 已领奖进度只从当日账号账本取得，不接受客户端回合数。
type LobbySchedule struct {
	Normal, Hard   DailyPlan
	Paid           bool
	LockedMode     Mode
	RewardedRounds int
}

func (c *Catalog) LobbySchedule(now time.Time, day DayState) (LobbySchedule, error) {
	var view LobbySchedule
	if c == nil || now.IsZero() || day.ServiceDay > premium.ServiceDayID(now) {
		return view, fmt.Errorf("帝国竞技场休息室时间或资源无效")
	}
	var err error
	if view.Normal, err = c.DailyPlan(now, Normal); err != nil {
		return view, err
	}
	if view.Hard, err = c.DailyPlan(now, Hard); err != nil {
		return view, err
	}
	if day.ServiceDay < view.Normal.ServiceDay {
		return view, nil
	}
	if !day.Paid {
		if day.Mode != 0 || day.ConfigHash != "" || len(day.NormalClaims) != 0 || day.HardCleared != 0 {
			return LobbySchedule{}, fmt.Errorf("帝国竞技场未支付账本含挑战进度")
		}
		return view, nil
	}
	if day.AccountID == "" || day.ConfigHash != view.Normal.ConfigHash || day.Mode != Normal && day.Mode != Hard {
		return LobbySchedule{}, fmt.Errorf("帝国竞技场休息室支付模式或资源账本不符")
	}
	view.Paid, view.LockedMode = true, day.Mode
	if day.Mode == Hard {
		if len(day.NormalClaims) != 0 || day.HardCleared < 0 || day.HardCleared > len(view.Hard.Encounters) {
			return LobbySchedule{}, fmt.Errorf("帝国竞技场困难领奖进度无效")
		}
		view.RewardedRounds = day.HardCleared
		return view, nil
	}
	if day.HardCleared != 0 {
		return LobbySchedule{}, fmt.Errorf("帝国竞技场普通账本含困难进度")
	}
	// 现用界面按已领奖回合的前缀覆盖灰色遮罩。非连续记录不能转换成总数，
	// 否则会把尚未领奖的怪物错误置灰；保留原账本并拒绝该投影。
	gap := false
	for _, encounter := range view.Normal.Encounters {
		if day.NormalClaims[encounter.MonsterID] {
			if gap {
				return LobbySchedule{}, fmt.Errorf("帝国竞技场普通领奖记录不是连续回合")
			}
			view.RewardedRounds++
		} else {
			gap = true
		}
	}
	if len(day.NormalClaims) != view.RewardedRounds {
		return LobbySchedule{}, fmt.Errorf("帝国竞技场领奖记录含非当日怪物或无效标记")
	}
	return view, nil
}

// EntryPreview 只读检查注册队伍、等级、金币、服务日及支付模式；不能用于发奖
// 或创建Run。真正入场仍必须通过Admit的账号双角色邮箱事务重新检查并提交。
type EntryPreview struct {
	Fee  uint32
	Plan DailyPlan
}

func (o *Owner) PreviewEntry(ctx context.Context, accountID, current string, mode Mode) (EntryPreview, error) {
	view, err := o.Lobby(ctx, accountID)
	if err != nil {
		return EntryPreview{}, err
	}
	if !view.Registered {
		return EntryPreview{}, fmt.Errorf("帝国竞技场尚未注册两名队员")
	}
	payer := -1
	for i, ch := range view.Characters {
		if ch.CharacterID == current {
			payer = i
		}
	}
	if payer < 0 || view.Characters[payer].Stats["gold"] < 0 {
		return EntryPreview{}, fmt.Errorf("帝国竞技场当前角色不在已注册队伍或金币无效")
	}
	quote, err := o.catalog.QuoteEntry(o.now(), mode, accountID, view.Characters, uint64(view.Characters[payer].Stats["gold"]), view.Day)
	if err != nil {
		return EntryPreview{}, err
	}
	return EntryPreview{Fee: quote.Fee, Plan: quote.Plan}, nil
}
