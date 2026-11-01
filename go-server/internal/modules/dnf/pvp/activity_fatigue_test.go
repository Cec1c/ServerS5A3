package pvp

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

type activityAbortRental struct{ repo.RentalAssetUnitOfWork }

func (r activityAbortRental) WithinRentalAssets(ctx context.Context, a, c string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return r.RentalAssetUnitOfWork.WithinRentalAssets(ctx, a, c, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository) error {
		if err := fn(ar, cr, ir, er); err != nil {
			return err
		}
		return errors.New("模拟街机提交前失败")
	})
}
func TestActivityArcadeActualAmountRollbackZeroInsufficientAndReplay(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"})
	g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1", Stats: map[string]int64{"fatigue": 50, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(now)}})
	event := repo.ActivityEvent{ID: "arcade:payment", AccountID: "a", CharacterID: "1", Metric: repo.ActivityFatigue, Amount: 40, Source: "arcade", OccurredAt: now}
	cmd := ConsumeArcadeCommand{AccountID: "a", CharacterID: "1", Amount: 40, UpdatedAt: now, Activity: &event}
	base := g.RentalAssets
	g.RentalAssets = activityAbortRental{base}
	if _, err := ConsumeArcadeFatigue(ctx, g, cmd); err == nil {
		t.Fatal("模拟失败未传播")
	}
	g.RentalAssets = base
	s, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || s.Metrics[repo.ActivityFatigue] != 0 {
		t.Fatal(s, err)
	}
	out, err := ConsumeArcadeFatigue(ctx, g, cmd)
	if err != nil || out.Before != 50 || out.After != 10 || out.Consumed != 40 {
		t.Fatal(out, err)
	}
	// 余额已不足再次支付，但同一已提交事件重试仍成功且不再扣除。
	out, err = ConsumeArcadeFatigue(ctx, g, cmd)
	if err != nil || out.After != 10 || out.Consumed != 0 {
		t.Fatal(out, err)
	}
	event.ID = "arcade:insufficient"
	if _, err := ConsumeArcadeFatigue(ctx, g, cmd); !errors.Is(err, ErrFatigueInsufficient) {
		t.Fatal(err)
	}
	cmd.Amount = 0
	if out, err = ConsumeArcadeFatigue(ctx, g, cmd); err != nil || out.After != 10 || out.Consumed != 0 {
		t.Fatal(out, err)
	}
	s, err = g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || s.Metrics[repo.ActivityFatigue] != 40 {
		t.Fatal(s, err)
	}
	events, err := g.Activity.Events(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || len(events) != 1 || events[0].DungeonID != 0 || events[0].Amount != 40 {
		t.Fatal(events, err)
	}
}
