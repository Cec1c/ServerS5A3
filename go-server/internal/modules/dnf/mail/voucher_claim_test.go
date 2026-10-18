package mail

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"sync"
	"testing"
)

func voucherClaimFixture(t *testing.T) (r.Group, Owner) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	g.Account.Save(ctx, r.AccountRecord{AccountID: "a"})
	g.Character.Save(ctx, r.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": 0}})
	g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{}})
	g.Mailbox.Save(ctx, r.MailboxRecord{CharacterID: "77", Mails: map[string]r.MailRecord{"1": {MailID: "1", Attachments: []r.MailAttachment{{ItemID: shop.TokenCeraVoucherItemID, Count: 100}}}}})
	return g, NewOwner(g)
}

type failVoucherAssets struct {
	r.MailboxAssetUnitOfWork
	tx r.AccountTradeMailboxUnitOfWork
}

func (u failVoucherAssets) WithinAccountTradeMailbox(ctx context.Context, a string, ids []string, m string, f func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.MailboxRepository) error) error {
	return u.tx.WithinAccountTradeMailbox(ctx, a, ids, m, func(a r.AccountRepository, c r.CharacterRepository, i r.InventoryRepository, m r.MailboxRepository) error {
		if e := f(a, c, i, m); e != nil {
			return e
		}
		return errors.New("注入领取提交失败")
	})
}
func TestVoucherClaimAtomicFailureConcurrentRetryAndOverflow(t *testing.T) {
	ctx := context.Background()
	g, o := voucherClaimFixture(t)
	broken := g
	broken.MailboxAssets = failVoucherAssets{g.MailboxAssets, g.MailboxAssets.(r.AccountTradeMailboxUnitOfWork)}
	if _, e := NewOwner(broken).Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}}); e == nil {
		t.Fatal("未触发失败")
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if shop.TokenCeraBalance(a) != 0 {
		t.Fatal("失败仍入账")
	}
	m, _, _ := g.Mailbox.Load(ctx, "77")
	if m.Mails["1"].Claimed {
		t.Fatal("失败仍结清邮件")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := o.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}}); e != nil && !errors.Is(e, ErrMailboxAlreadyClaimed) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	a, _, _ = g.Account.Load(ctx, "a")
	if shop.TokenCeraBalance(a) != 100 {
		t.Fatal("并发领取重复入账", a)
	}
	g, o = voucherClaimFixture(t)
	a, _, _ = g.Account.Load(ctx, "a")
	shop.SetTokenCeraBalance(&a, shop.MaxWalletBalance-50)
	g.Account.Save(ctx, a)
	if _, e := o.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}}); !errors.Is(e, shop.ErrWalletOverflow) {
		t.Fatal(e)
	}
	m, _, _ = g.Mailbox.Load(ctx, "77")
	if m.Mails["1"].Claimed {
		t.Fatal("溢出丢券")
	}
}
func TestVoucherMixedLetterDoesNotCreditWhenOrdinaryItemCannotFit(t *testing.T) {
	ctx := context.Background()
	g, _ := voucherClaimFixture(t)
	m, _, _ := g.Mailbox.Load(ctx, "77")
	letter := m.Mails["1"]
	letter.Attachments = append(letter.Attachments, r.MailAttachment{ItemID: 6001, Count: 1})
	m.Mails["1"] = letter
	g.Mailbox.Save(ctx, m)
	inv, _, _ := g.Inventory.Load(ctx, "77")
	inv.Slots = map[string]r.ItemStack{}
	for slot := int16(3); slot <= 359; slot++ {
		inv.Slots[mailboxSlotKey(0, slot)] = r.ItemStack{ItemID: 7001, Count: 1}
	}
	g.Inventory.Save(ctx, inv)
	resolver := func(uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{Kind: "equipment", SlotStart: 9, SlotEnd: 64, StackLimit: 1}, nil
	}
	_, e := NewOwner(g, resolver).Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}})
	if !errors.Is(e, ErrMailboxInventoryFull) {
		t.Fatal(e)
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if shop.TokenCeraBalance(a) != 0 {
		t.Fatal("放不下的混合信件仍入账")
	}
}
