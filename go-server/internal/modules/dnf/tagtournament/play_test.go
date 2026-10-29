package tagtournament

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPreparedAdmissionRejectsChangedServiceDayBeforePayment(t *testing.T) {
	o, g, now := ownerFixture(t)
	view, err := o.Lobby(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := o.catalog.DailyPlan(*now, Normal)
	before := view.Characters
	*now = now.Add(24 * time.Hour)
	if _, err = o.AdmitPrepared(context.Background(), "a", "1", view.Characters, plan); err == nil {
		t.Fatal("使用前一天准备的地图扣费入场")
	}
	for i, ch := range before {
		after, _, _ := g.Character.Load(context.Background(), ch.CharacterID)
		if !reflect.DeepEqual(ch, after) {
			t.Fatal("日计划失效仍扣费", i)
		}
	}
}

func TestPreviewNextDoesNotCommitRoomOrRestoreVitals(t *testing.T) {
	o, _, now := ownerFixture(t)
	r := admitRun(t, o, Normal)
	before := r.Snapshot()
	preview, restore, err := r.PreviewNext(*now, false)
	if err != nil || restore || preview.MonsterID == 0 || !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal(preview, restore, err)
	}
	actual, _, err := r.BeginNext(*now, false)
	if err != nil || !reflect.DeepEqual(preview, actual) {
		t.Fatal(actual, err)
	}
}

func TestCloseSettlesConfirmedOldDayOnlyAndCannotContinue(t *testing.T) {
	for _, mode := range []Mode{Normal, Hard} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			o, g, now := ownerFixture(t)
			r := admitRun(t, o, mode)
			clearRound(t, r, now)
			*now = time.Date(2026, 10, 20, 6, 0, 0, 0, beijing)
			if _, err := o.Settle(context.Background(), r); err == nil {
				t.Fatal("未经终止的过期挑战直接结算")
			}
			result, err := o.Close(context.Background(), r)
			if err != nil || len(result.MailIDs) != 1 || r.Snapshot().Phase != Abandoned {
				t.Fatal(result, err)
			}
			before := mailTotals(t, g)
			if repeat, err := o.Close(context.Background(), r); err != nil || len(repeat.MailIDs) != 0 || !reflect.DeepEqual(before, mailTotals(t, g)) {
				t.Fatal("结束重试重复发奖", repeat, err)
			}
			if _, _, err := r.BeginNext(*now, false); err == nil {
				t.Fatal("跨日结束后仍能打下一关")
			}
			if _, err := o.Admit(context.Background(), "a", "1", [2]string{"1", "2"}, mode); err != nil {
				t.Fatal("旧日结算污染新日入场", err)
			}
			if _, err := o.Close(context.Background(), r); err == nil {
				t.Fatal("新凭据建立后旧挑战仍能结算")
			}
		})
	}
}
