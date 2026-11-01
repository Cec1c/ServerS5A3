package tagtournament

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestBattlePassImperialWholeRunAndReplayOnlyOncePerAccount(t *testing.T) {
	o, g, now := ownerFixture(t)
	r := completedRecordRun(t, o, now, time.Second)
	for range 2 {
		if _, err := o.RecordCompletion(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := g.Character.ListByAccount(nil, "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, ch := range rows {
		total += ch.Stats[fmt.Sprintf("%s%s:35:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(*now))]
	}
	if total != 1 {
		t.Fatal("帝国双角色或同场重放重复计数", total)
	}
}
