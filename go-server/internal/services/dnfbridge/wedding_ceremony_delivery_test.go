package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"testing"
	"time"
)

type weddingDeliveryFaultUnit struct {
	r.CharacterTradeUnitOfWork
	unit r.WeddingLifecycleUnitOfWork
}

func (u weddingDeliveryFaultUnit) WithinWeddingLifecycle(ctx context.Context, ids []string, scope string, apply func(r.Group) error) error {
	return u.unit.WithinWeddingLifecycle(ctx, ids, scope, func(tx r.Group) error { tx.Mailbox = weddingDeliveryFaultMail{tx.Mailbox}; return apply(tx) })
}

type weddingDeliveryFaultMail struct{ r.MailboxRepository }

func (m weddingDeliveryFaultMail) Save(ctx context.Context, record r.MailboxRecord) error {
	if e := m.MailboxRepository.Save(ctx, record); e != nil {
		return e
	}
	if record.CharacterID == "30" {
		return errors.New("second gift mailbox fault")
	}
	return nil
}

func TestWeddingCeremonyFullBagsUseTransactionalMail(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			ctx := context.Background()
			f.now = time.Unix(f.at+60, 0)
			for _, id := range []string{"29", "30"} {
				c, _, _ := f.repos.Character.Load(ctx, id)
				c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_ceremony_ready_at"] = int64(wedding.InCeremony), f.at
				if e := r.SaveCharacterFields(ctx, f.repos.Character, c, r.CharacterFieldWedding); e != nil {
					t.Fatal(e)
				}
				bag, _, _ := f.repos.Inventory.Load(ctx, id)
				if bag.Slots == nil {
					bag.Slots = map[string]r.ItemStack{}
				}
				for slot := 3; slot <= 288; slot++ {
					bag.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 10002765, Count: 1}
				}
				if e := f.repos.Inventory.Save(ctx, bag); e != nil {
					t.Fatal(e)
				}
			}
			owner, e := f.s.currentWeddingOwner()
			if e != nil {
				t.Fatal(e)
			}
			if fail {
				owner.Repositories.CharacterTrade = weddingDeliveryFaultUnit{owner.Repositories.CharacterTrade, owner.Repositories.CharacterTrade.(r.WeddingLifecycleUnitOfWork)}
			}
			deliver, e := f.s.currentWeddingCeremonyDelivery(owner, 0, f.now)
			if e != nil {
				t.Fatal(e)
			}
			req := wedding.PairRequest{Actor: wedding.Actor{AccountID: "wedding:29", CharacterID: "29"}, Partner: wedding.Actor{AccountID: "wedding:30", CharacterID: "30"}, Now: f.now}
			_, e = owner.FinishAdmittedCeremonyDelivered(ctx, req, f.at, deliver)
			if (e != nil) != fail {
				t.Fatalf("mail result fail=%t err=%v", fail, e)
			}
			for _, id := range []string{"29", "30"} {
				c, _, _ := f.repos.Character.Load(ctx, id)
				state, _ := wedding.Read(c)
				box, found, _ := f.repos.Mailbox.Load(ctx, id)
				bag, _, _ := f.repos.Inventory.Load(ctx, id)
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 0)
				if fail {
					if state.Phase != wedding.InCeremony || found {
						t.Fatal("partial marriage or mailbox after second failure")
					}
				} else {
					if state.Phase != wedding.Married || !found || len(box.Mails) != 1 {
						t.Fatal("full bag blocked marriage/gift")
					}
					for _, mail := range box.Mails {
						if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 10002629 || mail.Attachments[0].Count != 1 || mail.Attachments[0].Extra["pvf_path"] == "" {
							t.Fatal("mail lost actual PVF grant identity")
						}
					}
				}
			}
			if !fail {
				if _, e = owner.FinishAdmittedCeremonyDelivered(ctx, req, f.at, deliver); !errors.Is(e, wedding.ErrState) {
					t.Fatal("duplicate end accepted", e)
				}
				for _, id := range []string{"29", "30"} {
					box, _, _ := f.repos.Mailbox.Load(ctx, id)
					if len(box.Mails) != 1 {
						t.Fatal("duplicate gift mail")
					}
				}
			}
		})
	}
}
