package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
)

func TestWeddingLifecycleCommitsOrRestoresAllOwners(t *testing.T) {
	for _, fault := range []string{"callback", "settings-write", "panic-write", "none"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			g := NewMemoryGroup()
			ids := []string{"2", "5", "9"}
			for _, id := range ids {
				if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "account" + id, Stats: map[string]int64{"gold": 1000}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:1": {ItemID: 10, Count: 1}}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{11: {Status: "active"}}}); err != nil {
					t.Fatal(err)
				}
			}
			scope, _ := r.WeddingPairScope(ids[:2])
			before := map[string]r.CharacterRecord{}
			for _, id := range ids {
				before[id], _, _ = g.Character.Load(ctx, id)
			}
			u := g.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
			if fault == "settings-write" {
				u.settings = &failRaidSettingsOnce{SettingsRepository: g.Settings, fail: true}
			}
			if fault == "panic-write" {
				u.mailbox = &weddingLifecyclePanicMailbox{MailboxRepository: g.Mailbox}
			}
			var err error
			panicked := false
			func() {
				defer func() {
					if p := recover(); p != nil {
						panicked = true
					}
				}()
				err = u.WithinWeddingGuestLifecycle(ctx, ids[:2], ids[2:], scope, func(tx r.Group) error {
					if tx.CharacterTrade != nil || tx.Account != nil {
						t.Fatal("unscoped/nested capabilities escaped")
					}
					if _, _, e := tx.Mailbox.Load(ctx, "10"); e == nil {
						t.Fatal("unowned mailbox accepted")
					}
					if _, _, e := tx.Settings.Load(ctx, "another-couple"); e == nil {
						t.Fatal("unowned settings accepted")
					}
					for _, id := range ids {
						c, _, e := tx.Character.Load(ctx, id)
						if e != nil {
							return e
						}
						c.Stats["gold"] -= 100
						if e = r.SaveCharacterFields(ctx, tx.Character, c, r.CharacterFieldStats); e != nil {
							return e
						}
						if e = tx.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id}); e != nil {
							return e
						}
						if e = tx.Quest.Save(ctx, r.QuestRecord{CharacterID: id}); e != nil {
							return e
						}
						if e = tx.Mailbox.Save(ctx, r.MailboxRecord{CharacterID: id, Mails: map[string]r.MailRecord{"wedding-once": {MailID: "wedding-once", Gold: 100}}}); e != nil {
							return e
						}
					}
					if e := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": "paid"}}); e != nil {
						return e
					}
					if fault == "callback" {
						return errors.New("fault after all staged writes")
					}
					return nil
				})
			}()
			if fault == "panic-write" && !panicked {
				t.Fatal("panic swallowed")
			}
			if fault != "none" && !panicked && err == nil {
				t.Fatal("failure committed")
			}
			if fault == "none" && err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				c, _, _ := g.Character.Load(ctx, id)
				i, _, _ := g.Inventory.Load(ctx, id)
				q, _, _ := g.Quest.Load(ctx, id)
				m, found, _ := g.Mailbox.Load(ctx, id)
				if fault == "none" {
					if c.Stats["gold"] != 900 || len(i.Slots) != 0 || len(q.States) != 0 || !found || len(m.Mails) != 1 {
						t.Fatal("partial successful commit")
					}
				} else if !reflect.DeepEqual(c, before[id]) || len(i.Slots) != 1 || len(q.States) != 1 || found {
					t.Fatalf("partial failed commit owner=%s", id)
				}
			}
			_, found, _ := g.Settings.Load(ctx, scope)
			if found != (fault == "none") {
				t.Fatal("ledger rollback mismatch")
			}
		})
	}
}

type weddingLifecyclePanicMailbox struct {
	r.MailboxRepository
	once sync.Once
}

func (m *weddingLifecyclePanicMailbox) Delete(ctx context.Context, id string) error {
	return m.MailboxRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}

func (m *weddingLifecyclePanicMailbox) Save(ctx context.Context, record r.MailboxRecord) error {
	if err := m.MailboxRepository.Save(ctx, record); err != nil {
		return err
	}
	m.once.Do(func() { panic("after actual mailbox write") })
	return nil
}

func TestWeddingLifecycleConcurrentReceiptAndScope(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	for _, id := range []string{"2", "5", "9"} {
		if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Stats: map[string]int64{"gold": 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	u := g.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
	scope, _ := r.WeddingPairScope([]string{"2", "5"})
	for _, guests := range [][]string{{"2"}, {"09"}, {"65536"}, {"9", "9"}} {
		if err := u.WithinWeddingGuestLifecycle(ctx, []string{"2", "5"}, guests, scope, func(r.Group) error { return nil }); err == nil {
			t.Fatal("invalid scope accepted", guests)
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- u.WithinWeddingGuestLifecycle(ctx, []string{"2", "5"}, []string{"9"}, scope, func(tx r.Group) error {
				ledger, _, err := tx.Settings.Load(ctx, scope)
				if err != nil {
					return err
				}
				if ledger.Values["receipt"] != "" {
					return nil
				}
				c, _, err := tx.Character.Load(ctx, "9")
				if err != nil {
					return err
				}
				c.Stats["gold"] -= 100
				if err = r.SaveCharacterFields(ctx, tx.Character, c, r.CharacterFieldStats); err != nil {
					return err
				}
				return tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": "gift"}})
			})
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := g.Character.Load(ctx, "9")
	if c.Stats["gold"] != 900 {
		t.Fatalf("duplicate guest debit %d", c.Stats["gold"])
	}
}

func TestWeddingGuestIndexIsAtomicAndOnlyGuestCapabilityCanWrite(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	hosts := []string{"2", "5"}
	for _, id := range hosts {
		if e := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "a" + id}); e != nil {
			t.Fatal(e)
		}
	}
	scope, _ := r.WeddingPairScope(hosts)
	if e := g.CharacterTrade.(r.WeddingLifecycleUnitOfWork).WithinWeddingLifecycle(ctx, hosts, scope, func(tx r.Group) error {
		if _, _, e := tx.Settings.Load(ctx, r.WeddingGuestIndexScope); e == nil {
			t.Fatal("pair-only capability escaped to index")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	u := g.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
	fault := errors.New("rollback index and ledger")
	for _, fail := range []bool{true, false} {
		e := u.WithinWeddingGuestLifecycle(ctx, hosts, nil, scope, func(tx r.Group) error {
			if e := tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.WeddingGuestIndexScope, Values: map[string]string{"next": "1"}}); e != nil {
				return e
			}
			if e := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"run": "1"}}); e != nil {
				return e
			}
			if fail {
				return fault
			}
			return nil
		})
		if fail && !errors.Is(e, fault) || !fail && e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{scope, r.WeddingGuestIndexScope} {
			_, found, e := g.Settings.Load(ctx, key)
			if e != nil || found == fail {
				t.Fatal("index/ledger partial commit", key, found, e)
			}
		}
	}
}
