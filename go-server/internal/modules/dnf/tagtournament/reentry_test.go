package tagtournament

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestResumePreparedRequiresPaidDayAndNeverPurchases(t *testing.T) {
	for _, mode := range []Mode{Normal, Hard} {
		o, g, now := ownerFixture(t)
		ctx := context.Background()
		view, _ := o.Lobby(ctx, "a")
		plan, _ := o.catalog.DailyPlan(*now, mode)
		before, _, _ := g.Account.Load(ctx, "a")
		if _, err := o.ResumePrepared(ctx, "a", "1", view.Characters, plan); err == nil {
			t.Fatal("未购买资格的重入被转成收费入场")
		}
		after, _, _ := g.Account.Load(ctx, "a")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("拒绝重入写入了日账本")
		}
		for _, ch := range view.Characters {
			got, _, _ := g.Character.Load(ctx, ch.CharacterID)
			if !reflect.DeepEqual(ch, got) {
				t.Fatal("未支付重入改写金币或角色")
			}
		}
	}
}

func TestResumePreparedRenewsPaidRunWithoutGoldOrDuplicateRewards(t *testing.T) {
	for _, mode := range []Mode{Normal, Hard} {
		o, g, now := ownerFixture(t)
		ctx := context.Background()
		old := admitRun(t, o, mode)
		clearRound(t, old, now)
		if _, err := o.Close(ctx, old); err != nil {
			t.Fatal(err)
		}
		view, _ := o.Lobby(ctx, "a")
		beforeMail := mailTotals(t, g)
		plan, _ := o.catalog.DailyPlan(*now, mode)
		admission, err := o.ResumePrepared(ctx, "a", "1", view.Characters, plan)
		if err != nil || admission.Fee() != 0 {
			t.Fatal("同日同模式不能免费重入", err)
		}
		fresh, err := NewRun(admission, [2]Vitals{{HP: 100, MaxHP: 100}, {HP: 100, MaxHP: 100}})
		if err != nil || o.ValidateRun(ctx, fresh) != nil || o.ValidateRun(ctx, old) == nil {
			t.Fatal("重入没有更新挑战凭据", err)
		}
		clearRound(t, fresh, now)
		if _, err = o.Close(ctx, fresh); err != nil || !reflect.DeepEqual(beforeMail, mailTotals(t, g)) {
			t.Fatal("重入重复领取已确认回合奖励", err)
		}
		for _, ch := range view.Characters {
			got, _, _ := g.Character.Load(ctx, ch.CharacterID)
			if !reflect.DeepEqual(ch, got) {
				t.Fatal("重入重复扣费或改写角色")
			}
		}
	}
}

func TestResumePreparedRechecksDayModeAndRollbackInsideTransaction(t *testing.T) {
	for _, scenario := range []string{"跨06点", "切换模式", "提交失败"} {
		t.Run(scenario, func(t *testing.T) {
			o, g, now := ownerFixture(t)
			ctx := context.Background()
			admitRun(t, o, Normal)
			view, _ := o.Lobby(ctx, "a")
			mode := Normal
			if scenario == "切换模式" {
				mode = Hard
			}
			plan, _ := o.catalog.DailyPlan(*now, mode)
			before, _, _ := g.Account.Load(ctx, "a")
			if scenario == "跨06点" {
				o.tx = faultTournamentTx{base: o.tx, before: func() { *now = now.Add(24 * time.Hour) }}
			}
			if scenario == "提交失败" {
				o.tx = faultTournamentTx{base: o.tx, after: errors.New("隔离重入提交失败")}
			}
			if _, err := o.ResumePrepared(ctx, "a", "1", view.Characters, plan); err == nil {
				t.Fatal("失效资格或失败事务重入成功")
			}
			after, _, _ := g.Account.Load(ctx, "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝重入后日账本被改变")
			}
			for _, ch := range view.Characters {
				got, _, _ := g.Character.Load(ctx, ch.CharacterID)
				if !reflect.DeepEqual(ch, got) {
					t.Fatal("拒绝重入后角色资产被改变")
				}
			}
		})
	}
}
