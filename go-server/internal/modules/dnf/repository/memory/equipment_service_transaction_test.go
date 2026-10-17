package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

type equipmentPhysicalAccountInventory struct {
	r.AccountInventoryRepository
	p *mailboxPhysicalPlan
}

func (s equipmentPhysicalAccountInventory) Save(ctx context.Context, v r.AccountInventoryRecord) error {
	if e := s.AccountInventoryRepository.Save(ctx, v); e != nil {
		return e
	}
	return s.p.after("account_inventory")
}
func (s equipmentPhysicalAccountInventory) Delete(ctx context.Context, id string) error {
	return s.AccountInventoryRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}

func TestEquipmentServicePhysicalFailureRollsBackAllOwners(t *testing.T) {
	for _, stage := range []string{"inventory", "account_inventory", "mailbox"} {
		for _, mode := range []string{"error", "cancel", "panic"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				g := NewMemoryGroup()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				g.Account.Save(ctx, r.AccountRecord{AccountID: "a", Metadata: map[string]string{"epic_piece:1": "10"}})
				g.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "a"})
				g.Character.Save(ctx, r.CharacterRecord{CharacterID: "42", AccountID: "a"})
				i := r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:9": {ItemID: 1001, Count: 1}}}
				a := r.AccountInventoryRecord{AccountID: "a", Slots: map[string]r.ItemStack{"0:363": {ItemID: 10099774, Count: 100}}}
				g.Inventory.Save(ctx, i)
				g.AccountInventory.Save(ctx, a)
				u := g.MailboxAssets.(*memoryMailboxAssetUnitOfWork)
				p := &mailboxPhysicalPlan{stage: stage, mode: mode, cancel: cancel, token: new(int), counts: map[string]int{}, active: true}
				u.inventory = mailboxPhysicalInv{g.Inventory, p}
				u.accountInventory = equipmentPhysicalAccountInventory{g.AccountInventory, p}
				u.mailbox = mailboxPhysicalMail{g.Mailbox, p}
				panicked := false
				var err error
				func() {
					defer func() {
						if v := recover(); v != nil {
							if v != p.token {
								t.Fatalf("异常被替换：%v", v)
							}
							panicked = true
						}
					}()
					err = u.WithinEquipmentServiceAssets(ctx, "a", "19", "42", func(x r.Group, _, _ r.CharacterRecord) error {
						ii := r.CloneInventory(i)
						delete(ii.Slots, "0:9")
						if e := x.Inventory.Save(ctx, ii); e != nil {
							return e
						}
						aa := r.CloneAccountInventory(a)
						delete(aa.Slots, "0:363")
						if e := x.AccountInventory.Save(ctx, aa); e != nil {
							return e
						}
						account, _, e := x.Account.Load(ctx, "a")
						if e != nil {
							return e
						}
						if e = r.SaveAccountMetadataEntry(ctx, x.Account, account, "epic_piece:1", "0", time.Now()); e != nil {
							return e
						}
						_, e = r.AppendSystemMail(ctx, x.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "42", Source: "跨界回滚测试", Attachments: []r.MailAttachment{{ItemID: 1001, Count: 1}}})
						return e
					})
				}()
				if mode == "panic" && !panicked || mode != "panic" && err == nil {
					t.Fatal("物理失败未传播", err, panicked)
				}
				ii, _, _ := g.Inventory.Load(context.Background(), "19")
				aa, _, _ := g.AccountInventory.Load(context.Background(), "a")
				ac, _, _ := g.Account.Load(context.Background(), "a")
				m, found, _ := g.Mailbox.Load(context.Background(), "42")
				if !reflect.DeepEqual(i, ii) || !reflect.DeepEqual(a, aa) || ac.Metadata["epic_piece:1"] != "10" || found || len(m.Mails) != 0 {
					t.Fatal("物理失败没有完整恢复", p.trace)
				}
			})
		}
	}
}
func TestEquipmentServiceRejectsForeignCharacterAndScopedEscapes(t *testing.T) {
	g := NewMemoryGroup()
	ctx := context.Background()
	g.Account.Save(ctx, r.AccountRecord{AccountID: "a"})
	g.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "a"})
	g.Character.Save(ctx, r.CharacterRecord{CharacterID: "99", AccountID: "b"})
	u := g.MailboxAssets.(r.EquipmentServiceUnitOfWork)
	called := false
	if e := u.WithinEquipmentServiceAssets(ctx, "a", "19", "99", func(r.Group, r.CharacterRecord, r.CharacterRecord) error { called = true; return nil }); e == nil || called {
		t.Fatal("跨账号目标放行")
	}
	if e := u.WithinEquipmentServiceAssets(ctx, "a", "19", "19", func(x r.Group, _, _ r.CharacterRecord) error {
		if _, _, e := x.Account.Load(ctx, "b"); e == nil {
			return errors.New("账号作用域逃逸")
		}
		if _, _, e := x.Inventory.Load(ctx, "99"); e == nil {
			return errors.New("背包作用域逃逸")
		}
		if _, _, e := x.Mailbox.Load(ctx, "99"); e == nil {
			return errors.New("邮箱作用域逃逸")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
