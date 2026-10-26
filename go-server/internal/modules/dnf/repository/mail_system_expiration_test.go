package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestSystemMailExplicitExpirationAndExistingUnlimited(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)
	for _, expiry := range []time.Time{{}, now.Add(15 * 24 * time.Hour)} {
		id, err := r.AppendSystemMail(ctx, repos.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "77", Source: "guild_contribution", CreatedAt: now, ExpireAt: expiry, Attachments: []r.MailAttachment{{ItemID: 490003262, Count: 3}}})
		if err != nil {
			t.Fatal(err)
		}
		box, _, _ := repos.Mailbox.Load(ctx, "77")
		m := box.Mails[id]
		if !m.ExpireAt.Equal(expiry) || (m.Metadata["unlimited"] == "true") != expiry.IsZero() {
			t.Fatal(m)
		}
	}
	for _, expiry := range []time.Time{now, now.Add(-time.Second)} {
		_, err := r.AppendSystemMail(ctx, repos.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "77", Source: "guild_contribution", CreatedAt: now, ExpireAt: expiry, Attachments: []r.MailAttachment{{ItemID: 490003262, Count: 3}}})
		if !errors.Is(err, r.ErrSystemMailInvalid) {
			t.Fatal(err)
		}
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if len(box.Mails) != 2 {
		t.Fatal("invalid mail was persisted")
	}
}
