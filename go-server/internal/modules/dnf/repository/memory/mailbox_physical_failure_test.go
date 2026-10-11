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

type mailboxPhysicalPlan struct {
	stage, mode, restorePanic string
	cancel                    context.CancelFunc
	token                     *int
	counts                    map[string]int
	trace                     []string
	active                    bool
}

func (p *mailboxPhysicalPlan) after(stage string) error {
	p.counts[stage]++
	n := p.counts[stage]
	phase := "write"
	if n > 1 {
		phase = "restore"
	}
	p.trace = append(p.trace, phase+":"+stage)
	if !p.active {
		return nil
	}
	if n > 1 && p.restorePanic == stage {
		panic("restore:" + stage)
	}
	if n == 1 && p.stage == stage {
		switch p.mode {
		case "cancel":
			p.cancel()
		case "panic":
			panic(p.token)
		case "error":
			return errors.New("afterwrite:" + stage)
		}
	}
	return nil
}

type mailboxPhysicalChar struct {
	repo.CharacterRepository
	p *mailboxPhysicalPlan
}

func (s mailboxPhysicalChar) Save(ctx context.Context, r repo.CharacterRecord) error {
	if e := s.CharacterRepository.Save(ctx, r); e != nil {
		return e
	}
	return s.p.after("character")
}
func (s mailboxPhysicalChar) Delete(ctx context.Context, id string) error {
	e := s.CharacterRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
	if e != nil {
		return e
	}
	return s.p.after("character")
}

type mailboxPhysicalInv struct {
	repo.InventoryRepository
	p *mailboxPhysicalPlan
}

func (s mailboxPhysicalInv) Save(ctx context.Context, r repo.InventoryRecord) error {
	if e := s.InventoryRepository.Save(ctx, r); e != nil {
		return e
	}
	return s.p.after("inventory")
}
func (s mailboxPhysicalInv) Delete(ctx context.Context, id string) error {
	e := s.InventoryRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
	if e != nil {
		return e
	}
	return s.p.after("inventory")
}

type mailboxPhysicalMail struct {
	repo.MailboxRepository
	p *mailboxPhysicalPlan
}

func (s mailboxPhysicalMail) Save(ctx context.Context, r repo.MailboxRecord) error {
	if e := s.MailboxRepository.Save(ctx, r); e != nil {
		return e
	}
	return s.p.after("mailbox")
}
func (s mailboxPhysicalMail) Delete(ctx context.Context, id string) error {
	e := s.MailboxRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
	if e != nil {
		return e
	}
	return s.p.after("mailbox")
}

