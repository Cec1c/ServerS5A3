package dnfbridge

import (
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAccountBootstrapRevokedSessionDoesNotCreate(t *testing.T) {
	r := memory.NewMemoryGroup()
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	q := &gameSession{accountID: "a", accountFromToken: true}
	q.loginRevoked.Store(true)
	if s.ensureAccountLevelRecords(context.Background(), r, q, "a", accountRecordBootstrapLoginSource) {
		t.Fatal("失效连接仍报告补档成功")
	}
	if _, found, _ := r.Account.Load(context.Background(), "a"); found {
		t.Fatal("失效连接创建了账号")
	}
}

func TestAccountBootstrapPreservesExistingSharedItems(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"354": {ItemID: 3033, Count: 99}}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	q := &gameSession{accountID: "a", accountFromToken: true}
	if !s.ensureAccountLevelRecords(ctx, r, q, "a", accountRecordBootstrapLoginSource) {
		t.Fatal("补档失败")
	}
	container, found, err := r.AccountInventory.Load(ctx, "a")
	if err != nil || !found || len(container.Slots) != 1 || container.Slots["354"].Count != 99 {
		t.Fatal("补档改变了共享物品", container, err)
	}
}
