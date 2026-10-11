package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

type accountMailFailAfterSave struct {
	repo.MailboxRepository
	fail bool
}

func (f *accountMailFailAfterSave) Save(ctx context.Context, r repo.MailboxRecord) error {
	if err := f.MailboxRepository.Save(ctx, r); err != nil {
		return err
	}
	if f.fail {
		f.fail = false
		return errors.New("post-mail-write failure")
	}
	return nil
}
func TestAccountMailboxOwnershipScopeAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "foreign_account", "foreign_character", "account_scope", "mail_scope", "callback_error", "post_mail_write", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			g := NewMemoryGroup()
			oldAccount := repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "same"}}
			oldMail := repo.MailboxRecord{CharacterID: "19", Mails: map[string]repo.MailRecord{}}
			if err := g.Account.Save(ctx, oldAccount); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "20", AccountID: "b"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Mailbox.Save(ctx, oldMail); err != nil {
				t.Fatal(err)
			}
			oldAccount, _, _ = g.Account.Load(ctx, "a")
			oldMail, _, _ = g.Mailbox.Load(ctx, "19")
			u := g.MailboxAssets.(*memoryMailboxAssetUnitOfWork)
			if scenario == "post_mail_write" {
				u.mailbox = &accountMailFailAfterSave{MailboxRepository: g.Mailbox, fail: true}
			}
			accountID, charID := "a", "19"
			if scenario == "foreign_account" {
				accountID = "b"
			}
			if scenario == "foreign_character" {
				charID = "20"
			}
			runctx, cancel := context.WithCancel(ctx)
			defer cancel()
			invoke := func() error {
				return u.WithinAccountMailbox(runctx, accountID, charID, func(a repo.AccountRepository, m repo.MailboxRepository) error {
					account, _, err := a.Load(runctx, "a")
					if err != nil {
						return err
					}
					account.Metadata["activity"] = "claimed"
					if scenario == "account_scope" {
						account.AccountID = "b"
					}
					if err := a.Save(runctx, account); err != nil {
						return err
					}
					mail := repo.MailboxRecord{CharacterID: "19", Mails: map[string]repo.MailRecord{"1": {MailID: "1", Attachments: []repo.MailAttachment{{ItemID: 490010051, Count: 1}}}}}
					if scenario == "mail_scope" {
						mail.CharacterID = "20"
					}
					if err := m.Save(runctx, mail); err != nil {
						return err
					}
					if scenario == "callback_error" {
						return errors.New("failure")
					}
					if scenario == "panic" {
						panic("callback panic")
					}
					if scenario == "cancel" {
						cancel()
					}
					return nil
				})
			}
			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = invoke()
			}()
			account, _, _ := g.Account.Load(ctx, "a")
			mail, _, _ := g.Mailbox.Load(ctx, "19")
			if scenario == "success" {
				if err != nil || account.Metadata["activity"] != "claimed" || len(mail.Mails) != 1 {
					t.Fatalf("commit err=%v account=%+v mail=%+v", err, account, mail)
				}
				return
			}
			if !panicked && err == nil {
				t.Fatal("invalid operation accepted")
			}
			if !reflect.DeepEqual(account, oldAccount) || !reflect.DeepEqual(mail, oldMail) {
				t.Fatalf("failure leaked state account=%+v mail=%+v", account, mail)
			}
		})
	}
}
