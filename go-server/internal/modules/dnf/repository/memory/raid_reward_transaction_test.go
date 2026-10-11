package memory

import (
	"context"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

type raidRewardPostMailSaveFailure struct{ accountMailFailAfterSave }

func (f *raidRewardPostMailSaveFailure) Delete(ctx context.Context, id string) error {
	return f.MailboxRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}

func TestRaidRewardMailboxUOWAllAssetsScopedAndRollback(t *testing.T) {
	for _, mode := range []string{"success", "late_abort", "post_mail_save_failure", "account_scope", "character_scope", "inventory_scope", "equipment_scope", "mail_scope", "foreign_account", "missing_character", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := NewMemoryGroup()
			if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{"keep": 7}}); e != nil {
				t.Fatal(e)
			}
			if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 1}}}); e != nil {
				t.Fatal(e)
			}
			if e := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account", Slots: map[string]repo.ItemStack{}}); e != nil {
				t.Fatal(e)
			}
			oldChar, _, _ := g.Character.Load(ctx, "31")
			oldInv, _, _ := g.Inventory.Load(ctx, "31")
			oldAcc, _, _ := g.AccountInventory.Load(ctx, "account")
			u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			if mode == "post_mail_save_failure" {
				u.mailbox = &raidRewardPostMailSaveFailure{accountMailFailAfterSave{MailboxRepository: g.Mailbox, fail: true}}
			}
			run, cancel := context.WithCancel(ctx)
			defer cancel()
			account, char := "account", "31"
			if mode == "foreign_account" {
				account = "other"
			}
			if mode == "missing_character" {
				char = "99"
			}
			panicked := false
			var e error
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				e = u.WithinAccountCharacterMailboxAssets(run, account, char, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, eq repo.EquipmentRepository, m repo.MailboxRepository) error {
					if mode == "account_scope" {
						_, _, e := a.Load(run, "other")
						return e
					}
					if mode == "character_scope" {
						_, _, e := c.Load(run, "99")
						return e
					}
					if mode == "inventory_scope" {
						_, _, e := i.Load(run, "99")
						return e
					}
					if mode == "equipment_scope" {
						_, _, e := eq.Load(run, "99")
						return e
					}
					if mode == "mail_scope" {
						_, _, e := m.Load(run, "99")
						return e
					}
					ar, _, e := a.Load(run, "account")
					if e != nil {
						return e
					}
					if ar.Slots == nil {
						ar.Slots = map[string]repo.ItemStack{}
					}
					ar.Slots["0:321"] = repo.ItemStack{ItemID: 4, Count: 2}
					if e = a.Save(run, ar); e != nil {
						return e
					}
					cr, _, e := c.Load(run, "31")
					if e != nil {
						return e
					}
					cr.Stats["receipt"] = 1
					if e = c.Save(run, cr); e != nil {
						return e
					}
					ir, _, e := i.Load(run, "31")
					if e != nil {
						return e
					}
					ir.Slots["0:50"] = repo.ItemStack{ItemID: 10096296, Count: 2}
					if e = i.Save(run, ir); e != nil {
						return e
					}
					if _, e = repo.AppendSystemMail(run, m, repo.SystemMailDelivery{RecipientCharacterID: "31", Source: "raid_reward_test", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}}); e != nil {
						return e
					}
					if mode == "late_abort" {
						return errors.New("late apply failure")
					}
					if mode == "panic" {
						panic("apply panic")
					}
					if mode == "cancel" {
						cancel()
					}
					return nil
				})
			}()
			newChar, _, _ := g.Character.Load(ctx, "31")
			newInv, _, _ := g.Inventory.Load(ctx, "31")
			newAcc, _, _ := g.AccountInventory.Load(ctx, "account")
			mail, _, _ := g.Mailbox.Load(ctx, "31")
			if mode == "success" {
				if e != nil || panicked || newChar.Stats["receipt"] != 1 || newInv.Slots["0:50"].Count != 2 || newAcc.Slots["0:321"].Count != 2 || len(mail.Mails) != 1 {
					t.Fatalf("atomic success missing error=%v char=%+v inv=%+v acc=%+v mail=%+v", e, newChar, newInv, newAcc, mail)
				}
				return
			}
			if (mode == "panic" && !panicked) || (mode != "panic" && (e == nil || panicked)) {
				t.Fatal("invalid UOW accepted")
			}
			if !reflect.DeepEqual(oldChar, newChar) || !reflect.DeepEqual(oldInv, newInv) || !reflect.DeepEqual(oldAcc, newAcc) || len(mail.Mails) != 0 {
				t.Fatal("rollback leaked assets/mail", mode, e)
			}
		})
	}
}

