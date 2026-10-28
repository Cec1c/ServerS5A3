package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"

	"testing"
)

func TestRaidWalletMailboxMemoryCommitFailureAndCancellationAreAtomic(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancel", "abort", "scope", "mail_error", "mail_panic"} {
		t.Run(mode, func(t *testing.T) {
			g := seedEpicBookAssets(t)
			u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a0, _, _ := g.Account.Load(ctx, "a")
			s0, _, _ := g.AccountInventory.Load(ctx, "a")
			c0, _, _ := g.Character.Load(ctx, "19")
			i0, _, _ := g.Inventory.Load(ctx, "19")
			m0, _, _ := g.Mailbox.Load(ctx, "19")
			if mode == "mail_error" || mode == "mail_panic" {
				u.mailbox = &privateWalletFailMail{MailboxRepository: g.Mailbox, mode: mode}
			}
			if mode == "error" || mode == "panic" || mode == "cancel" {
				u.inventory = &epicBookFailSave{InventoryRepository: g.Inventory, mode: mode, cancel: cancel}
			}
			var err error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				err = u.WithinAccountCharacterLedgerMailboxAssets(ctx, "a", "19", func(a repo.AccountRepository, s repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, _ repo.EquipmentRepository, m repo.MailboxRepository) error {
					if mode == "scope" {
						_, _, e := a.Load(ctx, "other")
						return e
					}
					account, _, e := a.Load(ctx, "a")
					if e != nil {
						return e
					}
					account.Metadata["account_happy_token_cera"] = "1030"
					if e = a.Save(ctx, account); e != nil {
						return e
					}
					if e = s.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{}}); e != nil {
						return e
					}
					ch := repo.CloneCharacter(c0)
					ch.Stats = map[string]int64{"gold": 42}
					if e = c.Save(ctx, ch); e != nil {
						return e
					}
					if e = i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 600, Count: 1}}}); e != nil {
						return e
					}
					if e = m.Save(ctx, repo.MailboxRecord{CharacterID: "19", Mails: map[string]repo.MailRecord{"reward": {MailID: "reward", RecipientCharacterID: "19", Attachments: []repo.MailAttachment{{ItemID: 600, Count: 3}}}}}); e != nil {
						return e
					}
					if mode == "abort" {
						return errors.New("礼包奖励失败")
					}
					return nil
				})
			}()
			a, _, _ := g.Account.Load(context.Background(), "a")
			s, _, _ := g.AccountInventory.Load(context.Background(), "a")
			c, _, _ := g.Character.Load(context.Background(), "19")
			i, _, _ := g.Inventory.Load(context.Background(), "19")
			m, _, _ := g.Mailbox.Load(context.Background(), "19")
			if mode == "success" {
				if err != nil || a.Metadata["account_happy_token_cera"] != "1030" || a.Metadata["other"] != "keep" || len(s.Slots) != 0 || c.Stats["gold"] != 42 || i.Slots["0:9"].Count != 1 || len(m.Mails) != 1 {
					t.Fatal("礼包事务提交失败", err)
				}
			} else if (!panicked && err == nil) || !reflect.DeepEqual(a, a0) || !reflect.DeepEqual(s, s0) || !reflect.DeepEqual(c, c0) || !reflect.DeepEqual(i, i0) || !reflect.DeepEqual(m, m0) {
				t.Fatal("礼包失败后资产未完整恢复", err, panicked)
			}
		})
	}
}

type privateWalletFailMail struct {
	repo.MailboxRepository
	mode string
	used bool
}

func (f *privateWalletFailMail) Save(ctx context.Context, m repo.MailboxRecord) error {
	if err := f.MailboxRepository.Save(ctx, m); err != nil {
		return err
	}
	if !f.used {
		f.used = true
		if f.mode == "mail_panic" {
			panic("mail failed after physical write")
		}
		return errors.New("mail failed after physical write")
	}
	return nil
}
func (f *privateWalletFailMail) Delete(ctx context.Context, id string) error {
	return f.MailboxRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, id)
}
