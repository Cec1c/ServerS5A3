package memory

import (
	"context"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

type accountTradePhysicalAccount struct {
	r.AccountRepository
	p *mailboxPhysicalPlan
}

func (a accountTradePhysicalAccount) Save(ctx context.Context, v r.AccountRecord) error {
	if e := a.AccountRepository.Save(ctx, v); e != nil {
		return e
	}
	return a.p.after("account")
}

type accountTradePhysicalCharacter struct {
	r.CharacterRepository
	p *mailboxPhysicalPlan
}

func (c accountTradePhysicalCharacter) Save(ctx context.Context, v r.CharacterRecord) error {
	if e := c.CharacterRepository.Save(ctx, v); e != nil {
		return e
	}
	return c.p.after("character:" + v.CharacterID)
}

type accountTradePhysicalInventory struct {
	r.InventoryRepository
	p *mailboxPhysicalPlan
}

func (i accountTradePhysicalInventory) Save(ctx context.Context, v r.InventoryRecord) error {
	if e := i.InventoryRepository.Save(ctx, v); e != nil {
		return e
	}
	return i.p.after("inventory:" + v.CharacterID)
}
func (i accountTradePhysicalInventory) Delete(ctx context.Context, id string) error {
	return i.InventoryRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}
func TestAccountTradeMailboxPhysicalAfterWriteFailureRestoresEveryAggregate(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, stage := range []string{"account", "character:2", "inventory:2", "mailbox"} {
			for _, mode := range []string{"error", "cancel", "panic"} {
				t.Run(fmt.Sprintf("existing%t/%s/%s", existing, stage, mode), func(t *testing.T) {
					g := NewMemoryGroup()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					g.Account.Save(ctx, r.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cera": "1000"}})
					for _, id := range []string{"1", "2"} {
						g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "a", Stats: map[string]int64{"gold": 100}})
					}
					g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"old": {ItemID: 1, Count: 1}}})
					if existing {
						g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "2", Slots: map[string]r.ItemStack{"old": {ItemID: 2, Count: 1}}})
						g.Mailbox.Save(ctx, r.MailboxRecord{CharacterID: "1", Mails: map[string]r.MailRecord{"old": {MailID: "old", Gold: 5}}})
					}
					oldA, _, _ := g.Account.Load(ctx, "a")
					oldC1, _, _ := g.Character.Load(ctx, "1")
					oldC2, _, _ := g.Character.Load(ctx, "2")
					oldI1, _, _ := g.Inventory.Load(ctx, "1")
					oldI2, hasI2, _ := g.Inventory.Load(ctx, "2")
					oldM, hasM, _ := g.Mailbox.Load(ctx, "1")
					token := 1
					p := &mailboxPhysicalPlan{stage: stage, mode: mode, cancel: cancel, token: &token, counts: map[string]int{}, active: true}
					u := g.MailboxAssets.(*memoryMailboxAssetUnitOfWork)
					u.account = accountTradePhysicalAccount{g.Account, p}
					u.character = accountTradePhysicalCharacter{g.Character, p}
					u.inventory = accountTradePhysicalInventory{g.Inventory, p}
					u.mailbox = mailboxPhysicalMail{g.Mailbox, p}
					var err error
					var recovered any
					func() {
						defer func() { recovered = recover() }()
						err = u.WithinAccountTradeMailbox(ctx, "a", []string{"2", "1"}, "1", func(a r.AccountRepository, c r.CharacterRepository, i r.InventoryRepository, m r.MailboxRepository) error {
							account, _, e := a.Load(ctx, "a")
							if e != nil {
								return e
							}
							account.Metadata["account_cera"] = "889"
							if e = a.Save(ctx, account); e != nil {
								return e
							}
							for _, id := range []string{"1", "2"} {
								ch, _, e := c.Load(ctx, id)
								if e != nil {
									return e
								}
								ch.Stats["gold"] = 0
								if e = c.Save(ctx, ch); e != nil {
									return e
								}
								if e = i.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"new": {ItemID: 3, Count: 1}}}); e != nil {
									return e
								}
							}
							return m.Save(ctx, r.MailboxRecord{CharacterID: "1", Mails: map[string]r.MailRecord{"new": {MailID: "new", Gold: 100}}})
						})
					}()
					if mode == "panic" {
						if recovered != &token {
							t.Fatal("panic身份改变", recovered)
						}
					} else if err == nil {
						t.Fatal("失败未返回错误")
					}
					if mode == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					a, _, _ := g.Account.Load(nil, "a")
					c1, _, _ := g.Character.Load(nil, "1")
					c2, _, _ := g.Character.Load(nil, "2")
					i1, _, _ := g.Inventory.Load(nil, "1")
					i2, foundI2, _ := g.Inventory.Load(nil, "2")
					m, foundM, _ := g.Mailbox.Load(nil, "1")
					if !reflect.DeepEqual(a, oldA) || !reflect.DeepEqual(c1, oldC1) || !reflect.DeepEqual(c2, oldC2) || !reflect.DeepEqual(i1, oldI1) || foundI2 != hasI2 || (hasI2 && !reflect.DeepEqual(i2, oldI2)) || foundM != hasM || (hasM && !reflect.DeepEqual(m, oldM)) {
						t.Fatalf("物理写入后回滚不完整 stage=%s trace=%v", stage, p.trace)
					}
				})
			}
		}
	}
}