func TestRaidRewardMailboxLateSaveFailurePreservesExistingAggregates(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{"old": 7}})
	g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 4}}, Warehouse: map[string]repo.ItemStack{"0:0": {ItemID: 9, Count: 6}}})
	g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 10096296, Count: 9}}})
	g.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: "31", Mails: map[string]repo.MailRecord{"8": {MailID: "8", Title: "existing", Attachments: []repo.MailAttachment{{ItemID: 9, Count: 6, RawEntry: []byte{1, 2}, Extra: map[string]string{"old": "keep"}}}}}})
	oldC, _, _ := g.Character.Load(ctx, "31")
	oldI, _, _ := g.Inventory.Load(ctx, "31")
	oldA, _, _ := g.AccountInventory.Load(ctx, "account")
	oldM, _, _ := g.Mailbox.Load(ctx, "31")
	u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
	u.mailbox = &raidRewardPostMailSaveFailure{accountMailFailAfterSave{MailboxRepository: g.Mailbox, fail: true}}
	e := u.WithinAccountCharacterMailboxAssets(ctx, "account", "31", func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, eq repo.EquipmentRepository, m repo.MailboxRepository) error {
		cr := repo.CloneCharacter(oldC)
		cr.Stats["receipt"] = 1
		if e := c.Save(ctx, cr); e != nil {
			return e
		}
		ir := repo.CloneInventory(oldI)
		ir.Slots["0:51"] = repo.ItemStack{ItemID: 10304483, Count: 1}
		if e := i.Save(ctx, ir); e != nil {
			return e
		}
		ar := repo.CloneAccountInventory(oldA)
		ar.Slots["0:354"] = repo.ItemStack{ItemID: 3033, Count: 1}
		if e := a.Save(ctx, ar); e != nil {
			return e
		}
		_, e := repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "31", Source: "raid_reward_test", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}})
		return e
	})
	if e == nil {
		t.Fatal("late physical mail failure not reached")
	}
	newC, _, _ := g.Character.Load(ctx, "31")
	newI, _, _ := g.Inventory.Load(ctx, "31")
	newA, _, _ := g.AccountInventory.Load(ctx, "account")
	newM, _, _ := g.Mailbox.Load(ctx, "31")
	if !reflect.DeepEqual(oldC, newC) || !reflect.DeepEqual(oldI, newI) || !reflect.DeepEqual(oldA, newA) || !reflect.DeepEqual(oldM, newM) {
		t.Fatal("late failure overwrote original aggregate")
	}
}

type raidRewardPhysicalFault struct {
	point, mode string
	used        bool
	cancel      context.CancelFunc
}

func (f *raidRewardPhysicalFault) after(point string) {
	if f.used || f.point != point {
		return
	}
	f.used = true
	if f.mode == "panic" {
		panic("physical save-after-write fixture")
	}
	f.cancel()
}

type raidRewardFaultAccount struct {
	repo.AccountInventoryRepository
	f *raidRewardPhysicalFault
}

func (s *raidRewardFaultAccount) Save(ctx context.Context, r repo.AccountInventoryRecord) error {
	e := s.AccountInventoryRepository.Save(ctx, r)
	if e == nil {
		s.f.after("account")
	}
	return e
}

type raidRewardFaultCharacter struct {
	repo.CharacterRepository
	f *raidRewardPhysicalFault
}

func (s *raidRewardFaultCharacter) Save(ctx context.Context, r repo.CharacterRecord) error {
	e := s.CharacterRepository.Save(ctx, r)
	if e == nil {
		s.f.after("character")
	}
	return e
}

type raidRewardFaultInventory struct {
	repo.InventoryRepository
	f *raidRewardPhysicalFault
}

func (s *raidRewardFaultInventory) Save(ctx context.Context, r repo.InventoryRecord) error {
	e := s.InventoryRepository.Save(ctx, r)
	if e == nil {
		s.f.after("inventory")
	}
	return e
}

type raidRewardFaultEquipment struct {
	repo.EquipmentRepository
	f *raidRewardPhysicalFault
}

func (s *raidRewardFaultEquipment) Save(ctx context.Context, r repo.EquipmentRecord) error {
	e := s.EquipmentRepository.Save(ctx, r)
	if e == nil {
		s.f.after("equipment")
	}
	return e
}

type raidRewardFaultMailbox struct {
	repo.MailboxRepository
	f *raidRewardPhysicalFault
}

func (s *raidRewardFaultMailbox) Save(ctx context.Context, r repo.MailboxRecord) error {
	e := s.MailboxRepository.Save(ctx, r)
	if e == nil {
		s.f.after("mailbox")
	}
	return e
}
func (s *raidRewardFaultMailbox) Delete(ctx context.Context, id string) error {
	return s.MailboxRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}
