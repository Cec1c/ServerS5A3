package raid

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func entryRefundFixture(t *testing.T, mode byte) (repo.Group, EntryRefundCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	at := time.Unix(1800000000, 0).UTC()
	d := EntryDebit{InstanceID: "00112233445566778899aabbccddeeff", Mode: mode, ItemID: 100, Count: 2, RefundItemID: 100, RefundCount: 2, PaidAt: at}
	c := repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{"existing": 7}}
	if err := RecordEntryDebit(&c, d); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "31"}); err != nil {
		t.Fatal(err)
	}
	return g, EntryRefundCommand{Debit: d, CharacterID: "31", AccountID: "account", FailedAt: at.Add(time.Hour), Attachments: []repo.MailAttachment{{ItemID: 100, Count: 2, Bind: true, RawEntry: []byte{1, 2}, Extra: map[string]string{"keep": "metadata"}}}}
}

func TestRaidEntryRefundAllModesConcurrentOnceAndQueryable(t *testing.T) {
	for mode := byte(1); mode <= 3; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			g, cmd := entryRefundFixture(t, mode)
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					status, err := RefundEntryByMail(context.Background(), g, cmd)
					if err != nil || !status.Refunded || status.MailID != "1" {
						t.Errorf("refund %+v %v", status, err)
					}
				}()
			}
			wg.Wait()
			mail, _, err := g.Mailbox.Load(context.Background(), cmd.CharacterID)
			if err != nil || len(mail.Mails) != 1 {
				t.Fatal("duplicated mail", err, mail)
			}
			if !reflect.DeepEqual(mail.Mails["1"].Attachments, cmd.Attachments) {
				t.Fatal("changed ticket metadata")
			}
			c, _, _ := g.Character.Load(context.Background(), cmd.CharacterID)
			status, err := QueryEntryRefund(c, cmd.Debit)
			if err != nil || !status.Refunded || c.Stats["existing"] != 7 {
				t.Fatal(status, err)
			}
			// Claimed/deleted mail is still a committed refund, never a new grant.
			m := mail.Mails["1"]
			m.Claimed = true
			m.Deleted = true
			mail.Mails["1"] = m
			_ = g.Mailbox.Save(context.Background(), mail)
			if _, err = RefundEntryByMail(context.Background(), g, cmd); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Mailbox.Load(context.Background(), cmd.CharacterID)
			if len(after.Mails) != 1 {
				t.Fatal("deleted mail caused regrant")
			}
			for key := range c.Stats {
				if len(key) >= 15 && key[:15] == "raid_reward_v1_" {
					t.Fatal("refund created clear reward")
				}
			}
		})
	}
}

type entryRefundFailAssets struct {
	repo.AccountCharacterAssetUnitOfWork
	mail        repo.AccountCharacterMailboxAssetUnitOfWork
	afterCommit bool
}

func (u entryRefundFailAssets) WithinAccountCharacterMailboxAssets(ctx context.Context, a, c string, fn func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error) error {
	err := u.mail.WithinAccountCharacterMailboxAssets(ctx, a, c, func(ai repo.AccountInventoryRepository, chars repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository, m repo.MailboxRepository) error {
		if err := fn(ai, chars, i, e, m); err != nil {
			return err
		}
		if !u.afterCommit {
			return errors.New("injected before commit")
		}
		return nil
	})
	if err == nil && u.afterCommit {
		return errors.New("lost commit acknowledgement")
	}
	return err
}

func TestRaidEntryRefundRollbackAndUnknownCommitRetry(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			g, cmd := entryRefundFixture(t, 1)
			wrapped := g
			wrapped.AccountAssets = entryRefundFailAssets{g.AccountAssets, g.AccountAssets.(repo.AccountCharacterMailboxAssetUnitOfWork), unknown}
			if _, err := RefundEntryByMail(nil, wrapped, cmd); err == nil {
				t.Fatal("injected failure ignored")
			}
			mail, found, _ := g.Mailbox.Load(context.Background(), cmd.CharacterID)
			c, _, _ := g.Character.Load(context.Background(), cmd.CharacterID)
			status, err := QueryEntryRefund(c, cmd.Debit)
			if err != nil || status.Refunded != unknown || (!unknown && found && len(mail.Mails) != 0) {
				t.Fatal("partial transaction", status, mail, err)
			}
			if status, err = RefundEntryByMail(nil, g, cmd); err != nil || !status.Refunded || status.MailID != "1" {
				t.Fatal(status, err)
			}
			mail, _, _ = g.Mailbox.Load(context.Background(), cmd.CharacterID)
			if len(mail.Mails) != 1 {
				t.Fatal("retry duplicate")
			}
		})
	}
}

func TestRaidEntryRefundRejectsUnpaidChangedPolicyAndForeignOwner(t *testing.T) {
	for _, kind := range []string{"unpaid", "item", "count", "mode", "instance", "account", "character", "early", "partial_receipt"} {
		t.Run(kind, func(t *testing.T) {
			g, cmd := entryRefundFixture(t, 1)
			switch kind {
			case "unpaid":
				c, _, _ := g.Character.Load(context.Background(), "31")
				c.Stats = map[string]int64{}
				_ = g.Character.Save(context.Background(), c)
			case "item":
				cmd.Attachments[0].ItemID++
			case "count":
				cmd.Attachments[0].Count++
			case "mode":
				cmd.Debit.Mode = 2
			case "instance":
				cmd.Debit.InstanceID = "11112233445566778899aabbccddeeff"
			case "account":
				cmd.AccountID = "foreign"
			case "character":
				cmd.CharacterID = "32"
			case "early":
				cmd.FailedAt = cmd.Debit.PaidAt.Add(-time.Second)
			case "partial_receipt":
				c, _, _ := g.Character.Load(context.Background(), "31")
				key, _ := entryDebitKey(cmd.Debit)
				c.Stats[key+"_mail"] = 1
				_ = g.Character.Save(context.Background(), c)
			}
			if _, err := RefundEntryByMail(nil, g, cmd); err == nil {
				t.Fatal("invalid refund accepted")
			}
			mail, _, _ := g.Mailbox.Load(context.Background(), "31")
			if len(mail.Mails) != 0 {
				t.Fatal("invalid refund sent mail")
			}
		})
	}
}
