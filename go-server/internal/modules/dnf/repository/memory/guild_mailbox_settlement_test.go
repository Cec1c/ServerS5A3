package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildSettlementMailboxAtomicRollbackAndOwner(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedCharacterSettlement(t, ctx, repos)
	u := repos.CharacterSettlement.(*memoryCharacterSettlementUnitOfWork)
	owner := r.GuildOwner{AccountID: "dnf:1", CharacterID: "77"}
	scope := r.GuildSettlementReceiptScope(owner, "contribution-test")
	u.settings = &failGuildClearSettings{SettingsRepository: repos.Settings, scope: scope, fail: true}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	award := func(tx r.Group) error {
		if tx.Mailbox == nil {
			t.Fatal("guild settlement does not include mailbox")
		}
		if _, _, err := tx.Mailbox.Load(ctx, "88"); err == nil {
			t.Fatal("another owner's mailbox exposed")
		}
		if err := tx.Mailbox.Save(ctx, r.MailboxRecord{CharacterID: "88"}); err == nil {
			t.Fatal("another owner's mailbox writable")
		}
		ch, _, err := tx.Character.Load(ctx, "77")
		if err != nil {
			return err
		}
		ch.Level++
		if err := tx.Character.Save(ctx, ch); err != nil {
			return err
		}
		if _, err := r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "77", Source: "guild_contribution", CreatedAt: now, Attachments: []r.MailAttachment{{ItemID: 490003262, Count: 3}}}); err != nil {
			return err
		}
		return tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"committed": "1"}})
	}
	before, _, _ := repos.Character.Load(ctx, "77")
	if err := u.WithinGuildSettlement(ctx, owner, "contribution-test", award); err == nil {
		t.Fatal("late failure was not returned")
	}
	after, _, _ := repos.Character.Load(ctx, "77")
	if before.Level != after.Level {
		t.Fatal("character escaped rollback")
	}
	if m, _, _ := repos.Mailbox.Load(ctx, "77"); len(m.Mails) != 0 {
		t.Fatal("mail escaped rollback")
	}
	if _, found, _ := repos.Settings.Load(ctx, scope); found {
		t.Fatal("receipt escaped rollback")
	}
	if err := u.WithinGuildSettlement(ctx, owner, "contribution-test", award); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := repos.Mailbox.Load(ctx, "77"); len(m.Mails) != 1 {
		t.Fatal("retry did not deliver exactly one mail")
	}
	if err := u.WithinCharacterSettlement(ctx, "77", func(tx r.Group) error {
		if tx.Mailbox != nil {
			return errors.New("ordinary settlement gained mailbox access")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
