package tagtournament

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestLobbyScheduleDailyResetAndRewardProgress(t *testing.T) {
	o, g, _ := ownerFixture(t)
	ctx := context.Background()
	r := admitRun(t, o, Normal)
	account, _, _ := g.Account.Load(ctx, "a")
	day, err := o.loadDay(account, o.now())
	if err != nil {
		t.Fatal(err)
	}
	day.NormalClaims = map[uint32]bool{r.admission.plan.Encounters[0].MonsterID: true, r.admission.plan.Encounters[1].MonsterID: true}
	view, err := o.catalog.LobbySchedule(o.now(), day)
	if err != nil || !view.Paid || view.LockedMode != Normal || view.RewardedRounds != 2 || len(view.Normal.Encounters) != 5 || len(view.Hard.Encounters) != 7 {
		t.Fatal(view, err)
	}
	next, err := o.catalog.LobbySchedule(o.now().Add(24*time.Hour), day)
	if err != nil || next.Paid || next.LockedMode != 0 || next.RewardedRounds != 0 || next.Normal.ServiceDay == view.Normal.ServiceDay {
		t.Fatal(next, err)
	}
	for _, scenario := range []string{"跳过首回合", "未知怪物", "false标记", "资源改变", "超前日期", "普通混困难"} {
		t.Run(scenario, func(t *testing.T) {
			bad := cloneDay(day)
			switch scenario {
			case "跳过首回合":
				delete(bad.NormalClaims, r.admission.plan.Encounters[0].MonsterID)
			case "未知怪物":
				bad.NormalClaims[99999] = true
			case "false标记":
				bad.NormalClaims[99999] = false
			case "资源改变":
				bad.ConfigHash = "旧资源"
			case "超前日期":
				bad.ServiceDay++
			case "普通混困难":
				bad.HardCleared = 1
			}
			if _, err := o.catalog.LobbySchedule(o.now(), bad); err == nil {
				t.Fatal("接受损坏的界面账本")
			}
		})
	}
	day.Mode = Hard
	day.NormalClaims = nil
	day.HardCleared = 7
	if view, err = o.catalog.LobbySchedule(o.now(), day); err != nil || view.RewardedRounds != 7 || view.LockedMode != Hard {
		t.Fatal(view, err)
	}
}

func TestPreviewEntryDoesNotChargeLockOrReplaceRun(t *testing.T) {
	o, g, _ := ownerFixture(t)
	ctx := context.Background()
	before, _, _ := g.Account.Load(ctx, "a")
	first, _, _ := g.Character.Load(ctx, "1")
	for _, mode := range []Mode{Normal, Hard, Normal} {
		p, err := o.PreviewEntry(ctx, "a", "1", mode)
		if err != nil || p.Fee != 120000 || p.Plan.Mode != mode {
			t.Fatal(p, err)
		}
	}
	after, _, _ := g.Account.Load(ctx, "a")
	ch, _, _ := g.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(first, ch) || len(mailTotals(t, g)) != 0 {
		t.Fatal("预览修改账号、金币或邮件")
	}
	r := admitRun(t, o, Hard)
	before, _, _ = g.Account.Load(ctx, "a")
	for _, current := range []string{"1", "2"} {
		p, err := o.PreviewEntry(ctx, "a", current, Hard)
		if err != nil || p.Fee != 0 || p.Plan.ServiceDay != r.admission.plan.ServiceDay {
			t.Fatal(p, err)
		}
	}
	if _, err := o.PreviewEntry(ctx, "a", "1", Normal); err == nil {
		t.Fatal("已付费仍允许其他模式")
	}
	if _, err := o.PreviewEntry(ctx, "a", "不存在", Hard); err == nil {
		t.Fatal("非队员通过预览")
	}
	after, _, _ = g.Account.Load(ctx, "a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("预览替换了有效挑战凭据")
	}
}
