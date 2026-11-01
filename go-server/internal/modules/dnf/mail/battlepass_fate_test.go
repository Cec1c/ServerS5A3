package mail

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type battlePassFailMailboxAssets struct{ repo.MailboxAssetUnitOfWork }

func (u battlePassFailMailboxAssets) WithinMailboxAssets(ctx context.Context, inventoryID, mailboxID string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, inventoryID, mailboxID, func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		if err := apply(c, i, m); err != nil {
			return err
		}
		return errors.New("注入邮件事务提交失败")
	})
}

func TestBattlePassFateMailActualClaimReplay(t *testing.T) {
	ctx, g, id := cubeWarehouseMailFixtures(t, repo.BattlePassFateItemID, 80)
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "acc-a"}); err != nil {
		t.Fatal(err)
	}
	resolver := func(uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{Kind: "stackable", PVFPath: "stackable/10304001/10304529.stk", SlotStart: 121, SlotEnd: 176, StackLimit: 9999}, nil
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	o := NewOwner(g, resolver)
	o.now = func() time.Time { return now }
	key := fmt.Sprintf("%s%s:43:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))
	ch, _, _ := g.Character.Load(ctx, "77")
	if ch.Stats[key] != 0 {
		t.Fatal("创建邮件就增加任务")
	}
	broken := g
	broken.MailboxAssets = fateClaimRejectAssets{g.MailboxAssets}
	bad := NewOwner(broken, resolver)
	bad.now = o.now
	if _, err := bad.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{id}}); err == nil {
		t.Fatal("预期提交失败")
	}
	ch, _, _ = g.Character.Load(ctx, "77")
	if ch.Stats[key] != 0 {
		t.Fatal("失败事务留下计数")
	}
	if _, err := o.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{id}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{id}}); err != nil && !errors.Is(err, ErrMailboxAlreadyClaimed) {
		t.Fatal(err)
	}
	ch, _, _ = g.Character.Load(ctx, "77")
	if ch.Stats[key] != 80 {
		t.Fatal("邮件领取数量/重放错误", ch.Stats)
	}
}