func seedMailboxPhysical(t *testing.T, existing bool) (repo.Group, *memoryMailboxAssetUnitOfWork) {
	t.Helper()
	g := NewMemoryGroup()
	if existing {
		g.Character.Save(nil, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"old": 8}})
		g.Inventory.Save(nil, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:4": {ItemID: 44, Count: 8}}})
		g.Mailbox.Save(nil, repo.MailboxRecord{CharacterID: "1", Mails: map[string]repo.MailRecord{"old": {MailID: "old", Title: "old", Attachments: []repo.MailAttachment{{ItemID: 55, Count: 2}}}}})
	}
	return g, g.MailboxAssets.(*memoryMailboxAssetUnitOfWork)
}
func mailboxPhysicalMutation(ctx context.Context, c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
	if e := c.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"new": 99}}); e != nil {
		return e
	}
	if e := i.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:4": {ItemID: 44, Count: 99}}}); e != nil {
		return e
	}
	return m.Save(ctx, repo.MailboxRecord{CharacterID: "1", Mails: map[string]repo.MailRecord{"new": {MailID: "new", Title: "new"}}})
}
func TestMailboxPhysicalAfterWriteCancelErrorPanicRestores(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, stage := range []string{"character", "inventory", "mailbox"} {
			for _, mode := range []string{"cancel", "error", "panic"} {
				t.Run(fmt.Sprintf("old%t/%s/%s", existing, stage, mode), func(t *testing.T) {
					g, u := seedMailboxPhysical(t, existing)
					oldC, hasC, _ := g.Character.Load(nil, "1")
					oldI, hasI, _ := g.Inventory.Load(nil, "1")
					oldM, hasM, _ := g.Mailbox.Load(nil, "1")
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					token := 1
					p := &mailboxPhysicalPlan{stage: stage, mode: mode, cancel: cancel, token: &token, counts: map[string]int{}, active: true}
					u.character = mailboxPhysicalChar{g.Character, p}
					u.inventory = mailboxPhysicalInv{g.Inventory, p}
					u.mailbox = mailboxPhysicalMail{g.Mailbox, p}
					var e error
					var panicValue any
					func() {
						defer func() { panicValue = recover() }()
						e = u.WithinMailboxAssets(ctx, "1", "1", func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
							return mailboxPhysicalMutation(ctx, c, i, m)
						})
					}()
					if mode == "panic" {
						if panicValue != p.token {
							t.Fatal("originalpanic changed", panicValue)
						}
					} else if panicValue != nil || e == nil {
						t.Fatal("failure acknowledged", e, panicValue)
					}
					if mode == "cancel" && !errors.Is(e, context.Canceled) {
						t.Fatal("lost cancellation", e)
					}
					newC, newHasC, _ := g.Character.Load(nil, "1")
					newI, newHasI, _ := g.Inventory.Load(nil, "1")
					newM, newHasM, _ := g.Mailbox.Load(nil, "1")
					if hasC != newHasC || hasI != newHasI || hasM != newHasM || !reflect.DeepEqual(oldC, newC) || !reflect.DeepEqual(oldI, newI) || !reflect.DeepEqual(oldM, newM) {
						t.Fatal("physical rollback leak", p.trace, newC, newI, newM)
					}
					p.active = false
					done := make(chan error, 1)
					go func() {
						done <- u.WithinMailboxAssets(nil, "1", "1", func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
							return mailboxPhysicalMutation(context.Background(), c, i, m)
						})
					}()
					select {
					case e := <-done:
						if e != nil {
							t.Fatal(e)
						}
					case <-time.After(time.Second):
						t.Fatal("shared mutex leaked")
					}
				})
			}
		}
	}
}

func TestMailboxRestorePanicAttemptsRemainingAndPreservesOriginal(t *testing.T) {
	for _, stage := range []string{"mailbox", "inventory", "character"} {
		for _, mode := range []string{"error", "panic"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				g, u := seedMailboxPhysical(t, true)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				token := 2
				p := &mailboxPhysicalPlan{stage: "mailbox", mode: mode, restorePanic: stage, cancel: cancel, token: &token, counts: map[string]int{}, active: true}
				u.character = mailboxPhysicalChar{g.Character, p}
				u.inventory = mailboxPhysicalInv{g.Inventory, p}
				u.mailbox = mailboxPhysicalMail{g.Mailbox, p}
				var e error
				var v any
				func() {
					defer func() { v = recover() }()
					e = u.WithinMailboxAssets(ctx, "1", "1", func(c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
						return mailboxPhysicalMutation(ctx, c, i, m)
					})
				}()
				if mode == "panic" && v != p.token {
					t.Fatal("lost originalpanic", v)
				}
				if mode == "error" && (e == nil || v != nil) {
					t.Fatal(e, v)
				}
				want := []string{"write:character", "write:inventory", "write:mailbox", "restore:mailbox", "restore:inventory", "restore:character"}
				if !reflect.DeepEqual(p.trace, want) {
					t.Fatal("restore order/coverage", p.trace)
				}
				c, _, _ := g.Character.Load(nil, "1")
				i, _, _ := g.Inventory.Load(nil, "1")
				if c.Stats["old"] != 8 || i.Slots["0:4"].Count != 8 {
					t.Fatal("other restores skipped")
				}
			})
		}
	}
}
