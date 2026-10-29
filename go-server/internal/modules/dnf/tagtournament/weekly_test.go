package tagtournament

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func weeklyPVFFixture(t *testing.T) (*Owner, repo.Group, *time.Time) {
	t.Helper()
	o, g, now := ownerFixture(t)
	source := catalogSource(t)
	source[ConfigPath] = strings.ReplaceAll(strings.ReplaceAll(source[ConfigPath], "20000 240000", "20000 13000"), "102 1668 1669 15", "777 1668 1669 15")
	source["stackable/stackable.lst"] += "777 `777.pvf`\n"
	source["stackable/777.pvf"] = strings.ReplaceAll(strings.Replace(source["stackable/102.pvf"], "[usable period] 14", "[usable period] 3", 1), "`[period]` 14", "`[period]` 9")
	var err error
	o.catalog, err = LoadCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return o, g, now
}

func TestWeeklyClaimReadsPVFAndIsOncePerAccountAcrossCharactersAndRestart(t *testing.T) {
	o, g, now := weeklyPVFFixture(t)
	ctx := context.Background()
	if _, err := o.ClaimWeekly(ctx, "a", "1"); err == nil {
		t.Fatal("没有通关也可领周奖")
	}
	r := completedRecordRun(t, o, now, time.Second)
	if _, err := o.RecordCompletion(ctx, r); err != nil {
		t.Fatal(err)
	}
	state, err := o.Weekly(ctx, "a")
	if err != nil || state.TargetTimeMS != 13000 || !state.CanClaim || state.Claimed {
		t.Fatal(state, err)
	}
	accountBefore, _, _ := g.Account.Load(ctx, "a")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "1"
			if i%2 == 0 {
				id = "2"
			}
			if _, err := o.ClaimWeekly(ctx, "a", id); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := mailTotals(t, g); got[777] != 1 || got[102] != 0 || got[100] != 27 || got[101] != 30 {
		t.Fatal("并发跨角色重复发奖或奖励写死", got)
	}
	accountAfter, _, _ := g.Account.Load(ctx, "a")
	for _, key := range []string{recordsKey, dayKey, rosterKey, "无关账号项"} {
		if accountBefore.Metadata[key] != accountAfter.Metadata[key] {
			t.Fatal("领取改变其它账本", key)
		}
	}
	for _, id := range []string{"1", "2"} {
		box, _, _ := g.Mailbox.Load(ctx, id)
		for _, m := range box.Mails {
			if m.Metadata["source"] == "tag_tournament_weekly" {
				if len(m.Attachments) != 1 || m.Attachments[0].Count != 1 || !m.Attachments[0].Bind || !m.Attachments[0].ExpireAt.Equal(now.Add(3*24*time.Hour)) ||
					!strings.Contains(m.Body, "3天") || !strings.Contains(m.Body, "9天") {
					t.Fatal("相对有效期未使用PVF", m)
				}
			}
		}
	}
	reopened, _ := NewOwnerWithClock(o.catalog, g, o.now)
	*now = now.Add(24 * time.Hour)
	state, err = reopened.Weekly(ctx, "a")
	if err != nil || !state.Claimed || state.CanClaim {
		t.Fatal("重建或每日重置恢复了已领额度", state, err)
	}
	*now = time.Unix(RecordWeek(*now), 0).AddDate(0, 0, 7)
	state, err = reopened.Weekly(ctx, "a")
	if err != nil || state.CanClaim || state.Claimed || state.Best != nil {
		t.Fatal("周四六点未重置或旧成绩可领新奖", state, err)
	}
	r = completedRecordRun(t, reopened, now, time.Second)
	if _, err = reopened.RecordCompletion(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.ClaimWeekly(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if got := mailTotals(t, g); got[777] != 2 {
		t.Fatal("新周达标后不能领新奖励", got)
	}
}

func TestWeeklyClaimRejectsSlowIncompleteForeignAndCorruptRecords(t *testing.T) {
	for _, scenario := range []string{"超时", "未通关", "其他账号角色", "已删除角色", "损坏领奖回执", "未来成绩", "旧周成绩", "普通成绩"} {
		t.Run(scenario, func(t *testing.T) {
			o, g, now := weeklyPVFFixture(t)
			ctx := context.Background()
			step := time.Second
			if scenario == "超时" {
				step = 2 * time.Second
			}
			if scenario != "未通关" {
				r := completedRecordRun(t, o, now, step)
				if _, err := o.RecordCompletion(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "其他账号角色":
				ch, _, _ := g.Character.Load(ctx, "2")
				ch.AccountID = "b"
				_ = g.Character.Save(ctx, ch)
			case "已删除角色":
				ch, _, _ := g.Character.Load(ctx, "2")
				ch.Stats["delete_flag"] = 1
				_ = g.Character.Save(ctx, ch)
			case "损坏领奖回执":
				a, _, _ := g.Account.Load(ctx, "a")
				a.Metadata[weeklyClaimKey] = `{"week":1}`
				_ = g.Account.Save(ctx, a)
			case "未来成绩":
				*now = now.Add(-time.Minute)
			case "旧周成绩":
				*now = now.Add(7 * 24 * time.Hour)
			case "普通成绩":
				a, _, _ := g.Account.Load(ctx, "a")
				l, err := loadRecordLedger(a)
				if err != nil {
					t.Fatal(err)
				}
				l.Weeks[RecordWeek(*now)].Mode = Normal
				_ = saveMetadata(ctx, g.Account, a, recordsKey, l, *now)
			}
			a, _, _ := g.Account.Load(ctx, "a")
			m, _, _ := g.Mailbox.Load(ctx, "2")
			if id, err := o.ClaimWeekly(ctx, "a", "2"); err == nil || id != "" {
				t.Fatal("非法领取成功", id, err)
			}
			after, _, _ := g.Account.Load(ctx, "a")
			mail, _, _ := g.Mailbox.Load(ctx, "2")
			if !reflect.DeepEqual(a, after) || !reflect.DeepEqual(m, mail) {
				t.Fatal("拒绝领取改变存档")
			}
		})
	}
}

func TestWeeklyClaimTransactionFailureAndThursdayBoundary(t *testing.T) {
	o, g, now := weeklyPVFFixture(t)
	ctx := context.Background()
	*now = time.Date(2026, 10, 22, 5, 59, 40, 0, recordZone)
	r := completedRecordRun(t, o, now, time.Second)
	if _, err := o.RecordCompletion(ctx, r); err != nil {
		t.Fatal(err)
	}
	base := o.tx
	failure := errors.New("模拟周奖邮件写入后事务失败")
	o.tx = faultTournamentTx{base: base, after: failure}
	if id, err := o.ClaimWeekly(ctx, "a", "2"); !errors.Is(err, failure) || id != "" {
		t.Fatal(id, err)
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if a.Metadata[weeklyClaimKey] != "" || mailTotals(t, g)[777] != 0 {
		t.Fatal("失败遗留邮件或消耗资格")
	}
	o.tx = faultTournamentTx{base: base, before: func() { *now = time.Date(2026, 10, 22, 6, 0, 0, 0, recordZone) }}
	if id, err := o.ClaimWeekly(ctx, "a", "2"); err == nil || id != "" {
		t.Fatal("锁内跨周后仍用旧资格", id, err)
	}
	if mailTotals(t, g)[777] != 0 {
		t.Fatal("跨周拒绝仍发邮件")
	}
}

func TestWeeklyConfigRejectsMalformedRewardAndUsesOptionalAbsence(t *testing.T) {
	for _, change := range []func(testSource){
		func(s testSource) {
			s[ConfigPath] = strings.Replace(s[ConfigPath], "102 1668 1669 15", "0 1668 1669 15", 1)
		},
		func(s testSource) {
			s[ConfigPath] = strings.Replace(s[ConfigPath], "102 1668 1669 15", "102 1668 1669", 1)
		},
		func(s testSource) { s[ConfigPath] = strings.Replace(s[ConfigPath], "20000 240000", "20000 0", 1) },
		func(s testSource) {
			s["stackable/102.pvf"] = strings.Replace(s["stackable/102.pvf"], "[usable period] 14", "[usable period] 0", 1)
		},
		func(s testSource) {
			s["stackable/102.pvf"] = strings.Replace(s["stackable/102.pvf"], "`[account]`", "`[trade]`", 1)
		},
	} {
		s := catalogSource(t)
		change(s)
		if _, err := LoadCatalog(s); err == nil {
			t.Fatal("损坏周奖配置被接受")
		}
	}
}
