package characterdata

import (
	"context"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestLevelChallengeCreationAtomicQuota(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Now().UTC()
	c := &onlineevent.LevelChallengeCatalog{Limit: 1, Job: "[all]"}
	build := func(id string) Creation {
		return Creation{LevelChallenge: c, Account: &repo.AccountRecord{AccountID: "a"}, Character: repo.CharacterRecord{CharacterID: id, AccountID: "a", Job: "8", Level: 1, CreatedAt: now}, Inventory: &repo.InventoryRecord{CharacterID: id}}
	}
	if err := NewCreator(r).Create(ctx, build("1")); err != nil {
		t.Fatal(err)
	}
	if err := NewCreator(r).Create(ctx, build("2")); err == nil {
		t.Fatal("第二个预约角色创建成功")
	}
	if _, found, _ := r.Character.Load(ctx, "2"); found {
		t.Fatal("拒绝后残留角色")
	}
	if _, found, _ := r.Inventory.Load(ctx, "2"); found {
		t.Fatal("拒绝后残留背包")
	}
	a, _, _ := r.Account.Load(ctx, "a")
	state, err := onlineevent.ReadLevelChallengeState(a)
	if err != nil || len(state) != 1 {
		t.Fatalf("名额=%v err=%v", state, err)
	}
	ordinary := build("3")
	ordinary.LevelChallenge = nil
	ordinary.Character.Slot = 1
	if err := NewCreator(r).Create(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	ch, _, _ := r.Character.Load(ctx, "3")
	if ch.Stats[onlineevent.LevelChallengeRoleStat] != 0 {
		t.Fatal("普通角色错误报名")
	}
	// 先占用槽位，再在账号登记成功之后制造创建失败，名额也必须回滚。
	r2 := memory.NewMemoryGroup()
	occupied := build("8")
	occupied.LevelChallenge = nil
	if err := NewCreator(r2).Create(ctx, occupied); err != nil {
		t.Fatal(err)
	}
	if err := NewCreator(r2).Create(ctx, build("9")); err == nil {
		t.Fatal("占用槽位仍可创建")
	}
	a2, _, _ := r2.Account.Load(ctx, "a")
	st2, err := onlineevent.ReadLevelChallengeState(a2)
	if err != nil || len(st2) != 0 {
		t.Fatalf("创建失败残留名额: %v %v", st2, err)
	}
}
