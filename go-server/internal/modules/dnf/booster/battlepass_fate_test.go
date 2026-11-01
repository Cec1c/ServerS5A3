package booster

import (
	"context"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestBattlePassFateBoxOnlyActualGainAndAtomicFailure(t *testing.T) {
	g := newOwnerTestRepositories(t, "dnf:1")
	owner, _ := NewOwner(g)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	project := func(a *Assets) (Changes, error) {
		if a.Inventory.Slots["0:5"].Count <= 0 {
			return Changes{}, errors.New("没有礼盒")
		}
		delete(a.Inventory.Slots, "0:5")
		a.Inventory.Slots["0:121"] = repo.ItemStack{ItemID: repo.BattlePassFateItemID, Count: 80}
		return Changes{Inventory: true}, nil
	}
	cmd := Command{AccountID: "dnf:1", CharacterID: "77", UpdatedAt: now, Project: func(a *Assets) (Changes, error) {
		_, _ = project(a)
		return Changes{}, errors.New("注入产物失败")
	}}
	if err := owner.Open(ctx, cmd); err == nil {
		t.Fatal("失败未拒绝")
	}
	key := fmt.Sprintf("%s%s:43:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))
	ch, _, _ := g.Character.Load(ctx, "77")
	if ch.Stats[key] != 0 {
		t.Fatal("失败礼盒产生计数")
	}
	cmd.Project = project
	if err := owner.Open(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if err := owner.Open(ctx, cmd); err == nil {
		t.Fatal("重复消费礼盒未拒绝")
	}
	ch, _, _ = g.Character.Load(ctx, "77")
	if ch.Stats[key] != 80 {
		t.Fatal("应按宿命之恒数量而非礼盒数量计数", ch.Stats)
	}
}
