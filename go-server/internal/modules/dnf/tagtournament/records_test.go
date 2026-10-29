package tagtournament

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func completedRecordRun(t *testing.T, o *Owner, now *time.Time, step time.Duration) *Run {
	t.Helper()
	r := admitRun(t, o, Hard)
	for i := 0; i < 7; i++ {
		*now = now.Add(step)
		e, _, err := r.BeginNext(*now, false)
		if err != nil {
			t.Fatal(err)
		}
		*now = now.Add(step)
		if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err != nil {
			t.Fatal(err)
		}
		if _, err = o.Settle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestRecordCompletionPersistsAccountBestWithoutRepeatingDailyReward(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	r := completedRecordRun(t, o, now, time.Second)
	first, err := o.RecordCompletion(ctx, r)
	if err != nil || first.Current.ElapsedMS != 13000 || first.Best.ElapsedMS != 13000 || first.Weekly == nil {
		t.Fatal(first, err)
	}
	before, _, _ := g.Account.Load(ctx, "a")
	*now = now.Add(time.Minute)
	again, err := o.RecordCompletion(ctx, r)
	after, _, _ := g.Account.Load(ctx, "a")
	if err != nil || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, after) {
		t.Fatal("同场重试改变计时或重复提交", again, err)
	}
	reopened, _ := NewOwnerWithClock(o.catalog, g, o.now)
	best, weekly, err := reopened.Records(ctx, "a")
	if err != nil || best[1].ElapsedMS != 13000 || weekly.ElapsedMS != 13000 || best[0] != nil {
		t.Fatal("重建Owner丢失成绩或串模式", best, weekly, err)
	}
	slower := completedRecordRun(t, o, now, 2*time.Second)
	result, err := o.RecordCompletion(ctx, slower)
	if err != nil || result.Current.ElapsedMS != 26000 || result.Best.ElapsedMS != 13000 || result.Weekly.ElapsedMS != 13000 {
		t.Fatal("较慢成绩覆盖最快", result, err)
	}
	faster := completedRecordRun(t, o, now, 500*time.Millisecond)
	result, err = o.RecordCompletion(ctx, faster)
	if err != nil || result.Current.ElapsedMS != 6500 || result.Best.ElapsedMS != 6500 || result.Weekly.ElapsedMS != 6500 {
		t.Fatal("已领奖重打未刷新更快成绩", result, err)
	}
	if total := mailTotals(t, g); total[100] != 27 || total[101] != 30 {
		t.Fatal("成绩记录重复发每日材料", total)
	}
	if _, err = o.RecordCompletion(ctx, r); err == nil {
		t.Fatal("旧挑战凭据可覆盖新成绩")
	}
	account, _, _ := g.Account.Load(ctx, "a")
	if account.Metadata["无关账号项"] != "保留" || account.Metadata[dayKey] == "" {
		t.Fatal("记录破坏其他账号账本")
	}
	_ = g.Account.Save(ctx, repo.AccountRecord{AccountID: "other"})
	other, otherWeek, err := o.Records(ctx, "other")
	if err != nil || other != [2]*ClearRecord{} || otherWeek != nil {
		t.Fatal("账号成绩串用", other, otherWeek, err)
	}
}

func TestRecordWeekResetsThursdaySixAndKeepsHistoricalBest(t *testing.T) {
	o, _, now := ownerFixture(t)
	*now = time.Date(2026, 10, 22, 5, 59, 40, 0, recordZone)
	r := completedRecordRun(t, o, now, time.Second)
	if _, err := o.RecordCompletion(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	previous := RecordWeek(*now)
	*now = time.Date(2026, 10, 22, 6, 0, 0, 0, recordZone)
	if RecordWeek(*now) != previous+7*86400 || RecordWeek(now.Add(-time.Nanosecond)) != previous {
		t.Fatal("周四06点周期错误")
	}
	best, weekly, err := o.Records(context.Background(), "a")
	if err != nil || weekly != nil || best[1] == nil || best[1].ElapsedMS != 13000 {
		t.Fatal("周重置删除历史最佳或残留上周个人时间", best, weekly, err)
	}
}

func TestRecordCompletionTransactionRollbackAndMalformedLedger(t *testing.T) {
	o, g, now := ownerFixture(t)
	r := completedRecordRun(t, o, now, time.Second)
	base := o.tx
	failure := errors.New("记录事务提交失败")
	o.tx = faultTournamentTx{base: base, after: failure}
	if result, err := o.RecordCompletion(context.Background(), r); !errors.Is(err, failure) || result.Current.ElapsedMS != 0 {
		t.Fatal("失败返回成功记录", result, err)
	}
	a, _, _ := g.Account.Load(context.Background(), "a")
	if _, exists := a.Metadata[recordsKey]; exists {
		t.Fatal("失败事务留下部分成绩")
	}
	o.tx = base
	if _, err := o.RecordCompletion(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	a, _, _ = g.Account.Load(context.Background(), "a")
	a.Metadata[recordsKey] = `{"weeks":{"1":{"mode":2,"elapsed_ms":0}}}`
	_ = g.Account.Save(context.Background(), a)
	if _, _, err := o.Records(context.Background(), "a"); err == nil {
		t.Fatal("损坏记录被当成空成绩")
	}
}