func TestRaidRewardMailboxPhysicalSavePanicAndCancelRestoreAll(t *testing.T) {
	for _, point := range []string{"account", "character", "inventory", "equipment", "mailbox"} {
		for _, mode := range []string{"panic", "cancel"} {
			for _, existingMail := range []bool{true, false} {
				if !existingMail && point != "mailbox" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/existingmail%t", point, mode, existingMail), func(t *testing.T) {
					ctx := context.Background()
					g := NewMemoryGroup()
					g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{"keep": 7}})
					g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 4, Count: 8}}, Warehouse: map[string]repo.ItemStack{"0:0": {ItemID: 8, Count: 9}}})
					g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 9, Count: 10}}})
					g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "31", Entries: map[string]repo.EquipmentEntry{"0": {ItemID: 35381, SlotIndex: 0, RawEntry: []byte{1, 2}}}})
					if existingMail {
						g.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: "31", Mails: map[string]repo.MailRecord{"8": {MailID: "8", Title: "keep", Attachments: []repo.MailAttachment{{ItemID: 8, Count: 9}}}}})
					}
					oldC, _, _ := g.Character.Load(ctx, "31")
					oldI, _, _ := g.Inventory.Load(ctx, "31")
					oldA, _, _ := g.AccountInventory.Load(ctx, "account")
					oldE, _, _ := g.Equipment.Load(ctx, "31")
					oldM, oldMFound, _ := g.Mailbox.Load(ctx, "31")
					run, cancel := context.WithCancel(ctx)
					defer cancel()
					fault := &raidRewardPhysicalFault{point: point, mode: mode, cancel: cancel}
					u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
					u.accountInventory = &raidRewardFaultAccount{g.AccountInventory, fault}
					u.character = &raidRewardFaultCharacter{g.Character, fault}
					u.inventory = &raidRewardFaultInventory{g.Inventory, fault}
					u.equipment = &raidRewardFaultEquipment{g.Equipment, fault}
					u.mailbox = &raidRewardFaultMailbox{g.Mailbox, fault}
					var e error
					var panicked bool
					func() {
						defer func() {
							if recover() != nil {
								panicked = true
							}
						}()
						e = u.WithinAccountCharacterMailboxAssets(run, "account", "31", func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, eq repo.EquipmentRepository, m repo.MailboxRepository) error {
							ar := repo.CloneAccountInventory(oldA)
							ar.Slots["0:354"] = repo.ItemStack{ItemID: 3033, Count: 3}
							if e := a.Save(run, ar); e != nil {
								return e
							}
							cr := repo.CloneCharacter(oldC)
							cr.Stats["receipt"] = 1
							if e := c.Save(run, cr); e != nil {
								return e
							}
							ir := repo.CloneInventory(oldI)
							ir.Slots["0:51"] = repo.ItemStack{ItemID: 10304483, Count: 1}
							if e := i.Save(run, ir); e != nil {
								return e
							}
							er := repo.CloneEquipment(oldE)
							er.Entries["1"] = repo.EquipmentEntry{ItemID: 35381, SlotIndex: 1, RawEntry: []byte{3, 4}}
							if e := eq.Save(run, er); e != nil {
								return e
							}
							_, e := repo.AppendSystemMail(run, m, repo.SystemMailDelivery{RecipientCharacterID: "31", Source: "raid_reward_physical_fault", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}})
							return e
						})
					}()
					if !fault.used || (mode == "panic" && !panicked) || (mode == "cancel" && (panicked || !errors.Is(e, context.Canceled))) {
						t.Fatal("physical boundary not exercised", fault.used, e, panicked)
					}
					newC, _, _ := g.Character.Load(ctx, "31")
					newI, _, _ := g.Inventory.Load(ctx, "31")
					newA, _, _ := g.AccountInventory.Load(ctx, "account")
					newE, _, _ := g.Equipment.Load(ctx, "31")
					newM, newMFound, _ := g.Mailbox.Load(ctx, "31")
					if !reflect.DeepEqual(oldC, newC) || !reflect.DeepEqual(oldI, newI) || !reflect.DeepEqual(oldA, newA) || !reflect.DeepEqual(oldE, newE) || !reflect.DeepEqual(oldM, newM) || oldMFound != newMFound {
						t.Fatal("physical write failure left partial aggregate")
					}
					// A real subsequent UOW must acquire the shared mutex after unwind.
					done := make(chan error, 1)
					go func() {
						done <- u.WithinAccountCharacterMailboxAssets(ctx, "account", "31", func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error {
							return nil
						})
					}()
					select {
					case e := <-done:
						if e != nil {
							t.Fatal(e)
						}
					case <-time.After(time.Second):
						t.Fatal("shared mutex remained locked")
					}
				})
			}
		}
	}
}
