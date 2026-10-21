package mail

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func mailPremiumResolver(id int64) (alignedcmd.PremiumContractResolution, error) {
	if id == 30 {
		return alignedcmd.PremiumContractResolution{ItemID: id, PremiumType: premium.TypeOverEquip, DurationSeconds: 86400}, nil
	}
	if id == 43 {
		return alignedcmd.PremiumContractResolution{ItemID: id, PremiumType: premium.TypeOverSkill, DurationSeconds: 86400}, nil
	}
	return alignedcmd.PremiumContractResolution{}, nil
}

func TestMailboxPremiumContractClaimsIgnoreFullBagAndPreserveOtherAttachments(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Now().UTC().Truncate(time.Second)
	account := r.AccountRecord{AccountID: "a"}
	premium.Upsert(&account, premium.TypeOverEquip, 86400, 2, now)
	for _, e := range []error{g.Account.Save(ctx, account), g.Character.Save(ctx, r.CharacterRecord{CharacterID: "77", AccountID: "a"}), g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{"0:3": {ItemID: 999, Count: 1}, "0:4": {ItemID: 999, Count: 1}, "0:5": {ItemID: 999, Count: 1}, "0:6": {ItemID: 999, Count: 1}, "0:7": {ItemID: 999, Count: 1}, "0:8": {ItemID: 999, Count: 1}, "0:65": {ItemID: 999, Count: 1}}})} {
		if e != nil {
			t.Fatal(e)
		}
	}
	_, e := r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "77", CreatedAt: now, Source: "cera_package_main_inventory_full", Attachments: []r.MailAttachment{{ItemID: 30, Count: 3}, {ItemID: 600, Count: 1}, {ItemID: 43, Count: 2}}})
	if e != nil {
		t.Fatal(e)
	}
	o := NewOwner(g, mailboxClaimTestResolver(65, 65, 1)).WithPremiumContractResolver(mailPremiumResolver)
	o.now = func() time.Time { return now }
	if _, e = o.Open(ctx, "77"); e != nil {
		t.Fatal(e)
	}
	box, _, _ := g.Mailbox.Load(ctx, "77")
	ids, _, _ := mailboxStoredClaimIDs(box.Mails["1"])
	before, _, _ := g.Inventory.Load(ctx, "77")
	result, e := o.Claim(ctx, "77", ExtractRequest{MailIDs: ids})
	if e != nil {
		t.Fatal(e)
	}
	if len(result.ContractActivations) != 2 || !result.InventoryFull || len(result.ItemSlotRefreshes) != 0 || result.WalletChanged {
		t.Fatalf("领取结果=%+v", result)
	}
	after, _, _ := g.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("契约改变背包")
	}
	got, _, _ := g.Account.Load(ctx, "a")
	if premium.ExpireAt(got, premium.TypeOverEquip) != now.Unix()+5*86400 || premium.ExpireAt(got, premium.TypeOverSkill) != now.Unix()+2*86400 {
		t.Fatal("契约未按原时间及数量叠加", got)
	}
	box, _, _ = g.Mailbox.Load(ctx, "77")
	left := box.Mails["1"]
	if len(left.Attachments) != 1 || left.Attachments[0].ItemID != 600 || left.Claimed || left.Deleted {
		t.Fatal("满包普通附件未保留", left)
	}
	if _, e = o.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{ids[0]}}); e == nil {
		t.Fatal("已领取契约重放成功")
	}
	replayed, _, _ := g.Account.Load(ctx, "a")
	if !reflect.DeepEqual(got, replayed) {
		t.Fatal("重放增加时长")
	}
}

type rejectPremiumClaimAssets struct{ r.MailboxAssetUnitOfWork }

func (u rejectPremiumClaimAssets) WithinAccountTradeMailbox(ctx context.Context, a string, ids []string, m string, apply func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.(r.AccountTradeMailboxUnitOfWork).WithinAccountTradeMailbox(ctx, a, ids, m, func(ar r.AccountRepository, c r.CharacterRepository, i r.InventoryRepository, b r.MailboxRepository) error {
		if e := apply(ar, c, i, b); e != nil {
			return e
		}
		return errors.New("契约邮件提交失败夹具")
	})
}

func TestMailboxPremiumContractFailureRollsBackAccountAndMail(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Now().UTC()
	for _, e := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: "a"}), g.Character.Save(ctx, r.CharacterRecord{CharacterID: "77", AccountID: "a"})} {
		if e != nil {
			t.Fatal(e)
		}
	}
	_, e := r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "77", CreatedAt: now, Source: "magic_box_reward_inventory_full", Attachments: []r.MailAttachment{{ItemID: 30, Count: 3}}})
	if e != nil {
		t.Fatal(e)
	}
	before, _, _ := g.Mailbox.Load(ctx, "77")
	account, _, _ := g.Account.Load(ctx, "a")
	broken := g
	broken.MailboxAssets = rejectPremiumClaimAssets{g.MailboxAssets}
	if _, e = NewOwner(broken).WithPremiumContractResolver(mailPremiumResolver).Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}}); e == nil {
		t.Fatal("提交失败仍返回成功")
	}
	after, _, _ := g.Mailbox.Load(ctx, "77")
	got, _, _ := g.Account.Load(ctx, "a")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, got) {
		t.Fatal("领取状态和契约未一起回滚")
	}
	result, e := NewOwner(g).WithPremiumContractResolver(mailPremiumResolver).Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{1}})
	if e != nil || len(result.ContractActivations) != 1 {
		t.Fatal(result, e)
	}
}
